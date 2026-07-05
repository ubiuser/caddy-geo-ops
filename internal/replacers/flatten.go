// Package replacers turns a decoded mmdb record (arbitrary nested maps, slices
// and scalars) into a flat set of dotted-path placeholder keys. This generic
// approach means every field in any database edition — GeoIP2, GeoLite2, DB-IP,
// and future fields — becomes a placeholder with no per-field code.
package replacers

import (
	"math/big"
	"strconv"
)

// Flatten walks value and writes prefix-qualified, dotted-path entries into dst.
//
//	{"country": {"iso_code": "GB"}}  with prefix "geo.geoip2-city"
//	-> dst["geo.geoip2-city.country.iso_code"] = "GB"
//
// Maps recurse by key, slices recurse by zero-based index (e.g. ".0", ".1"),
// and scalars are stringified.
func Flatten(prefix string, value any, dst map[string]string) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			Flatten(prefix+"."+key, child, dst)
		}

	case []any:
		for i, child := range v {
			Flatten(prefix+"."+strconv.Itoa(i), child, dst)
		}

	default:
		if s, ok := scalarToString(v); ok {
			dst[prefix] = s
		}
	}
}

// scalarToString stringifies a leaf value. The second result is false for nil
// (so a null field is simply omitted, leaving the placeholder empty).
func scalarToString(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", false

	case string:
		return x, true

	case bool:
		return strconv.FormatBool(x), true

	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true

	case float32:
		return strconv.FormatFloat(float64(x), 'f', -1, 32), true

	case int:
		return strconv.Itoa(x), true

	case int8, int16, int32, int64:
		return strconv.FormatInt(toInt64(x), 10), true

	case uint, uint8, uint16, uint32, uint64, uintptr:
		return strconv.FormatUint(toUint64(x), 10), true

	case []byte:
		return string(x), true

	case *big.Int:
		// maxminddb decodes mmdb uint128 fields into *big.Int.
		return x.String(), true

	case big.Int:
		return x.String(), true

	default:
		return "", false
	}
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int8:
		return int64(x)

	case int16:
		return int64(x)

	case int32:
		return int64(x)

	case int64:
		return x

	default:
		return 0
	}
}

func toUint64(v any) uint64 {
	switch x := v.(type) {
	case uint:
		return uint64(x)

	case uint8:
		return uint64(x)

	case uint16:
		return uint64(x)

	case uint32:
		return uint64(x)

	case uint64:
		return x

	case uintptr:
		return uint64(x)

	default:
		return 0
	}
}
