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
	MaxSizeMB int    // rotate LogPath to LogPath.1 once it exceeds this (0 = 5)
	Console   bool   // pretty console writer to stderr (true for CLI/dev)
}

// Setup configures the global logger and returns it. The file is rotated (one
// generation: LogPath -> LogPath.1) at startup when it already exceeds
// MaxSizeMB, and again whenever it crosses that size while running, so the
// log that explains a problem is never simply thrown away.
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
		f, err := openRotating(o.LogPath, maxBytes)
		if err != nil {
			return log.Logger, err
		}
		writers = append(writers, f)
	}

	logger := zerolog.New(io.MultiWriter(writers...)).With().Timestamp().Logger()
	log.Logger = logger
	return logger, nil
}

// rotatingFile is an append-only log file that rotates itself to path.1
// (overwriting the previous generation) once it grows past max bytes. The size
// is tracked in memory from the writes, so the check costs nothing per line.
type rotatingFile struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

// openRotating opens path for appending, rotating it first when it already
// exceeds max.
func openRotating(path string, max int64) (*rotatingFile, error) {
	r := &rotatingFile{path: path, max: max}
	if fi, err := os.Stat(path); err == nil && fi.Size() > max {
		_ = os.Rename(path, path+".1")
	}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("logging: open %s: %w", r.path, err)
	}
	r.f = f
	r.size = 0
	if fi, err := f.Stat(); err == nil {
		r.size = fi.Size()
	}
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size > 0 && r.size+int64(len(p)) > r.max {
		// Close before renaming: Windows cannot rename an open file.
		_ = r.f.Close()
		_ = os.Rename(r.path, r.path+".1")
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}
