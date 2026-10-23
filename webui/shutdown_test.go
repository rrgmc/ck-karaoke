package webui

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

// An event stream is an active connection that never goes idle, and
// http.Server.Shutdown waits for active connections. So an open stream would
// hold shutdown until its deadline and surface as "context deadline exceeded"
// on every stop, which looks like a fault but is only a stream doing what it
// is for.
func TestShutdownDoesNotWaitForEventStreams(t *testing.T) {
	srv := &Server{
		hub:      newHub(),
		stopping: make(chan struct{}),
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /events", srv.serveSSE)
	hs := &http.Server{Handler: mux}
	hs.RegisterOnShutdown(srv.BeginShutdown)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	go hs.Serve(ln)

	// Open a stream and leave it open, as a phone with the page up would.
	resp, err := http.Get("http://" + ln.Addr().String() + "/events")
	if err != nil {
		t.Fatalf("opening the stream: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q; the stream is not actually open", got)
	}

	// Generous enough that only a hang can exceed it, short enough that a
	// regression fails the test rather than slowing it down.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	if err := hs.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("shutdown took %s; it waited for the event stream", elapsed.Round(time.Millisecond))
	}
}

// Shutdown can be signalled more than once — RegisterOnShutdown fires on every
// Shutdown call, and closing a closed channel panics.
func TestBeginShutdownIsIdempotent(t *testing.T) {
	srv := &Server{hub: newHub(), stopping: make(chan struct{})}
	srv.BeginShutdown()
	srv.BeginShutdown()

	select {
	case <-srv.stopping:
	default:
		t.Error("stopping was not closed")
	}
}
