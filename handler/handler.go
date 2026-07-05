// Package handler implements the http.handlers.geo_ops middleware. It looks up
// the client IP against the shared geo_ops app and exposes every database field
// as a {geo.<db>.<field path>} placeholder on the request.
package handler

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"

	"github.com/ubiuser/caddy-geo-ops/app"
	"github.com/ubiuser/caddy-geo-ops/internal/clientip"
)

type (
	// geoLookuper is the slice of the geo_ops app the handler depends on. It is an
	// interface (satisfied by *app.App) so the handler can be unit-tested with a
	// fake lookup source.
	geoLookuper interface {
		LookupAll(addr netip.Addr) map[string]string
	}

	// Handler sets geo placeholders on the request.
	Handler struct {
		app    geoLookuper
		logger *zap.Logger
	}
)

// placeholderPrefix is the root of every key this handler serves. Any key under
// it that has no value resolves to "" (never an unknown-placeholder error).
const placeholderPrefix = "geo."

var (
	_ caddy.Module                = (*Handler)(nil)
	_ caddy.Provisioner           = (*Handler)(nil)
	_ caddyfile.Unmarshaler       = (*Handler)(nil)
	_ caddyhttp.MiddlewareHandler = (*Handler)(nil)

	errWrongAppType = errors.New("wrong app type")
)

func init() {
	caddy.RegisterModule(new(Handler{}))
	httpcaddyfile.RegisterHandlerDirective(app.AppID, parseCaddyfile)
}

// CaddyModule returns the Caddy module information.
func (*Handler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers." + app.AppID,
		New: func() caddy.Module { return new(Handler) },
	}
}

// Provision fetches the shared geo_ops app.
func (h *Handler) Provision(ctx caddy.Context) error {
	h.logger = ctx.Logger()

	a, err := ctx.App(app.AppID)
	if err != nil {
		return fmt.Errorf("load %s app: %w", app.AppID, err)
	}

	geoApp, ok := a.(*app.App)
	if !ok {
		return fmt.Errorf("%w for %s (expected: %T)", errWrongAppType, app.AppID, (*app.App)(nil))
	}

	h.app = geoApp

	return nil
}

// ServeHTTP looks up the client IP and registers the geo placeholders, then
// calls the next handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if addr, ok := clientip.FromRequest(r); ok {
		h.applyGeoPlaceholders(r, addr)
	} else {
		h.logger.Debug("no client IP available for geo lookup")
	}

	// Pass through the middleware chain unwrapped: Caddy's error handling
	// inspects the original error, so wrapping it here would break that.
	return next.ServeHTTP(w, r) //nolint:wrapcheck // middleware-chain passthrough
}

// UnmarshalCaddyfile parses the geo_ops handler directive, which takes no
// arguments:
//
//	route {
//	    geo_ops
//	    ...
//	}
func (*Handler) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		if d.NextArg() {
			return d.ArgErr()
		}
	}

	return nil
}

func parseCaddyfile(helper httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var handler Handler
	if err := handler.UnmarshalCaddyfile(helper.Dispenser); err != nil {
		return nil, fmt.Errorf("unmarshal caddyfile: %w", err)
	}

	return &handler, nil
}

// applyGeoPlaceholders looks up addr and registers the {geo.*} placeholders on
// the request's replacer. Any geo.* key without a value resolves to "".
func (h *Handler) applyGeoPlaceholders(r *http.Request, addr netip.Addr) {
	repl, ok := r.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer)
	if !ok {
		return
	}

	data := h.app.LookupAll(addr)

	repl.Map(func(key string) (any, bool) {
		if !strings.HasPrefix(key, placeholderPrefix) {
			return nil, false
		}

		if v, found := data[key]; found {
			return v, true
		}

		// Known prefix, unknown field: resolve to empty, never error.
		return "", true
	})
}
