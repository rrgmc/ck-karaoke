// Package kmclient is a client of KaraokeMachine's HTTP API, as the CK Queue
// Manager serves it, for CK Queue Remote.
//
// It decodes the same types the server encodes (queue/kmapi/wire), so a
// change to the contract shows up here as a compile error, not a silent
// mismatch.
package kmclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/rrgmc/ck-karaoke/queue/kmapi/wire"
)

// ErrUnauthorized matches an answer of 401 or 403: the call needs an access
// code, or a better one.
var ErrUnauthorized = errors.New("unauthorized")

// ErrNotCK reports a server that speaks KaraokeMachine's API but is not a CK
// Queue Manager: it has no CK catalogue routes.
var ErrNotCK = errors.New("not a CK Queue Manager")

// Error is an error answer from the server.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s (%d %s)", e.Message, e.Status, e.Code)
	}
	return fmt.Sprintf("%d %s", e.Status, e.Code)
}

// Is makes errors.Is(err, ErrUnauthorized) true for 401 and 403.
func (e *Error) Is(target error) bool {
	return target == ErrUnauthorized &&
		(e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden)
}

// Client talks to one server. It is safe for concurrent use.
type Client struct {
	base string
	http *http.Client

	mu    sync.Mutex
	token string
}

// New returns a client for the server at base, such as
// "http://192.168.1.10:8177". A nil http.Client means http.DefaultClient;
// pass one with a timeout for anything a person waits on.
func New(base string, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{base: strings.TrimRight(base, "/"), http: hc}
}

// Base is the server's address, as given to New.
func (c *Client) Base() string { return c.base }

// SetToken sets the bearer token sent with every call. Empty sends none.
func (c *Client) SetToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = token
}

// Token is the bearer token in use.
func (c *Client) Token() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// call sends one request to /api/v1 and decodes the answer into out, unless
// out is nil.
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/api/v1"+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if t := c.Token(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readError(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	return nil
}

func readError(resp *http.Response) error {
	e := &Error{Status: resp.StatusCode}
	var body wire.Error
	if b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10)); err == nil && json.Unmarshal(b, &body) == nil {
		e.Code, e.Message = body.Error, body.Message
	}
	if e.Code == "" {
		e.Code = strings.ToLower(strings.ReplaceAll(http.StatusText(resp.StatusCode), " ", "_"))
	}
	return e
}

// Discovery, access and the catalogue.

// Discover reads what the server says about itself.
func (c *Client) Discover(ctx context.Context) (wire.Discovery, error) {
	var d wire.Discovery
	return d, c.call(ctx, http.MethodGet, "/discover", nil, &d)
}

// Access reads the caller's level, the room's and which codes exist.
func (c *Client) Access(ctx context.Context) (wire.Access, error) {
	var a wire.Access
	return a, c.call(ctx, http.MethodGet, "/access", nil, &a)
}

// Login exchanges an access code for a token, and uses the token from then
// on.
func (c *Client) Login(ctx context.Context, code string) (wire.AccessGrant, error) {
	var g wire.AccessGrant
	if err := c.call(ctx, http.MethodPost, "/login", wire.Login{Password: code}, &g); err != nil {
		return g, err
	}
	c.SetToken(g.Token)
	return g, nil
}

// CKCatalog describes the unit's song database. A server without the route
// answers ErrNotCK.
func (c *Client) CKCatalog(ctx context.Context) (wire.CKCatalog, error) {
	var cat wire.CKCatalog
	err := c.call(ctx, http.MethodGet, "/ck/catalog", nil, &cat)
	var e *Error
	if errors.As(err, &e) && e.Status == http.StatusNotFound {
		return cat, fmt.Errorf("%w: %v", ErrNotCK, err)
	}
	return cat, err
}

// DownloadSongDB writes the unit's song database to w, unless its hash is
// sha256, in which case it writes nothing and reports false.
func (c *Client) DownloadSongDB(ctx context.Context, sha256 string, w io.Writer) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v1/ck/catalog/song.db", nil)
	if err != nil {
		return false, err
	}
	if sha256 != "" {
		req.Header.Set("If-None-Match", `"`+sha256+`"`)
	}
	if t := c.Token(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified:
		return false, nil
	case resp.StatusCode == http.StatusNotFound:
		return false, fmt.Errorf("%w: %v", ErrNotCK, readError(resp))
	case resp.StatusCode >= 400:
		return false, readError(resp)
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return false, fmt.Errorf("downloading the song database: %w", err)
	}
	return true, nil
}

// Playback.

// State reads the transport, the song playing and the settings.
func (c *Client) State(ctx context.Context) (wire.State, error) {
	var s wire.State
	return s, c.call(ctx, http.MethodGet, "/state", nil, &s)
}

// Transport sends play, pause, skip, restart or stop.
func (c *Client) Transport(ctx context.Context, verb string) (wire.State, error) {
	var s wire.State
	return s, c.call(ctx, http.MethodPost, "/transport/"+verb, nil, &s)
}

// The queue.

// Queue reads the queue.
func (c *Client) Queue(ctx context.Context) (wire.Queue, error) {
	var q wire.Queue
	return q, c.call(ctx, http.MethodGet, "/queue", nil, &q)
}

// AddToQueue adds a song with no singer. It needs the queue level.
func (c *Client) AddToQueue(ctx context.Context, song int) (wire.AddedToQueue, error) {
	var a wire.AddedToQueue
	return a, c.call(ctx, http.MethodPost, "/queue", wire.AddToQueue{Number: wire.Number(song)}, &a)
}

// RemoveEntry takes one entry off the queue.
func (c *Client) RemoveEntry(ctx context.Context, entry uint64) (wire.Queue, error) {
	var q wire.Queue
	return q, c.call(ctx, http.MethodDelete, "/queue/"+id(entry), nil, &q)
}

// MoveEntry moves one entry to a 0-based place in the queue.
func (c *Client) MoveEntry(ctx context.Context, entry uint64, to int) (wire.Queue, error) {
	var q wire.Queue
	return q, c.call(ctx, http.MethodPost, "/queue/"+id(entry)+"/move", wire.Move{ToIndex: to}, &q)
}

// ClearQueue empties the queue.
func (c *Client) ClearQueue(ctx context.Context) (wire.Queue, error) {
	var q wire.Queue
	return q, c.call(ctx, http.MethodDelete, "/queue", nil, &q)
}

// Singers.

// Singers lists the evening's singers.
func (c *Client) Singers(ctx context.Context) (wire.Singers, error) {
	var s wire.Singers
	return s, c.call(ctx, http.MethodGet, "/singers", nil, &s)
}

// RegisterSinger takes a seat for this phone. device names the phone, the
// same every time; name may be empty. The answer's token proves the seat.
func (c *Client) RegisterSinger(ctx context.Context, device, name string) (wire.SingerIssued, error) {
	var s wire.SingerIssued
	return s, c.call(ctx, http.MethodPost, "/singers",
		wire.RegisterSinger{Device: device, Name: wire.OptString(name)}, &s)
}

// RenameSinger names the caller's own singer.
func (c *Client) RenameSinger(ctx context.Context, singer uint64, token, name string) (wire.Singer, error) {
	var s wire.Singer
	return s, c.call(ctx, http.MethodPut, "/singers/"+id(singer)+"/name",
		wire.RenameSinger{Token: token, Name: name}, &s)
}

// CreateSinger adds a singer with no phone. It needs the host level.
func (c *Client) CreateSinger(ctx context.Context, name string) (wire.Singer, error) {
	var s wire.Singer
	return s, c.call(ctx, http.MethodPost, "/singers/hosted", wire.CreateSinger{Name: wire.OptString(name)}, &s)
}

// RemoveSinger removes a singer and their waiting requests. It needs the host
// level.
func (c *Client) RemoveSinger(ctx context.Context, singer uint64) (wire.Singer, error) {
	var s wire.Singer
	return s, c.call(ctx, http.MethodDelete, "/singers/"+id(singer), nil, &s)
}

// Requests.

// Requests lists the requests, whether requests are open, and what was sung.
func (c *Client) Requests(ctx context.Context) (wire.Requests, error) {
	var r wire.Requests
	return r, c.call(ctx, http.MethodGet, "/requests", nil, &r)
}

// Request asks for a song as the caller's own singer, proved by its token.
func (c *Client) Request(ctx context.Context, singer uint64, token string, song int) (wire.Request, error) {
	var r wire.Request
	return r, c.call(ctx, http.MethodPost, "/requests",
		wire.AddRequest{Singer: singer, Token: token, Number: wire.Number(song)}, &r)
}

// RequestFor asks for a song on a singer's behalf. It needs the host level.
func (c *Client) RequestFor(ctx context.Context, singer uint64, song int) (wire.Request, error) {
	var r wire.Request
	return r, c.call(ctx, http.MethodPost, "/singers/"+id(singer)+"/requests",
		wire.AddSingerRequest{Number: wire.Number(song)}, &r)
}

// Withdraw takes back one of the caller's own requests.
func (c *Client) Withdraw(ctx context.Context, request uint64, token string) (wire.Request, error) {
	var r wire.Request
	return r, c.call(ctx, http.MethodPost, "/requests/"+id(request)+"/withdraw", wire.Withdraw{Token: token}, &r)
}

// Accept puts a request in the queue; next puts it at the front. It needs the
// host level.
func (c *Client) Accept(ctx context.Context, request uint64, next bool) (wire.Request, error) {
	var r wire.Request
	return r, c.call(ctx, http.MethodPost, "/requests/"+id(request)+"/accept", wire.Accept{Next: next}, &r)
}

// Decline refuses a request. It needs the host level.
func (c *Client) Decline(ctx context.Context, request uint64) (wire.Request, error) {
	var r wire.Request
	return r, c.call(ctx, http.MethodPost, "/requests/"+id(request)+"/decline", nil, &r)
}

// SetAccepting opens or closes requests. It needs the host level.
func (c *Client) SetAccepting(ctx context.Context, accepting bool) (wire.Requests, error) {
	var r wire.Requests
	return r, c.call(ctx, http.MethodPut, "/requests/accepting", wire.Accepting{Accepting: accepting}, &r)
}

// ResetEvening starts a new evening. It needs the host level.
func (c *Client) ResetEvening(ctx context.Context) (wire.Requests, error) {
	var r wire.Requests
	return r, c.call(ctx, http.MethodPost, "/evening/reset", nil, &r)
}

func id(n uint64) string { return strconv.FormatUint(n, 10) }
