package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"cafeteria-management/internal/textnorm"
)

// tick makes the store clock advance one minute per call, so that
// timestamps are deterministic and strictly increasing.
func tick(s *Store) {
	base := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	n := 0
	s.now = func() time.Time {
		n++
		return base.Add(time.Duration(n) * time.Minute)
	}
}

// newList creates a list through an import and returns its ID.
func newList(t *testing.T, s *Store, name string, rows []ImportRow) int64 {
	t.Helper()
	sum, err := s.ImportFamilies(context.Background(), ImportRequest{FileName: name + ".xlsx", RowsRead: len(rows), Rows: rows, NewListName: name})
	if err != nil {
		t.Fatalf("create list %q: %v", name, err)
	}
	return sum.ListID
}

// replaceList imports rows into an existing list.
func replaceList(t *testing.T, s *Store, id int64, rows []ImportRow) ImportSummary {
	t.Helper()
	sum, err := s.ImportFamilies(context.Background(), ImportRequest{FileName: "r.xlsx", RowsRead: len(rows), Rows: rows, ListID: id})
	if err != nil {
		t.Fatalf("replace list %d: %v", id, err)
	}
	return sum
}

func listEntries(t *testing.T, s *Store, id int64) []Child {
	t.Helper()
	cs, err := s.Children(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func countRows(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

var (
	rowsMarie = []ImportRow{{ParentName: "Marie Dupont", ChildName: "Léa Dupont", Email: "marie@example.fr"}}
	rowsPaul  = []ImportRow{{ParentName: "Paul Martin", ChildName: "Zoé Martin", Email: "paul@example.fr"}}
)

// ---- migration 005 -------------------------------------------------------

// buildV4 creates a database at schema version 4 (migrations 001 to 004 run
// by hand), with data when withData is set.
func buildV4(t *testing.T, withData bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v4.db")
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := []string{`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`}
	for i, f := range []string{"001_families.sql", "002_family_entries.sql", "003_templates.sql", "004_last_selection.sql"} {
		body, err := migrationsFS.ReadFile("migrations/" + f)
		if err != nil {
			t.Fatal(err)
		}
		stmts = append(stmts, string(body), `INSERT INTO schema_migrations VALUES (`+string(rune('1'+i))+`, '2026-01-01T00:00:00Z')`)
	}
	if withData {
		stmts = append(stmts,
			`INSERT INTO parents VALUES (5, 'Marie Dupont', 'marie@example.fr', 'c5', 'u5')`,
			`INSERT INTO parents VALUES (9, 'Paul Martin', 'paul@example.fr', 'c9', 'u9')`,
			`INSERT INTO children (id, parent_id, family_number, name, name_key, invalid_email, created_at, updated_at) VALUES (7, 5, '', 'Léa', 'lea', '', 'c7', 'u7')`,
			`INSERT INTO children (id, parent_id, family_number, name, name_key, invalid_email, created_at, updated_at) VALUES (8, 5, '', 'Tom', 'tom', '', 'c8', 'u8')`,
			`INSERT INTO children (id, parent_id, family_number, name, name_key, invalid_email, created_at, updated_at) VALUES (12, 9, '', 'Zoé', 'zoe', '', 'c12', 'u12')`,
			`INSERT INTO children (id, parent_id, family_number, name, name_key, invalid_email, created_at, updated_at) VALUES (20, 9, '10', 'DUPONT', 'dupont', '', 'c20', 'u20')`,
			`INSERT INTO children (id, parent_id, family_number, name, name_key, invalid_email, created_at, updated_at) VALUES (21, NULL, '20', 'NOMAIL', 'nomail', 'bad@', 'c21', 'u21')`,
			`INSERT INTO last_selection VALUES (7)`,
			`INSERT INTO last_selection VALUES (12)`,
			`INSERT INTO imports VALUES (1, 'a.xlsx', '2026-02-01T10:00:00Z', 3, 2, 0, 0, 3, 0, 0)`,
			`INSERT INTO imports VALUES (2, 'b.xlsx', '2026-03-01T10:00:00Z', 5, 0, 0, 0, 2, 0, 0)`,
			`INSERT INTO templates (name, name_key, subject, body, created_at, updated_at) VALUES ('Menu', 'menu', 'Objet', 'Corps', 't', 't')`,
		)
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return path
}

func openPath(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigration005KeepsExistingData(t *testing.T) {
	ctx := context.Background()
	s := openPath(t, buildV4(t, true))

	ls, err := s.Lists(ctx)
	if err != nil || len(ls) != 1 {
		t.Fatalf("lists = %+v, %v", ls, err)
	}
	l := ls[0]
	if l.Name != "Liste importée" || l.Parents != 2 || l.Children != 5 || l.Families != 2 {
		t.Errorf("list = %+v", l)
	}
	if l.UsedAt.IsZero() {
		t.Error("used_at not set")
	}
	if want := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC); !l.LastImport.Equal(want) {
		t.Errorf("last import = %v, want %v", l.LastImport, want)
	}
	var key string
	if err := s.db.QueryRow(`SELECT name_key FROM lists WHERE id = ?`, l.ID).Scan(&key); err != nil || key != textnorm.Key("Liste importée") {
		t.Errorf("name_key = %q, %v; want %q", key, err, textnorm.Key("Liste importée"))
	}

	// IDs and values kept.
	type row struct{ name, email, fam, invalid string }
	want := map[int64]row{
		7: {"Léa", "marie@example.fr", "", ""}, 8: {"Tom", "marie@example.fr", "", ""},
		12: {"Zoé", "paul@example.fr", "", ""}, 20: {"DUPONT", "paul@example.fr", "10", ""},
		21: {"NOMAIL", "", "20", "bad@"},
	}
	cs := listEntries(t, s, l.ID)
	if len(cs) != len(want) {
		t.Fatalf("children = %+v", cs)
	}
	for _, c := range cs {
		w, ok := want[c.ID]
		if !ok || c.Name != w.name || c.Email != w.email || c.FamilyNumber != w.fam || c.InvalidEmail != w.invalid {
			t.Errorf("child = %+v, want %+v", c, w)
		}
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM parents WHERE list_id = ? AND id IN (5, 9) AND created_at IN ('c5','c9') AND updated_at IN ('u5','u9')`, l.ID); n != 2 {
		t.Errorf("parents kept with their IDs and dates = %d, want 2", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM children WHERE list_id = ? AND created_at = 'c'||id AND updated_at = 'u'||id`, l.ID); n != 5 {
		t.Errorf("children dates kept = %d, want 5", n)
	}

	// Selection pre-checked before is still pre-checked.
	if got := savedIDs(t, s, l.ID); !slices.Equal(got, []int64{7, 12}) {
		t.Errorf("saved = %v, want [7 12]", got)
	}

	// Imports linked to the list.
	imps, err := s.Imports(ctx, 10)
	if err != nil || len(imps) != 2 {
		t.Fatalf("imports = %+v, %v", imps, err)
	}
	for _, i := range imps {
		if i.ListID != l.ID || i.ListName != "Liste importée" || i.ListDeleted {
			t.Errorf("import = %+v", i)
		}
	}
	if v, err := s.ListVersion(ctx, l.ID); err != nil || v != 2 {
		t.Errorf("version = %d, %v; want 2", v, err)
	}

	// Templates untouched.
	if ts, err := s.Templates(ctx); err != nil || len(ts) != 1 || ts[0].Name != "Menu" || ts[0].Subject != "Objet" || ts[0].Body != "Corps" {
		t.Errorf("templates = %+v, %v", ts, err)
	}

	// Integrity.
	if n := countRows(t, s, `SELECT COUNT(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("foreign_key_check reports %d problems", n)
	}
	var ic string
	if err := s.db.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
		t.Errorf("integrity_check = %q, %v", ic, err)
	}
	var v int
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil || v != 5 {
		t.Errorf("schema version = %d, %v", v, err)
	}

	// Foreign keys point at the renamed tables, not at *_new.
	for table, refs := range map[string]string{"children": `REFERENCES "parents"`, "last_selection": `REFERENCES "children"`} {
		var def string
		if err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = ?`, table).Scan(&def); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(def, "_new") || !strings.Contains(def, refs) {
			t.Errorf("%s schema = %s", table, def)
		}
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%\_new' ESCAPE '\'`); n != 0 {
		t.Errorf("leftover *_new tables: %d", n)
	}

	// A re-import into the list keeps the IDs of matching entries.
	sum := replaceList(t, s, l.ID, []ImportRow{{ParentName: "Marie Dupont", ChildName: "Léa", Email: "marie@example.fr"}})
	if sum.Children.Unchanged != 1 || sum.Children.Deleted != 4 {
		t.Errorf("re-import = %+v", sum.Children)
	}
	if cs := listEntries(t, s, l.ID); len(cs) != 1 || cs[0].ID != 7 {
		t.Errorf("children after re-import = %+v", cs)
	}
}

func TestMigration005EmptyDatabase(t *testing.T) {
	ctx := context.Background()
	s := openPath(t, buildV4(t, false))
	if ls, err := s.Lists(ctx); err != nil || len(ls) != 0 {
		t.Fatalf("lists = %+v, %v", ls, err)
	}
	if _, err := s.DefaultList(ctx); !errors.Is(err, ErrNotFound) {
		t.Errorf("DefaultList err = %v", err)
	}
	// The new tables work.
	id := newList(t, s, "Cantine", rowsMarie)
	if cs := listEntries(t, s, id); len(cs) != 1 {
		t.Errorf("children = %+v", cs)
	}
}

func TestMigration005OnlyImportsGivesAList(t *testing.T) {
	path := buildV4(t, false)
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO imports VALUES (1, 'a.xlsx', '2026-02-01T10:00:00Z', 0, 0, 0, 0, 0, 0, 0)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s := openPath(t, path)
	ls, err := s.Lists(context.Background())
	if err != nil || len(ls) != 1 || ls[0].Name != "Liste importée" || ls[0].Children != 0 {
		t.Fatalf("lists = %+v, %v", ls, err)
	}
	imps, _ := s.Imports(context.Background(), 5)
	if len(imps) != 1 || imps[0].ListID != ls[0].ID {
		t.Errorf("imports = %+v", imps)
	}
}

func TestFreshDatabaseHasNoList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	for range 2 { // Open twice is idempotent
		s, err := Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if ls, err := s.Lists(context.Background()); err != nil || len(ls) != 0 {
			t.Fatalf("lists = %+v, %v", ls, err)
		}
		if n := countRows(t, s, `SELECT COUNT(*) FROM schema_migrations WHERE version = 5`); n != 1 {
			t.Errorf("migration 5 rows = %d", n)
		}
		s.Close()
	}
}

func TestMigration005Cascades(t *testing.T) {
	ctx := context.Background()

	t.Run("deleting a parent deletes its children and their selection", func(t *testing.T) {
		s := openPath(t, buildV4(t, true))
		if _, err := s.db.Exec(`DELETE FROM parents WHERE id = 5`); err != nil {
			t.Fatal(err)
		}
		if n := countRows(t, s, `SELECT COUNT(*) FROM children WHERE id IN (7, 8)`); n != 0 {
			t.Errorf("children of the parent left: %d", n)
		}
		if n := countRows(t, s, `SELECT COUNT(*) FROM children`); n != 3 {
			t.Errorf("children = %d, want 3", n)
		}
		if got := savedIDs(t, s, mainList(t, s)); !slices.Equal(got, []int64{12}) {
			t.Errorf("saved = %v, want [12]", got)
		}
	})

	t.Run("deleting a child deletes its selection row", func(t *testing.T) {
		s := openPath(t, buildV4(t, true))
		if _, err := s.db.Exec(`DELETE FROM children WHERE id = 7`); err != nil {
			t.Fatal(err)
		}
		if got := savedIDs(t, s, mainList(t, s)); !slices.Equal(got, []int64{12}) {
			t.Errorf("saved = %v", got)
		}
	})

	t.Run("deleting the list deletes its entries, keeps the imports", func(t *testing.T) {
		s := openPath(t, buildV4(t, true))
		if err := s.DeleteList(ctx, mainList(t, s)); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"parents", "children", "last_selection"} {
			if n := countRows(t, s, `SELECT COUNT(*) FROM `+table); n != 0 {
				t.Errorf("%s rows left: %d", table, n)
			}
		}
		imps, err := s.Imports(ctx, 10)
		if err != nil || len(imps) != 2 {
			t.Fatalf("imports = %+v, %v", imps, err)
		}
		for _, i := range imps {
			if i.ListID != 0 || !i.ListDeleted || i.ListName != "Liste importée" {
				t.Errorf("import = %+v", i)
			}
		}
		if n := countRows(t, s, `SELECT COUNT(*) FROM imports WHERE list_id IS NULL`); n != 2 {
			t.Errorf("imports with NULL list_id = %d", n)
		}
		if ts, _ := s.Templates(ctx); len(ts) != 1 {
			t.Errorf("templates = %+v", ts)
		}
	})

	t.Run("deleting a list leaves the other lists alone", func(t *testing.T) {
		s := openTest(t)
		a := newList(t, s, "A", rowsMarie)
		b := newList(t, s, "B", rowsPaul)
		bEntry := listEntries(t, s, b)[0].ID
		if err := s.SaveSelection(ctx, b, []int64{bEntry}); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteList(ctx, a); err != nil {
			t.Fatal(err)
		}
		if cs := listEntries(t, s, b); len(cs) != 1 {
			t.Errorf("B children = %+v", cs)
		}
		if got := savedIDs(t, s, b); !slices.Equal(got, []int64{bEntry}) {
			t.Errorf("B saved = %v", got)
		}
	})
}

func TestListConstraints(t *testing.T) {
	s := openTest(t)
	a, b := rawList(t, s, "A"), rawList(t, s, "B")
	exec := func(q string, args ...any) error {
		_, err := s.db.Exec(q, args...)
		return err
	}
	parent := func(id, list int64, email string) error {
		return exec(`INSERT INTO parents (id, list_id, name, email, created_at, updated_at) VALUES (?, ?, 'P', ?, 't', 't')`, id, list, email)
	}
	child := func(list int64, parentID any, fam, name string) error {
		return exec(`INSERT INTO children (list_id, parent_id, family_number, name, name_key, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 't', 't')`,
			list, parentID, fam, name, strings.ToLower(name))
	}

	t.Run("same email in two lists", func(t *testing.T) {
		if err := parent(1, a, "x@example.fr"); err != nil {
			t.Fatal(err)
		}
		if err := parent(2, b, "x@example.fr"); err != nil {
			t.Errorf("same email in another list refused: %v", err)
		}
		if err := parent(3, a, "x@example.fr"); err == nil {
			t.Error("same email twice in one list accepted")
		}
	})
	t.Run("same no-email family number in two lists", func(t *testing.T) {
		if err := child(a, nil, "77", "N"); err != nil {
			t.Fatal(err)
		}
		if err := child(b, nil, "77", "N"); err != nil {
			t.Errorf("other list refused: %v", err)
		}
		if err := child(a, nil, "77", "N2"); err == nil {
			t.Error("twice in one list accepted")
		}
	})
	t.Run("child whose parent is in another list", func(t *testing.T) {
		// Parent 1 is in list a.
		if err := child(b, 1, "", "Léa"); err == nil {
			t.Fatal("cross-list child accepted")
		}
		if err := child(a, 1, "", "Léa"); err != nil {
			t.Errorf("same-list child refused: %v", err)
		}
	})
	t.Run("unknown list", func(t *testing.T) {
		if err := parent(9, 999, "z@example.fr"); err == nil {
			t.Error("parent in an unknown list accepted")
		}
	})
	t.Run("duplicate list name key", func(t *testing.T) {
		if _, err := s.db.Exec(`INSERT INTO lists (name, name_key, created_at, updated_at, used_at) VALUES ('a', 'a', 't', 't', 't')`); err == nil {
			t.Error("duplicate name_key accepted")
		}
	})
}

// ---- ImportFamilies ------------------------------------------------------

func TestImportScopedToList(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	tick(s)

	shared := ImportRow{ParentName: "Marie Dupont", ChildName: "Léa Dupont", Email: "marie@example.fr"}
	a := newList(t, s, "Cantine", []ImportRow{shared, {ParentName: "Marie Dupont", ChildName: "Tom Dupont", Email: "marie@example.fr"}})
	b := newList(t, s, "Garderie", []ImportRow{shared, {ParentName: "Paul Martin", ChildName: "Zoé Martin", Email: "paul@example.fr"}})

	aEntries, bEntries := listEntries(t, s, a), listEntries(t, s, b)
	if len(aEntries) != 2 || len(bEntries) != 2 {
		t.Fatalf("a = %+v, b = %+v", aEntries, bEntries)
	}
	// The same family in two lists is two independent entries.
	for _, ca := range aEntries {
		for _, cb := range bEntries {
			if ca.ID == cb.ID || (ca.ParentID == cb.ParentID) {
				t.Errorf("entries or parents shared between lists: %+v %+v", ca, cb)
			}
		}
	}
	bBefore, err := s.List(ctx, b)
	if err != nil {
		t.Fatal(err)
	}

	// Replace A with a file that removes everyone but a new family.
	sum := replaceList(t, s, a, []ImportRow{{ParentName: "Ana Silva", ChildName: "Rui Silva", Email: "ana@example.fr"}})
	if sum.Children != (Changes{Added: 1, Deleted: 2}) || sum.Parents != (Changes{Added: 1, Deleted: 1}) {
		t.Errorf("A summary = %+v / %+v", sum.Parents, sum.Children)
	}
	if len(sum.DeletedChildren) != 2 {
		t.Errorf("deleted = %+v", sum.DeletedChildren)
	}

	// B is untouched: rows, IDs, counts, used_at.
	if got := listEntries(t, s, b); !slices.Equal(got, bEntries) {
		t.Errorf("B changed: %+v -> %+v", bEntries, got)
	}
	bAfter, _ := s.List(ctx, b)
	if bAfter != bBefore {
		t.Errorf("B list changed: %+v -> %+v", bBefore, bAfter)
	}
	if v, _ := s.ListVersion(ctx, b); v != 2 {
		t.Errorf("B version = %d, want 2", v)
	}

	// A file with only B's family into A: B's entries are never matched.
	sum = replaceList(t, s, a, []ImportRow{shared})
	if sum.Children.Added != 1 || sum.Children.Unchanged != 0 || sum.Parents.Added != 1 {
		t.Errorf("A summary = %+v / %+v (must not match B's rows)", sum.Parents, sum.Children)
	}

	// Counts per list.
	ls, _ := s.Lists(ctx)
	for _, l := range ls {
		switch l.ID {
		case a:
			if l.Children != 1 || l.Parents != 1 {
				t.Errorf("A counts = %+v", l)
			}
		case b:
			if l.Children != 2 || l.Parents != 2 {
				t.Errorf("B counts = %+v", l)
			}
		}
	}
}

func TestImportNewListAndDryRuns(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	tick(s)
	rows := []ImportRow{
		{ParentName: "Marie", ChildName: "Léa", Email: "m@example.fr"},
		{ParentName: "Marie", ChildName: "Tom", Email: "m@example.fr"},
	}

	// A dry run of a new list leaves nothing behind, not even the list.
	dry, err := s.ImportFamilies(ctx, ImportRequest{FileName: "a", Rows: rows, NewListName: "  Cantine   2026 ", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if dry.ListID != 0 || dry.ImportID != 0 || dry.Base != 0 || dry.ListName != "Cantine 2026" {
		t.Errorf("dry summary = %+v", dry)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM lists`); n != 0 {
		t.Fatalf("dry run left %d list rows", n)
	}
	assertImports(t, s, 0)
	if n := countRows(t, s, `SELECT COUNT(*) FROM parents`) + countRows(t, s, `SELECT COUNT(*) FROM children`); n != 0 {
		t.Fatalf("dry run left %d rows", n)
	}

	real, err := s.ImportFamilies(ctx, ImportRequest{FileName: "a.xlsx", RowsRead: 2, Rows: rows, NewListName: "  Cantine   2026 "})
	if err != nil {
		t.Fatal(err)
	}
	if real.ListID == 0 || real.ImportID == 0 || real.ListName != "Cantine 2026" {
		t.Errorf("real summary = %+v", real)
	}
	// Only additions, the same as the dry run.
	if real.Parents != dry.Parents || real.Children != dry.Children || real.Children != (Changes{Added: 2}) || len(real.DeletedChildren) != 0 {
		t.Errorf("dry = %+v/%+v, real = %+v/%+v", dry.Parents, dry.Children, real.Parents, real.Children)
	}

	// The real import sets the timestamps and the history.
	var created, updated, used string
	if err := s.db.QueryRow(`SELECT created_at, updated_at, used_at FROM lists WHERE id = ?`, real.ListID).Scan(&created, &updated, &used); err != nil {
		t.Fatal(err)
	}
	if created == "" || updated != used {
		t.Errorf("timestamps = %q %q %q", created, updated, used)
	}
	imps, _ := s.Imports(ctx, 5)
	if len(imps) != 1 || imps[0].ListID != real.ListID || imps[0].ListName != "Cantine 2026" || imps[0].FileName != "a.xlsx" {
		t.Errorf("imports = %+v", imps)
	}
	var lid int64
	var lname string
	if err := s.db.QueryRow(`SELECT list_id, list_name FROM imports`).Scan(&lid, &lname); err != nil || lid != real.ListID || lname != "Cantine 2026" {
		t.Errorf("imports row = %d %q, %v", lid, lname, err)
	}

	// Dry run of an existing list equals the real import and writes nothing.
	again := []ImportRow{{ParentName: "Marie Dupont", ChildName: "Léa", Email: "m@example.fr"}, {ParentName: "Zed", ChildName: "Zoé", Email: "z@example.fr"}}
	before, _ := s.List(ctx, real.ListID)
	dry, err = s.ImportFamilies(ctx, ImportRequest{FileName: "b", Rows: again, ListID: real.ListID, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if dry.ListID != real.ListID || dry.ListName != "Cantine 2026" || dry.ImportID != 0 || dry.Base != real.ImportID {
		t.Errorf("dry = %+v", dry)
	}
	if after, _ := s.List(ctx, real.ListID); after != before {
		t.Errorf("dry run changed the list: %+v -> %+v", before, after)
	}
	assertImports(t, s, 1)
	real2 := replaceList(t, s, real.ListID, again)
	if dry.Parents != real2.Parents || dry.Children != real2.Children || len(dry.DeletedChildren) != len(real2.DeletedChildren) {
		t.Errorf("dry %+v/%+v differs from real %+v/%+v", dry.Parents, dry.Children, real2.Parents, real2.Children)
	}
	if real2.Children != (Changes{Added: 1, Deleted: 1, Unchanged: 1}) {
		t.Errorf("children = %+v", real2.Children)
	}
	after, _ := s.List(ctx, real.ListID)
	if !after.UsedAt.After(before.UsedAt) {
		t.Errorf("used_at not updated: %v -> %v", before.UsedAt, after.UsedAt)
	}
	var upd string
	_ = s.db.QueryRow(`SELECT updated_at FROM lists WHERE id = ?`, real.ListID).Scan(&upd)
	if upd == updated {
		t.Error("updated_at not changed by a real import")
	}
}

func TestImportRequiresExactlyOneTarget(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	id := newList(t, s, "Cantine", rowsMarie)
	before := listEntries(t, s, id)

	for name, req := range map[string]ImportRequest{
		"neither":            {FileName: "x", Rows: rowsPaul},
		"both":               {FileName: "x", Rows: rowsPaul, ListID: id, NewListName: "Autre"},
		"blank new name":     {FileName: "x", Rows: rowsPaul, NewListName: "   "},
		"neither, dry run":   {FileName: "x", Rows: rowsPaul, DryRun: true},
		"both, dry run":      {FileName: "x", Rows: rowsPaul, ListID: id, NewListName: "Autre", DryRun: true},
		"blank name dry run": {FileName: "x", Rows: rowsPaul, NewListName: "\t", DryRun: true},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.ImportFamilies(ctx, req)
			if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrNameTaken) || errors.Is(err, ErrStaleImport) {
				t.Fatalf("err = %v, want a plain programming error", err)
			}
		})
	}
	if got := listEntries(t, s, id); !slices.Equal(got, before) {
		t.Errorf("entries changed: %+v", got)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM lists`); n != 1 {
		t.Errorf("lists = %d", n)
	}
	assertImports(t, s, 1)
}

func TestImportConflicts(t *testing.T) {
	ctx := context.Background()

	t.Run("unknown list", func(t *testing.T) {
		s := openTest(t)
		for _, dry := range []bool{true, false} {
			if _, err := s.ImportFamilies(ctx, ImportRequest{FileName: "x", Rows: rowsMarie, ListID: 42, DryRun: dry}); !errors.Is(err, ErrNotFound) {
				t.Errorf("dry=%v: err = %v", dry, err)
			}
		}
		assertImports(t, s, 0)
	})

	t.Run("list deleted between preview and import", func(t *testing.T) {
		s := openTest(t)
		id := newList(t, s, "A", rowsMarie)
		prev, err := s.ImportFamilies(ctx, ImportRequest{FileName: "x", Rows: rowsPaul, ListID: id, DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteList(ctx, id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ImportFamilies(ctx, ImportRequest{FileName: "x", Rows: rowsPaul, ListID: id, Base: &prev.Base}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
		assertImports(t, s, 1)
	})

	t.Run("stale only for the same list", func(t *testing.T) {
		s := openTest(t)
		a := newList(t, s, "A", rowsMarie)
		b := newList(t, s, "B", rowsPaul)
		prevA, err := s.ImportFamilies(ctx, ImportRequest{FileName: "x", Rows: rowsMarie, ListID: a, DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		replaceList(t, s, b, rowsMarie) // other list: does not matter
		if _, err := s.ImportFamilies(ctx, ImportRequest{FileName: "x", Rows: rowsMarie, ListID: a, Base: &prevA.Base}); err != nil {
			t.Fatalf("import into another list made A stale: %v", err)
		}
		// The same preview again is stale now (A changed).
		if _, err := s.ImportFamilies(ctx, ImportRequest{FileName: "x", Rows: rowsMarie, ListID: a, Base: &prevA.Base}); !errors.Is(err, ErrStaleImport) {
			t.Fatalf("err = %v, want ErrStaleImport", err)
		}
		// Nothing was written by the failed call.
		assertImports(t, s, 4)
	})

	t.Run("nil base is not checked", func(t *testing.T) {
		s := openTest(t)
		a := newList(t, s, "A", rowsMarie)
		replaceList(t, s, a, rowsMarie)
		replaceList(t, s, a, rowsMarie)
	})

	t.Run("new name taken, also re-cased or accented", func(t *testing.T) {
		s := openTest(t)
		newList(t, s, "Cantine été", rowsMarie)
		for _, name := range []string{"Cantine été", "CANTINE ÉTÉ", "cantine ete", "  Cantine   été "} {
			for _, dry := range []bool{true, false} {
				_, err := s.ImportFamilies(ctx, ImportRequest{FileName: "x", Rows: rowsPaul, NewListName: name, DryRun: dry})
				if !errors.Is(err, ErrNameTaken) {
					t.Errorf("%q dry=%v: err = %v", name, dry, err)
				}
			}
		}
		if n := countRows(t, s, `SELECT COUNT(*) FROM lists`); n != 1 {
			t.Errorf("lists = %d", n)
		}
		assertImports(t, s, 1)
	})

	t.Run("failure rolls back the new list", func(t *testing.T) {
		s := openTest(t)
		_, err := s.ImportFamilies(ctx, ImportRequest{FileName: "x", NewListName: "A", Rows: []ImportRow{{ParentName: "X", ChildName: "Y"}}})
		if err == nil {
			t.Fatal("want error")
		}
		if n := countRows(t, s, `SELECT COUNT(*) FROM lists`); n != 0 {
			t.Errorf("lists = %d, want 0", n)
		}
	})

	t.Run("closed store", func(t *testing.T) {
		s := openTest(t)
		s.Close()
		if _, err := s.ImportFamilies(ctx, ImportRequest{FileName: "x", NewListName: "A", Rows: rowsMarie}); err == nil {
			t.Error("want error")
		}
	})
}

// ---- list version --------------------------------------------------------

func TestListVersion(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	tick(s)
	a := newList(t, s, "A", rowsMarie)
	b := newList(t, s, "B", rowsPaul)
	version := func(id int64) int64 {
		t.Helper()
		v, err := s.ListVersion(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if va, vb := version(a), version(b); va != 1 || vb != 2 {
		t.Fatalf("versions = %d, %d; want 1, 2", va, vb)
	}

	// A list without any import has version 0.
	empty := rawList(t, s, "Vide")
	if v := version(empty); v != 0 {
		t.Errorf("empty list version = %d", v)
	}

	// A dry run does not change it.
	if _, err := s.ImportFamilies(ctx, ImportRequest{FileName: "x", Rows: rowsPaul, ListID: a, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if version(a) != 1 {
		t.Error("dry run changed the version")
	}

	// An import into B grows B's version only; a rename changes none.
	sum := replaceList(t, s, b, rowsPaul)
	if version(b) != sum.ImportID || version(a) != 1 {
		t.Errorf("versions = %d, %d", version(a), version(b))
	}
	if err := s.RenameList(ctx, a, "A bis"); err != nil {
		t.Fatal(err)
	}
	if version(a) != 1 {
		t.Error("rename changed the version")
	}

	// A deleted list is not found; its ID is never reused.
	if err := s.DeleteList(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListVersion(ctx, b); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
	c := newList(t, s, "B", rowsPaul)
	if c == b || c <= b {
		t.Errorf("deleted list ID %d reused: new list got %d", b, c)
	}
	if _, err := s.ListVersion(ctx, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("ListVersion(0) err = %v", err)
	}
	if _, err := s.ListVersion(ctx, -1); !errors.Is(err, ErrNotFound) {
		t.Errorf("ListVersion(-1) err = %v", err)
	}

	s.Close()
	if _, err := s.ListVersion(ctx, a); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("closed store: err = %v", err)
	}
}

// ---- Children, SelectedChildren, selection ------------------------------

func TestChildrenScopedToList(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a := newList(t, s, "A", []ImportRow{{ParentName: "Anne", ChildName: "Alice", Email: "a@example.fr"}})
	b := newList(t, s, "B", []ImportRow{{ParentName: "Bob", ChildName: "Bastien", Email: "b@example.fr"}, {ParentName: "Bob", ChildName: "Berta", Email: "b@example.fr"}})
	ae, be := listEntries(t, s, a), listEntries(t, s, b)
	if len(ae) != 1 || ae[0].Name != "Alice" || len(be) != 2 {
		t.Fatalf("a = %+v, b = %+v", ae, be)
	}
	if got := listEntries(t, s, 999); got == nil || len(got) != 0 {
		t.Errorf("unknown list = %#v", got)
	}

	t.Run("ChildrenByIDs drops IDs of other lists", func(t *testing.T) {
		got, err := s.ChildrenByIDs(ctx, a, []int64{ae[0].ID, be[0].ID, be[1].ID})
		if err != nil || !slices.Equal(got, ae) {
			t.Fatalf("got %+v, %v", got, err)
		}
		got, err = s.ChildrenByIDs(ctx, a, []int64{be[0].ID})
		if err != nil || len(got) != 0 {
			t.Fatalf("got %+v, %v", got, err)
		}
	})

	t.Run("SelectedChildren", func(t *testing.T) {
		va, _ := s.ListVersion(ctx, a)
		vb, _ := s.ListVersion(ctx, b)
		// IDs of another list are dropped even with a matching version.
		got, err := s.SelectedChildren(ctx, a, va, []int64{ae[0].ID, be[0].ID})
		if err != nil || !slices.Equal(got, ae) {
			t.Fatalf("got %+v, %v", got, err)
		}
		// Version of another list is stale.
		if _, err := s.SelectedChildren(ctx, a, vb, []int64{ae[0].ID}); !errors.Is(err, ErrStaleSelection) {
			t.Errorf("other list's version: err = %v", err)
		}
		// Deleted or unknown list is stale, not NotFound.
		for _, id := range []int64{999, 0, -1} {
			if _, err := s.SelectedChildren(ctx, id, 0, []int64{1}); !errors.Is(err, ErrStaleSelection) {
				t.Errorf("list %d: err = %v", id, err)
			}
		}
		// An import into B does not make A stale; into A does.
		replaceList(t, s, b, rowsPaul)
		if _, err := s.SelectedChildren(ctx, a, va, nil); err != nil {
			t.Errorf("import into B made A stale: %v", err)
		}
		replaceList(t, s, a, []ImportRow{{ParentName: "Anne", ChildName: "Alice", Email: "a@example.fr"}})
		if _, err := s.SelectedChildren(ctx, a, va, nil); !errors.Is(err, ErrStaleSelection) {
			t.Errorf("err = %v", err)
		}
		if err := s.DeleteList(ctx, a); err != nil {
			t.Fatal(err)
		}
		v, _ := s.ListVersion(ctx, b)
		if _, err := s.SelectedChildren(ctx, a, v, nil); !errors.Is(err, ErrStaleSelection) {
			t.Errorf("deleted list: err = %v", err)
		}
	})
}

func TestSaveSelectionPerList(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	tick(s)
	a := newList(t, s, "A", []ImportRow{{ParentName: "Anne", ChildName: "Alice", Email: "a@example.fr"}, {ParentName: "Anne", ChildName: "Ana", Email: "a@example.fr"}})
	b := newList(t, s, "B", []ImportRow{{ParentName: "Bob", ChildName: "Bastien", Email: "b@example.fr"}})
	ae, be := listEntries(t, s, a), listEntries(t, s, b)

	if err := s.SaveSelection(ctx, b, []int64{be[0].ID}); err != nil {
		t.Fatal(err)
	}
	usedB, _ := s.List(ctx, b)

	// Saving for A leaves B's rows intact and makes A the default.
	if err := s.SaveSelection(ctx, a, []int64{ae[0].ID, ae[1].ID}); err != nil {
		t.Fatal(err)
	}
	if got := savedIDs(t, s, b); !slices.Equal(got, []int64{be[0].ID}) {
		t.Errorf("B saved = %v", got)
	}
	if got := savedIDs(t, s, a); len(got) != 2 {
		t.Errorf("A saved = %v", got)
	}
	la, _ := s.List(ctx, a)
	if def, _ := s.DefaultList(ctx); def.ID != a || !la.UsedAt.After(usedB.UsedAt) {
		t.Errorf("default = %+v, A used_at %v, B used_at %v", def, la.UsedAt, usedB.UsedAt)
	}
	if lb, _ := s.List(ctx, b); lb.UsedAt != usedB.UsedAt {
		t.Error("saving for A changed B's used_at")
	}

	// IDs of B posted with A are not saved; the saved set of A is replaced.
	if err := s.SaveSelection(ctx, a, []int64{ae[0].ID, be[0].ID}); err != nil {
		t.Fatal(err)
	}
	if got := savedIDs(t, s, a); !slices.Equal(got, []int64{ae[0].ID}) {
		t.Errorf("A saved = %v", got)
	}
	if got := savedIDs(t, s, b); !slices.Equal(got, []int64{be[0].ID}) {
		t.Errorf("B saved = %v", got)
	}
	// Reading A never shows B's rows.
	m, _ := s.SavedSelection(ctx, a)
	if m[be[0].ID] {
		t.Error("SavedSelection(A) holds B's entry")
	}

	// Unknown list: ErrNotFound, and nothing changes.
	for _, id := range []int64{999, 0, -4} {
		if err := s.SaveSelection(ctx, id, []int64{ae[0].ID}); !errors.Is(err, ErrNotFound) {
			t.Errorf("list %d: err = %v", id, err)
		}
	}
	if got := savedIDs(t, s, a); !slices.Equal(got, []int64{ae[0].ID}) {
		t.Errorf("A saved = %v after failed saves", got)
	}
	if m, err := s.SavedSelection(ctx, 999); err != nil || len(m) != 0 {
		t.Errorf("SavedSelection(unknown) = %v, %v", m, err)
	}
}

// ---- Lists, List, DefaultList, rename, delete, Imports ------------------

func TestListsAndCounts(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	tick(s)

	if ls, err := s.Lists(ctx); err != nil || ls == nil || len(ls) != 0 {
		t.Fatalf("empty: %#v, %v", ls, err)
	}
	if _, err := s.DefaultList(ctx); !errors.Is(err, ErrNotFound) {
		t.Errorf("DefaultList err = %v", err)
	}
	if _, err := s.List(ctx, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("List err = %v", err)
	}

	z := newList(t, s, "Zèbre", rowsMarie) // created first
	e := newList(t, s, "École", []ImportRow{
		fam("10", "A", "a@example.fr", ""), fam("10", "A", "b@example.fr", ""), fam("20", "B", "", "x"),
	})
	m := newList(t, s, "marché", rowsPaul)
	empty := rawList(t, s, "Vide")

	ls, err := s.Lists(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, l := range ls {
		names = append(names, l.Name)
	}
	// Ordered by name key: école, marché, vide, zebre.
	if want := []string{"École", "marché", "Vide", "Zèbre"}; !slices.Equal(names, want) {
		t.Fatalf("order = %v, want %v", names, want)
	}
	byID := map[int64]List{}
	for _, l := range ls {
		byID[l.ID] = l
	}
	if l := byID[z]; l.Parents != 1 || l.Children != 1 || l.Families != 0 || l.LastImport.IsZero() || l.UsedAt.IsZero() {
		t.Errorf("Zèbre = %+v", l)
	}
	if l := byID[e]; l.Parents != 2 || l.Children != 3 || l.Families != 2 {
		t.Errorf("École = %+v (2 addresses, 3 entries, 2 family numbers)", l)
	}
	if l := byID[empty]; l.Parents != 0 || l.Children != 0 || !l.LastImport.IsZero() {
		t.Errorf("Vide = %+v", l)
	}
	one, err := s.List(ctx, e)
	if err != nil || one != byID[e] {
		t.Errorf("List = %+v, %v", one, err)
	}
	// LastImport is the date of the highest import ID of the list.
	first := byID[m].LastImport
	replaceList(t, s, m, rowsPaul)
	if l, _ := s.List(ctx, m); !l.LastImport.After(first) {
		t.Errorf("last import %v not after %v", l.LastImport, first)
	}
	// Ties on name key are impossible, but the id breaks equal keys anyway.
	s.Close()
	if _, err := s.Lists(ctx); err == nil {
		t.Error("closed store: want error")
	}
}

func TestDefaultList(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	tick(s)
	a := newList(t, s, "A", rowsMarie)
	b := newList(t, s, "B", rowsPaul)
	def := func() int64 {
		t.Helper()
		l, err := s.DefaultList(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return l.ID
	}
	if def() != b {
		t.Errorf("last import should win: %d", def())
	}
	replaceList(t, s, a, rowsMarie)
	if def() != a {
		t.Error("import into A should make A the default")
	}
	// Last saved selection wins.
	if err := s.SaveSelection(ctx, b, nil); err != nil {
		t.Fatal(err)
	}
	if def() != b {
		t.Error("Continue on B should make B the default")
	}
	// A dry run, a rename and reading change nothing.
	_, _ = s.ImportFamilies(ctx, ImportRequest{FileName: "x", Rows: rowsMarie, ListID: a, DryRun: true})
	if err := s.RenameList(ctx, a, "A2"); err != nil {
		t.Fatal(err)
	}
	if def() != b {
		t.Error("dry run or rename changed the default")
	}
	// Equal used_at: the larger id wins.
	if _, err := s.db.Exec(`UPDATE lists SET used_at = '2026-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if def() != b {
		t.Errorf("tie: want the largest id %d, got %d", b, def())
	}
	// After deleting the default, the other one is.
	if err := s.DeleteList(ctx, b); err != nil {
		t.Fatal(err)
	}
	if def() != a {
		t.Error("after delete")
	}
}

func TestRenameList(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a := newList(t, s, "Cantine été", rowsMarie)
	b := newList(t, s, "Garderie", rowsPaul)
	before := listEntries(t, s, a)
	_ = b

	if err := s.RenameList(ctx, 999, "X"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	for _, name := range []string{"Garderie", "GARDERIE", " garderie ", "gardérie"} {
		if err := s.RenameList(ctx, a, name); !errors.Is(err, ErrNameTaken) {
			t.Errorf("%q: err = %v", name, err)
		}
	}
	for _, name := range []string{"", "   ", "\t\n"} {
		err := s.RenameList(ctx, a, name)
		if err == nil || errors.Is(err, ErrNameTaken) || errors.Is(err, ErrNotFound) {
			t.Errorf("%q: err = %v", name, err)
		}
	}
	// Unknown ID reported before... or taken: taken name on unknown ID is an error either way.
	if err := s.RenameList(ctx, 999, "Garderie"); err == nil {
		t.Error("unknown + taken: want error")
	}

	// Re-casing the list's own name is allowed; the name is cleaned.
	if err := s.RenameList(ctx, a, "  CANTINE   ÉTÉ  "); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.List(ctx, a); l.Name != "CANTINE ÉTÉ" {
		t.Errorf("name = %q", l.Name)
	}
	if err := s.RenameList(ctx, a, "Cantine  2027"); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.List(ctx, a); l.Name != "Cantine 2027" {
		t.Errorf("name = %q", l.Name)
	}
	// Entries, version unchanged; the freed name can be used again.
	if got := listEntries(t, s, a); !slices.Equal(got, before) {
		t.Error("entries changed by a rename")
	}
	if err := s.RenameList(ctx, b, "Cantine été"); err != nil {
		t.Errorf("freed name refused: %v", err)
	}
	// The history shows the current name.
	imps, _ := s.Imports(ctx, 10)
	for _, i := range imps {
		if i.ListID == a && i.ListName != "Cantine 2027" {
			t.Errorf("import shows %q after rename", i.ListName)
		}
	}
	s.Close()
	if err := s.RenameList(ctx, a, "Z"); err == nil {
		t.Error("closed store: want error")
	}
}

func TestDeleteListAndImportHistory(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a := newList(t, s, "Cantine", rowsMarie)
	b := newList(t, s, "Garderie", rowsPaul)
	if err := s.SaveSelection(ctx, a, []int64{listEntries(t, s, a)[0].ID}); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteList(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteList(ctx, a); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if err := s.DeleteList(ctx, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete 0: %v", err)
	}
	if _, err := s.List(ctx, a); !errors.Is(err, ErrNotFound) {
		t.Errorf("List after delete: %v", err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM last_selection`); n != 0 {
		t.Errorf("last_selection rows = %d", n)
	}
	// The history keeps the old name with the deleted marker.
	imps, err := s.Imports(ctx, 10)
	if err != nil || len(imps) != 2 {
		t.Fatalf("imports = %+v, %v", imps, err)
	}
	if i := imps[0]; i.ListID != b || i.ListName != "Garderie" || i.ListDeleted {
		t.Errorf("live import = %+v", i)
	}
	if i := imps[1]; i.ListID != 0 || i.ListName != "Cantine" || !i.ListDeleted {
		t.Errorf("deleted-list import = %+v", i)
	}
	// A new list with the old name is a different list.
	c := newList(t, s, "Cantine", rowsMarie)
	if c == a {
		t.Error("ID reused")
	}
	if imps, _ = s.Imports(ctx, 10); imps[0].ListID != c || !imps[2].ListDeleted {
		t.Errorf("imports = %+v", imps)
	}
	s.Close()
	if err := s.DeleteList(ctx, b); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("closed store: %v", err)
	}
}

func TestImportsLimit(t *testing.T) {
	s := openTest(t)
	a := newList(t, s, "A", rowsMarie)
	for range 3 {
		replaceList(t, s, a, rowsMarie)
	}
	got, err := s.Imports(context.Background(), 2)
	if err != nil || len(got) != 2 || got[0].ID < got[1].ID {
		t.Fatalf("imports = %+v, %v", got, err)
	}
}

func TestDeleteListKeepsLastName(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	tick(s)
	a := newList(t, s, "Cantine", rowsMarie)
	replaceList(t, s, a, rowsPaul)
	b := newList(t, s, "Garderie", rowsPaul)
	other := newList(t, s, "Autre", rowsMarie)
	if err := s.RenameList(ctx, a, "Cantine 2027"); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameList(ctx, other, "Autre bis"); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteList(ctx, a); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM imports WHERE list_name = 'Cantine 2027' AND list_id IS NULL`); n != 2 {
		t.Errorf("imports with the last name = %d, want 2", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM imports WHERE list_name = 'Cantine'`); n != 0 {
		t.Errorf("imports with the old name = %d", n)
	}
	imps, err := s.Imports(ctx, 10)
	if err != nil || len(imps) != 4 {
		t.Fatalf("imports = %+v, %v", imps, err)
	}
	for _, i := range imps {
		switch {
		case i.ListDeleted:
			if i.ListName != "Cantine 2027" {
				t.Errorf("deleted import = %+v", i)
			}
		case i.ListID == b:
			if i.ListName != "Garderie" {
				t.Errorf("other import = %+v", i)
			}
		case i.ListID == other:
			if i.ListName != "Autre bis" {
				t.Errorf("other import = %+v", i)
			}
		default:
			t.Errorf("unexpected import %+v", i)
		}
	}
}

func TestDeleteListUnknownChangesNothing(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a := newList(t, s, "Cantine", rowsMarie)
	before := countRows(t, s, `SELECT COUNT(*) FROM imports WHERE list_id = ? AND list_name = 'Cantine'`, a)
	for _, id := range []int64{0, -1, a + 100} {
		if err := s.DeleteList(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("DeleteList(%d) = %v", id, err)
		}
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM imports WHERE list_id = ? AND list_name = 'Cantine'`, a); n != before {
		t.Errorf("imports changed: %d -> %d", before, n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM imports`); n != 1 {
		t.Errorf("imports = %d", n)
	}
	if _, err := s.List(ctx, a); err != nil {
		t.Errorf("list gone: %v", err)
	}
}
