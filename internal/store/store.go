// Package store is the SQLite persistence layer.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure Go SQLite driver
)

// AppDirName is the folder created in the user's config directory.
const AppDirName = "CafeteriaManagement"

// Store gives access to the app database.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// DefaultPath returns the database path in the user's app data folder:
// %AppData%\CafeteriaManagement\app.db on Windows,
// ~/Library/Application Support/CafeteriaManagement/app.db on macOS.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user config dir: %w", err)
	}
	return filepath.Join(dir, AppDirName, "app.db"), nil
}

// Open opens (or creates) the database at path and applies pending migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	// No "file:" prefix: plain paths work on Windows and with spaces.
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// One user, small data: a single connection avoids SQLite lock errors.
	db.SetMaxOpenConns(1)

	s := &Store{db: db, now: time.Now}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) timestamp() string {
	return s.now().UTC().Format(time.RFC3339)
}

func parseTime(v string) time.Time {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}
	}
	return t
}
