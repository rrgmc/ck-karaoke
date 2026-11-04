# Queue manager

The queue manager is a server that holds the only session with the karaoke
unit. It keeps the song queue, with an optional fair order, and feeds the unit
one song ahead. It serves KaraokeMachine's HTTP API, so KM Remote connects to
it unchanged. Its own page is for configuration and for the operator; people
queue songs with KM Remote.

The design and the measurements behind it are in
[QUEUE-MANAGER-STUDY.md](QUEUE-MANAGER-STUDY.md).

## Running it

```bash
go run ./cmd/ck-queue-manager -v
```

- The API listens on port 8177 on every interface. The iOS remote finds a
  machine only on that port.
- The configuration and operator page listens on `127.0.0.1:8176` only.
- The data directory defaults to `CK Karaoke Queue Manager` in the user
  configuration directory. It holds `song.db` (downloaded from the unit on a
  first start), `settings.json`, the learned song lengths, the server's id and
  the token secret, and, when kept, the evening.
- `ports/queue-desktop` is the same server in the system tray.

## Layers

| Package | Role |
|---|---|
| `queue/fair` | KaraokeMachine's evening, ported: singers, requests, Arrival and Turns order, operator actions, the JSON store. Pure state. |
| `queue/feeder` | Plays the evening on the unit: one long session for status and control, a short session per song named after its singer, one song buffered on the unit. |
| `queue/kmapi` | KaraokeMachine's `/api/v1`, the WebSocket events and the `_karaokemachine._tcp` mDNS announcement. |
| `queue/kmapi/wire` | The API's JSON types and access levels, shared by the server and its clients. |
| `queue/kmclient` | A client of the API and its event stream, for CK Queue Remote. Its tests run against `kmapi.Server`. |
| `queue/kmapi/kmapitest` | Serves the API with a fake unit and catalogue, for testing clients against the real server. |
| `queue/remote` | CK Queue Remote's backend for the web UI: a `webui.Backend` over `kmclient`, the mDNS search for a queue manager, and the song database sync. `server.Run` uses it with `Config.Backend` set to `queue`; `ck-karaoke-server -backend queue` runs it. |
| `queue/manager` | `Run(ctx, Config)`: finds the unit, opens the catalogue, wires the three together, serves both ports. |
| `cmd/ck-queue-manager` | The command line. |
| `ports/queue-desktop` | The tray app; its own module. |

## Rules

- **The evening is the queue; the unit holds one song.** Every change goes
  through `fair.Evening` behind `feeder.Room`. The feeder never decides the
  order; it only mirrors the head of the evening onto the unit.
- **The unit's song id is not trusted on its own.** It reads 0 after a pause,
  and the first status after `NextSong`, or after a song added while stopped,
  carries the previous id. The feeder accepts an id only when it changed or
  comes with that song's title (PROTOCOL.md, section 7.1).
- **The unit is found by model and database id**, never by its DHCP address.
  The last address is tried first.
- **The API is on the LAN, and the codes protect it.** Access levels, codes and
  bearer tokens follow KaraokeMachine. The room level defaults to `request`. A
  token is an HMAC over its level, expiry and that level's code, so changing a
  code ends its tokens. The configuration page is loopback only and acts as the
  host without a code.
- **The JSON shapes are KaraokeMachine's.** `queue/kmapi/testdata/golden` pins
  every response and event, and `queue/kmapi/testdata/kmcheck` reads them with
  KaraokeMachine's own Rust types. A missing field or a wrong type breaks KM
  Remote with no error, so change the goldens only with that check.
- **Run returns after the unit session has closed**, as `server.Run` does.

## Mapping the unit onto the API

- Song numbers are the unit's `SongIndex`, as strings, in one package `ck`.
- `kind` is `midi`. `melody_available` is false.
- `duration_ms` is 0 until the song has played to its end once; learned lengths
  are kept, and each one changes `catalog_version`.
- A song the evening did not hold (played from the unit's own remote) is
  reported with `origin.kind = "file"`.
- `/discover` sends `features` with `demo`, `song_book` and `web_control`
  false, so KM Remote leaves those controls out. The state carries
  `player_online: false` while the unit is not connected, and a command that
  needs the unit answers 409 `player_offline`. KM Remote reads these from
  KaraokeMachine's [PR 198](https://github.com/rrgmc/karaokemachine/pull/198)
  on; an older KM Remote ignores them.
- Songs queued on the unit by anyone else are adopted as songs with no singer,
  at the place the unit plays them, or, with the reroute policy, taken off the
  unit and added to the end of the evening.

## CK's own routes

Two routes under `/api/v1/ck/` are not part of KaraokeMachine's API. They let
CK Queue Remote browse the same song database that CK Karaoke downloads from
the unit. Both are open at the `view` level, like every `GET`.

| Route | Answer |
|---|---|
| `GET /api/v1/ck/catalog` | `wire.CKCatalog`: the unit's model and database id, the database version, the song count, and the file's size and SHA-256. |
| `GET /api/v1/ck/catalog/song.db` | The unit's `song.db` as it is, with the SHA-256 as its `ETag`. A request whose `If-None-Match` matches gets 304. |

A KaraokeMachine answers both with 404 `unknown_endpoint`, so a remote that
gets a `CKCatalog` knows it has found a CK Queue Manager. `/discover` cannot
tell them apart: both report `app: "karaokemachine"`.
