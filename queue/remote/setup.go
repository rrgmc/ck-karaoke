package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rrgmc/ck-karaoke/queue/kmapi/wire"
	"github.com/rrgmc/ck-karaoke/queue/kmclient"
	"github.com/rrgmc/ck-karaoke/webui"
)

var _ webui.Setup = (*Backend)(nil)

// UseTokenFile keeps the access token in path, one per queue manager by its
// id, so a code entered once lasts across restarts until the token expires
// or the host changes the code. Call it before Run.
func (b *Backend) UseTokenFile(path string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tokenFile = path
}

// identify learns the queue manager's id and name, once, and picks up the
// token kept for it.
func (b *Backend) identify(ctx context.Context) {
	b.mu.Lock()
	known, path := b.id != "", b.tokenFile
	b.mu.Unlock()
	if known {
		return
	}
	d, err := b.client.Discover(ctx)
	if err != nil {
		return
	}
	b.mu.Lock()
	b.id, b.name = d.ID, d.Name
	b.mu.Unlock()
	if path == "" {
		return
	}
	if token := readTokens(path)[d.ID]; token != "" && b.client.Token() == "" {
		b.client.SetToken(token)
	}
}

// SetupInfo reads what the queue manager says about itself and this app.
func (b *Backend) SetupInfo(ctx context.Context) webui.SetupInfo {
	info := webui.SetupInfo{Addr: b.client.Base(), HasCode: b.client.Token() != ""}
	b.mu.Lock()
	info.Name = b.name
	b.mu.Unlock()
	d, err := b.client.Discover(ctx)
	if err != nil {
		info.Err = wrap(err)
		return info
	}
	info.Name = d.Name
	a, err := b.client.Access(ctx)
	if err != nil {
		info.Err = wrap(err)
		return info
	}
	info.Level, info.Room = a.Access.String(), a.Room.String()
	return info
}

// EnterCode logs in with an access code and keeps the token.
func (b *Backend) EnterCode(ctx context.Context, code string) error {
	b.identify(ctx)
	grant, err := b.client.Login(ctx, code)
	var e *kmclient.Error
	switch {
	case errors.Is(err, kmclient.ErrUnauthorized),
		errors.As(err, &e) && e.Status == 404:
		return fmt.Errorf("%w: %v", webui.ErrWrongCode, err)
	case err != nil:
		return wrap(err)
	}
	b.mu.Lock()
	b.level = grant.Access
	b.mu.Unlock()
	return b.saveToken()
}

// ForgetCode drops the token, here and in the file.
func (b *Backend) ForgetCode() error {
	b.client.SetToken("")
	b.mu.Lock()
	b.level = wire.LevelView
	b.mu.Unlock()
	return b.saveToken()
}

// saveToken writes the client's token for this queue manager to the token
// file, or removes it when there is none.
func (b *Backend) saveToken() error {
	b.mu.Lock()
	id, path := b.id, b.tokenFile
	b.mu.Unlock()
	if path == "" || id == "" {
		return nil
	}
	tokens := readTokens(path)
	if t := b.client.Token(); t != "" {
		tokens[id] = t
	} else {
		delete(tokens, id)
	}
	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("keeping the access code: %w", err)
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("keeping the access code: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("keeping the access code: %w", err)
	}
	return nil
}

// readTokens reads the token file. A missing or unreadable file is empty.
func readTokens(path string) map[string]string {
	tokens := map[string]string{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &tokens)
	}
	return tokens
}
