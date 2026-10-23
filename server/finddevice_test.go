package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rrgmc/ck-karaoke/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resolver answers ErrNoDevice for the first misses calls, then finds addr.
func resolver(misses int, addr string, calls *int) func(context.Context) (string, []client.Device, error) {
	return func(ctx context.Context) (string, []client.Device, error) {
		*calls++
		if *calls <= misses {
			return "", nil, client.ErrNoDevice
		}
		return addr, nil, nil
	}
}

func fastSearch(t *testing.T) {
	t.Helper()
	saved := searchPause
	searchPause = time.Millisecond
	t.Cleanup(func() { searchPause = saved })
}

func TestFindDeviceKeepsLookingUntilOneAnswers(t *testing.T) {
	fastSearch(t)
	var calls, searching int
	cfg := Config{WaitForDevice: true, Searching: func() { searching++ }}

	device, _, err := findDevice(context.Background(), cfg, resolver(3, "10.0.0.5:35379", &calls), quietLogger())
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.5:35379", device)
	assert.Equal(t, 4, calls)
	assert.Equal(t, 3, searching)
}

func TestFindDeviceGivesUpWithoutWaitForDevice(t *testing.T) {
	var calls int
	_, _, err := findDevice(context.Background(), Config{}, resolver(1, "10.0.0.5:35379", &calls), quietLogger())
	assert.ErrorIs(t, err, client.ErrNoDevice)
	assert.Equal(t, 1, calls)
}

func TestFindDeviceStopsWhenCancelled(t *testing.T) {
	fastSearch(t)
	ctx, cancel := context.WithCancel(context.Background())
	var calls int
	cfg := Config{WaitForDevice: true, Searching: cancel}

	_, _, err := findDevice(ctx, cfg, resolver(100, "10.0.0.5:35379", &calls), quietLogger())
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, calls)
}

func TestFindDevicePrefersTheRememberedAddress(t *testing.T) {
	fastSearch(t)
	lastAddrFile, songDB := fixture(t, "10.0.0.9:35379")
	var calls int
	cfg := Config{WaitForDevice: true, LastAddrFile: lastAddrFile, SongDB: songDB}

	// Run falls back to the remembered address, so there is nothing to wait for.
	_, _, err := findDevice(context.Background(), cfg, resolver(100, "10.0.0.5:35379", &calls), quietLogger())
	assert.ErrorIs(t, err, client.ErrNoDevice)
	assert.Equal(t, 1, calls)
}

func TestFindDeviceRetriesFailedSearches(t *testing.T) {
	fastSearch(t)
	// What a search reports while the system blocks local network traffic.
	blocked := errors.New("sendto: operation not permitted")
	var calls int
	resolve := func(context.Context) (string, []client.Device, error) {
		calls++
		if calls == 1 {
			return "", nil, blocked
		}
		return "10.0.0.5:35379", nil, nil
	}

	device, _, err := findDevice(context.Background(), Config{WaitForDevice: true}, resolve, quietLogger())
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.5:35379", device)
	assert.Equal(t, 2, calls)
}

func TestFindDeviceReturnsFailedSearchesWithoutWaitForDevice(t *testing.T) {
	blocked := errors.New("sendto: operation not permitted")
	resolve := func(context.Context) (string, []client.Device, error) {
		return "", nil, blocked
	}

	_, _, err := findDevice(context.Background(), Config{}, resolve, quietLogger())
	assert.ErrorIs(t, err, blocked)
}
