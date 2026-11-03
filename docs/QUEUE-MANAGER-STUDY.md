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

The questions that could change the design were tested against a real unit
(see [Results from a real unit](#results-from-a-real-unit)). Feeding the unit
one song ahead works without gaps, the song ids fit KaraokeMachine's numbers
unchanged, and restart and pause can be handled.

There is one hard limit: **the queue manager cannot stop other clients from
queueing on the unit directly.** It can only notice their entries and adopt them.

## 1. What the unit allows

Facts from [PROTOCOL.md](PROTOCOL.md) that shape the design:

- **No reordering.** `UpOrder` and `DownOrder` are echoed but change nothing
  (section 6.4). The unit can add at the end (`AddOrder`), add at the front
  (`FirstOrder`), remove by song id (`DelOrder`) and play a song now (`PlayEx`).
- **Removal is by song id**, not by entry. This is unambiguous, because the
  unit silently refuses a song that is already in its queue.
- **The queue is visible.** The unit pushes it whenever it changes, ten entries
  per frame (section 7.2). Each entry names the session that queued it.
- **Attribution comes from the session name** (section 3.4). One session is one
  name.
- **Sessions are cheap.** Connect, one command and close takes about 250 ms. At
  least five sessions with distinct ids work at once (section 3.2).
- **Other clients stay possible.** The unit accepts several sessions, and its
  own infrared remote and on-screen menus still work. Nothing locks them out.
- **Playback status** arrives about every 2 seconds: position, current and
  next title, current song id, key, tempo, volumes (section 7.1). There is no
  pause flag and no song duration.
- **The catalogue has no durations.** `TableSong` has number, title, artist and
  language only (section 8.3).

## 2. Feeding the unit

The queue manager keeps the real queue in its own memory. The unit's queue is
only a short buffer.

**Approach: keep one song ahead.** While a song plays, the unit's queue holds
exactly the next song from the queue manager's order. When the next song
starts, the unit pushes an empty queue at once; the queue manager then sends
`AddOrder` for the following one. When the order changes, only the single
buffered entry needs a `DelOrder` and a new `AddOrder`.

- The real unit starts the buffered song by itself when a song ends, within one
  status interval (section [Results](#results-from-a-real-unit)).
- Reordering, fairness and removal cost at most two commands.
- The unit's own screen still shows the correct "next song" (status field 6).
- When the unit is idle or stopped, `AddOrder` starts the song immediately. So
  the queue manager adds nothing while a host holds playback, and the first
  song of an evening starts as soon as it is queued.
- To show the singer's name on the unit's screen, the queue manager queues each
  song through a short session named after the singer, with one identity per
  singer. This works and takes 0.3 to 1 second per song. A single long session
  for status and control stays open beside it.

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
| Song number: a decimal string from 1 to 9,999,999 | `SongIndex`, unique, 1001 to 20440 on the tested unit | Use the `SongIndex` as the number unchanged. KaraokeMachine reads a number as `bank × 1000 + slot`, but it accepts any number in range and KM Remote never splits it. Report one synthetic package. |
| `kind`: `midi`, `video`, `cdg`, `ultrastar` or `lrc` | nothing | Send `midi`; an unknown kind also maps to `Unknown` without error. |
| `duration_ms` (required) | nothing | Send `0` until known. The last position before a song ends gives its length, so the queue manager can store each song's duration after it plays once and fill `ahead_ms` from that. |
| `artist`, `language` | `SingerName`, `LanType` via `TableLan` | Direct. |
| `transport`: `idle`, `playing`, `paused`, `stopped` | no pause flag | Infer: an empty current title is `idle`; a position that has not moved between two status frames is `paused`; otherwise `playing`. The queue manager also knows when it sent `Pause` itself. The song id field is not usable for this: it reads `0` from a pause until the next song. |
| `position_ms` | status field 2 | Direct. |
| `transpose` (absolute, ±6) | `SetKey` takes a delta; status field 17 reports the absolute key | Send the difference from the reported key. |
| `tempo_ratio` (0.75 to 1.25) | `SetTempo` takes a delta in steps of 1000/30; status field 18 | Convert ratio to steps and send the difference. |
| `music_volume` (0.0 to 1.0) | `SetVolume`, 0 to 100 on a 13-step grid | Scale and snap. |
| `melody_enabled` | `SetMelody` toggles; no status reports it | Track it locally. It drifts if anyone else toggles it. Or report melody as unavailable. |
| `transport/restart` | no command | `PlayEx` with the current song id restarts it from the start. The queue manager must remember the id, because a pause clears the id field. |
| `transport/skip` | `NextSong` | `NextSong` starts the buffered song. `Skip` has no visible effect on the tested unit. With nothing buffered, the queue manager sends `PlayEx` with its next song instead. |
| `transport/play`, `pause`, `stop` | `Pause` (toggle), `Stop` | Play and pause need the inferred state to decide whether to toggle. After `Stop`, `NextSong` does nothing with an empty unit queue, so play starts the next song with `PlayEx`. |

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
| 1 | The unit cannot reorder; the design depends on feeding one song ahead. | Resolved | Tested: the buffered song starts by itself at the end of a song. |
| 2 | Other clients and the unit's own remote can still queue songs. | Medium (cannot be removed) | Adopt or re-route foreign entries; document it. |
| 3 | No pause flag in the status. | Low | Infer from a stalled position and from the queue manager's own commands. |
| 4 | Do all song ids fit KaraokeMachine's numbers? | Resolved | Tested: 1001 to 20440, unique; KaraokeMachine accepts any number from 1 to 9,999,999. Not yet run against KM Remote itself. |
| 5 | No song durations, so no wait estimates at first. | Low | Learn each duration from playback. |
| 6 | Melody state is not reported. | Low | Track locally or report melody unavailable. |
| 7 | Restart has no command. | Resolved | Tested: `PlayEx` with the current id restarts the song. |
| 8 | Per-singer names on the unit's screen need a session per singer. | Resolved | Tested: a short session per song works. |
| 9 | The API port must be on the LAN, unlike every current CK Karaoke port. | Medium (security) | Implement access codes; keep configuration on loopback. |
| 10 | KaraokeMachine's API may change. | Low | Pin a version; add contract tests from recorded responses. |
| 11 | Android: service type, Play policy, sleep and multicast. | Medium | Prototype on the Galaxy S23 after the desktop version works. |
| 12 | The tests used one unit model. | Low | Repeat the probe on other models when available. |

None of the open items blocks starting.

## Results from a real unit

Tested on 2026-11-03 against a RafKara `MK8513-BR-B`, database id
`d91c983b52ce486f24c53af1430f21e0`, catalogue of 11,999 songs, with
`internal/cmd/qmprobe`. That program runs a list of steps and logs every
status and queue push.

| Test | Result |
|---|---|
| `AddOrder` while idle (nothing played since power-on) | The song starts at once and does not appear in the queue. |
| `AddOrder` after `Stop` or after the last song ended | The same: the song starts at once. |
| End of a song with one song queued | The queued song starts within one status interval (2 s). The unit pushes an empty queue as it starts. |
| End of a song with an empty queue | The unit stops: current title empty, position 0, song id still the last song's. Nothing starts. |
| `NextSong` with a song queued | The queued song starts within 2 s. |
| `NextSong` with an empty queue, after `Stop` | Nothing happens. |
| `Skip` during a song (at 3 s and at 90 s) | No visible effect: same song, position keeps moving. |
| `Pause`, then `Pause` again | The position freezes, then moves on. While paused the song id field reads `0`, and it stays `0` after resuming until another song starts. Titles are unchanged. |
| `PlayEx` with the current song id | The song restarts from position 0. |
| `Stop` | Current title empty, position 0, song id unchanged. |
| `FirstOrder` with one song queued | The new song goes in front of it. |
| `DelOrder` | Removes the entry; the rest keep their order. |
| `AddOrder` of a song already in the queue, from another session | Silently refused: echoed, but no entry appears. |
| Short session named after a singer, one `AddOrder`, close | Works; the entry shows that name. Each takes 0.3 to 1 s. |
| `Queryorder` | The reply arrives with the next status push, up to 2 s later. |
| Status interval | About 2 s. |
| Song id range | 1001 to 20440, no duplicates. |
| First status after a song change | The position field holds a large negative number for one frame. Ignore it. |
| First status after `NextSong` | New title, previous song's id. The next status has the new id. |
| First status of a song added while stopped | New title, previous song's id, no negative position. The next status has the new id. |
| `PlayEx` of the current song after a pause | Restarts it; the song id field stays `0`. |

## Suggested order of work

1. The fair queue as a Go package, ported with the Rust tests.
2. The unit feeder on top of `client`.
3. The KaraokeMachine API, checked against recorded responses, then against KM
   Remote on desktop, Android and iOS.
4. The desktop tray app.
5. The Android host.
