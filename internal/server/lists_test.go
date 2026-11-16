package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"cafeteria-management/internal/store"
)

// ---- helpers ---------------------------------------------------------------

var (
	rowsAnne = []store.ImportRow{
		{ParentName: "Anne Martin", ChildName: "Zoé", Email: "anne@example.com"},
		{ParentName: "Anne Martin", ChildName: "Émile", Email: "anne@example.com"},
		{ParentName: "Bob Durand", ChildName: "Léa", Email: "bob@example.com"},
	}
	rowsCarl = []store.ImportRow{
		{ParentName: "Carl Petit", ChildName: "Chloé", Email: "carl@example.com"},
	}
	fileHeader = []any{"Nom parent", "Nom enfant", "Adresse email"}
)

// excel builds an Excel file with the three columns and the given rows.
func excel(t *testing.T, rows ...[]any) []byte {
	t.Helper()
	return xlsx(t, append([][]any{fileHeader}, rows...))
}

// text returns the page text with HTML entities decoded.
func text(rec *httptest.ResponseRecorder) string { return html.UnescapeString(rec.Body.String()) }

func get(s *Server, path, lang string) *httptest.ResponseRecorder {
	return send(s, http.MethodGet, path, nil, lang)
}

func post(s *Server, path string, v url.Values) *httptest.ResponseRecorder {
	return send(s, http.MethodPost, path, v, "fr")
}

// idsIn returns the entries of a list by name.
func idsIn(t *testing.T, st *store.Store, listID int64) map[string]int64 {
	t.Helper()
	cs, err := st.Children(context.Background(), listID)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]int64{}
	for _, c := range cs {
		m[c.Name] = c.ID
	}
	return m
}

func versionOf(t *testing.T, st *store.Store, listID int64) int64 {
	t.Helper()
	v, err := st.ListVersion(context.Background(), listID)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func listOf(t *testing.T, st *store.Store, id int64) store.List {
	t.Helper()
	l, err := st.List(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// snapshot describes the state of the database that an import changes.
func snapshot(t *testing.T, st *store.Store) string {
	t.Helper()
	ctx := context.Background()
	ls, err := st.Lists(ctx)
	if err != nil {
		t.Fatal(err)
	}
	imps, err := st.Imports(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	out := fmt.Sprintf("imports=%d", len(imps))
	for _, l := range ls {
		cs, _ := st.Children(ctx, l.ID)
		out += fmt.Sprintf("|%+v v=%d %+v", l, versionOf(t, st, l.ID), cs)
	}
	return out
}

// setUsedAt sets used_at of all lists by hand (old, distinct timestamps), so
// that a later write is visible without waiting.
func setUsedAt(t *testing.T, path string, used map[int64]string) {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for id, ts := range used {
		if _, err := db.Exec(`UPDATE lists SET used_at = ? WHERE id = ?`, ts, id); err != nil {
			t.Fatal(err)
		}
	}
}

// previewInto previews content into an existing list.
func previewInto(t *testing.T, s *Server, listID int64, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	return uploadTo(t, s, "f.xlsx", content, url.Values{"list": {fmt.Sprint(listID)}})
}

// previewNew previews content into a new list.
func previewNew(t *testing.T, s *Server, name string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	return uploadTo(t, s, "f.xlsx", content, url.Values{"list": {newListChoice}, "list_name": {name}})
}

// ---- import form and target validation ------------------------------------

func TestImportFormTargets(t *testing.T) {
	t.Run("no list: name field only", func(t *testing.T) {
		s, _ := newTestServer(t)
		body := get(s, "/import", "fr").Body.String()
		if !strings.Contains(body, `<input type="hidden" name="list" value="new">`) || !strings.Contains(body, `name="list_name"`) {
			t.Errorf("hidden list=new or name field missing:\n%s", body)
		}
		if strings.Contains(body, "<select") {
			t.Error("select shown without lists")
		}
	})

	t.Run("lists: empty choice first and selected, new, then by name", func(t *testing.T) {
		s, st := newTestServer(t)
		seedList(t, st, "Zèbre", rowsCarl)
		seedList(t, st, "École", rowsCarl)
		body := get(s, "/import", "fr").Body.String()
		iChoose := strings.Index(body, `<option value="" selected>— Choisir —</option>`)
		iNew := strings.Index(body, `<option value="new">Nouvelle liste…</option>`)
		iEcole := strings.Index(body, ">École</option>")
		iZebre := strings.Index(body, ">Zèbre</option>")
		if !(0 <= iChoose && iChoose < iNew && iNew < iEcole && iEcole < iZebre) {
			t.Errorf("option order wrong: %d %d %d %d\n%s", iChoose, iNew, iEcole, iZebre, body)
		}
		if strings.Contains(body, `type="hidden" name="list"`) {
			t.Error("hidden list field shown with lists")
		}
		en := get(s, "/import", "en").Body.String()
		if !strings.Contains(en, "— Choose —") || !strings.Contains(en, "New list…") {
			t.Errorf("english select labels missing")
		}
	})
}

func TestImportPreviewTargetErrors(t *testing.T) {
	long := func(n int) string { return strings.Repeat("é", n) }
	tests := []struct {
		name   string
		fields url.Values
		fr, en string
	}{
		{"no choice", url.Values{}, "Choisissez la liste à remplacer", "Choose the list to replace"},
		{"empty choice", url.Values{"list": {""}}, "Choisissez la liste à remplacer", "Choose the list to replace"},
		{"new, no name", url.Values{"list": {"new"}}, "Le nom de la liste est obligatoire.", "The list name is required."},
		{"new, blank name", url.Values{"list": {"new"}, "list_name": {"   "}}, "Le nom de la liste est obligatoire.", "The list name is required."},
		{"new, 81 runes", url.Values{"list": {"new"}, "list_name": {long(81)}}, "trop long (maximum 80 caractères)", "too long (maximum 80 characters)"},
		{"new, control character", url.Values{"list": {"new"}, "list_name": {"A\x07B"}}, "caractères non autorisés", "characters that are not allowed"},
		{"new, tab", url.Values{"list": {"new"}, "list_name": {"A\tB"}}, "caractères non autorisés", "characters that are not allowed"},
		{"new, taken", url.Values{"list": {"new"}, "list_name": {"Cantine été"}}, "Une liste porte déjà ce nom.", "A list already has this name."},
		{"new, taken re-cased", url.Values{"list": {"new"}, "list_name": {"CANTINE ÉTÉ"}}, "Une liste porte déjà ce nom.", "A list already has this name."},
		{"new, taken without accents and with spaces", url.Values{"list": {"new"}, "list_name": {"  cantine   ete "}}, "Une liste porte déjà ce nom.", "A list already has this name."},
		{"unknown id", url.Values{"list": {"999"}}, "Cette liste n'existe pas", "This list does not exist"},
		{"non numeric", url.Values{"list": {"abc"}}, "Cette liste n'existe pas", "This list does not exist"},
		{"negative", url.Values{"list": {"-1"}}, "Cette liste n'existe pas", "This list does not exist"},
		{"zero", url.Values{"list": {"0"}}, "Cette liste n'existe pas", "This list does not exist"},
		{"decimal", url.Values{"list": {"1.5"}}, "Cette liste n'existe pas", "This list does not exist"},
		{"overflow", url.Values{"list": {"99999999999999999999"}}, "Cette liste n'existe pas", "This list does not exist"},
		{"id with space", url.Values{"list": {"1 "}}, "Cette liste n'existe pas", "This list does not exist"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, st := newTestServer(t)
			seedList(t, st, "Cantine été", rowsCarl)
			before := snapshot(t, st)

			// Checked before the file: a request with no file at all
			// reports the target error.
			for lang, want := range map[string]string{"fr": tt.fr, "en": tt.en} {
				req := multipartRequest(t, "/import/preview", tt.fields, "", nil)
				req.Host = "127.0.0.1:8080"
				req.Header.Set("Accept-Language", lang)
				rec := httptest.NewRecorder()
				s.ServeHTTP(rec, req)
				if rec.Code != http.StatusUnprocessableEntity {
					t.Fatalf("%s: status = %d", lang, rec.Code)
				}
				if !strings.Contains(text(rec), want) {
					t.Errorf("%s: body does not contain %q:\n%s", lang, want, rec.Body)
				}
				if strings.Contains(rec.Body.String(), `name="token"`) {
					t.Error("a token was issued")
				}
			}
			// And with a valid file, the target error still wins.
			rec := uploadTo(t, s, "ok.xlsx", excel(t, []any{"M", "L", "m@example.fr"}), tt.fields)
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(text(rec), tt.fr) {
				t.Errorf("with a file: status = %d", rec.Code)
			}
			if got := snapshot(t, st); got != before {
				t.Errorf("database changed:\n%s\n%s", before, got)
			}
		})
	}
}

func TestImportPreviewKeepsTypedTargetEscaped(t *testing.T) {
	s, st := newTestServer(t)
	a := seedList(t, st, "Cantine", rowsCarl)

	rec := uploadTo(t, s, "x.xlsx", excel(t, []any{"M", "L", "m@example.fr"}), url.Values{"list": {"new"}, "list_name": {`<b>"x"</b>` + strings.Repeat("y", 90)}})
	body := rec.Body.String()
	if rec.Code != http.StatusUnprocessableEntity || strings.Contains(body, "<b>") {
		t.Fatalf("status %d, unescaped name in %s", rec.Code, body)
	}
	if !strings.Contains(body, "&lt;b&gt;") || !strings.Contains(body, `<option value="new" selected>`) {
		t.Errorf("typed values not shown again:\n%s", body)
	}

	// A valid target but no file: the file error, with the target kept.
	req := multipartRequest(t, "/import/preview", url.Values{"list": {fmt.Sprint(a)}, "list_name": {"zzz"}}, "", nil)
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Accept-Language", "fr")
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), fmt.Sprintf(`<option value="%d" selected>`, a)) ||
		!strings.Contains(rec.Body.String(), `value="zzz"`) {
		t.Errorf("status = %d\n%s", rec.Code, rec.Body)
	}
}

func TestImportPreviewNameBoundary(t *testing.T) {
	s, _ := newTestServer(t)
	name := strings.Repeat("é", 80)
	rec := previewNew(t, s, name, excel(t, []any{"M", "L", "m@example.fr"}))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), name) {
		t.Fatalf("80 runes: status = %d", rec.Code)
	}
}

// ---- preview text -----------------------------------------------------------

func TestPreviewNewList(t *testing.T) {
	s, st := newTestServer(t)
	seedList(t, st, "Garderie", rowsCarl)
	before := snapshot(t, st)

	rec := previewNew(t, s, "  Cantine   <i>2026</i> ", excel(t, []any{"Marie", "Léa", "marie@example.fr"}))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, want := range []string{
		"Nouvelle liste « Cantine &lt;i&gt;2026&lt;/i&gt; »",
		"Confirmer l&#39;import",
		`name="token"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body does not contain %q", want)
		}
	}
	for _, bad := range []string{"confirm_replace", "Remplacer la liste", "<i>2026", "Remplace la liste", "button-danger"} {
		if strings.Contains(body, bad) {
			t.Errorf("new-list preview contains %q", bad)
		}
	}
	// Additions only: no deletion section.
	if strings.Contains(body, "seront supprimés") {
		t.Error("new list shows deletions")
	}
	// A new-list dry run writes nothing, not even the list.
	if got := snapshot(t, st); got != before {
		t.Errorf("preview changed the database")
	}
	if ls, _ := st.Lists(context.Background()); len(ls) != 1 {
		t.Errorf("lists = %+v", ls)
	}
}

func TestPreviewReplaceList(t *testing.T) {
	file := excel(t, []any{"Anne Martin", "Zoé", "anne@example.com"})

	t.Run("deletions, red button, only that list counted", func(t *testing.T) {
		s, st := newTestServer(t)
		a := seedList(t, st, `Cantine <b>"2026"</b>`, rowsAnne)
		seedList(t, st, "Garderie", rowsAnne) // same family, other list
		rec := previewInto(t, s, a, file)
		body, plain := rec.Body.String(), text(rec)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		for _, want := range []string{
			`name="confirm_replace" value="1" required`,
			`Je confirme remplacer la liste « Cantine <b>"2026"</b> ».`,
			`Remplacer la liste « Cantine <b>"2026"</b> » et supprimer 2 enfant(s)`,
			"Enfants qui seront supprimés (2)", "Émile", "Léa",
			"Remplace la liste « Cantine",
			`class="button-danger"`,
		} {
			if !strings.Contains(plain, want) && !strings.Contains(body, want) {
				t.Errorf("page does not contain %q", want)
			}
		}
		// Escaped in label and button.
		if strings.Contains(body, "<b>") && !strings.Contains(body, "&lt;b&gt;") || strings.Contains(body, `<b>"2026"`) {
			t.Errorf("list name not escaped:\n%s", body)
		}
		if strings.Contains(body, "Remplacer la liste « Cantine &lt;b&gt;&#34;2026&#34;&lt;/b&gt; » et supprimer 2 enfant(s)") == false {
			t.Errorf("button text missing or not escaped")
		}
	})

	t.Run("no changes: checkbox and plain button still shown", func(t *testing.T) {
		s, st := newTestServer(t)
		a := seedList(t, st, "Cantine", rowsAnne)
		rec := previewInto(t, s, a, excel(t,
			[]any{"Anne Martin", "Zoé", "anne@example.com"},
			[]any{"Anne Martin", "Émile", "anne@example.com"},
			[]any{"Bob Durand", "Léa", "bob@example.com"}))
		body := rec.Body.String()
		for _, want := range []string{`name="confirm_replace" value="1" required`, "Remplacer la liste « Cantine »"} {
			if !strings.Contains(body, want) {
				t.Errorf("page does not contain %q", want)
			}
		}
		if strings.Contains(body, "button-danger") || strings.Contains(body, "et supprimer") {
			t.Error("red button or deletion count without deletions")
		}
	})

	t.Run("family data and english", func(t *testing.T) {
		s, st := newTestServer(t)
		a := seedList(t, st, "Cantine", []store.ImportRow{
			{ParentName: "A", ChildName: "A", Email: "a@example.com", FamilyNumber: "10"},
			{ParentName: "B", ChildName: "B", Email: "b@example.com", FamilyNumber: "20"},
		})
		csv := csvHeader + "10;F10;A;a@example.com\n"
		req := uploadRequest(t, "e.csv", []byte(csv), url.Values{"list": {fmt.Sprint(a)}})
		req.Host = "127.0.0.1:8080"
		req.Header.Set("Accept-Language", "fr")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if !strings.Contains(text(rec), "Remplacer la liste « Cantine » et supprimer 1 ligne(s) famille") {
			t.Errorf("family variant missing:\n%s", rec.Body)
		}
		req = uploadRequest(t, "e.csv", []byte(csv), url.Values{"list": {fmt.Sprint(a)}})
		req.Host = "127.0.0.1:8080"
		req.Header.Set("Accept-Language", "en")
		rec = httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		for _, want := range []string{"Replace the list “Cantine” and delete 1 family row(s)", "I confirm I want to replace the list “Cantine”.", "Replaces the list “Cantine”"} {
			if !strings.Contains(text(rec), want) {
				t.Errorf("english page does not contain %q", want)
			}
		}
	})

	t.Run("english children variant", func(t *testing.T) {
		s, st := newTestServer(t)
		a := seedList(t, st, "Cantine", rowsAnne)
		req := uploadRequest(t, "f.xlsx", file, url.Values{"list": {fmt.Sprint(a)}})
		req.Host = "127.0.0.1:8080"
		req.Header.Set("Accept-Language", "en")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if !strings.Contains(text(rec), "Replace the list “Cantine” and delete 2 child(ren)") {
			t.Errorf("page:\n%s", rec.Body)
		}
	})
}

// ---- replace confirmation -----------------------------------------------------

func TestConfirmReplaceNeedsCheckbox(t *testing.T) {
	setup := func(t *testing.T) (*Server, *store.Store, int64, string) {
		s, st := newTestServer(t)
		a := seedList(t, st, "Cantine été", rowsAnne)
		tok := tokenFrom(t, previewInto(t, s, a, excel(t, []any{"Anne Martin", "Zoé", "anne@example.com"})).Body.String())
		return s, st, a, tok
	}

	for name, field := range map[string][]string{
		"missing": nil, "empty": {""}, "zero": {"0"}, "on": {"on"}, "true": {"true"}, "two": {"2"}, "spaced": {" 1"}, "yes": {"yes"},
	} {
		t.Run(name, func(t *testing.T) {
			s, st, a, tok := setup(t)
			before := snapshot(t, st)
			form := url.Values{"token": {tok}}
			if field != nil {
				form["confirm_replace"] = field
			}
			for lang, want := range map[string]string{
				"fr": "Cochez la case de confirmation pour remplacer la liste « Cantine été ».",
				"en": "Tick the confirmation box to replace the list “Cantine été”.",
			} {
				req := httptest.NewRequest(http.MethodPost, "/import/confirm", strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.Host = "127.0.0.1:8080"
				req.Header.Set("Accept-Language", lang)
				rec := httptest.NewRecorder()
				s.ServeHTTP(rec, req)
				if rec.Code != http.StatusUnprocessableEntity {
					t.Fatalf("%s: status = %d", lang, rec.Code)
				}
				if !strings.Contains(text(rec), want) {
					t.Errorf("%s: message %q missing:\n%s", lang, want, rec.Body)
				}
				// The preview page again, with the same token.
				if got := tokenFrom(t, rec.Body.String()); got != tok {
					t.Errorf("token changed: %q", got)
				}
				if !strings.Contains(rec.Body.String(), `name="confirm_replace"`) {
					t.Error("checkbox missing from the re-rendered preview")
				}
				if _, ok := s.pending.get(tok); !ok {
					t.Error("token was used up")
				}
			}
			if got := snapshot(t, st); got != before {
				t.Errorf("database changed:\n%s\n%s", before, got)
			}

			// The same token with the box ticked saves.
			rec := confirmWith(s, url.Values{"token": {tok}, "confirm_replace": {"1"}})
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("confirm status = %d: %s", rec.Code, rec.Body)
			}
			if cs, _ := st.Children(context.Background(), a); len(cs) != 1 || cs[0].Name != "Zoé" {
				t.Errorf("children = %+v", cs)
			}
		})
	}
}

func TestConfirmNewListNeedsNoCheckbox(t *testing.T) {
	s, st := newTestServer(t)
	tok := tokenFrom(t, previewNew(t, s, "Cantine", excel(t, []any{"M", "L", "m@example.fr"})).Body.String())
	rec := confirmWith(s, url.Values{"token": {tok}})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/import?done=") {
		t.Fatalf("status = %d, location %q", rec.Code, rec.Header().Get("Location"))
	}
	if l := totals(t, st); l.Name != "Cantine" || l.Children != 1 {
		t.Errorf("list = %+v", l)
	}

	// A posted confirm_replace is ignored for a new list.
	tok = tokenFrom(t, previewNew(t, s, "Garderie", excel(t, []any{"M", "L", "m@example.fr"})).Body.String())
	if rec := confirmWith(s, url.Values{"token": {tok}, "confirm_replace": {"0"}}); rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestConfirmUnknownTokenChecksTokenFirst(t *testing.T) {
	s, _ := newTestServer(t)
	for _, form := range []url.Values{{"token": {"nope"}}, {"token": {"nope"}, "confirm_replace": {"1"}}, {}} {
		rec := confirmWith(s, form)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(text(rec), "Cette vérification a expiré") && !strings.Contains(text(rec), "expiré") {
			t.Errorf("form %v: status %d\n%s", form, rec.Code, rec.Body)
		}
		if strings.Contains(text(rec), "Cochez la case") {
			t.Error("checkbox message shown for an unknown token")
		}
	}
}

func TestConfirmCreatesOrReplacesOnlyTheTarget(t *testing.T) {
	s, st := newTestServer(t)
	a := seedList(t, st, "Cantine", rowsAnne)
	b := seedList(t, st, "Garderie", rowsCarl)
	bBefore := listOf(t, st, b)
	bEntries, _ := st.Children(context.Background(), b)

	tok := tokenFrom(t, previewInto(t, s, a, excel(t, []any{"Dan Roy", "Hugo", "dan@example.com"})).Body.String())
	// A posted list or list_name on confirm is ignored: the token decides.
	rec := confirmWith(s, url.Values{"token": {tok}, "confirm_replace": {"1"}, "list": {fmt.Sprint(b)}, "list_name": {"Autre"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if cs, _ := st.Children(context.Background(), a); len(cs) != 1 || cs[0].Name != "Hugo" {
		t.Errorf("A = %+v", cs)
	}
	if got, _ := st.Children(context.Background(), b); fmt.Sprint(got) != fmt.Sprint(bEntries) {
		t.Errorf("B changed: %+v", got)
	}
	if got := listOf(t, st, b); got != bBefore {
		t.Errorf("B list changed: %+v -> %+v", bBefore, got)
	}
	if ls, _ := st.Lists(context.Background()); len(ls) != 2 {
		t.Errorf("lists = %+v (a list was created)", ls)
	}
}

func TestConfirmConflicts(t *testing.T) {
	file := excel(t, []any{"Dan Roy", "Hugo", "dan@example.com"})

	conflict := func(t *testing.T, s *Server, tok string, wantCode int, want string) {
		t.Helper()
		rec := confirm(s, tok)
		if rec.Code != wantCode || !strings.Contains(text(rec), want) {
			t.Fatalf("status = %d, want %d with %q:\n%s", rec.Code, wantCode, want, rec.Body)
		}
		// The token is used up: the user checks the file again.
		if again := confirm(s, tok); again.Code != http.StatusUnprocessableEntity {
			t.Errorf("reused token: status = %d", again.Code)
		}
	}

	t.Run("stale: import into the same list", func(t *testing.T) {
		s, st := newTestServer(t)
		a := seedList(t, st, "Cantine", rowsAnne)
		tok := tokenFrom(t, previewInto(t, s, a, file).Body.String())
		seed(t, st, rowsCarl) // replaces list a (the first by name)
		before := snapshot(t, st)
		conflict(t, s, tok, http.StatusConflict, "Les données ont changé")
		if got := snapshot(t, st); got != before {
			t.Error("a conflicting confirm wrote data")
		}
	})

	t.Run("list deleted meanwhile", func(t *testing.T) {
		s, st := newTestServer(t)
		a := seedList(t, st, "Cantine", rowsAnne)
		seedList(t, st, "Garderie", rowsCarl)
		tok := tokenFrom(t, previewInto(t, s, a, file).Body.String())
		if err := st.DeleteList(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		before := snapshot(t, st)
		conflict(t, s, tok, http.StatusConflict, "a été supprimée entre-temps")
		if got := snapshot(t, st); got != before {
			t.Error("a conflicting confirm wrote data")
		}
	})

	t.Run("new name taken meanwhile", func(t *testing.T) {
		s, st := newTestServer(t)
		tok := tokenFrom(t, previewNew(t, s, "Cantine", file).Body.String())
		seedList(t, st, "CANTINE", rowsCarl)
		before := snapshot(t, st)
		conflict(t, s, tok, http.StatusConflict, "créée entre-temps")
		if got := snapshot(t, st); got != before {
			t.Error("a conflicting confirm wrote data")
		}
	})

	t.Run("import into another list does not block", func(t *testing.T) {
		s, st := newTestServer(t)
		a := seedList(t, st, "Cantine", rowsAnne)
		b := seedList(t, st, "Garderie", rowsCarl)
		tok := tokenFrom(t, previewInto(t, s, a, file).Body.String())
		if _, err := st.ImportFamilies(context.Background(), store.ImportRequest{FileName: "x", Rows: rowsCarl, ListID: b}); err != nil {
			t.Fatal(err)
		}
		if rec := confirm(s, tok); rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
	})

	t.Run("rename of the target does not block", func(t *testing.T) {
		s, st := newTestServer(t)
		a := seedList(t, st, "Cantine", rowsAnne)
		tok := tokenFrom(t, previewInto(t, s, a, file).Body.String())
		if err := st.RenameList(context.Background(), a, "Cantine 2027"); err != nil {
			t.Fatal(err)
		}
		if rec := confirm(s, tok); rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
		imps, _ := st.Imports(context.Background(), 5)
		if imps[0].ListName != "Cantine 2027" || imps[0].ListID != a {
			t.Errorf("import = %+v", imps[0])
		}
	})

	t.Run("two previews of the same list: the second is stale", func(t *testing.T) {
		s, st := newTestServer(t)
		a := seedList(t, st, "Cantine", rowsAnne)
		t1 := tokenFrom(t, previewInto(t, s, a, file).Body.String())
		t2 := tokenFrom(t, previewInto(t, s, a, excel(t, []any{"Eve", "Ida", "eve@example.com"})).Body.String())
		if rec := confirm(s, t1); rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d", rec.Code)
		}
		if rec := confirm(s, t2); rec.Code != http.StatusConflict {
			t.Fatalf("status = %d", rec.Code)
		}
	})
}

// ---- history and "done" message --------------------------------------------

func TestImportDoneAndHistory(t *testing.T) {
	s, st := newTestServer(t)
	tok := tokenFrom(t, previewNew(t, s, "Cantine", excel(t, []any{"M", "L", "m@example.fr"})).Body.String())
	rec := confirm(s, tok)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || strings.Contains(loc, "Cantine") {
		t.Fatalf("status %d, location %q (no list name in URLs)", rec.Code, loc)
	}
	a := mainList(t, st)

	done := get(s, loc, "fr")
	if !strings.Contains(text(done), "Import de « f.xlsx » enregistré dans la liste « Cantine ».") {
		t.Errorf("done message missing:\n%s", done.Body)
	}
	if !strings.Contains(done.Body.String(), fmt.Sprintf(`href="/children?list=%d"`, a)) || !strings.Contains(text(done), "Voir la liste") {
		t.Errorf("link to the list missing")
	}
	en := get(s, loc, "en")
	if !strings.Contains(text(en), "Import of “f.xlsx” saved in the list “Cantine”.") {
		t.Errorf("english message missing")
	}

	// Renamed: the history shows the current name.
	if err := st.RenameList(context.Background(), a, "Cantine 2027"); err != nil {
		t.Fatal(err)
	}
	page := get(s, "/import", "fr")
	if !strings.Contains(page.Body.String(), "<td>Cantine 2027</td>") || strings.Contains(page.Body.String(), "<td>Cantine</td>") {
		t.Errorf("history does not show the current name:\n%s", page.Body)
	}

	// Deleted: the list's last name ("Cantine 2027", not the name at import
	// time), marked, and no link in the done message.
	if err := st.DeleteList(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	page = get(s, loc, "fr")
	if !strings.Contains(text(page), "Cantine 2027 (supprimée)") {
		t.Errorf("deleted marker missing:\n%s", page.Body)
	}
	if strings.Contains(page.Body.String(), "/children?list=") || strings.Contains(text(page), "Voir la liste") {
		t.Error("link to a deleted list")
	}
	if strings.Contains(text(page), "Cantine (supprimée)") {
		t.Error("history shows the old name")
	}
	if !strings.Contains(text(get(s, "/import", "en")), "Cantine 2027 (deleted)") {
		t.Error("english deleted marker missing")
	}
}

// ---- /children --------------------------------------------------------------

func TestChildrenListSelection(t *testing.T) {
	setup := func(t *testing.T) (*Server, *store.Store, string, int64, int64) {
		s, st, path := newTestServerAt(t)
		a := seedList(t, st, "Cantine", rowsAnne)
		b := seedList(t, st, "Garderie", rowsCarl)
		// Old, distinct values: Cantine is the default.
		setUsedAt(t, path, map[int64]string{a: "2020-01-02T00:00:00Z", b: "2020-01-01T00:00:00Z"})
		return s, st, path, a, b
	}

	t.Run("default is the last used list; ?list= opens another without changing it", func(t *testing.T) {
		s, st, _, a, b := setup(t)
		beforeA, beforeB := listOf(t, st, a), listOf(t, st, b)

		body := get(s, "/children", "fr").Body.String()
		if !strings.Contains(body, "Liste « Cantine »") || strings.Contains(body, "Chloé") {
			t.Errorf("default page does not show Cantine:\n%s", body)
		}
		rec := get(s, fmt.Sprintf("/children?list=%d", b), "fr")
		body = rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, "Liste « Garderie »") || !strings.Contains(body, "Chloé") || strings.Contains(body, "Zoé") {
			t.Errorf("list b page wrong (status %d):\n%s", rec.Code, body)
		}
		// GETs never write.
		if listOf(t, st, a) != beforeA || listOf(t, st, b) != beforeB {
			t.Errorf("GET changed used_at: %+v %+v", listOf(t, st, a), listOf(t, st, b))
		}
		if def, _ := st.DefaultList(context.Background()); def.ID != a {
			t.Errorf("default = %d, want %d", def.ID, a)
		}
		if !strings.Contains(get(s, "/children", "fr").Body.String(), "Liste « Cantine »") {
			t.Error("opening list b changed the default")
		}
	})

	t.Run("Continue makes the list the default", func(t *testing.T) {
		s, st, _, _, b := setup(t)
		ids := idsIn(t, st, b)
		v := url.Values{"list": {fmt.Sprint(b)}, "version": {fmt.Sprint(versionOf(t, st, b))}, "child": {fmt.Sprint(ids["Chloé"])}}
		if rec := post(s, "/compose", v); rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if def, _ := st.DefaultList(context.Background()); def.ID != b {
			t.Errorf("default = %d, want %d", def.ID, b)
		}
		if !strings.Contains(get(s, "/children", "fr").Body.String(), "Liste « Garderie »") {
			t.Error("page does not open the list used last")
		}
	})

	t.Run("unknown list: 404 with the default list", func(t *testing.T) {
		s, _, _, _, _ := setup(t)
		for _, q := range []string{"999", "abc", "-1", "0", "", "1.5", "99999999999999999999"} {
			rec := get(s, "/children?list="+q, "fr")
			if rec.Code != http.StatusNotFound {
				t.Errorf("list=%q: status = %d", q, rec.Code)
			}
			if !strings.Contains(text(rec), "Cette liste n'existe pas") || !strings.Contains(rec.Body.String(), "Liste « Cantine »") {
				t.Errorf("list=%q: message or default list missing", q)
			}
		}
		if !strings.Contains(text(get(s, "/children?list=999", "en")), "This list does not exist") {
			t.Error("english message missing")
		}
	})

	t.Run("no list at all", func(t *testing.T) {
		s, _ := newTestServer(t)
		rec := get(s, "/children", "fr")
		if rec.Code != http.StatusOK || !strings.Contains(text(rec), "Rien à sélectionner") {
			t.Errorf("status %d:\n%s", rec.Code, rec.Body)
		}
		rec = get(s, "/children?list=1", "fr")
		if rec.Code != http.StatusNotFound || !strings.Contains(text(rec), "Rien à sélectionner") {
			t.Errorf("with ?list=1: status %d", rec.Code)
		}
	})

	t.Run("selector only from two lists, current selected", func(t *testing.T) {
		s, st, _, a, b := setup(t)
		body := get(s, fmt.Sprintf("/children?list=%d", b), "fr").Body.String()
		if !strings.Contains(body, `<select id="list-select" name="list">`) ||
			!strings.Contains(body, fmt.Sprintf(`<option value="%d" selected>Garderie</option>`, b)) ||
			!strings.Contains(body, fmt.Sprintf(`<option value="%d">Cantine</option>`, a)) ||
			!strings.Contains(body, "Afficher") {
			t.Errorf("selector wrong:\n%s", body)
		}
		if err := st.DeleteList(context.Background(), b); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(get(s, "/children", "fr").Body.String(), "list-select") {
			t.Error("selector shown with one list")
		}
	})

	t.Run("form carries the list and its version", func(t *testing.T) {
		s, st, _, a, b := setup(t)
		replaceVia := func(id int64) { // bump b's version only
			if _, err := st.ImportFamilies(context.Background(), store.ImportRequest{FileName: "x", Rows: rowsCarl, ListID: id}); err != nil {
				t.Fatal(err)
			}
		}
		replaceVia(b)
		for _, id := range []int64{a, b} {
			body := get(s, fmt.Sprintf("/children?list=%d", id), "fr").Body.String()
			for _, want := range []string{
				fmt.Sprintf(`<input type="hidden" name="list" value="%d">`, id),
				fmt.Sprintf(`name="version" value="%d"`, versionOf(t, st, id)),
			} {
				if !strings.Contains(body, want) {
					t.Errorf("list %d: page does not contain %q", id, want)
				}
			}
		}
		if versionOf(t, st, a) == versionOf(t, st, b) {
			t.Error("test setup: versions should differ")
		}
	})

	t.Run("only that list's remembered selection is checked", func(t *testing.T) {
		s, st, _, a, b := setup(t)
		ia, ib := idsIn(t, st, a), idsIn(t, st, b)
		for _, p := range []struct {
			list int64
			ids  []int64
		}{{a, []int64{ia["Zoé"]}}, {b, []int64{ib["Chloé"]}}} {
			v := url.Values{"list": {fmt.Sprint(p.list)}, "version": {fmt.Sprint(versionOf(t, st, p.list))}}
			for _, id := range p.ids {
				v.Add("child", fmt.Sprint(id))
			}
			if rec := post(s, "/compose", v); rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
		}
		checked := func(listID int64) []string {
			body := get(s, fmt.Sprintf("/children?list=%d", listID), "fr").Body.String()
			var out []string
			for _, row := range strings.Split(body, "<tr") {
				if strings.Contains(row, " checked") {
					for _, n := range []string{"Zoé", "Émile", "Léa", "Chloé"} {
						if strings.Contains(row, n) {
							out = append(out, n)
						}
					}
				}
			}
			return out
		}
		if got := checked(a); len(got) != 1 || got[0] != "Zoé" {
			t.Errorf("A checked = %v", got)
		}
		if got := checked(b); len(got) != 1 || got[0] != "Chloé" {
			t.Errorf("B checked = %v", got)
		}
	})
}

// ---- /compose ---------------------------------------------------------------

func TestComposeLists(t *testing.T) {
	setup := func(t *testing.T) (*Server, *store.Store, int64, int64, map[string]int64, map[string]int64) {
		s, st := newTestServer(t)
		a := seedList(t, st, "Cantine", rowsAnne)
		b := seedList(t, st, "Garderie", rowsCarl)
		return s, st, a, b, idsIn(t, st, a), idsIn(t, st, b)
	}
	vals := func(t *testing.T, st *store.Store, list int64, ids ...int64) url.Values {
		v := url.Values{"list": {fmt.Sprint(list)}, "version": {fmt.Sprint(versionOf(t, st, list))}}
		for _, id := range ids {
			v.Add("child", fmt.Sprint(id))
		}
		return v
	}

	t.Run("ok: list name, hidden list, back link", func(t *testing.T) {
		s, st, a, _, ia, _ := setup(t)
		rec := post(s, "/compose", vals(t, st, a, ia["Zoé"]))
		body := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		for _, want := range []string{
			"Liste « Cantine »",
			fmt.Sprintf(`<input type="hidden" name="list" value="%d">`, a),
			fmt.Sprintf(`href="/children?list=%d"`, a),
		} {
			if !strings.Contains(body, want) {
				t.Errorf("page does not contain %q", want)
			}
		}
		// Preview and template actions keep the list.
		for _, action := range []string{"preview", "save_template", "load", "bogus"} {
			v := vals(t, st, a, ia["Zoé"])
			v.Set("action", action)
			v.Set("subject", "S {{parent_name}}")
			v.Set("body", "B")
			v.Set("template_name", "T")
			rec := post(s, "/compose", v)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), fmt.Sprintf(`name="list" value="%d"`, a)) {
				t.Errorf("action %s: status %d, list lost", action, rec.Code)
			}
		}
		en := send(s, http.MethodPost, "/compose", vals(t, st, a, ia["Zoé"]), "en")
		if !strings.Contains(text(en), "List “Cantine”") {
			t.Error("english list line missing")
		}
	})

	t.Run("missing or invalid list is stale", func(t *testing.T) {
		s, st, a, _, ia, _ := setup(t)
		for name, list := range map[string]string{"missing": "", "text": "abc", "zero": "0", "negative": "-1", "unknown": "999"} {
			v := vals(t, st, a, ia["Zoé"])
			if list == "" {
				v.Del("list")
			} else {
				v.Set("list", list)
			}
			rec := post(s, "/compose", v)
			if rec.Code != http.StatusConflict || !strings.Contains(text(rec), "Les données ont changé") {
				t.Errorf("%s: status = %d", name, rec.Code)
			}
		}
	})

	t.Run("version of another list or an old version is stale", func(t *testing.T) {
		s, st, a, b, ia, _ := setup(t)
		v := vals(t, st, a, ia["Zoé"])
		v.Set("version", fmt.Sprint(versionOf(t, st, b)))
		if versionOf(t, st, a) == versionOf(t, st, b) {
			t.Fatal("setup: versions must differ")
		}
		if rec := post(s, "/compose", v); rec.Code != http.StatusConflict {
			t.Errorf("other list's version: status = %d", rec.Code)
		}

		old := vals(t, st, a, ia["Zoé"])
		if _, err := st.ImportFamilies(context.Background(), store.ImportRequest{FileName: "x", Rows: rowsAnne, ListID: a}); err != nil {
			t.Fatal(err)
		}
		if rec := post(s, "/compose", old); rec.Code != http.StatusConflict {
			t.Errorf("old version: status = %d", rec.Code)
		}
	})

	t.Run("import into another list still allows composing", func(t *testing.T) {
		s, st, a, b, ia, _ := setup(t)
		v := vals(t, st, a, ia["Zoé"])
		if _, err := st.ImportFamilies(context.Background(), store.ImportRequest{FileName: "x", Rows: rowsCarl, ListID: b}); err != nil {
			t.Fatal(err)
		}
		if rec := post(s, "/compose", v); rec.Code != http.StatusOK {
			t.Errorf("status = %d", rec.Code)
		}
	})

	t.Run("deleted list is stale and shows the default list", func(t *testing.T) {
		s, st, a, b, _, ib := setup(t)
		v := vals(t, st, b, ib["Chloé"])
		if err := st.DeleteList(context.Background(), b); err != nil {
			t.Fatal(err)
		}
		rec := post(s, "/compose", v)
		if rec.Code != http.StatusConflict || !strings.Contains(text(rec), "Les données ont changé") {
			t.Fatalf("status = %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Liste « Cantine »") || strings.Contains(rec.Body.String(), "Chloé") {
			t.Errorf("default list not shown instead:\n%s", rec.Body)
		}
		_ = a
	})

	t.Run("IDs of another list are dropped", func(t *testing.T) {
		s, st, a, _, ia, ib := setup(t)
		rec := post(s, "/compose", vals(t, st, a, ia["Zoé"], ib["Chloé"]))
		body := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if strings.Contains(body, "carl@example.com") || strings.Contains(body, "Carl Petit") || strings.Contains(body, fmt.Sprintf(`name="child" value="%d"`, ib["Chloé"])) {
			t.Errorf("another list's entry reached the recipients:\n%s", body)
		}
		if !strings.Contains(body, "anne@example.com") {
			t.Error("own entry missing")
		}
		// Only a foreign ID: refused, nothing is remembered for it.
		rec = post(s, "/compose", vals(t, st, a, ib["Chloé"]))
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(text(rec), "Sélectionnez au moins une ligne.") {
			t.Errorf("only foreign ID: status = %d", rec.Code)
		}
		if saved, _ := st.SavedSelection(context.Background(), a); saved[ib["Chloé"]] {
			t.Error("foreign ID saved with A")
		}
	})

	t.Run("remembered selection per list", func(t *testing.T) {
		s, st, a, b, ia, ib := setup(t)
		post(s, "/compose", vals(t, st, a, ia["Zoé"], ia["Léa"]))
		post(s, "/compose", vals(t, st, b, ib["Chloé"]))
		sa, _ := st.SavedSelection(context.Background(), a)
		sb, _ := st.SavedSelection(context.Background(), b)
		if len(sa) != 2 || !sa[ia["Zoé"]] || !sa[ia["Léa"]] || len(sb) != 1 || !sb[ib["Chloé"]] {
			t.Errorf("saved = %v / %v", sa, sb)
		}
	})
}

// ---- rename and delete pages --------------------------------------------------

func TestListRename(t *testing.T) {
	t.Run("form and flow", func(t *testing.T) {
		s, st := newTestServer(t)
		a := seedList(t, st, "Cantine", rowsAnne)
		seedList(t, st, "Garderie", rowsCarl)
		before := snapshot(t, st)

		rec := get(s, fmt.Sprintf("/lists/%d/rename", a), "fr")
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, `value="Cantine"`) || !strings.Contains(body, fmt.Sprintf(`action="/lists/%d/rename"`, a)) || !strings.Contains(body, `maxlength="80"`) {
			t.Fatalf("form wrong (status %d):\n%s", rec.Code, body)
		}
		if got := snapshot(t, st); got != before {
			t.Error("GET changed the database")
		}

		rec = post(s, fmt.Sprintf("/lists/%d/rename", a), url.Values{"name": {"  Cantine   été\u00a02027 "}})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/?renamed=1" {
			t.Fatalf("status %d, location %q", rec.Code, rec.Header().Get("Location"))
		}
		if l := listOf(t, st, a); l.Name != "Cantine été 2027" {
			t.Errorf("name = %q", l.Name)
		}
		if cs, _ := st.Children(context.Background(), a); len(cs) != 3 {
			t.Errorf("entries changed: %+v", cs)
		}
		home := get(s, "/?renamed=1", "fr")
		if !strings.Contains(text(home), "Liste renommée.") || !strings.Contains(home.Body.String(), "Cantine été 2027") {
			t.Errorf("notice or name missing:\n%s", home.Body)
		}
		if !strings.Contains(text(get(s, "/?renamed=1", "en")), "List renamed.") {
			t.Error("english notice missing")
		}
		// Re-casing the own name is allowed.
		if rec := post(s, fmt.Sprintf("/lists/%d/rename", a), url.Values{"name": {"CANTINE ÉTÉ 2027"}}); rec.Code != http.StatusSeeOther {
			t.Errorf("re-cased own name: status = %d", rec.Code)
		}
	})

	t.Run("errors keep the typed value escaped", func(t *testing.T) {
		s, st := newTestServer(t)
		a := seedList(t, st, "Cantine", rowsAnne)
		seedList(t, st, "Garderie été", rowsCarl)
		tests := []struct{ name, in, want string }{
			{"empty", "", "Le nom de la liste est obligatoire."},
			{"blank", "   ", "Le nom de la liste est obligatoire."},
			{"too long", strings.Repeat("a", 81), "trop long (maximum 80 caractères)"},
			{"control", "A\x00B", "caractères non autorisés"},
			{"taken", "garderie ete", "Une liste porte déjà ce nom."},
			{"taken markup", "<b>GARDERIE été</b>", ""},
		}
		for _, tt := range tests {
			rec := post(s, fmt.Sprintf("/lists/%d/rename", a), url.Values{"name": {tt.in}})
			if tt.want == "" {
				// Not a duplicate: it is simply renamed, with the markup kept as text.
				if rec.Code != http.StatusSeeOther {
					t.Errorf("%s: status = %d", tt.name, rec.Code)
				}
				continue
			}
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(text(rec), tt.want) {
				t.Errorf("%s: status %d\n%s", tt.name, rec.Code, rec.Body)
			}
			if l := listOf(t, st, a); l.Name != "Cantine" {
				t.Errorf("%s: name changed to %q", tt.name, l.Name)
			}
		}
		rec := post(s, fmt.Sprintf("/lists/%d/rename", a), url.Values{"name": {`"><script>alert(1)</script>` + strings.Repeat("x", 90)}})
		if strings.Contains(rec.Body.String(), "<script>alert") || !strings.Contains(rec.Body.String(), "&lt;script&gt;") {
			t.Errorf("typed name not escaped:\n%s", rec.Body)
		}
	})

	t.Run("unknown and bad IDs", func(t *testing.T) {
		s, st := newTestServer(t)
		seedList(t, st, "Cantine", rowsAnne)
		for _, id := range []string{"999", "abc", "-1", "0", "1.5", "99999999999999999999"} {
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				rec := send(s, method, "/lists/"+id+"/rename", url.Values{"name": {"X"}}, "fr")
				if rec.Code != http.StatusNotFound || !strings.Contains(text(rec), "Cette liste n'existe pas") {
					t.Errorf("%s %s: status = %d", method, id, rec.Code)
				}
			}
		}
	})

	// Plan decision 11: an unknown ID is a 404, whatever the posted name.
	t.Run("unknown ID with an invalid name is still 404", func(t *testing.T) {
		s, _ := newTestServer(t)
		for _, name := range []string{"", strings.Repeat("a", 81), "A\x07"} {
			rec := post(s, "/lists/999/rename", url.Values{"name": {name}})
			if rec.Code != http.StatusNotFound {
				t.Errorf("name %q: status = %d, want 404 (the list does not exist)", name, rec.Code)
			}
		}
	})

	t.Run("oversized body", func(t *testing.T) {
		s, st := newTestServer(t)
		a := seedList(t, st, "Cantine", rowsAnne)
		rec := post(s, fmt.Sprintf("/lists/%d/rename", a), url.Values{"name": {"Autre"}, "junk": {strings.Repeat("a", 1<<20+10)}})
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(text(rec), "trop volumineuse") {
			t.Fatalf("status = %d", rec.Code)
		}
		if l := listOf(t, st, a); l.Name != "Cantine" {
			t.Errorf("renamed by an oversized body: %q", l.Name)
		}
	})
}

func TestListDelete(t *testing.T) {
	t.Run("confirmation page then delete", func(t *testing.T) {
		s, st := newTestServer(t)
		a := seedList(t, st, "Cantine <i>x</i>", rowsAnne)
		b := seedList(t, st, "Garderie", rowsCarl)
		ids := idsIn(t, st, a)
		if err := st.SaveSelection(context.Background(), a, []int64{ids["Zoé"]}); err != nil {
			t.Fatal(err)
		}
		before := snapshot(t, st)

		rec := get(s, fmt.Sprintf("/lists/%d/delete", a), "fr")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		for _, want := range []string{
			"Supprimer la liste « Cantine <i>x</i> » ? Ses 3 ligne(s) (enfants ou familles) et 2 adresse(s) email seront supprimées.",
			"L'historique des imports est conservé.", `class="button-danger"`,
		} {
			if !strings.Contains(text(rec), want) && !strings.Contains(rec.Body.String(), want) {
				t.Errorf("page does not contain %q", want)
			}
		}
		if strings.Contains(rec.Body.String(), "<i>x</i>") {
			t.Error("list name not escaped")
		}
		if !strings.Contains(rec.Body.String(), fmt.Sprintf(`action="/lists/%d/delete"`, a)) {
			t.Error("form action missing")
		}
		if !strings.Contains(text(get(s, fmt.Sprintf("/lists/%d/delete", a), "en")), "Delete the list “Cantine <i>x</i>”? Its 3 row(s)") {
			t.Error("english confirmation missing")
		}
		if got := snapshot(t, st); got != before {
			t.Error("GET changed the database")
		}

		rec = post(s, fmt.Sprintf("/lists/%d/delete", a), nil)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/?deleted=1" {
			t.Fatalf("status %d, location %q", rec.Code, rec.Header().Get("Location"))
		}
		// Entries and remembered selection are gone; the other list is not.
		if _, err := st.List(context.Background(), a); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("list still there: %v", err)
		}
		if cs, _ := st.Children(context.Background(), a); len(cs) != 0 {
			t.Errorf("entries left: %+v", cs)
		}
		if sel, _ := st.SavedSelection(context.Background(), a); len(sel) != 0 {
			t.Errorf("selection left: %v", sel)
		}
		if cs, _ := st.Children(context.Background(), b); len(cs) != 1 {
			t.Errorf("B entries = %+v", cs)
		}
		// Import history is kept.
		imps, _ := st.Imports(context.Background(), 10)
		if len(imps) != 2 || !imps[1].ListDeleted || imps[1].ListName != "Cantine <i>x</i>" {
			t.Errorf("imports = %+v", imps)
		}

		home := get(s, "/?deleted=1", "fr")
		if !strings.Contains(text(home), "Liste supprimée.") || strings.Contains(home.Body.String(), "Cantine") {
			t.Errorf("home after delete:\n%s", home.Body)
		}
		if !strings.Contains(text(get(s, "/?deleted=1", "en")), "List deleted.") {
			t.Error("english notice missing")
		}
		// The old links die with the list.
		if rec := get(s, fmt.Sprintf("/children?list=%d", a), "fr"); rec.Code != http.StatusNotFound {
			t.Errorf("children of deleted list: status = %d", rec.Code)
		}
		for _, p := range []struct{ method, path string }{
			{http.MethodGet, fmt.Sprintf("/lists/%d/delete", a)},
			{http.MethodPost, fmt.Sprintf("/lists/%d/delete", a)}, // second delete
			{http.MethodGet, fmt.Sprintf("/lists/%d/rename", a)},
		} {
			rec := send(s, p.method, p.path, url.Values{}, "fr")
			if rec.Code != http.StatusNotFound || !strings.Contains(text(rec), "Cette liste n'existe pas") {
				t.Errorf("%s %s: status = %d", p.method, p.path, rec.Code)
			}
		}
	})

	t.Run("unknown and bad IDs", func(t *testing.T) {
		s, st := newTestServer(t)
		seedList(t, st, "Cantine", rowsAnne)
		for _, id := range []string{"999", "abc", "-1", "0"} {
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				if rec := send(s, method, "/lists/"+id+"/delete", url.Values{}, "fr"); rec.Code != http.StatusNotFound {
					t.Errorf("%s %s: status = %d", method, id, rec.Code)
				}
			}
		}
		if ls, _ := st.Lists(context.Background()); len(ls) != 1 {
			t.Errorf("lists = %+v", ls)
		}
	})

	t.Run("pending import of the deleted list is refused", func(t *testing.T) {
		s, st := newTestServer(t)
		a := seedList(t, st, "Cantine", rowsAnne)
		tok := tokenFrom(t, previewInto(t, s, a, excel(t, []any{"M", "L", "m@example.fr"})).Body.String())
		post(s, fmt.Sprintf("/lists/%d/delete", a), nil)
		rec := confirm(s, tok)
		if rec.Code != http.StatusConflict || !strings.Contains(text(rec), "supprimée entre-temps") {
			t.Errorf("status = %d", rec.Code)
		}
		if ls, _ := st.Lists(context.Background()); len(ls) != 0 {
			t.Errorf("a list came back: %+v", ls)
		}
	})
}

func TestListPostsRejectForeignRequests(t *testing.T) {
	s, st := newTestServer(t)
	a := seedList(t, st, "Cantine", rowsAnne)
	before := snapshot(t, st)

	for _, path := range []string{fmt.Sprintf("/lists/%d/rename", a), fmt.Sprintf("/lists/%d/delete", a)} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("name=Autre"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		if rec := do(s, req); rec.Code != http.StatusForbidden {
			t.Errorf("cross-site %s: status = %d", path, rec.Code)
		}
		req = httptest.NewRequest(http.MethodPost, path, strings.NewReader("name=Autre"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Host = "evil.example:8080"
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("other host %s: status = %d", path, rec.Code)
		}
		req = httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "evil.example:8080"
		rec = httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET other host %s: status = %d", path, rec.Code)
		}
	}
	if got := snapshot(t, st); got != before {
		t.Error("a refused request changed the database")
	}
}

func TestListRoutesMethods(t *testing.T) {
	s, st := newTestServer(t)
	a := seedList(t, st, "Cantine", rowsAnne)
	for _, tt := range []struct{ method, path string }{
		{http.MethodPut, fmt.Sprintf("/lists/%d/rename", a)},
		{http.MethodDelete, fmt.Sprintf("/lists/%d/delete", a)},
		{http.MethodPost, "/lists"},
		{http.MethodGet, fmt.Sprintf("/lists/%d", a)},
	} {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		if rec := do(s, req); rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: status = %d", tt.method, tt.path, rec.Code)
		}
	}
	if _, err := st.List(context.Background(), a); err != nil {
		t.Errorf("list gone: %v", err)
	}
}

// ---- home -----------------------------------------------------------------------

func TestHomeLists(t *testing.T) {
	t.Run("empty state", func(t *testing.T) {
		s, _ := newTestServer(t)
		rec := get(s, "/", "fr")
		body := text(rec)
		if rec.Code != http.StatusOK || !strings.Contains(body, "Aucune donnée pour l'instant") || !strings.Contains(rec.Body.String(), `href="/import"`) {
			t.Errorf("empty message or import link missing:\n%s", rec.Body)
		}
		if strings.Contains(rec.Body.String(), "<table") {
			t.Error("table shown without lists")
		}
	})

	t.Run("one line per list with counts, last import and actions", func(t *testing.T) {
		s, st := newTestServer(t)
		z := seedList(t, st, "Zèbre", rowsAnne)
		e := seedList(t, st, "École <b>", []store.ImportRow{
			{ParentName: "A", ChildName: "A", Email: "a@example.com", FamilyNumber: "10"},
			{ParentName: "A", ChildName: "A", Email: "b@example.com", FamilyNumber: "10"},
			{ParentName: "B", ChildName: "B", Email: "", FamilyNumber: "20", InvalidEmail: "x"},
		})
		empty, err := st.ImportFamilies(context.Background(), store.ImportRequest{FileName: "e.xlsx", NewListName: "Vide"})
		if err != nil {
			t.Fatal(err)
		}
		rec := get(s, "/", "fr")
		body := rec.Body.String()
		if strings.Count(body, `href="/lists/`) != 6 {
			t.Errorf("rename+delete links = %d, want 6", strings.Count(body, `href="/lists/`))
		}
		// Sorted by name key: école, vide, zebre.
		iE, iV, iZ := strings.Index(body, "École &lt;b&gt;"), strings.Index(body, ">Vide<"), strings.Index(body, ">Zèbre<")
		if !(0 < iE && iE < iV && iV < iZ) {
			t.Errorf("order: %d %d %d", iE, iV, iZ)
		}
		for _, want := range []string{
			"2 Familles", "2 Adresses email", // École: 2 family numbers, 2 addresses
			"3 Enfants", "2 Parents", // Zèbre: child labels
			"0 Enfants",
			fmt.Sprintf(`href="/children?list=%d"`, z), fmt.Sprintf(`href="/children?list=%d"`, e),
			fmt.Sprintf(`href="/lists/%d/rename"`, z), fmt.Sprintf(`href="/lists/%d/delete"`, e),
			"Dernier import", "Renommer", "Supprimer",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("home does not contain %q", want)
			}
		}
		// "Choose" only for a non-empty list.
		if strings.Contains(body, fmt.Sprintf(`href="/children?list=%d"`, empty.ListID)) {
			t.Error("Choose shown for an empty list")
		}
		if strings.Contains(body, "<b>") {
			t.Error("list name not escaped")
		}
		// The last import is shown as a date (not the "no import" dash) for a
		// list with an import.
		l := listOf(t, st, z)
		if want := ">" + html.EscapeString(l.LastImport.Local().Format("02/01/2006 15:04")) + "<"; !strings.Contains(body, want) {
			t.Errorf("last import date %q missing", want)
		}

		en := get(s, "/", "en").Body.String()
		for _, want := range []string{"Family lists", "Choose", "Rename", "Delete", "Last import", "Email addresses", "Children"} {
			if !strings.Contains(en, want) {
				t.Errorf("english home does not contain %q", want)
			}
		}
		if !strings.Contains(en, l.LastImport.Local().Format("2006-01-02 15:04")) {
			t.Error("english date format missing")
		}
	})
}

// ---- logs ---------------------------------------------------------------------

func TestListsLogNothingPersonal(t *testing.T) {
	logs := captureLogs(t)
	s, st := newTestServer(t)

	const secretList, secretChild, secretMail = "Famille Secrète", "Zoé Confidentielle", "confidentielle@example.fr"
	file := excel(t, []any{"Parent Caché", secretChild, secretMail})

	tok := tokenFrom(t, previewNew(t, s, secretList, file).Body.String())
	if rec := confirm(s, tok); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm = %d", rec.Code)
	}
	a := mainList(t, st)

	// Unconfirmed replacement, then a replacement, a conflict, a rename,
	// compose, and delete.
	tok = tokenFrom(t, previewInto(t, s, a, file).Body.String())
	confirmWith(s, url.Values{"token": {tok}})
	confirmWith(s, url.Values{"token": {tok}, "confirm_replace": {"1"}})
	confirm(s, tok) // used up
	post(s, fmt.Sprintf("/lists/%d/rename", a), url.Values{"name": {"Nom Renommé Secret"}})
	post(s, fmt.Sprintf("/lists/%d/rename", a), url.Values{"name": {strings.Repeat("é", 99)}}) // 422
	ids := idsIn(t, st, a)
	post(s, "/compose", url.Values{"list": {fmt.Sprint(a)}, "version": {fmt.Sprint(versionOf(t, st, a))}, "child": {fmt.Sprint(ids[secretChild])}})
	post(s, "/compose", url.Values{"list": {fmt.Sprint(a)}, "version": {"0"}, "child": {fmt.Sprint(ids[secretChild])}})
	get(s, "/children?list=zzz", "fr")
	post(s, fmt.Sprintf("/lists/%d/delete", a), nil)

	out := strings.ToLower(logs.String())
	if !strings.Contains(out, "import saved") || !strings.Contains(out, "list renamed") || !strings.Contains(out, "list deleted") {
		t.Errorf("expected info logs missing:\n%s", out)
	}
	for _, secret := range []string{secretList, secretChild, secretMail, "parent caché", "nom renommé secret", "confidentielle", "famille", "caché"} {
		if strings.Contains(out, strings.ToLower(secret)) {
			t.Errorf("logs contain %q:\n%s", secret, logs)
		}
	}
}

// ---- locale keys ---------------------------------------------------------------

func TestListLocaleKeys(t *testing.T) {
	s, _ := newTestServer(t)
	for _, lang := range []string{"fr", "en"} {
		loc := s.i18n.Localizer(s.i18n.Match(lang))
		for _, key := range []string{
			"lists.title", "lists.col.name", "lists.col.entries", "lists.col.addresses", "lists.col.last_import", "lists.no_import",
			"lists.choose", "lists.rename", "lists.delete", "lists.name_label", "lists.rename_title", "lists.save", "lists.cancel",
			"lists.renamed", "lists.delete_title", "lists.delete_confirm", "lists.delete_button", "lists.deleted",
			"lists.error.not_found", "lists.error.name_required", "lists.error.name_too_long", "lists.error.name_invalid", "lists.error.name_taken",
			"import.target_label", "import.target_choose", "import.target_new", "import.new_name_label", "import.new_name_note",
			"import.error.list_required", "import.error.list_gone", "import.error.list_name_taken", "import.error.replace_unconfirmed",
			"import.col.list", "import.list_deleted", "import.see_list", "import.replace_note", "import.done",
			"preview.target_new", "preview.target_replace", "preview.replace_check", "preview.confirm_replace",
			"preview.confirm_replace_deletions", "preview.confirm_replace_deletions_families",
			"children.list_label", "children.list_show", "children.list_current", "compose.list",
		} {
			if got := loc.T(key); got == key || got == "" {
				t.Errorf("%s: key %q has no message", lang, key)
			}
		}
		for _, key := range []string{"home.last_import", "home.children_button", "home.families_button", "preview.confirm_deletions", "preview.confirm_deletions_families"} {
			if got := loc.T(key); got != key {
				t.Errorf("%s: removed key %q still has a message %q", lang, key, got)
			}
		}
	}
}

// ---- name rules -----------------------------------------------------------------

func TestCheckName(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		wantClean   string
		wantProblem string
	}{
		{"plain", "Cantine", "Cantine", ""},
		{"accents and spaces cleaned", "  Cantine   été  ", "Cantine été", ""},
		{"empty", "", "", nameRequired},
		{"blank", "   ", "", nameRequired},
		{"exactly 80 runes", strings.Repeat("é", 80), strings.Repeat("é", 80), ""},
		{"81 runes", strings.Repeat("é", 81), "", nameTooLong},
		{"80 runes after cleaning", "  " + strings.Repeat("a", 80) + "  ", strings.Repeat("a", 80), ""},
		{"control character", "a\x07b", "", nameInvalid},
		{"tab", "a\tb", "", nameInvalid},
		{"newline", "a\nb", "", nameInvalid},
		{"invalid utf-8", "a\xffb", "", nameInvalid},
		{"markup is text", "<b>x</b>", "<b>x</b>", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clean, problem := checkName(tt.in)
			if clean != tt.wantClean || problem != tt.wantProblem {
				t.Errorf("checkName(%q) = %q, %q; want %q, %q", tt.in, clean, problem, tt.wantClean, tt.wantProblem)
			}
		})
	}
}

// ---- fix round 1: pending cap and double confirm -----------------------------

// fakeClock is a settable clock for pendingImports.now.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func TestPendingImportsCap(t *testing.T) {
	newP := func() (*pendingImports, *fakeClock) {
		c := &fakeClock{t: time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)}
		p := newPendingImports(time.Hour)
		p.now = c.now
		return p, c
	}

	t.Run("ninth entry evicts the earliest expiry", func(t *testing.T) {
		p, c := newP()
		var tokens []string
		for i := 0; i < maxPendingImports; i++ {
			tokens = append(tokens, p.put(pendingImport{fileName: fmt.Sprint(i)}))
			c.t = c.t.Add(time.Minute)
		}
		if len(p.items) != maxPendingImports {
			t.Fatalf("items = %d", len(p.items))
		}
		ninth := p.put(pendingImport{fileName: "new"})
		if len(p.items) != maxPendingImports {
			t.Fatalf("items = %d, want %d", len(p.items), maxPendingImports)
		}
		if _, ok := p.get(tokens[0]); ok {
			t.Error("oldest entry kept")
		}
		for _, tok := range tokens[1:] {
			if _, ok := p.get(tok); !ok {
				t.Errorf("entry %q evicted", tok)
			}
		}
		if it, ok := p.get(ninth); !ok || it.fileName != "new" {
			t.Errorf("new entry = %+v, %v", it, ok)
		}
		p.remove(ninth)
		if _, ok := p.get(ninth); ok {
			t.Error("removed entry still there")
		}
		if len(p.items) != maxPendingImports-1 {
			t.Errorf("items = %d", len(p.items))
		}
	})

	t.Run("expired entries are dropped before any live eviction", func(t *testing.T) {
		p, c := newP()
		// 3 entries that will expire, then 5 live ones later.
		var old []string
		for i := 0; i < 3; i++ {
			old = append(old, p.put(pendingImport{}))
		}
		c.t = c.t.Add(30 * time.Minute)
		var live []string
		for i := 0; i < 5; i++ {
			live = append(live, p.put(pendingImport{}))
		}
		c.t = c.t.Add(40 * time.Minute) // old ones expired, live ones not
		for _, tok := range old {
			if _, ok := p.get(tok); ok {
				t.Fatal("expired entry readable")
			}
		}
		extra := p.put(pendingImport{})
		if len(p.items) != 6 {
			t.Errorf("items = %d, want 6 (5 live + new)", len(p.items))
		}
		for _, tok := range live {
			if _, ok := p.get(tok); !ok {
				t.Error("live entry evicted")
			}
		}
		if _, ok := p.get(extra); !ok {
			t.Error("new entry missing")
		}
	})

	t.Run("8 live plus expired ones: no live eviction beyond one", func(t *testing.T) {
		p, c := newP()
		p.put(pendingImport{})
		p.put(pendingImport{})
		c.t = c.t.Add(30 * time.Minute)
		var live []string
		for i := 0; i < maxPendingImports-2; i++ {
			live = append(live, p.put(pendingImport{}))
		}
		c.t = c.t.Add(40 * time.Minute) // the first two are expired
		p.put(pendingImport{})
		for _, tok := range live {
			if _, ok := p.get(tok); !ok {
				t.Error("live entry evicted although expired ones could go")
			}
		}
	})
}

func TestConfirmTwiceSameToken(t *testing.T) {
	t.Run("replace into an existing list", func(t *testing.T) {
		s, st := newTestServer(t)
		a := seedList(t, st, "Cantine", rowsAnne)
		tok := tokenFrom(t, previewInto(t, s, a, excel(t, []any{"Dan Roy", "Hugo", "dan@example.com"})).Body.String())
		if rec := confirm(s, tok); rec.Code != http.StatusSeeOther {
			t.Fatalf("first confirm = %d: %s", rec.Code, rec.Body)
		}
		imps, _ := st.Imports(context.Background(), 20)
		before, version := snapshot(t, st), versionOf(t, st, a)

		rec := confirm(s, tok)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("second confirm = %d, want 422 (token used up)", rec.Code)
		}
		if got := snapshot(t, st); got != before {
			t.Errorf("database changed by the second confirm:\n%s\n%s", before, got)
		}
		if got := versionOf(t, st, a); got != version {
			t.Errorf("version %d -> %d", version, got)
		}
		if after, _ := st.Imports(context.Background(), 20); len(after) != len(imps) {
			t.Errorf("imports %d -> %d", len(imps), len(after))
		}
		if cs, _ := st.Children(context.Background(), a); len(cs) != 1 || cs[0].Name != "Hugo" {
			t.Errorf("children = %+v", cs)
		}
	})

	t.Run("new list", func(t *testing.T) {
		s, st := newTestServer(t)
		tok := tokenFrom(t, previewNew(t, s, "Cantine", excel(t, []any{"M", "L", "m@example.fr"})).Body.String())
		if rec := confirm(s, tok); rec.Code != http.StatusSeeOther {
			t.Fatalf("first confirm = %d", rec.Code)
		}
		if rec := confirm(s, tok); rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("second confirm = %d, want 422", rec.Code)
		}
		ls, _ := st.Lists(context.Background())
		if len(ls) != 1 || ls[0].Name != "Cantine" {
			t.Errorf("lists = %+v", ls)
		}
		if imps, _ := st.Imports(context.Background(), 20); len(imps) != 1 {
			t.Errorf("imports = %d", len(imps))
		}
	})
}
