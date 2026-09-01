package update //nolint:testpackage // package internals are heavily used in tests here

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
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
	"github.com/ubiuser/caddy-geo-ops/internal/db"
	"go.uber.org/zap/zaptest"
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

	// IP2Location token is stored as-is (no client object built, unlike MaxMind).
	u3, err := New(zaptest.NewLogger(t), Config{
		DBPath: dir, DBInfoFn: nopInfo,
		IP2LocationToken: "ip2loc-token",
	})
	require.NoError(t, err)
	assert.Equal(t, "ip2loc-token", u3.ip2locationToken)
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

func TestNewDownloadMemoryThresholdDefault(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Unset -> defaulted.
	u, err := New(zaptest.NewLogger(t), Config{DBPath: dir, DBInfoFn: nopInfo})
	require.NoError(t, err)
	assert.Equal(t, int64(defaultDownloadMemoryThreshold), u.downloadMemoryThreshold)

	// Non-positive -> also defaulted (same rule as Frequency/Timeout).
	u2, err := New(zaptest.NewLogger(t), Config{DBPath: dir, DBInfoFn: nopInfo, DownloadMemoryThreshold: -1})
	require.NoError(t, err)
	assert.Equal(t, int64(defaultDownloadMemoryThreshold), u2.downloadMemoryThreshold)

	// Explicit positive value -> stored as-is.
	u3, err := New(zaptest.NewLogger(t), Config{DBPath: dir, DBInfoFn: nopInfo, DownloadMemoryThreshold: 1024})
	require.NoError(t, err)
	assert.Equal(t, int64(1024), u3.downloadMemoryThreshold)
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

func TestIP2LocationFileCode(t *testing.T) {
	t.Parallel()

	code, ok := ip2locationFileCode(db.IP2LocationCountryType)
	require.Truef(t, ok, "expected a file code for IP2Location country")
	assert.Equal(t, "DB1LITEMMDB", code)

	code, ok = ip2locationFileCode(db.IP2LocationCityType)
	require.Truef(t, ok, "expected a file code for IP2Location city")
	assert.Equal(t, "DB11LITEMMDB", code)

	code, ok = ip2locationFileCode(db.IP2LocationASNType)
	require.Truef(t, ok, "expected a file code for IP2Location ASN")
	assert.Equal(t, "DBASNLITEMMDB", code)

	code, ok = ip2locationFileCode(db.IP2ProxyPX10Type)
	require.Truef(t, ok, "expected a file code for IP2Proxy PX10")
	assert.Equal(t, "PX10MMDB", code)

	code, ok = ip2locationFileCode(db.IP2ProxyPX10LiteType)
	require.Truef(t, ok, "expected a file code for IP2Proxy PX10 LITE")
	assert.Equal(t, "PX10LITEMMDB", code)

	_, ok = ip2locationFileCode(db.GeoIP2CityType)
	assert.Falsef(t, ok, "non-IP2Location type should not yield a file code")
}

func buildZip(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for name, content := range entries {
		w, err := zw.Create(name)
		require.NoError(t, err)

		_, err = w.Write(content)
		require.NoError(t, err)
	}

	require.NoError(t, zw.Close())

	return buf.Bytes()
}

func TestExtractMMDB(t *testing.T) {
	t.Parallel()

	z := buildZip(t, map[string][]byte{
		"LICENSE-CC-BY-SA-4.0.TXT":  []byte("license"),
		"README_LITE.TXT":           []byte("readme"),
		"IP2LOCATION-LITE-DB1.MMDB": []byte("fake-mmdb-bytes"),
	})

	rc, err := extractMMDB(z)
	require.NoError(t, err)

	defer rc.Close()

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "fake-mmdb-bytes", string(got))
}

func TestExtractMMDBNoEntry(t *testing.T) {
	t.Parallel()

	z := buildZip(t, map[string][]byte{
		"LICENSE-CC-BY-SA-4.0.TXT": []byte("license"),
		"README_LITE.TXT":          []byte("readme"),
	})

	_, err := extractMMDB(z)
	assert.ErrorIsf(t, err, errIP2LocationEntryNotFound, "zip without a .mmdb entry")
}

func TestExtractMMDBCorrupt(t *testing.T) {
	t.Parallel()

	_, err := extractMMDB([]byte("not a zip file"))
	require.Error(t, err)
	assert.NotErrorIsf(t, err, errIP2LocationEntryNotFound, "corrupt data is a different error")
}

// newIP2LocationUpdater builds an Updater wired for IP2Location download
// tests. token mirrors newDBIPUpdater's baseURL parameter: always "test-token"
// today, kept as a parameter so a future test asserting per-token behaviour
// doesn't need to touch this helper's signature.
//
//nolint:unparam // see doc comment above
func newIP2LocationUpdater(t *testing.T, baseURL, token string) *Updater {
	t.Helper()

	return &Updater{
		dbPath:                  t.TempDir(),
		httpClient:              &http.Client{},
		timeout:                 5 * time.Second,
		ip2locationURL:          baseURL,
		ip2locationToken:        token,
		downloadMemoryThreshold: defaultDownloadMemoryThreshold,
	}
}

func ip2locationZipBytes(t *testing.T, mmdbPayload []byte) []byte {
	t.Helper()

	return buildZip(t, map[string][]byte{
		"LICENSE-CC-BY-SA-4.0.TXT":  []byte("license"),
		"README_LITE.TXT":           []byte("readme"),
		"IP2LOCATION-LITE-DB1.MMDB": mmdbPayload,
	})
}

func TestDownloadIP2Location(t *testing.T) {
	t.Parallel()

	payload := []byte("fake-ip2location-mmdb-bytes")
	zipData := ip2locationZipBytes(t, payload)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test-token", r.URL.Query().Get("token"))
		assert.Equal(t, "DB1LITEMMDB", r.URL.Query().Get("file"))

		if r.Header.Get("If-Modified-Since") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		w.Header().Set("Content-Type", "application/zip")

		_, _ = w.Write(zipData)
	}))
	defer srv.Close()

	u := newIP2LocationUpdater(t, srv.URL, "test-token")

	// First download: file absent -> downloaded and extracted.
	updated, err := u.downloadIP2Location(t.Context(), db.IP2LocationCountryType, db.IP2LocationCountry)
	require.NoError(t, err)
	assert.Truef(t, updated, "expected updated=true on first download")

	got, err := os.ReadFile(filepath.Join(u.dbPath, string(db.IP2LocationCountry)))
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	// Second download: file now exists -> conditional request -> 304 -> no update.
	updated, err = u.downloadIP2Location(t.Context(), db.IP2LocationCountryType, db.IP2LocationCountry)
	require.NoError(t, err)
	assert.Falsef(t, updated, "expected updated=false on 304 Not Modified")
}

func TestDownloadIP2LocationUnexpectedStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	u := newIP2LocationUpdater(t, srv.URL, "test-token")

	_, err := u.downloadIP2Location(t.Context(), db.IP2LocationCountryType, db.IP2LocationCountry)
	require.Error(t, err)
	assert.NotContainsf(t, err.Error(), "test-token", "the token must never appear in an error message")
}

func TestDownloadIP2LocationUnknownType(t *testing.T) {
	t.Parallel()

	u := newIP2LocationUpdater(t, "http://example.invalid", "test-token")

	_, err := u.downloadIP2Location(t.Context(), db.GeoIP2CityType, db.GeoIP2City)
	assert.ErrorIsf(t, err, errNoIP2LocationCode, "unknown type should error before making a request")
}

func TestDownloadIP2LocationContextCanceled(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	u := newIP2LocationUpdater(t, srv.URL, "test-token")

	u.timeout = time.Minute

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // cancelled before the request is made

	_, err := u.downloadIP2Location(ctx, db.IP2LocationCountryType, db.IP2LocationCountry)
	require.Error(t, err)
	assert.ErrorIsf(t, err, context.Canceled, "a cancelled context must abort the download")

	// Regression: http.Client.Do returns a *url.Error whose Error() embeds the
	// full request URL (including the token) even on a context-cancellation
	// failure — this must not leak into the wrapped error's message.
	assert.NotContainsf(t, err.Error(), "test-token",
		"the token must never appear in an error message, even from the underlying *url.Error")
}

func TestRedactToken(t *testing.T) {
	t.Parallel()

	t.Run("empty token passes through unchanged", func(t *testing.T) {
		t.Parallel()

		err := assert.AnError

		got := redactToken(err, "")
		assert.Samef(t, err, got, "an empty token must return the exact same error value, not a wrapper")
	})

	t.Run("nil error passes through as nil", func(t *testing.T) {
		t.Parallel()

		got := redactToken(nil, "some-token")
		assert.NoErrorf(t, got, "a nil error must return nil regardless of token")
	})

	t.Run("populated token is redacted but the chain is preserved", func(t *testing.T) {
		t.Parallel()

		const token = "super-secret-token"

		underlying := fmt.Errorf("dial tcp example.invalid?token=%s: %w", token, context.Canceled)

		got := redactToken(underlying, token)
		require.Error(t, got)
		assert.NotContainsf(t, got.Error(), token, "the token must be scrubbed from the message")
		assert.ErrorIsf(t, got, context.Canceled, "errors.Is must still reach the wrapped sentinel")
	})
}

// ip2locationUpdaterWithFile builds an updater whose db folder holds one
// IP2Location file, plus a getDBInfo reporting it.
func ip2locationUpdaterWithFile(t *testing.T, srvURL, token string) *Updater {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, string(db.IP2LocationCountry))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))

	return &Updater{
		logger:           zaptest.NewLogger(t),
		dbPath:           dir,
		httpClient:       &http.Client{},
		frequency:        time.Hour,
		timeout:          5 * time.Second,
		ip2locationURL:   srvURL,
		ip2locationToken: token,
		getDBInfo:        func() map[db.Filename]string { return map[db.Filename]string{db.IP2LocationCountry: ""} },
	}
}

func TestUpdateAllIP2LocationGatedByToken(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	// No token configured: updateAll must not hit the server at all. This must
	// be fatal: if a token-gate regression lets a spurious hit through here,
	// letting the test limp into phase 2 below would risk masking the failure
	// behind a passing (but misleading) second assertion.
	u := ip2locationUpdaterWithFile(t, srv.URL, "")
	u.updateAll(t.Context(), false)
	require.Zerof(t, hits.Load(), "no token configured -> IP2Location databases must be skipped")

	// Token configured: updateAll must check it.
	u2 := ip2locationUpdaterWithFile(t, srv.URL, "tok")
	u2.updateAll(t.Context(), false)
	assert.NotZerof(t, hits.Load(), "token configured -> IP2Location databases must be checked")
}

func TestExtractMMDBToFile(t *testing.T) {
	t.Parallel()

	sample, err := os.ReadFile(filepath.Join("testdata", "ip2proxy-px10-sample.mmdb"))
	require.NoError(t, err)

	z := buildZip(t, map[string][]byte{
		"LICENSE-CC-BY-SA-4.0.TXT": []byte("license"),
		"README.TXT":               []byte("readme"),
		"IP2PROXY-PX10.MMDB":       sample,
	})

	dir := t.TempDir()

	rc, err := extractMMDBToFile(dir, db.IP2ProxyPX10, bytes.NewReader(z))
	require.NoError(t, err)

	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equalf(t, sample, got, "extracted content must match the real sample byte-for-byte")

	require.NoError(t, rc.Close())

	// Close must remove the temp zip file — nothing should remain in dir.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Emptyf(t, entries, "temp zip file must be removed after Close")
}

func TestExtractMMDBToFileNoEntry(t *testing.T) {
	t.Parallel()

	z := buildZip(t, map[string][]byte{
		"LICENSE-CC-BY-SA-4.0.TXT": []byte("license"),
	})

	dir := t.TempDir()

	_, err := extractMMDBToFile(dir, db.IP2ProxyPX10, bytes.NewReader(z))
	assert.ErrorIsf(t, err, errIP2LocationEntryNotFound, "zip without a .mmdb entry")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Emptyf(t, entries, "temp zip file must be removed even on error")
}

func TestShouldExtractInMemory(t *testing.T) {
	t.Parallel()

	assert.Truef(t, shouldExtractInMemory(50, 100), "below threshold -> in memory")
	assert.Truef(t, shouldExtractInMemory(100, 100), "exactly at threshold -> in memory")
	assert.Falsef(t, shouldExtractInMemory(101, 100), "above threshold -> disk")
	assert.Falsef(t, shouldExtractInMemory(-1, 100), "unknown size (-1) -> disk, not assumed small")
}

func TestDownloadIP2LocationSizeAdaptiveExtraction(t *testing.T) {
	t.Parallel()

	payload := []byte("fake-large-ip2proxy-mmdb-bytes")
	zipData := buildZip(t, map[string][]byte{
		"LICENSE-CC-BY-SA-4.0.TXT": []byte("license"),
		"IP2PROXY-PX10.MMDB":       payload,
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")

		_, _ = w.Write(zipData)
	}))
	// t.Cleanup (not defer): the two subtests below share srv and run in
	// parallel, so closing it must wait for them to finish, not fire as soon
	// as this function body returns (which happens immediately once a
	// parallel subtest calls t.Parallel()).
	t.Cleanup(srv.Close)

	t.Run("below threshold uses the in-memory path", func(t *testing.T) {
		t.Parallel()

		u := newIP2LocationUpdater(t, srv.URL, "test-token")

		u.downloadMemoryThreshold = int64(len(zipData)) + 1 // comfortably above

		updated, err := u.downloadIP2Location(t.Context(), db.IP2ProxyPX10Type, db.IP2ProxyPX10)
		require.NoError(t, err)
		assert.True(t, updated)

		got, err := os.ReadFile(filepath.Join(u.dbPath, string(db.IP2ProxyPX10)))
		require.NoError(t, err)
		assert.Equal(t, payload, got)
	})

	t.Run("at or above threshold uses the disk-based path", func(t *testing.T) {
		t.Parallel()

		u := newIP2LocationUpdater(t, srv.URL, "test-token")

		u.downloadMemoryThreshold = 1 // forces the disk-based path for any real payload

		updated, err := u.downloadIP2Location(t.Context(), db.IP2ProxyPX10Type, db.IP2ProxyPX10)
		require.NoError(t, err)
		assert.True(t, updated)

		got, err := os.ReadFile(filepath.Join(u.dbPath, string(db.IP2ProxyPX10)))
		require.NoError(t, err)
		assert.Equal(t, payload, got)

		// The temp zip file must not remain in dbPath after extraction.
		entries, err := os.ReadDir(u.dbPath)
		require.NoError(t, err)
		assert.Lenf(t, entries, 1, "expected only the final target file, no leftover temp zip")
	})

	t.Run("unknown content length uses the disk-based path", func(t *testing.T) {
		t.Parallel()

		unknownLenSrv := chunkedZipServer(t, zipData)

		u := newIP2LocationUpdater(t, unknownLenSrv.URL, "test-token")

		updated, err := u.downloadIP2Location(t.Context(), db.IP2ProxyPX10Type, db.IP2ProxyPX10)
		require.NoError(t, err)
		assert.True(t, updated)

		got, err := os.ReadFile(filepath.Join(u.dbPath, string(db.IP2ProxyPX10)))
		require.NoError(t, err)
		assert.Equal(t, payload, got)
	})
}

// chunkedZipServer serves zipData split across two flushed writes, which
// forces chunked transfer encoding — the response then carries no
// Content-Length header, exercising the "size unknown" branch of
// downloadIP2Location's size-adaptive extraction routing.
func chunkedZipServer(t *testing.T, zipData []byte) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")

		flusher, ok := w.(http.Flusher)
		if !ok {
			// require must not be used inside a non-test goroutine (this
			// handler runs on its own goroutine, not the test's) — assert
			// plus an explicit early return achieves the same effect.
			assert.Fail(t, "test server ResponseWriter must support flushing")

			return
		}

		_, _ = w.Write(zipData[:len(zipData)/2])

		flusher.Flush()

		_, _ = w.Write(zipData[len(zipData)/2:])
	}))
	t.Cleanup(srv.Close)

	return srv
}

func TestExtractMMDBToFileCorrupt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	_, err := extractMMDBToFile(dir, db.IP2ProxyPX10, strings.NewReader("not a zip file"))
	require.Error(t, err)
	assert.NotErrorIsf(t, err, errIP2LocationEntryNotFound, "corrupt data is a different error")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Emptyf(t, entries, "temp zip file must be removed even on error")
}
