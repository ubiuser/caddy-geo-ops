package ops_test

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/ubiuser/caddy-geo-ops/internal/db"
	"github.com/ubiuser/caddy-geo-ops/internal/ops"
	"github.com/ubiuser/caddy-geo-ops/internal/update"
)

const (
	// fixtureDir is the MaxMind-DB test-data directory, provided by the MaxMind-DB
	// git submodule at the repository root.
	fixtureDir = "../../MaxMind-DB/test-data"
)

// testIP is the canonical MaxMind test address that resolves to London, GB.
var testIP = netip.MustParseAddr("81.2.69.142")

// copyFixture copies a MaxMind-DB fixture into dir under the given taxonomy
// filename, skipping the test if the fixture is unavailable.
func copyFixture(t *testing.T, fixture, dir string, name db.Filename) {
	t.Helper()

	src := filepath.Join(fixtureDir, fixture)

	data, err := os.ReadFile(src)
	if err != nil {
		t.Skipf("fixture %s unavailable: %v", src, err)
	}

	require.NoError(t, os.WriteFile(filepath.Join(dir, string(name)), data, 0o644))
}

func TestOpsLookupCity(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	copyFixture(t, "GeoIP2-City-Test.mmdb", dir, db.GeoIP2City)
	// An unrecognised file must be ignored, not fail New.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.txt"), []byte("ignore me"), 0o644))

	o, err := ops.New(zaptest.NewLogger(t), ops.Config{DBPath: dir})
	require.NoError(t, err)

	defer o.Close()

	data := o.LookupAll(testIP)
	require.NotEmptyf(t, data, "expected placeholders for test IP")

	assert.Equal(t, "GB", data["geo.geoip2-city.country.iso_code"])
	assert.Equal(t, "London", data["geo.geoip2-city.city.names.en"])
	assert.Contains(t, data, "geo.geoip2-city.location.latitude")
}

func TestOpsReloadAndDelete(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	o, err := ops.New(zaptest.NewLogger(t), ops.Config{DBPath: dir})
	require.NoError(t, err)

	defer o.Close()

	assert.Emptyf(t, o.LookupAll(testIP), "expected no data before any database is loaded")

	// Add a country database and reload it (simulating a file change / download).
	copyFixture(t, "GeoIP2-Country-Test.mmdb", dir, db.GeoIP2Country)

	path := filepath.Join(dir, string(db.GeoIP2Country))

	require.NoError(t, o.Reload(path))

	assert.Equal(t, "GB", o.LookupAll(testIP)["geo.geoip2-country.country.iso_code"])
	assert.NotEmptyf(t, o.GetDBInfo()[db.GeoIP2Country], "GetDBInfo should report an MD5")

	// Delete removes the provider.
	require.NoError(t, o.Delete(path))
	assert.Emptyf(t, o.LookupAll(testIP), "expected no data after delete")
}

func TestReloadUnknownDatabase(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	o, err := ops.New(zaptest.NewLogger(t), ops.Config{DBPath: dir})
	require.NoError(t, err)

	defer o.Close()

	err = o.Reload(filepath.Join(dir, "mystery.mmdb"))
	assert.ErrorIs(t, err, ops.ErrUnknownDatabase)

	// The watcher wrapper treats an unrecognised file as benign.
	assert.NoError(t, o.ReloadWatchedForTest(filepath.Join(dir, "mystery.mmdb")))
}

func TestReloadValidateBeforeSwap(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	copyFixture(t, "GeoIP2-City-Test.mmdb", dir, db.GeoIP2City)

	path := filepath.Join(dir, string(db.GeoIP2City))

	o, err := ops.New(zaptest.NewLogger(t), ops.Config{DBPath: dir})
	require.NoError(t, err)

	defer o.Close()

	require.Equal(t, "GB", o.LookupAll(testIP)["geo.geoip2-city.country.iso_code"])

	// Corrupt the file and reload: the load must fail, and the previously
	// loaded provider must keep serving (validate-before-swap).
	require.NoError(t, os.WriteFile(path, []byte("not a valid mmdb"), 0o644))

	require.Errorf(t, o.Reload(path), "reloading a corrupt database should fail")
	assert.Equalf(t, "GB", o.LookupAll(testIP)["geo.geoip2-city.country.iso_code"],
		"previous provider must keep serving after a failed reload")
}

func TestNewMissingDir(t *testing.T) {
	t.Parallel()

	_, err := ops.New(zaptest.NewLogger(t), ops.Config{DBPath: filepath.Join(t.TempDir(), "nope")})
	assert.Errorf(t, err, "a non-existent db path should error")
}

func TestStartUpdaterIdempotent(t *testing.T) {
	t.Parallel()

	o, err := ops.New(zaptest.NewLogger(t), ops.Config{DBPath: t.TempDir()})
	require.NoError(t, err)

	defer o.Close()

	require.NoError(t, o.StartUpdater(update.Config{}))

	first := o.Updater()
	require.NoError(t, o.StartUpdater(update.Config{}))

	assert.Samef(t, first, o.Updater(), "a second StartUpdater must not replace the running updater")
}
