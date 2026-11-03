# Queue manager: viability study

Status: study only. Nothing described here is built.

This document evaluates a separate server app, called the **queue manager**
here. It is the only program that holds a session with the karaoke unit. Remote
apps connect to the queue manager instead of the unit, browse the catalogue,
ask for songs and see the queue. The queue manager decides the order, with an
optional fairness rule, and feeds songs to the unit.

The study also evaluates whether the queue manager can clone the network API of
KaraokeMachine (`F:\prog\personal\karaokemachine`), so that the existing
**KM Remote** app works with it unchanged.

References to the KaraokeMachine source are relative to that repository.

## Summary

| Question | Answer |
|---|---|
| Can a server own the unit's queue and apply its own order? | Yes, by feeding the unit one song at a time. The unit cannot reorder its own queue. |
| Can it clone the KaraokeMachine API for KM Remote? | Yes. The API is plain HTTP, JSON and one WebSocket, documented, MIT or Apache-2.0, with no signatures or identity checks. |
| Can it reproduce KaraokeMachine's fairness rule exactly? | Yes. The rule is a small, pure state machine (`crates/machine/km-queue`) that ports to Go directly. |
| Desktop tray app? | Yes, with a tray library outside Wails. |
| Android host? | Possible, with the most open questions: multicast, Wi-Fi sleep, foreground service limits and binding to the LAN. |

Three things need tests against a real unit before the design is final (see
[Blockers and open questions](#blockers-and-open-questions)):

1. how the unit moves from one song to the next when its queue holds only one
   entry;
2. how to tell "paused" from "playing", which no status field reports;
3. whether the unit's range of song ids fits KaraokeMachine's number model.

There is one hard limit: **the queue manager cannot stop other clients from
queueing on the unit directly.** It can only notice their entries and adopt them.

## 1. What the unit allows

Facts from [PROTOCOL.md](PROTOCOL.md) that shape the design:

- **No reordering.** `UpOrder` and `DownOrder` are echoed but change nothing
  (section 6.4). The unit can add at the end (`AddOrder`), add at the front
  (`FirstOrder`), remove by song id (`DelOrder`) and play a song now (`PlayEx`).
- **Removal is by song id**, not by entry. Two entries of the same song are
  ambiguous.
- **The queue is visible.** The unit pushes it whenever it changes, ten entries
  per frame (section 7.2). Each entry names the session that queued it.
- **Attribution comes from the session name** (section 3.4). One session is one
  name.
- **Sessions are cheap.** Connect, one command and close takes about 250 ms. At
  least five sessions with distinct ids work at once (section 3.2).
- **Other clients stay possible.** The unit accepts several sessions, and its
  own infrared remote and on-screen menus still work. Nothing locks them out.
- **Playback status** arrives about every 1.5 seconds: position, current and
  next title, current song id, key, tempo, volumes (section 7.1). There is no
  pause flag and no song duration.
- **The catalogue has no durations.** `TableSong` has number, title, artist and
  language only (section 8.3).

## 2. Feeding the unit

The queue manager keeps the real queue in its own memory. The unit's queue is
only a short buffer.

**Proposed approach: keep one song ahead.** While a song plays, the unit's queue
holds exactly the next song from the queue manager's order. When a new song
starts (status field 8 changes), the queue manager sends `AddOrder` for the
following one. When the order changes, only the single buffered entry might need
a `DelOrder` and a new `AddOrder`.

- Reordering, fairness and removal then cost at most two commands.
- The unit's own screen still shows the correct "next song" (status field 6).
- To attribute a song to its singer on the unit's screen, the queue manager can
  queue each song through a short session named after the singer, with one
  identity per singer. A single long session for status and control stays open
  beside it. This needs testing: a session per command adds about 250 ms and
  uses a slot briefly.

**Alternative: empty buffer.** Queue nothing on the unit and start each song
with `PlayEx` when the previous one ends. This gives complete control but leaves
the unit idle between songs and depends on reliably detecting the end of a song.
It is the fallback if the one-ahead approach misbehaves.

**Foreign entries.** Songs queued by another client or the unit's remote appear
in the pushed queue under a different name. The queue manager cannot prevent
them. The simplest policy is to adopt them as songs with no singer, shown in the
queue, and let them play in the unit's order. An optional policy removes them
with `DelOrder` and re-adds them through the fair queue.

## 3. Cloning the KaraokeMachine API

KM Remote talks to a KaraokeMachine over plain HTTP under `/api/v1`, with JSON
bodies and one server-to-client WebSocket at `/api/v1/events`
(`crates/remote/km-remote-core/src/client.rs`). The whole contract is in one file,
`crates/machine/km-api/src/dto.rs`, and the route table in
`crates/machine/km-api/src/routes.rs`. The project is MIT or Apache-2.0. There
is no TLS, no signature, no certificate pinning and no version handshake.

### 3.1 Announcing as a KaraokeMachine

| Mechanism | What the queue manager must do |
|---|---|
| mDNS / DNS-SD (desktop and Android remotes) | Advertise `_karaokemachine._tcp.local.` with TXT records `v=1`, `id` (16 hex characters, persisted), `name`, `api=/api/v1` and `url=http://<IPv4>:<port>` (`crates/machine/km-api/src/discover.rs`). |
| Unicast sweep (iOS remote) | Listen on **port 8177**, because the sweep only probes that port, and answer `GET /api/v1/discover` with `app: "karaokemachine"`. |
| Typed address | Works on any port. |

Go has several mDNS libraries (for example `github.com/grandcat/zeroconf`,
`github.com/brutella/dnssd`). On Android, the platform's `NsdManager` is a
better choice (section 6).

### 3.2 Endpoints needed

KM Remote's needs, from `client.rs` and the page handlers:

| Group | Endpoints | Needed for |
|---|---|---|
| Discovery | `GET /discover` | everything |
| Catalogue | `GET /songs/export?limit&after` (NDJSON, keyset paging, `x-km-catalog-version` header) | browsing and search |
| Access | `GET /access`, `POST /login` | every write; without `/access` the remote refuses all writes |
| Queue | `GET /queue`, `POST /queue`, `DELETE /queue/{id}`, `POST /queue/{id}/move`, `DELETE /queue` | queue page and host actions |
| Singers and requests | `GET/POST /singers`, `PUT /singers/{id}/name`, `GET/POST /requests`, `POST /requests/{id}/withdraw` | the default "queue" button, which asks for a song as a singer |
| Host actions | `/singers/hosted`, `/decline`, `/accept`, `/merge`, `/reissue`, `DELETE /singers/{id}`, `PUT /requests/accepting`, `POST /evening/reset` | the Host tab; optional |
| Playback | `GET /state`, `POST /transport/{play,pause,skip,restart,stop}`, `PUT /settings` | the now-playing bar and controls |
| Events | WebSocket `state` (at least every 5 s; KaraokeMachine sends 4 per second), `queue_changed`, `requests_changed`, `singers_changed`, `song_started`, `song_ended`, `settings_changed` | live updates; silence for 5 s makes the remote reconnect |

Search is done entirely inside KM Remote against its own copy of the catalogue,
so the queue manager needs no search endpoint. KM Remote does not use lyrics,
microphones, wallpapers or the admin routes.

### 3.3 Mapping this unit onto the API

| KM Remote expects | This unit gives | Mapping |
|---|---|---|
| Song number: a decimal string from 1 to 9,999,999, read as `bank × 1000 + slot` | `SongIndex`, an integer (for example 19535) | Use the `SongIndex` as the number if every id fits the range. Report one synthetic package. **Needs checking against a real catalogue.** |
| `kind`: `midi`, `video`, `cdg`, `ultrastar` or `lrc` | nothing | Send `midi`; an unknown kind also maps to `Unknown` without error. |
| `duration_ms` (required) | nothing | Send `0`. Wait-time estimates (`ahead_ms`) then show nothing useful. A measured duration could be stored after each song is played once. |
| `artist`, `language` | `SingerName`, `LanType` via `TableLan` | Direct. |
| `transport`: `idle`, `playing`, `paused`, `stopped` | no pause flag | Infer: a song id with a moving position is `playing`; a stalled position is `paused`; no song id is `idle`. **Needs checking.** |
| `position_ms` | status field 2 | Direct. |
| `transpose` (absolute, ±6) | `SetKey` takes a delta; status field 17 reports the absolute key | Send the difference from the reported key. |
| `tempo_ratio` (0.75 to 1.25) | `SetTempo` takes a delta in steps of 1000/30; status field 18 | Convert ratio to steps and send the difference. |
| `music_volume` (0.0 to 1.0) | `SetVolume`, 0 to 100 on a 13-step grid | Scale and snap. |
| `melody_enabled` | `SetMelody` toggles; no status reports it | Track it locally. It drifts if anyone else toggles it. Or report melody as unavailable. |
| `transport/restart` | no command | `PlayEx` with the current song id, if the unit restarts that way. Otherwise refuse with a 409 code. |
| `transport/play`, `pause`, `stop`, `skip` | `Pause` (toggle), `Stop`, `NextSong`, `Skip` | Play and pause need the inferred state to decide whether to toggle. |

Capabilities KM Remote does not negotiate are carried as flags in the state
(`transpose_available`, `tempo_available`, `melody_available`). Sending `false`
hides a control cleanly. Unsupported actions can answer `409` with a code; KM
Remote shows a generic refusal for codes it does not know.

### 3.4 The parts that need care

- **Exact JSON shapes.** KM Remote deserialises the Rust structures directly. A
  missing required field or a number where a string is expected (song numbers)
  fails the call, and a malformed event is silently skipped. The Go structures
  should be checked against recorded KaraokeMachine responses in tests.
- **Access levels.** `GET /access` must report a level at least as high as the
  action: `request` to ask for a song, `queue` for plain adds and settings,
  `control` for transport and queue edits, `host` for the Host tab.
- **API drift.** KaraokeMachine changes over time. Since both projects have the
  same owner, the API can be pinned to a KaraokeMachine version, and a contract
  test in that repository could replay the queue manager's responses.

## 4. Fairness, exactly as in KaraokeMachine

The rule lives in `crates/machine/km-queue/src/evening.rs`, `queue.rs` and
`seats.rs`. It is pure state with no I/O, so a direct Go port is realistic.

- A **singer** (a "seat") is registered by a device and a name and gets a token.
- A singer's songs are **requests**, kept oldest first, with the states
  `waiting`, `queued`, `played`, `declined` and `withdrawn`.
- Every change ends in `settle()`, which walks the waiting requests oldest
  first. In **Turns** order a request enters the queue only if its singer has no
  entry there; it always goes to the end. In **Arrival** order (the default)
  every request enters at once.
- A singer's next song therefore enters only when their current one starts, so
  the queue holds one song per singer and forms rounds without a round counter.
  A new singer waits at most one round.
- Limits: `max_waiting` per singer (default 10), queue cap 200, request cap 1000,
  optional `name_required` and `max_per_device`.
- Operator actions: remove, withdraw, decline, accept now (end or front), merge
  two singers, remove a singer, reset the evening. Manual moves are never undone
  by the rule.
- Optional persistence writes the whole evening to a JSON file and restores it,
  holding playback until a host presses Play.

The port should copy the Rust tests as Go table tests, so that both
implementations agree on the same scenarios.

This rule works with the feeding approach of section 2: the queue manager's
queue is the KaraokeMachine queue, and the unit only ever holds its head.

## 5. Shape of the app

- **Server only.** People use KM Remote (or another remote) to queue. The queue
  manager itself offers only a configuration page: the unit to use, the
  fairness order and limits, access codes, the name it announces, foreign-entry
  policy, and a status view.
- **Library reuse.** It uses `protocol`, `client` and `catalog` unchanged. New
  packages would hold the fair queue, the unit feeder and the KaraokeMachine API.
  They must not depend on platform code, and the repository's other rules (RakNet
  fork `replace`, clean session close) apply.
- **It listens on the LAN.** This app exists to be reached by other phones, so
  the CK Karaoke rule "bind to loopback" cannot apply to its API port. The
  KaraokeMachine access codes (`/login`, bearer tokens, levels) are then the
  protection and should be implemented, not stubbed. The configuration page
  should stay on loopback.
- **Where it lives.** A separate command and port directory in this repository
  keeps the shared library in one place. A separate repository is also possible;
  it would need the same `replace` directive.

## 6. Platforms

### Desktop (Windows, macOS, Linux)

A tray icon with Start, Stop, "Open configuration" and Quit, and the
configuration page in the system browser. Wails v2, used by `ports/desktop`, has
no tray support, so the queue manager would use a tray library such as
`fyne.io/systray`. That library needs cgo on macOS and Linux, which affects the
CI build matrix. Starting at login is per-platform work (a registry key, a
LaunchAgent, an autostart file). The desktop firewall prompt on first listen
should be expected on Windows and macOS.

### Android

Viable, with more open questions than the desktop. The existing pattern (Go
binary as a native library, a foreground service, a web view) carries over, but
a server that runs all evening differs from the current app:

- **Foreground service type.** Android 14 and later require a declared type.
  `dataSync` is limited to 6 hours a day on Android 15; `specialUse` fits but
  needs a justification for Google Play. The current ten-minute background
  countdown must not apply.
- **Wi-Fi and CPU sleep.** A `WifiLock` and a partial wake lock keep the server
  reachable with the screen off. Battery optimisation exemptions vary by
  manufacturer.
- **mDNS.** Multicast needs a `MulticastLock`. Registering the service through
  `NsdManager` from the Kotlin side is simpler than multicast from the Go child
  process.
- **Port 8177.** Unprivileged, so it can be bound; the iOS remote needs exactly
  this port.
- **Phone as host.** The phone must stay on the same Wi-Fi as the unit and the
  remotes, and on power.

### iOS

Not recommended as a host. iOS suspends background apps within minutes, which
rules out an all-evening server.

## Blockers and open questions

| # | Item | Severity | How to resolve |
|---|---|---|---|
| 1 | The unit cannot reorder; the design depends on feeding one song ahead. How does the unit behave at the end of a song when its queue holds one entry, and when the queue is empty? | High | Test against a unit with `AddOrder`, `DelOrder` and `NextSong`, which are all in the "used" command table. |
| 2 | Other clients and the unit's own remote can still queue songs. | Medium (cannot be removed) | Adopt or re-route foreign entries; document it. |
| 3 | No pause flag in the status. | Medium | Infer from the position; test pause, stop and idle. |
| 4 | Do all song ids fit 1 to 9,999,999, and does KM Remote accept them with one synthetic package? | Medium | Check `max(SongIndex)` on real catalogues; run KM Remote against a stub. |
| 5 | No song durations, so no wait estimates. | Low | Send 0; optionally learn durations from playback. |
| 6 | Melody state is not reported. | Low | Track locally or report melody unavailable. |
| 7 | Restart has no command. | Low | Test `PlayEx` with the current id; otherwise refuse. |
| 8 | Per-singer names on the unit's screen need a session per singer. | Low | Test short sessions per `AddOrder`; otherwise use one name. |
| 9 | The API port must be on the LAN, unlike every current CK Karaoke port. | Medium (security) | Implement access codes; keep configuration on loopback. |
| 10 | KaraokeMachine's API may change. | Low | Pin a version; add contract tests from recorded responses. |
| 11 | Android: service type, Play policy, sleep and multicast. | Medium | Prototype on the Galaxy S23 after the desktop version works. |

None of these is a blocker to starting. Items 1, 3, 4 and 7 can be settled in
one session against a real unit with a small test program, before any API work.

## Suggested order of work

1. A test program against a real unit for items 1, 3, 4 and 7.
2. The fair queue as a Go package, ported with the Rust tests.
3. The unit feeder on top of `client`.
4. The KaraokeMachine API, checked against recorded responses, then against KM
   Remote on desktop, Android and iOS.
5. The desktop tray app.
6. The Android host.
