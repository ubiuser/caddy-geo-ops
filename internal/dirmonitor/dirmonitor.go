// Package dirmonitor watches the database directory with fsnotify and, after a
// short debounce, invokes update/delete callbacks for changed *.mmdb files.
package dirmonitor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"

	"github.com/ubiuser/caddy-geo-ops/internal/logfields"
)

type (
	// Config holds the configuration for the DirMonitor.
	Config struct {
		UpdateFn func(filename string) error
		DeleteFn func(filename string) error
		DBPath   string
	}

	// DirMonitor watches the database directory with fsnotify and, after a
	// short debounce, invokes update/delete callbacks for changed *.mmdb files.
	// DirMonitor must be started before its callback channels will ever receive
	// values.
	//
	// The UpdateFn and DeleteFn callbacks are invoked exactly once per unique
	// file that changes during the lifetime of the DirMonitor, even if the file
	// is written to multiple times.
	DirMonitor struct {
		logger    *zap.Logger
		watcher   *fsnotify.Watcher
		updateCh  chan string
		deleteCh  chan string
		updateFn  func(filename string) error
		deleteFn  func(filename string) error
		quit      chan struct{}
		timers    timers
		wg        sync.WaitGroup
		closeOnce sync.Once
	}
)

var (
	ErrLoggerIsNil    = errors.New("logger is nil")
	ErrUpdateFnIsNil  = errors.New("update function is nil")
	ErrDeleteFnIsNil  = errors.New("delete function is nil")
	ErrDBPathIsEmpty  = errors.New("db path is empty")
	ErrDBPathNotExist = errors.New("db path does not exist")
)

// New creates a new DirMonitor.
func New(logger *zap.Logger, config Config) (*DirMonitor, error) {
	if logger == nil {
		return nil, ErrLoggerIsNil
	}

	if err := verifyConfig(config); err != nil {
		return nil, fmt.Errorf("verify config: %w", err)
	}

	dm := &DirMonitor{
		logger:   logger,
		updateCh: make(chan string),
		deleteCh: make(chan string),
		updateFn: config.UpdateFn,
		deleteFn: config.DeleteFn,
		timers: timers{
			mu:      sync.Mutex{},
			m:       make(map[string]*time.Timer),
			waitFor: defaultWaitFor,
		},
		quit: make(chan struct{}),
	}

	var err error

	dm.watcher, err = fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create watcher: %w", err)
	}

	if err = dm.watcher.Add(config.DBPath); err != nil {
		dm.watcher.Close()
		return nil, fmt.Errorf("add path: %w", err)
	}

	return dm, nil
}

// Start launches the event-handling and watcher goroutines.
func (dm *DirMonitor) Start() {
	dm.wg.Go(func() {
		dm.handleEvents()
	})

	dm.wg.Go(func() {
		dm.watcherLoop()
	})
}

// Close is idempotent; calling it more than once is a no-op.
func (dm *DirMonitor) Close() {
	dm.closeOnce.Do(func() {
		close(dm.quit)

		dm.timers.mu.Lock()
		for _, timer := range dm.timers.m {
			if timer != nil {
				timer.Stop()
			}
		}

		dm.timers.mu.Unlock()

		if dm.watcher != nil {
			dm.watcher.Close()
		}

		dm.wg.Wait()
	})
}

func verifyConfig(config Config) error {
	switch {
	case config.DBPath == "":
		return ErrDBPathIsEmpty

	case config.UpdateFn == nil:
		return ErrUpdateFnIsNil

	case config.DeleteFn == nil:
		return ErrDeleteFnIsNil
	}

	if _, err := os.Stat(config.DBPath); err != nil {
		return fmt.Errorf("%w (%s): %w", ErrDBPathNotExist, config.DBPath, err)
	}

	return nil
}

func (dm *DirMonitor) handleEvents() {
	for {
		select {
		case <-dm.quit:
			return

		case filename, ok := <-dm.updateCh:
			if !ok {
				return
			}

			if err := dm.updateFn(filename); err != nil {
				dm.logger.Warn("update callback", logfields.File(filename), zap.Error(err))
			}

		case filename, ok := <-dm.deleteCh:
			if !ok {
				return
			}

			if err := dm.deleteFn(filename); err != nil {
				dm.logger.Warn("delete callback", logfields.File(filename), zap.Error(err))
			}
		}
	}
}

//nolint:gocognit // complexity is difficult to avoid here
func (dm *DirMonitor) watcherLoop() {
	for {
		select {
		case event, ok := <-dm.watcher.Events:
			if !ok {
				dm.logWatcherClosed()
				return
			}

			if filepath.Ext(event.Name) != ".mmdb" {
				continue
			}

			// Debounce every change (create/write/chmod/remove/rename) for a file
			// into a single settle callback. Deciding update vs. delete from the
			// file's final state coalesces atomic-save patterns (remove+create)
			// into one update instead of a delete/update flap.
			if event.Has(fsnotify.Create) || event.Has(fsnotify.Write) ||
				event.Has(fsnotify.Chmod) || event.Has(fsnotify.Remove) ||
				event.Has(fsnotify.Rename) {
				name := event.Name
				dm.addOrResetTimer(name, func() {
					dm.removeTimer(name)
					dm.settle(name)
				})
			}

		case err, ok := <-dm.watcher.Errors:
			if !ok {
				dm.logWatcherClosed()
				return
			}

			if err != nil {
				dm.logger.Error("watcher error", zap.Error(err))
			}
		}
	}
}

// logWatcherClosed logs the watcher channels closing. A closed quit channel
// means Close() shut the watcher down (expected — debug); otherwise the watcher
// terminated on its own (unexpected — warn).
func (dm *DirMonitor) logWatcherClosed() {
	select {
	case <-dm.quit:
		dm.logger.Debug("watcher closed")

	default:
		dm.logger.Warn("watcher closed unexpectedly")
	}
}

// settle routes a debounced file change to the update or delete callback based
// on whether the file still exists once the debounce window has elapsed.
func (dm *DirMonitor) settle(name string) {
	ch, action := dm.updateCh, "update"
	if _, err := os.Stat(name); err != nil {
		ch, action = dm.deleteCh, "delete"
	}

	dm.logger.Debug("file change settled", logfields.File(name), logfields.Action(action))

	select {
	case ch <- name:
	case <-dm.quit:
	}
}
