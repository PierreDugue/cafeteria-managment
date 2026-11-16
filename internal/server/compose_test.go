package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"cafeteria-management/internal/store"
)

// send posts (or gets) as the local browser would, in the given language.
func send(s *Server, method, path string, v url.Values, lang string) *httptest.ResponseRecorder {
	var req *http.Request
	if method == http.MethodGet {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Accept-Language", lang)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

// composeVals is a compose form: current version, the IDs, then key/value
// pairs (action, subject, body...).
func composeVals(t *testing.T, st *store.Store, ids []int64, kv ...string) url.Values {
	t.Helper()
	v := url.Values{"version": {fmt.Sprint(currentVersion(t, st))}}
	for _, id := range ids {
		v.Add("child", fmt.Sprint(id))
	}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

func idsOf(m map[string]int64, names ...string) []int64 {
	out := make([]int64, len(names))
	for i, n := range names {
		out[i] = m[n]
	}
	return out
}

// silva: two families sharing one payer address, plus another family.
var silvaRows = []store.ImportRow{
	{ParentName: "SILVA Ana", ChildName: "SILVA Ana", Email: "silva@example.com", FamilyNumber: "1234"},
	{ParentName: "SILVA Rui", ChildName: "SILVA Rui", Email: "silva@example.com", FamilyNumber: "1250"},
	{ParentName: "DUPONT", ChildName: "DUPONT", Email: "dupont@example.com", FamilyNumber: "10"},
}

var previewBody = regexp.MustCompile(`(?s)<div class="preview-body">(.*?)</div>`)

func previewText(t *testing.T, body string) string {
	t.Helper()
	m := previewBody.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no preview in page: %s", body)
	}
	return m[1]
}

func TestComposePageContent(t *testing.T) {
	t.Run("children data", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		ids := childIDs(t, st)
		rec := send(s, http.MethodPost, "/compose", composeVals(t, st, idsOf(ids, "Zoé", "Léa")), "fr")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{
			`name="version" value="1"`,
			fmt.Sprintf(`name="child" value="%d"`, ids["Zoé"]),
			fmt.Sprintf(`name="child" value="%d"`, ids["Léa"]),
			"{{parent_name}}",
			"Nom du parent", "Écrire le message", "Aucun modèle enregistré.", `href="/templates"`,
			`value="preview"`, `value="save_template"`,
			`maxlength="255"`, `maxlength="20000"`, `maxlength="80"`,
			"<th>Parent</th>",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("page does not contain %q", want)
			}
		}
		for _, bad := range []string{"{{family_number}}", "{{child_name}}", "{{children}}"} {
			if strings.Contains(body, bad) {
				t.Errorf("page lists %q for children data", bad)
			}
		}
		if strings.Contains(body, `class="preview"`) {
			t.Error("preview shown before asking for it")
		}
		if strings.Contains(body, `name="child" value="`+fmt.Sprint(ids["Émile"])+`"`) {
			t.Error("unselected child posted back")
		}
	})

	t.Run("family data", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, silvaRows)
		ids := childIDs(t, st)
		rec := send(s, http.MethodPost, "/compose", composeVals(t, st, idsOf(ids, "SILVA Ana", "SILVA Rui", "DUPONT")), "fr")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{"2 email(s) seront envoyés, un par adresse.", "10 DUPONT", "1234 SILVA Ana, 1250 SILVA Rui", "{{parent_name}}", "{{family_number}}"} {
			if !strings.Contains(body, want) {
				t.Errorf("page does not contain %q", want)
			}
		}
		for _, bad := range []string{"Payeur", "<th>Parent</th>", "{{child_name}}", "{{children}}"} {
			if strings.Contains(body, bad) {
				t.Errorf("page contains %q", bad)
			}
		}
	})

	t.Run("english", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		ids := childIDs(t, st)
		body := send(s, http.MethodPost, "/compose", composeVals(t, st, idsOf(ids, "Léa")), "en").Body.String()
		for _, want := range []string{"Write the message", "No saved templates.", "Show preview", "Save template", "Parent name"} {
			if !strings.Contains(body, want) {
				t.Errorf("page does not contain %q", want)
			}
		}
	})

	t.Run("non-compose action shows an empty message", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		ids := childIDs(t, st)
		body := send(s, http.MethodPost, "/compose", composeVals(t, st, idsOf(ids, "Léa"), "action", "bogus", "subject", "Mon objet"), "fr").Body.String()
		if !strings.Contains(body, `value="Mon objet"`) {
			t.Errorf("subject lost: %s", body)
		}
	})
}

func TestComposeStaleVersion(t *testing.T) {
	setup := func(t *testing.T) (*Server, *store.Store, map[string]int64) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		return s, st, childIDs(t, st)
	}
	staleFR := "Les données ont changé depuis votre sélection"
	staleEN := "The data changed since your selection"

	for _, lang := range []struct{ code, want string }{{"fr", staleFR}, {"en", staleEN}} {
		for name, version := range map[string][]string{
			"missing":               nil,
			"empty":                 {""},
			"not a number":          {"abc"},
			"float":                 {"1.0"},
			"negative":              {"-1"},
			"older":                 {"0"},
			"newer":                 {"2"},
			"overflow":              {"99999999999999999999"},
			"first of two is wrong": {"0", "1"},
		} {
			t.Run(lang.code+" "+name, func(t *testing.T) {
				s, st, ids := setup(t)
				v := composeVals(t, st, idsOf(ids, "Léa"), "action", "preview", "subject", "S", "body", "B")
				delete(v, "version")
				for _, x := range version {
					v.Add("version", x)
				}
				rec := send(s, http.MethodPost, "/compose", v, lang.code)
				if rec.Code != http.StatusConflict {
					t.Fatalf("status = %d, want 409", rec.Code)
				}
				body := rec.Body.String()
				if !strings.Contains(body, lang.want) {
					t.Errorf("stale message missing: %s", body)
				}
				if !strings.Contains(body, `name="child"`) || strings.Contains(body, "preview-body") || strings.Contains(body, `name="subject"`) {
					t.Error("must show the children list only")
				}
			})
		}
	}

	t.Run("import after the list", func(t *testing.T) {
		s, st, ids := setup(t)
		v := composeVals(t, st, idsOf(ids, "Léa"))
		seed(t, st, threeChildren) // same content, new import
		rec := send(s, http.MethodPost, "/compose", v, "fr")
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d", rec.Code)
		}
		// The re-rendered list carries the new version.
		if !strings.Contains(rec.Body.String(), `name="version" value="2"`) {
			t.Errorf("new version missing: %s", rec.Body)
		}
	})

	t.Run("stale is checked for every action", func(t *testing.T) {
		s, st, ids := setup(t)
		for _, a := range []string{"load", "preview", "save_template", ""} {
			v := composeVals(t, st, idsOf(ids, "Léa"), "action", a, "template_name", "X", "subject", "S", "body", "B")
			v.Set("version", "0")
			if rec := send(s, http.MethodPost, "/compose", v, "fr"); rec.Code != http.StatusConflict {
				t.Errorf("action %q: status = %d", a, rec.Code)
			}
		}
		if ts, _ := st.Templates(t.Context()); len(ts) != 0 {
			t.Errorf("a stale request saved a template: %+v", ts)
		}
	})

	t.Run("no ids wins over stale version", func(t *testing.T) {
		s, _, _ := setup(t)
		rec := send(s, http.MethodPost, "/compose", url.Values{"version": {"0"}}, "fr")
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d", rec.Code)
		}
	})
}

func TestComposePreview(t *testing.T) {
	t.Run("renders for the chosen recipient", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		ids := childIDs(t, st)
		parents := map[string]int64{}
		cs, _ := st.Children(t.Context())
		for _, c := range cs {
			parents[c.Email] = c.ParentID
		}
		for email, name := range map[string]string{"anne@example.com": "Anne Martin", "bob@example.com": "Bob Durand"} {
			v := composeVals(t, st, idsOf(ids, "Zoé", "Émile", "Léa"), "action", "preview",
				"preview_to", fmt.Sprint(parents[email]),
				"subject", "Pour {{parent_name}}", "body", "Bonjour {{ Parent_Name }},\r\nMerci\r\n")
			rec := send(s, http.MethodPost, "/compose", v, "fr")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			body := rec.Body.String()
			text := previewText(t, body)
			if !strings.Contains(text, "Bonjour "+name+",") {
				t.Errorf("%s: preview = %q", email, text)
			}
			if !strings.Contains(body, "&lt;"+email+"&gt;") {
				t.Errorf("%s: recipient address not shown in preview", email)
			}
			if !strings.Contains(body, fmt.Sprintf(`<option value="%d" selected>`, parents[email])) {
				t.Errorf("%s: picker not kept on the chosen recipient", email)
			}
		}
	})

	t.Run("subject and body use the parent name", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		ids := childIDs(t, st)
		v := composeVals(t, st, idsOf(ids, "Zoé", "Émile"), "action", "preview", "subject", "Pour {{parent_name}}", "body", "{{parent_name}}")
		body := send(s, http.MethodPost, "/compose", v, "fr").Body.String()
		if !strings.Contains(body, "<strong>Objet</strong> Pour Anne Martin") {
			t.Errorf("subject: %s", body)
		}
		if text := previewText(t, body); text != "Anne Martin" {
			t.Errorf("body = %q", text)
		}
	})

	t.Run("first recipient by default and for an unknown preview_to", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		ids := childIDs(t, st)
		for name, pt := range map[string]string{"missing": "", "unknown": "424242", "not a number": "abc", "negative": "-5"} {
			v := composeVals(t, st, idsOf(ids, "Zoé", "Léa"), "action", "preview", "subject", "S", "body", "Hi {{parent_name}}")
			if pt != "" {
				v.Set("preview_to", pt)
			}
			rec := send(s, http.MethodPost, "/compose", v, "fr")
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: status = %d", name, rec.Code)
			}
			// Anne Martin sorts before Bob Durand.
			if text := previewText(t, rec.Body.String()); text != "Hi Anne Martin" {
				t.Errorf("%s: preview = %q", name, text)
			}
		}
	})

	t.Run("problems are shown and no text is rendered", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		ids := childIDs(t, st)
		v := composeVals(t, st, idsOf(ids, "Léa"), "action", "preview", "subject", "S", "body", "A {{parent_nam}} B {{family_number}} {{")
		rec := send(s, http.MethodPost, "/compose", v, "fr")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, "preview-body") {
			t.Error("rendered text shown with problems")
		}
		for _, want := range []string{
			"À corriger",
			"{{parent_nam}}", "n&#39;est pas un champ automatique connu",
			"{{family_number}}", "n&#39;est pas disponible avec les données importées",
			"est mal écrit",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("page does not contain %q", want)
			}
		}
		// The typed text is kept.
		if !strings.Contains(body, "A {{parent_nam}} B") {
			t.Error("body not kept")
		}
	})

	t.Run("empty and too long", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		ids := childIDs(t, st)
		v := composeVals(t, st, idsOf(ids, "Léa"), "action", "preview", "subject", "", "body", strings.Repeat("é", 20001))
		body := send(s, http.MethodPost, "/compose", v, "en").Body.String()
		for _, want := range []string{"To fix", "Subject: This field is required.", "Message: Too long: 20000 characters at most."} {
			if !strings.Contains(body, want) {
				t.Errorf("page does not contain %q", want)
			}
		}
		if strings.Contains(body, "preview-body") {
			t.Error("rendered text shown with problems")
		}
	})

	t.Run("family placeholder with children data is a problem in english", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		ids := childIDs(t, st)
		v := composeVals(t, st, idsOf(ids, "Léa"), "action", "preview", "subject", "S", "body", "{{family_number}}")
		body := send(s, http.MethodPost, "/compose", v, "en").Body.String()
		if !strings.Contains(body, "is not available with the imported data.") {
			t.Errorf("body: %s", body)
		}
	})

	t.Run("html is escaped", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, []store.ImportRow{{ParentName: `<i>Parent</i>`, ChildName: `<b>Kid</b>`, Email: "x@example.com"}})
		ids := childIDs(t, st)
		v := composeVals(t, st, idsOf(ids, "<b>Kid</b>"), "action", "preview",
			"subject", `<u>S</u> {{parent_name}}`, "body", `<script>alert(1)</script> {{parent_name}}`)
		body := send(s, http.MethodPost, "/compose", v, "fr").Body.String()
		for _, raw := range []string{"<script>", "<i>Parent", "<b>Kid", "<u>S"} {
			if strings.Contains(body, raw) {
				t.Errorf("unescaped %q in page", raw)
			}
		}
		text := previewText(t, body)
		for _, want := range []string{"&lt;script&gt;alert(1)&lt;/script&gt;", "&lt;i&gt;Parent&lt;/i&gt;", "&lt;script&gt;alert(1)&lt;/script&gt; &lt;i&gt;Parent&lt;/i&gt;"} {
			if !strings.Contains(text, want) {
				t.Errorf("preview %q does not contain %q", text, want)
			}
		}
	})

	t.Run("child placeholders are unknown with children data in both languages", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		ids := childIDs(t, st)
		for lang, want := range map[string]string{"fr": "n&#39;est pas un champ automatique connu", "en": "is not a known placeholder."} {
			for _, tok := range []string{"{{child_name}}", "{{children}}"} {
				for field, other := range map[string]string{"subject": "body", "body": "subject"} {
					v := composeVals(t, st, idsOf(ids, "Léa"), "action", "preview", other, "S", field, tok)
					body := send(s, http.MethodPost, "/compose", v, lang).Body.String()
					if !strings.Contains(body, want) || !strings.Contains(body, tok) || strings.Contains(body, "preview-body") {
						t.Errorf("%s %s in %s: %s", lang, tok, field, body)
					}
				}
			}
		}
	})

	t.Run("silva case through http", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, silvaRows)
		ids := childIDs(t, st)
		cases := []struct {
			lang  string
			names []string
			want  string
		}{
			{"fr", []string{"SILVA Ana", "SILVA Rui"}, "Chère SILVA Ana et SILVA Rui (1234 et 1250)"},
			{"en", []string{"SILVA Ana", "SILVA Rui"}, "Chère SILVA Ana and SILVA Rui (1234 and 1250)"},
			{"fr", []string{"SILVA Rui"}, "Chère SILVA Rui (1250)"},
			{"fr", []string{"SILVA Ana"}, "Chère SILVA Ana (1234)"},
		}
		for _, c := range cases {
			v := composeVals(t, st, idsOf(ids, c.names...), "action", "preview", "subject", "S", "body", "Chère {{parent_name}} ({{family_number}})")
			rec := send(s, http.MethodPost, "/compose", v, c.lang)
			if text := previewText(t, rec.Body.String()); text != c.want {
				t.Errorf("%s %v: %q, want %q", c.lang, c.names, text, c.want)
			}
		}
	})

	t.Run("child placeholders are unknown with family data in both languages", func(t *testing.T) {
		s, st := newTestServer(t)
		seed(t, st, silvaRows)
		ids := childIDs(t, st)
		for lang, want := range map[string]string{"fr": "n&#39;est pas un champ automatique connu", "en": "is not a known placeholder."} {
			for _, tok := range []string{"{{child_name}}", "{{children}}"} {
				v := composeVals(t, st, idsOf(ids, "DUPONT"), "action", "preview", "subject", "S", "body", tok)
				body := send(s, http.MethodPost, "/compose", v, lang).Body.String()
				if !strings.Contains(body, want) || strings.Contains(body, "not available") || strings.Contains(body, "preview-body") {
					t.Errorf("%s %s: %s", lang, tok, body)
				}
			}
		}
	})
}

func TestComposeLoadTemplate(t *testing.T) {
	setup := func(t *testing.T) (*Server, *store.Store, []int64, int64) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		id, err := st.CreateTemplate(t.Context(), "Rappel", "Objet du modèle", "Corps <b>du</b> modèle {{parent_name}}")
		if err != nil {
			t.Fatal(err)
		}
		return s, st, idsOf(childIDs(t, st), "Léa"), id
	}

	t.Run("replaces subject and body and shows the notice", func(t *testing.T) {
		s, st, ids, tid := setup(t)
		v := composeVals(t, st, ids, "action", "load", "template", fmt.Sprint(tid), "subject", "ancien", "body", "ancien corps")
		rec := send(s, http.MethodPost, "/compose", v, "fr")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{`value="Objet du modèle"`, "Corps &lt;b&gt;du&lt;/b&gt; modèle {{parent_name}}", "Modèle « Rappel » chargé."} {
			if !strings.Contains(body, want) {
				t.Errorf("page does not contain %q", want)
			}
		}
		if strings.Contains(body, "ancien") {
			t.Error("old text kept")
		}
		// The picker is reset to the empty option.
		if !strings.Contains(body, `<option value="" selected>`) || strings.Contains(body, fmt.Sprintf(`<option value="%d" selected`, tid)) {
			t.Errorf("picker not reset: %s", body)
		}
		if !strings.Contains(body, fmt.Sprintf(`<option value="%d">Rappel</option>`, tid)) {
			t.Error("template missing from picker")
		}
	})

	t.Run("english notice", func(t *testing.T) {
		s, st, ids, tid := setup(t)
		v := composeVals(t, st, ids, "action", "load", "template", fmt.Sprint(tid))
		if body := send(s, http.MethodPost, "/compose", v, "en").Body.String(); !strings.Contains(body, "Template “Rappel” loaded.") {
			t.Errorf("body: %s", body)
		}
	})

	t.Run("empty template keeps the text without error", func(t *testing.T) {
		s, st, ids, _ := setup(t)
		for _, tpl := range []string{"", "abc", "0", "-1"} {
			v := composeVals(t, st, ids, "action", "load", "template", tpl, "subject", "garde moi", "body", "mon texte")
			rec := send(s, http.MethodPost, "/compose", v, "fr")
			body := rec.Body.String()
			if rec.Code != http.StatusOK || !strings.Contains(body, `value="garde moi"`) || !strings.Contains(body, "mon texte") {
				t.Errorf("template %q: status %d, text lost", tpl, rec.Code)
			}
			if strings.Contains(body, "alert-error") || strings.Contains(body, "chargé") {
				t.Errorf("template %q: unexpected error or notice", tpl)
			}
		}
	})

	t.Run("missing template gives an error and keeps the text", func(t *testing.T) {
		s, st, ids, _ := setup(t)
		v := composeVals(t, st, ids, "action", "load", "template", "424242", "subject", "garde moi", "body", "mon texte")
		rec := send(s, http.MethodPost, "/compose", v, "fr")
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, "Ce modèle n&#39;existe pas (ou plus).") {
			t.Fatalf("status %d: %s", rec.Code, body)
		}
		if !strings.Contains(body, `value="garde moi"`) || !strings.Contains(body, "mon texte") {
			t.Error("text lost")
		}
	})

	t.Run("loaded text with a problem is not blocked until preview", func(t *testing.T) {
		s, st, ids, _ := setup(t)
		tid, err := st.CreateTemplate(t.Context(), "Familles", "S", "{{family_number}}")
		if err != nil {
			t.Fatal(err)
		}
		v := composeVals(t, st, ids, "action", "load", "template", fmt.Sprint(tid))
		rec := send(s, http.MethodPost, "/compose", v, "fr")
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "À corriger") {
			t.Errorf("status %d", rec.Code)
		}
	})
}

func TestComposeSaveTemplate(t *testing.T) {
	setup := func(t *testing.T) (*Server, *store.Store, []int64) {
		s, st := newTestServer(t)
		seed(t, st, threeChildren)
		return s, st, idsOf(childIDs(t, st), "Léa")
	}

	t.Run("created and listed", func(t *testing.T) {
		s, st, ids := setup(t)
		v := composeVals(t, st, ids, "action", "save_template", "template_name", "  Mon   modèle ", "subject", "Objet {{parent_name}}", "body", "Corps\r\n{{family_number}}\r\n")
		rec := send(s, http.MethodPost, "/compose", v, "fr")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "Modèle « Mon modèle » enregistré.") {
			t.Errorf("notice missing: %s", body)
		}
		if !strings.Contains(body, ">Mon modèle</option>") {
			t.Error("picker not refreshed")
		}
		ts, err := st.Templates(t.Context())
		if err != nil || len(ts) != 1 {
			t.Fatalf("templates = %+v, %v", ts, err)
		}
		if ts[0].Name != "Mon modèle" || ts[0].Subject != "Objet {{parent_name}}" || ts[0].Body != "Corps\n{{family_number}}" {
			t.Errorf("stored = %+v", ts[0])
		}
		list := send(s, http.MethodGet, "/templates", nil, "fr").Body.String()
		if !strings.Contains(list, "Mon modèle") || !strings.Contains(list, "Objet {{parent_name}}") {
			t.Errorf("not on /templates: %s", list)
		}
		// The name input is emptied after a save.
		if strings.Contains(body, `name="template_name" maxlength="80" value="Mon`) {
			t.Error("template name kept after save")
		}
	})

	t.Run("english notice", func(t *testing.T) {
		s, st, ids := setup(t)
		v := composeVals(t, st, ids, "action", "save_template", "template_name", "Reminder", "subject", "S", "body", "B")
		if body := send(s, http.MethodPost, "/compose", v, "en").Body.String(); !strings.Contains(body, "Template “Reminder” saved.") {
			t.Errorf("body: %s", body)
		}
	})

	t.Run("a placeholder of the other mode is accepted in a template", func(t *testing.T) {
		s, st, ids := setup(t)
		v := composeVals(t, st, ids, "action", "save_template", "template_name", "Famille", "subject", "S {{family_number}}", "body", "{{ PARENT_NAME }}")
		if rec := send(s, http.MethodPost, "/compose", v, "fr"); rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
	})

	t.Run("duplicate name gives 422 and keeps name and text", func(t *testing.T) {
		s, st, ids := setup(t)
		if _, err := st.CreateTemplate(t.Context(), "Menu", "s", "b"); err != nil {
			t.Fatal(err)
		}
		v := composeVals(t, st, ids, "action", "save_template", "template_name", " ménu ", "subject", "Mon objet", "body", "Mon corps")
		rec := send(s, http.MethodPost, "/compose", v, "fr")
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{"Un modèle porte déjà ce nom.", `value=" ménu "`, `value="Mon objet"`, "Mon corps"} {
			if !strings.Contains(body, want) {
				t.Errorf("page does not contain %q", want)
			}
		}
		if ts, _ := st.Templates(t.Context()); len(ts) != 1 {
			t.Errorf("templates = %+v", ts)
		}
	})

	t.Run("invalid input gives 422 and saves nothing", func(t *testing.T) {
		s, st, ids := setup(t)
		tests := []struct {
			name, tplName, subject, body, want string
		}{
			{"unknown placeholder", "N", "S", "{{nope}}", "n&#39;est pas un champ automatique connu"},
			{"malformed", "N", "S", "{{parent_name}", "est mal écrit"},
			{"children in subject", "N", "{{children}}", "B", "n&#39;est pas un champ automatique connu"},
			{"child_name in body", "N", "S", "{{child_name}}", "n&#39;est pas un champ automatique connu"},
			{"empty subject", "N", "", "B", "Ce champ est obligatoire."},
			{"empty body", "N", "S", "  ", "Ce champ est obligatoire."},
			{"empty name", "", "S", "B", "Le nom du modèle est obligatoire."},
			{"blank name", "   ", "S", "B", "Le nom du modèle est obligatoire."},
			{"81 rune name", strings.Repeat("é", 81), "S", "B", "au plus 80 caractères"},
			{"control char name", "a\x07b", "S", "B", "contient un caractère non autorisé"},
			{"newline in name", "a\nb", "S", "B", "contient un caractère non autorisé"},
			{"invalid utf8 name", "a\xffb", "S", "B", "contient un caractère non autorisé"},
			{"subject too long", "N", strings.Repeat("a", 256), "B", "Trop long : 255 caractères au maximum."},
			{"body too long", "N", "S", strings.Repeat("a", 20001), "Trop long : 20000 caractères au maximum."},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				v := composeVals(t, st, ids, "action", "save_template", "template_name", tt.tplName, "subject", tt.subject, "body", tt.body)
				rec := send(s, http.MethodPost, "/compose", v, "fr")
				if rec.Code != http.StatusUnprocessableEntity {
					t.Fatalf("status = %d", rec.Code)
				}
				if !strings.Contains(rec.Body.String(), tt.want) {
					t.Errorf("page does not contain %q", tt.want)
				}
			})
		}
		if ts, _ := st.Templates(t.Context()); len(ts) != 0 {
			t.Errorf("templates = %+v", ts)
		}
	})

	t.Run("80 rune name accepted", func(t *testing.T) {
		s, st, ids := setup(t)
		v := composeVals(t, st, ids, "action", "save_template", "template_name", strings.Repeat("é", 80), "subject", "S", "body", "B")
		if rec := send(s, http.MethodPost, "/compose", v, "fr"); rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	})
}

func TestComposeLogsNothingPersonal(t *testing.T) {
	logs := captureLogs(t)
	s, st := newTestServer(t)
	seed(t, st, []store.ImportRow{{ParentName: "Secret Parent", ChildName: "Secret Kid", Email: "secret.parent@example.com"}})
	tid, err := st.CreateTemplate(t.Context(), "Secret Template", "Secret Subject", "Secret Body")
	if err != nil {
		t.Fatal(err)
	}
	ids := idsOf(childIDs(t, st), "Secret Kid")
	for _, action := range []string{"", "load", "preview", "save_template"} {
		v := composeVals(t, st, ids, "action", action, "template", fmt.Sprint(tid),
			"template_name", "Secret Name "+action, "subject", "Secret Subject {{nope}}", "body", "Secret Body Text")
		send(s, http.MethodPost, "/compose", v, "fr")
		v.Set("subject", "Secret Subject")
		v.Set("body", "Secret Body {{parent_name}} {{child_name}}")
		send(s, http.MethodPost, "/compose", v, "fr")
	}
	stale := composeVals(t, st, ids, "action", "preview")
	stale.Set("version", "0")
	send(s, http.MethodPost, "/compose", stale, "fr")

	out := logs.String()
	for _, secret := range []string{"Secret", "secret.parent", "example.com"} {
		if strings.Contains(out, secret) {
			t.Errorf("log leaks %q: %s", secret, out)
		}
	}
}

func TestChildrenFormCarriesDataVersion(t *testing.T) {
	s, st := newTestServer(t)
	if body := send(s, http.MethodGet, "/children", nil, "fr").Body.String(); strings.Contains(body, `name="version"`) && !strings.Contains(body, `name="version" value="0"`) {
		t.Errorf("empty db: want version 0: %s", body)
	}
	seed(t, st, threeChildren)
	seed(t, st, threeChildren)
	want := `name="version" value="2"`

	if body := send(s, http.MethodGet, "/children", nil, "fr").Body.String(); !strings.Contains(body, want) {
		t.Errorf("GET /children: no %q", want)
	}
	// 422 and 409 re-renders carry the current version too.
	for name, v := range map[string]url.Values{
		"422 nothing selected": {},
		"409 stale":            {"child": {"1"}, "version": {"1"}},
	} {
		rec := send(s, http.MethodPost, "/compose", v, "fr")
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s (status %d): no %q", name, rec.Code, want)
		}
	}
}

var (
	composeFormRe = regexp.MustCompile(`(?s)<form method="post" action="/compose".*?</form>`)
	buttonTagRe   = regexp.MustCompile(`<button[^>]*>`)
	hiddenAttrRe  = regexp.MustCompile(`\shidden(\s|=|>)`) // not aria-hidden
	textareaRe    = regexp.MustCompile(`(?s)<textarea[^>]*>(.*?)</textarea>`)
)

// textareaValue returns what a browser puts in the textarea: the content
// after the opening tag, minus the one leading newline the HTML parser drops.
func textareaValue(t *testing.T, page string) string {
	t.Helper()
	m := textareaRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no textarea in page: %s", page)
	}
	v, ok := strings.CutPrefix(m[1], "\n")
	if !ok {
		t.Fatalf("textarea content %q does not start with the newline a browser swallows", m[1])
	}
	return v
}

// This is a markup test, not a browser test: it checks the first button of
// the form (the one the browser uses when Enter is pressed in a text field),
// it does not press Enter in a real browser.
func TestComposeDefaultButtonIsPreview(t *testing.T) {
	s, st := newTestServer(t)
	seed(t, st, threeChildren)
	mustTemplate(t, st, "Rappel", "Objet modèle", "Corps modèle")
	ids := idsOf(childIDs(t, st), "Léa")

	for _, lang := range []string{"fr", "en"} {
		rec := send(s, http.MethodPost, "/compose", composeVals(t, st, ids, "action", "preview"), lang)
		page := rec.Body.String()
		form := composeFormRe.FindString(page)
		if form == "" {
			t.Fatalf("%s: no compose form", lang)
		}
		if !strings.Contains(form, `value="load"`) {
			t.Fatalf("%s: load button missing, test would prove nothing", lang)
		}
		first := buttonTagRe.FindString(form)
		for _, want := range []string{`type="submit"`, `name="action"`, `value="preview"`, `visually-hidden`} {
			if !strings.Contains(first, want) {
				t.Errorf("%s: first button %q lacks %q", lang, first, want)
			}
		}
		for _, bad := range []string{"disabled", "display:none", "display: none"} {
			if strings.Contains(first, bad) {
				t.Errorf("%s: first button %q contains %q", lang, first, bad)
			}
		}
		if hiddenAttrRe.MatchString(first) {
			t.Errorf("%s: first button %q has the hidden attribute", lang, first)
		}
	}
}

func TestComposePreviewWithTemplateSelectedKeepsText(t *testing.T) {
	s, st := newTestServer(t)
	seed(t, st, threeChildren)
	tid := mustTemplate(t, st, "Rappel", "Objet modèle", "Corps modèle")
	ids := idsOf(childIDs(t, st), "Léa")
	v := composeVals(t, st, ids, "action", "preview", "template", fmt.Sprint(tid), "subject", "Mon objet", "body", "Mon corps")
	body := send(s, http.MethodPost, "/compose", v, "fr").Body.String()
	if !strings.Contains(body, `value="Mon objet"`) || textareaValue(t, body) != "Mon corps" {
		t.Errorf("typed text replaced: %s", body)
	}
	if strings.Contains(body, "Objet modèle") || strings.Contains(body, "Corps modèle") {
		t.Error("template text leaked into the form")
	}
}

func TestLeadingNewlinesRoundTrip(t *testing.T) {
	const text = "\n\nBonjour {{parent_name}}"
	s, st := newTestServer(t)
	seed(t, st, threeChildren)
	ids := idsOf(childIDs(t, st), "Léa")

	// Preview keeps the typed body.
	rec := send(s, http.MethodPost, "/compose", composeVals(t, st, ids, "action", "preview", "subject", "S", "body", text), "fr")
	if got := textareaValue(t, rec.Body.String()); got != text {
		t.Errorf("after preview: %q", got)
	}

	// Save as a template from the compose page.
	rec = send(s, http.MethodPost, "/compose", composeVals(t, st, ids, "action", "save_template", "template_name", "Lead", "subject", "S", "body", text), "fr")
	if rec.Code != http.StatusOK {
		t.Fatalf("save: status %d: %s", rec.Code, rec.Body)
	}
	if got := textareaValue(t, rec.Body.String()); got != text {
		t.Errorf("after save: %q", got)
	}
	ts, err := st.Templates(t.Context())
	if err != nil || len(ts) != 1 {
		t.Fatalf("templates = %+v, %v", ts, err)
	}
	if ts[0].Body != text {
		t.Fatalf("stored body = %q", ts[0].Body)
	}

	// Load into compose.
	rec = send(s, http.MethodPost, "/compose", composeVals(t, st, ids, "action", "load", "template", fmt.Sprint(ts[0].ID)), "fr")
	if got := textareaValue(t, rec.Body.String()); got != text {
		t.Errorf("after load: %q", got)
	}

	// Template edit form shows it, and saving it unchanged keeps it.
	path := fmt.Sprintf("/templates/%d", ts[0].ID)
	rec = send(s, http.MethodGet, path, nil, "fr")
	if got := textareaValue(t, rec.Body.String()); got != text {
		t.Errorf("edit form: %q", got)
	}
	rec = send(s, http.MethodPost, path, tplForm("Lead", "S", text), "fr")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update: status %d: %s", rec.Code, rec.Body)
	}
	if got, _ := st.Template(t.Context(), ts[0].ID); got.Body != text {
		t.Errorf("body after update = %q", got.Body)
	}
	// An invalid update re-renders the form with the typed text intact.
	rec = send(s, http.MethodPost, path, tplForm("Lead", "S", "\n\n{{oops}}"), "fr")
	if got := textareaValue(t, rec.Body.String()); got != "\n\n{{oops}}" {
		t.Errorf("error re-render: %q", got)
	}
	// New template form re-render too.
	rec = send(s, http.MethodPost, "/templates", tplForm("N", "S", "\n\n{{oops}}"), "en")
	if got := textareaValue(t, rec.Body.String()); got != "\n\n{{oops}}" {
		t.Errorf("new form re-render: %q", got)
	}
}

func TestTemplateDeleteSubjectLabel(t *testing.T) {
	s, st := newTestServer(t)
	id := mustTemplate(t, st, "Menu", "Rappel été", "Corps")
	path := fmt.Sprintf("/templates/%d/delete", id)
	fr := send(s, http.MethodGet, path, nil, "fr").Body.String()
	if !strings.Contains(fr, "Objet : Rappel été") {
		t.Errorf("french label: %s", fr)
	}
	en := send(s, http.MethodGet, path, nil, "en").Body.String()
	if !strings.Contains(en, "Subject: Rappel été") || strings.Contains(en, "Subject : ") {
		t.Errorf("english label: %s", en)
	}
}
