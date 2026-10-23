// Package client drives a compatible karaoke device over RakNet.
//
// The device speaks RakNet protocol version 6 with the connection password
// "CooKaraSystem". Opening a session takes one message and about 100ms.
package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rrgmc/ck-karaoke/protocol"
	"github.com/sandertv/go-raknet"
)

const (
	// Port is the UDP port the device listens on.
	Port = 35379

	// ProtocolVersion is the RakNet protocol version the device accepts. Any
	// other version is rejected with ID_INCOMPATIBLE_PROTOCOL_VERSION.
	ProtocolVersion = 6

	// Password is the RakNet connection password the device requires.
	Password = "CooKaraSystem"

	// DefaultTimeout bounds how long a command waits for its echo. Replies
	// normally arrive within ~100ms.
	DefaultTimeout = 5 * time.Second

	// CloseTimeout bounds how long Close waits for the disconnect notification
	// to reach the device. go-raknet's tick loop emits it about a second after
	// the connection is marked closing.
	CloseTimeout = 3 * time.Second
)

// ErrBusy reports that the device is reachable but will not open a session.
//
// Two causes look identical from here: all connection slots are taken, or the
// session frame was malformed. Either way the Dial gets as far as the RakNet
// handshake and is then ignored at the application layer, so the session frame
// is never echoed.
var ErrBusy = errors.New("device is not accepting connections")

// FrameHandler receives every frame as it arrives, before normal dispatch. It is
// for tracing and protocol work; returning an error does not stop the client.
type FrameHandler func(raw []byte, frame protocol.Frame, msg protocol.Message, err error)

// Options configures a Client.
type Options struct {
	// Identity is the client to present. Any UUID works — the device checks a
	// checksum, not a registry — so protocol.NewIdentity generates one. Defaults
	// to a generated identity.
	//
	// Two programs talking to the same device at once need different
	// identities; sharing one makes the second session silently displace the
	// first.
	Identity *protocol.Identity

	// Logger receives connection-level events. Defaults to slog.Default.
	Logger *slog.Logger

	// OnFrame, if set, is called for every frame received.
	OnFrame FrameHandler

	// Timeout bounds waits for command echoes. Defaults to DefaultTimeout.
	Timeout time.Duration
}

// Client is a session with a karaoke device.
type Client struct {
	conn     *raknet.Conn
	identity protocol.Identity
	clientID string
	logger   *slog.Logger
	timeout  time.Duration
	onFrame  FrameHandler
	seq      atomic.Uint32

	mu sync.Mutex
	// echoes maps an outstanding request's sequence number to the channel
	// waiting for its echo. Status frames run their own sequence counter that
	// overlaps this one, so only command and session frames are matched here.
	echoes map[uint32]chan *protocol.CommandFrame
	// queues receives asynchronous reserved-list replies, which arrive as status
	// frames rather than as the command's echo. queueMu serialises Queue calls:
	// the reply carries no correlating identity, so two callers in flight at once
	// would steal each other's results.
	queueMu  sync.Mutex
	queues   chan *protocol.Queue
	status   *protocol.Status
	onStatus func(*protocol.Status)
	queue    *protocol.Queue
	onQueue  func(*protocol.Queue)
}

// Dial connects to a device and opens a session. address may omit the port.
func Dial(ctx context.Context, address string, opts Options) (*Client, error) {
	identity := protocol.NewIdentity()
	if opts.Identity != nil {
		identity = *opts.Identity
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTimeout
	}

	c := &Client{
		identity: identity,
		clientID: identity.ClientID,
		logger:   opts.Logger,
		timeout:  opts.Timeout,
		onFrame:  opts.OnFrame,
		echoes:   map[uint32]chan *protocol.CommandFrame{},
		queues:   make(chan *protocol.Queue, 4),
	}

	dialer := &raknet.Dialer{
		ErrorLog:        opts.Logger,
		ProtocolVersion: ProtocolVersion,
		Password:        Password,
		PacketHandler:   raknet.PacketHandlerFunc(c.handle),
	}
	conn, err := dialer.DialContext(ctx, WithDefaultPort(address))
	if err != nil {
		return nil, fmt.Errorf("dialing %s: %w", address, err)
	}
	c.conn = conn

	// The device ignores everything until a session is open. Waiting for the
	// echo rather than sleeping keeps this at about 100ms; sending a command
	// in the same breath as the session start gets it dropped.
	// No tag is passed: the session frame's is a checksum of its own body, so
	// SessionStart computes it.
	if _, err := c.Send(ctx, &protocol.SessionStart{
		Seq: c.NextSeq(), ClientID: c.clientID, Name: identity.DisplayName(),
	}); err != nil {
		c.Close()

		// Tell "the device is off" apart from "the device is busy". An
		// unconnected ping needs no session, so if that still answers, the unit
		// is alive and simply has no free slot — which is worth saying plainly,
		// because the cause is usually another remote left connected.
		pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, perr := Ping(pingCtx, address); perr == nil {
			return nil, fmt.Errorf("%w: %s answers but will not open a session as %s",
				ErrBusy, address, identity)
		}
		return nil, fmt.Errorf("opening session: %w", err)
	}
	return c, nil
}

// handle dispatches an incoming packet. It always reports the packet as handled
// so raknet does not also queue it for Read.
func (c *Client) handle(_ *raknet.Conn, b []byte) (bool, error) {
	frame, err := protocol.ParseFrame(b, true)
	var msg protocol.Message
	if err == nil {
		msg, _ = protocol.ParseMessage(frame)
	}
	c.mu.Lock()
	onFrame := c.onFrame
	c.mu.Unlock()
	if onFrame != nil {
		onFrame(b, frame, msg, err)
	}
	if err != nil {
		c.logger.Debug("undecodable frame", "error", err, "bytes", len(b))
		return true, nil
	}

	switch f := frame.(type) {
	case *protocol.CommandFrame:
		c.deliverEcho(f)
	case *protocol.StatusFrame:
		c.deliverStatus(msg)
	}
	return true, nil
}

func (c *Client) deliverEcho(f *protocol.CommandFrame) {
	c.mu.Lock()
	ch, ok := c.echoes[f.Seq]
	if ok {
		delete(c.echoes, f.Seq)
	}
	c.mu.Unlock()
	if ok {
		ch <- f
	}
}

func (c *Client) deliverStatus(msg protocol.Message) {
	switch m := msg.(type) {
	case *protocol.Status:
		c.mu.Lock()
		c.status = m
		fn := c.onStatus
		c.mu.Unlock()
		if fn != nil {
			fn(m)
		}
	case *protocol.Queue:
		// The device pushes the reserved list unprompted as well as in reply to
		// Queryorder, so keep every one: it means a caller can have the current
		// list immediately instead of waiting the ~2s a query takes.
		c.mu.Lock()
		c.queue = m
		fn := c.onQueue
		c.mu.Unlock()
		if fn != nil {
			fn(m)
		}

		select {
		case c.queues <- m:
		default: // nobody waiting on a query; the cache above still has it
		}
	}
}

// Close ends the session, waiting for the device to be told about it.
//
// This wait is not optional. go-raknet's Close only marks the connection as
// closing; the DISCONNECTION_NOTIFICATION is sent by its tick loop at least a
// second later. A short-lived process that closes and exits immediately never
// sends it at all, so the device keeps the connection slot until its own
// timeout, and a handful of quick commands can then leave no slots free.
func (c *Client) Close() error {
	if err := c.conn.Close(); err != nil {
		return err
	}
	// closeImmediately cancels the connection's context once the notification
	// has gone out.
	t := time.NewTimer(CloseTimeout)
	defer t.Stop()
	select {
	case <-c.conn.Context().Done():
	case <-t.C:
		c.logger.Warn("device was not told the session ended; its connection slot may stay busy",
			"after", CloseTimeout)
	}
	return nil
}

// ClientID returns the identifier sent to the device.
func (c *Client) ClientID() string { return c.clientID }

// Identity returns the client identity this session presents.
func (c *Client) Identity() protocol.Identity { return c.identity }

// ClientGUID and ServerGUID return the RakNet GUIDs negotiated at connect time.
func (c *Client) ClientGUID() int64 { return c.conn.ClientGUID() }
func (c *Client) ServerGUID() int64 { return c.conn.ServerGUID() }

// RemoteAddr returns the device's address.
func (c *Client) RemoteAddr() string { return c.conn.RemoteAddr().String() }

// Context is cancelled when the session ends, for any reason. Long-running
// callers can select on it instead of polling to notice a dropped device.
func (c *Client) Context() context.Context { return c.conn.Context() }

// NextSeq returns the next request sequence number.
func (c *Client) NextSeq() uint32 { return c.seq.Add(1) }

// Status returns the most recent state the device pushed, or nil if none has
// arrived yet.
func (c *Client) Status() *protocol.Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// OnStatus registers a callback for device state updates, replacing any previous
// one. Pass nil to stop.
func (c *Client) OnStatus(fn func(*protocol.Status)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onStatus = fn
}

// LastQueue returns the most recent reserved-song list seen, whether it was
// asked for or pushed by the device, or nil if none has arrived.
func (c *Client) LastQueue() *protocol.Queue {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.queue
}

// OnQueue registers a callback for reserved-list updates, replacing any previous
// one. Pass nil to stop.
func (c *Client) OnQueue(fn func(*protocol.Queue)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onQueue = fn
}

// Send transmits a message and waits for the device to echo it back. The echo
// carries the request's sequence number, and for some query commands it also
// carries the reply in its Response field.
func (c *Client) Send(ctx context.Context, msg protocol.Message) (*protocol.CommandFrame, error) {
	seq, ok := sequenceOf(msg)
	if !ok {
		return nil, fmt.Errorf("%T has no sequence number to correlate on", msg)
	}

	data, err := msg.Encode()
	if err != nil {
		return nil, fmt.Errorf("encoding %s: %w", msg, err)
	}

	ch := make(chan *protocol.CommandFrame, 1)
	c.mu.Lock()
	c.echoes[seq] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.echoes, seq)
		c.mu.Unlock()
	}()

	if _, err := c.conn.Write(data); err != nil {
		return nil, fmt.Errorf("sending %s: %w", msg, err)
	}

	timeout := time.NewTimer(c.timeout)
	defer timeout.Stop()
	select {
	case echo := <-ch:
		return echo, nil
	case <-timeout.C:
		return nil, fmt.Errorf("timed out waiting for echo of %s", msg)
	case <-ctx.Done():
		// A caller-imposed deadline and the timer above describe the same
		// outcome — the command went out and nothing came back — so they are
		// worded alike. Which one fires is merely whichever is shorter, and
		// callers should not have to tell them apart.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("timed out waiting for echo of %s: %w", msg, ctx.Err())
		}
		return nil, ctx.Err()
	}
}

// Do sends a command with string arguments and waits for its echo.
func (c *Client) Do(ctx context.Context, cmd protocol.Command, args ...string) (*protocol.CommandFrame, error) {
	req := protocol.NewCommandRequest(c.NextSeq(), c.clientID, cmd, args...)
	req.Name = c.identity.DisplayName()
	return c.Send(ctx, req)
}

// sequenceOf extracts the correlating sequence number from a request.
func sequenceOf(msg protocol.Message) (uint32, bool) {
	switch m := msg.(type) {
	case *protocol.CommandRequest:
		return m.Seq, true
	case *protocol.SessionStart:
		return m.Seq, true
	case *protocol.DatabaseRequest:
		return m.Seq, true
	default:
		return 0, false
	}
}

// Playback and queue commands.

// Reserve adds a song to the end of the queue.
func (c *Client) Reserve(ctx context.Context, songID string) error {
	_, err := c.Do(ctx, protocol.CmdAddOrder, songID, protocol.OrderToken())
	return err
}

// ReserveFirst adds a song to the front of the queue.
func (c *Client) ReserveFirst(ctx context.Context, songID string) error {
	_, err := c.Do(ctx, protocol.CmdFirstOrder, songID, protocol.OrderToken())
	return err
}

// PlayNow starts a song immediately.
func (c *Client) PlayNow(ctx context.Context, songID string) error {
	_, err := c.Do(ctx, protocol.CmdPlayEx, songID)
	return err
}

// NextSong advances to the next queued song. It also resumes after Stop.
func (c *Client) NextSong(ctx context.Context) error {
	_, err := c.Do(ctx, protocol.CmdNextSong)
	return err
}

// Skip skips the current song.
func (c *Client) Skip(ctx context.Context) error {
	_, err := c.Do(ctx, protocol.CmdSkip)
	return err
}

// Pause pauses or resumes playback.
func (c *Client) Pause(ctx context.Context) error {
	_, err := c.Do(ctx, protocol.CmdPause)
	return err
}

// Stop halts playback; use NextSong to continue.
func (c *Client) Stop(ctx context.Context) error {
	_, err := c.Do(ctx, protocol.CmdStop)
	return err
}

// RemoveFromQueue drops a reserved song.
//
// DelOrder takes the song id, like Reserve. Its siblings UpOrder and DownOrder
// are acknowledged but have no visible effect, so reordering is not offered.
func (c *Client) RemoveFromQueue(ctx context.Context, songID string) error {
	_, err := c.Do(ctx, protocol.CmdDelOrder, songID)
	return err
}

// Repeat toggles repeating the current song.
func (c *Client) Repeat(ctx context.Context) error {
	_, err := c.Do(ctx, protocol.CmdRepeatFlag)
	return err
}

// Audio and tone.

// SetKey shifts the key by the given number of semitones. This is a delta, not
// an absolute setting: sending -1 twice goes from 0 to -2. Same for SetTempo.
func (c *Client) SetKey(ctx context.Context, semitones int) error {
	_, err := c.Do(ctx, protocol.CmdSetKey, strconv.Itoa(semitones))
	return err
}

// SetTempo shifts the tempo by the given number of steps.
func (c *Client) SetTempo(ctx context.Context, steps int) error {
	_, err := c.Do(ctx, protocol.CmdSetTempo, strconv.Itoa(steps))
	return err
}

// ResetKey and ResetTempo return those controls to normal.
func (c *Client) ResetKey(ctx context.Context) error {
	_, err := c.Do(ctx, protocol.CmdResetKey)
	return err
}

func (c *Client) ResetTempo(ctx context.Context) error {
	_, err := c.Do(ctx, protocol.CmdResetTempo)
	return err
}

// SetVolume sets the song volume, 0-100 absolute. The device snaps the value to
// its thirteen-position grid, so use protocol.VolumeNudge to step by exactly one
// position rather than guessing an increment.
func (c *Client) SetVolume(ctx context.Context, level int) error {
	_, err := c.Do(ctx, protocol.CmdSetVolume, strconv.Itoa(level))
	return err
}

// SetMicVolume sets a microphone's volume, 0-100. mic is 1 or 2.
func (c *Client) SetMicVolume(ctx context.Context, mic, level int) error {
	cmd := protocol.CmdSetMic1Vol
	switch mic {
	case 1:
	case 2:
		cmd = protocol.CmdSetMic2Vol
	default:
		return fmt.Errorf("microphone must be 1 or 2, got %d", mic)
	}
	_, err := c.Do(ctx, cmd, strconv.Itoa(level))
	return err
}

// SetReverb sets the echo/reverb level, 0-100.
func (c *Client) SetReverb(ctx context.Context, level int) error {
	_, err := c.Do(ctx, protocol.CmdSetReverbVol, strconv.Itoa(level))
	return err
}

// SetMelody toggles the guide melody.
//
// It is a toggle, not a setter, despite the name: the argument is always "0",
// and there is no melody field in the status frame to set from.
func (c *Client) SetMelody(ctx context.Context) error {
	_, err := c.Do(ctx, protocol.CmdSetMelody, "0")
	return err
}

// Queries.

// Queue returns the reserved-song list. Unlike the other queries the reply
// arrives as an asynchronous status frame rather than in the command echo.
func (c *Client) Queue(ctx context.Context, start, count int) (*protocol.Queue, error) {
	c.queueMu.Lock()
	defer c.queueMu.Unlock()

	// Drain any list that arrived unsolicited so we return this call's reply.
	for {
		select {
		case <-c.queues:
			continue
		default:
		}
		break
	}

	// Send without waiting for the echo. The reserved list comes back as its own
	// status frame, so the echo carries nothing we need — waiting for it only
	// added a way for the call to fail while the real reply was already on its
	// way.
	req := protocol.NewCommandRequest(c.NextSeq(), c.clientID, protocol.CmdQueryorder,
		strconv.Itoa(start), strconv.Itoa(count))
	if err := c.send(req); err != nil {
		return nil, err
	}

	timeout := time.NewTimer(c.timeout)
	defer timeout.Stop()
	select {
	case q := <-c.queues:
		return q, nil
	case <-timeout.C:
		return nil, errors.New("timed out waiting for the reserved-song list")
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("timed out waiting for the reserved-song list: %w", ctx.Err())
		}
		return nil, ctx.Err()
	}
}

// Volumes returns the device's mixer state. The reply rides in the command echo.
func (c *Client) Volumes(ctx context.Context) (*protocol.Volumes, error) {
	echo, err := c.Do(ctx, protocol.CmdQueryVolInfo)
	if err != nil {
		return nil, err
	}
	if echo.Response == nil {
		return nil, errors.New("device returned no volume data")
	}
	return &protocol.Volumes{Seq: echo.Seq, Values: echo.Response.Fields}, nil
}

// RecordingCount returns how many recordings the device has stored.
func (c *Client) RecordingCount(ctx context.Context) (int, error) {
	echo, err := c.Do(ctx, protocol.CmdASKRECCOUNT)
	if err != nil {
		return 0, err
	}
	if echo.Response == nil || len(echo.Response.Fields) == 0 {
		return 0, errors.New("device returned no recording count")
	}
	n, err := strconv.Atoi(echo.Response.Fields[0])
	if err != nil {
		return 0, fmt.Errorf("recording count %q: %w", echo.Response.Fields[0], err)
	}
	return n, nil
}

// Refresh runs the three queries a client conventionally issues on connect.
// They are not required (the device accepts commands without them), but they
// prime the queue and mixer state.
func (c *Client) Refresh(ctx context.Context) error {
	if _, err := c.Queue(ctx, 0, 10); err != nil {
		return fmt.Errorf("querying the queue: %w", err)
	}
	if _, err := c.Volumes(ctx); err != nil {
		return fmt.Errorf("querying volumes: %w", err)
	}
	if _, err := c.RecordingCount(ctx); err != nil {
		return fmt.Errorf("querying recording count: %w", err)
	}
	return nil
}

// Wait blocks for d, or until ctx is cancelled.
func (c *Client) Wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
