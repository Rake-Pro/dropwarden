//go:build windows || darwin || linux

package main

import (
	"fmt"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/rake-pro/dropwarden/cmd/dropwarden/icons"
	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/service"
	"github.com/rake-pro/dropwarden/internal/watcher"
)

// trayEngine is one (profile, platform) engine's tray widget set: a disabled
// status line plus its action items. Items stay hidden until that engine
// attaches, so a profile or platform that never comes up adds no clutter.
type trayEngine struct {
	prefix   string // status-line prefix: "Twitch", "Greg - Kick"
	who      string // action wording: "Twitch", "Greg's Kick"
	profile  string
	platform domain.Platform
	status   *application.MenuItem
	pause    *application.MenuItem
	skip     *application.MenuItem
	engine   func() *watcher.Engine
}

func (t *trayEngine) items() []*application.MenuItem {
	return []*application.MenuItem{t.status, t.pause, t.skip}
}

// buildTray creates the system tray, its menu, and a background goroutine that
// keeps the status lines current. Menu actions drive the engines through the
// backend (which may not have engines attached yet while Chrome starts).
func buildTray(wapp *application.App, win *application.WebviewWindow, backend *service.Backend, updates *updateTray) {
	tray := wapp.SystemTray.New()
	// Icon, not a text label: the chest is the app's mark in the menu bar /
	// tray, and its corner dot is the status indicator (see updateTrayStatus).
	tray.SetIcon(icons.Tray(icons.Idle))
	tray.SetTooltip("dropwarden")

	menu := application.NewMenu()

	add := func(prefix, who, profile string, platform domain.Platform, engine func() *watcher.Engine) *trayEngine {
		t := &trayEngine{prefix: prefix, who: who, profile: profile, platform: platform, engine: engine}
		t.status = menu.Add(prefix + ": starting...")
		t.status.SetEnabled(false)
		t.pause = menu.Add("Pause " + who + " watching")
		t.pause.OnClick(func(*application.Context) {
			if e := engine(); e != nil {
				if e.Status().Paused {
					e.Start()
				} else {
					e.Stop()
				}
			}
		})
		t.skip = menu.Add("Skip current " + who + " drop")
		t.skip.OnClick(func(*application.Context) {
			if e := engine(); e != nil {
				e.SkipCurrent()
			}
		})
		return t
	}

	// One item set per configured (profile, platform) pair. With a single
	// profile the labels collapse to the platform-only wording the tray always
	// had.
	profiles := backend.ProfileList()
	multi := len(profiles) > 1
	var engines []*trayEngine
	for i, p := range profiles {
		if i > 0 {
			menu.AddSeparator()
		}
		for _, plat := range []struct {
			platform domain.Platform
			label    string
		}{{domain.PlatformTwitch, "Twitch"}, {domain.PlatformKick, "Kick"}} {
			prefix, who := plat.label, plat.label
			if multi {
				prefix = p.DisplayName + " - " + plat.label
				who = p.DisplayName + "'s " + plat.label
			}
			name, platform := p.Name, plat.platform
			engines = append(engines, add(prefix, who, name, platform, func() *watcher.Engine {
				return backend.EngineFor(name, platform)
			}))
		}
	}
	// Hidden until the matching engine attaches; the tick below tracks presence.
	for _, t := range engines {
		for _, it := range t.items() {
			it.SetHidden(true)
		}
	}

	menu.AddSeparator()
	menu.Add("Refresh campaigns now").OnClick(func(*application.Context) {
		for _, e := range backend.AllEngines() {
			e.RefreshNow()
		}
	})
	menu.AddSeparator()
	menu.Add("Open dashboard").OnClick(func(*application.Context) { win.Show() })
	menu.AddSeparator()
	// Retitles itself: "Update vX.Y.Z Available..." once an update is pending;
	// a click opens the dashboard's update dialog, never a silent install.
	updates.attach(menu.Add("Check for Updates"))
	menu.AddSeparator()
	menu.Add("Quit dropwarden").OnClick(func(*application.Context) { wapp.Quit() })

	tray.SetMenu(menu)
	tray.AttachWindow(win)
	tray.OnClick(func() { win.Show() })

	go updateTrayStatus(wapp, tray, backend, engines)
}

// engineState is what trayLevel folds per engine: the watcher status plus
// the profile's needs-login flag, which lives outside the engine (a logged
// out engine sits paused in login mode, so the status alone reads as amber).
type engineState struct {
	status     watcher.Status
	needsLogin bool
}

// trayLevel folds every engine's state into the one level the icons show:
// attention (an engine is waiting on the user: log in, or act by hand) beats
// watching (any engine mid-watch) beats paused (nothing watching: paused by
// the user, or backing off after errors) beats idle.
func trayLevel(states []engineState) icons.Level {
	level := icons.Idle
	for _, es := range states {
		st := es.status
		switch {
		case es.needsLogin || st.State == watcher.StateNeedsUserAction:
			return icons.Attention
		case st.State.WatchInProgress() && !st.Paused:
			level = icons.Watching
		case (st.Paused || st.State == watcher.StateBackoff) && level == icons.Idle:
			level = icons.Paused
		}
	}
	return level
}

// trayLine renders one engine's status text.
func trayLine(st watcher.Status) string {
	if st.State == watcher.StateWatching && st.ChannelLogin != "" {
		return fmt.Sprintf("watching %s - %d/%d min", st.ChannelLogin, st.Current, st.Required)
	}
	if st.Paused {
		return "paused - choose Resume in the dashboard"
	}
	return st.State.String()
}

// updateTrayStatus refreshes the tray icon, its tooltip and every engine's
// status/pause items. Items track engine presence each tick, so a profile
// that starts late (or never) appears exactly when its engine does. The icon
// (tray and, on macOS, the Dock) only changes when the folded level does, so
// the menu bar is not repainted every tick.
// verify live: confirm menu-item label/hidden updates are reflected on
// Windows/macOS.
func updateTrayStatus(wapp *application.App, tray *application.SystemTray, backend *service.Backend, engines []*trayEngine) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	level := icons.Idle
	for range t.C {
		var tip string
		var states []engineState
		for _, te := range engines {
			e := te.engine()
			for _, it := range te.items() {
				it.SetHidden(e == nil)
			}
			if e == nil {
				continue
			}
			st := e.Status()
			es := engineState{status: st}
			if p := backend.Profile(te.profile); p != nil {
				es.needsLogin = p.NeedsLogin(te.platform)
			}
			states = append(states, es)
			line := trayLine(st)
			te.status.SetLabel(te.prefix + ": " + line)
			if st.Paused {
				te.pause.SetLabel("Resume " + te.who + " watching")
			} else {
				te.pause.SetLabel("Pause " + te.who + " watching")
			}
			if tip != "" {
				tip += " | "
			}
			tip += te.prefix + " " + line
		}
		if tip == "" {
			tip = "starting"
		}
		tray.SetTooltip("dropwarden - " + tip)
		if l := trayLevel(states); l != level {
			level = l
			tray.SetIcon(icons.Tray(level))
			setAppIcon(wapp, level) // macOS Dock only; see appmenu_other.go
		}
	}
}
