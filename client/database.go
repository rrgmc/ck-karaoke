package client

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rrgmc/ck-karaoke/protocol"
)

// SendRaw transmits already-encoded bytes without waiting for a reply. Intended
// for diagnostics; prefer Send or the typed commands.
func (c *Client) SendRaw(b []byte) error {
	if _, err := c.conn.Write(b); err != nil {
		return fmt.Errorf("sending %d raw bytes: %w", len(b), err)
	}
	return nil
}

// send writes a message without correlating a reply. The database transfer steps
// are bare opcodes with no sequence number, so they cannot be matched to an echo.
func (c *Client) send(msg protocol.Message) error {
	data, err := msg.Encode()
	if err != nil {
		return fmt.Errorf("encoding %s: %w", msg, err)
	}
	return c.SendRaw(data)
}

// DownloadOptions tunes the database transfer.
type DownloadOptions struct {
	// Path and Name locate the database on the device. Both default to the
	// protocol.DefaultDatabase* constants.
	Path, Name string
	// Idle ends the transfer once no chunk has arrived for this long.
	// Defaults to 15s.
	Idle time.Duration
}

// DownloadDatabase streams the song database, handing each chunk to sink as it
// arrives, and returns the number of chunks delivered.
//
// Chunks are passed **whole**, header included. Their headers are opaque and
// vary in length, so stripping a fixed prefix here would silently corrupt the
// file — feed them to catalog.Reassembler, which works the boundaries out from
// the SQLite header instead. catalog.Fetch does all of this for you.
//
// The device does not announce the transfer size or send a terminator, so the
// transfer ends once chunks stop arriving for Idle.
func (c *Client) DownloadDatabase(ctx context.Context, sink func(chunk []byte) error, opts DownloadOptions) (int, error) {
	if opts.Path == "" {
		opts.Path = protocol.DefaultDatabasePath
	}
	if opts.Name == "" {
		opts.Name = protocol.DefaultDatabaseName
	}
	if opts.Idle == 0 {
		opts.Idle = 15 * time.Second
	}

	var (
		mu      sync.Mutex
		chunks  int
		sinkErr error
		last    = time.Now()
	)

	// Chunks arrive on the frame path, so intercept them for the duration.
	prev := c.frameHandler()
	c.setFrameHandler(func(raw []byte, frame protocol.Frame, msg protocol.Message, err error) {
		if prev != nil {
			prev(raw, frame, msg, err)
		}
		chunk, ok := msg.(*protocol.DatabaseChunk)
		if !ok {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		last = time.Now()
		chunks++
		if err := sink(chunk.Raw); err != nil && sinkErr == nil {
			sinkErr = err
		}
	})
	defer c.setFrameHandler(prev)

	req := &protocol.DatabaseRequest{
		Seq: c.NextSeq(), ClientID: c.clientID, Path: opts.Path, Name: opts.Name,
	}
	if _, err := c.Send(ctx, req); err != nil {
		return 0, fmt.Errorf("requesting the database: %w", err)
	}

	// The transfer is driven by three bare opcodes. They carry no sequence
	// number, so there is nothing to correlate and nothing to wait for.
	for _, step := range []protocol.Message{
		protocol.NewDatabaseStart(),
		protocol.NewDatabaseContinue(),
		protocol.NewDatabaseFinish(),
	} {
		if err := c.send(step); err != nil {
			return 0, err
		}
		if err := c.Wait(ctx, 200*time.Millisecond); err != nil {
			return 0, err
		}
	}

	// Wait for the stream to go quiet.
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return chunks, ctx.Err()
		case <-tick.C:
			mu.Lock()
			idle, n, err := time.Since(last), chunks, sinkErr
			mu.Unlock()
			if err != nil {
				return n, err
			}
			if idle >= opts.Idle {
				if n == 0 {
					return 0, errors.New("device sent no database data")
				}
				return n, nil
			}
		}
	}
}

func (c *Client) setFrameHandler(fn FrameHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onFrame = fn
}

func (c *Client) frameHandler() FrameHandler {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.onFrame
}
