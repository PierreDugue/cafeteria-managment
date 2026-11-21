package store

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"slices"
	"testing"
)

// flagged returns the sorted flagged family numbers of a list.
func flagged(t *testing.T, s *Store, listID int64) []string {
	t.Helper()
	m, err := s.Tolerances(context.Background(), listID)
	if err != nil {
		t.Fatal(err)
	}
	out := slices.Sorted(maps.Keys(m))
	if out == nil {
		out = []string{}
	}
	return out
}

func mustSet(t *testing.T, s *Store, listID int64, fams ...string) {
	t.Helper()
	v, err := s.ListVersion(context.Background(), listID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetTolerances(context.Background(), listID, v, fams); err != nil {
		t.Fatalf("SetTolerances(%v): %v", fams, err)
	}
}

func wantFlags(t *testing.T, s *Store, listID int64, want ...string) {
	t.Helper()
	if got := flagged(t, s, listID); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		t.Fatalf("flags = %v, want %v", got, want)
	}
}

func createdAt(t *testing.T, s *Store, listID int64, fam string) string {
	t.Helper()
	var c string
	if err := s.db.QueryRow(`SELECT created_at FROM tolerances WHERE list_id = ? AND family_number = ?`, listID, fam).Scan(&c); err != nil {
		t.Fatal(err)
	}
	return c
}

// famRows: families 10 (two payer emails), 20 (one), 30 (no email only).
func famRows() []ImportRow {
	return []ImportRow{
		fam("10", "DUPONT", "a@example.fr", ""),
		fam("10", "DUPONT", "b@example.fr", ""),
		fam("20", "MARTIN", "m@example.fr", ""),
		fam("30", "NOMAIL", "", ""),
	}
}

func TestMigration008OnV7Database(t *testing.T) {
	ctx := context.Background()
	path := buildV4(t, true)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TABLE tolerances`, `DELETE FROM schema_migrations WHERE version = 8`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var v int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil || v != 7 {
		t.Fatalf("setup version = %d, %v", v, err)
	}
	db.Close()

	s = openPath(t, path)
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil || v < 8 {
		t.Errorf("version = %d, %v; want at least 8", v, err)
	}
	ls, err := s.Lists(ctx)
	if err != nil || len(ls) != 1 || ls[0].Parents != 2 || ls[0].Children != 5 {
		t.Errorf("lists = %+v, %v", ls, err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM templates`); n != 1 {
		t.Errorf("templates = %d", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM tolerances`); n != 0 {
		t.Errorf("tolerances has %d rows", n)
	}
	if got := flagged(t, s, ls[0].ID); len(got) != 0 {
		t.Errorf("flags = %v", got)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("foreign_key_check reports %d problems", n)
	}
}

func TestTolerancesTableConstraints(t *testing.T) {
	s := openTest(t)
	id := newList(t, s, "A", famRows())
	if _, err := s.db.Exec(`INSERT INTO tolerances VALUES (?, '', 't')`, id); err == nil {
		t.Error("empty family number accepted")
	}
	if _, err := s.db.Exec(`INSERT INTO tolerances VALUES (999, '10', 't')`); err == nil {
		t.Error("flag for an unknown list accepted")
	}
}

func TestSetTolerancesBasics(t *testing.T) {
	s := openTest(t)
	tick(s)
	id := newList(t, s, "A", famRows())

	wantFlags(t, s, id)
	mustSet(t, s, id, "10", "20")
	wantFlags(t, s, id, "10", "20")

	// Replace: 20 kept (keeps created_at), 10 removed, 30 added.
	c20 := createdAt(t, s, id, "20")
	mustSet(t, s, id, "20", "30")
	wantFlags(t, s, id, "20", "30")
	if got := createdAt(t, s, id, "20"); got != c20 {
		t.Errorf("created_at of kept flag = %q, want %q", got, c20)
	}
	if got := createdAt(t, s, id, "30"); got == c20 {
		t.Errorf("new flag has the old timestamp %q", got)
	}

	// Duplicates ignored.
	mustSet(t, s, id, "20", "20", "10", "10")
	wantFlags(t, s, id, "10", "20")
	if n := countRows(t, s, `SELECT COUNT(*) FROM tolerances`); n != 2 {
		t.Errorf("rows = %d", n)
	}

	// Empty set (and nil) clears.
	mustSet(t, s, id)
	wantFlags(t, s, id)
	mustSet(t, s, id, "10")
	mustSet(t, s, id, []string{}...)
	wantFlags(t, s, id)
}

func TestSetTolerancesNoEmailFamily(t *testing.T) {
	s := openTest(t)
	id := newList(t, s, "A", famRows())
	mustSet(t, s, id, "30")
	wantFlags(t, s, id, "30")
}

func TestSetTolerancesDoesNotTouchVersionOrSelection(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	id := newList(t, s, "A", famRows())
	cs := listEntries(t, s, id)
	var sel []int64
	for _, c := range cs {
		if c.CanEmail() {
			sel = append(sel, c.ID)
		}
	}
	v, _ := s.ListVersion(ctx, id)
	if err := s.SaveSelection(ctx, id, sel); err != nil {
		t.Fatal(err)
	}
	before := savedIDs(t, s, id)
	imports := countRows(t, s, `SELECT COUNT(*) FROM imports`)

	mustSet(t, s, id, "10", "20")
	if v2, _ := s.ListVersion(ctx, id); v2 != v {
		t.Errorf("version %d -> %d", v, v2)
	}
	if !slices.Equal(savedIDs(t, s, id), before) {
		t.Error("selection changed")
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM imports`); n != imports {
		t.Errorf("imports %d -> %d", imports, n)
	}
}

func TestSetTolerancesPerList(t *testing.T) {
	s := openTest(t)
	a := newList(t, s, "A", famRows())
	b := newList(t, s, "B", famRows()) // same family numbers

	mustSet(t, s, a, "10")
	wantFlags(t, s, a, "10")
	wantFlags(t, s, b)

	mustSet(t, s, b, "10", "20")
	wantFlags(t, s, a, "10")
	wantFlags(t, s, b, "10", "20")

	mustSet(t, s, a)
	wantFlags(t, s, a)
	wantFlags(t, s, b, "10", "20")
}

func TestSetTolerancesErrors(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a := newList(t, s, "A", famRows())
	b := newList(t, s, "B", []ImportRow{fam("99", "OTHER", "o@example.fr", "")})
	mustSet(t, s, a, "10")
	va, _ := s.ListVersion(ctx, a)

	tests := []struct {
		name     string
		list     int64
		version  int64
		families []string
		want     error
	}{
		{"stale version", a, va - 1, []string{"20"}, ErrStaleSelection},
		{"future version", a, va + 1, []string{"20"}, ErrStaleSelection},
		{"deleted list", 9999, 0, []string{"20"}, ErrStaleSelection},
		{"unknown number", a, va, []string{"20", "77"}, ErrUnknownFamily},
		{"number of another list", a, va, []string{"99"}, ErrUnknownFamily},
		{"empty number", a, va, []string{""}, ErrUnknownFamily},
		{"clearing with stale version", a, va - 1, nil, ErrStaleSelection},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.SetTolerances(ctx, tt.list, tt.version, tt.families)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			wantFlags(t, s, a, "10") // nothing changed
			wantFlags(t, s, b)
		})
	}
}

func TestSetTolerancesStaleAfterImport(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a := newList(t, s, "A", famRows())
	v, _ := s.ListVersion(ctx, a)
	replaceList(t, s, a, famRows())
	if err := s.SetTolerances(ctx, a, v, []string{"10"}); !errors.Is(err, ErrStaleSelection) {
		t.Fatalf("err = %v", err)
	}
	wantFlags(t, s, a)
}

func TestExcelListCannotHaveFlags(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	id := newList(t, s, "X", []ImportRow{{ParentName: "Marie", ChildName: "Léa", Email: "m@example.fr"}})
	v, _ := s.ListVersion(ctx, id)
	for _, f := range []string{"", "1", "Léa"} {
		if err := s.SetTolerances(ctx, id, v, []string{f}); !errors.Is(err, ErrUnknownFamily) {
			t.Errorf("flag %q: err = %v", f, err)
		}
	}
	wantFlags(t, s, id)
}

func TestTolerancesUnknownListEmpty(t *testing.T) {
	s := openTest(t)
	if got := flagged(t, s, 4242); len(got) != 0 {
		t.Errorf("flags = %v", got)
	}
}

func TestDeleteListRemovesFlags(t *testing.T) {
	s := openTest(t)
	a := newList(t, s, "A", famRows())
	b := newList(t, s, "B", famRows())
	mustSet(t, s, a, "10", "20")
	mustSet(t, s, b, "10")
	if err := s.DeleteList(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM tolerances WHERE list_id = ?`, a); n != 0 {
		t.Errorf("%d flags left for the deleted list", n)
	}
	wantFlags(t, s, b, "10")
	if n := countRows(t, s, `SELECT COUNT(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("foreign_key_check reports %d problems", n)
	}
}

// ---- imports ----------------------------------------------------------------

// importBoth runs the dry run and the real import of rows into list id and
// returns both summaries (checking the dry run changed nothing).
func importBoth(t *testing.T, s *Store, id int64, rows []ImportRow) (dry, real ImportSummary) {
	t.Helper()
	ctx := context.Background()
	before := flagged(t, s, id)
	req := ImportRequest{FileName: "f.csv", RowsRead: len(rows), Rows: rows, ListID: id}
	req.DryRun = true
	dry, err := s.ImportFamilies(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := flagged(t, s, id); !slices.Equal(got, before) {
		t.Fatalf("dry run changed flags: %v -> %v", before, got)
	}
	req.DryRun = false
	real, err = s.ImportFamilies(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if dry.TolerancesRemoved != real.TolerancesRemoved {
		t.Errorf("dry run removed = %d, real = %d", dry.TolerancesRemoved, real.TolerancesRemoved)
	}
	if len(dry.DeletedChildren) != len(real.DeletedChildren) {
		t.Fatalf("dry deleted %+v, real %+v", dry.DeletedChildren, real.DeletedChildren)
	}
	for i := range dry.DeletedChildren {
		if dry.DeletedChildren[i] != real.DeletedChildren[i] {
			t.Errorf("deleted[%d]: dry %+v, real %+v", i, dry.DeletedChildren[i], real.DeletedChildren[i])
		}
	}
	return dry, real
}

func TestImportKeepsFlagsOfFamiliesStillInFile(t *testing.T) {
	tests := []struct {
		name string
		rows []ImportRow
	}{
		{"same file", famRows()},
		{"payer email changed", []ImportRow{
			fam("10", "DUPONT", "a@example.fr", ""), fam("10", "DUPONT", "b@example.fr", ""),
			fam("20", "MARTIN", "new@example.fr", ""), fam("30", "NOMAIL", "", ""),
		}},
		{"billing name changed", []ImportRow{
			fam("10", "DURAND", "a@example.fr", ""), fam("10", "DURAND", "b@example.fr", ""),
			fam("20", "MARTIN-LEROY", "m@example.fr", ""), fam("30", "NOMAIL", "", ""),
		}},
		{"two emails, one removed", []ImportRow{
			fam("10", "DUPONT", "a@example.fr", ""),
			fam("20", "MARTIN", "m@example.fr", ""), fam("30", "NOMAIL", "", ""),
		}},
		{"valid entry becomes no-email", []ImportRow{
			fam("10", "DUPONT", "", "oups"),
			fam("20", "MARTIN", "m@example.fr", ""), fam("30", "NOMAIL", "", ""),
		}},
		{"no-email entry gets an email", []ImportRow{
			fam("10", "DUPONT", "a@example.fr", ""), fam("10", "DUPONT", "b@example.fr", ""),
			fam("20", "MARTIN", "m@example.fr", ""), fam("30", "NOMAIL", "n@example.fr", ""),
		}},
		{"order changed", []ImportRow{
			fam("30", "NOMAIL", "", ""), fam("20", "MARTIN", "m@example.fr", ""),
			fam("10", "DUPONT", "b@example.fr", ""), fam("10", "DUPONT", "a@example.fr", ""),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTest(t)
			id := newList(t, s, "A", famRows())
			mustSet(t, s, id, "10", "20", "30")
			c := createdAt(t, s, id, "20")

			dry, real := importBoth(t, s, id, tt.rows)
			for _, sum := range []ImportSummary{dry, real} {
				if sum.TolerancesRemoved != 0 {
					t.Errorf("TolerancesRemoved = %d", sum.TolerancesRemoved)
				}
				for _, d := range sum.DeletedChildren {
					if d.Tolerance {
						t.Errorf("deleted entry %+v marked", d)
					}
				}
			}
			wantFlags(t, s, id, "10", "20", "30")
			if got := createdAt(t, s, id, "20"); got != c {
				t.Errorf("created_at changed: %q -> %q", c, got)
			}
		})
	}
}

func TestImportRemovesFlagsOfGoneFamilies(t *testing.T) {
	s := openTest(t)
	id := newList(t, s, "A", famRows())
	mustSet(t, s, id, "10", "20", "30")

	// 10 (two rows) and 30 are gone; 20 stays.
	dry, real := importBoth(t, s, id, []ImportRow{fam("20", "MARTIN", "m@example.fr", "")})
	if real.TolerancesRemoved != 2 || dry.TolerancesRemoved != 2 {
		t.Fatalf("removed dry %d real %d, want 2", dry.TolerancesRemoved, real.TolerancesRemoved)
	}
	wantFlags(t, s, id, "20")

	if len(real.DeletedChildren) != 3 {
		t.Fatalf("deleted = %+v", real.DeletedChildren)
	}
	for _, d := range real.DeletedChildren {
		if !d.Tolerance {
			t.Errorf("deleted entry %+v not marked", d)
		}
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM tolerances`); n != 1 {
		t.Errorf("rows = %d", n)
	}

	// The family comes back later: no flag.
	replaceList(t, s, id, famRows())
	wantFlags(t, s, id, "20")
}

func TestImportMarksOnlyFlaggedGoneFamilies(t *testing.T) {
	s := openTest(t)
	id := newList(t, s, "A", famRows())
	mustSet(t, s, id, "10")
	_, real := importBoth(t, s, id, []ImportRow{fam("20", "MARTIN", "m@example.fr", "")})
	if real.TolerancesRemoved != 1 {
		t.Fatalf("removed = %d", real.TolerancesRemoved)
	}
	marked := 0
	for _, d := range real.DeletedChildren {
		if d.Tolerance {
			marked++
			if d.FamilyNumber != "10" {
				t.Errorf("marked %+v", d)
			}
		} else if d.FamilyNumber == "10" {
			t.Errorf("row of family 10 not marked: %+v", d)
		}
	}
	if marked != 2 {
		t.Errorf("marked = %d, want 2 (both rows of family 10)", marked)
	}
}

func TestImportFlaggedFamilyWithNoRowGoneCountsOnce(t *testing.T) {
	// Counted per family number, not per deleted row.
	s := openTest(t)
	id := newList(t, s, "A", famRows())
	mustSet(t, s, id, "10")
	dry, _ := importBoth(t, s, id, nil)
	if dry.TolerancesRemoved != 1 {
		t.Errorf("removed = %d, want 1", dry.TolerancesRemoved)
	}
	wantFlags(t, s, id)
}

func TestImportDryRunDoesNotRemoveFlags(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	id := newList(t, s, "A", famRows())
	mustSet(t, s, id, "10", "30")
	sum, err := s.ImportFamilies(ctx, ImportRequest{FileName: "f.csv", RowsRead: 1, Rows: []ImportRow{fam("20", "MARTIN", "m@example.fr", "")}, ListID: id, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if sum.TolerancesRemoved != 2 {
		t.Errorf("removed = %d", sum.TolerancesRemoved)
	}
	wantFlags(t, s, id, "10", "30")
	if len(listEntries(t, s, id)) != 4 {
		t.Error("dry run deleted entries")
	}
}

func TestImportExcelReplacingFamilyListRemovesAllFlags(t *testing.T) {
	s := openTest(t)
	id := newList(t, s, "A", famRows())
	mustSet(t, s, id, "10", "20", "30")
	dry, real := importBoth(t, s, id, []ImportRow{{ParentName: "Marie", ChildName: "Léa", Email: "m@example.fr"}})
	if dry.TolerancesRemoved != 3 || real.TolerancesRemoved != 3 {
		t.Fatalf("removed dry %d real %d, want 3", dry.TolerancesRemoved, real.TolerancesRemoved)
	}
	wantFlags(t, s, id)
}

func TestImportIntoOtherListLeavesFlags(t *testing.T) {
	s := openTest(t)
	a := newList(t, s, "A", famRows())
	b := newList(t, s, "B", famRows())
	mustSet(t, s, a, "10", "20")
	mustSet(t, s, b, "30")

	_, real := importBoth(t, s, b, []ImportRow{fam("20", "MARTIN", "m@example.fr", "")})
	if real.TolerancesRemoved != 1 {
		t.Errorf("removed = %d", real.TolerancesRemoved)
	}
	wantFlags(t, s, a, "10", "20")
	wantFlags(t, s, b)
}

func TestImportNewListHasNoFlags(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	rows := famRows()
	for _, dry := range []bool{true, false} {
		sum, err := s.ImportFamilies(ctx, ImportRequest{FileName: "f.csv", RowsRead: len(rows), Rows: rows, NewListName: "New", DryRun: dry})
		if err != nil {
			t.Fatal(err)
		}
		if sum.TolerancesRemoved != 0 {
			t.Errorf("dry=%v removed = %d", dry, sum.TolerancesRemoved)
		}
		if name := "New"; !dry {
			l, err := s.List(ctx, sum.ListID)
			if err != nil || l.Name != name {
				t.Fatalf("list = %+v, %v", l, err)
			}
			wantFlags(t, s, sum.ListID)
		}
	}
}

func TestImportWithoutFlagsRemovesNothing(t *testing.T) {
	s := openTest(t)
	id := newList(t, s, "A", famRows())
	_, real := importBoth(t, s, id, []ImportRow{fam("20", "MARTIN", "m@example.fr", "")})
	if real.TolerancesRemoved != 0 {
		t.Errorf("removed = %d", real.TolerancesRemoved)
	}
	for _, d := range real.DeletedChildren {
		if d.Tolerance {
			t.Errorf("marked %+v", d)
		}
	}
}
