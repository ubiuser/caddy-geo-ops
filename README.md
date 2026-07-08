# caddy-geo-ops

[![CI](https://github.com/ubiuser/caddy-geo-ops/actions/workflows/ci.yml/badge.svg)](https://github.com/ubiuser/caddy-geo-ops/actions/workflows/ci.yml)
[![Security](https://github.com/ubiuser/caddy-geo-ops/actions/workflows/security.yml/badge.svg)](https://github.com/ubiuser/caddy-geo-ops/actions/workflows/security.yml)
[![CodeQL](https://github.com/ubiuser/caddy-geo-ops/actions/workflows/codeql.yml/badge.svg)](https://github.com/ubiuser/caddy-geo-ops/actions/workflows/codeql.yml)
[![codecov](https://codecov.io/gh/ubiuser/caddy-geo-ops/graph/badge.svg)](https://codecov.io/gh/ubiuser/caddy-geo-ops)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/ubiuser/caddy-geo-ops/badge)](https://scorecard.dev/viewer/?uri=github.com/ubiuser/caddy-geo-ops)
[![Go Reference](https://pkg.go.dev/badge/github.com/ubiuser/caddy-geo-ops.svg)](https://pkg.go.dev/github.com/ubiuser/caddy-geo-ops)
[![golangci-lint](https://img.shields.io/badge/lint-golangci--lint-4b9fd5)](.golangci.yml)
[![Latest release](https://img.shields.io/github/v/release/ubiuser/caddy-geo-ops?sort=semver)](https://github.com/ubiuser/caddy-geo-ops/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A [Caddy](https://caddyserver.com) plugin for IP geolocation. It loads MaxMind **GeoIP2 /
GeoLite2** and **DB-IP** `.mmdb` databases and gives you:

- an **HTTP handler** that exposes every field of every loaded database as a request
  placeholder (`{geo.<db>.<field>}`),
- an **HTTP matcher** for routing/allow/deny by geo data,
- automatic, scheduled **database updates** (MaxMind and DB-IP) plus **hot-reload** when a
  database file changes on disk.

All three share one set of in-memory databases, owned by a Caddy **app**.

> Contributing? See [CONTRIBUTING.md](CONTRIBUTING.md) for the workflow, and
> [CLAUDE.md](CLAUDE.md) for architecture, design decisions, conventions, and the test layout.

---

## Contents

- [Install](#install)
- [Quick start](#quick-start)
- [Getting databases](#getting-databases)
  - [MaxMind (GeoIP2 / GeoLite2)](#maxmind-geoip2--geolite2)
  - [DB-IP](#db-ip)
  - [Supported editions & required filenames](#supported-editions--required-filenames)
- [Configuration](#configuration)
  - [The `geo_ops` app (global options)](#the-geo_ops-app-global-options)
  - [Automatic updates](#automatic-updates)
  - [Client IP behind a proxy](#client-ip-behind-a-proxy)
- [Placeholders](#placeholders)
  - [Field reference by edition](#field-reference-by-edition)
- [The handler](#the-handler)
- [The matcher](#the-matcher)
- [Examples](#examples)
- [CEL expression examples](#cel-expression-examples)
- [A complete Caddyfile](#a-complete-caddyfile)
- [JSON configuration](#json-configuration)
- [Logging](#logging)
- [Privacy / personal data](#privacy--personal-data)
- [Troubleshooting](#troubleshooting)
- [License](#license)

---

## Install

Caddy plugins are compiled into the Caddy binary with
[`xcaddy`](https://github.com/caddyserver/xcaddy):

```sh
# Latest tagged release:
xcaddy build --with github.com/ubiuser/caddy-geo-ops

# Or pin a specific version (recommended for reproducible builds):
xcaddy build --with github.com/ubiuser/caddy-geo-ops@v1.0.0
```

Versions are the project's git tags (e.g. `v1.0.0`), served by the Go module proxy — pin one
to keep builds reproducible and to control upgrades. `xcaddy` composes this plugin with any
others you need in the same `build` command (`--with ...@version` per plugin).

This produces a `caddy` binary that includes the `geo_ops` app, handler, and matcher.
Verify they're present:

```sh
./caddy list-modules | grep geo_ops
# geo_ops
# http.handlers.geo_ops
# http.matchers.geo_ops
```

---

## Quick start

1. Put a database in a directory, e.g. `/var/lib/geoip/geoip2-city.mmdb`
   (see [Getting databases](#getting-databases)).
2. Write a `Caddyfile`:

```caddyfile
{
	order geo_ops first
	geo_ops {
		db_path /var/lib/geoip
	}
}

:8080 {
	geo_ops
	respond "You appear to be in {geo.geoip2-city.country.iso_code} ({geo.geoip2-city.city.names.en})"
}
```

3. Run it and test:

```sh
./caddy run --config Caddyfile

curl -H "X-Forwarded-For: 89.105.110.225" localhost:8080
# You appear to be in GB (Tower Hamlets)
```

> **`order geo_ops first` is required.** The handler must run before any directive or
> matcher that reads its placeholders. See [The handler](#the-handler).

---

## Getting databases

The plugin never invents databases — it loads the `.mmdb` files you place in `db_path`, and
(optionally) keeps the ones already there up to date. So the first step is always to obtain
the editions you want and drop them in the folder under the [expected
filename](#supported-editions--required-filenames).

### MaxMind (GeoIP2 / GeoLite2)

GeoLite2 is free; GeoIP2 is the commercial, higher-accuracy version. Both use identical
record formats and the same plugin filenames.

1. Create a free account at <https://www.maxmind.com/en/geolite2/signup> (or a paid GeoIP2
   subscription).
2. In your account portal, **generate a license key** (Account → Manage License Keys) and
   note your **Account ID**.
3. Get the database files one of two ways:

   **a. Download manually** (Account → Download Databases). Download the **GeoIP2/GeoLite2
   binary (.mmdb)** archive, extract it, and copy the `.mmdb` into `db_path`. MaxMind's
   filenames (e.g. `GeoLite2-City.mmdb`) already match the plugin's taxonomy
   case-insensitively, so **no rename is needed** — just drop the file in.

   **b. Let the plugin auto-update** them — see [Automatic updates](#automatic-updates).
   Note that auto-update only *refreshes files that already exist*, so you still seed the
   folder once with an initial copy (option a), then the plugin keeps it fresh.

### DB-IP

DB-IP "Lite" databases are free and need no account.

1. Download the **MMDB** format of the editions you want from
   <https://db-ip.com/db/lite.php> (IP to City Lite, IP to Country Lite, IP to ASN Lite).
2. The download is gzipped and **dated**, e.g. `dbip-city-lite-2024-06.mmdb.gz`.
   Decompress it and **rename it to the un-dated filename** the plugin expects:

   ```sh
   gunzip dbip-city-lite-2024-06.mmdb.gz
   mv dbip-city-lite-2024-06.mmdb /var/lib/geoip/dbip-city-lite.mmdb
   ```

   The rename matters: one fixed name per edition means a new copy *overwrites* the old one
   instead of leaving stale dated files piling up. (Auto-update writes this canonical name
   for you; DB-IP needs **no credentials**.)

### Supported editions & required filenames

Place files under exactly these names (matching is case-insensitive). Anything else is
ignored.

| Edition | Filename | Source | Auto-update |
|---|---|---|---|
| GeoIP2 City | `geoip2-city.mmdb` | MaxMind (paid) | needs credentials |
| GeoIP2 Country | `geoip2-country.mmdb` | MaxMind (paid) | needs credentials |
| GeoIP2 ISP | `geoip2-isp.mmdb` | MaxMind (paid) | needs credentials |
| GeoIP2 Domain | `geoip2-domain.mmdb` | MaxMind (paid) | needs credentials |
| GeoIP2 Connection Type | `geoip2-connection-type.mmdb` | MaxMind (paid) | needs credentials |
| GeoIP2 Anonymous IP | `geoip2-anonymous-ip.mmdb` | MaxMind (paid) | needs credentials |
| GeoIP2 Enterprise | `geoip2-enterprise.mmdb` | MaxMind (paid) | needs credentials |
| GeoLite2 City | `geolite2-city.mmdb` | MaxMind (free) | needs credentials |
| GeoLite2 Country | `geolite2-country.mmdb` | MaxMind (free) | needs credentials |
| GeoLite2 ASN | `geolite2-asn.mmdb` | MaxMind (free) | needs credentials |
| DB-IP City Lite | `dbip-city-lite.mmdb` | DB-IP (free) | no credentials |
| DB-IP Country Lite | `dbip-country-lite.mmdb` | DB-IP (free) | no credentials |
| DB-IP ASN Lite | `dbip-asn-lite.mmdb` | DB-IP (free) | no credentials |

You can load several at once (e.g. a Country db plus an ASN db plus an Anonymous-IP db);
each contributes its own placeholders.

---

## Configuration

### The `geo_ops` app (global options)

Configure the shared app in the Caddyfile **global options** block:

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

| Option | Meaning | Default |
|---|---|---|
| `db_path` | Directory containing the `.mmdb` files. **Required.** | — |
| `auto_update` | Enable periodic remote updates of databases already present. | off |
| `account_id` | MaxMind Account ID (integer). Required to update MaxMind editions. | — |
| `license_key` | MaxMind license key. Required to update MaxMind editions. | — |
| `update_frequency` | Interval between update checks. | `24h` |
| `update_timeout` | Timeout for a single download. | `30s` |

> Use `{env.VAR}` to keep the license key out of the Caddyfile.

### Automatic updates

When `auto_update` is set, the plugin keeps the databases **already present** in `db_path`
fresh — it never downloads editions you haven't seeded.

- **MaxMind** editions are refreshed via MaxMind's update protocol and require
  `account_id` + `license_key`. Without credentials, MaxMind files are left untouched.
- **DB-IP** editions are refreshed from DB-IP's public monthly URLs and need **no**
  credentials.
- On startup, a database whose file is already older than `update_frequency` is refreshed
  immediately; fresh files are left alone (so reloading Caddy doesn't re-hit the vendors).
- Updates are written atomically and **hot-reloaded** with no downtime. Manually replacing
  a file in `db_path` triggers the same hot-reload.

If you don't set `auto_update`, you can instead manage files yourself (cron + MaxMind's
`geoipupdate`, a scripted DB-IP download, etc.) — the plugin hot-reloads on any change.

### Client IP behind a proxy

The plugin uses the client IP that **Caddy** resolves, which honors Caddy's standard
trusted-proxy configuration. If Caddy sits behind a load balancer or CDN, tell it which
upstreams to trust and which header carries the real client IP:

```caddyfile
{
	servers {
		trusted_proxies static private_ranges   # or specific CIDRs
		client_ip_headers X-Forwarded-For X-Real-IP
	}
}
```

Without this, lookups use the direct connection's IP. **Never** trust forwarding headers
without configuring `trusted_proxies` — they're spoofable.

---

## Placeholders

The handler exposes every field of every loaded database as:

```
{geo.<db>.<dotted field path>}
```

- `<db>` is the database filename without `.mmdb` — e.g. `geoip2-city`, `geolite2-country`,
  `dbip-asn-lite`. This keeps multiple databases from colliding.
- The field path mirrors the database record structure; nested objects use dots, and lists
  (like `subdivisions`) are indexed numerically (`.0`, `.1`).
- **A missing field resolves to an empty string**, never an error — so referencing a field
  that a given database doesn't have is safe.

Examples (against `geoip2-city.mmdb`):

| Placeholder | Example value |
|---|---|
| `{geo.geoip2-city.country.iso_code}` | `GB` |
| `{geo.geoip2-city.country.names.en}` | `United Kingdom` |
| `{geo.geoip2-city.city.names.en}` | `London` |
| `{geo.geoip2-city.location.latitude}` | `51.5142` |
| `{geo.geoip2-city.location.longitude}` | `-0.0931` |
| `{geo.geoip2-city.location.time_zone}` | `Europe/London` |
| `{geo.geoip2-city.postal.code}` | `OX1` |
| `{geo.geoip2-city.subdivisions.0.iso_code}` | `ENG` |
| `{geo.geoip2-city.continent.code}` | `EU` |

### Field reference by edition

These are the common fields per edition. Prefix each with `geo.<db>.` (and `names.<lang>`
supports `en`, `de`, `es`, `fr`, `ja`, `pt-BR`, `ru`, `zh-CN`). Booleans render as the
strings `"true"` / `"false"`.

**City** (`geoip2-city`, `geolite2-city`, `dbip-city-lite`)

```
continent.code                continent.names.en
country.iso_code              country.names.en        country.is_in_european_union
registered_country.iso_code
city.names.en
subdivisions.0.iso_code       subdivisions.0.names.en
location.latitude             location.longitude      location.time_zone
location.accuracy_radius      location.metro_code
postal.code
```

**Country** (`geoip2-country`, `geolite2-country`, `dbip-country-lite`)

```
continent.code                continent.names.en
country.iso_code              country.names.en        country.is_in_european_union
registered_country.iso_code   represented_country.iso_code
```

**ASN** (`geolite2-asn`, `dbip-asn-lite`)

```
autonomous_system_number              autonomous_system_organization
```

**ISP** (`geoip2-isp`)

```
isp        organization        autonomous_system_number        autonomous_system_organization
mobile_country_code            mobile_network_code
```

**Anonymous IP** (`geoip2-anonymous-ip`) — all booleans

```
is_anonymous          is_anonymous_vpn       is_hosting_provider
is_public_proxy       is_residential_proxy   is_tor_exit_node
```

**Connection Type** (`geoip2-connection-type`)

```
connection_type      # e.g. "Cable/DSL", "Cellular", "Corporate"
```

**Domain** (`geoip2-domain`)

```
domain               # second-level domain, e.g. "comcast.net"
```

**Enterprise** (`geoip2-enterprise`) — City fields above, plus confidence scores and:

```
traits.user_type                 traits.connection_type     traits.isp
traits.organization              traits.is_anonymous_proxy  traits.is_satellite_provider
country.confidence               city.confidence            location.accuracy_radius
```

> Not sure what a database exposes? Point a route at
> `respond "{geo.geoip2-city.country.iso_code} / {geo.geoip2-city.city.names.en}"` and
> experiment, or check the [MaxMind](https://dev.maxmind.com/geoip/docs/databases) /
> [DB-IP](https://db-ip.com/db/) schema docs.

---

## The handler

The `geo_ops` directive is the handler. It takes no arguments; it looks up the client IP
and registers the `{geo.*}` placeholders for the rest of the request.

```caddyfile
example.com {
	geo_ops
	# ... everything after here can use {geo.*} placeholders
}
```

It must run **before** anything that reads its placeholders — so always set
`order geo_ops first` in global options (or `route` it explicitly first). You only need the
handler if you want **placeholders**; the [matcher](#the-matcher) looks up geo data on its
own and works without it.

---

## The matcher

The `geo_ops` matcher matches a request against one or more **field = allowed-values**
conditions. The field is a placeholder key **without** the `geo.` prefix.

Semantics: **AND across fields, OR within a field's values** — every listed field must
equal one of its listed values. A field with no data for the client IP doesn't match.

Block form (multiple conditions):

```caddyfile
@northAmerica geo_ops {
	geoip2-country.country.iso_code US CA MX
}

@londonEnglish geo_ops {
	geoip2-city.city.names.en  London
	geoip2-city.country.iso_code GB
}
```

Inline form (single condition):

```caddyfile
@us geo_ops geoip2-country.country.iso_code US
```

At least one condition is required.

---

## Examples

All examples assume `order geo_ops first` and a `geo_ops { db_path ... }` global block, with
the relevant database present. Add the `geo_ops` handler in any site that uses `{geo.*}`
placeholders.

**Add a country header to every response**

```caddyfile
example.com {
	geo_ops
	header +X-Country "{geo.geoip2-country.country.iso_code}"
	reverse_proxy localhost:9000
}
```

**Pass geo data to a backend**

```caddyfile
example.com {
	geo_ops
	reverse_proxy localhost:9000 {
		header_up X-Geo-Country  "{geo.geoip2-city.country.iso_code}"
		header_up X-Geo-City     "{geo.geoip2-city.city.names.en}"
		header_up X-Geo-ASN      "{geo.geolite2-asn.autonomous_system_number}"
	}
}
```

**Block a set of countries (return 403)**

```caddyfile
example.com {
	@blocked geo_ops geoip2-country.country.iso_code RU KP IR
	respond @blocked "Not available in your region" 403

	reverse_proxy localhost:9000
}
```

**Allow-list: only serve specific countries, deny the rest**

```caddyfile
example.com {
	@allowed geo_ops geoip2-country.country.iso_code US CA GB
	handle @allowed {
		reverse_proxy localhost:9000
	}
	handle {
		respond "Access restricted" 403
	}
}
```

**Route different countries to different backends**

```caddyfile
example.com {
	@eu geo_ops geoip2-country.country.iso_code DE FR NL IE
	@us geo_ops geoip2-country.country.iso_code US

	handle @eu { reverse_proxy eu-backend.internal:9000 }
	handle @us { reverse_proxy us-backend.internal:9000 }
	handle    { reverse_proxy global-backend.internal:9000 }
}
```

**Redirect by country (e.g. localized site)**

```caddyfile
example.com {
	@de geo_ops geoip2-country.country.iso_code DE AT CH
	redir @de https://de.example.com{uri}
}
```

**Block anonymizers (VPN / proxy / Tor)** — needs `geoip2-anonymous-ip.mmdb`

```caddyfile
example.com {
	@anon geo_ops {
		geoip2-anonymous-ip.is_anonymous true
	}
	respond @anon "Anonymous networks are not allowed" 403

	reverse_proxy localhost:9000
}
```

**Block only Tor exit nodes**

```caddyfile
example.com {
	@tor geo_ops geoip2-anonymous-ip.is_tor_exit_node true
	respond @tor "Tor is blocked" 403
	reverse_proxy localhost:9000
}
```

**Match a specific ASN (network operator)** — needs an ASN database

```caddyfile
example.com {
	# Block a hosting/cloud ASN (example: AS14618 Amazon)
	@aws geo_ops geolite2-asn.autonomous_system_number 14618
	respond @aws "Datacenter traffic blocked" 403

	reverse_proxy localhost:9000
}
```

**Match a connection type** — needs `geoip2-connection-type.mmdb`

```caddyfile
example.com {
	@cellular geo_ops geoip2-connection-type.connection_type Cellular
	header @cellular +X-Lite-Mode "1"   # e.g. serve a lighter page to mobile networks
	reverse_proxy localhost:9000
}
```

**A geo debug endpoint**

```caddyfile
example.com {
	geo_ops
	handle /whereami {
		respond `country={geo.geoip2-city.country.iso_code}
city={geo.geoip2-city.city.names.en}
coords={geo.geoip2-city.location.latitude},{geo.geoip2-city.location.longitude}
asn={geo.geolite2-asn.autonomous_system_number} ({geo.geolite2-asn.autonomous_system_organization})`
	}
	reverse_proxy localhost:9000
}
```

**Log geo data with each request** — place geo fields into the access log via headers, or
use a structured field through a header the logger captures:

```caddyfile
example.com {
	geo_ops
	header +X-Country "{geo.geoip2-country.country.iso_code}"
	log {
		output file /var/log/caddy/access.log
	}
	reverse_proxy localhost:9000
}
```

---

## CEL expression examples

Caddy's built-in [`expression`](https://caddyserver.com/docs/caddyfile/matchers#expression)
matcher evaluates a [CEL](https://github.com/google/cel-spec) expression and can read the
`{geo.*}` placeholders. Two things to remember:

1. The `geo_ops` **handler must run first** (it's what sets the placeholders), so keep
   `order geo_ops first` **and** include `geo_ops` in the site.
2. Placeholders resolve to **strings** (booleans are `"true"`/`"false"`, numbers are their
   text form). Compare as strings, or convert with CEL's `double()` / `int()`.

**Country in a set**

```caddyfile
example.com {
	geo_ops
	@us_or_ca expression `{geo.geoip2-country.country.iso_code} in ["US", "CA"]`
	respond @us_or_ca "Hello, North America"
	reverse_proxy localhost:9000
}
```

**Block an entire continent**

```caddyfile
example.com {
	geo_ops
	@blockAsia expression `{geo.geoip2-country.continent.code} == "AS"`
	respond @blockAsia "Unavailable" 403
	reverse_proxy localhost:9000
}
```

**EU vs. non-EU (GDPR banner, etc.)**

```caddyfile
example.com {
	geo_ops
	@eu expression `{geo.geoip2-country.country.is_in_european_union} == "true"`
	header @eu +X-Show-Cookie-Banner "1"
	reverse_proxy localhost:9000
}
```

**Combine geo with the request path** — protect `/admin` to one country

```caddyfile
example.com {
	geo_ops
	@foreignAdmin expression `path('/admin/*') && {geo.geoip2-country.country.iso_code} != "CH"`
	respond @foreignAdmin "Admin is region-locked" 403
	reverse_proxy localhost:9000
}
```

**Anonymous OR public proxy** (boolean fields as strings)

```caddyfile
example.com {
	geo_ops
	@suspicious expression <<CEL
		{geo.geoip2-anonymous-ip.is_anonymous} == "true" ||
		{geo.geoip2-anonymous-ip.is_public_proxy} == "true"
	CEL
	respond @suspicious "Blocked" 403
	reverse_proxy localhost:9000
}
```

**Block a list of ASNs**

```caddyfile
example.com {
	geo_ops
	@badAsn expression `{geo.geolite2-asn.autonomous_system_number} in ["14618", "16509", "15169"]`
	respond @badAsn "Datacenter traffic blocked" 403
	reverse_proxy localhost:9000
}
```

**Numeric comparison (latitude)** — convert with `double()`; guard against an empty value

```caddyfile
example.com {
	geo_ops
	@northern expression `{geo.geoip2-city.location.latitude} != "" && double({geo.geoip2-city.location.latitude}) > 60.0`
	header @northern +X-Region "nordic"
	reverse_proxy localhost:9000
}
```

> When a country/city placeholder may be empty (IP not in the database), the equality and
> `in` forms simply don't match — which is usually the safe default for allow/deny rules.

---

## A complete Caddyfile

```caddyfile
{
	order geo_ops first

	servers {
		trusted_proxies static private_ranges
		client_ip_headers X-Forwarded-For
	}

	geo_ops {
		db_path          /var/lib/geoip
		auto_update
		account_id       123456
		license_key      {env.MAXMIND_LICENSE_KEY}
		update_frequency 24h
	}
}

example.com {
	geo_ops

	# Block anonymizers outright.
	@anon geo_ops geoip2-anonymous-ip.is_anonymous true
	respond @anon "Anonymous networks are not allowed" 403

	# Region-lock the admin area.
	@foreignAdmin expression `path('/admin/*') && {geo.geoip2-country.country.iso_code} != "US"`
	respond @foreignAdmin "Region-locked" 403

	# Tell the backend where the visitor is.
	reverse_proxy localhost:9000 {
		header_up X-Geo-Country "{geo.geoip2-city.country.iso_code}"
		header_up X-Geo-City    "{geo.geoip2-city.city.names.en}"
	}
}
```

---

## JSON configuration

The Caddyfile adapts to Caddy's native JSON. The app is configured under
`apps.geo_ops`; the handler and matcher use their module IDs
(`http.handlers.geo_ops`, `http.matchers.geo_ops`). To see the JSON for any Caddyfile:

```sh
./caddy adapt --config Caddyfile --pretty
```

Sketch of the app block:

```json
{
  "apps": {
    "geo_ops": {
      "db_path": "/var/lib/geoip",
      "auto_update": true,
      "account_id": 123456,
      "license_key": "…",
      "update_frequency": "24h"
    }
  }
}
```

---

## Logging

`geo_ops` logs through Caddy's structured (`zap`) logger, so its entries appear in Caddy's
normal log output and honour your `log` directive's level and format.

**Levels** — what to expect, and what's worth alerting on:

- **error** — an operation failed with no automatic recovery; likely needs your attention.
- **warn** — degraded but still serving. A background database update or hot-reload failed
  (the previously loaded database keeps serving and the next cycle retries); `auto_update` is
  enabled with MaxMind databases present but no credentials configured (so they will never
  update); or a per-request lookup against one database errored (the request still succeeds
  using the others). These are the events worth surfacing without paging.
- **info** — normal state changes: a database was loaded, updated, or removed; the periodic
  updater started.
- **debug** — per-request and per-decision detail: no client IP resolved, a matcher condition
  not satisfied (with the field, its allowed values, and the looked-up value), a database
  skipped because it is still fresh, a file change detected and routed. Turn this on to answer
  "why didn't this match / update / reload?".

**Standard fields** — entries carry a small, stable set of structured keys you can filter and
alert on:

| Key | Meaning |
|-----|---------|
| `database` | database edition / filename |
| `file` | a filesystem path (database or temp file) |
| `md5` | the loaded database's MD5 sum |
| `ip` | the client IP being looked up |
| `field`, `allowed`, `got`, `found` | matcher condition detail: which field, its allowed values, and the value found |
| `frequency` | the updater's refresh interval |
| `maxmind_enabled` | whether MaxMind credentials are configured |
| `action` | resolved file-change action (`update` or `delete`) |

To see debug detail, raise the log level (globally or per-logger) in your Caddyfile:

```caddyfile
{
	log {
		level DEBUG
	}
}
```

---

## Privacy / personal data

`geo_ops` processes **client IP addresses** and the **geolocation derived from them** — both
are personal data under regulations such as the GDPR and CCPA. What the module does with it:

- **It does not persist or cache it.** No per-request IP or lookup is written to disk or held
  in memory beyond the request; the only files written are the geo databases themselves.
- **It does not transmit it.** The auto-updater only downloads databases from MaxMind / DB-IP;
  client IPs and lookups never leave the process.
- **Logs.** The client `ip` can appear in `warn`-level logs on a lookup error, and a geo value
  (`got`) in `debug` logs on a matcher non-match. Both are emitted as structured fields, so you
  can drop or redact the `ip` (and geo) keys in your log pipeline if required — see
  [Logging](#logging).
- **Placeholders.** `{geo.*}` placeholders carry personal data wherever you route them —
  response headers, upstream headers, and especially **access logs**. Treat any sink you send
  them to as holding personal data.

You remain the data controller for how IPs and geolocation are used, logged, and retained in
your deployment.

---

## Troubleshooting

**Placeholders come out empty.**
- Make sure `order geo_ops first` is set and the `geo_ops` handler is in the site —
  placeholders only exist after the handler runs.
- Check the field path and `<db>` segment match a *loaded* database (see the
  [field reference](#field-reference-by-edition)). A wrong/absent field is empty by design.
- Confirm the client IP is actually in the database (private/localhost IPs usually aren't —
  test with a public IP via `X-Forwarded-For` and `trusted_proxies` configured).

**A database isn't being loaded.**
- The filename must match the [taxonomy](#supported-editions--required-filenames) exactly
  (case-insensitive). DB-IP files in particular must be renamed to drop the date suffix.
- Check Caddy's logs at startup for `database loaded` entries.

**Wrong IP is being geolocated (always the proxy's IP).**
- Configure `servers { trusted_proxies … ; client_ip_headers … }` so Caddy resolves the
  real client IP from the forwarding header.

**Auto-update isn't fetching MaxMind.**
- MaxMind needs both `account_id` and `license_key`; with only one set, the configuration
  is rejected at startup. DB-IP needs neither.
- Auto-update only refreshes databases **already present** — seed the folder first.

**Matcher never matches.**
- The condition field is the placeholder key **without** `geo.` (e.g.
  `geoip2-country.country.iso_code`, not `geo.geoip2-country.country.iso_code`).
- Values are matched by exact string equality; check casing (ISO codes are uppercase).

---

## License

[MIT](LICENSE) © 2026 Gabor Szabad
