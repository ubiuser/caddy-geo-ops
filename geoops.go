// Package geoops registers the geo_ops Caddy modules: the shared app, the HTTP
// handler, and the HTTP matcher. Importing this package (e.g. via
// `xcaddy build --with github.com/ubiuser/caddy-geo-ops`) is enough to make all
// three available in Caddy.
package geoops

//revive:disable:blank-imports // see package comment above
import (
	_ "github.com/ubiuser/caddy-geo-ops/app"
	_ "github.com/ubiuser/caddy-geo-ops/handler"
	_ "github.com/ubiuser/caddy-geo-ops/matcher"
)
