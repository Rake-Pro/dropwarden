//go:build windows || darwin || linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/rake-pro/dropwarden/internal/config"
	"github.com/rake-pro/dropwarden/internal/service"
	"github.com/rake-pro/dropwarden/internal/update"
	"github.com/rake-pro/dropwarden/internal/version"
)

const (
	// updateStartupDelay lets the tray, Chrome and the network come up before
	// the launch check.
	updateStartupDelay = 30 * time.Second
	// updateDeferRecheck is how often a deferred auto-install re-evaluates
	// whether a watch is still in progress.
	updateDeferRecheck = 5 * time.Minute
	// updateDownloadTimeout bounds one release download.
	updateDownloadTimeout = 15 * time.Minute
)

// updateController owns the self-updater: the launch + periodic background
// checks, the "Check for Updates" tray item, and the /api/update state the
// dashboard banner and the Updates settings pane read. Install hands the
// verified archive to `dropwarden-cli update-apply` (a sibling binary) and
// quits; without that helper it installs in-process and relaunches from
// PostShutdown. It implements service.Updater.
type updateController struct {
	cfg     func() *config.Config // live config: settings edits apply without a restart
	backend *service.Backend
	ctx     context.Context

	// Set once during startup, before any check can run.
	quit     func()
	show     func()
	item     *application.MenuItem
	helper   string // dropwarden-cli path; "" = install in-process
	relaunch string // update.SelfLaunchPath of this host
	hostExe  string

	mu           sync.Mutex
	pending      *update.Release
	lastCheck    time.Time
	lastErr      string
	trayNote     string // manual-check result suffix on the tray item
	checking     bool
	installing   bool
	busyDeferred bool
	deferring    bool   // the deferral loop is running
	postRelaunch string // in-process install: start this after shutdown
	skipVersion  string // auto-install of this version failed last run; manual install only
	skipErr      string // why it failed, kept as lastErr until the skip clears
}

var _ service.Updater = (*updateController)(nil)

func newUpdateController(ctx context.Context, cfg func() *config.Config, backend *service.Backend) *updateController {
	c := &updateController{cfg: cfg, backend: backend, ctx: ctx, quit: func() {}, show: func() {}}
	if exe, err := os.Executable(); err == nil {
		if exe, err = filepath.EvalSymlinks(exe); err == nil {
			c.hostExe = exe
			name := "dropwarden-cli"
			if runtime.GOOS == "windows" {
				name += ".exe"
			}
			helper := filepath.Join(filepath.Dir(exe), name)
			if _, err := os.Stat(helper); err == nil {
				c.helper = helper
			} else {
				log.Debug().Str("helper", helper).Msg("update: no dropwarden-cli helper, installs stay in-process")
			}
		}
	}
	if p, err := update.SelfLaunchPath(); err == nil {
		c.relaunch = p
	}
	return c
}

// loadFailure picks up the marker a failed update-apply left for this start
// (update.FailureFile in the app data dir) and deletes it: the error is
// surfaced through /api/update and automatic installs skip that version, so
// a failing update cannot turn into a restart loop. Called once the
// single-instance lock is held, before start.
func (c *updateController) loadFailure() {
	dir, err := config.Dir()
	if err != nil {
		return
	}
	f := update.TakeFailure(dir)
	if f == nil {
		return
	}
	log.Warn().Str("version", f.Version).Str("error", f.Error).Msg("update: previous install failed; automatic install of this version is skipped")
	c.mu.Lock()
	c.skipVersion, c.skipErr, c.lastErr = f.Version, f.Error, f.Error
	c.mu.Unlock()
}

// attach wires the tray item. Called once while the tray is built.
func (c *updateController) attach(item *application.MenuItem) {
	c.item = item
	item.OnClick(func(*application.Context) { go c.onClick() })
	c.retitle()
}

func (c *updateController) onClick() {
	c.mu.Lock()
	pending, busy := c.pending, c.checking || c.installing
	c.mu.Unlock()
	if busy {
		return
	}
	if pending != nil {
		if err := c.install(pending, true); err != nil {
			log.Info().Err(err).Msg("update: install not started")
		}
		return
	}
	c.check(c.ctx, true)
}

// start runs the launch check (~30s in) and then one check every
// update.interval_hours, reading the live config each time. Dev builds never
// auto-check (a 0.0.0-dev binary would otherwise immediately "upgrade" to the
// last release); the tray item and the settings button still work there.
func (c *updateController) start() {
	if update.IsDev(version.Version) {
		log.Debug().Msg("update: dev build, background checks disabled")
		return
	}
	if c.cfg().UpdateFeedURL() == "" {
		log.Info().Msg("update: update.feed_url is empty; update checks are disabled")
	}
	go func() {
		wait := updateStartupDelay
		for {
			t := time.NewTimer(wait)
			select {
			case <-c.ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
			cfg := c.cfg()
			if cfg.UpdateEnabled() && cfg.UpdateFeedURL() != "" {
				c.check(c.ctx, false)
			}
			wait = time.Duration(cfg.Update.IntervalHours) * time.Hour
			if wait <= 0 {
				wait = 24 * time.Hour
			}
		}
	}()
}

// check queries the feed. Background checks that find an update install it
// when auto_install is on (deferred while a watch is in progress); manual
// checks only record the result and surface it on the tray item and the
// dashboard.
func (c *updateController) check(ctx context.Context, manual bool) {
	c.mu.Lock()
	if c.checking || c.installing {
		c.mu.Unlock()
		return
	}
	c.checking = true
	c.trayNote = ""
	c.mu.Unlock()
	c.retitle()

	rel, err := c.doCheck(ctx)

	c.mu.Lock()
	c.checking = false
	c.lastCheck = time.Now()
	switch {
	case err != nil:
		c.lastErr = err.Error()
		if manual {
			c.trayNote = "last check failed"
		}
	default:
		// A skip only holds while the feed still offers that same version.
		if rel == nil || rel.Version != c.skipVersion {
			c.skipVersion, c.skipErr = "", ""
		}
		c.lastErr = c.skipErr
		c.pending = rel
		if rel == nil && manual {
			c.trayNote = "up to date"
		}
	}
	skip := c.skipVersion
	c.mu.Unlock()
	c.retitle()

	switch {
	case err != nil:
		log.Warn().Err(err).Msg("update check failed")
	case rel == nil:
		log.Debug().Msg("update: up to date")
	default:
		log.Info().Str("version", rel.Version).Msg("update available")
		if manual {
			c.show() // the dashboard banner carries What's new + Install now
		} else if rel.Version == skip {
			log.Warn().Str("version", rel.Version).Msg("update: automatic install of this version failed before; waiting for a manual install")
		} else if c.cfg().UpdateAutoInstall() {
			c.autoInstall(rel)
		}
	}
}

func (c *updateController) doCheck(ctx context.Context) (*update.Release, error) {
	feed := c.cfg().UpdateFeedURL()
	if feed == "" {
		return nil, errors.New("update checks are disabled: update.feed_url is empty")
	}
	upd, err := update.New(update.Config{FeedURL: feed, Current: version.Version})
	if err != nil {
		return nil, err
	}
	return upd.Check(ctx)
}

// autoInstall installs rel now unless a watch is in progress, in which case
// it re-evaluates every updateDeferRecheck and installs as soon as every
// engine is between watches. A manual install always goes straight through.
func (c *updateController) autoInstall(rel *update.Release) {
	if !c.backend.WatchInProgress() {
		if err := c.install(rel, false); err == nil {
			return
		}
	}
	c.mu.Lock()
	c.busyDeferred = true
	start := !c.deferring
	c.deferring = true
	c.mu.Unlock()
	log.Info().Str("version", rel.Version).Msg("update: watch in progress, deferring auto-install")
	if start {
		go c.deferLoop()
	}
}

func (c *updateController) deferLoop() {
	t := time.NewTicker(updateDeferRecheck)
	defer t.Stop()
	stop := func() {
		c.mu.Lock()
		c.deferring, c.busyDeferred = false, false
		c.mu.Unlock()
	}
	for {
		select {
		case <-c.ctx.Done():
			stop()
			return
		case <-t.C:
		}
		c.mu.Lock()
		rel, installing, skip := c.pending, c.installing, c.skipVersion
		c.mu.Unlock()
		if rel == nil || installing || rel.Version == skip || !c.cfg().UpdateAutoInstall() {
			stop()
			return
		}
		if c.backend.WatchInProgress() {
			log.Debug().Str("version", rel.Version).Msg("update: watch still in progress, auto-install stays deferred")
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

// InstallNow implements service.Updater: install the pending update right
// away, in the background, whatever the engines are doing.
func (c *updateController) InstallNow() error {
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
func (c *updateController) install(rel *update.Release, manual bool) error {
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
	if manual {
		c.skipVersion, c.skipErr = "", ""
	}
	c.mu.Unlock()
	c.retitle()
	go c.doInstall(rel)
	return nil
}

// doInstall downloads and verifies rel in this process (so a network failure
// never leaves the app quit), then hands the archive to the helper and quits.
func (c *updateController) doInstall(rel *update.Release) {
	failed := func(msg string, err error) {
		log.Error().Err(err).Str("version", rel.Version).Msg(msg)
		c.mu.Lock()
		c.installing = false
		c.lastErr = err.Error()
		c.mu.Unlock()
		c.retitle()
	}

	// Refuse up front when the install location cannot be written: handing
	// off and quitting would only fail in the updater and restart this build.
	if c.hostExe != "" {
		if err := update.ProbeWritable(c.hostExe); err != nil {
			failed("update install location check failed", fmt.Errorf("install location is not writable: %w", err))
			return
		}
	}

	feed := c.cfg().UpdateFeedURL()
	upd, err := update.New(update.Config{FeedURL: feed, Current: version.Version})
	if err != nil {
		failed("update download failed", err)
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, updateDownloadTimeout)
	archive, err := upd.Download(ctx, rel)
	cancel()
	if err != nil {
		failed("update download failed", err)
		return
	}

	if c.helper != "" && c.relaunch != "" {
		job := update.Job{
			Version:  rel.Version,
			FeedURL:  feed,
			Current:  version.Version,
			HostPID:  os.Getpid(),
			Relaunch: c.relaunch,
			HostExe:  c.hostExe,
			Args:     os.Args[1:],
			Archive:  archive,
		}
		path, err := update.WriteJob(job)
		if err != nil {
			os.Remove(archive)
			failed("update handoff failed", err)
			return
		}
		cmd := exec.Command(c.helper, "update-apply", path)
		cmd.SysProcAttr = detachedProcAttr()
		if err := cmd.Start(); err != nil {
			os.Remove(path)
			os.Remove(archive)
			failed("update handoff: start updater", err)
			return
		}
		_ = cmd.Process.Release()
		log.Info().Str("version", rel.Version).Str("helper", c.helper).Msg("update handed off to updater; quitting")
		c.quit()
		return
	}

	relaunch, err := update.Apply(archive)
	if err != nil {
		os.Remove(archive)
		failed("update apply failed", err)
		return
	}
	c.mu.Lock()
	c.postRelaunch = relaunch
	c.mu.Unlock()
	log.Info().Str("version", rel.Version).Msg("update installed in-process; restarting")
	c.quit()
}

// postShutdown runs as the Wails PostShutdown hook, after the single-instance
// lock is released: an in-process install starts the new binary here, so it
// is not turned away by the still-running old instance.
func (c *updateController) postShutdown() {
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

// CheckNow implements service.Updater (the settings pane's button).
func (c *updateController) CheckNow(ctx context.Context) service.UpdateStateDTO {
	c.check(ctx, true)
	return c.UpdateState()
}

// UpdateState implements service.Updater.
func (c *updateController) UpdateState() service.UpdateStateDTO {
	cfg := c.cfg()
	feed := cfg.UpdateFeedURL()
	st := service.UpdateStateDTO{
		CurrentVersion: version.Version,
		Available:      true,
		Dev:            update.IsDev(version.Version),
		Enabled:        cfg.UpdateEnabled() && feed != "",
		FeedURL:        feed,
		ShowChangelog:  cfg.UpdateShowChangelog(),
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.lastCheck.IsZero() {
		t := c.lastCheck
		st.LastCheck = &t
	}
	st.LastError = c.lastErr
	st.SkippedVersion = c.skipVersion
	st.BusyDeferred = c.busyDeferred
	st.Checking = c.checking
	st.Installing = c.installing
	if c.pending != nil {
		p := &service.PendingUpdateDTO{Version: c.pending.Version}
		if st.ShowChangelog {
			p.Notes = update.RenderNotes(c.pending.Notes())
		}
		st.Pending = p
	}
	return st
}

// retitle mirrors the controller state onto the tray item.
func (c *updateController) retitle() {
	if c.item == nil {
		return
	}
	c.mu.Lock()
	title, enabled := "Check for Updates", true
	switch {
	case c.installing && c.pending != nil:
		title, enabled = "Installing Update v"+c.pending.Version+"...", false
	case c.checking:
		title, enabled = "Checking for Updates...", false
	case c.pending != nil:
		title = "Install Update v" + c.pending.Version
	case c.trayNote != "":
		title += " (" + c.trayNote + ")"
	}
	c.mu.Unlock()
	c.item.SetLabel(title)
	c.item.SetEnabled(enabled)
}
