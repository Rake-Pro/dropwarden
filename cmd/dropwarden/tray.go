//go:build windows || darwin

package main

import (
	"fmt"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/rake-pro/dropwarden/internal/service"
	"github.com/rake-pro/dropwarden/internal/watcher"
)

// buildTray creates the system tray, its menu, and a background goroutine that
// keeps the status line current. Menu actions drive the engine through the
// backend (which may not have an engine attached yet while Chrome starts).
func buildTray(wapp *application.App, win *application.WebviewWindow, backend *service.Backend) {
	tray := wapp.SystemTray.New()
	tray.SetLabel("dropwarden")
	tray.SetTooltip("dropwarden")

	menu := application.NewMenu()

	status := menu.Add("Starting...")
	status.SetEnabled(false)
	menu.AddSeparator()

	pause := menu.Add("Pause watching")
	pause.OnClick(func(*application.Context) {
		if e := backend.Engine(); e != nil {
			if backend.Engine().Status().State == watcher.StateIdle {
				e.Start()
			} else {
				e.Stop()
			}
		}
	})
	menu.Add("Skip current drop").OnClick(func(*application.Context) {
		if e := backend.Engine(); e != nil {
			e.SkipCurrent()
		}
	})
	menu.Add("Refresh campaigns now").OnClick(func(*application.Context) {
		if e := backend.Engine(); e != nil {
			e.RefreshNow()
		}
	})
	menu.AddSeparator()
	menu.Add("Open dashboard").OnClick(func(*application.Context) { win.Show() })
	menu.AddSeparator()
	menu.Add("Quit dropwarden").OnClick(func(*application.Context) { wapp.Quit() })

	tray.SetMenu(menu)
	tray.AttachWindow(win)
	tray.OnClick(func() { win.Show() })

	go updateTrayStatus(tray, status, pause, backend)
}

// updateTrayStatus refreshes the tray tooltip and the status/pause menu items
// from the engine state. fyne-backed trays accept updates from any goroutine.
// verify live: confirm menu-item label updates are reflected on Windows/macOS.
func updateTrayStatus(tray *application.SystemTray, status, pause *application.MenuItem, backend *service.Backend) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for range t.C {
		e := backend.Engine()
		if e == nil {
			status.SetLabel("Connecting to Chrome...")
			tray.SetTooltip("dropwarden — starting")
			continue
		}
		st := e.Status()
		line := "Idle"
		tip := "dropwarden — idle"
		if st.State == watcher.StateWatching && st.ChannelLogin != "" {
			line = fmt.Sprintf("Watching %s — %d/%d min", st.ChannelLogin, st.Current, st.Required)
			tip = fmt.Sprintf("dropwarden — %s %d/%d", st.ChannelLogin, st.Current, st.Required)
		} else {
			line = "State: " + st.State.String()
			tip = "dropwarden — " + st.State.String()
		}
		status.SetLabel(line)
		tray.SetTooltip(tip)
		if st.State == watcher.StateIdle {
			pause.SetLabel("Resume watching")
		} else {
			pause.SetLabel("Pause watching")
		}
	}
}
