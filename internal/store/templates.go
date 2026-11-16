package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"cafeteria-management/internal/textnorm"
)

// Errors returned by the template methods.
var (
	ErrNotFound = errors.New("not found")
	// ErrNameTaken is shared by templates and lists: the name (compared by
	// textnorm.Key) is already used by another one.
	ErrNameTaken = errors.New("name already used")
)

// Template is a saved message (SPEC §4.3).
type Template struct {
	ID                  int64
	Name, Subject, Body string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

const templateSelect = `SELECT id, name, subject, body, created_at, updated_at FROM templates`

type rowScanner interface{ Scan(dest ...any) error }

func scanTemplate(r rowScanner) (Template, error) {
	var t Template
	var created, updated string
	if err := r.Scan(&t.ID, &t.Name, &t.Subject, &t.Body, &created, &updated); err != nil {
		return Template{}, err
	}
	t.CreatedAt, t.UpdatedAt = parseTime(created), parseTime(updated)
	return t, nil
}

// Templates returns all templates ordered by name_key, then id.
func (s *Store) Templates(ctx context.Context) ([]Template, error) {
	rows, err := s.db.QueryContext(ctx, templateSelect+` ORDER BY name_key, id`)
	if err != nil {
		return nil, fmt.Errorf("query templates: %w", err)
	}
	defer rows.Close()

	out := []Template{}
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, fmt.Errorf("scan template: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query templates: %w", err)
	}
	return out, nil
}

// Template returns one template, or ErrNotFound.
func (s *Store) Template(ctx context.Context, id int64) (Template, error) {
	t, err := scanTemplate(s.db.QueryRowContext(ctx, templateSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Template{}, ErrNotFound
	}
	if err != nil {
		return Template{}, fmt.Errorf("read template: %w", err)
	}
	return t, nil
}

// CreateTemplate stores a new template and returns its ID (ErrNameTaken).
// The name is cleaned here; length and content limits are the caller's.
func (s *Store) CreateTemplate(ctx context.Context, name, subject, body string) (int64, error) {
	name = textnorm.Clean(name)
	key := textnorm.Key(name)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if err := checkNameFree(ctx, tx, templateNameQuery, key, 0); err != nil {
		return 0, err
	}
	now := s.timestamp()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO templates (name, name_key, subject, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		name, key, subject, body, now, now)
	if err != nil {
		return 0, fmt.Errorf("insert template: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// UpdateTemplate replaces a template (ErrNotFound, ErrNameTaken).
func (s *Store) UpdateTemplate(ctx context.Context, id int64, name, subject, body string) error {
	name = textnorm.Clean(name)
	key := textnorm.Key(name)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := checkNameFree(ctx, tx, templateNameQuery, key, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE templates SET name = ?, name_key = ?, subject = ?, body = ?, updated_at = ? WHERE id = ?`,
		name, key, subject, body, s.timestamp(), id)
	if err != nil {
		return fmt.Errorf("update template: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// DeleteTemplate removes a template (ErrNotFound).
func (s *Store) DeleteTemplate(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM templates WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete template: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

const (
	templateNameQuery = `SELECT id FROM templates WHERE name_key = ? AND id <> ?`
	listNameQuery     = `SELECT id FROM lists WHERE name_key = ? AND id <> ?`
)

// checkNameFree runs query (templateNameQuery or listNameQuery) and returns
// ErrNameTaken if another row than exceptID has the name key. The UNIQUE
// index stays as a safety net.
func checkNameFree(ctx context.Context, tx *sql.Tx, query, key string, exceptID int64) error {
	var other int64
	err := tx.QueryRowContext(ctx, query, key, exceptID).Scan(&other)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check name: %w", err)
	}
	return ErrNameTaken
}
