package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrUnknownFamily is returned when a family number is not in the list.
var ErrUnknownFamily = errors.New("family number not in the list")

// Tolerances returns the flagged family numbers of a list (SPEC §4.2).
func (s *Store) Tolerances(ctx context.Context, listID int64) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT family_number FROM tolerances WHERE list_id = ?`, listID)
	if err != nil {
		return nil, fmt.Errorf("query tolerances: %w", err)
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, fmt.Errorf("scan tolerance: %w", err)
		}
		out[f] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query tolerances: %w", err)
	}
	return out, nil
}

// SetTolerances makes families the flagged family numbers of the list, in
// one transaction. ErrStaleSelection if the list is gone or its version is
// not version; ErrUnknownFamily if a number is not a family number of the
// list (nothing changed). Duplicates are ignored; kept flags keep
// created_at. It changes neither the list version nor the selection.
func (s *Store) SetTolerances(ctx context.Context, listID, version int64, families []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	current, err := listVersion(ctx, tx, listID)
	if errors.Is(err, ErrNotFound) {
		return ErrStaleSelection
	}
	if err != nil {
		return err
	}
	if current != version {
		return ErrStaleSelection
	}

	// Load the list's family numbers once, so a large post costs one query.
	known, err := listFamilyNumbers(ctx, tx, listID)
	if err != nil {
		return err
	}
	want := make(map[string]bool, len(families))
	for _, f := range families {
		if !known[f] {
			return ErrUnknownFamily
		}
		want[f] = true
	}

	rows, err := tx.QueryContext(ctx, `SELECT family_number FROM tolerances WHERE list_id = ?`, listID)
	if err != nil {
		return fmt.Errorf("read tolerances: %w", err)
	}
	var have []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			rows.Close()
			return fmt.Errorf("scan tolerance: %w", err)
		}
		have = append(have, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read tolerances: %w", err)
	}

	for _, f := range have {
		if want[f] {
			delete(want, f) // kept: already stored, keeps created_at
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM tolerances WHERE list_id = ? AND family_number = ?`, listID, f); err != nil {
			return fmt.Errorf("delete tolerance: %w", err)
		}
	}
	now := s.timestamp()
	for f := range want {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tolerances (list_id, family_number, created_at) VALUES (?, ?, ?)`, listID, f, now); err != nil {
			return fmt.Errorf("insert tolerance: %w", err)
		}
	}
	return tx.Commit()
}

// listFamilyNumbers returns the family numbers of the list's entries.
func listFamilyNumbers(ctx context.Context, tx *sql.Tx, listID int64) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT DISTINCT family_number FROM children WHERE list_id = ? AND family_number <> ''`, listID)
	if err != nil {
		return nil, fmt.Errorf("read family numbers: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, fmt.Errorf("scan family number: %w", err)
		}
		out[f] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read family numbers: %w", err)
	}
	return out, nil
}
