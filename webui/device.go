// Package webui serves a mobile-friendly UI for a karaoke device.
package webui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/rrgmc/ck-karaoke/client"
	"github.com/rrgmc/ck-karaoke/protocol"
)

// ErrOffline reports that the device is not currently reachable.
var ErrOffline = errors.New("device is offline")

// Device keeps one long-lived session to the karaoke unit and reconnects when it
// drops.
//
// A single shared session, rather than dialling per request, is what makes live
// status possible at all: the device pushes state continuously and only to a
// connected client.
type Device struct {
	addr     string
	identity protocol.Identity
	logger   *slog.Logger
	onStatus func(*protocol.Status)
	// onConnection reports the session coming up or going down, so the UI can
	// say so rather than silently failing every button.
	onConnection func(online bool, reason error)
	// onQueue forwards reserved-list updates, which the device pushes as well as
	// sending on request.
	onQueue func(*protocol.Queue)

	mu     sync.RWMutex
	client *client.Client
	err    error
	since  time.Time
	// lastDialErr suppresses repeat notifications while reconnecting.
	lastDialErr string
	// lastStatus is when the device last pushed state. It broadcasts roughly
	// every 1.5s unprompted, so silence is the most reliable sign the session
	// has died — more reliable than RakNet's own view, which stays "connected"
	// while the device quietly stops serving us.
	lastStatus time.Time
	// suspended stops Run from dialling. suspendC is closed to tell an open
	// session to end; resumeC is closed to release Run. Each is replaced by its
	// opposite transition, so a channel is only ever closed once.
	suspended bool
	suspendC  chan struct{}
	resumeC   chan struct{}
}

// NewDevice returns a Device for addr, presenting the given client identity.
// Call Run to keep it connected.
func NewDevice(addr string, identity protocol.Identity, logger *slog.Logger, onStatus func(*protocol.Status)) *Device {
	return &Device{
		addr:     addr,
		identity: identity,
		logger:   logger,
		onStatus: onStatus,
		err:      ErrOffline,
		suspendC: make(chan struct{}),
	}
}

// Suspend closes the session and stops reconnecting until Resume is called.
//
// This is for iOS, which has no equivalent of the foreground service the Android
// app uses to stay connected: the app is suspended shortly after it stops being
// looked at, and a session left open would hold one of the device's five client
// slots until the unit timed it out by itself. Nothing on a desktop needs this.
func (d *Device) Suspend() {
	d.mu.Lock()
	if d.suspended {
		d.mu.Unlock()
		return
	}
	d.suspended = true
	d.resumeC = make(chan struct{})
	close(d.suspendC) // watch is waiting on this
	d.mu.Unlock()
	d.logger.Info("suspending the device session")
}

// Resume starts reconnecting again after Suspend.
//
// Presenting the same identity again is safe because Suspend closed the session
// rather than abandoning it — the same reason the staleness path can reconnect.
func (d *Device) Resume() {
	d.mu.Lock()
	if !d.suspended {
		d.mu.Unlock()
		return
	}
	d.suspended = false
	d.suspendC = make(chan struct{})
	close(d.resumeC) // Run is waiting on this
	d.mu.Unlock()
	d.logger.Info("resuming the device session")
}

// Suspended reports whether the session is deliberately closed.
func (d *Device) Suspended() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.suspended
}

// waitResume blocks while suspended, reporting false if the context ended first.
func (d *Device) waitResume(ctx context.Context) bool {
	d.mu.RLock()
	suspended, resumeC := d.suspended, d.resumeC
	d.mu.RUnlock()
	if !suspended {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-resumeC:
		return true
	}
}

// OnQueue registers a callback for reserved-list updates.
func (d *Device) OnQueue(fn func(*protocol.Queue)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onQueue = fn
}

// LastQueue returns the most recent reserved list, or nil.
func (d *Device) LastQueue() *protocol.Queue {
	c, err := d.get()
	if err != nil {
		return nil
	}
	return c.LastQueue()
}

// OnConnection registers a callback for the session coming up or going down.
// It fires only on a change, not on every reconnect attempt, so a device that is
// off does not produce a notification every few seconds.
func (d *Device) OnConnection(fn func(online bool, reason error)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onConnection = fn
}

// Run maintains the session until ctx is cancelled. It returns only when the
// context ends.
func (d *Device) Run(ctx context.Context) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for ctx.Err() == nil {
		// Suspended sessions stay closed. Checked before dialling rather than
		// after, so being suspended costs nothing at all.
		if !d.waitResume(ctx) {
			return
		}
		d.mu.RLock()
		suspendC := d.suspendC
		d.mu.RUnlock()

		// Bounded, because Run's context is the process lifetime and carries no
		// deadline of its own — and go-raknet only sets a socket deadline when
		// the context has one. Against an address that answers nothing at all,
		// rather than refusing, the MTU discovery read blocks forever: Run stops
		// in Dial and never comes back, so the session is dead while the
		// reconnect loop still looks healthy. A stale DHCP lease is enough to
		// reach that, which is the ordinary way this device moves.
		dialCtx, cancelDial := context.WithTimeout(ctx, DialTimeout)
		c, err := client.Dial(dialCtx, d.addr, client.Options{
			Identity: &d.identity,
			Logger:   d.logger,
		})
		cancelDial()
		if err != nil {
			d.reportDialFailure(err)
			d.setDown(err)
			d.logger.Warn("device unreachable, retrying", "addr", d.addr, "error", err, "in", backoff)
			// Suspending during the backoff has to take effect now, not up to
			// maxBackoff later: on iOS this runs while the app is being put away.
			if !sleep(ctx, backoff, suspendC) {
				return
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		backoff = time.Second
		c.OnStatus(d.trackStatus)
		c.OnQueue(d.forwardQueue)
		d.setUp(c)
		d.logger.Info("device connected", "addr", c.RemoteAddr(),
			"serverGUID", fmt.Sprintf("%#016x", c.ServerGUID()))

		d.watch(ctx, c, suspendC)
	}
}

// StaleAfter is how long the device may go silent before the session is treated
// as dead. It pushes state about every 1.5s, so this is a wide margin.
const StaleAfter = 15 * time.Second

// DialTimeout bounds one connection attempt. RakNet's opening exchange sends
// twelve packets over about six seconds — four tries at each of three MTU sizes
// — before it even starts asking for the connection, so this has to be well
// clear of that; a timeout only means the ladder in Run backs off and tries
// again, which is what should happen to an address nothing is answering on.
const DialTimeout = 30 * time.Second

// trackStatus records that the device is still talking to us, then passes the
// update on.
func (d *Device) trackStatus(st *protocol.Status) {
	d.mu.Lock()
	d.lastStatus = time.Now()
	fn := d.onStatus
	d.mu.Unlock()
	if fn != nil {
		fn(st)
	}
}

func (d *Device) forwardQueue(q *protocol.Queue) {
	d.mu.RLock()
	fn := d.onQueue
	d.mu.RUnlock()
	if fn != nil {
		fn(q)
	}
}

// watch blocks until the session ends, either because raknet noticed or because
// the device went quiet.
//
// The second case is the one that matters. These units serve a single client, so
// a remote that connects elsewhere takes the slot and ours is silently dropped —
// RakNet keeps thinking it is connected and every command then times out
// forever. Watching for silence is what turns that into a reconnect.
func (d *Device) watch(ctx context.Context, c *client.Client, suspendC <-chan struct{}) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.Close()
			return

		case <-suspendC:
			// Closed rather than abandoned: an abandoned session holds one of the
			// device's client slots until the unit times it out.
			d.setDown(ErrOffline)
			c.Close()
			return

		case <-c.Context().Done():
			d.setDown(ErrOffline)
			d.logger.Warn("device session ended, reconnecting")
			c.Close()
			return

		case <-ticker.C:
			d.mu.RLock()
			last := d.lastStatus
			d.mu.RUnlock()
			if time.Since(last) > StaleAfter {
				d.logger.Warn("device stopped sending status, dropping the session",
					"silent_for", time.Since(last).Round(time.Second))
				d.setDown(ErrOffline)
				c.Close()
				return
			}
		}
	}
}

func (d *Device) setUp(c *client.Client) {
	d.mu.Lock()
	was := d.client != nil
	d.client, d.err, d.since, d.lastDialErr = c, nil, time.Now(), ""
	d.lastStatus = time.Now()
	fn := d.onConnection
	d.mu.Unlock()
	if fn != nil && !was {
		fn(true, nil)
	}
}

func (d *Device) setDown(err error) {
	d.mu.Lock()
	was := d.client != nil
	d.client, d.err = nil, err
	fn := d.onConnection
	d.mu.Unlock()
	if fn != nil && was {
		fn(false, err)
	}
}

// reportDialFailure announces a failed connection attempt once per distinct
// reason, so a device that is off does not produce a notification every few
// seconds while the backoff grinds on.
func (d *Device) reportDialFailure(err error) {
	d.mu.Lock()
	same := d.lastDialErr != "" && d.lastDialErr == err.Error()
	d.lastDialErr = err.Error()
	fn := d.onConnection
	d.mu.Unlock()
	if fn != nil && !same {
		fn(false, err)
	}
}

// LastError returns why the device is unavailable, or nil when it is connected.
func (d *Device) LastError() error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.err
}

// Online reports whether commands can be sent right now.
func (d *Device) Online() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.client != nil
}

// Addr returns the configured device address.
func (d *Device) Addr() string { return d.addr }

// Identity returns the client identity this device session presents.
func (d *Device) Identity() protocol.Identity { return d.identity }

// Status returns the last state the device pushed, or nil.
func (d *Device) Status() *protocol.Status {
	c, err := d.get()
	if err != nil {
		return nil
	}
	return c.Status()
}

func (d *Device) get() (*client.Client, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.client == nil {
		// Only wrapped when there is something to add. The stored reason is often
		// ErrOffline itself, and wrapping that in itself read "device is offline:
		// device is offline" in the UI.
		if d.err != nil && !errors.Is(d.err, ErrOffline) {
			return nil, fmt.Errorf("%w: %v", ErrOffline, d.err)
		}
		return nil, ErrOffline
	}
	return d.client, nil
}

// Do runs fn against the live session, or fails fast if the device is offline.
// Every handler goes through here so there is one place that knows about the
// disconnected case.
func (d *Device) Do(fn func(*client.Client) error) error {
	c, err := d.get()
	if err != nil {
		return err
	}
	return fn(c)
}

// sleep waits, reporting false only if the context ended — a suspend cuts the
// wait short but the loop carries on, to block on Resume instead.
func sleep(ctx context.Context, d time.Duration, interrupt <-chan struct{}) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-interrupt:
		return true
	case <-t.C:
		return true
	}
}
