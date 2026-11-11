package webui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// With searchFirst, Run searches before it dials. The search here lasts until
// the test cancels it, so no dial ever starts.
func TestDeviceSearchesBeforeTheFirstDial(t *testing.T) {
	d := testDevice(t)
	called := make(chan string, 1)
	d.SetResolver(func(ctx context.Context, current string) (string, error) {
		called <- current
		<-ctx.Done()
		return "", ctx.Err()
	}, true)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.Run(ctx)
	}()

	select {
	case current := <-called:
		assert.Equal(t, "127.0.0.1:1", current)
	case <-time.After(5 * time.Second):
		t.Fatal("the device was not searched for")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestResearchMovesToTheFoundAddress(t *testing.T) {
	d := testDevice(t)
	d.SetResolver(func(context.Context, string) (string, error) {
		return "127.0.0.1:2", nil
	}, false)

	d.research(context.Background())
	assert.Equal(t, "127.0.0.1:2", d.Addr())
}

func TestResearchKeepsTheAddressWhenNothingAnswers(t *testing.T) {
	d := testDevice(t)
	d.SetResolver(func(context.Context, string) (string, error) {
		return "", errors.New("no device")
	}, false)

	d.research(context.Background())
	assert.Equal(t, "127.0.0.1:1", d.Addr())
}
