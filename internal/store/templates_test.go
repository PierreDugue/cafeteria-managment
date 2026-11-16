package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestMigration003KeepsExistingData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")

	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`}
	for i, f := range []string{"migrations/001_families.sql", "migrations/002_family_entries.sql"} {
		body, err := migrationsFS.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		stmts = append(stmts, string(body), `INSERT INTO schema_migrations VALUES (`+string(rune('1'+i))+`, '2026-01-01T00:00:00Z')`)
	}
	stmts = append(stmts,
		`INSERT INTO parents VALUES (5, 'Marie Dupont', 'marie@example.fr', 't', 't')`,
		`INSERT INTO children (id, parent_id, name, name_key, created_at, updated_at) VALUES (7, 5, 'Léa', 'lea', 't', 't')`,
	)
	for _, q := range stmts {
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
	if len(cs) != 1 || cs[0].ID != 7 || cs[0].Name != "Léa" || cs[0].Email != "marie@example.fr" {
		t.Fatalf("children = %+v", cs)
	}
	var version int
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version < 3 {
		t.Fatalf("version = %d, %v", version, err)
	}
	ts, err := s.Templates(ctx)
	if err != nil || len(ts) != 0 {
		t.Fatalf("templates = %+v, %v", ts, err)
	}
	if _, err := s.CreateTemplate(ctx, "Menu", "s", "b"); err != nil {
		t.Fatal(err)
	}
}

func TestFreshDatabaseHasTemplatesTable(t *testing.T) {
	s := openTest(t)
	ts, err := s.Templates(context.Background())
	if err != nil || ts == nil || len(ts) != 0 {
		t.Fatalf("templates = %#v, %v", ts, err)
	}
}

func TestTemplatesCRUD(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return t0 }

	id, err := s.CreateTemplate(ctx, "  Rappel   cantine ", "Objet {{child_name}}", "Corps\n{{children}}")
	if err != nil || id <= 0 {
		t.Fatalf("create: %d, %v", id, err)
	}
	got, err := s.Template(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.Name != "Rappel cantine" || got.Subject != "Objet {{child_name}}" || got.Body != "Corps\n{{children}}" {
		t.Fatalf("template = %+v", got)
	}
	if !got.CreatedAt.Equal(t0) || !got.UpdatedAt.Equal(t0) {
		t.Fatalf("timestamps = %v %v", got.CreatedAt, got.UpdatedAt)
	}

	t1 := t0.Add(time.Hour)
	s.now = func() time.Time { return t1 }
	if err := s.UpdateTemplate(ctx, id, "Rappel 2", "S2", "B2"); err != nil {
		t.Fatal(err)
	}
	got, err = s.Template(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Rappel 2" || got.Subject != "S2" || got.Body != "B2" {
		t.Fatalf("after update = %+v", got)
	}
	if !got.CreatedAt.Equal(t0) || !got.UpdatedAt.Equal(t1) {
		t.Fatalf("timestamps after update = %v %v", got.CreatedAt, got.UpdatedAt)
	}

	if err := s.DeleteTemplate(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Template(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestTemplatesOrder(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	for _, n := range []string{"c", "b", "Á"} {
		if _, err := s.CreateTemplate(ctx, n, "s", "b"); err != nil {
			t.Fatal(err)
		}
	}
	ts, err := s.Templates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, x := range ts {
		names = append(names, x.Name)
	}
	if !slices.Equal(names, []string{"Á", "b", "c"}) {
		t.Fatalf("order = %v", names)
	}
}

func TestTemplateNameUniqueness(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	id, err := s.CreateTemplate(ctx, "Menu", "s", "b")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"Menu", " menu ", "Ménu", "MÉNU", "  Ménu\t"} {
		if _, err := s.CreateTemplate(ctx, n, "s", "b"); !errors.Is(err, ErrNameTaken) {
			t.Errorf("create %q: err = %v, want ErrNameTaken", n, err)
		}
	}
	other, err := s.CreateTemplate(ctx, "Autre", "s", "b")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTemplate(ctx, other, " ménu", "s", "b"); !errors.Is(err, ErrNameTaken) {
		t.Errorf("update to a taken name: %v", err)
	}
	// The failed update left the row alone.
	if got, _ := s.Template(ctx, other); got.Name != "Autre" {
		t.Errorf("name = %q", got.Name)
	}
	// Keeping or re-casing its own name is fine.
	for _, n := range []string{"Menu", "MENU", " Ménu "} {
		if err := s.UpdateTemplate(ctx, id, n, "s2", "b2"); err != nil {
			t.Errorf("update own name %q: %v", n, err)
		}
	}
	if ts, _ := s.Templates(ctx); len(ts) != 2 {
		t.Errorf("templates = %+v", ts)
	}
	// A name freed by delete can be reused.
	if err := s.DeleteTemplate(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTemplate(ctx, "menu", "s", "b"); err != nil {
		t.Errorf("reuse after delete: %v", err)
	}
}

func TestTemplateNotFound(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	for _, id := range []int64{0, -1, 999} {
		if _, err := s.Template(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Template(%d): %v", id, err)
		}
		if err := s.UpdateTemplate(ctx, id, "n", "s", "b"); !errors.Is(err, ErrNotFound) {
			t.Errorf("Update(%d): %v", id, err)
		}
		if err := s.DeleteTemplate(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Delete(%d): %v", id, err)
		}
	}
	// Updating a missing template must not create a row.
	if ts, _ := s.Templates(ctx); len(ts) != 0 {
		t.Errorf("templates = %+v", ts)
	}
}

func TestTemplatesClosedStore(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	s.Close()
	if _, err := s.Templates(ctx); err == nil {
		t.Error("Templates: want error")
	}
	if _, err := s.Template(ctx, 1); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("Template: err = %v", err)
	}
	if _, err := s.CreateTemplate(ctx, "a", "s", "b"); err == nil {
		t.Error("Create: want error")
	}
	if err := s.UpdateTemplate(ctx, 1, "a", "s", "b"); err == nil {
		t.Error("Update: want error")
	}
	if err := s.DeleteTemplate(ctx, 1); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("Delete: err = %v", err)
	}
}

func TestImportsLeaveTemplatesUntouched(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	id, err := s.CreateTemplate(ctx, "Menu", "s", "b {{children}}")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.Template(ctx, id)

	importRows(t, s, []ImportRow{{ParentName: "Marie", ChildName: "Léa", Email: "m@example.fr"}})
	importRows(t, s, []ImportRow{fam("10", "DUPONT", "d@example.fr", "")})
	importRows(t, s, nil)

	after, err := s.Template(ctx, id)
	if err != nil || after != before {
		t.Fatalf("template changed: %+v -> %+v, %v", before, after, err)
	}
}

func TestDataVersion(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	version := func() int64 {
		t.Helper()
		v, err := s.DataVersion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := version(); v != 0 {
		t.Fatalf("empty db version = %d", v)
	}
	rows := []ImportRow{{ParentName: "Marie", ChildName: "Léa", Email: "m@example.fr"}}

	// A preview is rolled back.
	if _, err := s.ImportFamilies(ctx, ImportRequest{FileName: "a.xlsx", RowsRead: 1, Rows: rows, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if v := version(); v != 0 {
		t.Fatalf("version after preview = %d", v)
	}

	sum := importRows(t, s, rows)
	if v := version(); v != sum.ImportID || v == 0 {
		t.Fatalf("version = %d, import ID %d", v, sum.ImportID)
	}
	first := version()
	sum = importRows(t, s, append(rows, ImportRow{ParentName: "Paul", ChildName: "Tom", Email: "p@example.fr"}))
	if v := version(); v != sum.ImportID || v <= first {
		t.Fatalf("version = %d, previous %d", v, first)
	}

	s.Close()
	if _, err := s.DataVersion(ctx); err == nil {
		t.Error("closed store: want error")
	}
}

func TestSelectedChildren(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	seedChildren(t, s, childrenRows)
	version, err := s.DataVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	all, err := s.Children(ctx)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("current version gives the same rows as ChildrenByIDs", func(t *testing.T) {
		ids := []int64{all[4].ID, all[1].ID, 9999, -1, 0}
		got, err := s.SelectedChildren(ctx, version, ids)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := s.ChildrenByIDs(ctx, ids)
		if !slices.Equal(got, want) || len(got) != 2 {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})

	t.Run("empty ids with the right version", func(t *testing.T) {
		for _, ids := range [][]int64{nil, {}} {
			got, err := s.SelectedChildren(ctx, version, ids)
			if err != nil || got == nil || len(got) != 0 {
				t.Fatalf("got %#v, %v", got, err)
			}
		}
	})

	t.Run("older, newer and negative versions are stale", func(t *testing.T) {
		for _, v := range []int64{version - 1, version + 1, 0, -1} {
			if _, err := s.SelectedChildren(ctx, v, []int64{all[0].ID}); !errors.Is(err, ErrStaleSelection) {
				t.Errorf("version %d: err = %v", v, err)
			}
		}
	})

	t.Run("empty ids with the wrong version", func(t *testing.T) {
		if _, err := s.SelectedChildren(ctx, version+1, nil); !errors.Is(err, ErrStaleSelection) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("an import makes the old version stale", func(t *testing.T) {
		s := openTest(t)
		seedChildren(t, s, childrenRows)
		v, _ := s.DataVersion(ctx)
		cs, _ := s.Children(ctx)
		seedChildren(t, s, childrenRows[:1]) // same file shape, new import
		if _, err := s.SelectedChildren(ctx, v, []int64{cs[0].ID}); !errors.Is(err, ErrStaleSelection) {
			t.Fatalf("err = %v", err)
		}
		v2, _ := s.DataVersion(ctx)
		if _, err := s.SelectedChildren(ctx, v2, []int64{cs[0].ID}); err != nil {
			t.Fatalf("new version: %v", err)
		}
	})

	t.Run("entries without email are returned for the caller to refuse", func(t *testing.T) {
		s := openTest(t)
		importRows(t, s, []ImportRow{fam("10", "DUPONT", "d@example.fr", ""), fam("20", "NOMAIL", "", "bad@")})
		v, _ := s.DataVersion(ctx)
		cs, _ := s.Children(ctx)
		ids := []int64{}
		for _, c := range cs {
			ids = append(ids, c.ID)
		}
		got, err := s.SelectedChildren(ctx, v, ids)
		if err != nil || len(got) != 2 {
			t.Fatalf("got %+v, %v", got, err)
		}
		noMail := 0
		for _, c := range got {
			if !c.CanEmail() {
				noMail++
			}
		}
		if noMail != 1 {
			t.Errorf("entries without email = %d", noMail)
		}
	})

	t.Run("many ids", func(t *testing.T) {
		ids := make([]int64, 5000)
		for i := range ids {
			ids[i] = int64(i + 1)
		}
		got, err := s.SelectedChildren(ctx, version, ids)
		if err != nil || len(got) != len(all) {
			t.Fatalf("got %d, %v", len(got), err)
		}
	})

	t.Run("closed store", func(t *testing.T) {
		c := openTest(t)
		c.Close()
		if _, err := c.SelectedChildren(ctx, 0, []int64{1}); err == nil || errors.Is(err, ErrStaleSelection) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestImportFamiliesStaleStillWorksWithSharedVersionRead(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	rows := []ImportRow{{ParentName: "Marie", ChildName: "Léa", Email: "m@example.fr"}}
	preview, err := s.ImportFamilies(ctx, ImportRequest{FileName: "a.xlsx", RowsRead: 1, Rows: rows, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	importRows(t, s, rows) // someone else imports in between
	if _, err := s.ImportFamilies(ctx, ImportRequest{FileName: "a.xlsx", RowsRead: 1, Rows: rows, Base: &preview.Base}); !errors.Is(err, ErrStaleImport) {
		t.Fatalf("err = %v", err)
	}
}
