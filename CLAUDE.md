# CLAUDE.md

Guidance for Claude Code when working in this repository.

## What this is

CK Karaoke: a remote control for compatible karaoke units (Videoke VSK, CooKara,
RafKara) over RakNet. A reusable Go library, one web UI, and native wrappers for
Android, iOS and the desktop. Read [docs/BUILDING.md](docs/BUILDING.md) for the layout,
[docs/PROTOCOL.md](docs/PROTOCOL.md) for the wire format and
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the design rules.

## Commands

`Taskfile.yml` wraps the common commands (`task --list`; `task check` runs
what CI checks for the Go code). The plain commands:

```bash
go build ./... && go vet ./... && go test ./...
gofmt -l .
go run ./cmd/ck-karaoke-server -v            # debug server on :8080
go run ./internal/cmd/genicon                # re-render icon rasters
```

```bash
./ports/android/build.sh                     # needs JAVA_HOME (JDK 17-21) and Gradle 8.13+
./ports/ios/build.sh                         # macOS only
(cd ports/desktop && wails build)            # desktop; separate Go module
(cd ports/desktop && go vet -tags desktop,production .)
```

The version is in `version/VERSION`, the one source every build reads. A
release tag must match it, so bump the file before tagging.

All tests are hermetic. The catalog tests run against a seeded fixture
(`catalog/fixture_test.go`); the protocol tests replay reference encodings in
`protocol/testdata/golden_frames.txt`, which guard against silent wire-format
regressions because the device tolerates many malformed frames. Write new tests
with `stretchr/testify` (`require` for preconditions, `assert` for checks).
Older tests in `protocol`, `catalog` and `favorites` use the standard library;
leave them as they are.

## Worktrees

**Make every change in its own git worktree, never in the main checkout.**
Several sessions work in this repository at once, and a branch switched or a
file edited in the shared checkout lands under whoever else is working there.

- **Start** each change with `EnterWorktree`, before the first edit. It creates
  `.claude/worktrees/<name>` on a new branch from `origin/master`. Without that
  tool, run
  `git worktree add -b <branch> .claude/worktrees/<name> origin/master`.
- **Keep the main checkout on `master` with a clean tree.** Do not switch its
  branch, edit files in it, or build in it.
- **Bring the branch up to date** before opening a pull request: `git fetch`
  then `git rebase origin/master`.
- **Merge** through a pull request. GitHub protects `master`: it refuses
  direct pushes, including from admins. Push the branch, open the pull
  request with `gh pr create`, and merge it with
  `gh pr merge --rebase --delete-branch`, which keeps the history linear. The
  main checkout catches up with `git pull`.
- **Clean up** after the merge: `ExitWorktree` with `remove`, or
  `git worktree remove .claude/worktrees/<name>` and `git branch -D <branch>`.
  The rebase merge gives the commits new hashes, so `git branch -d` reports
  the branch as unmerged.

Each worktree has its own build outputs (`bin/`, `data/`, the Android and iOS
build directories), so a port has to be built again inside a new worktree.

## Pull requests

Opening a pull request needs no approval. Ask before merging one, and before
tagging. Keep CI green.

## Port status

| Port | Built | Run |
|---|---|---|
| Windows desktop | CI and locally | against a real unit |
| Android | CI and locally | on a Galaxy S23 against a real unit |
| macOS desktop | CI and locally | against a real unit |
| iOS | CI (simulator, unsigned) and locally | on an iPad (6th generation) and the simulator against a real unit |
| Linux desktop | CI | not yet |

Checked so far on macOS and iOS: the local network prompt, discovery (also when
the prompt is answered late), the catalogue download and the device session.
Still to check:

- iOS: the QR scanner, and backup save through the share sheet. To install on
  a device, sign the release IPA as `ports/ios/README.md` describes, or run
  from Xcode after `./ports/ios/build.sh`.
- macOS desktop: the backup Save dialog.

## Rules

- **The RakNet fork is mandatory.** The device speaks protocol version 6;
  `go.mod` replaces `sandertv/go-raknet` with `RangelReale/go-raknet`. Do not
  remove the `replace`. `ports/desktop/go.mod` repeats it, and so must any other
  module that imports this library.
- **State protocol behaviour as fact**, in code comments and in
  `docs/PROTOCOL.md`: no provenance and no history.
- **No song-popularity or ranking lists.**
- **The library must not depend on platform code.** Wails lives only in
  `ports/desktop`, which is its own module.
- **`server.Run` assembles the server.** Every port goes through it; keep
  `main` packages thin.
- **Close sessions cleanly.** Keep the `SIGPIPE` handling in
  `cmd/ck-karaoke-server`, keep `server.Run` waiting for the device session, and
  never return `START_STICKY` on Android.
- **Favorites sharing and backup only add.** `favorites.DB.AddSongs` and
  `EnsureFolder` are the only writes they use. Do not add delete paths to them.
- **Keep the QR and XML formats compatible**; existing codes and backup files
  must keep importing.
- **Bind the app ports to loopback.** The server is unauthenticated.
- **Commands that change stored state or reach external services** (YouTube,
  recording, UploadPic, AddBarrage, Cheer, or any command not in the "used"
  table of `protocol/command.go`): confirm with the user before sending one to a
  real device.

## Working with a device

Identify a unit by its model and database id (`client.Ping` prints both), not
by address or RakNet GUID; the address is DHCP and the GUID is not stable.

To exercise the command path without changing anything, send a value the device
already reports, for example the current microphone level.

Wait for the session echo before sending commands; never sleep instead.

## Verifying changes

1. `go build ./... && go vet ./... && go test ./...`, and `gofmt -l .` prints
   nothing.
2. For `server/`, `client/` or `webui/` changes, also run the debug server
   against a unit and check that the `listening <addr>` line appears (the Android
   wrapper parses it).
3. Cross-compile the phone builds, which surface desktop-only assumptions:
   `GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/ck-karaoke-server`.
4. Vet the desktop module from `ports/desktop` with `-tags desktop,production`.
