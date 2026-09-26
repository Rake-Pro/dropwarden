//go:build windows || darwin || linux

package main

import (
	"fmt"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/rake-pro/dropwarden/internal/domain"
	"github.com/rake-pro/dropwarden/internal/service"
	"github.com/rake-pro/dropwarden/internal/watcher"
)

// trayEngine is one (profile, platform) engine's tray widget set: a disabled
// status line plus its action items. Items stay hidden until that engine
// attaches, so a profile or platform that never comes up adds no clutter.
type trayEngine struct {
	prefix string // status-line prefix: "Twitch", "Greg - Kick"
	who    string // action wording: "Twitch", "Greg's Kick"
	status *application.MenuItem
	pause  *application.MenuItem
	skip   *application.MenuItem
	engine func() *watcher.Engine
}

func (t *trayEngine) items() []*application.MenuItem {
	return []*application.MenuItem{t.status, t.pause, t.skip}
}

// buildTray creates the system tray, its menu, and a background goroutine that
// keeps the status lines current. Menu actions drive the engines through the
// backend (which may not have engines attached yet while Chrome starts).
func buildTray(wapp *application.App, win *application.WebviewWindow, backend *service.Backend, updates *updateController) {
	tray := wapp.SystemTray.New()
	tray.SetLabel("dropwarden")
	tray.SetTooltip("dropwarden")

	menu := application.NewMenu()

	add := func(prefix, who string, engine func() *watcher.Engine) *trayEngine {
		t := &trayEngine{prefix: prefix, who: who, engine: engine}
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
			engines = append(engines, add(prefix, who, func() *watcher.Engine {
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
	// Retitles itself: "Install Update vX.Y.Z" once an update is pending.
	updates.attach(menu.Add("Check for Updates"))
	menu.AddSeparator()
	menu.Add("Quit dropwarden").OnClick(func(*application.Context) { wapp.Quit() })

	tray.SetMenu(menu)
	tray.AttachWindow(win)
	tray.OnClick(func() { win.Show() })

	go updateTrayStatus(tray, engines)
}

// trayLine renders one engine's status text.
func trayLine(st watcher.Status) string {
	if st.State == watcher.StateWatching && st.ChannelLogin != "" {
		return fmt.Sprintf("watching %s - %d/%d min", st.ChannelLogin, st.Current, st.Required)
	}
	if st.Paused {
		return "paused"
	}
	return st.State.String()
}

// updateTrayStatus refreshes the tray tooltip and every engine's status/pause
// items. Items track engine presence each tick, so a profile that starts late
// (or never) appears exactly when its engine does. fyne-backed trays accept
// updates from any goroutine.
// verify live: confirm menu-item label/hidden updates are reflected on
// Windows/macOS.
func updateTrayStatus(tray *application.SystemTray, engines []*trayEngine) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for range t.C {
		var tip string
		for _, te := range engines {
			e := te.engine()
			for _, it := range te.items() {
				it.SetHidden(e == nil)
			}
			if e == nil {
				continue
			}
			st := e.Status()
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
	}
}
