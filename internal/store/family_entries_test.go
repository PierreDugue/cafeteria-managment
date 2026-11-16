package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// fam builds a family entry row; email "" means no valid email.
func fam(num, name, email, invalid string) ImportRow {
	return ImportRow{ParentName: name, ChildName: name, Email: email, FamilyNumber: num, InvalidEmail: invalid}
}

func importRows(t *testing.T, s *Store, rows []ImportRow) ImportSummary {
	t.Helper()
	sum, err := s.ImportFamilies(context.Background(), ImportRequest{FileName: "f.csv", RowsRead: len(rows), Rows: rows})
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func allChildren(t *testing.T, s *Store) []Child {
	t.Helper()
	cs, err := s.Children(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func findEntry(t *testing.T, cs []Child, num, email string) Child {
	t.Helper()
	for _, c := range cs {
		if c.FamilyNumber == num && c.Email == email {
			return c
		}
	}
	t.Fatalf("entry (%q, %q) not found in %+v", num, email, cs)
	return Child{}
}

func TestMigration002KeepsExistingData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")

	// A database as migration 001 left it, holding Excel data.
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	m1, err := migrationsFS.ReadFile("migrations/001_families.sql")
	if err != nil {
		files, _ := migrationsFS.ReadDir("migrations")
		t.Fatalf("read 001: %v (files %v)", err, files)
	}
	for _, q := range []string{
		string(m1),
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`INSERT INTO schema_migrations VALUES (1, '2026-01-01T00:00:00Z')`,
		`INSERT INTO parents VALUES (5, 'Marie Dupont', 'marie@example.fr', 't', 't')`,
		`INSERT INTO parents VALUES (9, 'Paul Martin', 'paul@example.fr', 't', 't')`,
		`INSERT INTO children VALUES (7, 5, 'Léa', 'lea', 't', 't')`,
		`INSERT INTO children VALUES (8, 5, 'Tom', 'tom', 't', 't')`,
		`INSERT INTO children VALUES (12, 9, 'Zoé', 'zoe', 't', 't')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	cs := allChildren(t, s)
	if len(cs) != 3 {
		t.Fatalf("children = %+v", cs)
	}
	want := map[int64]struct{ name, email string }{
		7: {"Léa", "marie@example.fr"}, 8: {"Tom", "marie@example.fr"}, 12: {"Zoé", "paul@example.fr"},
	}
	for _, c := range cs {
		w, ok := want[c.ID]
		if !ok || c.Name != w.name || c.Email != w.email || c.FamilyNumber != "" || c.InvalidEmail != "" || !c.CanEmail() {
			t.Errorf("child = %+v, want %+v", c, w)
		}
	}
	tot, err := s.Totals(ctx)
	if err != nil || tot.Parents != 2 || tot.Children != 3 || tot.Families != 0 {
		t.Fatalf("totals = %+v, %v", tot, err)
	}

	var version int
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version < 2 {
		t.Fatalf("version = %d, %v", version, err)
	}

	// Constraints work on the migrated table.
	if _, err := s.db.Exec(`INSERT INTO children (parent_id, name, name_key, created_at, updated_at) VALUES (5, 'LÉA', 'lea', 't', 't')`); err == nil {
		t.Error("duplicate (parent, name_key) accepted after migration")
	}
	// Foreign key cascade still works.
	if _, err := s.db.Exec(`DELETE FROM parents WHERE id = 9`); err != nil {
		t.Fatal(err)
	}
	if got := allChildren(t, s); len(got) != 2 {
		t.Errorf("cascade delete failed: %+v", got)
	}

	// Re-import keeps IDs of the migrated rows.
	sum := importRows(t, s, []ImportRow{
		{ParentName: "Marie Dupont", ChildName: "Léa", Email: "marie@example.fr"},
	})
	if sum.Children.Unchanged != 1 || sum.Children.Deleted != 1 {
		t.Errorf("summary = %+v", sum.Children)
	}
	if got := allChildren(t, s); len(got) != 1 || got[0].ID != 7 {
		t.Errorf("children = %+v", got)
	}
}

func TestMigration002Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	for range 2 {
		s, err := Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
}

func TestFamilyEntriesBasics(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	sum := importRows(t, s, []ImportRow{
		fam("10", "DUPONT Marie", "marie@example.fr", ""),
		fam("10", "DUPONT Jean", "jean@example.fr", ""),
		fam("10", "DUPONT Marie", "", "oups@gmailcom"),
		fam("20", "MARTIN", "shared@example.fr", ""),
		fam("30", "DURAND", "shared@example.fr", ""),
		fam("40", "NOMAIL", "", ""),
	})
	if sum.Children != (Changes{Added: 6}) || sum.Parents != (Changes{Added: 3}) {
		t.Fatalf("summary = %+v / %+v", sum.Children, sum.Parents)
	}

	cs := allChildren(t, s)
	if len(cs) != 6 {
		t.Fatalf("children = %+v", cs)
	}
	nm := findEntry(t, cs, "10", "")
	if nm.CanEmail() || nm.ParentID != 0 || nm.ParentName != "" || nm.InvalidEmail != "oups@gmailcom" || nm.Name != "DUPONT Marie" {
		t.Errorf("no-email entry = %+v", nm)
	}
	if e := findEntry(t, cs, "40", ""); e.InvalidEmail != "" || e.CanEmail() {
		t.Errorf("empty-email entry = %+v", e)
	}
	// Two families on one email: one parent, two entries.
	a, b := findEntry(t, cs, "20", "shared@example.fr"), findEntry(t, cs, "30", "shared@example.fr")
	if a.ParentID == 0 || a.ParentID != b.ParentID {
		t.Errorf("shared parent: %+v %+v", a, b)
	}
	if v := findEntry(t, cs, "10", "marie@example.fr"); !v.CanEmail() || v.InvalidEmail != "" || v.FamilyNumber != "10" {
		t.Errorf("valid entry = %+v", v)
	}

	tot, err := s.Totals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 4 distinct family numbers; three distinct addresses.
	if tot.Families != 4 || tot.Children != 6 || tot.Parents != 3 {
		t.Errorf("totals = %+v", tot)
	}
}

func TestFamilyEntriesReimportStable(t *testing.T) {
	s := openTest(t)
	rows := []ImportRow{
		fam("10", "DUPONT", "a@example.fr", ""),
		fam("20", "MARTIN", "", "bad"),
		fam("30", "DURAND", "", ""),
	}
	importRows(t, s, rows)
	before := allChildren(t, s)

	sum := importRows(t, s, rows)
	if sum.Children != (Changes{Unchanged: 3}) || sum.Parents != (Changes{Unchanged: 1}) {
		t.Fatalf("summary = %+v / %+v", sum.Children, sum.Parents)
	}
	after := allChildren(t, s)
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("entry changed: %+v -> %+v", before[i], after[i])
		}
	}

	// Changed NOMFACT (valid entry) and changed invalid value / name
	// (no-email entries) are updates keeping the ID.
	sum = importRows(t, s, []ImportRow{
		fam("10", "DUPONT-LEROY", "a@example.fr", ""),
		fam("20", "MARTIN", "", "other"),
		fam("30", "DURAND Jr", "", ""),
	})
	if sum.Children != (Changes{Updated: 3}) {
		t.Fatalf("children = %+v", sum.Children)
	}
	if sum.Parents != (Changes{Updated: 1}) {
		t.Errorf("parents = %+v", sum.Parents)
	}
	updated := allChildren(t, s)
	for _, b := range before {
		var found bool
		for _, a := range updated {
			if a.ID == b.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("ID %d lost", b.ID)
		}
	}
	if e := findEntry(t, updated, "20", ""); e.InvalidEmail != "other" {
		t.Errorf("entry = %+v", e)
	}
	if e := findEntry(t, updated, "10", "a@example.fr"); e.Name != "DUPONT-LEROY" {
		t.Errorf("entry = %+v", e)
	}
}

func TestFamilyEntriesNameKeyUpdated(t *testing.T) {
	s := openTest(t)
	importRows(t, s, []ImportRow{fam("10", "Old", "a@example.fr", "")})
	importRows(t, s, []ImportRow{fam("10", "Éléonore", "a@example.fr", "")})
	var key string
	if err := s.db.QueryRow(`SELECT name_key FROM children`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "eleonore" {
		t.Errorf("name_key = %q", key)
	}
}

func TestFamilyEntriesMoveBetweenKeys(t *testing.T) {
	s := openTest(t)
	importRows(t, s, []ImportRow{fam("10", "DUPONT", "a@example.fr", "")})
	old := findEntry(t, allChildren(t, s), "10", "a@example.fr")

	// New payer email: delete + add, the old parent goes away.
	sum := importRows(t, s, []ImportRow{fam("10", "DUPONT", "b@example.fr", "")})
	if sum.Children != (Changes{Added: 1, Deleted: 1}) || sum.Parents != (Changes{Added: 1, Deleted: 1}) {
		t.Fatalf("summary = %+v / %+v", sum.Children, sum.Parents)
	}
	if len(sum.DeletedChildren) != 1 || sum.DeletedChildren[0] != (DeletedChild{ChildName: "DUPONT", ParentName: "DUPONT", Email: "a@example.fr", FamilyNumber: "10"}) {
		t.Errorf("deleted = %+v", sum.DeletedChildren)
	}
	if e := findEntry(t, allChildren(t, s), "10", "b@example.fr"); e.ID == old.ID {
		t.Error("ID reused for a different key")
	}

	// Valid -> no valid email: delete + add, no parent created.
	sum = importRows(t, s, []ImportRow{fam("10", "DUPONT", "", "bad")})
	if sum.Children != (Changes{Added: 1, Deleted: 1}) || sum.Parents != (Changes{Deleted: 1}) {
		t.Fatalf("summary = %+v / %+v", sum.Children, sum.Parents)
	}
	if tot, _ := s.Totals(context.Background()); tot.Parents != 0 || tot.Children != 1 {
		t.Errorf("totals = %+v", tot)
	}

	// No valid email -> valid: the reverse.
	sum = importRows(t, s, []ImportRow{fam("10", "DUPONT", "c@example.fr", "")})
	if sum.Children != (Changes{Added: 1, Deleted: 1}) || sum.Parents != (Changes{Added: 1}) {
		t.Fatalf("summary = %+v / %+v", sum.Children, sum.Parents)
	}
	if len(sum.DeletedChildren) != 1 || sum.DeletedChildren[0] != (DeletedChild{ChildName: "DUPONT", FamilyNumber: "10"}) {
		t.Errorf("deleted = %+v (no-email entry: empty Email and ParentName)", sum.DeletedChildren)
	}
}

func TestFamilyEntriesNoEmailCountersAndRemoval(t *testing.T) {
	s := openTest(t)
	sum := importRows(t, s, []ImportRow{fam("10", "A", "", ""), fam("20", "B", "", "x")})
	if sum.Parents != (Changes{}) || sum.Children != (Changes{Added: 2}) {
		t.Fatalf("summary = %+v / %+v", sum.Children, sum.Parents)
	}

	// Removed from the file: deleted and listed, no panic.
	sum = importRows(t, s, []ImportRow{fam("10", "A", "", "")})
	if sum.Children != (Changes{Unchanged: 1, Deleted: 1}) || sum.Parents != (Changes{}) {
		t.Fatalf("summary = %+v / %+v", sum.Children, sum.Parents)
	}
	if len(sum.DeletedChildren) != 1 || sum.DeletedChildren[0] != (DeletedChild{ChildName: "B", FamilyNumber: "20"}) {
		t.Errorf("deleted = %+v", sum.DeletedChildren)
	}
}

func TestFamilyValidAndNoEmailEntryCoexist(t *testing.T) {
	s := openTest(t)
	sum := importRows(t, s, []ImportRow{
		fam("10", "DUPONT", "a@example.fr", ""),
		fam("10", "DUPONT", "", "bad"),
	})
	if sum.Children.Added != 2 || sum.Parents.Added != 1 {
		t.Fatalf("summary = %+v / %+v", sum.Children, sum.Parents)
	}
	sum = importRows(t, s, []ImportRow{
		fam("10", "DUPONT", "a@example.fr", ""),
		fam("10", "DUPONT", "", "bad"),
	})
	if sum.Children != (Changes{Unchanged: 2}) {
		t.Errorf("re-import = %+v", sum.Children)
	}
}

func TestFamilyDryRunMatchesRealImport(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	importRows(t, s, []ImportRow{fam("10", "A", "a@example.fr", ""), fam("20", "B", "", "x"), fam("30", "C", "c@example.fr", "")})
	before := allChildren(t, s)

	rows := []ImportRow{fam("10", "A2", "a@example.fr", ""), fam("20", "B", "", ""), fam("40", "D", "", "")}
	dry, err := s.ImportFamilies(ctx, ImportRequest{FileName: "x", RowsRead: 3, Rows: rows, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if dry.ImportID != 0 {
		t.Error("dry run recorded an import")
	}
	after := allChildren(t, s)
	if len(before) != len(after) {
		t.Fatalf("dry run changed data: %+v -> %+v", before, after)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("dry run changed data: %+v -> %+v", before[i], after[i])
		}
	}
	assertImports(t, s, 1)

	real := importRows(t, s, rows)
	if dry.Children != real.Children || dry.Parents != real.Parents || len(dry.DeletedChildren) != len(real.DeletedChildren) {
		t.Errorf("dry %+v vs real %+v", dry, real)
	}
}

func TestImportRowWithoutEmailAndFamilyNumberRejected(t *testing.T) {
	s := openTest(t)
	importRows(t, s, []ImportRow{fam("10", "A", "a@example.fr", "")})

	_, err := s.ImportFamilies(context.Background(), ImportRequest{FileName: "x", Rows: []ImportRow{
		fam("20", "B", "b@example.fr", ""),
		{ParentName: "X", ChildName: "Y"},
	}})
	if err == nil {
		t.Fatal("want error")
	}
	cs := allChildren(t, s)
	if len(cs) != 1 || cs[0].FamilyNumber != "10" {
		t.Errorf("nothing must be saved: %+v", cs)
	}
	assertImports(t, s, 1)
}

func TestSwitchBetweenExcelAndFamilyLayouts(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	importRows(t, s, []ImportRow{
		{ParentName: "Marie", ChildName: "Léa", Email: "shared@example.fr"},
		{ParentName: "Marie", ChildName: "Tom", Email: "shared@example.fr"},
		{ParentName: "Paul", ChildName: "Zoé", Email: "paul@example.fr"},
	})
	var sharedID int64
	if err := s.db.QueryRow(`SELECT id FROM parents WHERE email = 'shared@example.fr'`).Scan(&sharedID); err != nil {
		t.Fatal(err)
	}

	// Excel -> families.
	sum := importRows(t, s, []ImportRow{
		fam("10", "DUPONT", "shared@example.fr", ""),
		fam("20", "NOMAIL", "", ""),
	})
	if sum.Children != (Changes{Added: 2, Deleted: 3}) {
		t.Errorf("children = %+v", sum.Children)
	}
	if sum.Parents != (Changes{Updated: 1, Deleted: 1}) {
		t.Errorf("parents = %+v", sum.Parents)
	}
	var afterID int64
	if err := s.db.QueryRow(`SELECT id FROM parents WHERE email = 'shared@example.fr'`).Scan(&afterID); err != nil || afterID != sharedID {
		t.Errorf("shared email parent ID %d -> %d (%v)", sharedID, afterID, err)
	}
	tot, _ := s.Totals(ctx)
	if tot.Children != 2 || tot.Families != 2 || tot.Parents != 1 {
		t.Errorf("totals = %+v", tot)
	}

	// Families -> Excel.
	sum = importRows(t, s, []ImportRow{{ParentName: "Marie", ChildName: "Léa", Email: "shared@example.fr"}})
	if sum.Children != (Changes{Added: 1, Deleted: 2}) || sum.Parents.Unchanged+sum.Parents.Updated != 1 {
		t.Errorf("summary = %+v / %+v", sum.Children, sum.Parents)
	}
	tot, _ = s.Totals(ctx)
	if tot.Children != 1 || tot.Families != 0 {
		t.Errorf("totals = %+v", tot)
	}
	if err := s.db.QueryRow(`SELECT id FROM parents WHERE email = 'shared@example.fr'`).Scan(&afterID); err != nil || afterID != sharedID {
		t.Errorf("parent ID changed: %d", afterID)
	}
}

func TestInvalidEmailStoredAsGiven(t *testing.T) {
	s := openTest(t)
	long := strings.Repeat("é", 253) + "…" // what the importer produces for a cut value
	importRows(t, s, []ImportRow{fam("10", "A", "", long)})
	cs := allChildren(t, s)
	if len(cs) != 1 || cs[0].InvalidEmail != long || !utf8.ValidString(cs[0].InvalidEmail) {
		t.Fatalf("children = %+v", cs)
	}
}

func TestChildrenOrderFamilies(t *testing.T) {
	s := openTest(t)
	importRows(t, s, []ImportRow{
		fam("10", "ZOLA Anne", "z@example.fr", ""),
		fam("20", "BERT", "bert@example.fr", ""),
		fam("10", "ADAM Paul", "a@example.fr", ""),
		fam("10", "ZOLA Anne", "", "bad"),
		fam("30", "CARON", "", ""),
	})
	cs := allChildren(t, s)
	var got []string
	for _, c := range cs {
		got = append(got, c.FamilyNumber+":"+c.Name+":"+c.Email)
	}
	want := []string{
		"10:ADAM Paul:a@example.fr", // family 10 sorts by its smallest name (adam)
		"10:ZOLA Anne:z@example.fr", // valid before no-email
		"10:ZOLA Anne:",             // no-email entry right after its family's valid rows
		"20:BERT:bert@example.fr",
		"30:CARON:",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("order:\n got %v\nwant %v", got, want)
	}

	// ChildrenByIDs returns no-email entries too.
	ids := []int64{cs[2].ID, cs[4].ID}
	sel, err := s.ChildrenByIDs(context.Background(), ids)
	if err != nil || len(sel) != 2 || sel[0].CanEmail() || sel[1].CanEmail() {
		t.Fatalf("selected = %+v, %v", sel, err)
	}
	// The window only sees the selected rows, so check by content.
	for _, c := range sel {
		if c.FamilyNumber == "10" && (c.InvalidEmail != "bad" || c.Name != "ZOLA Anne") {
			t.Errorf("entry = %+v", c)
		}
	}
}

func TestChildrenOrderExcelUnchangedWithFamilyColumns(t *testing.T) {
	s := openTest(t)
	importRows(t, s, []ImportRow{
		{ParentName: "P", ChildName: "Zoé", Email: "p@example.fr"},
		{ParentName: "P", ChildName: "Émile", Email: "p@example.fr"},
	})
	cs := allChildren(t, s)
	if cs[0].Name != "Émile" || cs[1].Name != "Zoé" || cs[0].FamilyNumber != "" || cs[0].InvalidEmail != "" {
		t.Errorf("children = %+v", cs)
	}
}

func TestTotalsFamilies(t *testing.T) {
	s := openTest(t)
	importRows(t, s, []ImportRow{
		fam("10", "A", "a@example.fr", ""),
		fam("10", "A", "b@example.fr", ""),
		fam("10", "A", "", "x"),
		fam("11", "B", "a@example.fr", ""),
	})
	tot, err := s.Totals(context.Background())
	if err != nil || tot.Families != 2 || tot.Children != 4 || tot.Parents != 2 {
		t.Fatalf("totals = %+v, %v", tot, err)
	}
}

func TestChildrenTableConstraints(t *testing.T) {
	s := openTest(t)
	exec := func(q string, args ...any) error {
		_, err := s.db.Exec(q, args...)
		return err
	}
	if err := exec(`INSERT INTO parents (id, name, email, created_at, updated_at) VALUES (1, 'P', 'p@example.fr', 't', 't')`); err != nil {
		t.Fatal(err)
	}
	ins := `INSERT INTO children (parent_id, family_number, name, name_key, invalid_email, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 't', 't')`

	// Valid rows first.
	for _, args := range [][]any{
		{1, "", "Léa", "lea", ""},
		{1, "10", "FAM", "fam", ""},
		{nil, "10", "FAM", "fam", "bad"},
	} {
		if err := exec(ins, args...); err != nil {
			t.Fatalf("valid insert %v: %v", args, err)
		}
	}

	bad := map[string][]any{
		"duplicate Excel (parent, name_key)": {1, "", "LÉA", "lea", ""},
		"duplicate family (parent, number)":  {1, "10", "Other", "other", ""},
		"two no-email entries, one number":   {nil, "10", "Other", "other", ""},
		"parent NULL with empty number":      {nil, "", "X", "x", ""},
		"invalid_email set with a parent":    {1, "11", "X", "x", "bad"},
	}
	for name, args := range bad {
		t.Run(name, func(t *testing.T) {
			if err := exec(ins, args...); err == nil {
				t.Fatal("insert accepted")
			}
		})
	}
	// Same family number on another parent is fine.
	if err := exec(`INSERT INTO parents (id, name, email, created_at, updated_at) VALUES (2, 'Q', 'q@example.fr', 't', 't')`); err != nil {
		t.Fatal(err)
	}
	if err := exec(ins, 2, "10", "FAM", "fam", ""); err != nil {
		t.Errorf("same family on another parent rejected: %v", err)
	}
}
