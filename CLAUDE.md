# CLAUDE.md

Guidance for working in this repository. For user-facing documentation (install, configure,
placeholders, handler/matcher and CEL examples), see [README.md](README.md).

## Goal

A Caddy plugin for geo-location: an `http.handler` and an `http.matcher`, plus value
placeholders, backed by a shared Caddy **app** that owns the geo-IP databases.

Module path: `github.com/ubiuser/caddy-geo-ops` (Go 1.26).

## Requirements

- Handle multiple mmdb databases: MaxMind GeoIP2 and GeoLite2, and DB-IP databases.
- Manage automatic (periodic remote downloads) and manual (local file copies) updates,
  and hot-reload a database when its file changes.
- Derive the client IP honoring configurable forwarding headers (`X-Forwarded-For`,
  `X-Real-IP`, …).
- Expose all available database fields as placeholders usable in the Caddyfile.
- Share database providers between the handler and matcher via a Caddy app.

## Architecture

A single shared app owns all database state; the handler and matcher are thin consumers
that fetch it via `ctx.App(...)`.

```
app/        geo_ops                 Caddy app — owns Ops; lifecycle Provision/Validate/Start/Stop/Cleanup
  └─ internal/ops                   Ops: provider registry keyed by db.Filename, RWMutex-guarded, atomic swap on reload
       ├─ internal/db               Filename ⇄ Type taxonomy across GeoIP2 / GeoLite2 / DB-IP
       ├─ internal/replacers        generic flatten of a decoded record into dotted-path placeholder keys
       ├─ internal/dirmonitor       fsnotify watcher with debounce → triggers reload
       └─ internal/update           periodic updater (MaxMind via geoipupdate/v7; DB-IP via hardcoded URLs)
handler/    http.handlers.geo_ops   middleware that sets {geo.*} placeholders on the request
  └─ internal/clientip              client IP from Caddy core's resolved value
matcher/    http.matchers.geo_ops   request matcher over geo fields
  └─ internal/clientip              client IP from Caddy core's resolved value
```

Registration IDs (keep stable): app `geo_ops`; handler `http.handlers.geo_ops` (Caddyfile
directive `geo_ops`); matcher `http.matchers.geo_ops`.

**Reload trigger.** `internal/dirmonitor` watches the db folder with fsnotify (not polling)
and debounces event bursts, then signals `Ops`. It decides update vs. delete from the
file's final state, so an atomic-save (remove+create) coalesces into a single update rather
than a delete/update flap. This covers both manual file copies and in-place refreshes from
`internal/update`.

**Concurrency / validate-before-swap.** Requests read providers under `RWMutex.RLock`
(`Ops` holds a value `sync.RWMutex`; it is only ever used via `*Ops`). On reload, the new
reader is built and validated **fully before** the write lock is taken; then the single
changed provider is swapped and the old one closed *after* unlocking. If the new file fails
to load, the existing provider keeps serving — a failed reload never drops a working db.

## Placeholders

Scheme: `{geo.<db>.<dotted field path>}`, where `<db>` is the database filename without
extension (`db.Filename.Key()`), so multiple loaded databases never collide. The field path
mirrors the mmdb record structure; slices index numerically.

Examples (against `geoip2-city.mmdb`, IP `81.2.69.142`):

- `{geo.geoip2-city.country.iso_code}` → `GB`
- `{geo.geoip2-city.city.names.en}` → `London`
- `{geo.geoip2-city.location.latitude}` → `51.5142`
- `{geo.geoip2-city.subdivisions.0.iso_code}` → `ENG`

A missing field resolves to empty, never an error: `handler/handler.go` registers one
`repl.Map` provider that returns `""` for any unset key under the `geo.` root (so we don't
pre-populate thousands of keys, and unknown `geo.*` references don't fail).

The handler must run **before** anything that consumes its placeholders — use
`order geo_ops first` (or `before <directive>`) in the Caddyfile.

## Client IP / forwarding headers

Take the client IP from Caddy core, don't parse headers:

```go
ipStr, _ := caddyhttp.GetVar(r.Context(), caddyhttp.ClientIPVarKey).(string)
addr, _ := netip.ParseAddr(ipStr)
```

`ClientIPVarKey` already honors the operator's global `trusted_proxies` / `client_ip_headers`
(`X-Forwarded-For`, `X-Real-IP`, `CF-Connecting-IP`, …), strict right-to-left mode, IPv6 zone
stripping, and port handling — exactly the "configurable forwarding headers" requirement, and
what the maintained matcher modules (`caddy-maxmind-geolocation`, `ipfilter-caddy`) do. The
value is a canonical bare IP, or `""`/`"@"` (Unix socket) — `internal/clientip` declines those
(no geo lookup). Never read `r.RemoteAddr` or trust `X-Forwarded-For` directly — spoofable
behind a proxy.

## mmdb reader library

Use `github.com/oschwald/maxminddb-golang/v2` only — **not** `github.com/IncSW/geoip2`. The
typed `geoip2-golang/v2` wrapper is not a dependency: the generic decode below serves both
handler and matcher uniformly (it remains a reasonable future addition if strongly-typed
records are ever wanted).

The "expose all fields" requirement is met by `maxminddb/v2`'s generic
`reader.Lookup(addr).Decode(&record)` into a `map[string]any`: every field — including DB-IP
quirks, `uint128` (`*big.Int`), and future MaxMind fields — is reachable with no per-field
code. `internal/replacers.Flatten` walks the nested maps/slices/scalars into dotted-path
keys. A not-found IP leaves the record empty with no error.

**Windows / reload caveat: do NOT use `maxminddb.Open()` (mmap).** On Windows an mmap'd file
holds a handle that blocks the atomic `os.Rename` the updater uses to swap in a new db. Read
the file into a `[]byte` and use `maxminddb.OpenBytes(data)` (note: v2 spells it `OpenBytes`,
not `FromBytes`). This also keeps the load-validate-swap clean — the new reader exists fully
in memory before the write lock. See `internal/ops/provider.go`.

IPs are `netip.Addr` (`netip.ParseAddr` / `MustParseAddr`), not `net.IP`.

## Database file naming

Databases are recognized by **filename** (case-insensitive); each file in `db_path` must be
named after its `internal/db` constant — `geoip2-city.mmdb`, `geolite2-asn.mmdb`,
`dbip-city-lite.mmdb`, etc. Unrecognized names are silently ignored.

This matters for DB-IP: free downloads carry a month suffix (`dbip-city-lite-2024-06.mmdb`)
and must be renamed to the un-dated form when placed manually. Intentional — one fixed name
per edition means a new download or manual copy **overwrites** the previous file instead of
piling up dated versions. (The auto-updater already writes the canonical name, so this only
affects manual copies; MaxMind's extracted filenames already match.)

## Auto-update

The updater refreshes only databases **already present** in `db_path` — it never downloads
new types. Each pass lists existing `*.mmdb` files and, for each recognized type, fetches a
fresh copy and writes it to a temp file then `os.Rename`s over the target (atomic); the
watcher then reloads it.

- **Initial pass (on `Start`)** refreshes only databases whose file mtime is already older
  than the update frequency — a cold start with a stale db updates promptly, but a config
  reload (which re-runs `Start`) won't re-check fresh files, staying within MaxMind's
  ~2-checks/day guidance.
- **Periodic ticks** check regardless of age.
- **Shutdown** cancels the in-flight download via context, so a reload/shutdown mid-update
  returns promptly. `Stop` is idempotent.
- **Startup cleanup** removes crash-orphaned `*.tmp-*` files older than the download timeout
  (the age gate avoids deleting a concurrent in-flight temp during a reload).

Per vendor:

- **MaxMind (GeoIP2 / GeoLite2)** — `geoipupdate/v7` client; requires operator-supplied
  **Account ID** + **License Key**. Conditional via MD5; client returns a decompressed mmdb.
- **DB-IP** — hardcoded monthly URLs (DB-IP Lite is public, no auth); conditional via
  `If-Modified-Since`, gzip-decompressed. Falls back to the previous month if the current
  month isn't published yet (start-of-month gap); if neither exists it's a benign skip.

MaxMind credentials gate only MaxMind downloads; DB-IP updates regardless. Frequency and
timeout are configurable (non-positive values are defaulted).

## Caddyfile

App config is a global option; the handler is a directive; the matcher is a named matcher.

```caddyfile
{
    order geo_ops first
    geo_ops {
        db_path          /var/lib/geoip
        auto_update                       # optional
        account_id       123456           # MaxMind, only with auto_update
        license_key      xxxxxxxx         # MaxMind, only with auto_update
        update_frequency 24h
        update_timeout   30s
    }
}

example.com {
    geo_ops                               # sets {geo.*} placeholders

    @us geo_ops {
        geoip2-country.country.iso_code US
    }
    respond @us "hello from the US"

    header X-Country {geo.geoip2-country.country.iso_code}
}
```

Behind a proxy, configure client-IP trust with the standard global
`servers { trusted_proxies ... }` / `client_ip_headers ...` options.

## Build / Test / Run

Dependencies are pinned to published versions, so building into Caddy with `xcaddy` needs
no replace flags:

```sh
xcaddy build --with github.com/ubiuser/caddy-geo-ops=.
./caddy run --config ./Caddyfile
```

```sh
golangci-lint run --fix && golangci-lint run   # lint + format (must be clean)
(cd e2e && golangci-lint run --fix)            # e2e is a separate module — lint it too
go test -race ./...                            # unit tests (root module)
```

### Test layout — two modules, deliberately

- **Plugin module (root)** holds the code and all **unit tests**, with a lean `go.mod`
  (only what the plugin imports). Logic packages (`internal/*`) and the Caddyfile parsers
  (`app`, `matcher`) plus the handler (via a small `geoLookuper` interface seam) are tested
  directly. DB-backed tests copy fixtures from the **MaxMind-DB git submodule** at the repo
  root (`MaxMind-DB/test-data`); run `git submodule update --init` after cloning (or clone
  with `--recursive`), else those tests `t.Skip`. The test IP `81.2.69.142` resolves to
  London, GB. MaxMind-DB carries its own `go.mod`, so it stays out of this module's `./...`.
- **Test loggers** use `zaptest.NewLogger(t)` (from `go.uber.org/zap/zaptest`), not
  `zap.NewNop()` — log output is routed to the test's `t.Log` and shown only on failure (or
  with `-v`), so a failing test surfaces the component's own logs instead of swallowing them.
  Pass the `*testing.T` in scope; for parallel subtests the parent `t` stays valid until all
  children finish.
- **Test requests** are built with `httptest.NewRequestWithContext(t.Context(), …)`, not the
  context-less `httptest.NewRequest`, so each request is tied to the test's lifecycle context
  (auto-cancelled when the test ends) instead of `context.Background()`. Helpers that build
  requests take the `*testing.T` to reach `t.Context()`.
- **`e2e/` module** is a separate local module (own `go.mod` with replaces) that keeps the
  heavy Caddy-server / `caddytest` dep tree out of the plugin's `go.mod` (library consumers
  don't inherit it). It both builds a runnable Caddy (`cd e2e && go build .`, then
  `./caddy run --config Caddyfile`) and hosts the `caddytest` end-to-end tests
  (`cd e2e && go test ./...`). Gotchas:
  - Fixtures in `e2e/testdata/`; config sets `db_path testdata` (cwd is the package dir).
  - Pin `admin localhost:2999` in the test Caddyfile — the port `caddytest` polls; omitting
    it makes Caddy move admin to `:2019` on load and the harness fails with "POSTed
    configuration isn't active".
  - `caddytest` ships its TLS certs inside the caddy module, so they're found automatically;
    it binds real ports (`:9080`, admin `:2999`), so these tests don't run in parallel.

## Conventions

- Lint/format with `golangci-lint` (config in `.golangci.yml`, `default: all`) — clean
  before committing. `//nolint` directives must name the linter and carry a reason
  (`nolintlint` + gocritic `whyNoLint`); keep lines within `golines`' 120-col limit.
- Logging: `zap` via `ctx.Logger()`; no `fmt.Print`. See **Logging** below for the principles.
- Errors: wrap with `fmt.Errorf("...: %w", err)`; sentinel errors for control flow
  (e.g. `ErrUnknownDatabase`, `errDBIPNotPublished`).
- Assert implemented Caddy interfaces with `var _ Iface = (*T)(nil)` guards.
- Lifecycle order: `New` → `UnmarshalCaddyfile` → `Provision` → `Validate` → use →
  `Cleanup` (and `Start`/`Stop` for the app). Loading databases from disk in `Provision`
  is intended (so handler/matcher have data immediately); keep **goroutines and network**
  out of `Provision` — start the watcher/updater in the app's `Start`, tear down in `Stop`.
  `Cleanup` is the only release hook when `Provision` succeeds but `Validate` (or a sibling
  module) fails, so it must release what `Provision` allocated.
- Reload safety: Caddy provisions the new module **before** stopping the old one, so both
  briefly coexist. Keep db state in the app (survives reloads); no package-level globals.
- Privacy/PII: client IPs and geolocation are personal data. Never log an IP as free-text —
  use `logfields.IP` so the `ip` key stays filterable/redactable; don't persist or transmit
  per-request lookups; remember `{geo.*}` placeholders carry PII wherever they're routed. See
  README **Privacy / personal data**.

### Logging

`zap` via `ctx.Logger()` (stored in `Provision`); never `fmt.Print`. The existing log
statements follow these principles — preserve them when adding code:

- **No blind spots.** Control flow that silently does nothing must leave a debug trail. Any
  early `return`/`continue` that skips work an operator might expect — no client IP, a matcher
  condition not met, a database skipped (still fresh, or no MaxMind credentials), an
  unrecognised file ignored — gets a `Debug` line saying *why*. Errors are never swallowed
  without a log.
- **Correct level.** `Error` = an operation failed with no recovery / the operator must act.
  `Warn` = degraded but still serving, or auto-retried — a failed background update or
  hot-reload keeps the previous database live, so it is `Warn`, not `Error`. `Info` = normal
  state changes (database loaded/updated/removed, updater started). `Debug` = per-request and
  per-decision detail. Expected lifecycle events (graceful watcher shutdown) are `Debug`, not
  `Warn` — gate on intent (see `dirmonitor.logWatcherClosed`, which checks `quit`).
- **Standard keys.** All structured fields go through `internal/logfields` constructors
  (`logfields.Database`, `.File`, `.IP`, ...), which fix both the key and the zap field type
  so a datum is always logged identically — giving operators a stable, documented key set to
  filter on. Don't inline `zap.String("database", ...)`; add or extend a constructor instead.
  (`zap.Error` is the one exception — it is zap's built-in `error` key.)
- **Hot paths stay near-zero-cost.** On the per-request path (handler/matcher), guard
  field-carrying debug logs with `if ce := logger.Check(zap.DebugLevel, msg); ce != nil {
  ce.Write(fields...) }` so the fields aren't constructed when debug is off. Field-less debug
  calls and cold paths (per-cycle, per-file-change, startup) do **not** need the guard — don't
  add it there. `Error`/`Warn` are rare, so they stay unguarded.

## Reference code

Dependencies are pinned to published versions; read APIs authoritatively with `go doc`
(or browse `$GOMODCACHE`) at the version in `go.mod`. The upstream repositories of the
imported dependencies this code integrates with — confirm against the pinned version when
it matters, since `main` may be ahead:

- [caddyserver/caddy](https://github.com/caddyserver/caddy) (`github.com/caddyserver/caddy/v2`)
  — Caddy module/app/provisioner interfaces, `caddy.Replacer`, `ctx.App`,
  `caddyhttp.ClientIPVarKey`, `caddytest`.
- [oschwald/maxminddb-golang](https://github.com/oschwald/maxminddb-golang)
  (`github.com/oschwald/maxminddb-golang/v2`) — the mmdb reader (`OpenBytes`, `Lookup`, `Decode`).
- [maxmind/geoipupdate](https://github.com/maxmind/geoipupdate) (`github.com/maxmind/geoipupdate/v7`)
  — the MaxMind update client (`geoipupdate/v7/client`).
- [fsnotify/fsnotify](https://github.com/fsnotify/fsnotify) (`github.com/fsnotify/fsnotify`)
  — the filesystem watcher behind `internal/dirmonitor`.

## Non-goals

- No IP allow/deny policy engine — geo data is exposed; access decisions are left to the
  matcher plus standard Caddy directives.
- No GUI / admin dashboard.
- No bundled database files — operators supply them (manual copy or auto-update).
- No lookup cache — mmdb reads are in-memory and fast; a cache adds memory, eviction, and a
  cross-reload staleness window for little gain. Revisit only if profiling shows a hotspot.
```
