package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrStaleSelection is returned when the data changed (an import was saved)
// since the IDs of a selection were read.
var ErrStaleSelection = errors.New("data changed since the selection")

// querier is satisfied by *sql.DB and *sql.Tx.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Child is a child with the parent they are listed under. A child listed
// with two parents is two Child values (SPEC §4.1).
//
// It also holds a family entry of the CSV export (SPEC §4.1.1): Name is then
// the billing name and FamilyNumber is set. ParentID is 0 and Email is ""
// when the entry has no valid payer email; InvalidEmail then holds the value
// that was rejected ("" if it was empty).
type Child struct {
	ID           int64
	Name         string
	FamilyNumber string
	InvalidEmail string
	ParentID     int64
	ParentName   string
	Email        string
}

// CanEmail reports whether an email can be sent for this entry, that is
// whether it has a valid address.
func (c Child) CanEmail() bool { return c.ParentID != 0 }

const childrenSelect = `
SELECT c.id, c.name, c.family_number, c.invalid_email,
       COALESCE(p.id, 0), COALESCE(p.name, ''), COALESCE(p.email, '')
FROM children c LEFT JOIN parents p ON p.id = c.parent_id`

// Family entries sort by the smallest name of their family so the rows of a
// family stay adjacent, then valid emails before the no-email entry.
const childrenOrder = `
ORDER BY CASE WHEN c.family_number = '' THEN c.name_key
              ELSE MIN(c.name_key) OVER (PARTITION BY c.family_number) END,
         c.family_number, c.name_key, p.id IS NULL, p.email, c.id`

// Children returns the children (and family entries) of one list with their
// parent, ordered by normalized name, then parent email.
func (s *Store) Children(ctx context.Context, listID int64) ([]Child, error) {
	return queryChildren(ctx, s.db, childrenSelect+` WHERE c.list_id = ?`+childrenOrder, listID)
}

// ChildrenByIDs is like Children but only for the given IDs. IDs unknown or
// of another list are skipped; no IDs means no query and an empty result.
func (s *Store) ChildrenByIDs(ctx context.Context, listID int64, ids []int64) ([]Child, error) {
	return childrenByIDs(ctx, s.db, listID, ids)
}

// SelectedChildren is like ChildrenByIDs, but fails with ErrStaleSelection
// unless the list still exists at version (see ListVersion). The check and
// the read share one transaction, so an import cannot slip between them.
func (s *Store) SelectedChildren(ctx context.Context, listID, version int64, ids []int64) ([]Child, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	current, err := listVersion(ctx, tx, listID)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrStaleSelection
	}
	if err != nil {
		return nil, err
	}
	if current != version {
		return nil, ErrStaleSelection
	}
	return childrenByIDs(ctx, tx, listID, ids)
}

// idPlaceholders returns "?,?,..." for ids and the ids as query arguments.
// Only placeholders go into the SQL string, never values.
func idPlaceholders(ids []int64) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(ids)), ","), args
}

func childrenByIDs(ctx context.Context, q querier, listID int64, ids []int64) ([]Child, error) {
	if len(ids) == 0 {
		return []Child{}, nil
	}
	placeholders, args := idPlaceholders(ids)
	// The list filter drops IDs of other lists: they can never reach the
	// recipients.
	return queryChildren(ctx, q,
		childrenSelect+" WHERE c.list_id = ? AND c.id IN ("+placeholders+")"+childrenOrder,
		append([]any{listID}, args...)...)
}

func queryChildren(ctx context.Context, q querier, query string, args ...any) ([]Child, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query children: %w", err)
	}
	defer rows.Close()

	out := []Child{}
	for rows.Next() {
		var c Child
		if err := rows.Scan(&c.ID, &c.Name, &c.FamilyNumber, &c.InvalidEmail, &c.ParentID, &c.ParentName, &c.Email); err != nil {
			return nil, fmt.Errorf("scan child: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query children: %w", err)
	}
	return out, nil
}

// SaveSelection replaces the saved selection of one list with ids, keeping
// only the entries of that list that have a valid email, and makes it the
// last list used. The last selection is a convenience: nothing else reads
// it. The other lists' saved selections are untouched (ErrNotFound).
func (s *Store) SaveSelection(ctx context.Context, listID int64, ids []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM last_selection WHERE child_id IN (SELECT id FROM children WHERE list_id = ?)`, listID); err != nil {
		return fmt.Errorf("clear last selection: %w", err)
	}
	if len(ids) > 0 {
		placeholders, args := idPlaceholders(ids)
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO last_selection (child_id)
			 SELECT id FROM children
			 WHERE list_id = ? AND parent_id IS NOT NULL AND id IN (`+placeholders+`)`,
			append([]any{listID}, args...)...); err != nil {
			return fmt.Errorf("save last selection: %w", err)
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE lists SET used_at = ? WHERE id = ?`, s.timestamp(), listID)
	if err != nil {
		return fmt.Errorf("mark list used: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// SavedSelection returns the IDs saved by SaveSelection for one list that
// still exist.
func (s *Store) SavedSelection(ctx context.Context, listID int64) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT s.child_id FROM last_selection s JOIN children c ON c.id = s.child_id WHERE c.list_id = ?`, listID)
	if err != nil {
		return nil, fmt.Errorf("query last selection: %w", err)
	}
	defer rows.Close()

	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan last selection: %w", err)
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query last selection: %w", err)
	}
	return out, nil
}
