// Package logging configures the global zerolog logger with a console writer
// and an optional size-capped file, mirroring the GoShareIt setup.
package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// ringBuffer keeps the most recent raw (JSON) log lines in memory so the debug
// UI can show a live tail without reading the log file.
type ringBuffer struct {
	mu   sync.Mutex
	buf  []string
	size int
}

var recent = &ringBuffer{size: 400}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line == "" {
			continue
		}
		r.buf = append(r.buf, line)
		if len(r.buf) > r.size {
			r.buf = r.buf[len(r.buf)-r.size:]
		}
	}
	return len(p), nil
}

// RecentLines returns up to the last n captured log lines (newest last). A
// non-positive n returns all retained lines.
func RecentLines(n int) []string {
	recent.mu.Lock()
	defer recent.mu.Unlock()
	if n <= 0 || n > len(recent.buf) {
		n = len(recent.buf)
	}
	out := make([]string, n)
	copy(out, recent.buf[len(recent.buf)-n:])
	return out
}

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
	// Always capture a rolling tail in memory for the debug UI.
	writers = append(writers, recent)

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
