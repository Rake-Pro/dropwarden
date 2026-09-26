package service

import (
	"context"
	"time"

	"github.com/rake-pro/dropwarden/internal/update"
	"github.com/rake-pro/dropwarden/internal/version"
)

// Updater is the self-updater the GUI host runs (cmd/dropwarden's update
// controller). It is nil in the CLI and in tests; /api/update then reports a
// disabled updater.
type Updater interface {
	// UpdateState is the current snapshot for the dashboard and settings.
	UpdateState() UpdateStateDTO
	// CheckNow runs a manual check and returns the resulting state; a failed
	// check is reported through LastError, not an error.
	CheckNow(ctx context.Context) UpdateStateDTO
	// InstallNow starts installing the pending update immediately (download,
	// verify, hand off, quit), ignoring the watch-in-progress deferral. It
	// returns once the install is under way.
	InstallNow() error
}

// UpdateStateDTO is GET /api/update and the "update" field of /api/state.
type UpdateStateDTO struct {
	CurrentVersion string            `json:"currentVersion"`
	Enabled        bool              `json:"enabled"`       // background checks on (config + a feed configured)
	Available      bool              `json:"available"`     // an updater runs in this process at all
	Dev            bool              `json:"dev"`           // dev build: never auto-checks
	FeedURL        string            `json:"feedURL"`       // "" = checking disabled
	ShowChangelog  bool              `json:"showChangelog"` // pending.notes is populated only when true
	Pending        *PendingUpdateDTO `json:"pending"`
	LastCheck      *time.Time        `json:"lastCheck"`
	LastError      string            `json:"lastError"`
	SkippedVersion string            `json:"skippedVersion"` // auto-install of this version failed; only a manual install retries it
	BusyDeferred   bool              `json:"busyDeferred"`   // auto-install waiting for a watch to finish
	Checking       bool              `json:"checking"`
	Installing     bool              `json:"installing"`
}

// PendingUpdateDTO is a found, not yet installed release.
type PendingUpdateDTO struct {
	Version string            `json:"version"`
	Notes   []update.NoteLine `json:"notes"`
}

// SetUpdater installs the host's self-updater.
func (b *Backend) SetUpdater(u Updater) {
	b.mu.Lock()
	b.upd = u
	b.mu.Unlock()
}

func (b *Backend) updater() Updater {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.upd
}

// updateState returns the updater's state, or a disabled placeholder when no
// updater runs in this process.
func (b *Backend) updateState() UpdateStateDTO {
	if u := b.updater(); u != nil {
		return u.UpdateState()
	}
	return UpdateStateDTO{CurrentVersion: version.Version, Dev: update.IsDev(version.Version), LastError: "the updater is not running in this process"}
}

// WatchInProgress reports whether any engine, in any profile, is opening,
// validating, watching or claiming right now. The self-updater defers an
// automatic install while this is true.
func (b *Backend) WatchInProgress() bool {
	for _, e := range b.AllEngines() {
		if e.Status().State.WatchInProgress() {
			return true
		}
	}
	return false
}
