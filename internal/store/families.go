package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"cafeteria-management/internal/textnorm"
)

// ImportRow is one entry to import: a parent/child pair (Excel) or a family
// entry (CSV export, SPEC §4.1.1).
//
// For a family entry ChildName is its billing name (NOMFACT), and ParentName
// too. Email must be lower-case and valid (see package importer); Email == ""
// means the entry has no valid payer email (InvalidEmail then holds the
// rejected value, or "" if it was empty), allowed only when FamilyNumber != "".
type ImportRow struct {
	ParentName   string
	ChildName    string
	Email        string
	FamilyNumber string
	InvalidEmail string
}

// Changes counts what an import does to one kind of record.
type Changes struct {
	Added     int
	Updated   int // existing record whose name changed
	Deleted   int
	Unchanged int
}

// DeletedChild describes a child (or family entry) removed by an import.
// Email == "" for an entry without a valid email.
type DeletedChild struct {
	ChildName    string
	ParentName   string
	Email        string
	FamilyNumber string
}

// ErrStaleImport is returned when the data changed since the preview the
// import was based on (see ImportRequest.Base).
var ErrStaleImport = errors.New("data changed since the preview")

// ImportRequest describes an import.
type ImportRequest struct {
	FileName string
	RowsRead int
	Rows     []ImportRow
	// DryRun computes the changes and saves nothing.
	DryRun bool
	// Base, if set, must equal the Base of the preview's summary: the import
	// fails with ErrStaleImport if another import happened in between, so it
	// does exactly what the preview showed.
	Base *int64
}

// ImportSummary is the result of ImportFamilies.
type ImportSummary struct {
	ImportID        int64 // 0 for a dry run
	Base            int64 // last import ID before this one (0 if none)
	Parents         Changes
	Children        Changes
	DeletedChildren []DeletedChild // sorted by child name
}

// ImportFamilies replaces all parents and children with rows (SPEC §4.1):
// matching records are updated, new ones added, the others deleted.
// Parents match on email, children on parent + normalized name, family
// entries on parent + family number (family number alone without a valid
// email).
//
// Everything runs in one transaction. A dry run computes the changes the
// same way and then rolls back, so a preview matches the real import.
// The import history is only written by a real import.
func (s *Store) ImportFamilies(ctx context.Context, req ImportRequest) (ImportSummary, error) {
	var sum ImportSummary

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return sum, err
	}
	defer tx.Rollback()

	// Data only changes through imports, so the last import ID identifies
	// the state the changes are computed from.
	if sum.Base, err = lastImportID(ctx, tx); err != nil {
		return sum, err
	}
	if req.Base != nil && *req.Base != sum.Base {
		return sum, ErrStaleImport
	}

	parents, err := loadParents(ctx, tx)
	if err != nil {
		return sum, err
	}
	children, err := loadChildren(ctx, tx)
	if err != nil {
		return sum, err
	}

	now := s.timestamp()
	keptParents := map[int64]bool{}
	keptChildren := map[int64]bool{}

	for _, r := range req.Rows {
		if r.Email == "" && r.FamilyNumber == "" {
			return sum, errors.New("import row without email must have a family number")
		}

		// An entry without a valid email has no parent (parentID 0).
		var parentID int64
		if r.Email != "" {
			p, ok := parents[r.Email]
			switch {
			case !ok:
				res, err := tx.ExecContext(ctx,
					`INSERT INTO parents (name, email, created_at, updated_at) VALUES (?, ?, ?, ?)`,
					r.ParentName, r.Email, now, now)
				if err != nil {
					return sum, fmt.Errorf("insert parent: %w", err)
				}
				id, err := res.LastInsertId()
				if err != nil {
					return sum, err
				}
				p = &parentRec{id: id, name: r.ParentName, email: r.Email}
				parents[r.Email] = p
				sum.Parents.Added++
				keptParents[id] = true
			case !keptParents[p.id]:
				// First row for an existing parent: its name comes from this row.
				if p.name != r.ParentName {
					if _, err := tx.ExecContext(ctx,
						`UPDATE parents SET name = ?, updated_at = ? WHERE id = ?`,
						r.ParentName, now, p.id); err != nil {
						return sum, fmt.Errorf("update parent: %w", err)
					}
					p.name = r.ParentName
					sum.Parents.Updated++
				} else {
					sum.Parents.Unchanged++
				}
				keptParents[p.id] = true
			}
			parentID = p.id
		}

		key := childKey{parentID: parentID, family: r.FamilyNumber}
		if r.FamilyNumber == "" {
			key.nameKey = textnorm.Key(r.ChildName)
		}
		nameKey := textnorm.Key(r.ChildName)
		c, ok := children[key]
		switch {
		case !ok:
			var dbParent sql.NullInt64
			if parentID != 0 {
				dbParent = sql.NullInt64{Int64: parentID, Valid: true}
			}
			res, err := tx.ExecContext(ctx,
				`INSERT INTO children (parent_id, family_number, name, name_key, invalid_email, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				dbParent, r.FamilyNumber, r.ChildName, nameKey, r.InvalidEmail, now, now)
			if err != nil {
				return sum, fmt.Errorf("insert child: %w", err)
			}
			id, err := res.LastInsertId()
			if err != nil {
				return sum, err
			}
			children[key] = &childRec{id: id, name: r.ChildName, invalidEmail: r.InvalidEmail}
			sum.Children.Added++
			keptChildren[id] = true
		case !keptChildren[c.id]:
			if c.name != r.ChildName || c.invalidEmail != r.InvalidEmail {
				if _, err := tx.ExecContext(ctx,
					`UPDATE children SET name = ?, name_key = ?, invalid_email = ?, updated_at = ? WHERE id = ?`,
					r.ChildName, nameKey, r.InvalidEmail, now, c.id); err != nil {
					return sum, fmt.Errorf("update child: %w", err)
				}
				sum.Children.Updated++
			} else {
				sum.Children.Unchanged++
			}
			keptChildren[c.id] = true
		}
	}

	// Delete what the file no longer contains: children first, so each
	// deletion is counted and listed, then parents.
	byID := map[int64]*parentRec{}
	for _, p := range parents {
		byID[p.id] = p
	}
	for key, c := range children {
		if keptChildren[c.id] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM children WHERE id = ?`, c.id); err != nil {
			return sum, fmt.Errorf("delete child: %w", err)
		}
		d := DeletedChild{ChildName: c.name, FamilyNumber: key.family}
		if p := byID[key.parentID]; p != nil { // nil: entry without a valid email
			d.ParentName, d.Email = p.name, p.email
		}
		sum.Children.Deleted++
		sum.DeletedChildren = append(sum.DeletedChildren, d)
	}
	for _, p := range parents {
		if keptParents[p.id] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM parents WHERE id = ?`, p.id); err != nil {
			return sum, fmt.Errorf("delete parent: %w", err)
		}
		sum.Parents.Deleted++
	}

	sort.Slice(sum.DeletedChildren, func(i, j int) bool {
		a, b := sum.DeletedChildren[i], sum.DeletedChildren[j]
		if ka, kb := textnorm.Key(a.ChildName), textnorm.Key(b.ChildName); ka != kb {
			return ka < kb
		}
		if a.Email != b.Email {
			return a.Email < b.Email
		}
		return a.FamilyNumber < b.FamilyNumber
	})

	if req.DryRun {
		return sum, nil // deferred Rollback discards everything
	}

	res, err := tx.ExecContext(ctx, `INSERT INTO imports (file_name, imported_at, rows_read,
		parents_added, parents_updated, parents_deleted,
		children_added, children_updated, children_deleted)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.FileName, now, req.RowsRead,
		sum.Parents.Added, sum.Parents.Updated, sum.Parents.Deleted,
		sum.Children.Added, sum.Children.Updated, sum.Children.Deleted)
	if err != nil {
		return sum, fmt.Errorf("record import: %w", err)
	}
	if sum.ImportID, err = res.LastInsertId(); err != nil {
		return sum, err
	}

	if err := tx.Commit(); err != nil {
		return sum, fmt.Errorf("commit import: %w", err)
	}
	return sum, nil
}

type parentRec struct {
	id    int64
	name  string
	email string
}

// childKey identifies a child across imports: (parent, name key) for an
// Excel child, (parent or 0, family number) for a family entry.
type childKey struct {
	parentID int64
	family   string
	nameKey  string
}

type childRec struct {
	id           int64
	name         string
	invalidEmail string
}

func loadParents(ctx context.Context, tx *sql.Tx) (map[string]*parentRec, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, name, email FROM parents`)
	if err != nil {
		return nil, fmt.Errorf("load parents: %w", err)
	}
	defer rows.Close()

	out := map[string]*parentRec{}
	for rows.Next() {
		p := &parentRec{}
		if err := rows.Scan(&p.id, &p.name, &p.email); err != nil {
			return nil, err
		}
		out[strings.ToLower(p.email)] = p
	}
	return out, rows.Err()
}

func loadChildren(ctx context.Context, tx *sql.Tx) (map[childKey]*childRec, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, parent_id, family_number, name, name_key, invalid_email FROM children`)
	if err != nil {
		return nil, fmt.Errorf("load children: %w", err)
	}
	defer rows.Close()

	out := map[childKey]*childRec{}
	for rows.Next() {
		c := &childRec{}
		var k childKey
		var parentID sql.NullInt64 // NULL: entry without a valid email
		var nameKey string
		if err := rows.Scan(&c.id, &parentID, &k.family, &c.name, &nameKey, &c.invalidEmail); err != nil {
			return nil, err
		}
		k.parentID = parentID.Int64
		if k.family == "" {
			k.nameKey = nameKey
		}
		out[k] = c
	}
	return out, rows.Err()
}

// Totals is the number of records currently stored.
type Totals struct {
	Parents  int
	Children int
	Families int // distinct family numbers (0 for Excel data)
}

// Totals counts parents and children.
func (s *Store) Totals(ctx context.Context) (Totals, error) {
	var t Totals
	err := s.db.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(*) FROM parents), (SELECT COUNT(*) FROM children),
		(SELECT COUNT(DISTINCT family_number) FROM children WHERE family_number <> '')`).
		Scan(&t.Parents, &t.Children, &t.Families)
	if err != nil {
		return t, fmt.Errorf("count families: %w", err)
	}
	return t, nil
}

// ImportRecord is one entry of the import history.
type ImportRecord struct {
	ID         int64
	FileName   string
	ImportedAt time.Time
	RowsRead   int
	Parents    Changes // Unchanged is not stored
	Children   Changes
}

// Imports returns the most recent imports first, at most limit.
func (s *Store) Imports(ctx context.Context, limit int) ([]ImportRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, file_name, imported_at, rows_read,
		parents_added, parents_updated, parents_deleted,
		children_added, children_updated, children_deleted
		FROM imports ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list imports: %w", err)
	}
	defer rows.Close()

	var out []ImportRecord
	for rows.Next() {
		var r ImportRecord
		var at string
		if err := rows.Scan(&r.ID, &r.FileName, &at, &r.RowsRead,
			&r.Parents.Added, &r.Parents.Updated, &r.Parents.Deleted,
			&r.Children.Added, &r.Children.Updated, &r.Children.Deleted); err != nil {
			return nil, err
		}
		r.ImportedAt = parseTime(at)
		out = append(out, r)
	}
	return out, rows.Err()
}
