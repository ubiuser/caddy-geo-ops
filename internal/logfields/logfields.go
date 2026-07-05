// Package logfields centralises the structured-log field keys used across the
// geo_ops module. Each constructor fixes both the key and the zap field type,
// so a given datum (an IP, a database name, ...) is always logged under the
// same key and type everywhere — giving operators a stable, documented set of
// keys to filter on.
package logfields

import (
	"net/netip"
	"time"

	"go.uber.org/zap"
)

// Database is the database edition / filename a log entry concerns.
func Database(name string) zap.Field {
	return zap.String("database", name)
}

// File is a filesystem path a log entry concerns.
func File(path string) zap.Field {
	return zap.String("file", path)
}

// MD5 is the hex MD5 sum of a database file.
func MD5(sum string) zap.Field {
	return zap.String("md5", sum)
}

// IP is a client IP address.
func IP(addr netip.Addr) zap.Field {
	return zap.Stringer("ip", addr)
}

// Frequency is the updater's refresh interval.
func Frequency(d time.Duration) zap.Field {
	return zap.Duration("frequency", d)
}

// MaxmindEnabled reports whether MaxMind credentials are configured.
func MaxmindEnabled(enabled bool) zap.Field {
	return zap.Bool("maxmind_enabled", enabled)
}

// Action is the resolved file-change action ("update" or "delete").
func Action(action string) zap.Field {
	return zap.String("action", action)
}

// GeoField is the matcher condition's geo field path.
func GeoField(name string) zap.Field {
	return zap.String("field", name)
}

// Allowed is the matcher condition's set of allowed values.
func Allowed(values []string) zap.Field {
	return zap.Strings("allowed", values)
}

// Got is the looked-up value the matcher compared against.
func Got(value string) zap.Field {
	return zap.String("got", value)
}

// Found reports whether the matcher field was present in the lookup data.
func Found(found bool) zap.Field {
	return zap.Bool("found", found)
}
