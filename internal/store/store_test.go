package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"cafeteria-management/internal/textnorm"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "sub dir", "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	for range 2 {
		s, err := Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
}

func TestImportFamilies(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	first := []ImportRow{
		{ParentName: "Marie Dupont", ChildName: "Léa Dupont", Email: "marie@example.fr"},
		{ParentName: "Marie Dupont", ChildName: "Tom Dupont", Email: "marie@example.fr"},
		{ParentName: "Paul Martin", ChildName: "Zoé Martin", Email: "paul@example.fr"},
	}

	// A dry run changes nothing.
	sum, err := s.ImportFamilies(ctx, into(t, s, ImportRequest{FileName: "a.xlsx", RowsRead: 3, Rows: first, DryRun: true}))
	if err != nil {
		t.Fatal(err)
	}
	if sum.Parents.Added != 2 || sum.Children.Added != 3 || sum.ImportID != 0 {
		t.Fatalf("dry run summary = %+v", sum)
	}
	assertTotals(t, s, 0, 0)
	assertImports(t, s, 0)

	sum, err = s.ImportFamilies(ctx, into(t, s, ImportRequest{FileName: "a.xlsx", RowsRead: 3, Rows: first}))
	if err != nil {
		t.Fatal(err)
	}
	if sum.Parents.Added != 2 || sum.Children.Added != 3 || sum.ImportID == 0 {
		t.Fatalf("summary = %+v", sum)
	}
	assertTotals(t, s, 2, 3)

	// Second file: Marie renamed, Léa's name written differently, Tom gone,
	// Paul gone, a new family added.
	second := []ImportRow{
		{ParentName: "Marie Dupont-Leroy", ChildName: "LEA DUPONT", Email: "marie@example.fr"},
		{ParentName: "Ana Silva", ChildName: "Rui Silva", Email: "ana@example.fr"},
	}
	preview, err := s.ImportFamilies(ctx, into(t, s, ImportRequest{FileName: "b.xlsx", RowsRead: 2, Rows: second, DryRun: true}))
	if err != nil {
		t.Fatal(err)
	}
	sum, err = s.ImportFamilies(ctx, into(t, s, ImportRequest{FileName: "b.xlsx", RowsRead: 2, Rows: second, Base: &preview.Base}))
	if err != nil {
		t.Fatal(err)
	}

	// The same preview cannot be confirmed twice: the data has changed.
	if _, err := s.ImportFamilies(ctx, into(t, s, ImportRequest{FileName: "b.xlsx", RowsRead: 2, Rows: second, Base: &preview.Base})); !errors.Is(err, ErrStaleImport) {
		t.Fatalf("stale import: err = %v", err)
	}

	wantParents := Changes{Added: 1, Updated: 1, Deleted: 1}
	wantChildren := Changes{Added: 1, Updated: 1, Deleted: 2}
	if sum.Parents != wantParents || sum.Children != wantChildren {
		t.Fatalf("parents = %+v, children = %+v", sum.Parents, sum.Children)
	}
	if preview.Parents != sum.Parents || preview.Children != sum.Children {
		t.Errorf("preview %+v differs from import %+v", preview, sum)
	}

	want := []DeletedChild{
		// Marie is renamed in the same import: her new name is shown.
		{ChildName: "Tom Dupont", ParentName: "Marie Dupont-Leroy", Email: "marie@example.fr"},
		{ChildName: "Zoé Martin", ParentName: "Paul Martin", Email: "paul@example.fr"},
	}
	if len(sum.DeletedChildren) != len(want) {
		t.Fatalf("deleted = %+v", sum.DeletedChildren)
	}
	for i := range want {
		if sum.DeletedChildren[i] != want[i] {
			t.Errorf("deleted[%d] = %+v, want %+v", i, sum.DeletedChildren[i], want[i])
		}
	}
	assertTotals(t, s, 2, 2)

	// Importing the same file again changes nothing.
	sum, err = s.ImportFamilies(ctx, into(t, s, ImportRequest{FileName: "b.xlsx", RowsRead: 2, Rows: second}))
	if err != nil {
		t.Fatal(err)
	}
	if sum.Parents != (Changes{Unchanged: 2}) || sum.Children != (Changes{Unchanged: 2}) {
		t.Errorf("re-import summary = %+v", sum)
	}

	imports, err := s.Imports(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(imports) != 3 || imports[0].FileName != "b.xlsx" || imports[2].FileName != "a.xlsx" {
		t.Fatalf("imports = %+v", imports)
	}
	if imports[1].Children.Deleted != 2 || imports[1].ImportedAt.IsZero() {
		t.Errorf("import record = %+v", imports[1])
	}
}

// mainList returns the ID of the first list (by name), or 0 if there is none.
func mainList(t *testing.T, s *Store) int64 {
	t.Helper()
	ls, err := s.Lists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) == 0 {
		return 0
	}
	return ls[0].ID
}

// into aims req at the first list, or at a new list "Cantine" if there is
// none yet, so single-list tests keep reading like one-table tests.
func into(t *testing.T, s *Store, req ImportRequest) ImportRequest {
	t.Helper()
	if id := mainList(t, s); id != 0 {
		req.ListID = id
	} else {
		req.NewListName = "Cantine"
	}
	return req
}

// rawList inserts a list row directly and returns its ID.
func rawList(t *testing.T, s *Store, name string) int64 {
	t.Helper()
	res, err := s.db.Exec(`INSERT INTO lists (name, name_key, created_at, updated_at, used_at) VALUES (?, ?, 't', 't', 't')`,
		name, textnorm.Key(name))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// totals returns the counts of the first list (zero value if none).
func totals(t *testing.T, s *Store) List {
	t.Helper()
	l, err := s.List(context.Background(), mainList(t, s))
	if err != nil && !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	return l
}

// assertTotals checks the counts summed over all lists.
func assertTotals(t *testing.T, s *Store, parents, children int) {
	t.Helper()
	ls, err := s.Lists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var p, c int
	for _, l := range ls {
		p, c = p+l.Parents, c+l.Children
	}
	if p != parents || c != children {
		t.Fatalf("totals = %d parents, %d children, want %d, %d", p, c, parents, children)
	}
}

func assertImports(t *testing.T, s *Store, n int) {
	t.Helper()
	got, err := s.Imports(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("imports = %d, want %d", len(got), n)
	}
}
