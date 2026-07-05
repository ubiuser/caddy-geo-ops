// Package update periodically refreshes database files that already exist in
// the db folder. MaxMind editions are fetched via the geoipupdate client
// (Account ID + License Key); DB-IP Lite editions are fetched from hardcoded
// public URLs. Downloads are written to a temp file and atomically renamed over
// the target, which the dirmonitor then picks up and reloads.
package update

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/maxmind/geoipupdate/v7/client"
	"go.uber.org/zap"

	"github.com/ubiuser/caddy-geo-ops/internal/db"
	"github.com/ubiuser/caddy-geo-ops/internal/logfields"
)

type (
	// Config configures the Updater. MaxMind credentials are optional: if absent,
	// only DB-IP databases are updated.
	Config struct {
		DBInfoFn   func() map[db.Filename]string
		DBPath     string
		LicenseKey string
		AccountID  int
		Frequency  time.Duration
		Timeout    time.Duration
	}

	// Updater refreshes existing databases on a fixed interval.
	Updater struct {
		logger     *zap.Logger
		maxmind    *client.Client
		httpClient *http.Client
		getDBInfo  func() map[db.Filename]string
		cancel     context.CancelFunc
		dbPath     string
		baseURL    string
		wg         sync.WaitGroup
		frequency  time.Duration
		timeout    time.Duration
		closeOnce  sync.Once
	}
)

const (
	defaultFrequency = 24 * time.Hour
	defaultTimeout   = 30 * time.Second

	dbipBaseURL = "https://download.db-ip.com/free/"

	// tmpSuffix marks the in-progress temp files writeAtomic creates; it is also
	// the matcher for the startup cleanup of crash-leftover temps.
	tmpSuffix = ".tmp-"

	yyyyMMFormat = "2006-01"
)

var (
	errLoggerIsNil      = errors.New("logger is nil")
	errDBPathIsEmpty    = errors.New("db path is empty")
	errDBPathNotExist   = errors.New("db path does not exist")
	errDBInfoFnIsNil    = errors.New("db info function is nil")
	errUnexpectedStatus = errors.New("unexpected status")

	// errDBIPNotPublished means neither the current nor the previous month's
	// DB-IP file is available yet (the start-of-month gap). It is benign — the
	// existing database keeps serving — so it is logged at debug, not error.
	errDBIPNotPublished = errors.New("db-ip database not yet published")
	errNoDBIPUrl        = errors.New("no DB-IP url")
)

// New creates an Updater. It does not start any goroutines; call Start.
func New(logger *zap.Logger, config Config) (*Updater, error) {
	if logger == nil {
		return nil, errLoggerIsNil
	}

	if config.DBPath == "" {
		return nil, errDBPathIsEmpty
	}

	if config.DBInfoFn == nil {
		return nil, errDBInfoFnIsNil
	}

	if _, err := os.Stat(config.DBPath); err != nil {
		return nil, fmt.Errorf("%w (%s): %w", errDBPathNotExist, config.DBPath, err)
	}

	// Default on non-positive values too: a negative frequency would panic
	// time.NewTicker, and a non-positive timeout yields an already-expired ctx.
	if config.Frequency <= 0 {
		config.Frequency = defaultFrequency
	}

	if config.Timeout <= 0 {
		config.Timeout = defaultTimeout
	}

	u := &Updater{
		logger:     logger,
		dbPath:     config.DBPath,
		httpClient: &http.Client{},
		frequency:  config.Frequency,
		timeout:    config.Timeout,
		getDBInfo:  config.DBInfoFn,
		baseURL:    dbipBaseURL,
	}

	// Only build the MaxMind client when credentials are present; DB-IP needs none.
	if config.AccountID > 0 && config.LicenseKey != "" {
		c, err := client.New(config.AccountID, config.LicenseKey)
		if err != nil {
			return nil, fmt.Errorf("create maxmind client: %w", err)
		}

		u.maxmind = &c
	}

	// Remove temp files orphaned by a previous crash mid-write.
	u.cleanStaleTemps()

	return u, nil
}

// Start launches the periodic update loop.
func (u *Updater) Start() {
	ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // cancel is stored and invoked in Stop

	u.cancel = cancel

	u.logger.Info("starting periodic updater",
		logfields.Frequency(u.frequency),
		logfields.MaxmindEnabled(u.maxmind != nil),
	)
	u.warnIfMaxmindUnconfigured()

	ticker := time.NewTicker(u.frequency)

	u.wg.Go(func() {
		defer ticker.Stop()

		// Initial pass: refresh databases already older than the update
		// frequency so a cold start with a stale database updates promptly.
		// Recently-written files are skipped (see updateAll), so this does not
		// re-check the vendor on every config reload.
		u.updateAll(ctx, true)

		for {
			select {
			case <-ctx.Done():
				return

			case <-ticker.C:
				u.updateAll(ctx, false)
			}
		}
	})
}

// Stop cancels any in-flight download and waits for the loop to finish. It is
// idempotent.
func (u *Updater) Stop() {
	u.closeOnce.Do(func() {
		if u.cancel != nil {
			u.cancel()
		}

		u.wg.Wait()
	})
}

// warnIfMaxmindUnconfigured warns once at startup when MaxMind databases are
// present but no credentials are configured, so the operator knows those files
// will silently never be auto-updated (the per-cycle skip is only logged at
// debug). DB-IP-only setups are valid and do not trigger this.
func (u *Updater) warnIfMaxmindUnconfigured() {
	if u.maxmind != nil {
		return
	}

	for filename := range u.getDBInfo() {
		if db.IsGeoIP2OrGeoLite2(db.ToType(filename)) {
			u.logger.Warn("MaxMind databases present but no credentials configured; " +
				"they will not be auto-updated")

			return
		}
	}
}

// cleanStaleTemps removes writeAtomic temp files left behind by a crash between
// create and rename. It is age-gated by timeout so it never deletes a temp that
// a concurrent in-flight download (e.g. another updater during a config reload)
// is actively writing — a live temp is always younger than the download timeout.
func (u *Updater) cleanStaleTemps() {
	entries, err := os.ReadDir(u.dbPath)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.Contains(entry.Name(), tmpSuffix) {
			continue
		}

		var info os.FileInfo

		info, err = entry.Info()
		if err != nil || time.Since(info.ModTime()) < u.timeout {
			continue
		}

		path := filepath.Join(u.dbPath, entry.Name())
		if err = os.Remove(path); err != nil {
			u.logger.Debug("failed to remove stale temp", logfields.File(entry.Name()), zap.Error(err))
			continue
		}

		u.logger.Debug("removed stale temp", logfields.File(entry.Name()))
	}
}

// updateAll refreshes every recognised, already-present database. It bails out
// promptly if the context is cancelled (shutdown) rather than running the full
// set of sequential downloads.
//
//revive:disable-next-line:flag-parameter // the parameter `initial` is only used in the initial run
func (u *Updater) updateAll(ctx context.Context, initial bool) {
	for filename, md5 := range u.getDBInfo() {
		if ctx.Err() != nil {
			return
		}

		// On the initial startup pass, skip databases refreshed within the last
		// `frequency`. This avoids re-checking the vendor on every config reload
		// (which would exceed MaxMind's ~2-checks/day guidance), while still
		// refreshing a database that is genuinely stale on a cold start.
		if initial && u.recentlyUpdated(filename) {
			u.logger.Debug("skipping recently-updated database on initial pass",
				logfields.Database(string(filename)),
				logfields.Frequency(u.frequency),
			)

			continue
		}

		dbType := db.ToType(filename)

		switch {
		case db.IsGeoIP2OrGeoLite2(dbType):
			if u.maxmind == nil {
				u.logger.Debug("skipping MaxMind database; no credentials configured",
					logfields.Database(string(filename)),
				)

				continue
			}

			u.run(filename, func() (bool, error) { return u.downloadMaxmind(ctx, dbType, filename, md5) })

		case db.IsDBIP(dbType):
			u.run(filename, func() (bool, error) { return u.downloadDBIP(ctx, dbType, filename) })
		}
	}
}

// recentlyUpdated reports whether the database file was modified within the
// last `frequency` (i.e. it is not yet due for a refresh). A stat error means
// we can't tell, so it returns false (don't skip).
func (u *Updater) recentlyUpdated(filename db.Filename) bool {
	info, err := os.Stat(filepath.Join(u.dbPath, string(filename)))
	if err != nil {
		return false
	}

	return time.Since(info.ModTime()) < u.frequency
}

// run executes a single download and logs the outcome.
func (u *Updater) run(filename db.Filename, fn func() (bool, error)) {
	updated, err := fn()

	switch {
	case errors.Is(err, context.Canceled):
		// Shutting down; not an error.
	case errors.Is(err, errDBIPNotPublished):
		u.logger.Debug("database not yet published", logfields.Database(string(filename)))

	case err != nil:
		u.logger.Warn("update database", logfields.Database(string(filename)), zap.Error(err))

	case updated:
		u.logger.Info("database updated", logfields.Database(string(filename)))

	default:
		u.logger.Debug("database is up to date", logfields.Database(string(filename)))
	}
}

// downloadMaxmind fetches a GeoIP2/GeoLite2 edition via the geoipupdate client.
// The client performs the conditional check using md5 and returns an already
// decompressed mmdb stream.
func (u *Updater) downloadMaxmind(
	ctx context.Context,
	dbType db.Type,
	filename db.Filename,
	md5 string,
) (bool, error) {
	dctx, cancel := context.WithTimeout(ctx, u.timeout)
	defer cancel()

	resp, err := u.maxmind.Download(dctx, string(dbType), md5)
	if err != nil {
		return false, fmt.Errorf("download: %w", err)
	}
	defer resp.Reader.Close()

	if !resp.UpdateAvailable {
		return false, nil
	}

	if err = u.writeAtomic(filename, resp.Reader); err != nil {
		return false, fmt.Errorf("write: %w", err)
	}

	return true, nil
}

// downloadDBIP fetches a DB-IP Lite edition from its monthly URL, falling back
// to the previous month if the current month isn't published yet (the
// start-of-month gap). Returns errDBIPNotPublished if neither is available.
func (u *Updater) downloadDBIP(ctx context.Context, dbType db.Type, filename db.Filename) (bool, error) {
	now := time.Now().UTC()

	for _, when := range []time.Time{now, now.AddDate(0, -1, 0)} {
		url, ok := u.dbipURL(dbType, when)
		if !ok {
			return false, fmt.Errorf("%w for type %s", errNoDBIPUrl, dbType)
		}

		updated, found, err := u.fetchDBIP(ctx, filename, url)
		if err != nil {
			return false, fmt.Errorf("fetch %s: %w", url, err)
		}

		if found {
			return updated, nil
		}
	}

	return false, errDBIPNotPublished
}

// fetchDBIP performs one conditional GET. found is false on 404, so the caller
// can fall back to another month; on 304 it is true with updated=false.
func (u *Updater) fetchDBIP(
	ctx context.Context,
	filename db.Filename,
	url string,
) (updated, found bool, err error) {
	dctx, cancel := context.WithTimeout(ctx, u.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(dctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return false, false, fmt.Errorf("new request: %w", err)
	}

	if info, statErr := os.Stat(filepath.Join(u.dbPath, string(filename))); statErr == nil {
		req.Header.Set("If-Modified-Since", info.ModTime().UTC().Format(http.TimeFormat))
	}

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return false, false, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return false, true, nil

	case http.StatusNotFound:
		return false, false, nil

	case http.StatusOK:
		// proceed below
	default:
		return false, false, fmt.Errorf("%w %s for %s", errUnexpectedStatus, resp.Status, url)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return false, false, fmt.Errorf("gunzip: %w", err)
	}
	defer gz.Close()

	if err = u.writeAtomic(filename, gz); err != nil {
		return false, false, fmt.Errorf("write: %w", err)
	}

	return true, true, nil
}

// writeAtomic streams r into a temp file in the db directory and renames it
// over the target. Because readers use OpenBytes (no mmap), the target file is
// not held open, so the rename succeeds on Windows.
func (u *Updater) writeAtomic(filename db.Filename, r io.Reader) error {
	target := filepath.Join(u.dbPath, string(filename))

	tmp, err := os.CreateTemp(u.dbPath, string(filename)+tmpSuffix+"*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}

	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err = io.Copy(tmp, r); err != nil {
		if err2 := tmp.Close(); err2 != nil {
			return fmt.Errorf("copy: %w, close temp: %w", err, err2)
		}

		return fmt.Errorf("copy: %w", err)
	}

	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}

	if err = os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

// dbipURL returns the DB-IP Lite download URL for a type and month.
func (u *Updater) dbipURL(dbType db.Type, t time.Time) (string, bool) {
	var slug string

	switch dbType {
	case db.DBIPCityType:
		slug = "dbip-city-lite"

	case db.DBIPCountryType:
		slug = "dbip-country-lite"

	case db.DBIPASNType:
		slug = "dbip-asn-lite"

	default:
		return "", false
	}

	return fmt.Sprintf("%s%s-%s.mmdb.gz", u.baseURL, slug, t.Format(yyyyMMFormat)), true
}
