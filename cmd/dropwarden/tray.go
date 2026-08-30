//go:build windows || darwin || linux

package main

import (
	"fmt"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/rake-pro/dropwarden/internal/service"
	"github.com/rake-pro/dropwarden/internal/watcher"
)

// trayPlatform is one platform's tray widget set: a disabled status line plus
// its action items. Kick's set stays hidden until the kick runtime attaches.
type trayPlatform struct {
	label  string // "Twitch", "Kick"
	status *application.MenuItem
	pause  *application.MenuItem
	skip   *application.MenuItem
	engine func() *watcher.Engine
}

// buildTray creates the system tray, its menu, and a background goroutine that
// keeps the status lines current. Menu actions drive the engines through the
// backend (which may not have engines attached yet while Chrome starts).
func buildTray(wapp *application.App, win *application.WebviewWindow, backend *service.Backend) {
	tray := wapp.SystemTray.New()
	tray.SetLabel("dropwarden")
	tray.SetTooltip("dropwarden")

	menu := application.NewMenu()

	addPlatform := func(label string, engine func() *watcher.Engine) *trayPlatform {
		p := &trayPlatform{label: label, engine: engine}
		p.status = menu.Add(label + ": starting...")
		p.status.SetEnabled(false)
		p.pause = menu.Add("Pause " + label + " watching")
		p.pause.OnClick(func(*application.Context) {
			if e := engine(); e != nil {
				if e.Status().Paused {
					e.Start()
				} else {
					e.Stop()
				}
			}
		})
		p.skip = menu.Add("Skip current " + label + " drop")
		p.skip.OnClick(func(*application.Context) {
			if e := engine(); e != nil {
				e.SkipCurrent()
			}
		})
		return p
	}

	twitch := addPlatform("Twitch", backend.Engine)
	menu.AddSeparator()
	kick := addPlatform("Kick", backend.KickEngine)
	// Hidden until the kick runtime attaches; a Twitch-only setup keeps the
	// tray it always had.
	for _, it := range []*application.MenuItem{kick.status, kick.pause, kick.skip} {
		it.SetHidden(true)
	}

	menu.AddSeparator()
	menu.Add("Refresh campaigns now").OnClick(func(*application.Context) {
		for _, e := range []*watcher.Engine{backend.Engine(), backend.KickEngine()} {
			if e != nil {
				e.RefreshNow()
			}
		}
	})
	menu.AddSeparator()
	menu.Add("Open dashboard").OnClick(func(*application.Context) { win.Show() })
	menu.AddSeparator()
	menu.Add("Quit dropwarden").OnClick(func(*application.Context) { wapp.Quit() })

	tray.SetMenu(menu)
	tray.AttachWindow(win)
	tray.OnClick(func() { win.Show() })

	go updateTrayStatus(tray, twitch, kick, backend)
}

// trayLine renders one platform's status text.
func trayLine(st watcher.Status) string {
	if st.State == watcher.StateWatching && st.ChannelLogin != "" {
		return fmt.Sprintf("watching %s — %d/%d min", st.ChannelLogin, st.Current, st.Required)
	}
	if st.Paused {
		return "paused"
	}
	return st.State.String()
}

// updateTrayStatus refreshes the tray tooltip and per-platform status/pause
// items from engine state. fyne-backed trays accept updates from any goroutine.
// verify live: confirm menu-item label/hidden updates are reflected on
// Windows/macOS.
func updateTrayStatus(tray *application.SystemTray, twitch, kick *trayPlatform, backend *service.Backend) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for range t.C {
		var tip string
		if te := backend.Engine(); te == nil {
			twitch.status.SetLabel("Twitch: connecting to Chrome...")
			tip = "dropwarden — starting"
		} else {
			tst := te.Status()
			twitch.status.SetLabel("Twitch: " + trayLine(tst))
			tip = "dropwarden — " + trayLine(tst)
			if tst.Paused {
				twitch.pause.SetLabel("Resume Twitch watching")
			} else {
				twitch.pause.SetLabel("Pause Twitch watching")
			}
		}

		// Kick items track the engine every tick (visible only while it exists),
		// independent of whether the Twitch engine is up.
		ke := backend.KickEngine()
		for _, it := range []*application.MenuItem{kick.status, kick.pause, kick.skip} {
			it.SetHidden(ke == nil)
		}
		if ke != nil {
			kst := ke.Status()
			kick.status.SetLabel("Kick: " + trayLine(kst))
			if kst.Paused {
				kick.pause.SetLabel("Resume Kick watching")
			} else {
				kick.pause.SetLabel("Pause Kick watching")
			}
			tip += " · kick " + trayLine(kst)
		}
		tray.SetTooltip(tip)
	}
}
