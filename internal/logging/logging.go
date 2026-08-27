// Package logging configures the global zerolog logger with a console writer
// and an optional size-capped file, mirroring the GoShareIt setup.
package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Options controls logger setup.
type Options struct {
	Level     string // trace|debug|info|warn|error
	File      bool   // also write to LogPath
	LogPath   string // file destination when File is true
	MaxSizeMB int    // truncate LogPath if it exceeds this on startup (0 = 5)
	Console   bool   // pretty console writer to stderr (true for CLI/dev)
}

// Setup configures the global logger and returns it. A file that already
// exceeds MaxSizeMB is truncated on startup rather than rotated, which keeps
// the implementation dependency-free while bounding disk use.
func Setup(o Options) (zerolog.Logger, error) {
	level, err := zerolog.ParseLevel(o.Level)
	if err != nil || o.Level == "" {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)

	var writers []io.Writer
	if o.Console {
		writers = append(writers, zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})
	} else {
		writers = append(writers, os.Stderr)
	}

	if o.File && o.LogPath != "" {
		if err := os.MkdirAll(filepath.Dir(o.LogPath), 0o700); err != nil {
			return log.Logger, fmt.Errorf("logging: mkdir: %w", err)
		}
		maxBytes := int64(o.MaxSizeMB) * 1024 * 1024
		if maxBytes <= 0 {
			maxBytes = 5 * 1024 * 1024
		}
		if fi, err := os.Stat(o.LogPath); err == nil && fi.Size() > maxBytes {
			_ = os.Truncate(o.LogPath, 0)
		}
		f, err := os.OpenFile(o.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return log.Logger, fmt.Errorf("logging: open %s: %w", o.LogPath, err)
		}
		writers = append(writers, f)
	}

	logger := zerolog.New(io.MultiWriter(writers...)).With().Timestamp().Logger()
	log.Logger = logger
	return logger, nil
}
