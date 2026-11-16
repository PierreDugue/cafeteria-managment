package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"cafeteria-management/internal/store"
)

func tplForm(name, subject, body string) url.Values {
	return url.Values{"name": {name}, "subject": {subject}, "body": {body}}
}

func mustTemplate(t *testing.T, st *store.Store, name, subject, body string) int64 {
	t.Helper()
	id, err := st.CreateTemplate(t.Context(), name, subject, body)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestTemplatesList(t *testing.T) {
	t.Run("empty state and nav", func(t *testing.T) {
		s, _ := newTestServer(t)
		rec := send(s, http.MethodGet, "/templates", nil, "fr")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{"Modèles de message", "Aucun modèle pour l&#39;instant.", `href="/templates/new"`, `<a href="/templates" aria-current="page">Modèles</a>`} {
			if !strings.Contains(body, want) {
				t.Errorf("page does not contain %q", want)
			}
		}
	})

	t.Run("lists templates ordered, escaped", func(t *testing.T) {
		s, st := newTestServer(t)
		id := mustTemplate(t, st, "b <i>name</i>", "Subject <b>x</b>", "body")
		mustTemplate(t, st, "Á first", "S2", "body")
		body := send(s, http.MethodGet, "/templates", nil, "en").Body.String()
		if strings.Contains(body, "<i>name") || strings.Contains(body, "<b>x") {
			t.Error("unescaped HTML")
		}
		if i, j := strings.Index(body, "Á first"), strings.Index(body, "b &lt;i&gt;name"); i < 0 || j < 0 || i > j {
			t.Errorf("order wrong (%d, %d)", i, j)
		}
		for _, want := range []string{fmt.Sprintf(`href="/templates/%d"`, id), fmt.Sprintf(`href="/templates/%d/delete"`, id), "Last updated", "Edit", "Delete"} {
			if !strings.Contains(body, want) {
				t.Errorf("page does not contain %q", want)
			}
		}
		if strings.Contains(body, "No templates yet.") {
			t.Error("empty state shown")
		}
	})

	t.Run("notices", func(t *testing.T) {
		s, _ := newTestServer(t)
		for path, want := range map[string]string{
			"/templates?saved=1":   "Modèle enregistré.",
			"/templates?deleted=1": "Modèle supprimé.",
		} {
			if body := send(s, http.MethodGet, path, nil, "fr").Body.String(); !strings.Contains(body, want) {
				t.Errorf("%s: no %q", path, want)
			}
		}
		for path, want := range map[string]string{
			"/templates?saved=1":   "Template saved.",
			"/templates?deleted=1": "Template deleted.",
		} {
			if body := send(s, http.MethodGet, path, nil, "en").Body.String(); !strings.Contains(body, want) {
				t.Errorf("%s: no %q", path, want)
			}
		}
		for _, path := range []string{"/templates?saved=2", "/templates?saved=", "/templates"} {
			if body := send(s, http.MethodGet, path, nil, "fr").Body.String(); strings.Contains(body, "alert-success") {
				t.Errorf("%s: unexpected notice", path)
			}
		}
	})
}

func TestTemplateCreateFlow(t *testing.T) {
	s, st := newTestServer(t)

	form := send(s, http.MethodGet, "/templates/new", nil, "fr")
	if form.Code != http.StatusOK {
		t.Fatalf("new: status = %d", form.Code)
	}
	for _, want := range []string{`action="/templates"`, `name="name"`, `name="subject"`, `name="body"`, `maxlength="80"`, `maxlength="255"`, `maxlength="20000"`,
		"{{parent_name}}", "{{family_number}}", "données familles (CSV) uniquement", "Nouveau modèle"} {
		if !strings.Contains(form.Body.String(), want) {
			t.Errorf("new form does not contain %q", want)
		}
	}
	for _, bad := range []string{"{{child_name}}", "{{children}}", "données enfants (Excel) uniquement"} {
		if strings.Contains(form.Body.String(), bad) {
			t.Errorf("new form lists removed placeholder %q", bad)
		}
	}

	rec := send(s, http.MethodPost, "/templates", tplForm("  Rappel   cantine ", "  Objet {{family_number}}  ", "Bonjour {{ Parent_Name }}\r\n{{parent_name}}\r\n\r\n"), "fr")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/templates?saved=1" {
		t.Fatalf("status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}
	ts, _ := st.Templates(t.Context())
	if len(ts) != 1 || ts[0].Name != "Rappel cantine" || ts[0].Subject != "Objet {{family_number}}" || ts[0].Body != "Bonjour {{ Parent_Name }}\n{{parent_name}}" {
		t.Fatalf("stored = %+v", ts)
	}
	if loc := rec.Header().Get("Location"); strings.Contains(loc, "Rappel") {
		t.Error("name in redirect URL")
	}
}

func TestTemplateCreateErrors(t *testing.T) {
	long := func(n int) string { return strings.Repeat("é", n) }
	tests := []struct {
		name                     string
		in                       url.Values
		wantCode                 int
		wantFR, wantEN, wantNone string
	}{
		{"duplicate ignoring case accents spaces", tplForm(" ménu ", "S", "B"), 422, "Un modèle porte déjà ce nom.", "A template with this name already exists.", ""},
		{"empty name", tplForm("", "S", "B"), 422, "Le nom du modèle est obligatoire.", "The template name is required.", ""},
		{"name 81 runes", tplForm(long(81), "S", "B"), 422, "au plus 80 caractères", "at most 80 characters", ""},
		{"name 80 runes ok", tplForm(long(80), "S", "B"), 303, "", "", ""},
		{"name with control char", tplForm("a\tb", "S", "B"), 422, "contient un caractère non autorisé", "contains a character that is not allowed", ""},
		{"empty subject", tplForm("N", "", "B"), 422, "Objet : Ce champ est obligatoire.", "Subject: This field is required.", ""},
		{"empty body", tplForm("N", "S", ""), 422, "Message : Ce champ est obligatoire.", "Message: This field is required.", ""},
		{"subject 256", tplForm("N", long(256), "B"), 422, "Trop long : 255 caractères au maximum.", "Too long: 255 characters at most.", ""},
		{"subject 255 ok", tplForm("N", long(255), "B"), 303, "", "", ""},
		{"body 20001", tplForm("N", "S", long(20001)), 422, "Trop long : 20000 caractères au maximum.", "Too long: 20000 characters at most.", ""},
		{"body 20000 ok", tplForm("N", "S", long(20000)), 303, "", "", ""},
		{"unknown placeholder", tplForm("N", "S", "{{parent_nam}}"), 422, "« {{parent_nam}} » n&#39;est pas un champ", "“{{parent_nam}}” is not a known placeholder.", ""},
		{"malformed placeholder", tplForm("N", "{{parent_name}", "B"), 422, "est mal écrit", "is badly written", ""},
		{"children in subject is unknown", tplForm("N", "{{children}}", "B"), 422, "« {{children}} » n&#39;est pas un champ", "“{{children}}” is not a known placeholder.", ""},
		{"child_name in subject is unknown", tplForm("N", "{{child_name}}", "B"), 422, "« {{child_name}} » n&#39;est pas un champ", "“{{child_name}}” is not a known placeholder.", ""},
		{"child_name in body is unknown", tplForm("N", "S", "{{ Child_Name }}"), 422, "n&#39;est pas un champ automatique connu", "is not a known placeholder.", ""},
		{"children in body is unknown", tplForm("N", "S", "{{children}}"), 422, "« {{children}} » n&#39;est pas un champ", "“{{children}}” is not a known placeholder.", ""},
		{"control char in subject", tplForm("N", "a\x00b", "B"), 422, "Contient un caractère non autorisé", "Contains a character that is not allowed", ""},
		{"all placeholders accepted", tplForm("N", "{{parent_name}} {{family_number}}", "{{parent_name}}{{family_number}}"), 303, "", "", ""},
		{"no fields at all", url.Values{}, 422, "Le nom du modèle est obligatoire.", "The template name is required.", ""},
	}
	for _, tt := range tests {
		for lang, want := range map[string]string{"fr": tt.wantFR, "en": tt.wantEN} {
			t.Run(tt.name+" "+lang, func(t *testing.T) {
				s, st := newTestServer(t)
				mustTemplate(t, st, "Menu", "s", "b")
				rec := send(s, http.MethodPost, "/templates", tt.in, lang)
				if rec.Code != tt.wantCode {
					t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantCode, rec.Body)
				}
				ts, _ := st.Templates(t.Context())
				if tt.wantCode == http.StatusSeeOther {
					if len(ts) != 2 {
						t.Errorf("templates = %d, want 2", len(ts))
					}
					return
				}
				if len(ts) != 1 {
					t.Errorf("a refused template was saved: %+v", ts)
				}
				body := rec.Body.String()
				if !strings.Contains(body, want) {
					t.Errorf("page does not contain %q: %s", want, body)
				}
				// The typed values come back.
				if n := tt.in.Get("name"); n != "" && len(n) < 100 && !strings.Contains(body, `value="`+strings.TrimSpace(n)+`"`) && !strings.Contains(body, `value="`+n+`"`) {
					t.Errorf("name %q not kept", n)
				}
			})
		}
	}
}

func TestTemplateFormKeepsValuesEscaped(t *testing.T) {
	s, _ := newTestServer(t)
	rec := send(s, http.MethodPost, "/templates", tplForm(`"><script>x</script>`, `<b>S</b> {{nope}}`, `</textarea><script>y</script>`), "fr")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "<script>x") || strings.Contains(body, "<script>y") || strings.Contains(body, "<b>S") {
		t.Errorf("unescaped HTML: %s", body)
	}
	if !strings.Contains(body, "&lt;/textarea&gt;&lt;script&gt;y") {
		t.Error("body not kept")
	}
}

func TestTemplateEditFlow(t *testing.T) {
	s, st := newTestServer(t)
	id := mustTemplate(t, st, "Menu", "Objet", "Corps")
	other := mustTemplate(t, st, "Autre", "O2", "C2")
	path := fmt.Sprintf("/templates/%d", id)

	rec := send(s, http.MethodGet, path, nil, "fr")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, want := range []string{"Modifier le modèle", `action="` + path + `"`, `value="Menu"`, `value="Objet"`, "Corps"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("edit form does not contain %q", want)
		}
	}

	t.Run("update", func(t *testing.T) {
		rec := send(s, http.MethodPost, path, tplForm("MÉNU", "Nouvel objet", "Nouveau corps"), "fr")
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/templates?saved=1" {
			t.Fatalf("status %d location %q", rec.Code, rec.Header().Get("Location"))
		}
		got, _ := st.Template(t.Context(), id)
		if got.Name != "MÉNU" || got.Subject != "Nouvel objet" || got.Body != "Nouveau corps" {
			t.Errorf("stored = %+v", got)
		}
	})

	t.Run("name taken by another keeps values", func(t *testing.T) {
		rec := send(s, http.MethodPost, path, tplForm(" autre ", "S", "B"), "en")
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "A template with this name already exists.") {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), `action="`+path+`"`) {
			t.Error("form should still post to the template")
		}
		if got, _ := st.Template(t.Context(), other); got.Name != "Autre" || got.Subject != "O2" {
			t.Errorf("other changed: %+v", got)
		}
	})

	t.Run("validation error", func(t *testing.T) {
		rec := send(s, http.MethodPost, path, tplForm("Menu", "S", "{{oops}}"), "fr")
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "{{oops}}") {
			t.Fatalf("status %d", rec.Code)
		}
		if got, _ := st.Template(t.Context(), id); got.Body != "Nouveau corps" {
			t.Errorf("template changed: %+v", got)
		}
	})

	t.Run("unknown id gives 404 with the list", func(t *testing.T) {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			rec := send(s, method, "/templates/9999", tplForm("N", "S", "B"), "fr")
			if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Ce modèle n&#39;existe pas (ou plus).") || !strings.Contains(rec.Body.String(), "Modèles de message") {
				t.Errorf("%s: status %d", method, rec.Code)
			}
		}
		if ts, _ := st.Templates(t.Context()); len(ts) != 2 {
			t.Errorf("templates = %+v", ts)
		}
	})
}

func TestTemplateDeleteFlow(t *testing.T) {
	s, st := newTestServer(t)
	id := mustTemplate(t, st, "Menu <i>1</i>", "Objet <b>x</b>", "Corps")
	path := fmt.Sprintf("/templates/%d/delete", id)

	rec := send(s, http.MethodGet, path, nil, "fr")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Supprimer définitivement le modèle « Menu &lt;i&gt;1&lt;/i&gt; » ?", `method="post" action="` + path + `"`, "Annuler", "Objet &lt;b&gt;x&lt;/b&gt;"} {
		if !strings.Contains(body, want) {
			t.Errorf("confirmation does not contain %q", want)
		}
	}
	if strings.Contains(body, "<i>1") || strings.Contains(body, "<b>x") {
		t.Error("unescaped HTML")
	}
	if en := send(s, http.MethodGet, path, nil, "en").Body.String(); !strings.Contains(en, "Delete the template “Menu") {
		t.Errorf("english confirmation: %s", en)
	}
	if _, err := st.Template(t.Context(), id); err != nil {
		t.Fatalf("GET deleted the template: %v", err)
	}

	rec = send(s, http.MethodPost, path, nil, "fr")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/templates?deleted=1" {
		t.Fatalf("status %d location %q", rec.Code, rec.Header().Get("Location"))
	}
	if _, err := st.Template(t.Context(), id); err == nil {
		t.Fatal("template still there")
	}

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, path},
		{http.MethodPost, path},
		{http.MethodGet, "/templates/9999/delete"},
		{http.MethodPost, "/templates/9999/delete"},
	} {
		rec := send(s, tc.method, tc.path, nil, "en")
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "This template does not exist (any more).") {
			t.Errorf("%s %s: status %d", tc.method, tc.path, rec.Code)
		}
	}
}

func TestTemplateBadIDs(t *testing.T) {
	s, st := newTestServer(t)
	mustTemplate(t, st, "Menu", "S", "B")
	for _, id := range []string{"abc", "0", "-1", "1.5", "99999999999999999999", "1abc"} {
		for _, tc := range []struct{ method, suffix string }{
			{http.MethodGet, ""}, {http.MethodPost, ""}, {http.MethodGet, "/delete"}, {http.MethodPost, "/delete"},
		} {
			rec := send(s, tc.method, "/templates/"+id+tc.suffix, tplForm("N", "S", "B"), "fr")
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s /templates/%s%s: status %d, want 404", tc.method, id, tc.suffix, rec.Code)
			}
		}
	}
	if ts, _ := st.Templates(t.Context()); len(ts) != 1 || ts[0].Name != "Menu" {
		t.Errorf("templates = %+v", ts)
	}
}

func TestTemplateRoutesMethods(t *testing.T) {
	s, st := newTestServer(t)
	id := mustTemplate(t, st, "Menu", "S", "B")
	for _, tc := range []struct{ method, path string }{
		{http.MethodPut, "/templates"},
		{http.MethodDelete, fmt.Sprintf("/templates/%d", id)},
		{http.MethodPut, fmt.Sprintf("/templates/%d/delete", id)},
		{http.MethodDelete, fmt.Sprintf("/templates/%d/delete", id)},
		{http.MethodPost, "/templates/new"},
	} {
		rec := send(s, tc.method, tc.path, nil, "fr")
		if rec.Code == http.StatusOK || rec.Code == http.StatusSeeOther {
			t.Errorf("%s %s: status %d", tc.method, tc.path, rec.Code)
		}
	}
	if _, err := st.Template(t.Context(), id); err != nil {
		t.Fatalf("template removed: %v", err)
	}
}

func TestTemplatePostsRejectCrossSite(t *testing.T) {
	s, st := newTestServer(t)
	id := mustTemplate(t, st, "Menu", "S", "B")
	seed(t, st, threeChildren)
	cid := childIDs(t, st)["Léa"]

	posts := map[string]url.Values{
		"/templates":                            tplForm("New", "S", "B"),
		fmt.Sprintf("/templates/%d", id):        tplForm("Changed", "S", "B"),
		fmt.Sprintf("/templates/%d/delete", id): nil,
		"/compose":                              {"version": {"1"}, "child": {fmt.Sprint(cid)}, "action": {"save_template"}, "template_name": {"Evil"}, "subject": {"S"}, "body": {"B"}},
	}
	for path, v := range posts {
		for _, hdr := range []map[string]string{
			{"Sec-Fetch-Site": "cross-site"},
			{"Origin": "https://evil.example"},
		} {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(v.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			for k, val := range hdr {
				req.Header.Set(k, val)
			}
			if rec := do(s, req); rec.Code != http.StatusForbidden {
				t.Errorf("POST %s with %v: status %d, want 403", path, hdr, rec.Code)
			}
		}
	}
	ts, _ := st.Templates(t.Context())
	if len(ts) != 1 || ts[0].Name != "Menu" {
		t.Errorf("a cross-site request changed templates: %+v", ts)
	}
}

func TestTemplatePostsRejectOtherHosts(t *testing.T) {
	s, st := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/templates", strings.NewReader(tplForm("N", "S", "B").Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Host = "evil.example"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d", rec.Code)
	}
	if ts, _ := st.Templates(t.Context()); len(ts) != 0 {
		t.Errorf("templates = %+v", ts)
	}
}

func TestTemplateOversizedBody(t *testing.T) {
	s, st := newTestServer(t)
	id := mustTemplate(t, st, "Menu", "S", "B")
	huge := tplForm("N", "S", "B")
	huge.Set("junk", strings.Repeat("a", 1<<20+10))

	for _, path := range []string{"/templates", fmt.Sprintf("/templates/%d", id)} {
		rec := send(s, http.MethodPost, path, huge, "fr")
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("POST %s: status = %d, want 422", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "trop volumineuse") {
			t.Errorf("POST %s: too_large message missing", path)
		}
	}
	if ts, _ := st.Templates(t.Context()); len(ts) != 1 || ts[0].Name != "Menu" || ts[0].Subject != "S" {
		t.Errorf("templates = %+v", ts)
	}
}

func TestTemplatesLogsNothingPersonal(t *testing.T) {
	logs := captureLogs(t)
	s, st := newTestServer(t)

	rec := send(s, http.MethodPost, "/templates", tplForm("Secret Name", "Secret Subject", "Secret Body {{parent_name}}"), "fr")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d", rec.Code)
	}
	ts, _ := st.Templates(t.Context())
	path := fmt.Sprintf("/templates/%d", ts[0].ID)
	send(s, http.MethodPost, path, tplForm("Secret Name 2", "Secret Subject", "Secret Body"), "fr")
	send(s, http.MethodPost, path, tplForm("Secret Name 2", "Secret Subject", "Secret {{bad}}"), "fr")
	send(s, http.MethodPost, "/templates", tplForm("Secret Name 2", "Secret Subject", "Secret Body"), "fr") // duplicate
	send(s, http.MethodPost, path+"/delete", nil, "fr")

	out := logs.String()
	if !strings.Contains(out, "template created") || !strings.Contains(out, "template deleted") {
		t.Errorf("expected info logs with the ID: %s", out)
	}
	if strings.Contains(out, "Secret") {
		t.Errorf("log leaks template text: %s", out)
	}
}
