package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

// entryID returns the ID of the entry called name.
func entryID(t *testing.T, s *Store, name string) int64 {
	t.Helper()
	for _, c := range allChildren(t, s) {
		if c.Name == name {
			return c.ID
		}
	}
	t.Fatalf("no entry named %q", name)
	return 0
}

func savedIDs(t *testing.T, s *Store, listID int64) []int64 {
	t.Helper()
	m, err := s.SavedSelection(context.Background(), listID)
	if err != nil {
		t.Fatal(err)
	}
	out := []int64{}
	for id, ok := range m {
		if !ok {
			t.Errorf("saved map holds a false value for %d", id)
		}
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func sortedIDs(ids ...int64) []int64 {
	slices.Sort(ids)
	return ids
}

func TestSavedSelectionEmptyDatabase(t *testing.T) {
	m, err := openTest(t).SavedSelection(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || len(m) != 0 {
		t.Fatalf("got %#v, want empty non-nil map", m)
	}
}

func TestSaveSelection(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (*Store, int64, int64, int64) {
		s := openTest(t)
		seedChildren(t, s, childrenRows)
		return s, entryID(t, s, "Zoé"), entryID(t, s, "Émile"), entryID(t, s, "Alice")
	}

	t.Run("saves and replaces the previous set", func(t *testing.T) {
		s, zoe, emile, alice := setup(t)
		if err := s.SaveSelection(ctx, mainList(t, s), []int64{zoe, emile}); err != nil {
			t.Fatal(err)
		}
		if got, want := savedIDs(t, s, mainList(t, s)), sortedIDs(zoe, emile); !slices.Equal(got, want) {
			t.Fatalf("saved = %v, want %v", got, want)
		}
		if err := s.SaveSelection(ctx, mainList(t, s), []int64{alice, zoe}); err != nil {
			t.Fatal(err)
		}
		if got, want := savedIDs(t, s, mainList(t, s)), sortedIDs(alice, zoe); !slices.Equal(got, want) {
			t.Fatalf("saved = %v, want %v (Émile must be gone)", got, want)
		}
	})

	t.Run("saving twice is idempotent and duplicates are fine", func(t *testing.T) {
		s, zoe, _, _ := setup(t)
		// Duplicate IDs in one call must not violate the primary key.
		for i := 0; i < 2; i++ {
			if err := s.SaveSelection(ctx, mainList(t, s), []int64{zoe, zoe}); err != nil {
				t.Fatalf("call %d: %v", i, err)
			}
		}
		if got := savedIDs(t, s, mainList(t, s)); !slices.Equal(got, []int64{zoe}) {
			t.Fatalf("saved = %v", got)
		}
	})

	t.Run("unknown and non positive IDs are ignored", func(t *testing.T) {
		s, zoe, _, _ := setup(t)
		if err := s.SaveSelection(ctx, mainList(t, s), []int64{zoe, 424242, 0, -3}); err != nil {
			t.Fatal(err)
		}
		if got := savedIDs(t, s, mainList(t, s)); !slices.Equal(got, []int64{zoe}) {
			t.Fatalf("saved = %v", got)
		}
	})

	t.Run("only unknown IDs clear the previous set", func(t *testing.T) {
		s, zoe, _, _ := setup(t)
		if err := s.SaveSelection(ctx, mainList(t, s), []int64{zoe}); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveSelection(ctx, mainList(t, s), []int64{424242}); err != nil {
			t.Fatal(err)
		}
		if got := savedIDs(t, s, mainList(t, s)); len(got) != 0 {
			t.Fatalf("saved = %v", got)
		}
	})

	t.Run("empty and nil slices clear", func(t *testing.T) {
		for name, ids := range map[string][]int64{"empty": {}, "nil": nil} {
			t.Run(name, func(t *testing.T) {
				s, zoe, emile, _ := setup(t)
				if err := s.SaveSelection(ctx, mainList(t, s), []int64{zoe, emile}); err != nil {
					t.Fatal(err)
				}
				if err := s.SaveSelection(ctx, mainList(t, s), ids); err != nil {
					t.Fatal(err)
				}
				if got := savedIDs(t, s, mainList(t, s)); len(got) != 0 {
					t.Fatalf("saved = %v", got)
				}
			})
		}
	})

	t.Run("saving for a list that does not exist", func(t *testing.T) {
		s := openTest(t)
		if err := s.SaveSelection(ctx, 1, []int64{1, 2}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if got := savedIDs(t, s, mainList(t, s)); len(got) != 0 {
			t.Fatalf("saved = %v", got)
		}
	})

	t.Run("entries without an email are ignored", func(t *testing.T) {
		s := openTest(t)
		importRows(t, s, []ImportRow{fam("10", "DUPONT", "d@example.fr", ""), fam("20", "NOMAIL", "", ""), fam("30", "BADMAIL", "", "oops")})
		ok, noMail, bad := entryID(t, s, "DUPONT"), entryID(t, s, "NOMAIL"), entryID(t, s, "BADMAIL")
		if err := s.SaveSelection(ctx, mainList(t, s), []int64{ok, noMail, bad}); err != nil {
			t.Fatal(err)
		}
		if got := savedIDs(t, s, mainList(t, s)); !slices.Equal(got, []int64{ok}) {
			t.Fatalf("saved = %v, want only %d", got, ok)
		}
	})

	t.Run("many IDs", func(t *testing.T) {
		s, zoe, _, _ := setup(t)
		ids := make([]int64, 0, 3000)
		for i := int64(1000); i < 4000; i++ {
			ids = append(ids, i)
		}
		ids = append(ids, zoe)
		if err := s.SaveSelection(ctx, mainList(t, s), ids); err != nil {
			t.Fatal(err)
		}
		if got := savedIDs(t, s, mainList(t, s)); !slices.Equal(got, []int64{zoe}) {
			t.Fatalf("saved = %v", got)
		}
	})

	t.Run("canceled context changes nothing", func(t *testing.T) {
		s, zoe, emile, _ := setup(t)
		if err := s.SaveSelection(ctx, mainList(t, s), []int64{zoe}); err != nil {
			t.Fatal(err)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if err := s.SaveSelection(canceled, mainList(t, s), []int64{emile}); err == nil {
			t.Fatal("want an error")
		}
		if got := savedIDs(t, s, mainList(t, s)); !slices.Equal(got, []int64{zoe}) {
			t.Fatalf("saved = %v, want the previous set", got)
		}
	})

	t.Run("closed store", func(t *testing.T) {
		s, zoe, _, _ := setup(t)
		lid := mainList(t, s)
		s.Close()
		if err := s.SaveSelection(ctx, lid, []int64{zoe}); err == nil {
			t.Error("SaveSelection: want an error")
		}
		if _, err := s.SavedSelection(ctx, lid); err == nil {
			t.Error("SavedSelection: want an error")
		}
	})
}

func TestSavedSelectionFollowsImports(t *testing.T) {
	ctx := context.Background()

	t.Run("deleted entry loses its saved row and a reused ID is not selected", func(t *testing.T) {
		s := openTest(t)
		seedChildren(t, s, []ImportRow{
			{ParentName: "Anne Martin", ChildName: "Alice", Email: "anne@example.com"},
			{ParentName: "Bob Durand", ChildName: "Bastien", Email: "bob@example.com"},
		})
		alice, bastien := entryID(t, s, "Alice"), entryID(t, s, "Bastien")
		if err := s.SaveSelection(ctx, mainList(t, s), []int64{alice, bastien}); err != nil {
			t.Fatal(err)
		}

		// Bastien (the highest ID) leaves the file, Chloé arrives.
		seedChildren(t, s, []ImportRow{
			{ParentName: "Anne Martin", ChildName: "Alice", Email: "anne@example.com"},
			{ParentName: "Carl Petit", ChildName: "Chloé", Email: "carl@example.com"},
		})
		if got := savedIDs(t, s, mainList(t, s)); !slices.Equal(got, []int64{alice}) {
			t.Fatalf("saved = %v, want only Alice %d (kept entry stays saved)", got, alice)
		}
		chloe := entryID(t, s, "Chloé")
		if chloe == bastien {
			t.Logf("ID %d was reused by the new entry", chloe)
		}
		m, _ := s.SavedSelection(ctx, mainList(t, s))
		if m[chloe] || m[bastien] {
			t.Errorf("new entry %d or deleted entry %d is pre-selected: %v", chloe, bastien, m)
		}
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM last_selection WHERE child_id NOT IN (SELECT id FROM children)`).Scan(&n); err != nil || n != 0 {
			t.Errorf("orphan saved rows = %d, %v", n, err)
		}
	})

	t.Run("removing the parent removes the saved entry", func(t *testing.T) {
		s := openTest(t)
		seedChildren(t, s, childrenRows)
		zoe := entryID(t, s, "Zoé")
		if err := s.SaveSelection(ctx, mainList(t, s), []int64{zoe}); err != nil {
			t.Fatal(err)
		}
		seedChildren(t, s, []ImportRow{{ParentName: "Anne Martin", ChildName: "Alice", Email: "anne@example.com"}})
		if got := savedIDs(t, s, mainList(t, s)); len(got) != 0 {
			t.Fatalf("saved = %v", got)
		}
	})

	t.Run("an import that keeps everything keeps the selection", func(t *testing.T) {
		s := openTest(t)
		seedChildren(t, s, childrenRows)
		zoe, emile := entryID(t, s, "Zoé"), entryID(t, s, "Émile")
		if err := s.SaveSelection(ctx, mainList(t, s), []int64{zoe, emile}); err != nil {
			t.Fatal(err)
		}
		seedChildren(t, s, childrenRows)
		if got, want := savedIDs(t, s, mainList(t, s)), sortedIDs(zoe, emile); !slices.Equal(got, want) {
			t.Fatalf("saved = %v, want %v", got, want)
		}
	})

	t.Run("family entries removed from the file are dropped", func(t *testing.T) {
		s := openTest(t)
		importRows(t, s, []ImportRow{fam("10", "DUPONT", "d@example.fr", ""), fam("20", "MARTIN", "m@example.fr", "")})
		d, m := entryID(t, s, "DUPONT"), entryID(t, s, "MARTIN")
		if err := s.SaveSelection(ctx, mainList(t, s), []int64{d, m}); err != nil {
			t.Fatal(err)
		}
		importRows(t, s, []ImportRow{fam("10", "DUPONT", "d@example.fr", "")})
		if got := savedIDs(t, s, mainList(t, s)); !slices.Equal(got, []int64{d}) {
			t.Fatalf("saved = %v, want %v", got, []int64{d})
		}
	})
}

func TestMigration004KeepsExistingData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")

	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`}
	for i, f := range []string{"migrations/001_families.sql", "migrations/002_family_entries.sql", "migrations/003_templates.sql"} {
		body, err := migrationsFS.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		stmts = append(stmts, string(body), `INSERT INTO schema_migrations VALUES (`+string(rune('1'+i))+`, '2026-01-01T00:00:00Z')`)
	}
	stmts = append(stmts,
		`INSERT INTO parents VALUES (5, 'Marie Dupont', 'marie@example.fr', 't', 't')`,
		`INSERT INTO children (id, parent_id, name, name_key, created_at, updated_at) VALUES (7, 5, 'Léa', 'lea', 't', 't')`,
		`INSERT INTO templates (name, name_key, subject, body, created_at, updated_at) VALUES ('Menu', 'menu', 'Objet', 'Corps', 't', 't')`,
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

	var version int
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version < 4 {
		t.Fatalf("version = %d, %v", version, err)
	}
	if got := savedIDs(t, s, mainList(t, s)); len(got) != 0 {
		t.Fatalf("saved = %v, want none after migrating", got)
	}
	if cs := allChildren(t, s); len(cs) != 1 || cs[0].ID != 7 || cs[0].Email != "marie@example.fr" {
		t.Fatalf("children = %+v", cs)
	}
	if ts, err := s.Templates(ctx); err != nil || len(ts) != 1 || ts[0].Name != "Menu" {
		t.Fatalf("templates = %+v, %v", ts, err)
	}
	if err := s.SaveSelection(ctx, mainList(t, s), []int64{7}); err != nil {
		t.Fatal(err)
	}
	if got := savedIDs(t, s, mainList(t, s)); !slices.Equal(got, []int64{7}) {
		t.Fatalf("saved = %v", got)
	}
}

func TestFreshDatabaseHasLastSelectionTable(t *testing.T) {
	s := openTest(t)
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM last_selection`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("count = %d, %v", n, err)
	}
	// Reopening applies nothing twice.
	var v int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 4`).Scan(&v); err != nil || v != 1 {
		t.Fatalf("migration 4 rows = %d, %v", v, err)
	}
}
