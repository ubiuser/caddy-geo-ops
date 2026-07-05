package update //nolint:testpackage // package internals are heavily used in tests here

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/ubiuser/caddy-geo-ops/internal/db"
)

func nopInfo() map[db.Filename]string { return nil }

func newDBIPUpdater(t *testing.T, baseURL string) *Updater {
	t.Helper()

	return &Updater{
		dbPath:     t.TempDir(),
		httpClient: &http.Client{},
		timeout:    5 * time.Second,
		baseURL:    baseURL,
	}
}

func TestNewValidation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	_, err := New(zaptest.NewLogger(t), Config{DBInfoFn: nopInfo})
	assert.ErrorIs(t, err, errDBPathIsEmpty)

	_, err = New(zaptest.NewLogger(t), Config{DBPath: dir})
	assert.ErrorIs(t, err, errDBInfoFnIsNil)

	_, err = New(zaptest.NewLogger(t), Config{DBPath: filepath.Join(dir, "nope"), DBInfoFn: nopInfo})
	assert.Errorf(t, err, "nonexistent db path should error")
}

func TestNewDefaultsAndCredentials(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// No credentials -> no MaxMind client, defaults applied.
	u, err := New(zaptest.NewLogger(t), Config{DBPath: dir, DBInfoFn: nopInfo})
	require.NoError(t, err)
	assert.Nilf(t, u.maxmind, "expected no MaxMind client without credentials")
	assert.Equal(t, defaultFrequency, u.frequency)
	assert.Equal(t, defaultTimeout, u.timeout)

	// With credentials -> MaxMind client built.
	u2, err := New(zaptest.NewLogger(t), Config{
		DBPath: dir, DBInfoFn: nopInfo,
		AccountID: 12345, LicenseKey: "abcdef",
		Frequency: time.Hour, Timeout: 5 * time.Second,
	})
	require.NoError(t, err)
	assert.NotNilf(t, u2.maxmind, "expected MaxMind client with credentials")
}

// TestNonPositiveDefaulted ensures a negative frequency/timeout is defaulted
// rather than reaching time.NewTicker (which would panic) or producing an
// already-expired context.
func TestNonPositiveDefaulted(t *testing.T) {
	t.Parallel()

	u, err := New(zaptest.NewLogger(t), Config{
		DBPath: t.TempDir(), DBInfoFn: nopInfo,
		Frequency: -5 * time.Minute, Timeout: -1,
	})
	require.NoError(t, err)
	assert.Equal(t, defaultFrequency, u.frequency)
	assert.Equal(t, defaultTimeout, u.timeout)

	assert.NotPanicsf(t, u.Start, "Start must not panic with a defaulted frequency")
	u.Stop()
}

func TestStopIdempotent(t *testing.T) {
	t.Parallel()

	u, err := New(zaptest.NewLogger(t), Config{DBPath: t.TempDir(), DBInfoFn: nopInfo})
	require.NoError(t, err)

	u.Start()
	u.Stop()
	assert.NotPanicsf(t, u.Stop, "a second Stop must not panic")
}

func TestDBIPURL(t *testing.T) {
	t.Parallel()

	u := &Updater{baseURL: dbipBaseURL}

	got, ok := u.dbipURL(db.DBIPCityType, time.Date(2025, time.June, 15, 0, 0, 0, 0, time.UTC))
	require.Truef(t, ok, "expected a URL for DBIP city")
	assert.Equal(t, "https://download.db-ip.com/free/dbip-city-lite-2025-06.mmdb.gz", got)

	_, ok = u.dbipURL(db.GeoIP2CityType, time.Now())
	assert.Falsef(t, ok, "non-DBIP type should not yield a URL")
}

func TestCleanStaleTemps(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write := func(name string, age time.Duration) {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))

		mtime := time.Now().Add(-age)
		require.NoError(t, os.Chtimes(path, mtime, mtime))
	}

	write("geoip2-city.mmdb", 0)                   // real db: keep
	write("geoip2-city.mmdb.tmp-111", 2*time.Hour) // crash orphan: remove
	write("geoip2-city.mmdb.tmp-222", 0)           // could be in-flight: keep

	u := &Updater{logger: zaptest.NewLogger(t), dbPath: dir, timeout: time.Minute}
	u.cleanStaleTemps()

	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}

	assert.Truef(t, exists("geoip2-city.mmdb"), "real database must be kept")
	assert.Falsef(t, exists("geoip2-city.mmdb.tmp-111"), "stale temp must be removed")
	assert.Truef(t, exists("geoip2-city.mmdb.tmp-222"), "recent temp must be kept (possibly in-flight)")
}

func TestWriteAtomic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	u := &Updater{dbPath: dir}

	require.NoError(t, u.writeAtomic(db.GeoIP2City, strings.NewReader("hello-db")))

	got, err := os.ReadFile(filepath.Join(dir, string(db.GeoIP2City)))
	require.NoError(t, err)
	assert.Equal(t, "hello-db", string(got))

	// No leftover temp files on success.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Lenf(t, entries, 1, "expected exactly the target file (no leftover temp)")
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, assert.AnError }

func TestWriteAtomicCleanupOnError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	u := &Updater{dbPath: dir}

	require.Error(t, u.writeAtomic(db.DBIPCity, errReader{}))

	// The temp file must be removed and the target never created.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Emptyf(t, entries, "no temp file should remain after a failed write")
}

func gzipBytes(t *testing.T, payload []byte) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)
	_, err := zw.Write(payload)
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	return buf.Bytes()
}

func TestDownloadDBIP(t *testing.T) {
	t.Parallel()

	payload := []byte("fake-mmdb-bytes")
	gz := gzipBytes(t, payload)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Honour conditional requests so we can assert the 304 path.
		if r.Header.Get("If-Modified-Since") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		w.Header().Set("Content-Type", "application/gzip")

		_, _ = w.Write(gz)
	}))
	defer srv.Close()

	u := newDBIPUpdater(t, srv.URL+"/free/")

	// First download: file absent -> downloaded and gunzipped.
	updated, err := u.downloadDBIP(t.Context(), db.DBIPCityType, db.DBIPCity)
	require.NoError(t, err)
	assert.Truef(t, updated, "expected updated=true on first download")

	got, err := os.ReadFile(filepath.Join(u.dbPath, string(db.DBIPCity)))
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	// Second download: file now exists -> conditional request -> 304 -> no update.
	updated, err = u.downloadDBIP(t.Context(), db.DBIPCityType, db.DBIPCity)
	require.NoError(t, err)
	assert.Falsef(t, updated, "expected updated=false on 304 Not Modified")
}

func TestDownloadDBIPFallbackToPreviousMonth(t *testing.T) {
	t.Parallel()

	payload := []byte("previous-month-mmdb")
	gz := gzipBytes(t, payload)

	now := time.Now().UTC()
	curMonth := now.Format(yyyyMMFormat)
	prevMonth := now.AddDate(0, -1, 0).Format(yyyyMMFormat)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, curMonth):
			w.WriteHeader(http.StatusNotFound) // current month not published yet

		case strings.Contains(r.URL.Path, prevMonth):
			w.Header().Set("Content-Type", "application/gzip")

			_, _ = w.Write(gz)

		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	u := newDBIPUpdater(t, srv.URL+"/free/")

	updated, err := u.downloadDBIP(t.Context(), db.DBIPCityType, db.DBIPCity)
	require.NoError(t, err)
	assert.Truef(t, updated, "should fall back to the previous month")

	got, err := os.ReadFile(filepath.Join(u.dbPath, string(db.DBIPCity)))
	require.NoError(t, err)
	assert.Equal(t, payload, got)
}

func TestDownloadDBIPNotPublished(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	u := newDBIPUpdater(t, srv.URL+"/free/")

	_, err := u.downloadDBIP(t.Context(), db.DBIPCityType, db.DBIPCity)
	assert.ErrorIsf(t, err, errDBIPNotPublished, "both months 404 -> errDBIPNotPublished")
}

func TestDownloadDBIPUnexpectedStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	u := newDBIPUpdater(t, srv.URL+"/free/")

	_, err := u.downloadDBIP(t.Context(), db.DBIPCityType, db.DBIPCity)
	require.Error(t, err)
	assert.NotErrorIsf(t, err, errDBIPNotPublished, "a 5xx is a real error, not 'not published'")
}

// countingDBIPServer returns a server that 304s every request (so no body is
// needed) and counts how many requests it received.
func countingDBIPServer(t *testing.T, hits *int32) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(hits, 1)
		w.WriteHeader(http.StatusNotModified)
	}))
	t.Cleanup(srv.Close)

	return srv
}

// dbipUpdaterWithFile builds an updater whose db folder holds one DB-IP file
// with the given mtime, plus a getDBInfo reporting it.
func dbipUpdaterWithFile(t *testing.T, srvURL string, freq time.Duration, mtime time.Time) *Updater {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, string(db.DBIPCity))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
	require.NoError(t, os.Chtimes(path, mtime, mtime))

	return &Updater{
		logger:     zaptest.NewLogger(t),
		dbPath:     dir,
		httpClient: &http.Client{},
		frequency:  freq,
		timeout:    5 * time.Second,
		baseURL:    srvURL + "/free/",
		getDBInfo:  func() map[db.Filename]string { return map[db.Filename]string{db.DBIPCity: ""} },
	}
}

func TestInitialUpdateSkipsRecentFile(t *testing.T) {
	t.Parallel()

	var hits int32

	srv := countingDBIPServer(t, &hits)

	// File written "now"; frequency 1h -> within frequency -> skip on initial pass.
	u := dbipUpdaterWithFile(t, srv.URL, time.Hour, time.Now())

	u.updateAll(t.Context(), true)
	assert.Zerof(t, atomic.LoadInt32(&hits), "a recently-updated file must be skipped on the initial pass")
}

func TestInitialUpdateChecksStaleFile(t *testing.T) {
	t.Parallel()

	var hits int32

	srv := countingDBIPServer(t, &hits)

	// File last modified 2h ago; frequency 1h -> stale -> checked on initial pass.
	u := dbipUpdaterWithFile(t, srv.URL, time.Hour, time.Now().Add(-2*time.Hour))

	u.updateAll(t.Context(), true)
	assert.NotZerof(t, atomic.LoadInt32(&hits), "a stale file must be checked on the initial pass")
}

func TestPeriodicUpdateChecksRecentFile(t *testing.T) {
	t.Parallel()

	var hits int32

	srv := countingDBIPServer(t, &hits)

	// Even a fresh file is checked on a periodic (non-initial) pass.
	u := dbipUpdaterWithFile(t, srv.URL, time.Hour, time.Now())

	u.updateAll(t.Context(), false)
	assert.NotZerof(t, atomic.LoadInt32(&hits), "periodic passes check regardless of file age")
}

func TestDownloadDBIPContextCanceled(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	u := newDBIPUpdater(t, srv.URL+"/free/")

	u.timeout = time.Minute

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // cancelled before the request is made

	_, err := u.downloadDBIP(ctx, db.DBIPCityType, db.DBIPCity)
	require.Error(t, err)
	assert.ErrorIsf(t, err, context.Canceled, "a cancelled context must abort the download")
}
