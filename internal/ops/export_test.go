package ops

import (
	"github.com/ubiuser/caddy-geo-ops/internal/update"
)

func (o *Ops) Updater() *update.Updater {
	return o.updater
}

func (o *Ops) ReloadWatchedForTest(path string) error {
	return o.reloadWatched(path)
}
