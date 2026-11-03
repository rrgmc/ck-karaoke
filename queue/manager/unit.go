package manager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/rrgmc/ck-karaoke/client"
	"github.com/rrgmc/ck-karaoke/protocol"
	"github.com/rrgmc/ck-karaoke/queue/feeder"
)

// unitDialer opens sessions with the chosen unit wherever it is now. The
// unit's address comes from DHCP, so it is found by model and database id:
// the last address first, then a broadcast.
type unitDialer struct {
	log      *slog.Logger
	choice   func() UnitChoice
	found    func(client.Device)
	override string

	mu   sync.Mutex
	addr string
}

// errNoUnit reports that no unit matching the choice answered.
var errNoUnit = errors.New("no karaoke unit matching the configuration answered")

func (d *unitDialer) matches(dev client.Device) bool {
	c := d.choice()
	return (c.Model == "" || dev.Info.Model == c.Model) && (c.DatabaseID == "" || dev.Info.DatabaseID == c.DatabaseID)
}

// locate returns the unit's address.
func (d *unitDialer) locate(ctx context.Context) (string, error) {
	if d.override != "" {
		return client.WithDefaultPort(d.override), nil
	}
	d.mu.Lock()
	last := d.addr
	d.mu.Unlock()
	if last == "" {
		last = d.choice().Addr
	}
	if last != "" {
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		dev, err := client.Ping(pctx, last)
		cancel()
		if err == nil && d.matches(*dev) {
			d.mu.Lock()
			d.addr = dev.Addr
			d.mu.Unlock()
			return dev.Addr, nil
		}
	}
	devices, err := client.Discover(ctx, "", 3*time.Second)
	if err != nil && !errors.Is(err, client.ErrNoDevice) {
		return "", err
	}
	for _, dev := range devices {
		if d.matches(dev) {
			d.mu.Lock()
			changed := d.addr != dev.Addr
			d.addr = dev.Addr
			d.mu.Unlock()
			if changed {
				d.log.Info("found the karaoke unit", "addr", dev.Addr, "info", dev.Info.String())
				if d.found != nil {
					d.found(dev)
				}
			}
			return dev.Addr, nil
		}
	}
	return "", errNoUnit
}

// Dial finds the unit and opens a session with it.
func (d *unitDialer) Dial(ctx context.Context, identity protocol.Identity) (feeder.Unit, error) {
	addr, err := d.locate(ctx)
	if err != nil {
		return nil, err
	}
	c, err := client.Dial(ctx, addr, client.Options{Identity: &identity, Logger: d.log})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", addr, err)
	}
	return c, nil
}

// Addr returns where the unit was last found.
func (d *unitDialer) Addr() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.addr
}
