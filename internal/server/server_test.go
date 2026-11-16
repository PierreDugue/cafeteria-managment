package server

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"cafeteria-management/internal/mailer"
	"cafeteria-management/internal/store"
)

func newTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	s, st, _ := newTestServerAt(t)
	return s, st
}

// newTestServerAt is newTestServer that also returns the database path, for
// tests that need to set timestamps by hand (see setUsedAt).
func newTestServerAt(t *testing.T) (*Server, *store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.db")
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	s, err := New("test", st, &fakeQueue{provider: mailer.ProviderTest, interval: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return s, st, path
}

// do sends a request as the local browser would.
func do(s *Server, req *http.Request) *httptest.ResponseRecorder {
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Accept-Language", "fr-FR")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

// totals returns the counts of the first list (zero value if none).
func totals(t *testing.T, st *store.Store) store.List {
	t.Helper()
	id := mainList(t, st)
	if id == 0 {
		return store.List{}
	}
	l, err := st.List(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestRoutes(t *testing.T) {
	s, _ := newTestServer(t)

	tests := []struct {
		path     string
		wantCode int
		wantBody string
	}{
		{"/", http.StatusOK, "Gestion cantine"},
		{"/import", http.StatusOK, "Importer le fichier des familles"},
		{"/healthz", http.StatusOK, "ok"},
		{"/static/style.css", http.StatusOK, "body"},
		{"/missing", http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := do(s, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("body does not contain %q", tt.wantBody)
			}
		})
	}
}

func TestEnglish(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "localhost:8080"
	req.Header.Set("Accept-Language", "en-GB,en;q=0.9")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "Cafeteria management") || !strings.Contains(rec.Body.String(), `lang="en"`) {
		t.Fatalf("not English: %s", rec.Body.String())
	}
}

func TestRejectsForeignRequests(t *testing.T) {
	s, _ := newTestServer(t)

	t.Run("other host", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = "evil.example:8080"
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d", rec.Code)
		}
	})

	t.Run("cross-site post", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/import/confirm", strings.NewReader("token=x"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		if rec := do(s, req); rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d", rec.Code)
		}
	})
}

func TestImportFlow(t *testing.T) {
	s, st := newTestServer(t)

	// First import.
	rec := upload(t, s, "familles.xlsx", xlsx(t, [][]any{
		{"Nom parent", "Nom enfant", "Adresse email"},
		{"Marie Dupont", "Léa Dupont", "marie@example.fr"},
		{"Marie Dupont", "Tom Dupont", "marie@example.fr"},
		{"Paul Martin", "Zoé Martin", "paul@example.fr"},
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("preview status = %d: %s", rec.Code, rec.Body)
	}
	token := tokenFrom(t, rec.Body.String())

	// Nothing saved before confirmation.
	if tot := totals(t, st); tot.Children != 0 {
		t.Fatalf("saved before confirm: %+v", tot)
	}

	rec = confirm(s, token)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/import?done=") {
		t.Fatalf("confirm = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if tot := totals(t, st); tot.Children != 3 || tot.Parents != 2 {
		t.Fatalf("totals = %+v", tot)
	}

	done := do(s, httptest.NewRequest(http.MethodGet, rec.Header().Get("Location"), nil))
	if !strings.Contains(done.Body.String(), "Import de « familles.xlsx » enregistré") {
		t.Errorf("no confirmation message: %s", done.Body)
	}

	// A token works only once.
	if rec := confirm(s, token); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("reused token: status = %d", rec.Code)
	}

	// Second file without Tom and Paul: the preview lists the deletions.
	rec = upload(t, s, "familles-v2.xlsx", xlsx(t, [][]any{
		{"Nom parent", "Nom enfant", "Adresse email"},
		{"Marie Dupont", "Léa Dupont", "marie@example.fr"},
	}))
	body := rec.Body.String()
	for _, want := range []string{"Enfants qui seront supprimés (2)", "Tom Dupont", "Zoé Martin", "supprimer 2 enfant(s)"} {
		if !strings.Contains(body, want) {
			t.Errorf("preview does not contain %q", want)
		}
	}
	confirm(s, tokenFrom(t, body))
	if tot := totals(t, st); tot.Children != 1 || tot.Parents != 1 {
		t.Fatalf("totals after second import = %+v", tot)
	}
}

// Confirming an old preview after another import must not apply changes the
// preview did not show.
func TestImportStalePreview(t *testing.T) {
	s, st := newTestServer(t)
	header := []any{"Nom parent", "Nom enfant", "Adresse email"}
	seed(t, st, []store.ImportRow{{ParentName: "Old", ChildName: "Old", Email: "old@example.fr"}}) // the list both previews replace

	tokenA := tokenFrom(t, upload(t, s, "a.xlsx", xlsx(t, [][]any{header, {"Marie", "Léa", "marie@example.fr"}})).Body.String())
	tokenB := tokenFrom(t, upload(t, s, "b.xlsx", xlsx(t, [][]any{header, {"Paul", "Zoé", "paul@example.fr"}})).Body.String())

	if rec := confirm(s, tokenB); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm B = %d", rec.Code)
	}
	rec := confirm(s, tokenA)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Les données ont changé") {
		t.Fatalf("confirm A = %d: %s", rec.Code, rec.Body)
	}

	imports, err := st.Imports(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(imports) != 2 || imports[0].FileName != "b.xlsx" {
		t.Fatalf("imports = %+v", imports)
	}
}

func TestImportRejected(t *testing.T) {
	s, st := newTestServer(t)

	tests := []struct {
		name     string
		file     string
		content  []byte
		wantCode int
		wantBody string
	}{
		{
			name: "wrong extension", file: "familles.xls", content: []byte("a,b,c"),
			wantCode: http.StatusUnprocessableEntity, wantBody: "Seuls les fichiers .csv et Excel .xlsx",
		},
		{
			name: "not excel", file: "familles.xlsx", content: []byte("hello"),
			wantCode: http.StatusUnprocessableEntity, wantBody: "Ce fichier ne peut pas être lu",
		},
		{
			name: "missing columns", file: "familles.xlsx",
			content:  xlsx(t, [][]any{{"Nom parent", "Classe"}, {"Jean", "CP"}}),
			wantCode: http.StatusUnprocessableEntity, wantBody: "Colonnes introuvables : « nom enfant », « adresse email »",
		},
		{
			name: "row errors", file: "familles.xlsx",
			content: xlsx(t, [][]any{
				{"Nom parent", "Nom enfant", "Adresse email"},
				{"Jean", "Léa", "pas-un-email"},
				{"Jean", "Tom", "jean@example.fr"},
			}),
			wantCode: http.StatusOK, wantBody: "Adresse email invalide : pas-un-email",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := upload(t, s, tt.file, tt.content)
			body := rec.Body.String()
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if !strings.Contains(body, tt.wantBody) {
				t.Errorf("body does not contain %q:\n%s", tt.wantBody, body)
			}
			if strings.Contains(body, `name="token"`) {
				t.Error("a file with errors must not be importable")
			}
		})
	}

	if tot := totals(t, st); tot.Children != 0 {
		t.Errorf("rejected files saved data: %+v", tot)
	}
}

// importTargetFields are the form fields of the default import target: the
// first list if there is one, else a new list "Cantine".
func importTargetFields(t *testing.T, s *Server) url.Values {
	t.Helper()
	if id := mainList(t, s.store); id != 0 {
		return url.Values{"list": {fmt.Sprint(id)}}
	}
	return url.Values{"list": {newListChoice}, "list_name": {"Cantine"}}
}

// upload previews a file into the default target (see importTargetFields).
func upload(t *testing.T, s *Server, name string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	return do(s, uploadRequest(t, name, content, importTargetFields(t, s)))
}

// uploadTo previews a file with the given target fields (list, list_name).
func uploadTo(t *testing.T, s *Server, name string, content []byte, fields url.Values) *httptest.ResponseRecorder {
	t.Helper()
	return do(s, uploadRequest(t, name, content, fields))
}

// uploadRequest builds the multipart request of /import/preview.
func uploadRequest(t *testing.T, name string, content []byte, fields url.Values) *http.Request {
	t.Helper()
	return multipartRequest(t, "/import/preview", fields, name, content)
}

// multipartRequest builds a multipart POST with the given fields and, if
// fileName is not empty, a "file" part.
func multipartRequest(t *testing.T, path string, fields url.Values, fileName string, content []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, vs := range fields {
		for _, v := range vs {
			if err := mw.WriteField(k, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	if fileName != "" {
		fw, err := mw.CreateFormFile("file", fileName)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

// confirm confirms a pending import, ticking the replacement checkbox (the
// server ignores it for a new list).
func confirm(s *Server, token string) *httptest.ResponseRecorder {
	return confirmWith(s, url.Values{"token": {token}, "confirm_replace": {"1"}})
}

// confirmWith posts exactly the given confirmation form.
func confirmWith(s *Server, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/import/confirm", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return do(s, req)
}

var tokenRE = regexp.MustCompile(`name="token" value="([^"]+)"`)

func tokenFrom(t *testing.T, body string) string {
	t.Helper()
	m := tokenRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no confirm token in page:\n%s", body)
	}
	return m[1]
}

func xlsx(t *testing.T, lines [][]any) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	for i, l := range lines {
		ref, err := excelize.CoordinatesToCellName(1, i+1)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.SetSheetRow("Sheet1", ref, &l); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
