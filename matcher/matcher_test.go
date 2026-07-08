package matcher_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/ubiuser/caddy-geo-ops/app"
	"github.com/ubiuser/caddy-geo-ops/matcher"
)

// fakeApp is a stand-in for *app.App that returns canned lookup data.
type fakeApp struct {
	data map[string]string
}

func (f fakeApp) LookupAll(netip.Addr) map[string]string { return f.data }

// requestWithIP builds a request whose context carries the resolved client IP
// the way Caddy core sets it before matchers run (empty clientIP = none).
func requestWithIP(t *testing.T, clientIP string) *http.Request {
	t.Helper()

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	vars := make(map[string]any)
	ctx := context.WithValue(r.Context(), caddyhttp.VarsCtxKey, vars)

	if clientIP != "" {
		caddyhttp.SetVar(ctx, caddyhttp.ClientIPVarKey, clientIP)
	}

	return r.WithContext(ctx)
}

func TestUnmarshalCaddyfileBlock(t *testing.T) {
	t.Parallel()

	d := caddyfile.NewTestDispenser(`geo_ops {
		geoip2-country.country.iso_code US CA
		geoip2-city.city.names.en       London
	}`)

	var m matcher.Matcher
	require.NoError(t, m.UnmarshalCaddyfile(d))

	want := map[string][]string{
		"geoip2-country.country.iso_code": {"US", "CA"},
		"geoip2-city.city.names.en":       {"London"},
	}
	assert.Equal(t, want, m.Conditions)
}

func TestUnmarshalCaddyfileInline(t *testing.T) {
	t.Parallel()

	d := caddyfile.NewTestDispenser(`geo_ops geoip2-country.country.iso_code US`)

	var m matcher.Matcher
	require.NoError(t, m.UnmarshalCaddyfile(d))

	assert.Equal(t, []string{"US"}, m.Conditions["geoip2-country.country.iso_code"])
}

func TestUnmarshalCaddyfileErrors(t *testing.T) {
	t.Parallel()

	// A field with no values is an error.
	d := caddyfile.NewTestDispenser(`geo_ops {
		geoip2-country.country.iso_code
	}`)

	var m matcher.Matcher
	require.Error(t, m.UnmarshalCaddyfile(d))
}

func TestValidate(t *testing.T) {
	t.Parallel()

	var empty matcher.Matcher
	require.Error(t, empty.Validate())

	configured := matcher.Matcher{Conditions: map[string][]string{"x": {"y"}}}
	require.NoError(t, configured.Validate())
}

func TestMatchWithError(t *testing.T) {
	t.Parallel()

	const testIP = "81.2.69.142"

	lookup := fakeApp{data: map[string]string{
		"geo.geoip2-country.country.iso_code": "US",
		"geo.geoip2-city.city.names.en":       "London",
	}}

	cases := []struct {
		conditions map[string][]string
		name       string
		clientIP   string
		want       bool
	}{
		{
			name:       "all conditions satisfied (AND across fields)",
			clientIP:   testIP,
			conditions: map[string][]string{"geoip2-country.country.iso_code": {"US", "CA"}},
			want:       true,
		},
		{
			name:       "value not in allowed list",
			clientIP:   testIP,
			conditions: map[string][]string{"geoip2-country.country.iso_code": {"GB", "FR"}},
			want:       false,
		},
		{
			name:       "field absent from lookup data",
			clientIP:   testIP,
			conditions: map[string][]string{"geoip2-country.country.is_in_european_union": {"true"}},
			want:       false,
		},
		{
			name:     "one of several conditions fails",
			clientIP: testIP,
			conditions: map[string][]string{
				"geoip2-country.country.iso_code": {"US"},
				"geoip2-city.city.names.en":       {"Paris"},
			},
			want: false,
		},
		{
			name:       "no client IP resolved",
			clientIP:   "",
			conditions: map[string][]string{"geoip2-country.country.iso_code": {"US"}},
			want:       false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := matcher.NewTestMatcher(zaptest.NewLogger(t), tc.conditions, lookup)

			got, err := m.MatchWithError(requestWithIP(t, tc.clientIP))
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestCaddyModuleID(t *testing.T) {
	t.Parallel()

	// matcher.go hard-codes this ID as a literal (moduledoc scanner requirement);
	// assert it still tracks app.AppID so a namespace rename can't silently drift.
	assert.Equal(t, "http.matchers."+app.AppID, string(new(matcher.Matcher).CaddyModule().ID))
}
