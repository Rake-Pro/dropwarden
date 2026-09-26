package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rake-pro/dropwarden/internal/update"
	"github.com/rake-pro/dropwarden/internal/version"
	"github.com/rake-pro/dropwarden/internal/watcher"
)

// Updater is the self-updater the GUI host runs (UpdateController, wired by
// cmd/dropwarden). It is nil in the CLI and in tests; /api/update then reports
// a disabled updater.
type Updater interface {
	// UpdateState is the current snapshot for the dashboard and settings.
	UpdateState() UpdateStateDTO
	// CheckNow runs a manual check and returns the resulting state; a failed
	// feed check is reported through LastError, not an error. The error is
	// for a check that is refused outright (dev build).
	CheckNow(ctx context.Context) (UpdateStateDTO, error)
	// InstallNow starts installing the pending update immediately (download,
	// verify, hand off, quit), ignoring the watch-in-progress deferral. It
	// returns once the install is under way.
	InstallNow() error
}

// Update status values (UpdateStateDTO.Status).
const (
	UpdateStatusIdle          = "idle"
	UpdateStatusChecking      = "checking"
	UpdateStatusUpToDate      = "up_to_date"
	UpdateStatusAvailable     = "available"
	UpdateStatusNonePublished = "none_published"
	UpdateStatusError         = "error"
)

// Update phase values (UpdateStateDTO.Phase).
const (
	UpdatePhaseIdle        = "idle"
	UpdatePhaseChecking    = "checking"
	UpdatePhaseDownloading = "downloading"
	UpdatePhaseVerifying   = "verifying"
	UpdatePhaseInstalling  = "installing"
	UpdatePhaseHandingOff  = "handing_off"
	UpdatePhaseRestarting  = "restarting"
)

// UpdateStateDTO is GET /api/update, the body of POST /api/update/check and
// /api/update/install, and the "update" field of /api/state.
type UpdateStateDTO struct {
	CurrentVersion string            `json:"currentVersion"`
	Enabled        bool              `json:"enabled"` // background checks on (config + a feed configured)
	AutoInstall    bool              `json:"autoInstall"`
	ShowChangelog  bool              `json:"showChangelog"` // pending.notes is populated only when true
	IntervalHours  int               `json:"intervalHours"`
	FeedURL        string            `json:"feedURL"`  // "" = checking disabled
	DevBuild       bool              `json:"devBuild"` // dev build: checks and installs are refused
	Status         string            `json:"status"`   // UpdateStatus*
	Phase          string            `json:"phase"`    // UpdatePhase*
	Percent        int               `json:"percent"`  // download percent; -1 while the total is unknown
	BytesDone      int64             `json:"bytesDone"`
	BytesTotal     int64             `json:"bytesTotal"`
	Pending        *PendingUpdateDTO `json:"pending"`
	LastCheck      *time.Time        `json:"lastCheck"`
	LastError      string            `json:"lastError"`
	DeferredReason string            `json:"deferredReason"` // auto-install waiting on these watches
	FailedVersion  string            `json:"failedVersion"`  // last install that failed (this run or the previous one)
	FailMessage    string            `json:"failMessage"`    // friendly reason for FailedVersion
	SkippedVersion string            `json:"skippedVersion"` // auto-install of this version failed; only a manual install retries it
	PromptSeq      int               `json:"promptSeq"`      // bumped whenever the UI should open the update dialog
	// InstalledNotes are the running version's release notes (the feed's
	// notes when the feed names this exact version, else empty), for the
	// "Updated to vX" banner after a restart.
	InstalledNotes []update.NoteLine `json:"installedNotes"`
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

// Power is the host's sleep inhibitor (internal/power.Controller): Refresh
// re-evaluates the keep-awake mode against the watch state, Active reports
// whether the inhibitor is held.
type Power interface {
	Refresh()
	Active() bool
}

// SetPower installs the host's sleep inhibitor.
func (b *Backend) SetPower(p Power) {
	b.mu.Lock()
	b.pwr = p
	b.mu.Unlock()
}

func (b *Backend) power() Power {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.pwr
}

// KeepingAwake reports whether the sleep inhibitor is held right now.
func (b *Backend) KeepingAwake() bool {
	if p := b.power(); p != nil {
		return p.Active()
	}
	return false
}

// WrapEmitterForPower returns an emitter that forwards every engine event to
// next and refreshes the sleep inhibitor on each state transition.
func (b *Backend) WrapEmitterForPower(next watcher.Emitter) watcher.Emitter {
	return &powerEmitter{next: next, b: b}
}

type powerEmitter struct {
	next watcher.Emitter
	b    *Backend
}

func (e *powerEmitter) Emit(event string, payload any) {
	if e.next != nil {
		e.next.Emit(event, payload)
	}
	if event != watcher.EvState {
		return
	}
	if p := e.b.power(); p != nil {
		p.Refresh()
	}
}

// updateState returns the updater's state, or a disabled placeholder when no
// updater runs in this process.
func (b *Backend) updateState() UpdateStateDTO {
	if u := b.updater(); u != nil {
		return u.UpdateState()
	}
	cfg := b.config()
	return UpdateStateDTO{
		CurrentVersion: version.Version,
		DevBuild:       update.IsDev(version.Version),
		AutoInstall:    cfg.UpdateAutoInstall(),
		ShowChangelog:  cfg.UpdateShowChangelog(),
		IntervalHours:  cfg.Update.IntervalHours,
		FeedURL:        cfg.UpdateFeedURL(),
		Status:         UpdateStatusIdle,
		Phase:          UpdatePhaseIdle,
		Percent:        -1,
		LastError:      "the updater is not running in this process",
		InstalledNotes: []update.NoteLine{},
	}
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

// WatchSummary describes every watch in progress for the update deferral
// banner, e.g. "Twitch is watching sven; Kick is watching xqc". With more
// than one profile each entry names its profile. "" when nothing is mid-watch.
func (b *Backend) WatchSummary() string {
	multi := len(b.config().Profiles) > 1
	var parts []string
	for _, e := range b.AllEngines() {
		st := e.Status()
		if !st.State.WatchInProgress() {
			continue
		}
		who := platformLabel(st.Platform)
		if multi && st.Profile != "" {
			name := st.Profile
			if eff, err := b.config().Effective(st.Profile); err == nil {
				name = eff.DisplayName
			}
			who = name + "'s " + who
		}
		if st.ChannelLogin != "" {
			parts = append(parts, fmt.Sprintf("%s is watching %s", who, st.ChannelLogin))
		} else {
			parts = append(parts, fmt.Sprintf("%s is %s", who, st.State))
		}
	}
	return strings.Join(parts, "; ")
}

// platformLabel is the display name of a platform ("twitch" -> "Twitch").
func platformLabel(p string) string {
	switch p {
	case "kick":
		return "Kick"
	case "", "twitch":
		return "Twitch"
	}
	return strings.ToUpper(p[:1]) + p[1:]
}
