// Command qmfeed runs the queue manager's feeder against a real unit: it gives
// singers songs in a fair evening and logs what the unit does as the feeder
// plays them, skipping each song after a while.
//
//	go run ./internal/cmd/qmfeed -model MK8513-BR-B -play 20s Ana:1229,3656 Beto:6541
//
// Each argument is a singer and the songs they ask for, in order. With
// -foreign, a separate session queues one song while the first plays, to show
// the feeder adopting it. With -controls, the first song is paused, resumed,
// restarted and stopped, and Play starts the next one.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/rrgmc/ck-karaoke/client"
	"github.com/rrgmc/ck-karaoke/protocol"
	"github.com/rrgmc/ck-karaoke/queue/fair"
	"github.com/rrgmc/ck-karaoke/queue/feeder"
)

var start = time.Now()

func logf(format string, args ...any) {
	fmt.Printf("%7.2fs  %s\n", time.Since(start).Seconds(), fmt.Sprintf(format, args...))
}

func main() {
	addr := flag.String("addr", "", "unit address; found by broadcast if empty")
	model := flag.String("model", "", "use only a unit of this model")
	dbID := flag.String("db", "", "use only a unit with this database id")
	order := flag.String("order", "turns", "arrival or turns")
	play := flag.Duration("play", 20*time.Second, "how long each song plays before the skip")
	foreign := flag.String("foreign", "", "a song id another session queues while the first song plays")
	verbose := flag.Bool("v", false, "log the feeder's debug messages")
	controls := flag.Bool("controls", false, "pause, resume, restart, stop and play during the first song")
	flag.Parse()
	if err := run(*addr, *model, *dbID, fair.Order(*order), *play, *foreign, *verbose, *controls, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(addr, model, dbID string, order fair.Order, play time.Duration, foreign string, verbose, controls bool,
	singers []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	address, err := find(ctx, addr, model, dbID)
	if err != nil {
		return err
	}

	room := feeder.NewRoom(fair.NewEvening(order), nil)
	total := 0
	for _, arg := range singers {
		name, songs, ok := strings.Cut(arg, ":")
		if !ok {
			return fmt.Errorf("%q: want NAME:ID,ID", arg)
		}
		var err error
		room.Do(func(e *fair.Evening, now time.Time) {
			var issued fair.Issued
			sn, _ := fair.NameOf(name)
			issued, err = e.Register("qmfeed-"+name, &sn, "token-"+name, fair.SeatRules{})
			if err != nil {
				return
			}
			for _, s := range strings.Split(songs, ",") {
				n, perr := strconv.ParseUint(s, 10, 32)
				if perr != nil {
					err = perr
					return
				}
				if _, err = e.Request(issued.Seat.ID, fair.ByLevel,
					fair.RequestedSong{Number: uint32(n), Title: s}, fair.DefaultRequestRules, now); err != nil {
					return
				}
				total++
			}
		})
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if foreign != "" {
		total++
	}
	logf("evening: %v", numbers(room))

	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	id := protocol.NewIdentity()
	id.Name = "QueueManager"
	started := make(chan feeder.Song, 16)
	f := feeder.New(feeder.Options{
		Dialer:   feeder.ClientDialer{Addr: address, Logger: logger},
		Room:     room,
		Identity: &id,
		Logger:   logger,
		OnEvent: func(e feeder.Event) {
			switch e := e.(type) {
			case feeder.SongStarted:
				singer := "(no singer)"
				if e.Song.Entry != nil && e.Song.Entry.Singer != "" {
					singer = e.Song.Entry.Singer
				}
				logf("STARTED %d %q for %s; evening now %v", e.Song.Number, e.Song.Title, singer, numbers(room))
				started <- e.Song
			case feeder.SongEnded:
				logf("ENDED %d natural=%v length=%s", e.Song.Number, e.Natural, e.Length.Round(time.Second))
			case feeder.StateChanged:
				logf("state %s pos=%s held=%v", e.State.Transport, e.State.Position.Round(time.Second), e.State.Held)
			}
		},
	})
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.Run(runCtx)
	}()
	defer func() {
		stop()
		<-done
		logf("session closed")
	}()

	for played := 0; played < total; played++ {
		var song feeder.Song
		select {
		case song = <-started:
		case <-time.After(30 * time.Second):
			return errors.New("no song started within 30 s")
		case <-ctx.Done():
			return nil
		}
		if played == 0 && foreign != "" {
			if err := queueForeign(ctx, address, foreign); err != nil {
				logf("foreign queue failed: %v", err)
			}
		}
		if played == 0 && controls {
			// Ends the first song with Stop, and Play starts the next one.
			if err := exercise(ctx, f); err != nil {
				return err
			}
			continue
		}
		select {
		case <-time.After(play):
		case <-ctx.Done():
			return nil
		}
		if played == total-1 {
			logf(">> stop after %d", song.Number)
			return f.Stop(ctx)
		}
		logf(">> skip %d", song.Number)
		if err := f.Skip(ctx); err != nil {
			return err
		}
	}
	return nil
}

// exercise pauses, resumes, restarts, stops and plays, logging the state the
// feeder infers after each step.
func exercise(ctx context.Context, f *feeder.Feeder) error {
	steps := []struct {
		name string
		do   func(context.Context) error
	}{
		{"pause", f.Pause}, {"play", f.Play}, {"restart", f.Restart}, {"stop", f.Stop}, {"play", f.Play},
	}
	for _, s := range steps {
		select {
		case <-time.After(7 * time.Second):
		case <-ctx.Done():
			return nil
		}
		st := f.State()
		logf(">> %s (before: %s pos=%s)", s.name, st.Transport, st.Position.Round(time.Second))
		if err := s.do(ctx); err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
	}
	return nil
}

// find resolves the unit and checks it is the one asked for.
func find(ctx context.Context, addr, model, dbID string) (string, error) {
	devices, err := client.Discover(ctx, "", 5*time.Second)
	if addr != "" {
		d, perr := client.Ping(ctx, addr)
		if perr != nil {
			return "", perr
		}
		devices, err = []client.Device{*d}, nil
	}
	if err != nil {
		return "", err
	}
	for _, d := range devices {
		logf("found %s", d)
		if (model == "" || d.Info.Model == model) && (dbID == "" || d.Info.DatabaseID == dbID) {
			return d.Addr, nil
		}
	}
	return "", errors.New("no unit matches -model and -db")
}

func queueForeign(ctx context.Context, address, song string) error {
	id := protocol.NewIdentity()
	id.Name = "OtherPhone"
	c, err := client.Dial(ctx, address, client.Options{Identity: &id})
	if err != nil {
		return err
	}
	defer c.Close()
	logf(">> another session queues %s", song)
	return c.Reserve(ctx, song)
}

func numbers(room *feeder.Room) []uint32 {
	var out []uint32
	for _, e := range room.Entries() {
		out = append(out, e.Number)
	}
	return out
}
