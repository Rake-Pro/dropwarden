// Package store persists campaigns, drops, progress, the watch queue, session
// history, and claims in a single SQLite database (pure-Go modernc driver, no
// cgo). It is safe for concurrent use.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Store is a handle to the dropwarden database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path, enables WAL and foreign
// keys, and applies migrations.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("store: empty path")
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("store: mkdir: %w", err)
		}
	}
	// _pragma args are honored by the modernc driver via the DSN.
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	// SQLite tolerates a single writer; keep the pool small and predictable.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// OpenMemory opens an in-memory database, for tests.
func OpenMemory() (*Store, error) {
	db, err := sql.Open("sqlite", "file::memory:?cache=shared&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, fmt.Errorf("store: open memory: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_meta (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("store: meta table: %w", err)
	}
	var current int
	err := s.db.QueryRow(`SELECT version FROM schema_meta LIMIT 1`).Scan(&current)
	if err == sql.ErrNoRows {
		if _, err := s.db.Exec(`INSERT INTO schema_meta(version) VALUES(0)`); err != nil {
			return fmt.Errorf("store: seed meta: %w", err)
		}
		current = 0
	} else if err != nil {
		return fmt.Errorf("store: read version: %w", err)
	}
	for i := current; i < len(migrations); i++ {
		if _, err := s.db.Exec(migrations[i]); err != nil {
			return fmt.Errorf("store: migration %d: %w", i+1, err)
		}
	}
	if _, err := s.db.Exec(`UPDATE schema_meta SET version = ?`, len(migrations)); err != nil {
		return fmt.Errorf("store: bump version: %w", err)
	}
	_ = schemaVersion // referenced for documentation of intent
	return nil
}
