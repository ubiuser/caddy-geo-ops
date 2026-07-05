package main

import (
	"net/http"
	"testing"

	"github.com/caddyserver/caddy/v2/caddytest"
	"github.com/stretchr/testify/require"
)

// The geo_ops + standard modules are registered via main.go's blank imports
// (this test shares package main), and caddytest also pulls modules/standard.

// e2eConfig drives the full stack: the geo_ops app loads ./testdata, the handler
// exposes {geo.*} placeholders, and the matcher routes by country. Client IP is
// taken from X-Forwarded-For via trusted_proxies, so the tests choose which IP
// to look up. The admin endpoint is pinned to localhost:2999 — the port
// caddytest starts and polls; omitting it makes Caddy move admin to the default
// :2019 on load and the harness fails with "POSTed configuration isn't active".
const (
	e2eConfig = `
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
				db_path testdata
			}
		}

		:9080 {
			geo_ops

			@gb geo_ops {
				geoip2-city.country.iso_code GB
			}
			respond @gb "matched country={geo.geoip2-city.country.iso_code} city={geo.geoip2-city.city.names.en}"

			respond "nomatch country=[{geo.geoip2-city.country.iso_code}] missing=[{geo.geoip2-city.no.such.field}]"
		}
	`
)

func request(t *testing.T, forwardedFor string) *http.Request {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://localhost:9080/", http.NoBody)
	require.NoError(t, err)
	req.Header.Set("X-Forwarded-For", forwardedFor)

	return req
}

func TestEndToEnd(t *testing.T) {
	t.Parallel()

	tester := caddytest.NewTester(t)
	tester.InitServer(e2eConfig, "caddyfile")

	// The canonical MaxMind test IP resolves to London, GB: the matcher's @gb
	// route fires and the handler's placeholders resolve from the City db.
	//nolint:bodyclose // false alert, body is closed in tester.AssertResponse
	tester.AssertResponse(request(t, "81.2.69.142"), http.StatusOK,
		"matched country=GB city=London")

	// An IP with no record: the matcher does not match, and missing fields
	// resolve to empty (never a literal {geo...} placeholder).
	//nolint:bodyclose // false alert, body is closed in tester.AssertResponse
	tester.AssertResponse(request(t, "10.0.0.1"), http.StatusOK,
		"nomatch country=[] missing=[]")
}
