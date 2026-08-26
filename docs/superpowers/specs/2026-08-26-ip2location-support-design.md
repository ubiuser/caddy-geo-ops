# IP2Location database support — design

- **Date:** 2026-08-26
- **Status:** approved, pending implementation plan
- **Issue:** [ubiuser/caddy-geo-ops#12](https://github.com/ubiuser/caddy-geo-ops/issues/12)

## Problem / motivation

Issue #12 asks for IP2Location's MMDB databases to be supported alongside the existing
MaxMind (GeoIP2/GeoLite2) and DB-IP vendors: same file format, same generic decode path,
different taxonomy and a different (and previously undocumented) download mechanism.

## Goals

- Recognise, load, hot-reload, and auto-update the three free **IP2Location LITE** MMDB
  editions: Country (DB1), City (DB11), and ASN.
- Reuse the existing generic mmdb decode path unchanged — no new placeholder logic, no
  new record types. IP2Location's field names simply show up under their own
  `{geo.ip2location-*.<field>}` prefix like any other database.

## Non-goals

- **IP2Proxy PX10** is out of scope for this change. It's a paid-only product and public
  docs are inconsistent about whether it even ships in MMDB format (a boilerplate note on
  IP2Location's own FAQ says MMDB is "supported for DB1 and DB9," which contradicts the
  confirmed MMDB availability on DB11 and ASN's own product pages). Revisit as a follow-up
  issue once someone can confirm its format and auth against a real paid account.
- No git submodule of real IP2Location fixture data. `github.com/ip2location/sample-databases`
  was investigated as a `MaxMind-DB`-style fixture source (per the issue reporter's
  suggestion) and confirmed to contain only `.bin`/`.csv`/`.cidr.csv` samples — no `.mmdb`
  files and no ASN folder at all. It cannot serve as an MMDB test-fixture source. The real
  LITE files are also large (~15MB decompressed for DB1 alone), too large to commit as repo
  test fixtures the way MaxMind's purpose-built tiny test databases are. Verified live
  against the issue reporter's own free LITE download token (used only for verification
  during design, discarded afterward, never committed or logged).

## Verified download mechanics

The download API is not documented in detail publicly, so it was verified with a live
token during design:

- Endpoint: `https://www.ip2location.com/download?token=<TOKEN>&file=<CODE>`
- File codes, confirmed by live request:

  | Database | Filename (this module) | `db.Type` | IP2Location file code |
  |---|---|---|---|
  | Country | `ip2location-country.mmdb` | `IP2LocationCountryType` | `DB1LITEMMDB` |
  | City | `ip2location-city.mmdb` | `IP2LocationCityType` | `DB11LITEMMDB` |
  | ASN | `ip2location-asn.mmdb` | `IP2LocationASNType` | `DBASNLITEMMDB` |

- The endpoint responds **302** to a presigned Cloudflare R2 URL (e.g.
  `.../IP2LOCATIONLITE14/IPV4/IP2LOCATION-LITE-DB1.MMDB.ZIP?X-Amz-...`, valid ~10 minutes).
  Go's `http.Client` follows this automatically (default redirect policy); no special
  handling needed.
- The final response is a real `application/zip` (`Content-Type`), with `ETag` and
  `Last-Modified` present.
- The zip contains **three entries**: `LICENSE-CC-BY-SA-4.0.TXT`, `README_LITE.TXT`, and
  `IP2LOCATION-LITE-<CODE>.MMDB` — the last one is genuine MMDB-format data (distinct from
  IP2Location's separate proprietary `.BIN` product, which uses a different file code
  ending `LITEBIN` that this module never requests). Extraction must select the entry by
  `.mmdb` suffix (case-insensitive), not assume a single-entry archive.
- **Conditional GET works end-to-end**: sending `If-Modified-Since` on the initial request
  to `www.ip2location.com` survives the redirect to R2 and correctly yields a bare
  **304 Not Modified**. So the fetch can mirror DB-IP's `fetchDBIP` conditional pattern
  exactly, using the local file's mtime like DB-IP does today — and it's actually simpler
  than DB-IP, since there's no month-in-URL / previous-month-fallback loop: one fixed URL
  per database, always current.

## Design

### 1. Taxonomy (`internal/db`)

Add to `filename.go`:

```go
IP2LocationCountry Filename = "ip2location-country.mmdb"
IP2LocationCity     Filename = "ip2location-city.mmdb"
IP2LocationASN      Filename = "ip2location-asn.mmdb"
```

Add to `type.go`:

```go
IP2LocationCountryType Type = "IP2Location-Country"
IP2LocationCityType    Type = "IP2Location-City"
IP2LocationASNType     Type = "IP2Location-ASN"
```

Extend `ToType`'s switch with the three new cases, and add:

```go
// IsIP2Location reports whether a database type is an IP2Location LITE edition
// (updatable via the hardcoded IP2Location download endpoint).
func IsIP2Location(t Type) bool
```

mirroring `IsDBIP`. These `db.Type` string values are internal labels only — IP2Location's
download API is keyed by file *code* (`DB1LITEMMDB`, ...), not by these strings, so unlike
MaxMind's `dbType` (which is passed verbatim to `geoipupdate`'s client) there's no external
contract on their spelling.

### 2. Download mechanics (`internal/update`)

New constant and `Config`/`Updater` fields, parallel to the existing DB-IP/MaxMind ones:

```go
const ip2locationBaseURL = "https://www.ip2location.com/download"

// Config gains:
IP2LocationToken string

// Updater gains:
ip2locationToken string
```

New unexported helper mapping `db.Type` → IP2Location file code (mirrors `dbipURL`'s
switch shape):

```go
func ip2locationFileCode(dbType db.Type) (string, bool)
```

New top-level fetch, mirroring `downloadDBIP`/`fetchDBIP` but without the month-fallback
loop (single fixed URL per database):

```go
func (u *Updater) downloadIP2Location(ctx context.Context, dbType db.Type, filename db.Filename) (bool, error)
```

It builds the URL (`ip2locationBaseURL?token=...&file=...`), performs one conditional GET
with `If-Modified-Since` set from the existing local file's mtime (same as `fetchDBIP`),
and on 200:

1. Reads the full response body into memory (`io.ReadAll` — files are tens of MB at most,
   consistent with the existing DB-IP/MaxMind download sizes; no new size-capping logic,
   matching the existing threat model of admin-configured trusted vendor endpoints).
2. Opens it with `archive/zip.NewReader` (needs `io.ReaderAt` + size, hence the buffering —
   zip's central directory makes it non-streamable, unlike DB-IP's gzip).
3. Finds the entry whose name has a `.mmdb` suffix (case-insensitive); errors with a new
   sentinel (e.g. `errIP2LocationEntryNotFound`) if none is present — this is the "vendor
   changed their zip layout" failure mode, logged at `Warn` by the existing `run` dispatch
   (falls into its generic `err != nil` branch) same as any other update failure.
4. Opens that entry (`zip.File.Open()`, itself an `io.ReadCloser`) and passes it straight
   into the existing `writeAtomic` unchanged — no changes needed there.

This extraction step (steps 2–3) is factored into its own small function
(e.g. `extractMMDB(data []byte) (io.ReadCloser, error)`) so it's unit-testable against a
synthetic in-memory zip without needing network access or real IP2Location data.

`updateAll`'s switch gains a case:

```go
case db.IsIP2Location(dbType):
    if u.ip2locationToken == "" {
        u.logger.Debug("skipping IP2Location database; no token configured", logfields.Database(string(filename)))
        continue
    }
    u.run(filename, func() (bool, error) { return u.downloadIP2Location(ctx, dbType, filename) })
```

`warnIfMaxmindUnconfigured` gets a twin, `warnIfIP2LocationUnconfigured`, called from
`Start()` alongside it: warns once at startup if IP2Location databases are present in
`db_path` but no token is configured, so an operator doesn't silently get stale data.

### 3. Caddyfile / app surface (`app`)

New field, parallel to `LicenseKey`:

```go
IP2LocationToken string `json:"ip2location_token,omitempty"`
```

`UnmarshalCaddyfile` gains an `"ip2location_token"` case (single required argument, same
shape as `license_key`). `Start` passes it through to `update.Config`. No new `Validate`
pairing rule is needed — it's a single credential, not a two-part pair like MaxMind's
account-id/license-key. The existing "credentials set but auto_update disabled" warning in
`Validate` is extended to also check `IP2LocationToken != ""`.

Caddyfile example addition:

```caddyfile
geo_ops {
    db_path          /var/lib/geoip
    auto_update
    ip2location_token xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
}
```

### 4. Testing

Mirrors the existing DB-IP test coverage in shape:

- `internal/db/db_test.go`: extend the existing filename/type table with the three
  IP2Location entries (`Key()`, `ToType`, `IsIP2Location`, mutual-exclusivity with
  MaxMind/DB-IP classifiers).
- `internal/update/update_test.go`:
  - `TestIP2LocationFileCode` — code lookup for all three types + unknown type, mirrors
    `TestDBIPURL`.
  - `TestExtractMMDB` — valid zip with 3 entries picks the `.mmdb` one; zip with no
    `.mmdb` entry returns `errIP2LocationEntryNotFound`; corrupt/non-zip bytes return an
    error. Pure function, no network.
  - `TestDownloadIP2Location` — happy path against an `httptest.Server` serving a
    synthetic zip fixture (license/readme/mmdb entries with dummy bytes, built with
    `archive/zip.Writer` in the test).
  - `TestDownloadIP2LocationNotModified` — conditional GET, 304 short-circuits.
  - `TestDownloadIP2LocationUnexpectedStatus` — non-200/304/404 is a real error.
  - `TestDownloadIP2LocationContextCanceled` — mirrors the DB-IP equivalent.
- `app/app_test.go`: add an "ip2location only (auto_update, no maxmind/dbip creds)" case
  to the existing config table, mirroring the existing "db-ip only" case.

No real-data decode test is added (consistent with the current DB-IP coverage, which has
none either, per the non-goals above).

### 5. Docs

- `README.md`: config option table/example gains `ip2location_token`; the placeholder
  scheme examples gain an IP2Location line. **Field schema confirmed identical to
  MaxMind's** (updated after this spec was first written): during plan authoring, the
  three real IP2Location LITE MMDB files were downloaded with a live token and decoded
  against the test IP `81.2.69.142` via `maxminddb-golang/v2`. Results were field-for-field
  identical to GeoIP2/GeoLite2's schema — `continent.code`/`continent.names.*`,
  `country.iso_code`/`country.names.*`, `registered_country.*` (Country); those plus
  `city.names.en`, `location.latitude`/`longitude`/`time_zone`, `postal.code`,
  `subdivisions.0.iso_code`/`names.*` (City); `autonomous_system_number`/
  `autonomous_system_organization` (ASN). So `{geo.ip2location-city.city.names.en}` etc.
  resolve the same field paths as `{geo.geoip2-city.city.names.en}` — no placeholder guess
  needed; the README's Field reference by edition section adds `ip2location-*` to the
  existing City/Country/ASN edition lists rather than duplicating field blocks.
- `CLAUDE.md`: **Auto-update** section gains an "IP2Location" bullet (LITE-only, single
  token, no MaxMind-style account/license pairing, conditional via `If-Modified-Since`,
  zip-wrapped so needs extraction unlike DB-IP's plain gzip). **Database file naming**
  section gains the three new canonical filenames. **Caddyfile** example gains
  `ip2location_token`.

## Open follow-up (not part of this change)

- IP2Proxy PX10 support, once its MMDB availability and paid-account auth flow are
  confirmed — track as a separate issue.
