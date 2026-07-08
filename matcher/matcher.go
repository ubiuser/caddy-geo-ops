// Package matcher implements the http.matchers.geo_ops request matcher. It
// matches when the client IP's looked-up geo fields satisfy a set of
// field/allowed-values conditions.
package matcher

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"slices"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"

	"github.com/ubiuser/caddy-geo-ops/app"
	"github.com/ubiuser/caddy-geo-ops/internal/clientip"
	"github.com/ubiuser/caddy-geo-ops/internal/logfields"
)

type (
	// geoLookuper is the slice of the geo_ops app the matcher depends on. It is
	// an interface (satisfied by *app.App) so MatchWithError can be unit-tested
	// with a fake lookup source.
	geoLookuper interface {
		LookupAll(addr netip.Addr) map[string]string
	}

	// Matcher matches requests by geo field values.
	Matcher struct {
		// Conditions maps a field path — the placeholder key without the leading
		// "geo." root, e.g. "geoip2-country.country.iso_code" — to the allowed
		// values. The request matches when, for every condition, the client's
		// looked-up value equals one of the listed values (AND across fields, OR
		// within each field's values).
		Conditions map[string][]string `json:"conditions,omitempty"`

		app    geoLookuper
		logger *zap.Logger
	}
)

const placeholderPrefix = "geo."

var (
	_ caddy.Module                      = (*Matcher)(nil)
	_ caddy.Provisioner                 = (*Matcher)(nil)
	_ caddy.Validator                   = (*Matcher)(nil)
	_ caddyfile.Unmarshaler             = (*Matcher)(nil)
	_ caddyhttp.RequestMatcherWithError = (*Matcher)(nil)

	errMatcherMinConditions = errors.New("geo_ops matcher requires at least one condition")
	errWrongAppType         = errors.New("wrong app type")
)

func init() {
	caddy.RegisterModule(new(Matcher))
}

// CaddyModule returns the Caddy module information.
func (*Matcher) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		// Must be a static string literal ("http.matchers." + app.AppID), not a
		// concatenation: caddyserver.com's moduledoc scanner reads this field via
		// AST and only accepts a literal, so a computed ID here makes the plugin
		// unregisterable on the download page.
		ID:  "http.matchers.geo_ops",
		New: func() caddy.Module { return new(Matcher) },
	}
}

// Provision fetches the shared geo_ops app.
func (m *Matcher) Provision(ctx caddy.Context) error {
	m.logger = ctx.Logger()

	a, err := ctx.App(app.AppID)
	if err != nil {
		return fmt.Errorf("load %s app: %w", app.AppID, err)
	}

	geoApp, ok := a.(*app.App)
	if !ok {
		return fmt.Errorf("%w for %s (expected: %T)", errWrongAppType, app.AppID, (*app.App)(nil))
	}

	m.app = geoApp

	return nil
}

// Validate ensures at least one condition is configured.
func (m *Matcher) Validate() error {
	if len(m.Conditions) == 0 {
		return errMatcherMinConditions
	}

	return nil
}

// MatchWithError reports whether the client IP's geo data satisfies every
// condition. A request with no resolvable client IP does not match.
func (m *Matcher) MatchWithError(r *http.Request) (bool, error) {
	addr, ok := clientip.FromRequest(r)
	if !ok {
		if ce := m.logger.Check(zap.DebugLevel, "no client IP available; not matching"); ce != nil {
			ce.Write()
		}

		return false, nil
	}

	data := m.app.LookupAll(addr)

	for field, allowed := range m.Conditions {
		got, found := data[placeholderPrefix+field]
		if !found || !slices.Contains(allowed, got) {
			// found=false means the field wasn't in the lookup data at all (typo'd
			// field, or that database edition isn't loaded), as opposed to a value
			// that simply didn't match — distinct debugging stories.
			if ce := m.logger.Check(zap.DebugLevel, "geo condition not satisfied"); ce != nil {
				ce.Write(
					logfields.GeoField(field),
					logfields.Allowed(allowed),
					logfields.Got(got),
					logfields.Found(found),
				)
			}

			return false, nil
		}
	}

	return true, nil
}

// UnmarshalCaddyfile parses the matcher block:
//
//	@geo	geo_ops {
//	    geoip2-country.country.iso_code US CA
//	    geoip2-city.city.names.en       London
//	}
func (m *Matcher) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	if m.Conditions == nil {
		m.Conditions = make(map[string][]string)
	}

	for d.Next() {
		// Inline form: geo_ops <field> <value...>
		if args := d.RemainingArgs(); len(args) > 0 {
			if len(args) < 2 {
				return fmt.Errorf("geo_ops matcher requires at least two arguments: %w", d.ArgErr())
			}

			m.Conditions[args[0]] = append(m.Conditions[args[0]], args[1:]...)
		}

		for nesting := d.Nesting(); d.NextBlock(nesting); {
			field := d.Val()

			values := d.RemainingArgs()
			if len(values) == 0 {
				return fmt.Errorf("geo_ops matcher field %s requires at least one value: %w", field, d.ArgErr())
			}

			m.Conditions[field] = append(m.Conditions[field], values...)
		}
	}

	return nil
}
