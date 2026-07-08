package app_test

import (
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/ubiuser/caddy-geo-ops/app"
	"github.com/ubiuser/caddy-geo-ops/internal/ops"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		app     app.App
		wantErr require.ErrorAssertionFunc
	}{
		"no creds, no auto_update": {
			app:     app.App{},
			wantErr: require.NoError,
		},
		"both creds + auto_update": {
			app:     app.App{AutoUpdate: true, AccountID: 1, LicenseKey: "k"},
			wantErr: require.NoError,
		},
		"db-ip only (auto_update, no creds)": {
			app:     app.App{AutoUpdate: true},
			wantErr: require.NoError,
		},
		"account_id only + auto_update": {
			app:     app.App{AutoUpdate: true, AccountID: 1},
			wantErr: require.Error,
		},
		"license_key only + auto_update": {
			app:     app.App{AutoUpdate: true, LicenseKey: "k"},
			wantErr: require.Error,
		},
		"creds without auto_update (warns, no error)": {
			app:     app.App{AccountID: 1, LicenseKey: "k"},
			wantErr: require.NoError,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := tc.app.Validate()
			tc.wantErr(t, err)
		})
	}
}

func TestCleanup(t *testing.T) {
	t.Parallel()

	// Provision failed before ops was built: Cleanup must not panic.
	var a app.App
	require.NoError(t, a.Cleanup())

	// With ops set (empty dir -> no providers), Stop then Cleanup are both safe
	// and idempotent — the double teardown must not panic.
	o, err := ops.New(zaptest.NewLogger(t), ops.Config{DBPath: t.TempDir()})
	require.NoError(t, err)

	a.SetOps(o)

	require.NoError(t, a.Stop())
	require.NoError(t, a.Cleanup())
}

// Ensure caddy.Duration is wired as expected (compile-time sanity on the type).
var _ caddy.Module = (*app.App)(nil)

func TestUnmarshalCaddyfile(t *testing.T) {
	t.Parallel()

	d := caddyfile.NewTestDispenser(`
		geo_ops {
			db_path          /var/lib/geoip
			auto_update
			account_id       12345
			license_key      secret-key
			update_frequency 12h
			update_timeout   20s
		}
	`)

	var a app.App
	require.NoError(t, a.UnmarshalCaddyfile(d))

	assert.Equal(t, "/var/lib/geoip", a.DBPath)
	assert.True(t, a.AutoUpdate)
	assert.Equal(t, 12345, a.AccountID)
	assert.Equal(t, "secret-key", a.LicenseKey)
	assert.Equal(t, 12*time.Hour, time.Duration(a.UpdateFrequency))
	assert.Equal(t, 20*time.Second, time.Duration(a.UpdateTimeout))
}

func TestUnmarshalCaddyfileErrors(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"unknown option":  `geo_ops { bogus value }`,
		"auto_update arg": `geo_ops { auto_update yes }`,
		// Missing arg must be on its own line: on a single line NextArg would
		// otherwise consume the closing "}" as the value.
		"db_path missing arg": "geo_ops {\n\tdb_path\n}",
		"bad account_id":      `geo_ops { account_id notanumber }`,
		"bad frequency":       `geo_ops { update_frequency nope }`,
	}

	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var a app.App
			assert.Error(t, a.UnmarshalCaddyfile(caddyfile.NewTestDispenser(input)))
		})
	}
}

func TestCaddyModuleID(t *testing.T) {
	t.Parallel()

	// app.go hard-codes this ID as a literal (moduledoc scanner requirement);
	// assert it still equals app.AppID so a namespace rename can't silently drift.
	assert.Equal(t, app.AppID, string(new(app.App).CaddyModule().ID))
}
