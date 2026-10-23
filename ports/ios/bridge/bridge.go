// Command bridge exposes the CK Karaoke web server to the iOS app as a C API.
//
// It is built with -buildmode=c-archive and linked into the app, so the server
// runs on a goroutine inside the app's own process. That is the whole reason this
// package exists: the Android app ships this same server as a separate executable
// and forks it, which iOS forbids outright.
//
// Everything here is called from Swift, so the surface is deliberately tiny —
// strings in, an int out, and no callbacks. Swift polls CKPort because the
// first run downloads the song catalogue and takes about twenty seconds, and a
// deadline long enough for that is indistinguishable from no deadline at all.
package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"github.com/rrgmc/ck-karaoke/client"
	"github.com/rrgmc/ck-karaoke/server"
	"github.com/rrgmc/ck-karaoke/webui"
)

var (
	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	port    int
	// failure is C-allocated and outlives the call that returns it, so Swift can
	// read it without owning it. It is freed only when the next run starts, which
	// bounds the whole thing to one small allocation however often Swift polls.
	failure *C.char
	device  *webui.Device
	// percent is catalogue download progress, or -1 when nothing is downloading.
	percent = -1
	// searching is whether a search found no device and another one is running.
	searching bool
	// generation counts starts, so a run that is abandoned rather than stopped
	// cannot clear the state of the one that replaced it. Without it a stuck run
	// finishing late would zero the new run's port, and the app would sit looking
	// at a server it had already been told about.
	generation int
)

// cgoGetenv reads the process environment as C sees it now, which is not what
// os.Getenv reads.
//
// The Go runtime copies the environment once, when it initialises — and in a
// c-archive that is at process start, long before Swift runs. So a setenv from
// Swift is invisible to os.Getenv and visible here. Android has no such problem:
// there the wrapper sets the variables on a ProcessBuilder and the server is
// exec'd with them already in place.
func cgoGetenv(key string) string {
	ckey := C.CString(key)
	defer C.free(unsafe.Pointer(ckey))
	return C.GoString(C.getenv(ckey))
}

// CKStart brings the server up on a goroutine and returns at once.
//
// addr may be empty, in which case the device is found by sweeping the local
// subnet — iOS cannot broadcast without an entitlement Apple grants only on
// request. Returns 1 if a server is already running, otherwise 0.
//
//export CKStart
func CKStart(dataDir, name, addr *C.char) C.int {
	mu.Lock()
	if running {
		mu.Unlock()
		return 1
	}
	if failure != nil {
		C.free(unsafe.Pointer(failure))
		failure = nil
	}
	// Copied to Go strings here and nowhere else. These pointers belong to the
	// caller and only for the duration of this call — Swift's withCString frees
	// its buffer the moment the closure returns — so a C.GoString evaluated later,
	// from a closure running on the server goroutine, reads freed memory. The
	// simulator can hide this: its freed bytes often start with a NUL and read
	// back as the empty string.
	dir := C.GoString(dataDir)
	goAddr := C.GoString(addr)
	goName := C.GoString(name)

	// Xcode's console shows the app's stderr, which is the nearest thing here to
	// the logcat the Android wrapper writes to. Debug level, because the
	// per-request lines are logged at Debug and they are the only way to tell a
	// web view that failed to load from one that loaded something empty.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg := server.Config{
		Addr: goAddr,
		// Loopback only. Binding the WiFi interface would put an unauthenticated
		// remote control on the network for anyone to find.
		Listen: "127.0.0.1:0",
		SongDB: filepath.Join(dir, "song.db"),
		FavDB:  filepath.Join(dir, "favorites.db"),
		// So switching the karaoke unit off still leaves a browsable song list and
		// working favorites, rather than a failure screen.
		LastAddrFile: filepath.Join(dir, "device.addr"),
		Name:         goName,
		Logger:       logger,

		// Go believes it is in UTC on iOS, so Swift sets these before calling in.
		Local: server.LocalZone(cgoGetenv),

		Resolve: func(ctx context.Context) (string, []client.Device, error) {
			return client.ResolveSweep(ctx, goAddr, client.DefaultSweepTimeout)
		},
		// A first launch waits for the user to answer the local network prompt.
		WaitForDevice: true,
		Searching: func() {
			mu.Lock()
			defer mu.Unlock()
			searching = true
		},
		Ready: func(listen net.Addr, device string) {
			mu.Lock()
			if tcp, ok := listen.(*net.TCPAddr); ok {
				port = tcp.Port
			}
			p := port
			mu.Unlock()
			// The equivalent of the CLI's `listening <addr>` line. Swift gets the
			// port from CKPort, but without this there is nothing in the log
			// to check a blank web view against.
			logger.Info("listening", "addr", listen, "port", p, "device", device)
		},
		Progress: func(have, total int) {
			mu.Lock()
			defer mu.Unlock()
			if total <= 0 {
				return
			}
			percent = have * 100 / total
			searching = false
		},
		OnDevice: func(d *webui.Device) {
			mu.Lock()
			defer mu.Unlock()
			device = d
		},
	}

	ctx, stop := context.WithCancel(context.Background())
	generation++
	gen := generation
	cancel, running, port, percent, searching = stop, true, 0, -1, false
	mu.Unlock()

	go func() {
		err := server.Run(ctx, cfg)
		mu.Lock()
		defer mu.Unlock()
		stop()
		if gen != generation {
			// Superseded while we were winding down. Everything below belongs to
			// whoever replaced us.
			return
		}
		running, port, device, searching = false, 0, nil, false
		if err != nil && ctx.Err() == nil {
			failure = C.CString(err.Error())
		}
	}()
	return 0
}

// CKPort is the port the server is listening on, or 0 until it is.
//
//export CKPort
func CKPort() C.int {
	mu.Lock()
	defer mu.Unlock()
	return C.int(port)
}

// CKProgress is how much of the song catalogue has arrived, 0 to 100, or -1
// when nothing is being downloaded.
//
// Only a first run downloads anything, and it takes long enough that without this
// the startup screen cannot say whether it is working or stuck.
//
//export CKProgress
func CKProgress() C.int {
	mu.Lock()
	defer mu.Unlock()
	return C.int(percent)
}

// CKSearching is 1 while no device has answered and the server is still looking
// for one, otherwise 0.
//
//export CKSearching
func CKSearching() C.int {
	mu.Lock()
	defer mu.Unlock()
	if searching {
		return 1
	}
	return 0
}

// CKError is why the server stopped, or NULL if it has not.
//
// The returned string belongs to Go and must not be freed by the caller; it stays
// valid until the next CKStart.
//
//export CKError
func CKError() *C.char {
	mu.Lock()
	defer mu.Unlock()
	return failure
}

// CKSuspend closes the session to the karaoke unit and stops reconnecting.
//
// Called as the app's background time runs out. Leaving the session open would
// hold one of the device's five client slots until the unit timed it out on its
// own, and the app is about to stop running either way.
//
//export CKSuspend
func CKSuspend() {
	mu.Lock()
	d := device
	mu.Unlock()
	if d != nil {
		d.Suspend()
	}
}

// CKResume reconnects after CKSuspend.
//
//export CKResume
func CKResume() {
	mu.Lock()
	d := device
	mu.Unlock()
	if d != nil {
		d.Resume()
	}
}

// CKStop shuts the server down and waits briefly for it to let go of the
// session, so the device does not go on showing a client connected.
//
// It blocks for up to six seconds, so call it off the main thread.
//
// It always leaves the bridge startable, which is the part that matters: a run
// that will not wind down is abandoned rather than waited on forever. Otherwise
// CKStart's guard against double-starting becomes a guard against ever
// starting again, and Try again does nothing at all for the rest of the process's
// life — which is exactly how it behaved.
//
//export CKStop
func CKStop() {
	mu.Lock()
	stop, was := cancel, running
	mu.Unlock()
	if !was || stop == nil {
		return
	}
	stop()
	// server.Run's own shutdown deadline is five seconds; this outlasts it rather
	// than racing it.
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := !running
		mu.Unlock()
		if done {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Still going. Abandon it: bump the generation so its own bookkeeping is
	// ignored when it eventually returns, and declare the bridge free.
	mu.Lock()
	defer mu.Unlock()
	generation++
	running, port, device = false, 0, nil
}

func main() {}
