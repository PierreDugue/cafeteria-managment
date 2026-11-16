package server

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"cafeteria-management/internal/store"
)

// mainList returns the ID of the first list (by name), or 0 if there is none.
func mainList(t *testing.T, st *store.Store) int64 {
	t.Helper()
	ls, err := st.Lists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) == 0 {
		return 0
	}
	return ls[0].ID
}

// target aims an import at the first list, or at a new list "Cantine" when
// there is none, so single-list tests read as before.
func target(t *testing.T, st *store.Store, req store.ImportRequest) store.ImportRequest {
	t.Helper()
	if id := mainList(t, st); id != 0 {
		req.ListID = id
	} else {
		req.NewListName = "Cantine"
	}
	return req
}

// seed imports rows into the first list (created as "Cantine" if needed).
func seed(t *testing.T, st *store.Store, rows []store.ImportRow) {
	t.Helper()
	if _, err := st.ImportFamilies(context.Background(), target(t, st, store.ImportRequest{FileName: "a.xlsx", RowsRead: len(rows), Rows: rows})); err != nil {
		t.Fatal(err)
	}
}

// seedList creates a list called name from rows and returns its ID.
func seedList(t *testing.T, st *store.Store, name string, rows []store.ImportRow) int64 {
	t.Helper()
	sum, err := st.ImportFamilies(context.Background(), store.ImportRequest{FileName: name + ".xlsx", RowsRead: len(rows), Rows: rows, NewListName: name})
	if err != nil {
		t.Fatal(err)
	}
	return sum.ListID
}

// threeChildren: 3 children, 2 parents.
var threeChildren = []store.ImportRow{
	{ParentName: "Anne Martin", ChildName: "Zoé", Email: "anne@example.com"},
	{ParentName: "Anne Martin", ChildName: "Émile", Email: "anne@example.com"},
	{ParentName: "Bob Durand", ChildName: "Léa", Email: "bob@example.com"},
}

func postCompose(s *Server, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/compose", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return do(s, req)
}

func childIDs(t *testing.T, st *store.Store) map[string]int64 {
	t.Helper()
	cs, err := st.Children(context.Background(), mainList(t, st))
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]int64{}
	for _, c := range cs {
		m[c.Name] = c.ID
	}
	return m
}

func form(ids ...any) string {
	v := url.Values{}
	for _, id := range ids {
		v.Add("child", fmt.Sprint(id))
	}
	return v.Encode()
}

// currentVersion returns the version of the first list, as the /children
// form would carry it.
func currentVersion(t *testing.T, st *store.Store) int64 {
	t.Helper()
	v, err := st.ListVersion(context.Background(), mainList(t, st))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// formAt is form plus the hidden list and version fields.
func formAt(listID, version int64, ids ...any) string {
	return form(ids...) + "&" + listQS(listID, version)
}

// listQS is the hidden list and version fields as a query string.
func listQS(listID, version int64) string {
	return fmt.Sprintf("list=%d&version=%d", listID, version)
}

// currentQS is listQS for the first list at its current version.
func currentQS(t *testing.T, st *store.Store) string {
	t.Helper()
	return listQS(mainList(t, st), currentVersion(t, st))
}

// formV is form with the first list and its current version.
func formV(t *testing.T, st *store.Store, ids ...any) string {
	t.Helper()
	return form(ids...) + "&" + currentQS(t, st)
}

// manyIDs builds a form body with n distinct positive IDs.
func manyIDs(n int) string {
	v := url.Values{}
	for i := 1; i <= n; i++ {
		v.Add("child", fmt.Sprint(i))
	}
	return v.Encode()
}

// fakeID returns an ID that does not exist in a database seeded with a few
// rows.
const fakeID = 424242

func TestChildrenPage(t *testing.T) {
	t.Run("rows with data attributes", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		rec := do(s, httptest.NewRequest(http.MethodGet, "/children", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{
			`data-search="emile anne martin anne@example.com" data-email="anne@example.com"`,
			`data-search="lea bob durand bob@example.com" data-email="bob@example.com"`,
			`data-search="zoe anne martin anne@example.com"`,
			`action="/compose"`,
			`/static/children.js`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("body does not contain %q", want)
			}
		}
		if i, j := strings.Index(body, "Émile"), strings.Index(body, "Zoé"); i < 0 || j < 0 || i > j {
			t.Errorf("Émile should be listed before Zoé")
		}
		if strings.Contains(body, `href="/import"`) && strings.Contains(body, "Rien à sélectionner pour l&#39;instant") {
			t.Error("empty message shown with children")
		}
	})

	t.Run("empty database links to import", func(t *testing.T) {
		s, _ := newTestServer(t)
		rec := do(s, httptest.NewRequest(http.MethodGet, "/children", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "Rien à sélectionner pour l&#39;instant") || !strings.Contains(body, `href="/import"`) {
			t.Errorf("missing empty message or import link: %s", body)
		}
		if strings.Contains(body, `name="child"`) {
			t.Error("unexpected checkbox")
		}
	})

	t.Run("english", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		req := httptest.NewRequest(http.MethodGet, "/children", nil)
		req.Host = "127.0.0.1:8080"
		req.Header.Set("Accept-Language", "en")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		body := rec.Body.String()
		if !strings.Contains(body, `lang="en"`) || !strings.Contains(body, "Select all") {
			t.Errorf("not English: %s", body)
		}
		if strings.Contains(body, "Tout sélectionner") {
			t.Error("French text in English page")
		}
	})

	t.Run("html escaped", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, []store.ImportRow{{ParentName: `<b>P</b>`, ChildName: `<script>alert(1)</script>`, Email: "x@example.com"}})
		body := do(s, httptest.NewRequest(http.MethodGet, "/children", nil)).Body.String()
		if strings.Contains(body, "<script>alert") || strings.Contains(body, "<b>P</b>") {
			t.Errorf("unescaped HTML: %s", body)
		}
		if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
			t.Error("escaped name missing")
		}
	})

	t.Run("nav and home link", func(t *testing.T) {
		s, st := newTestServer(t)
		if body := do(s, httptest.NewRequest(http.MethodGet, "/", nil)).Body.String(); strings.Contains(body, `href="/children?list=`) {
			t.Error("home links to /children with an empty database")
		}
		seed(t, st, threeChildren)
		want := fmt.Sprintf(`href="/children?list=%d"`, mainList(t, st))
		if body := do(s, httptest.NewRequest(http.MethodGet, "/", nil)).Body.String(); !strings.Contains(body, want) {
			t.Errorf("home has no link %s to the list", want)
		}
	})
}

func TestCompose(t *testing.T) {
	t.Run("groups by parent", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		ids := childIDs(t, st)
		rec := postCompose(s, formV(t, st, ids["Zoé"], ids["Léa"], ids["Émile"]))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "2 email(s) seront envoyés") {
			t.Errorf("want 2 emails: %s", body)
		}
		if !strings.Contains(body, "Émile, Zoé") {
			t.Errorf("Anne's children should be grouped in store order")
		}
		if strings.Count(body, "<td>anne@example.com</td>") != 1 || strings.Count(body, "<td>bob@example.com</td>") != 1 {
			t.Errorf("each parent must appear once")
		}
	})

	t.Run("invalid non-positive and duplicate ids ignored", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		ids := childIDs(t, st)
		rec := postCompose(s, formV(t, st, "abc", "", "-3", 0, "1.5", "99999999999999999999", ids["Léa"], ids["Léa"], " "+fmt.Sprint(ids["Léa"])))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "1 email(s)") || strings.Contains(body, "anne@example.com") {
			t.Errorf("want only Bob: %s", body)
		}
	})

	t.Run("import after the selection is stale", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		ids := childIDs(t, st)
		stale := currentVersion(t, st)
		// Second import removes Léa (and Bob) between list and compose.
		seed(t, st, threeChildren[:2])
		rec := postCompose(s, formAt(mainList(t, st), stale, ids["Zoé"], ids["Léa"]))
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "Les données ont changé depuis votre sélection") {
			t.Errorf("stale message missing: %s", body)
		}
		if strings.Contains(body, "seront envoyés") || strings.Contains(body, "bob@example.com") && strings.Contains(body, "email(s)") {
			t.Error("recipients must not be listed")
		}
		if !strings.Contains(body, `name="child"`) {
			t.Error("children list should be shown again")
		}
	})

	t.Run("stale selection in english", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		ids := childIDs(t, st)
		stale := currentVersion(t, st)
		seed(t, st, threeChildren[:2])
		req := httptest.NewRequest(http.MethodPost, "/compose", strings.NewReader(formAt(mainList(t, st), stale, ids["Zoé"], ids["Léa"])))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Host = "127.0.0.1:8080"
		req.Header.Set("Accept-Language", "en")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "The data changed since your selection") {
			t.Errorf("status %d body: %s", rec.Code, rec.Body)
		}
	})

	t.Run("nothing valid gives 422", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		for name, body := range map[string]string{
			"no field":                "",
			"version only":            "version=1",
			"list and version only":   "list=1&version=1",
			"invalid":                 formV(t, st, "abc", -1, 0),
			"unknown id":              formV(t, st, fakeID),
			"invalid without version": form("abc", -1, 0),
		} {
			t.Run(name, func(t *testing.T) {
				rec := postCompose(s, body)
				if rec.Code != http.StatusUnprocessableEntity {
					t.Fatalf("status = %d", rec.Code)
				}
				out := rec.Body.String()
				if !strings.Contains(out, "Sélectionnez au moins une ligne.") {
					t.Errorf("error message missing")
				}
				if !strings.Contains(out, `name="child"`) {
					t.Errorf("list should be shown again")
				}
			})
		}
	})

	t.Run("422 in english", func(t *testing.T) {
		s, _ := newTestServer(t)
		req := httptest.NewRequest(http.MethodPost, "/compose", nil)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Host = "127.0.0.1:8080"
		req.Header.Set("Accept-Language", "en")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Select at least one row.") {
			t.Fatalf("status %d body %s", rec.Code, rec.Body)
		}
	})

	t.Run("cross-site rejected", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		ids := childIDs(t, st)
		req := httptest.NewRequest(http.MethodPost, "/compose", strings.NewReader(formV(t, st, ids["Léa"])))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		if rec := do(s, req); rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d", rec.Code)
		}
	})

	t.Run("oversized body rejected", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		ids := childIDs(t, st)
		body := formV(t, st, ids["Léa"]) + "&junk=" + strings.Repeat("a", 1<<20+10)
		rec := postCompose(s, body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "bob@example.com") && strings.Contains(rec.Body.String(), "email(s) seront") {
			t.Error("oversized body must not be processed")
		}
		if !strings.Contains(rec.Body.String(), "La sélection envoyée est trop volumineuse.") {
			t.Error("too_large message missing")
		}
		if strings.Contains(rec.Body.String(), "Sélectionnez au moins une ligne.") {
			t.Error("none_selected message must not be shown for an oversized body")
		}
	})

	t.Run("oversized body english", func(t *testing.T) {
		s, _ := newTestServer(t)
		req := httptest.NewRequest(http.MethodPost, "/compose", strings.NewReader("junk="+strings.Repeat("a", 1<<20+10)))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Host = "127.0.0.1:8080"
		req.Header.Set("Accept-Language", "en")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "The submitted selection is too large.") {
			t.Fatalf("status = %d, body lacks too_large message", rec.Code)
		}
	})

	t.Run("too many ids", func(t *testing.T) {
		s, _ := newTestServer(t)
		rec := postCompose(s, manyIDs(maxSelectedChildren+1))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "Trop de lignes sélectionnées (maximum 5000).") {
			t.Errorf("too_many message missing: %s", body)
		}
	})

	t.Run("too many ids english", func(t *testing.T) {
		s, _ := newTestServer(t)
		req := httptest.NewRequest(http.MethodPost, "/compose", strings.NewReader(manyIDs(maxSelectedChildren+1)))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Host = "127.0.0.1:8080"
		req.Header.Set("Accept-Language", "en")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Too many rows selected (maximum 5000).") {
			t.Fatalf("status = %d, body lacks too_many message", rec.Code)
		}
	})

	t.Run("exactly max ids accepted", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		// IDs 1..5000 include the three real rows: the request passes the
		// limit and the version check.
		rec := postCompose(s, manyIDs(maxSelectedChildren)+"&"+currentQS(t, st))
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Trop d") {
			t.Fatalf("status = %d, want 200 without too_many", rec.Code)
		}
	})

	t.Run("duplicates and invalid ids do not count", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		v := url.Values{}
		for i := 0; i < 4000; i++ {
			v.Add("child", "1")
			v.Add("child", "abc")
		}
		v.Set("version", fmt.Sprint(currentVersion(t, st)))
		v.Set("list", fmt.Sprint(mainList(t, st)))
		rec := postCompose(s, v.Encode())
		if rec.Code == http.StatusInternalServerError || strings.Contains(rec.Body.String(), "Trop d") {
			t.Fatalf("status = %d, duplicates/invalid must not trigger too_many", rec.Code)
		}
	})

	t.Run("html escaped", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, []store.ImportRow{{ParentName: `<i>P</i>`, ChildName: `<script>alert(1)</script>`, Email: "x@example.com"}})
		ids := childIDs(t, st)
		body := postCompose(s, formV(t, st, ids[`<script>alert(1)</script>`])).Body.String()
		if strings.Contains(body, "<script>alert") || strings.Contains(body, "<i>P</i>") {
			t.Errorf("unescaped HTML: %s", body)
		}
		if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
			t.Error("escaped name missing")
		}
	})

	t.Run("get not allowed", func(t *testing.T) {
		s, _ := newTestServer(t)
		rec := do(s, httptest.NewRequest(http.MethodGet, "/compose", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d", rec.Code)
		}
	})
}

func TestComposeRefusesEntryWithoutEmail(t *testing.T) {
	s, st := newTestServer(t)
	seed(t, st, []store.ImportRow{
		{ParentName: "DUPONT", ChildName: "DUPONT", Email: "a@example.com", FamilyNumber: "10"},
		{ParentName: "NOMAIL", ChildName: "NOMAIL", FamilyNumber: "20", InvalidEmail: "secret.bad@gmailcom"},
		{ParentName: "EMPTY", ChildName: "EMPTY", FamilyNumber: "30"},
	})
	ids := childIDs(t, st)

	for name, selected := range map[string][]any{
		"valid and invalid": {ids["DUPONT"], ids["NOMAIL"]},
		"only invalid":      {ids["NOMAIL"]},
		"only empty":        {ids["EMPTY"]},
	} {
		t.Run(name, func(t *testing.T) {
			rec := postCompose(s, formV(t, st, selected...))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, "La sélection contient une famille sans adresse email valide.") {
				t.Errorf("error message missing: %s", body)
			}
			if strings.Contains(body, "seront envoyés") {
				t.Error("recipients page shown")
			}
		})
	}

	t.Run("english", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/compose", strings.NewReader(formV(t, st, ids["NOMAIL"])))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Host = "127.0.0.1:8080"
		req.Header.Set("Accept-Language", "en")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "The selection contains a family without a valid email address.") {
			t.Fatalf("status %d body %s", rec.Code, rec.Body)
		}
	})

	t.Run("log has the id only", func(t *testing.T) {
		var buf bytes.Buffer
		old := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
		t.Cleanup(func() { slog.SetDefault(old) })

		postCompose(s, formV(t, st, ids["NOMAIL"]))
		out := buf.String()
		if !strings.Contains(out, "entry without a valid email selected") || !strings.Contains(out, fmt.Sprintf("id=%d", ids["NOMAIL"])) {
			t.Errorf("log = %q", out)
		}
		for _, secret := range []string{"secret.bad", "NOMAIL", "a@example.com"} {
			if strings.Contains(out, secret) {
				t.Errorf("log leaks %q: %s", secret, out)
			}
		}
	})
}

func TestComposeFamilies(t *testing.T) {
	s, st := newTestServer(t)
	seed(t, st, []store.ImportRow{
		{ParentName: "DUPONT", ChildName: "DUPONT", Email: "shared@example.com", FamilyNumber: "10"},
		{ParentName: "DUPONT", ChildName: "MARTIN", Email: "shared@example.com", FamilyNumber: "20"},
		{ParentName: "ZED", ChildName: "ZED", Email: "z@example.com", FamilyNumber: "30"},
	})
	ids := childIDs(t, st)

	rec := postCompose(s, formV(t, st, ids["DUPONT"], ids["MARTIN"]))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{"1 email(s) seront envoyés, un par adresse.", "Familles concernées", "10 DUPONT, 20 MARTIN"} {
		if !strings.Contains(body, want) {
			t.Errorf("body does not contain %q: %s", want, body)
		}
	}
	if strings.Contains(body, "Payeur") {
		t.Error("payer column must be gone")
	}
	if strings.Count(body, "shared@example.com") != 2 { // table + preview picker
		t.Errorf("shared address must be one recipient: %s", body)
	}
	if strings.Contains(body, "z@example.com") {
		t.Error("unselected family listed")
	}

	t.Run("english", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/compose", strings.NewReader(formV(t, st, ids["ZED"])))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Host = "127.0.0.1:8080"
		req.Header.Set("Accept-Language", "en")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		b := rec.Body.String()
		if !strings.Contains(b, "1 email(s) will be sent, one per address.") || !strings.Contains(b, "Families concerned") || strings.Contains(b, "Payer") {
			t.Errorf("body: %s", b)
		}
	})
}

func TestChildrenPageFamilies(t *testing.T) {
	rows := []store.ImportRow{
		{ParentName: "DUPONT", ChildName: "DUPONT", Email: "a@example.com", FamilyNumber: "10"},
		{ParentName: "BAD", ChildName: "BAD", FamilyNumber: "20", InvalidEmail: `<b>x</b>@gmailcom`},
		{ParentName: "EMPTY", ChildName: "EMPTY", FamilyNumber: "30"},
		{ParentName: "<i>XSS</i>", ChildName: "<i>XSS</i>", Email: "xss@example.com", FamilyNumber: "40"},
	}
	get := func(t *testing.T, lang string) string {
		s, st := newTestServer(t)
		seed(t, st, rows)
		req := httptest.NewRequest(http.MethodGet, "/children", nil)
		req.Host = "127.0.0.1:8080"
		req.Header.Set("Accept-Language", lang)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		return rec.Body.String()
	}

	t.Run("french", func(t *testing.T) {
		body := get(t, "fr")
		for _, want := range []string{
			"N° famille", "Nom de facturation", "Email du payeur", "Nom, n° de famille ou email",
			"{children} famille(s) sélectionnée(s)",
			"2 ligne(s) sans adresse email valide",
			"Email du payeur manquant",
			`class="warning-text"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("body does not contain %q", want)
			}
		}
		if strings.Contains(body, "<th>Parent</th>") || strings.Contains(body, "<th>Enfant</th>") {
			t.Error("child-mode columns in family mode")
		}
		if !strings.Contains(body, "Email du payeur invalide : &lt;b&gt;x&lt;/b&gt;@gmailcom") {
			t.Errorf("invalid value not shown escaped: %s", body)
		}
		if strings.Contains(body, "<b>x</b>") || strings.Contains(body, "<i>XSS</i>") {
			t.Error("unescaped HTML")
		}
		if !strings.Contains(body, "&lt;i&gt;XSS&lt;/i&gt;") {
			t.Error("escaped billing name missing")
		}
	})

	t.Run("english", func(t *testing.T) {
		body := get(t, "en")
		for _, want := range []string{
			"Family no.", "Billing name", "Payer email", "Name, family no. or email",
			"{children} family row(s) selected",
			"2 row(s) without a valid email address",
			"Payer email missing",
			"Invalid payer email:",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("body does not contain %q", want)
			}
		}
		if strings.Contains(body, "Email du payeur") {
			t.Error("French in English page")
		}
	})

	t.Run("disabled rows have no name attribute", func(t *testing.T) {
		body := get(t, "fr")
		if n := strings.Count(body, `type="checkbox" disabled`); n != 2 {
			t.Fatalf("disabled checkboxes = %d, want 2", n)
		}
		for _, line := range strings.Split(body, "\n") {
			if strings.Contains(line, "disabled") && strings.Contains(line, "checkbox") {
				if strings.Contains(line, "name=") || strings.Contains(line, "value=") {
					t.Errorf("disabled checkbox is submittable: %s", line)
				}
			}
		}
		if n := strings.Count(body, `name="child"`); n != 2 {
			t.Errorf("selectable checkboxes = %d, want 2", n)
		}
		if n := strings.Count(body, `class="row-disabled"`); n != 2 {
			t.Errorf("disabled rows = %d, want 2", n)
		}
	})

	t.Run("search text includes family number and invalid value", func(t *testing.T) {
		body := get(t, "fr")
		if !strings.Contains(body, `data-search="dupont 10 dupont a@example.com"`) {
			t.Errorf("search attribute missing: %s", body)
		}
	})

	t.Run("no notice when all rows have an email", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, rows[:1])
		body := do(s, httptest.NewRequest(http.MethodGet, "/children", nil)).Body.String()
		if strings.Contains(body, "sans adresse email valide") || strings.Contains(body, "disabled") {
			t.Error("notice or disabled row without no-email entries")
		}
	})
}

func TestChildrenPageExcelDataKeepsChildLabels(t *testing.T) {
	s, st := newTestServer(t)
	seed(t, st, threeChildren)
	body := do(s, httptest.NewRequest(http.MethodGet, "/children", nil)).Body.String()
	if strings.Contains(body, "N° famille") || strings.Contains(body, "disabled") || strings.Contains(body, "row-disabled") {
		t.Errorf("family labels in child mode: %s", body)
	}
}

func TestHomeFamilyStats(t *testing.T) {
	s, st := newTestServer(t)
	seed(t, st, []store.ImportRow{
		{ParentName: "A", ChildName: "A", Email: "a@example.com", FamilyNumber: "10"},
		{ParentName: "A", ChildName: "A", Email: "b@example.com", FamilyNumber: "10"},
	})
	body := do(s, httptest.NewRequest(http.MethodGet, "/", nil)).Body.String()
	for _, want := range []string{"1 Familles", "2 Adresses email", "Choisir"} {
		if !strings.Contains(body, want) {
			t.Errorf("home does not contain %q", want)
		}
	}
	if strings.Contains(body, "Enfants</td>") || strings.Contains(body, "Parents</td>") {
		t.Error("child labels with family data")
	}
}
