//go:build windows || darwin

package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/rake-pro/dropwarden/internal/watcher"
)

// wailsEmitter forwards engine events to the frontend via the Wails event bus.
// It implements watcher.Emitter.
type wailsEmitter struct {
	app *application.App
}

func newWailsEmitter(app *application.App) *wailsEmitter { return &wailsEmitter{app: app} }

// Emit forwards an engine event to all windows. The frontend subscribes with
// @wailsio/runtime Events.On(name, cb).
func (e *wailsEmitter) Emit(event string, payload any) {
	if e.app == nil {
		return
	}
	e.app.Event.Emit(event, payload)
}

var _ watcher.Emitter = (*wailsEmitter)(nil)
