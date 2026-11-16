package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"cafeteria-management/internal/textnorm"
)

// List is a named family list (SPEC §4.0) with its current counts.
type List struct {
	ID         int64
	Name       string
	Parents    int       // valid email addresses
	Children   int       // children or family entries
	Families   int       // distinct family numbers (0 for Excel data)
	LastImport time.Time // zero if the list has no import
	UsedAt     time.Time
}

// listSelect reads a list with its counts. The data is small, so correlated
// subqueries are enough.
const listSelect = `
SELECT l.id, l.name, l.used_at,
  (SELECT COUNT(*) FROM parents p WHERE p.list_id = l.id),
  (SELECT COUNT(*) FROM children c WHERE c.list_id = l.id),
  (SELECT COUNT(DISTINCT c.family_number) FROM children c WHERE c.list_id = l.id AND c.family_number <> ''),
  COALESCE((SELECT i.imported_at FROM imports i WHERE i.list_id = l.id ORDER BY i.id DESC LIMIT 1), '')
FROM lists l`

func scanList(r rowScanner) (List, error) {
	var l List
	var used, last string
	if err := r.Scan(&l.ID, &l.Name, &used, &l.Parents, &l.Children, &l.Families, &last); err != nil {
		return List{}, err
	}
	l.UsedAt, l.LastImport = parseTime(used), parseTime(last)
	return l, nil
}

// Lists returns all lists ordered by name_key, then id.
func (s *Store) Lists(ctx context.Context) ([]List, error) {
	rows, err := s.db.QueryContext(ctx, listSelect+` ORDER BY l.name_key, l.id`)
	if err != nil {
		return nil, fmt.Errorf("query lists: %w", err)
	}
	defer rows.Close()

	out := []List{}
	for rows.Next() {
		l, err := scanList(rows)
		if err != nil {
			return nil, fmt.Errorf("scan list: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query lists: %w", err)
	}
	return out, nil
}

// List returns one list, or ErrNotFound.
func (s *Store) List(ctx context.Context, id int64) (List, error) {
	l, err := scanList(s.db.QueryRowContext(ctx, listSelect+` WHERE l.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return List{}, ErrNotFound
	}
	if err != nil {
		return List{}, fmt.Errorf("read list: %w", err)
	}
	return l, nil
}

// DefaultList returns the list used last (largest used_at, then largest
// id), or ErrNotFound if there is no list.
func (s *Store) DefaultList(ctx context.Context) (List, error) {
	l, err := scanList(s.db.QueryRowContext(ctx, listSelect+` ORDER BY l.used_at DESC, l.id DESC LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return List{}, ErrNotFound
	}
	if err != nil {
		return List{}, fmt.Errorf("read default list: %w", err)
	}
	return l, nil
}

// RenameList renames a list (ErrNotFound, ErrNameTaken). The name is
// cleaned here; length and content limits are the caller's. Renaming keeps
// the entries, the selection and the list version.
func (s *Store) RenameList(ctx context.Context, id int64, name string) error {
	name = textnorm.Clean(name)
	if name == "" {
		return errors.New("rename list: empty name")
	}
	key := textnorm.Key(name)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := checkNameFree(ctx, tx, listNameQuery, key, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE lists SET name = ?, name_key = ?, updated_at = ? WHERE id = ?`,
		name, key, s.timestamp(), id)
	if err != nil {
		return fmt.Errorf("rename list: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// DeleteList removes a list with its entries and remembered selection
// (cascade). Its import history is kept, under the last name of the list
// (ErrNotFound).
func (s *Store) DeleteList(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`UPDATE imports SET list_name = (SELECT name FROM lists WHERE id = ?) WHERE list_id = ?`,
		id, id); err != nil {
		return fmt.Errorf("keep list name in history: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM lists WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete list: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// ListVersion returns the version of a list: the ID of its last import (0
// if none). It only grows, and an import into another list or a rename does
// not change it (ErrNotFound).
func (s *Store) ListVersion(ctx context.Context, id int64) (int64, error) {
	return listVersion(ctx, s.db, id)
}

// listVersion checks that the list exists, then reads its last import ID.
func listVersion(ctx context.Context, q querier, id int64) (int64, error) {
	var exists int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM lists WHERE id = ?`, id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("read list: %w", err)
	}
	var v int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM imports WHERE list_id = ?`, id).Scan(&v); err != nil {
		return 0, fmt.Errorf("read list version: %w", err)
	}
	return v, nil
}
