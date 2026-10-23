# Device protocol

This document specifies the LAN protocol that CK Karaoke uses to control
compatible karaoke units: the Videoke VSK family and other devices sold under
the CooKara and RafKara names. The reference implementation is the `protocol`
package (wire format) and the `client` package (session and commands).

Byte values are hexadecimal. "BE" means big-endian.

## Contents

1. [Transport](#1-transport)
2. [Discovery](#2-discovery)
3. [Sessions](#3-sessions)
4. [Control frames](#4-control-frames)
5. [Field lists](#5-field-lists)
6. [Commands](#6-commands)
7. [Status frames](#7-status-frames)
8. [The song database](#8-the-song-database)
9. [Fields with no known meaning](#9-fields-with-no-known-meaning)

---

## 1. Transport

| Property | Value |
|---|---|
| Transport | UDP, port **35379** |
| Framing | RakNet, **protocol version 6** |
| Connection password | `CooKaraSystem` |

The device rejects every other RakNet protocol version with
`ID_INCOMPATIBLE_PROTOCOL_VERSION`. Common RakNet libraries for Go hardcode
version 11, so this module pins a fork of `sandertv/go-raknet` that accepts 6
(see `go.mod`).

Every application message described below is one RakNet user packet, sent
reliable and ordered.

## 2. Discovery

### 2.1 Unconnected ping

A client finds a unit by sending RakNet's `ID_UNCONNECTED_PING` (`01`) and
reading the `ID_UNCONNECTED_PONG` (`1C`) reply. This needs no session and works
while other clients are connected.

The pong carries its payload **raw, to the end of the packet, with no `uint16`
length prefix**. A parser that expects the prefix consumes the first two payload
bytes as a bogus length. A correct parse of a typical pong returns 90
bytes beginning `pxP24QU5`.

### 2.2 The device information payload

The payload is base64 text (with MIME line breaks) over `DES/CBC/NoPadding`
ciphertext, so its decoded length is always a multiple of 8.

```
key  8F 66 0F 72 D0 D4 D0 8F
iv   7C BA 70 F5 77 4E 49 F8
```

The plain text is recovered by running the cipher in the **encrypt** direction.
The decrypt direction returns noise. Trailing zero bytes are padding.

The plain text is a slash-separated record:

```
RafKara//MK8513-BR-B/1/d91c983b52ce486f24c53af1430f21e0/1/M/A//
```

| Index | Meaning | Example |
|---|---|---|
| 0 | product family | `RafKara` |
| 2 | hardware model | `MK8513-BR-B` |
| 4 | song database id | `d91c983b52ce486f24c53af1430f21e0` |
| 1, 3, 5, 6, 7 | unknown | `""`, `1`, `1`, `M`, `A` |

The database id is the same value as status field 13 (section 7.1) and the last
directory of the database path (section 8.1). A client can therefore match a
stored catalogue to a unit without connecting. See `protocol.DecodeDeviceInfo`.

### 2.3 Finding units on a network

**Broadcast.** Send the ping to the subnet's directed broadcast address. The
socket needs `SO_BROADCAST` set explicitly, and the address must come from the
interface's real netmask: on a /22 such as `192.168.68.0/22` it is
`192.168.71.255`, not `192.168.68.255`. Units answer within milliseconds. See
`client.Discover`.

**Unicast sweep.** Where broadcast is not available (iOS grants it only with a
special entitlement), ping every host address on each local IPv4 subnet in
turn, nearest to the client's own address first, in paced batches. See
`client.Sweep`.

The RakNet server GUID a unit reports is not a stable identifier and can change.
Identify a unit by its model and database id instead.

## 3. Sessions

### 3.1 Opening a session

After the RakNet connection is up, the client sends a **session frame**
(`50/00`, section 4) and waits for the device to echo it. Until the session
frame is echoed the device ignores commands and pushes no status. After the echo
it is fully responsive.

| Step | Typical time |
|---|---|
| Session echo | 60 to 100 ms |
| Connect, one command, close | about 250 ms |

A command sent immediately after the session frame, without waiting for the
echo, is dropped. Wait for the echo rather than sleeping.

### 3.2 Identity

A client identifies itself with a **client id**, a random version 4 UUID written
as 36 lower-case characters, and a **name**. Any UUID is accepted; there is
nothing to register. Each running program should generate its own UUID.

Opening a second session with a UUID that is already connected **silently
displaces** the earlier session. RakNet does not notice, and the displaced
client's commands then time out. The only reliable sign of displacement is that
status frames stop arriving (section 7). A client that sees no status for 15
seconds should treat the session as dead and reconnect.

At least five simultaneous sessions with distinct UUIDs work.

### 3.3 The session checksum

The four header bytes that carry a command's tag (section 4.1) carry a
**checksum** in a session frame:

```
body     = 0A 24 <client id, 36 bytes> 12 <name length, 1 byte> <name>
checksum = adler32(body), BE
```

The device verifies this checksum. A session frame with a wrong checksum is
ignored, which looks exactly like a busy device. The name is part of the body,
so changing the name changes the checksum. See `protocol.SessionChecksum`.

### 3.4 The client name

The name is free text of up to 255 bytes. The device shows it beside every song
the session queues (section 6.5), so it should be short and printable. CK Karaoke
keeps printable ASCII only, collapses whitespace, caps it at 20 characters, and
falls back to `NE2211` when empty. See `protocol.CleanName`.

The device takes the queuer's name from the **session**, not from the command
frame. Every song queued through one session carries that session's name.

### 3.5 Closing a session

Close the RakNet connection and wait until `DISCONNECTION_NOTIFICATION` has been
sent before the process exits. A process that exits first leaves the device
holding the connection slot until its own timeout. Some RakNet libraries send the
notification from a timer about a second after `Close` returns; `client.Close`
waits for it.

A unit that answers an unconnected ping but never echoes the session frame is
reachable but refusing the session: all slots are taken, or the session frame is
malformed. `client.ErrBusy` reports this case.

## 4. Control frames

### 4.1 Header

```
Class(1) Kind(1) 00 00 00 00 Seq(4, BE) FF FF FF FF [00 00 00 00] Tag(4)
```

| Field | Value |
|---|---|
| Class | always `50` |
| Kind | `00` session, `02` command, `03` status |
| Seq | sender's sequence number |
| `00 00 00 00` in brackets | present only in frames **from the device** |
| Tag | command tag (section 4.4), session checksum (section 3.3), or unknown (status) |

The same bytes decode differently depending on direction, so a parser must know
which way a frame travelled.

### 4.2 Session and command frames (kinds 00 and 02)

After the header comes one body (client to device) or two (device to client):

```
Len(4, BE) 0A 24 <client id, 36> 12 <name length> <name> [<field list>]
```

`Len` counts every byte after itself, starting at `0A`. The body prefix is laid
out like a protocol-buffer message: `0A` is field 1 (length-delimited) with
length `24` (36), and `12` is field 2 (length-delimited) followed by its length.

A **session frame** has no field list. A **command frame** has one (section 5).

**Echoes.** The device answers every session and command frame by echoing it
with the same `Seq`. The echo carries two bodies, each with its own `Len` and
`0A` prefix:

1. the request as received;
2. either the request again, with field marker `B`, or a **response** field
   list with marker `J` carrying data (section 6.3).

A session echo's second body ends with three extra bytes, `A0 01 01`.

### 4.3 Status frames (kind 03)

Status frames come only from the device. After the header:

```
00 00 00 00 00 Len(4, BE) <field list>
```

A parser tells the two body shapes apart by the five bytes after the header:
four zero bytes and `00` mean status; a length followed by `0A` means a command.

The status frame's tag has no known meaning and clients ignore it.

### 4.4 Command tags

Command frames carry a four-byte tag. Read as little-endian ASCII, most tags are
a fragment of the command's name.

| Tag | Bytes | Commands |
|---|---|---|
| `_Ask` | `6B 73 41 5F` | AskDB |
| `_Add` | `64 64 41 5F` | AddOrder |
| `_Fir` | `72 69 46 5F` | FirstOrder |
| `_Nex` | `78 65 4E 5F` | NextSong |
| `_Que` | `65 75 51 5F` | Queryorder, QueryVolInfo, ASKRECCOUNT, and the default |
| `_Set` | `74 65 53 5F` | SetTempo, SetVolume, SetMic1Vol, SetReverbVol, SetMelody |
| `_Swi` | `69 77 53 5F` | RepeatFlag |
| none | `00 00 00 00` | Pause |
| UTF-16 `Se` | `00 65 00 53` | Stop, SetKey, PlayEx, Skip |

The device does not verify command tags: a command with the wrong tag is still
executed. CK Karaoke sends the tags above and `_Que` for every other command.
See `protocol.TagFor`.

### 4.5 Correlation

An echo carries the request's `Seq`. Status frames run an **independent**
counter that overlaps the request numbering, so match replies on sequence number
**and** frame kind, never on the number alone.

## 5. Field lists

A field list is a run of length-prefixed strings closed by a terminator that
carries the command id:

```
<marker> <len(1)> <bytes> ... 50 <command> [<extra>]
```

| Part | Value |
|---|---|
| marker | `42` (`B`) in requests and request echoes, `4A` (`J`) in response lists |
| len | one byte; every field is text |
| terminator | `50` (`P`) followed by the command id |
| extra | one more byte, present only when the command id has its high bit set |

**The command id is the terminator**, so a frame's meaning is at its end.
`SetMelody` (`F8`) is the only command with the high bit set; its extra byte is
`06`. See `protocol.FieldList` and `protocol.ExtraFor`.

All numbers travel as decimal text: `"19535"`, `"-1"`, `"84"`.

## 6. Commands

### 6.1 Command table

Commands used by CK Karaoke:

| Id | Name | Arguments | Effect |
|---|---|---|---|
| `01` | AskDB | path, file name | start a database transfer (section 8) |
| `02` | AddOrder | song id, order token | queue a song at the end |
| `03` | DelOrder | song id | remove that song from the queue |
| `05` | Pause | | pause or resume |
| `06` | Stop | | stop playback; NextSong resumes |
| `07` | NextSong | | start the next queued song |
| `0A` | Queryorder | start, count | request the queue (section 6.4) |
| `0B` | SetKey | semitones (delta) | shift the key |
| `0C` | SetTempo | steps (delta) | shift the tempo |
| `0D` | SetVolume | level 0 to 100 | music volume |
| `0E` | SetMic1Vol | level 0 to 100 | microphone 1 volume |
| `0F` | SetMic2Vol | level 0 to 100 | microphone 2 volume |
| `11` | ResetTempo | | tempo back to normal |
| `12` | ResetKey | | key back to the original |
| `13` | QueryVolInfo | | request the mixer state |
| `15` | FirstOrder | song id, order token | queue a song at the front |
| `18` | RepeatFlag | | toggle repeat of the current song |
| `1C` | PlayEx | song id | play a song now |
| `26` | SetReverbVol | level 0 to 100 | echo / reverb |
| `2C` | Skip | | skip the current song |
| `2E` | ASKRECCOUNT | | request the number of stored recordings |
| `F8` | SetMelody | `"0"` | toggle the guide melody |

Commands the device defines that CK Karaoke does not use:

| Id | Name | Note |
|---|---|---|
| `04` | Play | |
| `10` | SetMusicvol | |
| `16` | AskRecFile | recordings |
| `17` | AskRecList | recordings |
| `1D` | UpOrder | accepted, no visible effect |
| `1E` | DownOrder | accepted, no visible effect |
| `25` | ChangeNickName | |
| `28` | SetVocalMode | |
| `2F` | AskRecList_HttpInf | recordings |
| `30` | AddBarrage | on-screen messages |
| `31` | Cheer | |
| `32` | UploadPic | |
| `65` | AddYouTubeOrder | reaches an external service |
| `66` | FirstYouTubeOrder | reaches an external service |
| `67` | TubeDownload | reaches an external service |
| `68` | GetDownloadListCount | |
| `69` | AskDownloadList | |

Several of these change stored state or reach external services. Their
arguments are not specified here.

### 6.2 Control semantics

| Commands | Argument |
|---|---|
| SetKey, SetTempo | a **delta**. Sending `-1` twice goes from 0 to -2. |
| SetVolume, SetMic1Vol, SetMic2Vol, SetReverbVol | an **absolute** level, snapped to a grid |
| SetMelody | a **toggle**. The argument is always `"0"`, and no status field reports the melody state. |

Key and tempo apply to the current song. With nothing playing, the device
accepts the command and reports no change.

**Volume grid.** Volume-style controls have thirteen positions,
`floor(step × 100 / 12)`:

```
0  8  16  25  33  41  50  58  66  75  83  91  100
```

An off-grid level snaps to the nearest position, rounding up on an exact tie:
79 becomes 83, 84 becomes 83, 60 becomes 58, 63 becomes 66. To step by one
position, send the level of the next grid position; adding a fixed amount moves
two positions or none. See `protocol.VolumeNudge`.

### 6.3 Where replies arrive

| Command | Reply |
|---|---|
| QueryVolInfo | response field list in the echo (section 6.6) |
| ASKRECCOUNT | response field list in the echo; field 0 is the count |
| Queryorder | a separate status frame with discriminator `03` (section 7.2) |
| everything else | the echo only |

### 6.4 The queue

`Queryorder` takes two arguments, the first entry to return and the number of
entries, for example `("0", "10")`. The reply is a status frame (section 7.2).
The device also pushes the queue unprompted when it changes.

`DelOrder` removes the named song and keeps the order of the others. `UpOrder`
and `DownOrder` are echoed but change nothing, so reordering is not supported.

### 6.5 Queue entries

Each queue entry has four values: the song id and three more.

```
"19535", "123456789", "30.NE2211", "1"
```

The third value names the client that queued the song, as
`<counter>.<session name>`. The counter increments per entry. The second and
fourth values have no known meaning. See `protocol.QueueEntry.QueuedBy`.

### 6.6 Mixer state

The `QueryVolInfo` reply has eleven values, for example:

```
"0", "1000", "75", "50", "50", "75", "50", "50", "50", "0", "0"
```

Value 1 is the tempo in the same per-mille units as status field 18. The other
positions are not fully mapped; status fields 14 to 18 are the reliable source
of the mixer state.

### 6.7 The order token

`AddOrder` and `FirstOrder` take a second argument, an 8-digit decimal number.
Its meaning is unknown and the device accepts any value. Conventional values lie
between 84,000,000 and 87,000,000; CK Karaoke sends `84000000 + (milliseconds
mod 3000000)`. See `protocol.OrderToken`.

## 7. Status frames

The field list of a status frame ends in a **discriminator** rather than a
command id:

| Discriminator | Content |
|---|---|
| `00` | playback status (section 7.1) |
| `03` | the queue (section 7.2) |
| `13` | mixer state, same layout as the QueryVolInfo response |

### 7.1 Playback status

The device pushes playback status about every 1.5 seconds while a session is
open. It has **nineteen** fields:

| Index | Meaning |
|---|---|
| 0, 1 | lyric lines 1 and 2 shown on screen |
| 2 | playback position, milliseconds |
| 4 | current song title |
| 6 | next song title |
| 8 | current song id |
| 9, 10 | sweep percentage for lyric lines 1 and 2 |
| 13 | song database id |
| 14 | music volume, 0 to 100 |
| 15 | microphone volume, 0 to 100 |
| 16 | echo / reverb, 0 to 100 |
| 17 | key, semitones from the original |
| 18 | tempo, per-mille duration ratio |
| 3, 5, 7, 11, 12 | unknown (section 9) |

A blank numeric field means "not applicable", not zero.

**Tempo** is a duration ratio centred on 1000, with one step equal to 1000/30.
It rises as the song slows: `SetTempo(-1)` takes it to 1034, `SetTempo(+1)` to
967. To show steps, compute `round((1000 - raw) × 30 / 1000)`. Round rather than
truncate: 967 is 0.99 of a step.

### 7.2 Queue

```
total, start, end, then (end - start) groups of: song id, v1, v2, v3
```

`total` is the length of the whole queue; `start` and `end` bound the entries
in this frame. Each group is one entry as in section 6.5.

## 8. The song database

The device holds its catalogue as a SQLite file and sends it on request.

### 8.1 Transfer

1. Send `AskDB` with two arguments: the path and the file name. The default path
   is `/storage/emulated/0/Android/data/com.multak.cookara_br_a/files/db/`
   followed by the database id (section 2.2), and the file name is `song.db`.
   Wait for the echo.
2. Send three **bare step frames**, about 200 ms apart:

   | Byte | Step | Payload |
   |---|---|---|
   | `85` | start | none |
   | `24` | continue | `00 00` |
   | `7F` | finish | none |

3. The device streams the file as a few very large **chunk frames**, each
   starting with `4B`. A 2.2 MB catalogue typically arrives as two frames.
4. The device sends no size and no terminator. Treat the transfer as finished
   when no chunk has arrived for 15 seconds.

Bare step frames sit outside the control frame format: one id byte, optionally
followed by payload. The device may also send `84` (ready) and `22` (info) step
frames, which a client can ignore.

### 8.2 Chunk frames

```
4B <header> <slice of the file>
```

The header is opaque and **its length varies**: typically 72 bytes on the first
chunk (counting the `4B`) and 75 on the rest. No field in it gives the payload
length. Stripping a fixed length therefore misaligns every later chunk and
produces a file that opens and lists songs but fails SQLite's integrity check.

The boundaries can be recovered without decoding the header:

1. The first chunk's payload begins at the SQLite magic `SQLite format 3\0`,
   which gives that chunk's header length.
2. The SQLite header gives the file's exact size: page size (offset 16, BE
   `uint16`, where 1 means 65536) times page count (offset 28, BE `uint32`).
3. The first payload's length is the device's maximum chunk size. Every later
   chunk carries `min(maximum, remaining)` bytes, and its header is whatever
   precedes them.

After reassembly, run `PRAGMA integrity_check` and read `TableSongSinger` before
trusting the file. See `catalog.Reassembler` and `catalog.Verify`.

### 8.3 Schema

The columns CK Karaoke uses:

| Table | Columns |
|---|---|
| `TableDBInfo` | `ProjectName`, `ProductID`, `DBVersion` |
| `TableLan` | `LanType`, `LanName_EG` (English language name) |
| `TableSong` | `SongIndex`, `SongName`, `SingerName`, `LanType`, `FirstWord`, `AlphaIndex` |
| `TableSinger` | `SingerIndex`, `SingerName`, `FirstWord`, `AlphaIndex`, `LanType` |
| `TableSongSinger` | `SongIndex`, `SingerIndex` |

- `TableSong.SongIndex` is the song id the commands take.
- Text is UTF-8.
- `FirstWord` is the title (or artist name) in upper case with accents and
  punctuation removed, truncated to 19 characters. Sort and search on it to
  get accent-insensitive results; SQLite's default collation sorts accented
  letters after Z.
- `AlphaIndex` is the initial-letter bucket: 1 to 26 for A to Z, 27 for a
  leading digit. It always agrees with the first character of `FirstWord`.
- `TableSong.SingerName` is a denormalised copy; `TableSongSinger` is the
  authoritative link between songs and artists.

## 9. Fields with no known meaning

These fields exist but their meaning is unknown. Clients should pass them through
or ignore them.

- Discovery record fields 1, 3, 5, 6 and 7 (section 2.2).
- Playback status fields 3, 5, 7, 11 and 12 (section 7.1). Typical values are
  `"0"`, `""`, `""`, `"0"`, `"0"`.
- The status frame tag (section 4.3).
- Queue entry values 2 and 4 (section 6.5).
- Most positions of the mixer state reply (section 6.6).
- The order token (section 6.7).
- The extra byte after `SetMelody`'s command id (section 5).
- The chunk frame header (section 8.2).
- The device does not expose performance scoring over this protocol.
