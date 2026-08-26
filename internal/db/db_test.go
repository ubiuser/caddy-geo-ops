package db_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/ubiuser/caddy-geo-ops/internal/db"
)

func TestKey(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "geoip2-city", db.GeoIP2City.Key())
	assert.Equal(t, "geoip2-anonymous-ip", db.GeoIP2AnonymousIP.Key())
	assert.Equal(t, "dbip-city-lite", db.DBIPCity.Key())
	assert.Equal(t, "ip2location-city", db.IP2LocationCity.Key())
}

func TestToFilename(t *testing.T) {
	t.Parallel()

	// Base name only, lowercased (recognition is case-insensitive).
	assert.Equal(t, db.GeoIP2City, db.ToFilename(`C:\data\GeoIP2-City.mmdb`))
	assert.Equal(t, db.GeoIP2City, db.ToFilename("/var/lib/geoip/geoip2-city.mmdb"))
}

// TestToTypeMappings pins every filename -> type mapping AND the exact Type
// string (which doubles as the MaxMind edition ID for the updater). A typo in a
// constant would otherwise silently break auto-update for that edition.
func TestToTypeMappings(t *testing.T) {
	t.Parallel()

	cases := []struct {
		filename    db.Filename
		typ         db.Type
		edition     string // exact type/edition-ID string
		maxmind     bool   // expected IsGeoIP2OrGeoLite2
		dbip        bool   // expected IsDBIP
		ip2location bool   // expected IsIP2Location
	}{
		{db.GeoIP2AnonymousIP, db.GeoIP2AnonymousIPType, "GeoIP2-Anonymous-IP", true, false, false},
		{db.GeoIP2City, db.GeoIP2CityType, "GeoIP2-City", true, false, false},
		{db.GeoIP2ConnectionType, db.GeoIP2ConnectionTypeType, "GeoIP2-Connection-Type", true, false, false},
		{db.GeoIP2Country, db.GeoIP2CountryType, "GeoIP2-Country", true, false, false},
		{db.GeoIP2Domain, db.GeoIP2DomainType, "GeoIP2-Domain", true, false, false},
		{db.GeoIP2Enterprise, db.GeoIP2EnterpriseType, "GeoIP2-Enterprise", true, false, false},
		{db.GeoIP2ISP, db.GeoIP2ISPType, "GeoIP2-ISP", true, false, false},
		{db.GeoLite2ASN, db.GeoLite2ASNType, "GeoLite2-ASN", true, false, false},
		{db.GeoLite2City, db.GeoLite2CityType, "GeoLite2-City", true, false, false},
		{db.GeoLite2Country, db.GeoLite2CountryType, "GeoLite2-Country", true, false, false},
		{db.DBIPCity, db.DBIPCityType, "DBIP-City-Lite", false, true, false},
		{db.DBIPCountry, db.DBIPCountryType, "DBIP-Country-Lite", false, true, false},
		{db.DBIPASN, db.DBIPASNType, "DBIP-ASN-Lite", false, true, false},
		{db.IP2LocationCountry, db.IP2LocationCountryType, "IP2Location-Country", false, false, true},
		{db.IP2LocationCity, db.IP2LocationCityType, "IP2Location-City", false, false, true},
		{db.IP2LocationASN, db.IP2LocationASNType, "IP2Location-ASN", false, false, true},
	}

	for _, tc := range cases {
		t.Run(string(tc.filename), func(t *testing.T) {
			t.Parallel()

			assert.Equalf(t, tc.typ, db.ToType(tc.filename), "filename -> type")
			assert.Equalf(t, tc.edition, string(tc.typ), "exact edition-ID string")
			assert.Truef(t, db.IsKnown(tc.filename), "should be a known database")
			assert.Equalf(t, tc.maxmind, db.IsGeoIP2OrGeoLite2(tc.typ), "IsGeoIP2OrGeoLite2")
			assert.Equalf(t, tc.dbip, db.IsDBIP(tc.typ), "IsDBIP")
			assert.Equalf(t, tc.ip2location, db.IsIP2Location(tc.typ), "IsIP2Location")

			// A known type must be in exactly one category.
			count := 0
			for _, b := range []bool{tc.maxmind, tc.dbip, tc.ip2location} {
				if b {
					count++
				}
			}

			assert.Equalf(t, 1, count, "type must be exactly one of MaxMind/DB-IP/IP2Location")
		})
	}
}

func TestUnknownAndCaseInsensitive(t *testing.T) {
	t.Parallel()

	assert.Equal(t, db.UnknownType, db.ToType("not-a-db.mmdb"))
	assert.False(t, db.IsKnown("not-a-db.mmdb"))
	assert.False(t, db.IsGeoIP2OrGeoLite2(db.UnknownType))
	assert.False(t, db.IsDBIP(db.UnknownType))
	assert.False(t, db.IsIP2Location(db.UnknownType))

	// GeoIP2-ASN is not a real MaxMind edition (ASN ships only as GeoLite2-ASN),
	// so it must not be recognised.
	assert.Equal(t, db.UnknownType, db.ToType("geoip2-asn.mmdb"))

	// Recognition is case-insensitive via ToFilename.
	assert.Equal(t, db.GeoIP2CityType, db.ToType(db.ToFilename(`C:\db\GeoIP2-City.MMDB`)))
}
