// Command qmprobe runs scripted command sequences against a unit and logs every
// playback status and queue push, to measure how the unit behaves for the queue
// manager study (docs/QUEUE-MANAGER-STUDY.md).
//
//	go run ./internal/cmd/qmprobe -addr 192.168.1.50 watch:10s
//	go run ./internal/cmd/qmprobe add:19535 wait:5s skip wait:10s queue
//
// Steps: add:ID, first:ID, del:ID, playex:ID, next, skip, pause, stop,
// queue, wait:DUR, until-song:DUR (wait for the current song id to change),
// as:NAME:ID (queue ID through a short session named NAME), watch:DUR.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rrgmc/ck-karaoke/catalog"
	"github.com/rrgmc/ck-karaoke/client"
	"github.com/rrgmc/ck-karaoke/protocol"
	_ "modernc.org/sqlite"
)

var start = time.Now()

func logf(format string, args ...any) {
	fmt.Printf("%7.2fs  %s\n", time.Since(start).Seconds(), fmt.Sprintf(format, args...))
}

type watcher struct {
	mu      sync.Mutex
	last    string
	songID  string
	changed chan struct{}
}

// status logs a playback status when anything but the lyrics changes. The
// position is logged on every frame so stalls are visible.
func (w *watcher) status(f []string) {
	get := func(i int) string {
		if i < len(f) {
			return f[i]
		}
		return "?"
	}
	key := fmt.Sprintf("id=%q cur=%q next=%q u3=%q u5=%q u7=%q u11=%q u12=%q key=%s tempo=%s",
		get(8), get(4), get(6), get(3), get(5), get(7), get(11), get(12), get(17), get(18))
	w.mu.Lock()
	defer w.mu.Unlock()
	if key != w.last {
		logf("STATUS pos=%s %s", get(2), key)
		w.last = key
	} else {
		logf("status pos=%s", get(2))
	}
	if get(8) != w.songID {
		w.songID = get(8)
		select {
		case w.changed <- struct{}{}:
		default:
		}
	}
}

func main() {
	addr := flag.String("addr", "", "unit address; found by broadcast if empty")
	sweep := flag.Bool("sweep", false, "find the unit by unicast sweep instead of broadcast")
	flag.Parse()
	ctx := context.Background()

	resolve := client.Resolve
	if *sweep {
		resolve = client.ResolveSweep
	}
	address, devices, err := resolve(ctx, *addr, 10*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, d := range devices {
		logf("found %s", d)
	}

	w := &watcher{changed: make(chan struct{}, 1)}
	id := protocol.NewIdentity()
	id.Name = "qmprobe"
	c, err := client.Dial(ctx, address, client.Options{
		Identity: &id,
		Logger:   slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		OnFrame: func(_ []byte, frame protocol.Frame, msg protocol.Message, _ error) {
			sf, ok := frame.(*protocol.StatusFrame)
			if !ok || sf.Fields == nil {
				return
			}
			switch sf.Fields.Command {
			case 0x00:
				w.status(sf.Fields.Fields)
			case 0x03:
				logf("QUEUE frame %v", sf.Fields.Fields)
			}
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer c.Close()
	logf("session open")

	for _, step := range flag.Args() {
		logf(">> %s", step)
		if err := run(ctx, c, address, w, step); err != nil {
			logf("!! %s: %v", step, err)
		}
	}
}

func run(ctx context.Context, c *client.Client, address string, w *watcher, step string) error {
	op, arg, _ := strings.Cut(step, ":")
	dur := func() time.Duration {
		d, err := time.ParseDuration(arg)
		if err != nil {
			return 5 * time.Second
		}
		return d
	}
	switch op {
	case "add":
		return c.Reserve(ctx, arg)
	case "first":
		return c.ReserveFirst(ctx, arg)
	case "del":
		return c.RemoveFromQueue(ctx, arg)
	case "playex":
		return c.PlayNow(ctx, arg)
	case "next":
		return c.NextSong(ctx)
	case "skip":
		return c.Skip(ctx)
	case "pause":
		return c.Pause(ctx)
	case "stop":
		return c.Stop(ctx)
	case "queue":
		q, err := c.Queue(ctx, 0, 10)
		if err != nil {
			return err
		}
		for _, e := range q.Entries {
			logf("   entry %s by %q fields=%v", e.SongID, e.QueuedBy(), e.Fields)
		}
		logf("   total=%d", q.Total)
	case "wait", "watch":
		time.Sleep(dur())
	case "until-song":
		select {
		case <-w.changed:
		default:
		}
		select {
		case <-w.changed:
			logf("   song changed")
		case <-time.After(dur()):
			logf("   no song change")
		}
	case "db":
		if _, err := os.Stat(arg); err != nil {
			if err := catalog.Fetch(ctx, c, arg, nil); err != nil {
				return err
			}
		}
		db, err := sql.Open("sqlite", arg)
		if err != nil {
			return err
		}
		defer db.Close()
		var n, lo, hi int
		if err := db.QueryRow(`select count(*), min(SongIndex), max(SongIndex) from TableSong`).Scan(&n, &lo, &hi); err != nil {
			return err
		}
		logf("   songs=%d min=%d max=%d", n, lo, hi)
		var dup int
		db.QueryRow(`select count(*) from (select SongIndex from TableSong group by SongIndex having count(*) > 1)`).Scan(&dup)
		logf("   duplicate SongIndex values=%d", dup)
		rows, err := db.Query(`select SongIndex, SongName, SingerName from TableSong order by random() limit 8`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int
			var title, artist string
			rows.Scan(&id, &title, &artist)
			logf("   %d  %s / %s", id, title, artist)
		}
	case "as":
		name, song, _ := strings.Cut(arg, ":")
		t0 := time.Now()
		id := protocol.NewIdentity()
		id.Name = name
		s, err := client.Dial(ctx, address, client.Options{Identity: &id})
		if err != nil {
			return err
		}
		err = s.Reserve(ctx, song)
		s.Close()
		logf("   short session took %s", time.Since(t0).Round(time.Millisecond))
		return err
	default:
		return fmt.Errorf("unknown step")
	}
	return nil
}
