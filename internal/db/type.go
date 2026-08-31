package db

// Type is the database type field in the mmdb file (the MaxMind/DB-IP edition).
type Type string

const (
	UnknownType Type = "unknown"

	GeoIP2AnonymousIPType    Type = "GeoIP2-Anonymous-IP"
	GeoIP2CityType           Type = "GeoIP2-City"
	GeoIP2ConnectionTypeType Type = "GeoIP2-Connection-Type"
	GeoIP2CountryType        Type = "GeoIP2-Country"
	GeoIP2DomainType         Type = "GeoIP2-Domain"
	GeoIP2EnterpriseType     Type = "GeoIP2-Enterprise"
	GeoIP2ISPType            Type = "GeoIP2-ISP"

	GeoLite2ASNType     Type = "GeoLite2-ASN"
	GeoLite2CityType    Type = "GeoLite2-City"
	GeoLite2CountryType Type = "GeoLite2-Country"

	DBIPCityType    Type = "DBIP-City-Lite"
	DBIPCountryType Type = "DBIP-Country-Lite"
	DBIPASNType     Type = "DBIP-ASN-Lite"

	IP2LocationCountryType Type = "IP2Location-Country"
	IP2LocationCityType    Type = "IP2Location-City"
	IP2LocationASNType     Type = "IP2Location-ASN"
)

// ToType converts a filename to a database type.
//
//nolint:cyclop // switch cases for all recognised database types (MaxMind/GeoLite2/DB-IP/IP2Location)
func ToType(filename Filename) Type {
	switch filename {
	case GeoIP2AnonymousIP:
		return GeoIP2AnonymousIPType

	case GeoIP2City:
		return GeoIP2CityType

	case GeoIP2ConnectionType:
		return GeoIP2ConnectionTypeType

	case GeoIP2Country:
		return GeoIP2CountryType

	case GeoIP2Domain:
		return GeoIP2DomainType

	case GeoIP2Enterprise:
		return GeoIP2EnterpriseType

	case GeoIP2ISP:
		return GeoIP2ISPType

	case GeoLite2ASN:
		return GeoLite2ASNType

	case GeoLite2City:
		return GeoLite2CityType

	case GeoLite2Country:
		return GeoLite2CountryType

	case DBIPCity:
		return DBIPCityType

	case DBIPCountry:
		return DBIPCountryType

	case DBIPASN:
		return DBIPASNType

	case IP2LocationCountry:
		return IP2LocationCountryType

	case IP2LocationCity:
		return IP2LocationCityType

	case IP2LocationASN:
		return IP2LocationASNType

	default:
		return UnknownType
	}
}

// IsGeoIP2OrGeoLite2 reports whether a database type is a MaxMind GeoIP2 or
// GeoLite2 edition (i.e. updatable via the geoipupdate client).
func IsGeoIP2OrGeoLite2(t Type) bool {
	switch t {
	case GeoIP2AnonymousIPType,
		GeoIP2CityType,
		GeoIP2ConnectionTypeType,
		GeoIP2CountryType,
		GeoIP2DomainType,
		GeoIP2EnterpriseType,
		GeoIP2ISPType,
		GeoLite2ASNType,
		GeoLite2CityType,
		GeoLite2CountryType:
		return true

	default:
		return false
	}
}

// IsDBIP reports whether a database type is a DB-IP Lite edition (updatable
// via the hardcoded DB-IP download URLs).
func IsDBIP(t Type) bool {
	switch t {
	case DBIPCityType, DBIPCountryType, DBIPASNType:
		return true

	default:
		return false
	}
}

// IsIP2Location reports whether a database type is an IP2Location LITE edition
// (updatable via the hardcoded IP2Location download endpoint).
func IsIP2Location(t Type) bool {
	switch t {
	case IP2LocationCountryType, IP2LocationCityType, IP2LocationASNType:
		return true

	default:
		return false
	}
}

// IsKnown reports whether the filename maps to a recognised database type.
func IsKnown(filename Filename) bool {
	return ToType(filename) != UnknownType
}
