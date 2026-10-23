package webui

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/rrgmc/ck-karaoke/protocol"
	"github.com/stretchr/testify/assert"
)

func testDevice(t *testing.T) *Device {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewDevice("127.0.0.1:1", protocol.NewIdentity(), logger, nil)
}

func TestSuspendResumeToggles(t *testing.T) {
	d := testDevice(t)
	assert.False(t, d.Suspended())

	d.Suspend()
	assert.True(t, d.Suspended())

	d.Resume()
	assert.False(t, d.Suspended())
}

// Both must be safe to call repeatedly: the iOS lifecycle callbacks fire more
// than once, and closing a channel twice panics.
func TestSuspendResumeAreIdempotent(t *testing.T) {
	d := testDevice(t)

	assert.NotPanics(t, func() {
		d.Suspend()
		d.Suspend()
		d.Suspend()
	})
	assert.True(t, d.Suspended())

	assert.NotPanics(t, func() {
		d.Resume()
		d.Resume()
		d.Resume()
	})
	assert.False(t, d.Suspended())

	// And the cycle must survive being repeated, which is the case that catches a
	// channel that was not replaced on the way back.
	assert.NotPanics(t, func() {
		for range 3 {
			d.Suspend()
			d.Resume()
		}
	})
}

func TestResumeWithoutSuspendDoesNothing(t *testing.T) {
	d := testDevice(t)
	assert.NotPanics(t, d.Resume)
	assert.False(t, d.Suspended())
}

func TestWaitResumeReturnsImmediatelyWhenRunning(t *testing.T) {
	d := testDevice(t)
	done := make(chan bool, 1)
	go func() { done <- d.waitResume(context.Background()) }()

	select {
	case ok := <-done:
		assert.True(t, ok)
	case <-time.After(time.Second):
		t.Fatal("waitResume blocked while the device was not suspended")
	}
}

func TestWaitResumeBlocksUntilResume(t *testing.T) {
	d := testDevice(t)
	d.Suspend()

	done := make(chan bool, 1)
	go func() { done <- d.waitResume(context.Background()) }()

	select {
	case <-done:
		t.Fatal("waitResume returned while the device was suspended")
	case <-time.After(50 * time.Millisecond):
	}

	d.Resume()
	select {
	case ok := <-done:
		assert.True(t, ok, "waitResume should report the context is still alive")
	case <-time.After(time.Second):
		t.Fatal("waitResume did not return after Resume")
	}
}

func TestWaitResumeReportsCancelledContext(t *testing.T) {
	d := testDevice(t)
	d.Suspend()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() { done <- d.waitResume(ctx) }()
	cancel()

	select {
	case ok := <-done:
		assert.False(t, ok, "a cancelled context must stop Run rather than resume it")
	case <-time.After(time.Second):
		t.Fatal("waitResume ignored the cancelled context")
	}
}

// Suspending during the reconnect backoff has to take effect at once, not up to
// maxBackoff later: on iOS this runs while the app is being put away.
//
// Tested on sleep directly rather than by driving Run. Reaching Run's backoff
// needs a dial to fail first, and no address fails fast here — RakNet is UDP, so
// a closed port does not refuse, it retries until its own timeout, well over ten
// seconds. The version this replaces dialled 127.0.0.1:1 and waited for
// LastError() to be non-nil, which a Device is born with (NewDevice sets
// ErrOffline), so it waited for nothing at all and then asserted against
// whatever Run happened to be doing. It passed only when other tests ran ahead
// of it, and hung for over thirty seconds when run alone.
func TestSleepWakesOnSuspend(t *testing.T) {
	suspendC := make(chan struct{})
	done := make(chan bool, 1)

	start := time.Now()
	go func() { done <- sleep(context.Background(), 30*time.Second, suspendC) }()
	close(suspendC)

	select {
	case woke := <-done:
		assert.True(t, woke, "a suspend interrupts the backoff; it does not end Run")
		assert.Less(t, time.Since(start), time.Second, "sleep waited out the backoff")
	case <-time.After(2 * time.Second):
		t.Fatal("sleep ignored the suspend and stayed in the backoff")
	}
}

// A cancelled context, by contrast, ends Run rather than merely interrupting it.
func TestSleepReportsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() { done <- sleep(ctx, 30*time.Second, make(chan struct{})) }()
	cancel()

	select {
	case woke := <-done:
		assert.False(t, woke, "a cancelled context must stop Run, not resume the loop")
	case <-time.After(2 * time.Second):
		t.Fatal("sleep ignored the cancelled context")
	}
}
