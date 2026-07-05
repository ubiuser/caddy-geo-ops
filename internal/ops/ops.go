// Package ops is the shared core owned by the geo_ops Caddy app: a registry of
// database providers that the handler and matcher consume. It loads databases
// from a directory, hot-reloads them on change (validate-before-swap), and
// optionally runs the directory watcher and the remote updater.
package ops

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sync"

	"go.uber.org/zap"

	"github.com/ubiuser/caddy-geo-ops/internal/db"
	"github.com/ubiuser/caddy-geo-ops/internal/dirmonitor"
	"github.com/ubiuser/caddy-geo-ops/internal/logfields"
	"github.com/ubiuser/caddy-geo-ops/internal/update"
)

type (
	// Config configures the Ops core.
	Config struct {
		DBPath string
	}

	// Ops is a concurrency-safe registry of database providers keyed by filename.
	Ops struct {
		logger     *zap.Logger
		providers  map[db.Filename]*provider
		dirMonitor *dirmonitor.DirMonitor
		updater    *update.Updater
		dbPath     string
		mu         sync.RWMutex
	}
)

var (
	// ErrUnknownDatabase indicates a file whose name does not map to a supported
	// database edition; such files are skipped, not treated as errors.
	ErrUnknownDatabase   = errors.New("unknown database")
	errEmptyDatabasePath = errors.New("db path is empty")
)

// New loads every recognised *.mmdb file in the configured directory. It does
// not start the watcher or updater (call StartWatcher / StartUpdater).
func New(logger *zap.Logger, config Config) (*Ops, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	if config.DBPath == "" {
		return nil, errEmptyDatabasePath
	}

	o := &Ops{
		logger:    logger,
		dbPath:    config.DBPath,
		providers: make(map[db.Filename]*provider),
	}

	entries, err := os.ReadDir(o.dbPath)
	if err != nil {
		return nil, fmt.Errorf("read directory %s: %w", o.dbPath, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filePath := filepath.Join(o.dbPath, entry.Name())
		if err = o.Reload(filePath); err != nil {
			if errors.Is(err, ErrUnknownDatabase) {
				continue
			}

			return nil, fmt.Errorf("load %s: %w", filePath, err)
		}
	}

	return o, nil
}

// LookupAll resolves addr against every loaded database and returns the merged
// set of placeholder keys (geo.<db>.<field path>) to values.
func (o *Ops) LookupAll(addr netip.Addr) map[string]string {
	o.mu.RLock()
	defer o.mu.RUnlock()

	out := make(map[string]string)

	for filename, p := range o.providers {
		if err := p.flattenInto(addr, out); err != nil {
			o.logger.Warn("lookup",
				logfields.Database(string(filename)),
				logfields.IP(addr),
				zap.Error(err),
			)
		}
	}

	return out
}

// GetDBInfo returns the loaded databases and their MD5 sums, for the updater.
func (o *Ops) GetDBInfo() map[db.Filename]string {
	o.mu.RLock()
	defer o.mu.RUnlock()

	info := make(map[db.Filename]string, len(o.providers))
	for filename, p := range o.providers {
		info[filename] = p.md5
	}

	return info
}

// Reload loads filePath into a new provider and atomically swaps it in. The new
// reader is built and validated fully before the write lock is taken; if it
// fails to load, the existing provider keeps serving. Unrecognised files yield
// ErrUnknownDatabase and are ignored by callers.
func (o *Ops) Reload(filePath string) error {
	filename := db.ToFilename(filePath)
	if !db.IsKnown(filename) {
		return ErrUnknownDatabase
	}

	p, err := loadProvider(filePath)
	if err != nil {
		return fmt.Errorf("load provider: %w", err)
	}

	o.mu.Lock()
	old := o.providers[filename]

	o.providers[filename] = p
	o.mu.Unlock()

	if old != nil {
		old.stop()
	}

	o.logger.Info("database loaded",
		logfields.Database(string(filename)),
		logfields.MD5(p.md5),
	)

	return nil
}

// Delete removes the provider for filePath, if present.
func (o *Ops) Delete(filePath string) error {
	filename := db.ToFilename(filePath)

	o.mu.Lock()
	old := o.providers[filename]
	delete(o.providers, filename)
	o.mu.Unlock()

	if old != nil {
		old.stop()
		o.logger.Info("database removed", logfields.Database(string(filename)))
	}

	return nil
}

// StartWatcher starts the fsnotify directory watcher that drives hot reloads.
func (o *Ops) StartWatcher() error {
	if o.dirMonitor != nil {
		return nil
	}

	dm, err := dirmonitor.New(o.logger, dirmonitor.Config{
		DBPath:   o.dbPath,
		UpdateFn: o.reloadWatched,
		DeleteFn: o.Delete,
	})
	if err != nil {
		return fmt.Errorf("create dir monitor: %w", err)
	}

	dm.Start()

	o.dirMonitor = dm

	return nil
}

// StartUpdater starts the periodic remote updater. The watcher must be running
// so that downloaded files are picked up and reloaded; this starts it if not.
func (o *Ops) StartUpdater(config update.Config) error {
	if o.updater != nil {
		return nil
	}

	if err := o.StartWatcher(); err != nil {
		return fmt.Errorf("start watcher: %w", err)
	}

	config.DBPath = o.dbPath
	config.DBInfoFn = o.GetDBInfo

	u, err := update.New(o.logger, config)
	if err != nil {
		return fmt.Errorf("create updater: %w", err)
	}

	u.Start()

	o.updater = u

	return nil
}

// Close stops the watcher and updater and releases all providers.
func (o *Ops) Close() {
	if o.updater != nil {
		o.updater.Stop()

		o.updater = nil
	}

	if o.dirMonitor != nil {
		o.dirMonitor.Close()

		o.dirMonitor = nil
	}

	o.mu.Lock()
	for _, p := range o.providers {
		p.stop()
	}

	o.providers = nil
	o.mu.Unlock()
}

// reloadWatched is the dirmonitor's update callback. It reloads the file but
// treats an unrecognised name as benign (the watcher fires for any *.mmdb,
// while only known taxonomy names are loadable), so it doesn't surface as an
// error in the watcher's log.
func (o *Ops) reloadWatched(filePath string) error {
	if err := o.Reload(filePath); err != nil {
		if errors.Is(err, ErrUnknownDatabase) {
			o.logger.Debug("ignoring unrecognized database file", logfields.File(filePath))
			return nil
		}

		return fmt.Errorf("reload: %w", err)
	}

	return nil
}
