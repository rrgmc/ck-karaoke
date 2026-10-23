# Using CK Karaoke as a Go library

The Go packages in this repository can be used on their own, without the web UI
or any of the apps.

| Package | Purpose |
|---|---|
| `protocol` | wire format: frames, field lists, commands, messages |
| `client` | RakNet session to the device, discovery, database download |
| `catalog` | the device's song database: reassembly, verification, search |
| `favorites` | favorites folders (SQLite) |
| `favorites/qrshare` | one folder as a numeric QR code |
| `favorites/xmlbackup` | every folder as an XML backup file |
| `webui` | the web UI: handlers, templates, static assets, event stream |
| `server` | assembles the whole server from a `Config` |

## Example

Connect to a unit, print every status update, and queue a song by its number:

```go
c, err := client.Dial(ctx, "192.168.1.50", client.Options{})
if err != nil {
    return err
}
defer c.Close()

c.OnStatus(func(s *protocol.Status) { fmt.Println(s) })
if err := c.Reserve(ctx, "19535"); err != nil {
    return err
}
```

## The RakNet fork

The device speaks RakNet protocol version 6, which upstream
`sandertv/go-raknet` does not support. A module that imports this library must
repeat the fork `replace` from this repository's [go.mod](../go.mod), because Go
applies `replace` directives only in the main module:

```
replace github.com/sandertv/go-raknet => github.com/RangelReale/go-raknet v0.0.0-20260813174103-4ace5ce8d361
```

## Further reading

- [PROTOCOL.md](PROTOCOL.md): the device protocol.
- [ARCHITECTURE.md](ARCHITECTURE.md): how the server and the ports fit together,
  and the design rules that keep them working.
- [BUILDING.md](BUILDING.md): building the apps and running the tests.
