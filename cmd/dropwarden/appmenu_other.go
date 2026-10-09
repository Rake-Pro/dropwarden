//go:build !darwin

package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/rake-pro/dropwarden/cmd/dropwarden/icons"
)

// installAppMenu is a no-op off macOS: Windows and Linux would render an
// application menu as a menu bar inside the dashboard window, and the tray
// menu already carries every action there.
func installAppMenu(*application.App) {}

// setAppIcon is a no-op off macOS. Windows shows the exe's resource icon in
// the taskbar and ignores App.SetIcon; on Linux (gtk3) App.SetIcon only
// seeds the icon for windows created afterwards and leaks the previous
// pixbuf, so the open dashboard would never change anyway.
func setAppIcon(*application.App, icons.Level) {}
