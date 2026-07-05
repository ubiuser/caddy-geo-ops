// Package db defines the canonical filename taxonomy for the mmdb databases this
// module recognises and maps those filenames to their MaxMind/DB-IP type.
//
// Databases are identified by filename (case-insensitively), so each file must
// be named after its lowercase constant below — e.g. geoip2-city.mmdb,
// geolite2-asn.mmdb, dbip-city-lite.mmdb. A file that doesn't match is ignored.
//
// DB-IP note: the free downloads ship with a month suffix
// (e.g. dbip-city-lite-2024-06.mmdb) and must be renamed to the un-dated form
// (dbip-city-lite.mmdb) to be recognised. This is intentional — a single fixed
// name per edition means a new download (or manual copy) overwrites the previous
// file rather than leaving multiple dated versions accumulating in the db
// folder. The auto-updater already writes this canonical, un-dated name.
package db

import (
	"path"
	"strings"
)

// Filename is the lower case uniform filename of the database file.
type Filename string

const (
	GeoIP2AnonymousIP    Filename = "geoip2-anonymous-ip.mmdb"
	GeoIP2City           Filename = "geoip2-city.mmdb"
	GeoIP2ConnectionType Filename = "geoip2-connection-type.mmdb"
	GeoIP2Country        Filename = "geoip2-country.mmdb"
	GeoIP2Domain         Filename = "geoip2-domain.mmdb"
	GeoIP2Enterprise     Filename = "geoip2-enterprise.mmdb"
	GeoIP2ISP            Filename = "geoip2-isp.mmdb"

	GeoLite2ASN     Filename = "geolite2-asn.mmdb"
	GeoLite2City    Filename = "geolite2-city.mmdb"
	GeoLite2Country Filename = "geolite2-country.mmdb"

	DBIPCity    Filename = "dbip-city-lite.mmdb"
	DBIPCountry Filename = "dbip-country-lite.mmdb"
	DBIPASN     Filename = "dbip-asn-lite.mmdb"
)

// ToFilename converts the file path to a lowercase file name used as the
// stable key for a database (and as the placeholder source segment).
//
// Both "/" and "\" are treated as separators regardless of the current OS:
// filepath.Base only splits on the running OS's separator, but a db_path (from
// the Caddyfile) may use either, so a Windows-style path must still reduce to
// its base name on Linux and vice versa.
func ToFilename(filePath string) Filename {
	normalized := strings.ReplaceAll(filePath, `\`, "/")

	return Filename(strings.ToLower(path.Base(normalized)))
}

// Key returns the filename without its extension, used as the placeholder
// "source" segment, e.g. geoip2-city.mmdb -> "geoip2-city".
func (f Filename) Key() string {
	name := string(f)

	return strings.TrimSuffix(name, path.Ext(name))
}
