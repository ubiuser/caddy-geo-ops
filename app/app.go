// Package app implements the shared geo_ops Caddy app. It owns the database
// providers (loading, hot-reload, and optional auto-update) and is consumed by
// the http.handlers.geo_ops and http.matchers.geo_ops modules via ctx.App.
package app

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"go.uber.org/zap"

	"github.com/ubiuser/caddy-geo-ops/internal/ops"
	"github.com/ubiuser/caddy-geo-ops/internal/update"
)

type (
	// App is the geo_ops application module.
	//
	//nolint:tagliatelle // snake_case is the Caddyfile convention
	App struct {
		ops             *ops.Ops
		logger          *zap.Logger
		DBPath          string         `json:"db_path,omitempty"`
		LicenseKey      string         `json:"license_key,omitempty"`
		AccountID       int            `json:"account_id,omitempty"`
		UpdateFrequency caddy.Duration `json:"update_frequency,omitempty"`
		UpdateTimeout   caddy.Duration `json:"update_timeout,omitempty"`
		AutoUpdate      bool           `json:"auto_update,omitempty"`
	}
)

// AppID is the Caddy module ID / app namespace.
const AppID = "geo_ops"

var (
	_ caddy.Module          = (*App)(nil)
	_ caddy.Provisioner     = (*App)(nil)
	_ caddy.Validator       = (*App)(nil)
	_ caddy.CleanerUpper    = (*App)(nil)
	_ caddy.App             = (*App)(nil)
	_ caddyfile.Unmarshaler = (*App)(nil)

	errUnrecognized                         = errors.New("unrecognized geo_ops option")
	errDbPathRequired                       = errors.New("db_path is required")
	errAccountIDLicenseKeyMustBeSetTogether = errors.New(
		"auto_update: account_id and license_key must be set together (or both omitted for DB-IP-only updates)",
	)
)

func init() {
	caddy.RegisterModule(new(App))
	httpcaddyfile.RegisterGlobalOption(AppID, parseGlobalOption)
}

// CaddyModule returns the Caddy module information.
func (*App) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		// Must be a static string literal (matching AppID), not the AppID
		// constant: caddyserver.com's moduledoc scanner reads this field via AST
		// and only accepts a literal, so a constant/concatenation here makes the
		// plugin unregisterable on the download page.
		ID:  "geo_ops",
		New: func() caddy.Module { return new(App) },
	}
}

// Provision loads the databases from disk. No goroutines or network I/O happen
// here — those start in Start.
func (a *App) Provision(ctx caddy.Context) error {
	a.logger = ctx.Logger()

	if a.DBPath == "" {
		return errDbPathRequired
	}

	o, err := ops.New(a.logger, ops.Config{DBPath: a.DBPath})
	if err != nil {
		return fmt.Errorf("init geo databases: %w", err)
	}

	a.ops = o

	return nil
}

// Validate checks that the configuration is coherent. It runs after Provision.
func (a *App) Validate() error {
	if a.AutoUpdate {
		// MaxMind auto-update needs both credentials; only one set is almost
		// certainly a mistake (it would otherwise be silently ignored, leaving
		// MaxMind databases un-updated). Both omitted is valid: DB-IP only.
		if (a.AccountID > 0) != (a.LicenseKey != "") {
			return errAccountIDLicenseKeyMustBeSetTogether
		}

		return nil
	}

	// Credentials configured but auto_update is off: harmless, but they have no
	// effect, so warn rather than fail.
	if (a.AccountID > 0 || a.LicenseKey != "") && a.logger != nil {
		a.logger.Warn("account_id/license_key are set but auto_update is disabled; they have no effect")
	}

	return nil
}

// Start launches the directory watcher and, if enabled, the remote updater.
func (a *App) Start() error {
	if err := a.ops.StartWatcher(); err != nil {
		return fmt.Errorf("start watcher: %w", err)
	}

	if a.AutoUpdate {
		if err := a.ops.StartUpdater(update.Config{
			AccountID:  a.AccountID,
			LicenseKey: a.LicenseKey,
			Frequency:  time.Duration(a.UpdateFrequency),
			Timeout:    time.Duration(a.UpdateTimeout),
		}); err != nil {
			return fmt.Errorf("start updater: %w", err)
		}
	} else {
		a.logger.Debug("auto_update disabled; periodic updater not started")
	}

	return nil
}

// Stop tears down the watcher/updater and releases all providers. Caddy calls
// this for an app that was Started (the normal serving path).
func (a *App) Stop() error {
	if a.ops != nil {
		a.ops.Close()
	}

	return nil
}

// Cleanup releases the state allocated in Provision. Caddy calls it when the
// module instance is decommissioned — including when Provision succeeds but
// Validate (or a sibling module) fails, in which case Start/Stop never run, so
// this is the only release hook. Ops.Close is idempotent, so it's safe that
// Stop also calls it on the normal path.
func (a *App) Cleanup() error {
	if a.ops != nil {
		a.ops.Close()
	}

	return nil
}

// LookupAll resolves addr against all loaded databases, returning placeholder
// keys (geo.<db>.<field path>) to values. Used by the handler and matcher.
func (a *App) LookupAll(addr netip.Addr) map[string]string {
	if a.ops == nil {
		return nil
	}

	return a.ops.LookupAll(addr)
}

// UnmarshalCaddyfile parses the geo_ops global option block.
//
//	geo_ops {
//	    db_path          /var/lib/geoip
//	    auto_update
//	    account_id       123456
//	    license_key      xxxxxxxx
//	    update_frequency 24h
//	    update_timeout   30s
//	}
//
//nolint:cyclop,gocognit // nested block is unavoidable
func (a *App) UnmarshalCaddyfile(dispenser *caddyfile.Dispenser) error {
	for dispenser.Next() {
		for dispenser.NextBlock(0) {
			switch dispenser.Val() {
			case "db_path":
				if !dispenser.NextArg() {
					return fmt.Errorf("db_path requires an argument: %w", dispenser.ArgErr())
				}

				a.DBPath = dispenser.Val()

			case "auto_update":
				if dispenser.NextArg() {
					return fmt.Errorf("auto_update takes no arguments: %w", dispenser.ArgErr())
				}

				a.AutoUpdate = true

			case "account_id":
				if !dispenser.NextArg() {
					return fmt.Errorf("account_id requires an argument: %w", dispenser.ArgErr())
				}

				id, err := strconv.Atoi(dispenser.Val())
				if err != nil {
					return fmt.Errorf("account_id has an invalid value: %w", err)
				}

				a.AccountID = id

			case "license_key":
				if !dispenser.NextArg() {
					return fmt.Errorf("license_key requires an argument: %w", dispenser.ArgErr())
				}

				a.LicenseKey = dispenser.Val()

			case "update_frequency":
				if !dispenser.NextArg() {
					return fmt.Errorf("update_frequency requires an argument: %w", dispenser.ArgErr())
				}

				dur, err := caddy.ParseDuration(dispenser.Val())
				if err != nil {
					return fmt.Errorf("update_frequency has an invalid value: %w", err)
				}

				a.UpdateFrequency = caddy.Duration(dur)

			case "update_timeout":
				if !dispenser.NextArg() {
					return fmt.Errorf("update_timeout requires an argument: %w", dispenser.ArgErr())
				}

				dur, err := caddy.ParseDuration(dispenser.Val())
				if err != nil {
					return fmt.Errorf("update_timeout has an invalid value: %w", err)
				}

				a.UpdateTimeout = caddy.Duration(dur)

			default:
				return fmt.Errorf("%w: %s", errUnrecognized, dispenser.Val())
			}
		}
	}

	return nil
}

// parseGlobalOption adapts the Caddyfile global option into an app config.
func parseGlobalOption(d *caddyfile.Dispenser, _ any) (any, error) { //nolint:ireturn // Caddy global-option signature
	app := new(App)
	if err := app.UnmarshalCaddyfile(d); err != nil {
		return nil, fmt.Errorf("parsing geo_ops block: %w", err)
	}

	return httpcaddyfile.App{
		Name:  AppID,
		Value: caddyconfig.JSON(app, nil),
	}, nil
}
