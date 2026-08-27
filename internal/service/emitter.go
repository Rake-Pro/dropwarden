// Package service holds thin adapters between the watcher engine and the
// outside world: a log-sink emitter for the CLI, and (in the Wails app) the
// event-forwarding emitter and the bound services. It contains no business
// logic.
package service

import (
	"github.com/rs/zerolog"

	"github.com/rake-pro/dropwarden/internal/watcher"
)

// LogEmitter implements watcher.Emitter by writing events to a zerolog logger.
// It is the emitter used by the headless CLI so every engine event is visible
// in the log without a UI.
type LogEmitter struct {
	log zerolog.Logger
}

// NewLogEmitter builds a LogEmitter.
func NewLogEmitter(log zerolog.Logger) *LogEmitter { return &LogEmitter{log: log} }

// Emit logs the event and payload.
func (l *LogEmitter) Emit(event string, payload any) {
	l.log.Info().Str("event", event).Interface("payload", payload).Msg("engine event")
}

// compile-time check.
var _ watcher.Emitter = (*LogEmitter)(nil)
