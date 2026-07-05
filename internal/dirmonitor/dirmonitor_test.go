package dirmonitor_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/ubiuser/caddy-geo-ops/internal/dirmonitor"
)

// newTestMonitor starts a monitor on a fresh temp dir, returning the dir and
// channels that receive the basenames passed to the update/delete callbacks.
func newTestMonitor(t *testing.T) (_ string, updatesCh, deletesCh <-chan string) {
	t.Helper()

	dir := t.TempDir()

	updates := make(chan string, 16)
	deletes := make(chan string, 16)

	dm, err := dirmonitor.New(zaptest.NewLogger(t), dirmonitor.Config{
		DBPath:   dir,
		UpdateFn: func(name string) error { updates <- filepath.Base(name); return nil },
		DeleteFn: func(name string) error { deletes <- filepath.Base(name); return nil },
	})
	require.NoError(t, err)

	dm.Start()
	t.Cleanup(dm.Close)

	return dir, updates, deletes
}

func waitFor(t *testing.T, ch <-chan string, d time.Duration) (string, bool) {
	t.Helper()

	select {
	case v := <-ch:
		return v, true

	case <-time.After(d):
		return "", false
	}
}

func TestDebouncedUpdate(t *testing.T) {
	t.Parallel()

	dir, updates, _ := newTestMonitor(t)

	path := filepath.Join(dir, "geoip2-city.mmdb")
	// Several quick writes should coalesce into a single update (100ms debounce).
	for range 3 {
		require.NoError(t, os.WriteFile(path, []byte("data"), 0o644))
		time.Sleep(10 * time.Millisecond)
	}

	got, ok := waitFor(t, updates, 2*time.Second)
	require.Truef(t, ok, "expected an update event")
	assert.Equal(t, "geoip2-city.mmdb", got)

	// The burst should have produced just one debounced callback.
	_, extra := waitFor(t, updates, 300*time.Millisecond)
	assert.Falsef(t, extra, "expected a single debounced update, got more")
}

func TestIgnoresNonMmdb(t *testing.T) {
	t.Parallel()

	dir, updates, _ := newTestMonitor(t)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644))

	got, ok := waitFor(t, updates, 500*time.Millisecond)
	assert.Falsef(t, ok, "non-mmdb file should not trigger an update, got %q", got)
}

func TestDeleteEvent(t *testing.T) {
	t.Parallel()

	dir, updates, deletes := newTestMonitor(t)

	path := filepath.Join(dir, "geolite2-asn.mmdb")
	require.NoError(t, os.WriteFile(path, []byte("data"), 0o644))

	_, ok := waitFor(t, updates, 2*time.Second)
	require.Truef(t, ok, "expected initial update event")

	require.NoError(t, os.Remove(path))

	got, ok := waitFor(t, deletes, 2*time.Second)
	require.Truef(t, ok, "expected a delete event")
	assert.Equal(t, "geolite2-asn.mmdb", got)
}

func TestRemoveThenRecreateDoesNotDelete(t *testing.T) {
	t.Parallel()

	dir, updates, deletes := newTestMonitor(t)
	path := filepath.Join(dir, "geoip2-city.mmdb")

	require.NoError(t, os.WriteFile(path, []byte("v1"), 0o644))

	_, ok := waitFor(t, updates, 2*time.Second)
	require.Truef(t, ok, "expected an update for the initial create")

	// Atomic-save pattern: remove then recreate within the debounce window.
	// The monitor should settle to a single update (the file exists again),
	// never a delete.
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.WriteFile(path, []byte("v2"), 0o644))

	got, ok := waitFor(t, updates, 2*time.Second)
	require.Truef(t, ok, "expected a coalesced update")
	assert.Equal(t, "geoip2-city.mmdb", got)

	_, gotDelete := waitFor(t, deletes, 300*time.Millisecond)
	assert.Falsef(t, gotDelete, "remove+create must not produce a delete")
}

func TestCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	dm, err := dirmonitor.New(zaptest.NewLogger(t), dirmonitor.Config{
		DBPath:   t.TempDir(),
		UpdateFn: func(string) error { return nil },
		DeleteFn: func(string) error { return nil },
	})
	require.NoError(t, err)
	dm.Start()

	dm.Close()
	assert.NotPanicsf(t, dm.Close, "a second Close must not panic")
}

func TestNewRejectsBadConfig(t *testing.T) {
	t.Parallel()

	_, err := dirmonitor.New(
		nil,
		dirmonitor.Config{
			DBPath:   t.TempDir(),
			UpdateFn: func(string) error { return nil },
			DeleteFn: func(string) error { return nil },
		},
	)
	assert.ErrorIs(t, err, dirmonitor.ErrLoggerIsNil)

	_, err = dirmonitor.New(
		zaptest.NewLogger(t),
		dirmonitor.Config{
			DBPath:   "",
			UpdateFn: func(string) error { return nil },
			DeleteFn: func(string) error { return nil },
		},
	)
	assert.Errorf(t, err, "empty db path should error")
}
