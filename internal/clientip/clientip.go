// Package clientip extracts the client IP from a request using Caddy's
// pre-computed value, which already honours the operator's global
// trusted_proxies and client_ip_headers configuration (X-Forwarded-For,
// X-Real-IP, etc.). See CLAUDE.md "Client IP / forwarding headers".
package clientip

import (
	"net/http"
	"net/netip"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// FromRequest returns the client IP Caddy resolved for the request. The second
// result is false if no valid client IP is available.
func FromRequest(r *http.Request) (netip.Addr, bool) {
	ipStr, ok := caddyhttp.GetVar(r.Context(), caddyhttp.ClientIPVarKey).(string)
	if !ok || ipStr == "" {
		return netip.Addr{}, false
	}

	addr, err := netip.ParseAddr(ipStr)
	if err != nil {
		return netip.Addr{}, false
	}

	return addr, true
}
