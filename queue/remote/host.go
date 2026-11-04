package remote

import (
	"context"
	"fmt"
	"strconv"

	"github.com/rrgmc/ck-karaoke/queue/kmapi/wire"
	"github.com/rrgmc/ck-karaoke/webui"
)

var _ webui.Host = (*Backend)(nil)

// refreshLevel reads this app's access level, so HostAllowed can answer
// without the network.
func (b *Backend) refreshLevel(ctx context.Context) {
	a, err := b.client.Access(ctx)
	if err != nil {
		return
	}
	b.mu.Lock()
	b.level = a.Access
	b.mu.Unlock()
}

// HostAllowed reports whether this app held the host level when last asked.
func (b *Backend) HostAllowed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.level >= wire.LevelHost
}

// HostView reads the requests waiting for the host and the singers.
func (b *Backend) HostView(ctx context.Context) webui.HostView {
	var v webui.HostView
	rs, err := b.client.Requests(ctx)
	if err != nil {
		v.Err = wrap(err)
		return v
	}
	singers, err := b.client.Singers(ctx)
	if err != nil {
		v.Err = wrap(err)
		return v
	}
	v.Accepting = rs.Accepting
	names := make(map[uint64]string, len(singers.Singers))
	for _, s := range singers.Singers {
		name := ""
		if s.Name != nil {
			name = *s.Name
		}
		names[s.ID] = name
		v.Singers = append(v.Singers, webui.HostSinger{ID: strconv.FormatUint(s.ID, 10), Name: name, Phone: s.Phone})
	}
	for _, r := range rs.Requests {
		if r.State != "waiting" {
			continue
		}
		v.Waiting = append(v.Waiting, webui.HostRequest{
			ID:     strconv.FormatUint(r.ID, 10),
			SongID: strconv.FormatUint(uint64(r.Number), 10),
			Singer: names[r.Singer],
		})
	}
	return v
}

// HostAction runs one of the host's actions.
func (b *Backend) HostAction(ctx context.Context, action, id, value string) error {
	var n uint64
	switch action {
	case "accept", "accept-next", "decline", "remove-singer":
		var err error
		if n, err = strconv.ParseUint(id, 10, 64); err != nil {
			return fmt.Errorf("bad id %q", id)
		}
	}
	var err error
	switch action {
	case "accept":
		_, err = b.client.Accept(ctx, n, false)
	case "accept-next":
		_, err = b.client.Accept(ctx, n, true)
	case "decline":
		_, err = b.client.Decline(ctx, n)
	case "open":
		_, err = b.client.SetAccepting(ctx, true)
	case "close":
		_, err = b.client.SetAccepting(ctx, false)
	case "add-singer":
		_, err = b.client.CreateSinger(ctx, value)
	case "remove-singer":
		_, err = b.client.RemoveSinger(ctx, n)
	case "reset":
		_, err = b.client.ResetEvening(ctx)
	default:
		return fmt.Errorf("%w: %s", webui.ErrUnsupported, action)
	}
	return wrap(err)
}
