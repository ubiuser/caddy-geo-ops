package app

import (
	"github.com/ubiuser/caddy-geo-ops/internal/ops"
)

// SetOps allows tests to inject the Ops instance after the app is built.
func (a *App) SetOps(o *ops.Ops) {
	a.ops = o
}
