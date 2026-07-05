package logfields_test

import (
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/ubiuser/caddy-geo-ops/internal/logfields"
)

// TestFields locks in the key and zap field type each constructor produces —
// the contract that keeps log output filterable on a stable, typed set of keys.
// The key doubles as the subtest name (it uniquely identifies each field).
func TestFields(t *testing.T) {
	t.Parallel()

	cases := []struct {
		field    zap.Field
		wantKey  string
		wantType zapcore.FieldType
	}{
		{logfields.Database("GeoIP2-City"), "database", zapcore.StringType},
		{logfields.File("/db/geoip2-city.mmdb"), "file", zapcore.StringType},
		{logfields.MD5("d41d8cd98f00b204e9800998ecf8427e"), "md5", zapcore.StringType},
		{logfields.IP(netip.MustParseAddr("81.2.69.142")), "ip", zapcore.StringerType},
		{logfields.Frequency(time.Hour), "frequency", zapcore.DurationType},
		{logfields.MaxmindEnabled(true), "maxmind_enabled", zapcore.BoolType},
		{logfields.Action("update"), "action", zapcore.StringType},
		{logfields.GeoField("geoip2-country.country.iso_code"), "field", zapcore.StringType},
		{logfields.Allowed([]string{"US", "CA"}), "allowed", zapcore.ArrayMarshalerType},
		{logfields.Got("GB"), "got", zapcore.StringType},
		{logfields.Found(true), "found", zapcore.BoolType},
	}

	for _, tc := range cases {
		t.Run(tc.wantKey, func(t *testing.T) {
			t.Parallel()

			assert.Equalf(t, tc.wantKey, tc.field.Key, "field key")
			assert.Equalf(t, tc.wantType, tc.field.Type, "field type")
		})
	}
}
