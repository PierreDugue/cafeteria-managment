package store

import (
	"context"
	"fmt"
	"strings"
)

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

// Children returns all children (and family entries) with their parent,
// ordered by normalized name, then parent email.
func (s *Store) Children(ctx context.Context) ([]Child, error) {
	return s.queryChildren(ctx, childrenSelect+childrenOrder)
}

// ChildrenByIDs is like Children but only for the given IDs. Unknown IDs are
// skipped; no IDs means no query and an empty result.
func (s *Store) ChildrenByIDs(ctx context.Context, ids []int64) ([]Child, error) {
	if len(ids) == 0 {
		return []Child{}, nil
	}
	// Only placeholders go into the SQL string, never values.
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return s.queryChildren(ctx, childrenSelect+" WHERE c.id IN ("+placeholders+")"+childrenOrder, args...)
}

func (s *Store) queryChildren(ctx context.Context, query string, args ...any) ([]Child, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
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
