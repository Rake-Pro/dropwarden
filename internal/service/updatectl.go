package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/update"
	"github.com/rake-pro/dropwarden/internal/version"
	"github.com/rake-pro/dropwarden/internal/watcher"
)

const (
	// updateStartupDelay lets the tray, Chrome and the network come up before
	// the launch check.
	updateStartupDelay = 30 * time.Second
	// updateDeferRecheck is the fallback cadence at which a deferred
	// auto-install re-evaluates whether a watch is still in progress (engine
	// state transitions out of a watch re-evaluate at once).
	updateDeferRecheck = 5 * time.Minute
	// updateDownloadTimeout bounds one release download.
	updateDownloadTimeout = 15 * time.Minute
	// defaultRestartDelay gives the UI time to paint "restarting" before quit.
	defaultRestartDelay = 1500 * time.Millisecond
)

// errDevBuild refuses checks and installs on a dev build: a 0.0.0-dev binary
// would otherwise immediately "upgrade" to the last release.
var errDevBuild = errors.New("dev build, updates disabled")

// UpdateOptions configures an UpdateController. Config and Backend are
// required; the rest have working defaults.
type UpdateOptions struct {
	Ctx     context.Context       // lifetime; cancelled when the app shuts down
	Config  func() *config.Config // live config: settings edits apply without a restart
	Backend *Backend

	Version  string // running version; default version.Version
	Helper   string // dropwarden-cli path; "" = install in-process
	Relaunch string // update.SelfLaunchPath of this host
	HostExe  string // resolved host executable, probed for writability

	// StartHelper starts `helper update-apply jobPath` detached from this
	// process. Required when Helper is set.
	StartHelper func(helper, jobPath string) error
	// Apply installs an archive in-process and returns the relaunch path;
	// default update.Apply.
	Apply func(archive string) (string, error)
	// Changed runs after every state change (the tray retitles itself).
	Changed func()
	// RestartDelay is the pause between phase "restarting" and quit; default
	// 1.5 s.
	RestartDelay time.Duration
}

// UpdateController owns the self-updater: the launch + periodic background
// checks, the tray item's state, and the /api/update snapshot the dashboard
// reads. Install downloads and verifies in this process, then hands the
// archive to `dropwarden-cli update-apply` (a sibling binary) and quits;
// without that helper it installs in-process and relaunches from
// PostShutdown. It implements Updater.
type UpdateController struct {
	opt UpdateOptions
	dev bool

	wake chan struct{} // an engine left a watch: re-evaluate a deferred install

	mu             sync.Mutex
	quit           func()
	show           func()
	pending        *update.Release
	installed      *update.Release // the feed's release when it is the running version
	lastCheck      time.Time
	lastErr        string
	status         string
	phase          string
	bytesDone      int64
	bytesTotal     int64
	trayNote       string // manual-check result suffix on the tray item
	checking       bool
	installing     bool
	deferring      bool // the deferral loop is running
	deferredReason string
	postRelaunch   string // in-process install: start this after shutdown
	skipVersion    string // auto-install of this version failed; manual install only
	failedVersion  string
	failMessage    string
	promptSeq      int
}

var _ Updater = (*UpdateController)(nil)

// NewUpdateController builds the controller. Nothing runs until Start.
func NewUpdateController(opt UpdateOptions) *UpdateController {
	if opt.Ctx == nil {
		opt.Ctx = context.Background()
	}
	if opt.Version == "" {
		opt.Version = version.Version
	}
	if opt.Apply == nil {
		opt.Apply = update.Apply
	}
	if opt.RestartDelay <= 0 {
		opt.RestartDelay = defaultRestartDelay
	}
	return &UpdateController{
		opt:    opt,
		dev:    update.IsDev(opt.Version),
		wake:   make(chan struct{}, 1),
		quit:   func() {},
		show:   func() {},
		status: UpdateStatusIdle,
		phase:  UpdatePhaseIdle,
	}
}

// SetWindowHooks installs the app's quit and show-dashboard actions, which
// exist only once the window does.
func (c *UpdateController) SetWindowHooks(quit, show func()) {
	c.mu.Lock()
	if quit != nil {
		c.quit = quit
	}
	if show != nil {
		c.show = show
	}
	c.mu.Unlock()
}

func (c *UpdateController) changed() {
	if c.opt.Changed != nil {
		c.opt.Changed()
	}
}

func (c *UpdateController) doShow() {
	c.mu.Lock()
	f := c.show
	c.mu.Unlock()
	f()
}

// LoadFailure picks up the marker a failed update-apply left for this start
// (update.FailureFile in the app data dir) and deletes it: the failure is
// surfaced through failedVersion/failMessage and automatic installs skip that
// version, so a failing update cannot turn into a restart loop. When a marker
// existed, a check runs right away so the pending release is known again.
// Called once the single-instance lock is held.
func (c *UpdateController) LoadFailure() {
	dir, err := config.Dir()
	if err != nil {
		return
	}
	c.loadFailureFrom(dir)
}

func (c *UpdateController) loadFailureFrom(dir string) {
	f := update.TakeFailure(dir)
	if f == nil {
		return
	}
	log.Warn().Str("version", f.Version).Str("error", f.Error).Msg("update: previous install failed; automatic install of this version is skipped")
	c.mu.Lock()
	c.skipVersion = f.Version
	c.failedVersion, c.failMessage = f.Version, friendlyUpdateError(f.Error)
	c.mu.Unlock()
	c.changed()
	if !c.dev && c.opt.Config().UpdateFeedURL() != "" {
		go c.check(c.opt.Ctx, false)
	}
}

// friendlyUpdateError maps an install error to the sentence the UI shows.
func friendlyUpdateError(raw string) string {
	l := strings.ToLower(raw)
	switch {
	case strings.Contains(l, "not writable") || strings.Contains(l, "permission denied") || strings.Contains(l, "access is denied"):
		return "The install folder is not writable"
	case strings.Contains(l, "checksum mismatch"):
		return "The download did not verify (checksum mismatch)"
	case strings.Contains(l, "timeout") || strings.Contains(l, "deadline exceeded") || strings.Contains(l, "timed out"):
		return "The download timed out"
	}
	return raw
}

// Start runs the launch check (~30 s in) and then one check every
// update.interval_hours, reading the live config each time. Dev builds never
// check.
func (c *UpdateController) Start() {
	if c.dev {
		log.Debug().Msg("update: dev build, background checks disabled")
		return
	}
	if c.opt.Config().UpdateFeedURL() == "" {
		log.Info().Msg("update: update.feed_url is empty; update checks are disabled")
	}
	go func() {
		wait := updateStartupDelay
		for {
			t := time.NewTimer(wait)
			select {
			case <-c.opt.Ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
			cfg := c.opt.Config()
			if cfg.UpdateEnabled() && cfg.UpdateFeedURL() != "" {
				c.check(c.opt.Ctx, false)
			}
			wait = time.Duration(cfg.Update.IntervalHours) * time.Hour
			if wait <= 0 {
				wait = 24 * time.Hour
			}
		}
	}()
}

// TrayClick is the tray item: with an update pending it opens the dashboard
// and asks it (promptSeq) to show the update dialog; otherwise it runs a
// manual check, which does the same when it finds one. It never installs.
func (c *UpdateController) TrayClick() {
	c.mu.Lock()
	pending, busy := c.pending, c.checking || c.installing
	if pending != nil && !busy {
		c.promptSeq++
	}
	c.mu.Unlock()
	switch {
	case c.dev || busy:
		c.doShow()
	case pending != nil:
		c.changed()
		c.doShow()
	default:
		c.check(c.opt.Ctx, true)
	}
}

// TrayLabel is the tray item's current title and enabled state.
func (c *UpdateController) TrayLabel() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	title := "Check for Updates"
	switch {
	case c.dev:
		return title + " (dev build)", false
	case c.installing && c.pending != nil:
		return "Installing Update v" + c.pending.Version + "...", false
	case c.checking:
		return "Checking for Updates...", false
	case c.pending != nil:
		return "Update v" + c.pending.Version + " Available...", true
	case c.trayNote != "":
		title += " (" + c.trayNote + ")"
	}
	return title, true
}

// check queries the feed. Background checks that find an update install it
// when auto_install is on (deferred while a watch is in progress); manual
// checks record the result, open the dashboard and bump promptSeq.
func (c *UpdateController) check(ctx context.Context, manual bool) {
	c.mu.Lock()
	if c.checking || c.installing {
		c.mu.Unlock()
		return
	}
	c.checking = true
	c.trayNote = ""
	c.status, c.phase = UpdateStatusChecking, UpdatePhaseChecking
	c.mu.Unlock()
	c.changed()

	rel, latest, err := c.doCheck(ctx)

	c.mu.Lock()
	c.checking = false
	c.phase = UpdatePhaseIdle
	c.lastCheck = time.Now()
	switch {
	case errors.Is(err, update.ErrNoRelease):
		c.status, c.lastErr, c.pending = UpdateStatusNonePublished, "", nil
		if manual {
			c.trayNote = "nothing published yet"
		}
	case err != nil:
		c.status, c.lastErr = UpdateStatusError, err.Error()
		if manual {
			c.trayNote = "last check failed"
		}
	default:
		// A skip only holds while the feed still offers that same version.
		if rel == nil || rel.Version != c.skipVersion {
			c.skipVersion = ""
			if rel == nil || rel.Version != c.failedVersion {
				c.failedVersion, c.failMessage = "", ""
			}
		}
		c.lastErr = ""
		c.pending = rel
		if latest != nil && latest.Version == strings.TrimPrefix(c.opt.Version, "v") {
			c.installed = latest
		}
		if rel == nil {
			c.status = UpdateStatusUpToDate
			if manual {
				c.trayNote = "up to date"
			}
		} else {
			c.status = UpdateStatusAvailable
			if manual {
				c.promptSeq++
			}
		}
	}
	skip := c.skipVersion
	c.mu.Unlock()
	c.changed()

	switch {
	case errors.Is(err, update.ErrNoRelease):
		log.Info().Msg("update: no release published yet")
	case err != nil:
		log.Warn().Err(err).Msg("update check failed")
	case rel == nil:
		log.Debug().Msg("update: up to date")
	default:
		log.Info().Str("version", rel.Version).Msg("update available")
		if manual {
			c.doShow() // the dashboard opens the update dialog (promptSeq)
		} else if rel.Version == skip {
			log.Warn().Str("version", rel.Version).Msg("update: automatic install of this version failed before; waiting for a manual install")
		} else if c.opt.Config().UpdateAutoInstall() {
			c.autoInstall(rel)
		}
	}
}

// doCheck returns the newer release (nil when up to date) and the release
// the feed names, newer or not.
func (c *UpdateController) doCheck(ctx context.Context) (*update.Release, *update.Release, error) {
	feed := c.opt.Config().UpdateFeedURL()
	if feed == "" {
		return nil, nil, errors.New("update checks are disabled: update.feed_url is empty")
	}
	upd, err := update.New(update.Config{FeedURL: feed, Current: c.opt.Version})
	if err != nil {
		return nil, nil, err
	}
	return upd.CheckLatest(ctx)
}

// autoInstall installs rel now unless a watch is in progress, in which case
// it waits for an engine to leave its watch (or the fallback ticker) and
// installs as soon as every engine is between watches. A manual install
// always goes straight through.
func (c *UpdateController) autoInstall(rel *update.Release) {
	if !c.opt.Backend.WatchInProgress() {
		if err := c.install(rel, false); err == nil {
			return
		}
	}
	reason := c.opt.Backend.WatchSummary()
	c.mu.Lock()
	c.deferredReason = reason
	start := !c.deferring
	c.deferring = true
	c.mu.Unlock()
	c.changed()
	log.Info().Str("version", rel.Version).Str("reason", reason).Msg("update: watch in progress, deferring auto-install")
	if start {
		go c.deferLoop()
	}
}

func (c *UpdateController) deferLoop() {
	t := time.NewTicker(updateDeferRecheck)
	defer t.Stop()
	stop := func() {
		c.mu.Lock()
		c.deferring, c.deferredReason = false, ""
		c.mu.Unlock()
		c.changed()
	}
	for {
		select {
		case <-c.opt.Ctx.Done():
			stop()
			return
		case <-t.C:
		case <-c.wake:
		}
		c.mu.Lock()
		rel, installing, skip := c.pending, c.installing, c.skipVersion
		c.mu.Unlock()
		if rel == nil || installing || rel.Version == skip || !c.opt.Config().UpdateAutoInstall() {
			stop()
			return
		}
		if c.opt.Backend.WatchInProgress() {
			reason := c.opt.Backend.WatchSummary()
			c.mu.Lock()
			c.deferredReason = reason
			c.mu.Unlock()
			c.changed()
			log.Debug().Str("version", rel.Version).Str("reason", reason).Msg("update: watch still in progress, auto-install stays deferred")
			continue
		}
		if err := c.install(rel, false); err != nil {
			log.Debug().Err(err).Str("version", rel.Version).Msg("update: auto-install declined, retrying on the next tick")
			continue
		}
		stop()
		return
	}
}

// WatchEnded re-evaluates a deferred auto-install now (an engine just left a
// watch). Cheap and non-blocking; a no-op when nothing is deferred.
func (c *UpdateController) WatchEnded() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// WrapEmitter returns an emitter that forwards every engine event to next and
// calls WatchEnded on each state transition out of a watch.
func (c *UpdateController) WrapEmitter(next watcher.Emitter) watcher.Emitter {
	return &watchEndEmitter{next: next, c: c}
}

type watchEndEmitter struct {
	next watcher.Emitter
	c    *UpdateController
}

func (e *watchEndEmitter) Emit(event string, payload any) {
	if e.next != nil {
		e.next.Emit(event, payload)
	}
	if event != watcher.EvState {
		return
	}
	if p, ok := payload.(watcher.StatePayload); ok && p.From.WatchInProgress() && !p.To.WatchInProgress() {
		e.c.WatchEnded()
	}
}

// InstallNow implements Updater: install the pending update right away, in
// the background, whatever the engines are doing.
func (c *UpdateController) InstallNow() error {
	if c.dev {
		return errDevBuild
	}
	c.mu.Lock()
	rel := c.pending
	c.mu.Unlock()
	if rel == nil {
		return errors.New("no update is pending - check for updates first")
	}
	return c.install(rel, true)
}

// install claims the installer and returns an error, without doing anything,
// when an install or a check is already running. Otherwise it runs doInstall
// in the background and returns nil. A manual install clears an automatic
// install skip.
func (c *UpdateController) install(rel *update.Release, manual bool) error {
	c.mu.Lock()
	switch {
	case c.installing:
		c.mu.Unlock()
		return errors.New("an update is already installing")
	case c.checking:
		c.mu.Unlock()
		return errors.New("an update check is running - try again in a moment")
	}
	c.installing = true
	c.lastErr = ""
	c.deferredReason = ""
	c.phase, c.bytesDone, c.bytesTotal = UpdatePhaseDownloading, 0, 0
	if manual {
		c.skipVersion = ""
		c.failedVersion, c.failMessage = "", ""
	}
	c.mu.Unlock()
	c.changed()
	go c.doInstall(rel, manual)
	return nil
}

func (c *UpdateController) setPhase(p string) {
	c.mu.Lock()
	c.phase = p
	c.mu.Unlock()
	c.changed()
}

// doInstall downloads and verifies rel in this process (so a network failure
// never leaves the app quit), then hands the archive to the helper and quits.
// A failed automatic install skips that version until the user installs it
// by hand, so the deferral loop and later checks never retry it on their own.
func (c *UpdateController) doInstall(rel *update.Release, manual bool) {
	failed := func(msg string, err error) {
		log.Error().Err(err).Str("version", rel.Version).Msg(msg)
		c.mu.Lock()
		c.installing = false
		c.phase, c.bytesDone, c.bytesTotal = UpdatePhaseIdle, 0, 0
		c.lastErr = err.Error()
		c.failedVersion, c.failMessage = rel.Version, friendlyUpdateError(err.Error())
		if !manual {
			c.skipVersion = rel.Version
		}
		c.mu.Unlock()
		c.changed()
	}

	// Refuse up front when the install location cannot be written: handing
	// off and quitting would only fail in the updater and restart this build.
	if c.opt.HostExe != "" {
		if err := update.ProbeWritable(c.opt.HostExe); err != nil {
			failed("update install location check failed", fmt.Errorf("install location is not writable: %w", err))
			return
		}
	}

	feed := c.opt.Config().UpdateFeedURL()
	upd, err := update.New(update.Config{FeedURL: feed, Current: c.opt.Version})
	if err != nil {
		failed("update download failed", err)
		return
	}
	ctx, cancel := context.WithTimeout(c.opt.Ctx, updateDownloadTimeout)
	archive, err := upd.DownloadProgress(ctx, rel, func(done, total int64) {
		c.mu.Lock()
		c.bytesDone, c.bytesTotal = done, total
		verifying := total > 0 && done >= total && c.phase != UpdatePhaseVerifying
		if verifying {
			c.phase = UpdatePhaseVerifying
		}
		c.mu.Unlock()
		if verifying {
			c.changed()
		}
	})
	cancel()
	if err != nil {
		failed("update download failed", err)
		return
	}

	if c.opt.Helper != "" && c.opt.Relaunch != "" && c.opt.StartHelper != nil {
		c.setPhase(UpdatePhaseHandingOff)
		job := update.Job{
			Version:  rel.Version,
			FeedURL:  feed,
			Current:  c.opt.Version,
			HostPID:  os.Getpid(),
			Relaunch: c.opt.Relaunch,
			HostExe:  c.opt.HostExe,
			Args:     os.Args[1:],
			Archive:  archive,
		}
		path, err := update.WriteJob(job)
		if err != nil {
			os.Remove(archive)
			failed("update handoff failed", err)
			return
		}
		if err := c.opt.StartHelper(c.opt.Helper, path); err != nil {
			os.Remove(path)
			os.Remove(archive)
			failed("update handoff: start updater", err)
			return
		}
		log.Info().Str("version", rel.Version).Str("helper", c.opt.Helper).Msg("update handed off to updater; quitting")
		c.restart()
		return
	}

	c.setPhase(UpdatePhaseInstalling)
	relaunch, err := c.opt.Apply(archive)
	if err != nil {
		os.Remove(archive)
		failed("update apply failed", err)
		return
	}
	c.mu.Lock()
	c.postRelaunch = relaunch
	c.mu.Unlock()
	log.Info().Str("version", rel.Version).Msg("update installed in-process; restarting")
	c.restart()
}

// restart shows phase "restarting" long enough for the UI to paint it, then
// quits the app.
func (c *UpdateController) restart() {
	c.setPhase(UpdatePhaseRestarting)
	select {
	case <-time.After(c.opt.RestartDelay):
	case <-c.opt.Ctx.Done():
	}
	c.mu.Lock()
	quit := c.quit
	c.mu.Unlock()
	quit()
}

// PostShutdown runs as the Wails PostShutdown hook, after the single-instance
// lock is released: an in-process install starts the new binary here, so it
// is not turned away by the still-running old instance.
func (c *UpdateController) PostShutdown() {
	c.mu.Lock()
	p := c.postRelaunch
	c.mu.Unlock()
	if p == "" {
		return
	}
	if err := update.Relaunch(p, os.Args[1:]...); err != nil {
		log.Error().Err(err).Msg("update relaunch failed - start the app manually")
	}
}

// CheckNow implements Updater (the dashboard's Check button).
func (c *UpdateController) CheckNow(ctx context.Context) (UpdateStateDTO, error) {
	if c.dev {
		return c.UpdateState(), errDevBuild
	}
	c.check(ctx, true)
	return c.UpdateState(), nil
}

// UpdateState implements Updater.
func (c *UpdateController) UpdateState() UpdateStateDTO {
	cfg := c.opt.Config()
	feed := cfg.UpdateFeedURL()
	st := UpdateStateDTO{
		CurrentVersion: c.opt.Version,
		Enabled:        cfg.UpdateEnabled() && feed != "",
		AutoInstall:    cfg.UpdateAutoInstall(),
		ShowChangelog:  cfg.UpdateShowChangelog(),
		IntervalHours:  cfg.Update.IntervalHours,
		FeedURL:        feed,
		DevBuild:       c.dev,
		InstalledNotes: []update.NoteLine{},
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st.Status, st.Phase = c.status, c.phase
	st.BytesDone, st.BytesTotal = c.bytesDone, c.bytesTotal
	st.Percent = -1
	if c.bytesTotal > 0 {
		st.Percent = int(c.bytesDone * 100 / c.bytesTotal)
		if st.Percent > 100 {
			st.Percent = 100
		}
	}
	if !c.lastCheck.IsZero() {
		t := c.lastCheck
		st.LastCheck = &t
	}
	st.LastError = c.lastErr
	st.DeferredReason = c.deferredReason
	st.FailedVersion, st.FailMessage = c.failedVersion, c.failMessage
	st.SkippedVersion = c.skipVersion
	st.PromptSeq = c.promptSeq
	if c.pending != nil {
		p := &PendingUpdateDTO{Version: c.pending.Version, Notes: []update.NoteLine{}}
		if st.ShowChangelog {
			p.Notes = update.RenderNotes(c.pending.Notes())
		}
		st.Pending = p
	}
	if c.installed != nil {
		st.InstalledNotes = update.RenderNotes(c.installed.Notes())
	}
	return st
}
