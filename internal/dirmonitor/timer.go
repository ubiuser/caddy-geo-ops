package dirmonitor

import (
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type (
	timers struct {
		m       map[string]*time.Timer
		waitFor time.Duration
		mu      sync.Mutex
	}
)

const (
	defaultWaitFor = 100 * time.Millisecond
)

func (dm *DirMonitor) addOrResetTimer(filename string, callback func()) {
	dm.timers.mu.Lock()
	defer dm.timers.mu.Unlock()

	if timer, ok := dm.timers.m[toTimerKey(filename)]; ok {
		timer.Reset(dm.timers.waitFor)
		return
	}

	dm.timers.m[toTimerKey(filename)] = time.AfterFunc(dm.timers.waitFor, callback)
}

func (dm *DirMonitor) removeTimer(filename string) {
	dm.timers.mu.Lock()
	defer dm.timers.mu.Unlock()

	if timer, ok := dm.timers.m[toTimerKey(filename)]; ok {
		timer.Stop()
		delete(dm.timers.m, toTimerKey(filename))
	}
}

func toTimerKey(filename string) string {
	return strings.ToLower(filepath.Base(filename))
}
