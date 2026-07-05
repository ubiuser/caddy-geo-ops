package replacers_test

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ubiuser/caddy-geo-ops/internal/replacers"
)

func TestFlatten(t *testing.T) {
	t.Parallel()

	record := map[string]any{
		"country": map[string]any{
			"iso_code":             "GB",
			"is_in_european_union": false,
			"names": map[string]any{
				"en":    "United Kingdom",
				"pt-BR": "Reino Unido",
			},
		},
		"location": map[string]any{
			"latitude":  51.5142,
			"longitude": -0.0931,
		},
		"subdivisions": []any{
			map[string]any{"iso_code": "ENG"},
			map[string]any{"iso_code": "WLS"},
		},
		"autonomous_system_number": uint64(12345),
		// mmdb uint128 fields decode to *big.Int.
		"big_value": new(big.Int).SetUint64(18446744073709551615),
	}

	got := make(map[string]string)
	for k, v := range record {
		replacers.Flatten("geo.geoip2-city."+k, v, got)
	}

	want := map[string]string{
		"geo.geoip2-city.country.iso_code":             "GB",
		"geo.geoip2-city.country.is_in_european_union": "false",
		"geo.geoip2-city.country.names.en":             "United Kingdom",
		"geo.geoip2-city.country.names.pt-BR":          "Reino Unido",
		"geo.geoip2-city.location.latitude":            "51.5142",
		"geo.geoip2-city.location.longitude":           "-0.0931",
		"geo.geoip2-city.subdivisions.0.iso_code":      "ENG",
		"geo.geoip2-city.subdivisions.1.iso_code":      "WLS",
		"geo.geoip2-city.autonomous_system_number":     "12345",
		"geo.geoip2-city.big_value":                    "18446744073709551615",
	}

	assert.Equal(t, want, got)
}

func TestScalarToString(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   any
		want string
		ok   bool
	}{
		{"string", "x", "x", true},
		{"bool", true, "true", true},
		{"float64", 1.5, "1.5", true},
		{"uint64", uint64(42), "42", true},
		{"int", -7, "-7", true},
		{"bytes", []byte("hi"), "hi", true},
		{"big.Int pointer (uint128)", new(big.Int).SetUint64(18446744073709551615), "18446744073709551615", true},
		{"nil", nil, "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := replacers.ScalarToString(tc.in)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.ok, ok)
		})
	}
}
