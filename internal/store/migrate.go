package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type migration struct {
	version int
	name    string
	sql     string
}

// migrate applies, in order, the embedded migrations not applied yet.
// Files are named NNN_description.sql; each runs in its own transaction.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	migs, err := loadMigrations()
	if err != nil {
		return err
	}
	for _, m := range migs {
		if m.version <= current {
			continue
		}
		if err := s.apply(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) apply(ctx context.Context, m migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("migration %s: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		m.version, s.timestamp()); err != nil {
		return fmt.Errorf("record migration %s: %w", m.name, err)
	}
	return tx.Commit()
}

func loadMigrations() ([]migration, error) {
	files, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return nil, err
	}

	var migs []migration
	for _, f := range files {
		name := strings.TrimPrefix(f, "migrations/")
		prefix, _, ok := strings.Cut(name, "_")
		version, err := strconv.Atoi(prefix)
		if !ok || err != nil || version <= 0 {
			return nil, fmt.Errorf("bad migration file name %q", name)
		}
		body, err := migrationsFS.ReadFile(f)
		if err != nil {
			return nil, err
		}
		migs = append(migs, migration{version: version, name: name, sql: string(body)})
	}

	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })
	for i := 1; i < len(migs); i++ {
		if migs[i].version == migs[i-1].version {
			return nil, fmt.Errorf("duplicate migration version %d", migs[i].version)
		}
	}
	return migs, nil
}
