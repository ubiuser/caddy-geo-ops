package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/caddytest"
	"github.com/stretchr/testify/require"
)

const (
	reloadURL = "http://localhost:9080/"
	reloadIP  = "81.2.69.142" // canonical MaxMind test IP → London, GB

	// The two responses that prove a working db is serving. Both fixtures resolve
	// the IP to country GB (so the @gb matcher always fires → 200); they differ only
	// in the city field, so the live body flips as reloads swap the provider.
	bodyCity    = "country=GB city=London" // geoip2-city fixture
	bodyCountry = "country=GB city="       // geoip2-country fixture (no city)
)

// reloadStress holds the shared configuration and the counters the swapper and the
// request workers touch concurrently. Always used via a pointer (it embeds atomics).
type reloadStress struct {
	deadline time.Time
	client   *http.Client
	dbDir    string
	dbFile   string
	city     []byte
	country  []byte
	pause    time.Duration
	requests atomic.Int64
	swaps    atomic.Int64
	sawCity  atomic.Bool
	sawEmpty atomic.Bool
}

// TestConcurrentReloadUnderLoad stresses the Ops RWMutex / hot-reload path: many
// goroutines hammer the handler+matcher while another goroutine atomically swaps
// the database file out from under them, forcing the dirmonitor watcher to rebuild
// and swap the provider repeatedly. Run with -race (CI does) to surface any data
// race between request-time RLock reads and reload-time provider writes/closes.
//
// Two invariants are checked:
//   - validate-before-swap means a working database is never dropped, so *every*
//     request resolves country=GB and returns 200 — there is no window where the
//     db is absent (which would route to "other").
//   - the body actually flips between the city and country fixtures during the run,
//     proving reloads genuinely took effect concurrently with traffic (not a hollow
//     pass where the watcher never fired).
//
// Swaps are paced just above the dirmonitor debounce (100ms) so each one settles
// into a real reload rather than being coalesced away by back-to-back events.
//
// Not parallel: it binds fixed ports (:9080/:2999) shared with TestEndToEnd, so the
// two must not run concurrently. Go completes this non-parallel test before resuming
// the parallel one, so they never contend for the port.
//
//nolint:paralleltest // deliberately serial — see the "Not parallel" note above.
func TestConcurrentReloadUnderLoad(t *testing.T) {
	const (
		testDuration   = 3 * time.Second
		reqWorkers     = 16
		swapPause      = 150 * time.Millisecond // > dirmonitor's 100ms debounce
		requestTimeout = 5 * time.Second
		dbFileMode     = 0o600
	)

	// A private db_path we own, so swaps don't touch the committed testdata and the
	// watcher only ever sees our churn. Seed it with the City fixture.
	dbDir := t.TempDir()
	dbFile := filepath.Join(dbDir, "geoip2-city.mmdb")
	cityData := readFixture(t, "geoip2-city.mmdb")
	require.NoError(t, os.WriteFile(dbFile, cityData, dbFileMode))

	tester := caddytest.NewTester(t)
	tester.InitServer(reloadConfig(dbDir), "caddyfile")

	stress := &reloadStress{
		// Raise the per-host idle-connection cap so tight-looping workers reuse
		// connections instead of churning ephemeral ports.
		client:   &http.Client{Timeout: requestTimeout, Transport: &http.Transport{MaxIdleConnsPerHost: reqWorkers}},
		deadline: time.Now().Add(testDuration),
		pause:    swapPause,
		dbDir:    dbDir,
		dbFile:   dbFile,
		city:     cityData,
		country:  readFixture(t, "geoip2-country.mmdb"),
	}

	var wg sync.WaitGroup

	wg.Go(func() {
		stress.swapLoop(t)
	})

	for range reqWorkers {
		wg.Go(func() {
			stress.requestLoop(t)
		})
	}

	wg.Wait()

	// The stress actually happened, and reloads actually took effect: the live body
	// was observed as both fixtures while traffic flowed.
	require.Positivef(t, stress.requests.Load(), "no requests were made")
	require.Positivef(t, stress.swaps.Load(), "no database swaps succeeded")
	require.Truef(t, stress.sawCity.Load(), "never served the city fixture — reloads to it didn't take effect")
	require.Truef(t, stress.sawEmpty.Load(), "never served the country fixture — reloads to it didn't take effect")
	t.Logf("served %d requests across %d reload swaps with no race or regression",
		stress.requests.Load(), stress.swaps.Load())
}

// requestLoop fires lookups until the deadline, asserting the working-db invariant
// (always 200 + country=GB) and recording which fixture is live.
func (s *reloadStress) requestLoop(t *testing.T) {
	t.Helper()

	for time.Now().Before(s.deadline) {
		status, body := fetch(t, s.client)

		s.requests.Add(1)

		switch {
		case status != http.StatusOK:
			t.Errorf("status=%d body=%q, want 200", status, body)

			return

		case body == bodyCity:
			s.sawCity.Store(true)

		case body == bodyCountry:
			s.sawEmpty.Store(true)

		default:
			t.Errorf("unexpected body %q (did a reload drop the working db?)", body)

			return
		}
	}
}

// swapLoop alternately replaces the db file with the country and city fixtures
// until the deadline, pacing above the debounce so each swap settles into a reload.
func (s *reloadStress) swapLoop(t *testing.T) {
	t.Helper()

	useCountry := true

	for time.Now().Before(s.deadline) {
		payload := s.city
		if useCountry {
			payload = s.country
		}

		if swapDB(t, s.dbDir, s.dbFile, payload) {
			s.swaps.Add(1)
		}

		useCountry = !useCountry

		time.Sleep(s.pause)
	}
}

// reloadConfig builds the Caddyfile pointing geo_ops at dbDir. filepath.ToSlash
// keeps a Windows temp path a valid (quoted) Caddyfile token.
func reloadConfig(dbDir string) string {
	return `
		{
			admin localhost:2999
			http_port 9080
			https_port 9443
			order geo_ops first
			servers {
				trusted_proxies static 0.0.0.0/0 ::/0
				client_ip_headers X-Forwarded-For
			}
			geo_ops {
				db_path "` + filepath.ToSlash(dbDir) + `"
			}
		}

		:9080 {
			geo_ops

			@gb geo_ops {
				geoip2-city.country.iso_code GB
			}
			respond @gb "country={geo.geoip2-city.country.iso_code} city={geo.geoip2-city.city.names.en}"
			respond "other"
		}
	`
}

// readFixture loads a committed testdata mmdb into memory.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)

	return data
}

// fetch issues one geo lookup and returns the status and body. It uses t.Errorf
// (not require) because it runs in worker goroutines, where FailNow is illegal.
func fetch(t *testing.T, client *http.Client) (int, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, reloadURL, http.NoBody)
	if err != nil {
		t.Errorf("build request: %v", err)

		return 0, ""
	}

	req.Header.Set("X-Forwarded-For", reloadIP)

	resp, err := client.Do(req)
	if err != nil {
		t.Errorf("request: %v", err)

		return 0, ""
	}

	body, readErr := io.ReadAll(resp.Body)

	_ = resp.Body.Close()
	if readErr != nil {
		t.Errorf("read body: %v", readErr)

		return resp.StatusCode, ""
	}

	return resp.StatusCode, string(body)
}

// swapDB writes data to a temp file in dir and atomically renames it over target —
// the same replace-in-place an update performs. Returns true on success; a failed
// swap is logged but not fatal (we assert only that some swaps land), keeping the
// test free of OS file-timing flakiness.
//
// The rename is retried briefly: on Windows, renaming over the target fails with a
// transient sharing error if the watcher happens to be mid-read of it during a
// reload. The window is microseconds, so a few short retries absorb it. (This
// contention is an artefact of swapping every ~150ms; real updates are far apart.)
func swapDB(t *testing.T, dir, target string, data []byte) bool {
	t.Helper()

	const (
		renameRetries = 5
		renameBackoff = 10 * time.Millisecond
	)

	tmp, err := os.CreateTemp(dir, "swap-*.tmp")
	if err != nil {
		t.Logf("create temp: %v", err)

		return false
	}

	name := tmp.Name()

	_, writeErr := tmp.Write(data)

	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(name)

		t.Logf("write temp: %v / close: %v", writeErr, closeErr)

		return false
	}

	var renameErr error
	for range renameRetries {
		if renameErr = os.Rename(name, target); renameErr == nil {
			return true
		}

		time.Sleep(renameBackoff)
	}

	_ = os.Remove(name)

	t.Logf("rename swap failed after retries: %v", renameErr)

	return false
}
