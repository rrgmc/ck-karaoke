package remote

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/rrgmc/ck-karaoke/queue/kmapi/wire"
	"github.com/rrgmc/ck-karaoke/queue/kmclient"
	"github.com/rrgmc/ck-karaoke/webui"
)

var _ webui.Singer = (*Backend)(nil)

// seat is this phone's singer at one queue manager, for one evening.
type seat struct {
	Evening string `json:"evening"`
	Singer  uint64 `json:"singer"`
	Token   string `json:"token"`
}

// singerFile is what UseSingerFile keeps: a name for this install, the same
// on every registration so the queue manager knows the phone again, and the
// seat at each queue manager by its id.
type singerFile struct {
	Device string          `json:"device"`
	Seats  map[string]seat `json:"seats"`
}

// UseSingerFile keeps this phone's singer in path, so it keeps its name and
// its requests across restarts within an evening. Call it before Run.
func (b *Backend) UseSingerFile(path string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.singerFile = path
}

func (b *Backend) loadSingers() singerFile {
	b.mu.Lock()
	path := b.singerFile
	b.mu.Unlock()
	f := singerFile{Seats: map[string]seat{}}
	if path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(raw, &f)
		}
	}
	if f.Seats == nil {
		f.Seats = map[string]seat{}
	}
	if f.Device == "" {
		var buf [8]byte
		_, _ = rand.Read(buf[:])
		f.Device = hex.EncodeToString(buf[:])
		b.saveSingers(f)
	}
	return f
}

func (b *Backend) saveSingers(f singerFile) {
	b.mu.Lock()
	path := b.singerFile
	b.mu.Unlock()
	if path == "" {
		return
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		b.log.Warn("cannot keep the singer", "file", path, "error", err)
		return
	}
	_, werr := tmp.Write(data)
	if err := errors.Join(werr, tmp.Close()); err != nil {
		os.Remove(tmp.Name())
		b.log.Warn("cannot keep the singer", "file", path, "error", err)
		return
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		b.log.Warn("cannot keep the singer", "file", path, "error", err)
	}
}

// mySeat returns this phone's seat for tonight's evening and the evening's
// singers. A seat from an earlier evening, or one the host removed, does not
// count. With create, a missing seat is registered, named name if not empty.
func (b *Backend) mySeat(ctx context.Context, create bool, name string) (seat, *wire.Singer, error) {
	b.identify(ctx)
	b.mu.Lock()
	id := b.id
	b.mu.Unlock()
	if id == "" {
		return seat{}, nil, webui.ErrQueueManager
	}
	singers, err := b.client.Singers(ctx)
	if err != nil {
		return seat{}, nil, wrap(err)
	}
	f := b.loadSingers()
	if s, ok := f.Seats[id]; ok && s.Evening == singers.Evening {
		for i := range singers.Singers {
			if singers.Singers[i].ID == s.Singer {
				return s, &singers.Singers[i], nil
			}
		}
	}
	if !create {
		return seat{}, nil, nil
	}
	issued, err := b.client.RegisterSinger(ctx, f.Device, name)
	if err != nil {
		return seat{}, nil, wrap(err)
	}
	s := seat{Evening: issued.Evening, Singer: issued.Singer.ID, Token: issued.Token}
	f.Seats[id] = s
	b.saveSingers(f)
	return s, &issued.Singer, nil
}

// request asks for a song as this phone's singer, taking a seat if needed.
func (b *Backend) request(ctx context.Context, song int) error {
	s, _, err := b.mySeat(ctx, true, "")
	if err != nil {
		return err
	}
	_, err = b.client.Request(ctx, s.Singer, s.Token, song)
	return wrap(err)
}

// Me describes this phone's singer and their requests tonight.
func (b *Backend) Me(ctx context.Context) webui.Me {
	var me webui.Me
	rs, err := b.client.Requests(ctx)
	if err != nil {
		me.Err = wrap(err)
		return me
	}
	me.Accepting = rs.Accepting
	s, singer, err := b.mySeat(ctx, false, "")
	if err != nil {
		me.Err = err
		return me
	}
	if singer == nil {
		return me
	}
	if singer.Name != nil {
		me.Name = *singer.Name
	}
	for _, r := range rs.Requests {
		if r.Singer != s.Singer {
			continue
		}
		mr := webui.MyRequest{
			ID:     strconv.FormatUint(r.ID, 10),
			SongID: strconv.FormatUint(uint64(r.Number), 10),
			State:  r.State,
		}
		if r.Position != nil {
			mr.Position = *r.Position + 1
		}
		me.Requests = append(me.Requests, mr)
	}
	return me
}

// SetName names this phone's singer, taking a seat if needed.
func (b *Backend) SetName(ctx context.Context, name string) error {
	s, singer, err := b.mySeat(ctx, true, name)
	if err != nil {
		return err
	}
	if singer != nil && singer.Name != nil && *singer.Name == name {
		return nil
	}
	_, err = b.client.RenameSinger(ctx, s.Singer, s.Token, name)
	return wrap(err)
}

// Withdraw takes back one of this phone's requests.
func (b *Backend) Withdraw(ctx context.Context, requestID string) error {
	id, err := strconv.ParseUint(requestID, 10, 64)
	if err != nil {
		return fmt.Errorf("bad request id %q", requestID)
	}
	s, singer, err := b.mySeat(ctx, false, "")
	if err != nil {
		return err
	}
	if singer == nil {
		return fmt.Errorf("%w: no singer on this phone tonight", kmclient.ErrUnauthorized)
	}
	_, err = b.client.Withdraw(ctx, id, s.Token)
	return wrap(err)
}
