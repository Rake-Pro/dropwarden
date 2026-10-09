//go:build darwin

package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/rake-pro/dropwarden/cmd/dropwarden/icons"
)

// installAppMenu gives dropwarden the standard macOS menu bar: the
// application menu (About, Hide, Quit with Cmd+Q), Edit (Cmd+C/V/X/A inside
// the webview work only when an Edit menu with those roles exists) and
// Window (Minimise, Zoom, Close, which hides the dashboard like the title-bar
// button does). Without it the "dropwarden" menu next to the Apple menu is
// empty and Cmd+Q does nothing, so the only ways to quit were the tray menu
// and the Dock's context menu.
func installAppMenu(wapp *application.App) {
	menu := application.NewMenu()
	menu.AddRole(application.AppMenu)
	menu.AddRole(application.EditMenu)
	menu.AddRole(application.WindowMenu)
	wapp.Menu.Set(menu)
}

// setAppIcon swaps the Dock icon to the level's variant so the status dot is
// visible there too. The bytes come from the icons package's process-long
// cache: Wails hands the pointer to the main queue and reads it after this
// call returns.
func setAppIcon(wapp *application.App, level icons.Level) {
	wapp.SetIcon(icons.Dock(level))
}
