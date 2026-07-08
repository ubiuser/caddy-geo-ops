package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/ubiuser/caddy-geo-ops/app"
	"github.com/ubiuser/caddy-geo-ops/handler"
)

// fakeApp is a stand-in for *app.App that returns canned lookup data.
type fakeApp struct {
	data map[string]string
}

func (f fakeApp) LookupAll(netip.Addr) map[string]string { return f.data }

// newRequest builds a request carrying a fresh replacer and (optionally) a
// resolved client IP, mimicking what Caddy core sets up before the handler runs.
func newRequest(t *testing.T, clientIP string) (*http.Request, *caddy.Replacer) {
	t.Helper()

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	repl := caddy.NewReplacer()

	ctx := context.WithValue(r.Context(), caddy.ReplacerCtxKey, repl)

	vars := make(map[string]any)

	ctx = context.WithValue(ctx, caddyhttp.VarsCtxKey, vars)
	if clientIP != "" {
		caddyhttp.SetVar(ctx, caddyhttp.ClientIPVarKey, clientIP)
	}

	return r.WithContext(ctx), repl
}

func newNext(called *bool) caddyhttp.HandlerFunc {
	return func(http.ResponseWriter, *http.Request) error {
		*called = true
		return nil
	}
}

func TestServeHTTPSetsPlaceholders(t *testing.T) {
	t.Parallel()

	h := handler.NewTestHandler(zaptest.NewLogger(t), fakeApp{data: map[string]string{
		"geo.geoip2-city.country.iso_code": "GB",
		"geo.geoip2-city.city.names.en":    "London",
	}})

	req, repl := newRequest(t, "81.2.69.142")

	var called bool
	require.NoError(t, h.ServeHTTP(httptest.NewRecorder(), req, newNext(&called)))
	assert.Truef(t, called, "next handler was not called")

	// Known field resolves to its value.
	assert.Equal(t, "GB", repl.ReplaceKnown("{geo.geoip2-city.country.iso_code}", ""))
	// Unknown field under the geo. root resolves to empty (never a literal).
	assert.Emptyf(t, repl.ReplaceKnown("{geo.geoip2-city.does.not.exist}", ""),
		"unknown geo field should resolve to empty")
	// A non-geo unknown placeholder is left untouched (we don't claim it).
	assert.Equalf(t, "{custom.unknown.thing}", repl.ReplaceKnown("{custom.unknown.thing}", ""),
		"non-geo placeholder should be left verbatim")
}

func TestServeHTTPNoClientIP(t *testing.T) {
	t.Parallel()

	h := handler.NewTestHandler(
		zaptest.NewLogger(t),
		fakeApp{data: map[string]string{"geo.geoip2-city.country.iso_code": "GB"}},
	)

	req, repl := newRequest(t, "") // no client IP resolved

	var called bool
	require.NoError(t, h.ServeHTTP(httptest.NewRecorder(), req, newNext(&called)))
	assert.Truef(t, called, "next handler must still be called when no client IP is available")

	// With no lookup performed, the geo provider is not registered, so the
	// placeholder stays a literal.
	assert.Equalf(t, "{geo.geoip2-city.country.iso_code}",
		repl.ReplaceKnown("{geo.geoip2-city.country.iso_code}", ""),
		"placeholder should be left verbatim when no lookup happened")
}

func TestUnmarshalCaddyfile(t *testing.T) {
	t.Parallel()

	var h handler.Handler
	require.NoErrorf(t, h.UnmarshalCaddyfile(caddyfile.NewTestDispenser("geo_ops")),
		"bare directive should parse")

	var h2 handler.Handler
	require.Errorf(t, h2.UnmarshalCaddyfile(caddyfile.NewTestDispenser("geo_ops surprise")),
		"directive with an argument should error")
}

func TestCaddyModuleID(t *testing.T) {
	t.Parallel()

	// handler.go hard-codes this ID as a literal (moduledoc scanner requirement);
	// assert it still tracks app.AppID so a namespace rename can't silently drift.
	assert.Equal(t, "http.handlers."+app.AppID, string(new(handler.Handler).CaddyModule().ID))
}
