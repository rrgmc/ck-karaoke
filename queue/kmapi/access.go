package kmapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rrgmc/ck-karaoke/queue/kmapi/wire"
)

// Level is an access level; see wire.Level.
type Level = wire.Level

// The access levels, lowest first.
const (
	View    = wire.LevelView
	Request = wire.LevelRequest
	Queue   = wire.LevelQueue
	Control = wire.LevelControl
	Host    = wire.LevelHost
	Admin   = wire.LevelAdmin
)

// ParseLevel reads a level's name.
func ParseLevel(s string) (Level, error) { return wire.ParseLevel(s) }

// Room levels: what everybody gets without a code. Host and Admin are never
// room levels.
func roomLevel(l Level) Level { return min(l, Control) }

// Codes are the codes a person types to reach a level. An empty code is not
// set.
type Codes struct {
	Queue   string
	Control string
	Host    string
}

func (c Codes) of(l Level) string {
	switch l {
	case Queue:
		return c.Queue
	case Control:
		return c.Control
	case Host:
		return c.Host
	}
	return ""
}

// TokenTTL is how long an access token lasts.
const TokenTTL = 12 * time.Hour

// requiredLevel is the level a route needs, as KaraokeMachine's
// routes::required_access sets it. path is relative to /api/v1.
func requiredLevel(method, path string) Level {
	if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions || path == "/login" {
		return View
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case method == http.MethodPost && path == "/singers",
		method == http.MethodPost && path == "/requests",
		method == http.MethodPut && len(parts) == 3 && parts[0] == "singers" && parts[2] == "name",
		method == http.MethodPost && len(parts) == 3 && parts[0] == "requests" && parts[2] == "withdraw":
		return Request
	case method == http.MethodPost && path == "/queue", method == http.MethodPut && path == "/settings":
		return Queue
	case method == http.MethodPost && path == "/singers/hosted",
		method == http.MethodPost && path == "/evening/reset",
		method == http.MethodPut && path == "/requests/accepting",
		method == http.MethodDelete && len(parts) == 2 && parts[0] == "singers",
		method == http.MethodPost && len(parts) == 3 && parts[0] == "singers" &&
			(parts[2] == "reissue" || parts[2] == "requests" || parts[2] == "merge"),
		method == http.MethodPost && len(parts) == 3 && parts[0] == "requests" &&
			(parts[2] == "decline" || parts[2] == "accept"):
		return Host
	}
	return Control
}

// auth issues and checks access tokens.
//
// A token is "v1.<level>.<expires unix>.<nonce>.<mac>": an HMAC-SHA256 over
// its own fields and the level's current code, keyed on a secret the server
// keeps. Nothing is stored per token. Changing a level's code ends that level's
// tokens; a new secret ends them all.
type auth struct {
	secret []byte
	now    func() time.Time

	mu       sync.Mutex
	codes    Codes
	room     Level
	failures map[string][]time.Time
	all      []time.Time
}

// Login limits: five failures per address and twenty in all, per minute.
const (
	maxFailedLogins       = 5
	maxFailedLoginsGlobal = 20
	loginWindow           = time.Minute
)

func newAuth(secret []byte, room Level, codes Codes, now func() time.Time) *auth {
	return &auth{secret: secret, room: roomLevel(room), codes: codes, now: now, failures: map[string][]time.Time{}}
}

func (a *auth) set(room Level, codes Codes) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.room, a.codes = roomLevel(room), codes
}

func (a *auth) snapshot() (Level, Codes) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.room, a.codes
}

func (a *auth) sign(level Level, code string, expires int64, nonce string) string {
	m := hmac.New(sha256.New, a.secret)
	fmt.Fprintf(m, "ck-queue-access-v1|%s|%s|%d|%s", level, code, expires, nonce)
	return hex.EncodeToString(m.Sum(nil)[:16])
}

// issue returns a token for a level.
func (a *auth) issue(level Level) string {
	_, codes := a.snapshot()
	expires := a.now().Add(TokenTTL).Unix()
	nonce := randomHex(8)
	return fmt.Sprintf("v1.%s.%d.%s.%s", level, expires, nonce, a.sign(level, codes.of(level), expires, nonce))
}

// levelOf returns the level a token opens, or false when it does not verify.
func (a *auth) levelOf(token string) (Level, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 5 || parts[0] != "v1" {
		return View, false
	}
	level, err := ParseLevel(parts[1])
	if err != nil || level < Queue || level > Host {
		return View, false
	}
	expires, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || a.now().Unix() >= expires {
		return View, false
	}
	_, codes := a.snapshot()
	code := codes.of(level)
	if code == "" {
		return View, false
	}
	want := a.sign(level, code, expires, parts[3])
	if !hmac.Equal([]byte(want), []byte(parts[4])) {
		return View, false
	}
	return level, true
}

// callerLevel is the higher of the room's level and the token's.
func (a *auth) callerLevel(token string) Level {
	room, _ := a.snapshot()
	if l, ok := a.levelOf(token); ok && l > room {
		return l
	}
	return room
}

var (
	errWrongCode   = errors.New("incorrect code")
	errNoCodes     = errors.New("no access code is set")
	errTooManyTrys = errors.New("too many failed codes; wait a minute")
)

// login exchanges a code for a token, trying the host, control and queue
// codes in that order. It returns how long to wait when the address or the
// server has failed too often.
func (a *auth) login(addr, code string) (Level, string, time.Duration, error) {
	now := a.now()
	a.mu.Lock()
	cut := now.Add(-loginWindow)
	a.failures[addr] = recent(a.failures[addr], cut)
	a.all = recent(a.all, cut)
	if len(a.failures[addr]) >= maxFailedLogins || len(a.all) >= maxFailedLoginsGlobal {
		oldest := a.all[0]
		if len(a.failures[addr]) >= maxFailedLogins {
			oldest = a.failures[addr][0]
		}
		a.mu.Unlock()
		return View, "", oldest.Add(loginWindow).Sub(now), errTooManyTrys
	}
	codes := a.codes
	a.mu.Unlock()

	if codes == (Codes{}) {
		return View, "", 0, errNoCodes
	}
	for _, l := range []Level{Host, Control, Queue} {
		if c := codes.of(l); c != "" && hmac.Equal([]byte(c), []byte(strings.TrimSpace(code))) {
			return l, a.issue(l), 0, nil
		}
	}
	a.mu.Lock()
	a.failures[addr] = append(a.failures[addr], now)
	a.all = append(a.all, now)
	a.mu.Unlock()
	return View, "", 0, errWrongCode
}

func recent(ts []time.Time, cut time.Time) []time.Time {
	i := 0
	for i < len(ts) && !ts[i].After(cut) {
		i++
	}
	return ts[i:]
}

// randomHex returns n random bytes in lowercase hex. Tests replace it.
var randomHex = func(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("kmapi: cannot read random bytes: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// bearer returns the token of an Authorization header.
func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}
