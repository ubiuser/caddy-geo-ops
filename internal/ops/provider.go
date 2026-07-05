package ops

import (
	//revive:disable-next-line:imports-blocklist // see below
	"crypto/md5" //nolint:gosec // MD5 required by the geoipupdate protocol, not for security
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"

	"github.com/oschwald/maxminddb-golang/v2"

	"github.com/ubiuser/caddy-geo-ops/internal/db"
	"github.com/ubiuser/caddy-geo-ops/internal/replacers"
)

type (
	// provider owns one open mmdb reader plus the metadata needed for conditional
	// updates and placeholder naming.
	provider struct {
		reader *maxminddb.Reader
		// prefix is the placeholder prefix for this database, e.g. "geo.geoip2-city".
		prefix string
		// md5 is the hex MD5 of the file the reader was built from; used by the
		// updater to skip unchanged downloads.
		md5 string
	}
)

const (
	// placeholderRoot is the first segment of every placeholder key this module
	// produces, e.g. geo.geoip2-city.country.iso_code.
	placeholderRoot = "geo"
)

// loadProvider reads the file fully into memory and opens a reader from the
// bytes (not mmap — see CLAUDE.md Windows/reload caveat). It validates the
// database by parsing it; a corrupt file returns an error so the caller can
// keep the previous provider.
func loadProvider(filePath string) (*provider, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	reader, err := maxminddb.OpenBytes(data)
	if err != nil {
		return nil, fmt.Errorf("open mmdb: %w", err)
	}

	sum := md5.Sum(data) //nolint:gosec // change detection for the geoipupdate protocol, not security

	return &provider{
		reader: reader,
		prefix: placeholderRoot + "." + db.ToFilename(filePath).Key(),
		md5:    hex.EncodeToString(sum[:]),
	}, nil
}

// flattenInto looks up addr and writes this database's fields into dst as
// prefixed, dotted-path placeholder keys. A missing record contributes nothing.
func (p *provider) flattenInto(addr netip.Addr, dst map[string]string) error {
	var record map[string]any
	// The IP is intentionally omitted from this error: the caller (Ops.LookupAll)
	// logs it once as the structured `ip` field, so it stays filterable/redactable
	// and isn't duplicated as free-text PII in the message.
	if err := p.reader.Lookup(addr).Decode(&record); err != nil {
		return fmt.Errorf("decode record: %w", err)
	}

	for key, value := range record {
		replacers.Flatten(p.prefix+"."+key, value, dst)
	}

	return nil
}

func (p *provider) stop() {
	if p.reader != nil {
		_ = p.reader.Close()
	}
}
