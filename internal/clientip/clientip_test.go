package clientip_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/stretchr/testify/assert"

	"github.com/ubiuser/caddy-geo-ops/internal/clientip"
)

func TestFromRequest(t *testing.T) {
	t.Parallel()

	withVar := func(value any, set bool) (string, bool) {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)

		vars := make(map[string]any)

		ctx := context.WithValue(r.Context(), caddyhttp.VarsCtxKey, vars)
		if set {
			caddyhttp.SetVar(ctx, caddyhttp.ClientIPVarKey, value)
		}

		addr, ok := clientip.FromRequest(r.WithContext(ctx))
		if !ok {
			return "", false
		}

		return addr.String(), true
	}

	got, ok := withVar("81.2.69.142", true)
	assert.True(t, ok)
	assert.Equal(t, "81.2.69.142", got)

	_, ok = withVar(nil, false)
	assert.Falsef(t, ok, "missing client IP var should yield ok=false")

	_, ok = withVar("not-an-ip", true)
	assert.Falsef(t, ok, "invalid IP should yield ok=false")

	_, ok = withVar("", true)
	assert.Falsef(t, ok, "empty IP should yield ok=false")

	// Caddy stores "@" in ClientIPVarKey for a Unix-socket client that has no
	// forwarding header; it has no geolocatable IP, so we decline the lookup.
	_, ok = withVar("@", true)
	assert.Falsef(t, ok, "unix-socket sentinel (@) should yield ok=false")
}
