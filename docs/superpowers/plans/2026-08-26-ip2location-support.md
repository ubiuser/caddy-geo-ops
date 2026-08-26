# IP2Location Database Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add support for the three free IP2Location LITE MMDB databases (Country/DB1,
City/DB11, ASN) alongside the existing MaxMind and DB-IP vendors, per
[issue #12](https://github.com/ubiuser/caddy-geo-ops/issues/12).

**Architecture:** Extend the existing `internal/db` filename/type taxonomy with three new
entries, add an IP2Location download path to `internal/update` (single-token auth,
conditional GET, zip-archive extraction — the one real mechanical difference from DB-IP's
plain gzip stream), wire a new `ip2location_token` Caddyfile field through `app`, and update
docs. No changes to the generic mmdb decode path (`internal/replacers`, `internal/ops`) are
needed — IP2Location LITE's MMDB schema was verified (by decoding a real downloaded file
against the test IP `81.2.69.142`) to be field-for-field identical to MaxMind's
GeoIP2/GeoLite2 schema for Country/City/ASN.

**Tech Stack:** Go 1.26, standard library `archive/zip` (no new dependency), existing
`net/http`, `go.uber.org/zap`, `testify`.

**Spec:** `docs/superpowers/specs/2026-08-26-ip2location-support-design.md`

## Global Constraints

- Go 1.26; module path `github.com/ubiuser/caddy-geo-ops`.
- `golangci-lint run --fix && golangci-lint run` must be clean before every commit.
- Wrap errors with `fmt.Errorf("...: %w", err)`; use sentinel errors for control flow
  (matching the existing `errDBIPNotPublished` / `errNoDBIPUrl` pattern).
- Structured logging only via `ctx.Logger()` / stored `*zap.Logger`, never `fmt.Print`; use
  the existing `internal/logfields` constructors (`logfields.Database`, `.File`) — don't
  inline `zap.String(...)`.
- **The `ip2location_token` credential must never appear in a log line or error message** —
  it is a secret, same class as `license_key`. Build any error text from the file *code*
  (`DB1LITEMMDB`, ...), never from the token-bearing URL.
- Test loggers use `zaptest.NewLogger(t)`, not `zap.NewNop()`.
- Test requests use `t.Context()` (via `httptest.NewRequestWithContext` or, for
  `internal/update`, passing `t.Context()` directly into the function under test) — never
  bare `context.Background()`.
- TDD: write the failing test, watch it fail, implement, watch it pass, then commit.
- Follow existing patterns exactly (see `internal/update/update.go`'s DB-IP path and
  `internal/db`'s existing taxonomy) rather than introducing new structural conventions.

---

### Task 1: Taxonomy — `internal/db`

**Files:**
- Modify: `internal/db/filename.go`
- Modify: `internal/db/type.go`
- Modify: `internal/db/db_test.go`

**Interfaces:**
- Produces: `db.IP2LocationCountry`, `db.IP2LocationCity`, `db.IP2LocationASN`
  (`db.Filename`); `db.IP2LocationCountryType`, `db.IP2LocationCityType`,
  `db.IP2LocationASNType` (`db.Type`); `db.IsIP2Location(t db.Type) bool`. Consumed by
  Tasks 2, 4, 5, 6.

- [ ] **Step 1: Write the failing test**

Replace `TestToTypeMappings` in `internal/db/db_test.go` with (adds an `ip2location` column,
three new cases, and turns the old two-way mutual-exclusion check into a three-way one):

```go
// TestToTypeMappings pins every filename -> type mapping AND the exact Type
// string (which doubles as the MaxMind edition ID for the updater). A typo in a
// constant would otherwise silently break auto-update for that edition.
func TestToTypeMappings(t *testing.T) {
	t.Parallel()

	cases := []struct {
		filename    db.Filename
		typ         db.Type
		edition     string // exact type/edition-ID string
		maxmind     bool   // expected IsGeoIP2OrGeoLite2
		dbip        bool   // expected IsDBIP
		ip2location bool   // expected IsIP2Location
	}{
		{db.GeoIP2AnonymousIP, db.GeoIP2AnonymousIPType, "GeoIP2-Anonymous-IP", true, false, false},
		{db.GeoIP2City, db.GeoIP2CityType, "GeoIP2-City", true, false, false},
		{db.GeoIP2ConnectionType, db.GeoIP2ConnectionTypeType, "GeoIP2-Connection-Type", true, false, false},
		{db.GeoIP2Country, db.GeoIP2CountryType, "GeoIP2-Country", true, false, false},
		{db.GeoIP2Domain, db.GeoIP2DomainType, "GeoIP2-Domain", true, false, false},
		{db.GeoIP2Enterprise, db.GeoIP2EnterpriseType, "GeoIP2-Enterprise", true, false, false},
		{db.GeoIP2ISP, db.GeoIP2ISPType, "GeoIP2-ISP", true, false, false},
		{db.GeoLite2ASN, db.GeoLite2ASNType, "GeoLite2-ASN", true, false, false},
		{db.GeoLite2City, db.GeoLite2CityType, "GeoLite2-City", true, false, false},
		{db.GeoLite2Country, db.GeoLite2CountryType, "GeoLite2-Country", true, false, false},
		{db.DBIPCity, db.DBIPCityType, "DBIP-City-Lite", false, true, false},
		{db.DBIPCountry, db.DBIPCountryType, "DBIP-Country-Lite", false, true, false},
		{db.DBIPASN, db.DBIPASNType, "DBIP-ASN-Lite", false, true, false},
		{db.IP2LocationCountry, db.IP2LocationCountryType, "IP2Location-Country", false, false, true},
		{db.IP2LocationCity, db.IP2LocationCityType, "IP2Location-City", false, false, true},
		{db.IP2LocationASN, db.IP2LocationASNType, "IP2Location-ASN", false, false, true},
	}

	for _, tc := range cases {
		t.Run(string(tc.filename), func(t *testing.T) {
			t.Parallel()

			assert.Equalf(t, tc.typ, db.ToType(tc.filename), "filename -> type")
			assert.Equalf(t, tc.edition, string(tc.typ), "exact edition-ID string")
			assert.Truef(t, db.IsKnown(tc.filename), "should be a known database")
			assert.Equalf(t, tc.maxmind, db.IsGeoIP2OrGeoLite2(tc.typ), "IsGeoIP2OrGeoLite2")
			assert.Equalf(t, tc.dbip, db.IsDBIP(tc.typ), "IsDBIP")
			assert.Equalf(t, tc.ip2location, db.IsIP2Location(tc.typ), "IsIP2Location")

			// A known type must be in exactly one category.
			count := 0
			for _, b := range []bool{tc.maxmind, tc.dbip, tc.ip2location} {
				if b {
					count++
				}
			}
			assert.Equalf(t, 1, count, "type must be exactly one of MaxMind/DB-IP/IP2Location")
		})
	}
}
```

Also add one line to `TestKey`:

```go
	assert.Equal(t, "ip2location-city", db.IP2LocationCity.Key())
```

And one line to `TestUnknownAndCaseInsensitive`:

```go
	assert.False(t, db.IsIP2Location(db.UnknownType))
```

- [ ] **Step 2: Run tests to verify they fail (compile error — new identifiers don't exist yet)**

Run: `go test ./internal/db/... -v`
Expected: FAIL to build — `undefined: db.IP2LocationCountry` (etc.)

- [ ] **Step 3: Implement the taxonomy additions**

In `internal/db/filename.go`, add after the `DBIPASN` const:

```go
	IP2LocationCountry Filename = "ip2location-country.mmdb"
	IP2LocationCity    Filename = "ip2location-city.mmdb"
	IP2LocationASN     Filename = "ip2location-asn.mmdb"
```

In `internal/db/type.go`, add after `DBIPASNType`:

```go
	IP2LocationCountryType Type = "IP2Location-Country"
	IP2LocationCityType    Type = "IP2Location-City"
	IP2LocationASNType     Type = "IP2Location-ASN"
```

Add three cases to `ToType`'s switch, after the `DBIPASN` case and before `default`:

```go
	case IP2LocationCountry:
		return IP2LocationCountryType

	case IP2LocationCity:
		return IP2LocationCityType

	case IP2LocationASN:
		return IP2LocationASNType
```

Add a new function after `IsDBIP`:

```go
// IsIP2Location reports whether a database type is an IP2Location LITE edition
// (updatable via the hardcoded IP2Location download endpoint).
func IsIP2Location(t Type) bool {
	switch t {
	case IP2LocationCountryType, IP2LocationCityType, IP2LocationASNType:
		return true

	default:
		return false
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/db/... -v`
Expected: PASS

- [ ] **Step 5: Lint and commit**

```bash
golangci-lint run --fix && golangci-lint run
git add internal/db/filename.go internal/db/type.go internal/db/db_test.go
git commit -m "feat(db): add IP2Location LITE taxonomy (Country/City/ASN)"
```

---

### Task 2: Config plumbing + file-code lookup — `internal/update`

**Files:**
- Modify: `internal/update/update.go`
- Modify: `internal/update/update_test.go`

**Interfaces:**
- Consumes: `db.IP2LocationCountryType`, `db.IP2LocationCityType`, `db.IP2LocationASNType`
  (Task 1).
- Produces: `Config.IP2LocationToken string`; `Updater.ip2locationToken string`;
  `Updater.ip2locationURL string` (test-overridable base URL, mirrors the existing
  `baseURL` field used for DB-IP); `ip2locationFileCode(dbType db.Type) (string, bool)`.
  Consumed by Task 4 (download) and Task 5 (routing/warn) and Task 6 (App → Config wiring).

- [ ] **Step 1: Write the failing test**

Add to `internal/update/update_test.go`:

```go
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

	_, ok = ip2locationFileCode(db.GeoIP2CityType)
	assert.Falsef(t, ok, "non-IP2Location type should not yield a file code")
}
```

Also extend `TestNewDefaultsAndCredentials` — add after the existing MaxMind-credentials
block:

```go
	// IP2Location token is stored as-is (no client object built, unlike MaxMind).
	u3, err := New(zaptest.NewLogger(t), Config{
		DBPath: dir, DBInfoFn: nopInfo,
		IP2LocationToken: "ip2loc-token",
	})
	require.NoError(t, err)
	assert.Equal(t, "ip2loc-token", u3.ip2locationToken)
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/update/... -run 'TestIP2LocationFileCode|TestNewDefaultsAndCredentials' -v`
Expected: FAIL to build — `undefined: ip2locationFileCode`, `u.ip2locationToken` field
doesn't exist.

- [ ] **Step 3: Implement**

In `internal/update/update.go`, add a new constant next to `dbipBaseURL`:

```go
	ip2locationBaseURL = "https://www.ip2location.com/download"
```

Add `IP2LocationToken string` to the `Config` struct (after `LicenseKey`):

```go
	Config struct {
		DBInfoFn         func() map[db.Filename]string
		DBPath           string
		LicenseKey       string
		IP2LocationToken string
		AccountID        int
		Frequency        time.Duration
		Timeout          time.Duration
	}
```

Add two fields to the `Updater` struct (after `baseURL`):

```go
	Updater struct {
		logger           *zap.Logger
		maxmind          *client.Client
		httpClient       *http.Client
		getDBInfo        func() map[db.Filename]string
		cancel           context.CancelFunc
		dbPath           string
		baseURL          string
		ip2locationURL   string
		ip2locationToken string
		wg               sync.WaitGroup
		frequency        time.Duration
		timeout          time.Duration
		closeOnce        sync.Once
	}
```

In `New`, extend the `Updater{...}` literal to set the new fields:

```go
	u := &Updater{
		logger:           logger,
		dbPath:           config.DBPath,
		httpClient:       &http.Client{},
		frequency:        config.Frequency,
		timeout:          config.Timeout,
		getDBInfo:        config.DBInfoFn,
		baseURL:          dbipBaseURL,
		ip2locationURL:   ip2locationBaseURL,
		ip2locationToken: config.IP2LocationToken,
	}
```

Add the lookup function near `dbipURL`:

```go
// ip2locationFileCode returns the IP2Location download file code for a type.
func ip2locationFileCode(dbType db.Type) (string, bool) {
	switch dbType {
	case db.IP2LocationCountryType:
		return "DB1LITEMMDB", true

	case db.IP2LocationCityType:
		return "DB11LITEMMDB", true

	case db.IP2LocationASNType:
		return "DBASNLITEMMDB", true

	default:
		return "", false
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/update/... -v`
Expected: PASS (full package, since the struct-literal change touches every test that
constructs `Updater{}` — confirm none of the existing tests broke)

- [ ] **Step 5: Lint and commit**

```bash
golangci-lint run --fix && golangci-lint run
git add internal/update/update.go internal/update/update_test.go
git commit -m "feat(update): add IP2Location token config and file-code lookup"
```

---

### Task 3: Zip extraction — `internal/update`

**Files:**
- Modify: `internal/update/update.go`
- Modify: `internal/update/update_test.go`

**Interfaces:**
- Produces: `extractMMDB(data []byte) (io.ReadCloser, error)`; `errIP2LocationEntryNotFound`.
  Consumed by Task 4.

- [ ] **Step 1: Write the failing test**

Add to `internal/update/update_test.go`:

```go
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
```

Add `"archive/zip"` to the test file's import block (alongside the existing `"bytes"`).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/update/... -run TestExtractMMDB -v`
Expected: FAIL to build — `undefined: extractMMDB`, `undefined: errIP2LocationEntryNotFound`

- [ ] **Step 3: Implement**

Add `"archive/zip"` and `"bytes"` to `internal/update/update.go`'s import block (alongside
the existing `"compress/gzip"`).

Add the sentinel error next to `errNoDBIPUrl`:

```go
	errIP2LocationEntryNotFound = errors.New("no .mmdb entry found in IP2Location zip")
```

Add the function near `writeAtomic`:

```go
// extractMMDB locates the .mmdb entry inside an IP2Location download zip
// (which also contains a license file and a README) and returns a reader over
// its decompressed contents. zip.NewReader needs random access for the
// central directory, so the whole archive must already be in memory — unlike
// DB-IP's plain gzip stream, this can't be handled as a single io.Reader pipe.
func extractMMDB(data []byte) (io.ReadCloser, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}

	for _, f := range zr.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".mmdb") {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open zip entry %s: %w", f.Name, err)
		}

		return rc, nil
	}

	return nil, errIP2LocationEntryNotFound
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/update/... -v`
Expected: PASS

- [ ] **Step 5: Lint and commit**

```bash
golangci-lint run --fix && golangci-lint run
git add internal/update/update.go internal/update/update_test.go
git commit -m "feat(update): extract the .mmdb entry from an IP2Location download zip"
```

---

### Task 4: Download + conditional fetch — `internal/update`

**Files:**
- Modify: `internal/update/update.go`
- Modify: `internal/update/update_test.go`

**Interfaces:**
- Consumes: `ip2locationFileCode` (Task 2), `extractMMDB`/`errIP2LocationEntryNotFound`
  (Task 3), `u.writeAtomic` (existing).
- Produces: `func (u *Updater) downloadIP2Location(ctx context.Context, dbType db.Type, filename db.Filename) (bool, error)`;
  `errNoIP2LocationCode`. Consumed by Task 5.

- [ ] **Step 1: Write the failing test**

Add to `internal/update/update_test.go`:

```go
func newIP2LocationUpdater(t *testing.T, baseURL, token string) *Updater {
	t.Helper()

	return &Updater{
		dbPath:           t.TempDir(),
		httpClient:       &http.Client{},
		timeout:          5 * time.Second,
		ip2locationURL:   baseURL,
		ip2locationToken: token,
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
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/update/... -run TestDownloadIP2Location -v`
Expected: FAIL to build — `undefined: u.downloadIP2Location`, `undefined: errNoIP2LocationCode`

- [ ] **Step 3: Implement**

Add the sentinel error next to `errIP2LocationEntryNotFound`:

```go
	errNoIP2LocationCode = errors.New("no IP2Location file code")
```

Add the function near `downloadDBIP`:

```go
// downloadIP2Location fetches an IP2Location LITE edition from its fixed
// download URL. The request is conditional (If-Modified-Since against the
// existing file's mtime — this survives the endpoint's redirect to its
// backing object storage, confirmed against a live account during design).
// On change, the response is a zip archive; extractMMDB pulls the .mmdb entry
// out before the atomic write.
func (u *Updater) downloadIP2Location(ctx context.Context, dbType db.Type, filename db.Filename) (bool, error) {
	code, ok := ip2locationFileCode(dbType)
	if !ok {
		return false, fmt.Errorf("%w for type %s", errNoIP2LocationCode, dbType)
	}

	url := fmt.Sprintf("%s?token=%s&file=%s", u.ip2locationURL, u.ip2locationToken, code)

	dctx, cancel := context.WithTimeout(ctx, u.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(dctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return false, fmt.Errorf("new request: %w", err)
	}

	if info, statErr := os.Stat(filepath.Join(u.dbPath, string(filename))); statErr == nil {
		req.Header.Set("If-Modified-Since", info.ModTime().UTC().Format(http.TimeFormat))
	}

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return false, nil

	case http.StatusOK:
		// proceed below
	default:
		// Never include `url` here — it carries the token. `code` identifies
		// the database without leaking the credential into logs.
		return false, fmt.Errorf("%w %s for IP2Location file %s", errUnexpectedStatus, resp.Status, code)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("read body: %w", err)
	}

	mmdb, err := extractMMDB(data)
	if err != nil {
		return false, fmt.Errorf("extract: %w", err)
	}
	defer mmdb.Close()

	if err = u.writeAtomic(filename, mmdb); err != nil {
		return false, fmt.Errorf("write: %w", err)
	}

	return true, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/update/... -v`
Expected: PASS

- [ ] **Step 5: Lint and commit**

```bash
golangci-lint run --fix && golangci-lint run
git add internal/update/update.go internal/update/update_test.go
git commit -m "feat(update): download and extract IP2Location LITE editions"
```

---

### Task 5: Routing + unconfigured warning — `internal/update`

**Files:**
- Modify: `internal/update/update.go`
- Modify: `internal/update/update_test.go`

**Interfaces:**
- Consumes: `db.IsIP2Location` (Task 1), `u.downloadIP2Location` (Task 4).
- Produces: `updateAll` now dispatches IP2Location databases;
  `warnIfIP2LocationUnconfigured()` called from `Start()`.

- [ ] **Step 1: Write the failing test**

Add to `internal/update/update_test.go`:

```go
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

	var hits int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	// No token configured: updateAll must not hit the server at all.
	u := ip2locationUpdaterWithFile(t, srv.URL, "")
	u.updateAll(t.Context(), false)
	assert.Zerof(t, atomic.LoadInt32(&hits), "no token configured -> IP2Location databases must be skipped")

	// Token configured: updateAll must check it.
	u2 := ip2locationUpdaterWithFile(t, srv.URL, "tok")
	u2.updateAll(t.Context(), false)
	assert.NotZerof(t, atomic.LoadInt32(&hits), "token configured -> IP2Location databases must be checked")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/update/... -run TestUpdateAllIP2LocationGatedByToken -v`
Expected: FAIL — `hits` is nonzero even with no token (the switch in `updateAll` doesn't
recognise IP2Location types yet, so nothing runs and the assertion for the *first* subcase
trivially passes, but the *second* subcase fails: `hits` stays zero because nothing routes
to `downloadIP2Location` at all)

- [ ] **Step 3: Implement**

In `updateAll`'s switch (`internal/update/update.go`), add a case after the `db.IsDBIP`
case:

```go
		case db.IsIP2Location(dbType):
			if u.ip2locationToken == "" {
				u.logger.Debug("skipping IP2Location database; no token configured",
					logfields.Database(string(filename)),
				)

				continue
			}

			u.run(filename, func() (bool, error) { return u.downloadIP2Location(ctx, dbType, filename) })
		}
```

Add a new function after `warnIfMaxmindUnconfigured`:

```go
// warnIfIP2LocationUnconfigured warns once at startup when IP2Location
// databases are present but no token is configured, so the operator knows
// those files will silently never be auto-updated (the per-cycle skip is only
// logged at debug).
func (u *Updater) warnIfIP2LocationUnconfigured() {
	if u.ip2locationToken != "" {
		return
	}

	for filename := range u.getDBInfo() {
		if db.IsIP2Location(db.ToType(filename)) {
			u.logger.Warn("IP2Location databases present but no token configured; " +
				"they will not be auto-updated")

			return
		}
	}
}
```

In `Start()`, call it alongside the existing MaxMind warning:

```go
	u.warnIfMaxmindUnconfigured()
	u.warnIfIP2LocationUnconfigured()
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/update/... -v`
Expected: PASS (full package)

- [ ] **Step 5: Lint and commit**

```bash
golangci-lint run --fix && golangci-lint run
git add internal/update/update.go internal/update/update_test.go
git commit -m "feat(update): route IP2Location databases through the periodic updater"
```

---

### Task 6: Caddyfile / app wiring — `app`

**Files:**
- Modify: `app/app.go`
- Modify: `app/app_test.go`

**Interfaces:**
- Consumes: `update.Config.IP2LocationToken` (Task 2).
- Produces: `App.IP2LocationToken string` (Caddyfile directive `ip2location_token`).

- [ ] **Step 1: Write the failing test**

Extend the Caddyfile in `TestUnmarshalCaddyfile` (`app/app_test.go`):

```go
	d := caddyfile.NewTestDispenser(`
		geo_ops {
			db_path            /var/lib/geoip
			auto_update
			account_id         12345
			license_key        secret-key
			ip2location_token  ip2loc-secret
			update_frequency   12h
			update_timeout     20s
		}
	`)

	var a app.App
	require.NoError(t, a.UnmarshalCaddyfile(d))

	assert.Equal(t, "/var/lib/geoip", a.DBPath)
	assert.True(t, a.AutoUpdate)
	assert.Equal(t, 12345, a.AccountID)
	assert.Equal(t, "secret-key", a.LicenseKey)
	assert.Equal(t, "ip2loc-secret", a.IP2LocationToken)
	assert.Equal(t, 12*time.Hour, time.Duration(a.UpdateFrequency))
	assert.Equal(t, 20*time.Second, time.Duration(a.UpdateTimeout))
```

Add two cases to `TestValidate`'s `cases` map:

```go
		"ip2location token only + auto_update": {
			app:     app.App{AutoUpdate: true, IP2LocationToken: "t"},
			wantErr: require.NoError,
		},
		"ip2location token without auto_update (warns, no error)": {
			app:     app.App{IP2LocationToken: "t"},
			wantErr: require.NoError,
		},
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./app/... -run 'TestUnmarshalCaddyfile|TestValidate' -v`
Expected: FAIL to build — `a.IP2LocationToken undefined (type app.App has no field or method IP2LocationToken)`

- [ ] **Step 3: Implement**

In `app/app.go`, add a field to the `App` struct (after `LicenseKey`):

```go
		LicenseKey      string         `json:"license_key,omitempty"`
		IP2LocationToken string        `json:"ip2location_token,omitempty"`
```

(Re-run `gofmt`/`golangci-lint run --fix` afterward to fix struct-tag column alignment —
don't hand-align it.)

In `UnmarshalCaddyfile`'s switch, add a case after `"license_key"`:

```go
		case "ip2location_token":
			if !dispenser.NextArg() {
				return fmt.Errorf("ip2location_token requires an argument: %w", dispenser.ArgErr())
			}

			a.IP2LocationToken = dispenser.Val()
```

Update the doc comment above `UnmarshalCaddyfile` to include the new directive:

```go
//	geo_ops {
//	    db_path            /var/lib/geoip
//	    auto_update
//	    account_id         123456
//	    license_key        xxxxxxxx
//	    ip2location_token  xxxxxxxx
//	    update_frequency   24h
//	    update_timeout     30s
//	}
```

In `Start()`, thread the token through to `update.Config`:

```go
	if a.AutoUpdate {
		if err := a.ops.StartUpdater(update.Config{
			AccountID:        a.AccountID,
			LicenseKey:       a.LicenseKey,
			IP2LocationToken: a.IP2LocationToken,
			Frequency:        time.Duration(a.UpdateFrequency),
			Timeout:          time.Duration(a.UpdateTimeout),
		}); err != nil {
```

In `Validate()`, extend the "credentials without auto_update" warning condition:

```go
	// Credentials configured but auto_update is off: harmless, but they have no
	// effect, so warn rather than fail.
	if (a.AccountID > 0 || a.LicenseKey != "" || a.IP2LocationToken != "") && a.logger != nil {
		a.logger.Warn("account_id/license_key/ip2location_token are set but auto_update is disabled; they have no effect")
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./app/... -v`
Expected: PASS

- [ ] **Step 5: Lint and commit**

```bash
golangci-lint run --fix && golangci-lint run
git add app/app.go app/app_test.go
git commit -m "feat(app): add ip2location_token Caddyfile option"
```

---

### Task 7: Documentation — `README.md`, `CLAUDE.md`

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`

**Interfaces:**
- Consumes: filenames/config field names/log semantics from Tasks 1–6 (no code interfaces
  produced — this task is docs-only, verify by reading the rendered result, not by `go
  test`).

- [ ] **Step 1: Update `README.md`'s intro and feature list**

Replace (line 13-14):

```markdown
A [Caddy](https://caddyserver.com) plugin for IP geolocation. It loads MaxMind **GeoIP2 /
GeoLite2** and **DB-IP** `.mmdb` databases and gives you:
```

with:

```markdown
A [Caddy](https://caddyserver.com) plugin for IP geolocation. It loads MaxMind **GeoIP2 /
GeoLite2**, **DB-IP**, and **IP2Location** `.mmdb` databases and gives you:
```

Replace (line 19):

```markdown
- automatic, scheduled **database updates** (MaxMind and DB-IP) plus **hot-reload** when a
```

with:

```markdown
- automatic, scheduled **database updates** (MaxMind, DB-IP, and IP2Location) plus
  **hot-reload** when a
```

- [ ] **Step 2: Add a Contents entry**

In the Contents list, after `- [DB-IP](#db-ip)`, add:

```markdown
  - [IP2Location](#ip2location)
```

- [ ] **Step 3: Add the "IP2Location" getting-databases section**

After the existing `### DB-IP` section (ends just before `### Supported editions & required
filenames`), insert:

```markdown
### IP2Location

IP2Location LITE databases are free but require a personal **download token** (register at
<https://lite.ip2location.com/ip2location-lite>, then find your token in the account
download area).

1. Download the **MMDB** format of the editions you want — Country (DB1), City (DB11), or
   ASN — either manually from the LITE site, or automate it:

   ```sh
   curl -L "https://www.ip2location.com/download?token=$TOKEN&file=DB11LITEMMDB" -o db11.zip
   ```

2. The download is a **zip archive** containing a license file, a README, and the `.mmdb`
   itself (e.g. `IP2LOCATION-LITE-DB11.MMDB`). Extract it and rename to the plugin's
   canonical filename:

   ```sh
   unzip -j db11.zip '*.MMDB' -d /var/lib/geoip
   mv /var/lib/geoip/IP2LOCATION-LITE-DB11.MMDB /var/lib/geoip/ip2location-city.mmdb
   ```

   (Auto-update writes this canonical name for you — see
   [Automatic updates](#automatic-updates).)

> IP2Location LITE uses the same field schema as MaxMind's GeoIP2/GeoLite2 for these three
> editions — `{geo.ip2location-city.*}` resolves the same field paths as
> `{geo.geoip2-city.*}` (see [Field reference by edition](#field-reference-by-edition));
> only the underlying geolocation data differs between vendors.
```

- [ ] **Step 4: Add rows to the supported-editions table**

After the `DB-IP ASN Lite` row, add:

```markdown
| IP2Location Country (DB1) | `ip2location-country.mmdb` | IP2Location LITE (free) | needs token |
| IP2Location City (DB11) | `ip2location-city.mmdb` | IP2Location LITE (free) | needs token |
| IP2Location ASN | `ip2location-asn.mmdb` | IP2Location LITE (free) | needs token |
```

- [ ] **Step 5: Update the Configuration section**

Replace the global-options example:

```caddyfile
{
	order geo_ops first
	geo_ops {
		db_path          /var/lib/geoip   # required: directory holding the *.mmdb files
		auto_update                       # optional: enable scheduled downloads
		account_id       123456           # MaxMind Account ID (only with auto_update)
		license_key      {env.MAXMIND_KEY}  # MaxMind license key (only with auto_update)
		update_frequency 24h              # how often to check (default 24h)
		update_timeout   1m               # per-download timeout (default 30s)
	}
}
```

with:

```caddyfile
{
	order geo_ops first
	geo_ops {
		db_path            /var/lib/geoip   # required: directory holding the *.mmdb files
		auto_update                         # optional: enable scheduled downloads
		account_id         123456           # MaxMind Account ID (only with auto_update)
		license_key        {env.MAXMIND_KEY}   # MaxMind license key (only with auto_update)
		ip2location_token  {env.IP2LOCATION_TOKEN}  # IP2Location download token (only with auto_update)
		update_frequency   24h              # how often to check (default 24h)
		update_timeout     1m               # per-download timeout (default 30s)
	}
}
```

Add a row to the option table, after `license_key`:

```markdown
| `ip2location_token` | IP2Location LITE download token. Required to update IP2Location editions. | — |
```

Replace:

```markdown
> Use `{env.VAR}` to keep the license key out of the Caddyfile.
```

with:

```markdown
> Use `{env.VAR}` to keep the license key and IP2Location token out of the Caddyfile.
```

In "### Automatic updates", add a bullet after the DB-IP one:

```markdown
- **IP2Location** LITE editions are refreshed from IP2Location's download endpoint and
  require `ip2location_token`. Without a token, IP2Location files are left untouched.
```

- [ ] **Step 6: Extend the Field reference by edition section**

In the `**City**`, `**Country**`, and `**ASN**` edition-list headers, add the matching
IP2Location filename (the field blocks themselves are unchanged — the schema is identical):

```markdown
**City** (`geoip2-city`, `geolite2-city`, `dbip-city-lite`, `ip2location-city`)
```

```markdown
**Country** (`geoip2-country`, `geolite2-country`, `dbip-country-lite`, `ip2location-country`)
```

```markdown
**ASN** (`geolite2-asn`, `dbip-asn-lite`, `ip2location-asn`)
```

- [ ] **Step 7: Update the "A complete Caddyfile" / "JSON configuration" examples**

In the complete-Caddyfile example's `geo_ops` block, add a line after `license_key`:

```caddyfile
		ip2location_token {env.IP2LOCATION_TOKEN}
```

In the JSON configuration example, add a field after `"license_key"`:

```json
      "ip2location_token": "…",
```

- [ ] **Step 8: Update Privacy and FAQ mentions**

Replace:

```markdown
- **It does not transmit it.** The auto-updater only downloads databases from MaxMind / DB-IP;
```

with:

```markdown
- **It does not transmit it.** The auto-updater only downloads databases from
  MaxMind / DB-IP / IP2Location;
```

Replace:

```markdown
  (case-insensitive). DB-IP files in particular must be renamed to drop the date suffix.
```

with:

```markdown
  (case-insensitive). DB-IP files in particular must be renamed to drop the date suffix;
  IP2Location files must be extracted from their download zip and renamed to the canonical
  name.
```

Replace:

```markdown
- MaxMind needs both `account_id` and `license_key`; with only one set, the configuration
  is rejected at startup. DB-IP needs neither.
```

with:

```markdown
- MaxMind needs both `account_id` and `license_key`; with only one set, the configuration
  is rejected at startup. DB-IP needs no credentials. IP2Location needs a single
  `ip2location_token`.
```

- [ ] **Step 9: Update `CLAUDE.md`**

In **Requirements**, replace:

```markdown
- Handle multiple mmdb databases: MaxMind GeoIP2 and GeoLite2, and DB-IP databases.
```

with:

```markdown
- Handle multiple mmdb databases: MaxMind GeoIP2 and GeoLite2, DB-IP, and IP2Location
  databases.
```

In **Auto-update**'s "Per vendor:" list, add a bullet after the DB-IP one:

```markdown
- **IP2Location (LITE)** — hardcoded download endpoint
  (`https://www.ip2location.com/download`); requires an operator-supplied **download
  token**. Conditional via `If-Modified-Since` (confirmed to survive the endpoint's
  redirect to its backing object storage). The response is a zip archive (license + README
  + the `.mmdb`); the updater extracts the `.mmdb` entry before the atomic write — unlike
  DB-IP's plain gzip stream, this needs the whole response buffered first (`archive/zip`
  needs random access for the central directory).
```

Replace:

```markdown
MaxMind credentials gate only MaxMind downloads; DB-IP updates regardless. Frequency and
timeout are configurable (non-positive values are defaulted).
```

with:

```markdown
MaxMind credentials gate only MaxMind downloads; DB-IP updates regardless of credentials;
IP2Location updates only when `ip2location_token` is set. Frequency and timeout are
configurable (non-positive values are defaulted).
```

In **Database file naming**, add a paragraph after the existing DB-IP note:

```markdown
IP2Location's free LITE downloads are delivered as a zip archive (containing a license
file, a README, and the `.mmdb` itself) and must be extracted and renamed to the canonical
name (`ip2location-country.mmdb`, `ip2location-city.mmdb`, `ip2location-asn.mmdb`) if
placed manually. The auto-updater already extracts and writes the canonical name.
```

In the **Caddyfile** example block, add a line after `license_key`:

```caddyfile
    ip2location_token xxxxxxxx         # IP2Location, only with auto_update
```

- [ ] **Step 10: Proofread and commit**

Read through both files' diffs once for consistency (heading levels, table column
alignment, that every new cross-reference anchor — `#ip2location` — actually matches the
heading you added), then:

```bash
git add README.md CLAUDE.md
git commit -m "docs: document IP2Location LITE support"
```

---

## Follow-up (not part of this plan)

IP2Proxy PX10 support is out of scope (see the spec's Non-goals) — track separately once
its MMDB availability and paid-account auth are confirmed.
