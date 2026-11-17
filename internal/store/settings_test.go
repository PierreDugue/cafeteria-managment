package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestSettingsDefaultsOnFreshDatabase(t *testing.T) {
	s := openTest(t)
	got, err := s.Settings(context.Background())
	if err != nil || got != (Settings{}) {
		t.Fatalf("Settings = %+v, %v; want zero value", got, err)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	for _, lang := range []string{"fr", "en", "de", "FR", ""} {
		t.Run("language "+lang, func(t *testing.T) {
			s := openTest(t)
			if err := s.SaveSettings(ctx, Settings{Language: lang}); err != nil {
				t.Fatal(err)
			}
			got, err := s.Settings(ctx)
			if err != nil || got.Language != lang {
				t.Fatalf("Settings = %+v, %v; want %q", got, err, lang)
			}
		})
	}
}

func TestSaveSettingsUpsertKeepsOneRow(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	t1 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	s.now = func() time.Time { return t1 }
	if err := s.SaveSettings(ctx, Settings{Language: "en"}); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return t2 }
	if err := s.SaveSettings(ctx, Settings{Language: "fr"}); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM settings`); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
	var v, at string
	if err := s.db.QueryRow(`SELECT value, updated_at FROM settings WHERE key = 'language'`).Scan(&v, &at); err != nil {
		t.Fatal(err)
	}
	if v != "fr" || at != t2.Format(time.RFC3339) {
		t.Errorf("value, updated_at = %q, %q", v, at)
	}

	// Back to automatic: stored as an empty string, not deleted.
	if err := s.SaveSettings(ctx, Settings{}); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM settings WHERE key = 'language' AND value = ''`); n != 1 {
		t.Errorf("empty language row missing")
	}
}

func TestSettingsUnknownKeyIgnoredAndKept(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.db.Exec(`INSERT INTO settings (key, value, updated_at) VALUES ('future', 'x', 'now')`); err != nil {
		t.Fatal(err)
	}
	got, err := s.Settings(ctx)
	if err != nil || got != (Settings{}) {
		t.Fatalf("Settings = %+v, %v", got, err)
	}
	if err := s.SaveSettings(ctx, Settings{Language: "en"}); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM settings WHERE key = 'future' AND value = 'x'`); n != 1 {
		t.Error("unknown key was not kept")
	}
	if got, _ := s.Settings(ctx); got.Language != "en" {
		t.Errorf("language = %q", got.Language)
	}
}

func TestSettingsSurviveReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSettings(ctx, Settings{Language: "en"}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = openPath(t, path)
	if got, err := s.Settings(ctx); err != nil || got.Language != "en" {
		t.Fatalf("Settings = %+v, %v", got, err)
	}
}

func TestSettingsErrorsOnClosedStore(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := s.Settings(ctx); err == nil {
		t.Error("Settings: want error")
	}
	if err := s.SaveSettings(ctx, Settings{Language: "en"}); err == nil {
		t.Error("SaveSettings: want error")
	}
}

func TestMigration007OnV6Database(t *testing.T) {
	ctx := context.Background()
	path := buildV4(t, true)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Go back to version 6: remove what 007 created.
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TABLE settings`, `DELETE FROM schema_migrations WHERE version = 7`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var v int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil || v != 6 {
		t.Fatalf("setup version = %d, %v", v, err)
	}
	db.Close()

	s = openPath(t, path)
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil || v != 7 {
		t.Errorf("version = %d, %v", v, err)
	}
	ls, err := s.Lists(ctx)
	if err != nil || len(ls) != 1 || ls[0].Parents != 2 || ls[0].Children != 5 {
		t.Errorf("lists = %+v, %v", ls, err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM templates`); n != 1 {
		t.Errorf("templates = %d", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM settings`); n != 0 {
		t.Errorf("settings has %d rows", n)
	}
	if got, err := s.Settings(ctx); err != nil || got != (Settings{}) {
		t.Errorf("Settings = %+v, %v", got, err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Errorf("foreign_key_check reports %d problems", n)
	}
}
