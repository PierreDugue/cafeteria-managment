package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cafeteria-management/internal/store"
)

// checkedRadio returns the value of the checked language radio, or "".
func checkedRadio(body string) string {
	for _, v := range []string{"auto", "fr", "en"} {
		if strings.Contains(body, `value="`+v+`" checked`) {
			return v
		}
	}
	return ""
}

func postSettings(s *Server, lang string, v url.Values) *httptest.ResponseRecorder {
	return send(s, http.MethodPost, "/settings", v, lang)
}

func storedLanguage(t *testing.T, st *store.Store) string {
	t.Helper()
	got, err := st.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return got.Language
}

func TestSettingsPageAutomatic(t *testing.T) {
	s, _ := newTestServer(t)
	for _, tt := range []struct{ accept, wantHTML, wantBrowser string }{
		{"fr-FR", `lang="fr"`, "Français"},
		{"en-GB,en;q=0.9", `lang="en"`, "English"},
	} {
		rec := send(s, http.MethodGet, "/settings", nil, tt.accept)
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, tt.wantHTML) {
			t.Fatalf("%s: status %d, body %s", tt.accept, rec.Code, body)
		}
		if checkedRadio(body) != "auto" {
			t.Errorf("%s: checked = %q, want auto", tt.accept, checkedRadio(body))
		}
		if !strings.Contains(body, tt.wantBrowser) {
			t.Errorf("%s: browser language %q not shown", tt.accept, tt.wantBrowser)
		}
		if !strings.Contains(body, `href="/settings" aria-current="page"`) {
			t.Errorf("%s: settings nav item not active", tt.accept)
		}
		if strings.Contains(body, "alert-error") || strings.Contains(body, "alert-success") {
			t.Errorf("%s: unexpected notice", tt.accept)
		}
	}
}

func TestSettingsPageShowsStoredLanguage(t *testing.T) {
	_, st := newTestServer(t)
	if err := st.SaveSettings(context.Background(), store.Settings{Language: "en"}); err != nil {
		t.Fatal(err)
	}
	s, err := New(context.Background(), "test", st, &fakeQueue{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := send(s, http.MethodGet, "/settings", nil, "fr-FR").Body.String()
	if checkedRadio(body) != "en" || !strings.Contains(body, `lang="en"`) {
		t.Errorf("checked = %q; body %s", checkedRadio(body), body)
	}
}

func TestSettingsSaveFlow(t *testing.T) {
	s, st := newTestServer(t)

	rec := postSettings(s, "fr", url.Values{"language": {"en"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings?saved=1" {
		t.Fatalf("status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}
	if got := storedLanguage(t, st); got != "en" {
		t.Fatalf("stored = %q", got)
	}

	// Any page is now English although the browser asks for French.
	home := send(s, http.MethodGet, "/", nil, "fr").Body.String()
	if !strings.Contains(home, `<html lang="en"`) || !strings.Contains(home, "Settings") {
		t.Errorf("home not in English: %s", home)
	}
	page := send(s, http.MethodGet, "/settings?saved=1", nil, "fr").Body.String()
	if !strings.Contains(page, "Settings saved.") || checkedRadio(page) != "en" {
		t.Errorf("saved page: checked %q, %s", checkedRadio(page), page)
	}
	if plain := send(s, http.MethodGet, "/settings", nil, "fr").Body.String(); strings.Contains(plain, "Settings saved.") {
		t.Error("notice shown without ?saved=1")
	}

	// Back to automatic.
	rec = postSettings(s, "fr", url.Values{"language": {"auto"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("auto: status %d", rec.Code)
	}
	if got := storedLanguage(t, st); got != "" {
		t.Errorf("stored = %q, want empty", got)
	}
	if home := send(s, http.MethodGet, "/", nil, "fr").Body.String(); !strings.Contains(home, `<html lang="fr"`) {
		t.Errorf("home should follow Accept-Language fr")
	}
	if home := send(s, http.MethodGet, "/", nil, "en").Body.String(); !strings.Contains(home, `<html lang="en"`) {
		t.Errorf("home should follow Accept-Language en")
	}

	// French can be forced against an English browser.
	postSettings(s, "en", url.Values{"language": {"fr"}})
	if got := storedLanguage(t, st); got != "fr" {
		t.Errorf("stored = %q", got)
	}
	if home := send(s, http.MethodGet, "/", nil, "en").Body.String(); !strings.Contains(home, `<html lang="fr"`) {
		t.Errorf("home should be French")
	}
}

func TestSettingsSaveInvalid(t *testing.T) {
	big := strings.Repeat("a", maxSettingsFormBytes+10)
	cases := []struct {
		name string
		v    url.Values
	}{
		{"unsupported", url.Values{"language": {"de"}}},
		{"empty", url.Values{"language": {""}}},
		{"missing field", url.Values{"other": {"en"}}},
		{"uppercase", url.Values{"language": {"FR"}}},
		{"padded", url.Values{"language": {" en"}}},
		{"region", url.Values{"language": {"fr-FR"}}},
		{"oversized", url.Values{"language": {"en"}, "junk": {big}}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s, st := newTestServer(t)
			postSettings(s, "fr", url.Values{"language": {"fr"}}) // known starting point
			rec := postSettings(s, "en", tt.v)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, "alert-error") || !strings.Contains(body, "Formulaire invalide") {
				t.Errorf("error not shown in the saved language (French): %s", body)
			}
			if got := storedLanguage(t, st); got != "fr" {
				t.Errorf("stored = %q, want fr", got)
			}
			if home := send(s, http.MethodGet, "/", nil, "en").Body.String(); !strings.Contains(home, `<html lang="fr"`) {
				t.Error("in-memory language changed")
			}
		})
	}
}

func TestSettingsInvalidErrorInBothLanguages(t *testing.T) {
	for accept, want := range map[string]string{"fr": "Formulaire invalide", "en": "Invalid form: nothing was saved."} {
		s, _ := newTestServer(t)
		rec := postSettings(s, accept, url.Values{"language": {"de"}})
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: status %d, body %s", accept, rec.Code, rec.Body.String())
		}
	}
}

func TestSettingsProtection(t *testing.T) {
	s, st := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader("language=en"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if rec := do(s, req); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site: status %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader("language=en"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "evil.example:8080"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("other host: status %d", rec.Code)
	}
	if got := storedLanguage(t, st); got != "" {
		t.Errorf("stored = %q after refused posts", got)
	}

	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if rec := do(s, httptest.NewRequest(m, "/settings", nil)); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status %d", m, rec.Code)
		}
	}
}

func TestSettingsUnsupportedStoredValueIsAutomatic(t *testing.T) {
	_, st := newTestServer(t)
	if err := st.SaveSettings(context.Background(), store.Settings{Language: "de"}); err != nil {
		t.Fatal(err)
	}
	s, err := New(context.Background(), "test", st, &fakeQueue{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for accept, want := range map[string]string{"fr": `lang="fr"`, "en": `lang="en"`} {
		rec := send(s, http.MethodGet, "/settings", nil, accept)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) || checkedRadio(rec.Body.String()) != "auto" {
			t.Errorf("%s: status %d, checked %q", accept, rec.Code, checkedRadio(rec.Body.String()))
		}
	}
}

func TestSettingsSurviveRestart(t *testing.T) {
	s, st := newTestServer(t)
	postSettings(s, "fr", url.Values{"language": {"en"}})
	s2, err := New(context.Background(), "test", st, &fakeQueue{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if home := send(s2, http.MethodGet, "/", nil, "fr").Body.String(); !strings.Contains(home, `<html lang="en"`) {
		t.Error("restarted server did not keep the language")
	}
}

func TestNewFailsWhenSettingsCannotBeRead(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if s, err := New(context.Background(), "test", st, &fakeQueue{}, nil); err == nil {
		t.Fatalf("New = %v, nil; want error", s)
	}
}

func TestSettingsLanguageSetsJoiningWord(t *testing.T) {
	s, st := newTestServer(t)
	seed(t, st, silvaRows)
	ids := childIDs(t, st)
	preview := func(accept string) string {
		v := composeVals(t, st, idsOf(ids, "SILVA Ana", "SILVA Rui"), "action", "preview", "subject", "S", "body", "{{parent_name}}")
		return previewText(t, send(s, http.MethodPost, "/compose", v, accept).Body.String())
	}
	if got := preview("fr"); got != "SILVA Ana et SILVA Rui" {
		t.Errorf("auto fr: %q", got)
	}
	postSettings(s, "fr", url.Values{"language": {"en"}})
	if got := preview("fr"); got != "SILVA Ana and SILVA Rui" {
		t.Errorf("setting en: %q", got)
	}
}

func TestSettingsSendingSectionTestBanner(t *testing.T) {
	s, _ := newTestServer(t)
	if body := send(s, http.MethodGet, "/settings", nil, "en").Body.String(); !strings.Contains(body, "test-banner") {
		t.Error("test banner missing with the test provider")
	}
	g := newGraphEnv(t)
	if body := send(g.s, http.MethodGet, "/settings", nil, "en").Body.String(); strings.Contains(body, "test-banner") {
		t.Error("test banner shown with a real provider")
	}
}

func TestSettingsConcurrentAccess(t *testing.T) {
	s, _ := newTestServer(t)
	var wg sync.WaitGroup
	deadline := time.Now().Add(300 * time.Millisecond)
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; time.Now().Before(deadline) && n < 50; n++ {
				if i%2 == 0 {
					send(s, http.MethodGet, "/", nil, "fr")
					send(s, http.MethodGet, "/settings", nil, "en")
				} else {
					lang := []string{"fr", "en", "auto"}[n%3]
					postSettings(s, "fr", url.Values{"language": {lang}})
				}
			}
		}()
	}
	wg.Wait()
}
