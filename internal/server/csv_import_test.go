package server

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const csvHeader = "NUMFAM;NUMFACFACT;NOMFACT;EMAIL PAYEUR\n"

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

func TestCSVImportFlow(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	logs := captureLogs(t)

	csv := csvHeader +
		"10;F10;DUPONT Élodie;Marie@Example.fr\n" +
		"10;F10;DUPONT Élodie;marie@example.fr\n" +
		"20;F20;MARTIN <i>Paul</i>;secret.typo@gmailcom\n" +
		"30;F30;DURAND;\n"
	rec := upload(t, s, "export.csv", []byte(csv))
	if rec.Code != http.StatusOK {
		t.Fatalf("preview status = %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Fichier « export.csv ».",
		"Familles à enregistrer (par adresse)",
		"Sans email valide",
		"Familles (par adresse)",
		"Adresses email",
		"Avertissements (2)",
		"Email du payeur invalide : secret.typo@gmailcom",
		"Email du payeur manquant (famille 30)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("preview does not contain %q", want)
		}
	}
	if strings.Contains(body, "feuille") {
		t.Error("CSV preview mentions a sheet")
	}
	if strings.Contains(body, "Lignes valides") {
		t.Error("child wording in family preview")
	}
	token := tokenFrom(t, body)
	if tot, _ := st.Totals(ctx); tot.Children != 0 {
		t.Fatalf("saved before confirm: %+v", tot)
	}

	rec = confirm(s, token)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm = %d: %s", rec.Code, rec.Body)
	}
	tot, _ := st.Totals(ctx)
	if tot.Children != 3 || tot.Parents != 1 || tot.Families != 3 {
		t.Fatalf("totals = %+v", tot)
	}

	// /children shows the family entries and the unselectable ones.
	list := do(s, httptest.NewRequest(http.MethodGet, "/children", nil)).Body.String()
	for _, want := range []string{"DUPONT Élodie", "2 ligne(s) sans adresse email valide", "marie@example.fr"} {
		if !strings.Contains(list, want) {
			t.Errorf("/children does not contain %q", want)
		}
	}
	// The user's HTML never reaches the page unescaped.
	if strings.Contains(list, "<i>Paul</i>") || !strings.Contains(list, "MARTIN &lt;i&gt;Paul&lt;/i&gt;") {
		t.Errorf("NOMFACT not escaped: %s", list)
	}
	if strings.Contains(body, "<i>Paul</i>") {
		t.Error("NOMFACT not escaped in preview")
	}

	// Invalid values never reach the logs.
	out := logs.String()
	if !strings.Contains(out, "import saved") {
		t.Errorf("expected an import log: %q", out)
	}
	for _, secret := range []string{"secret.typo", "gmailcom", "marie@example.fr", "DUPONT", "MARTIN"} {
		if strings.Contains(out, secret) {
			t.Errorf("logs contain %q: %s", secret, out)
		}
	}

	// Re-importing the same file: nothing changes.
	rec = upload(t, s, "export.csv", []byte(csv))
	if !strings.Contains(rec.Body.String(), "identique aux données actuelles") {
		t.Errorf("re-import should be unchanged: %s", rec.Body)
	}
}

func TestCSVUploadExtensions(t *testing.T) {
	s, _ := newTestServer(t)
	good := []byte(csvHeader + "10;F;DUPONT;a@example.fr\n")

	for _, name := range []string{"x.csv", "X.CSV", "Export.Csv"} {
		t.Run("accepted "+name, func(t *testing.T) {
			rec := upload(t, s, name, good)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="token"`) {
				t.Fatalf("status = %d", rec.Code)
			}
		})
	}
	for _, name := range []string{"x.xls", "x.txt", "x", "x.csv.exe", "x.xlsm"} {
		t.Run("refused "+name, func(t *testing.T) {
			rec := upload(t, s, name, good)
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Seuls les fichiers .csv et Excel .xlsx") {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body)
			}
		})
	}

	t.Run("english format error", func(t *testing.T) {
		rec := uploadLang(t, s, "x.txt", good, "en")
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Only .csv and Excel .xlsx files are accepted.") {
			t.Fatalf("status = %d", rec.Code)
		}
	})
}

func uploadLang(t *testing.T, s *Server, name string, content []byte, lang string) *httptest.ResponseRecorder {
	t.Helper()
	req := uploadRequest(t, name, content)
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Accept-Language", lang)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestCSVUploadErrors(t *testing.T) {
	s, st := newTestServer(t)
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"binary", "NUMFAM;NOMFACT\x00;EMAIL PAYEUR\n", "Ce fichier ne peut pas être lu"},
		{"empty", "", "Le fichier ne contient aucune donnée."},
		{"header only", csvHeader, "Le fichier ne contient aucune donnée."},
		{"missing column", "NUMFAM;NOMFACT\n1;A\n", "Colonnes introuvables : « EMAIL PAYEUR »"},
		{"blocking lines", csvHeader + ";F;DUPONT;a@example.fr\n10;F;;a@example.fr\n",
			"Numéro de famille (NUMFAM) manquant"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := upload(t, s, "x.csv", []byte(tt.content))
			body := rec.Body.String()
			if !strings.Contains(body, tt.want) {
				t.Errorf("body does not contain %q:\n%s", tt.want, body)
			}
			if strings.Contains(body, `name="token"`) {
				t.Error("importable despite errors")
			}
		})
	}
	if tot, _ := st.Totals(context.Background()); tot.Children != 0 {
		t.Errorf("data saved: %+v", tot)
	}

	t.Run("billing name error", func(t *testing.T) {
		rec := upload(t, s, "x.csv", []byte(csvHeader+"10;F;;a@example.fr\n"))
		if !strings.Contains(rec.Body.String(), "Nom de facturation (NOMFACT) manquant") {
			t.Errorf("body: %s", rec.Body)
		}
	})
}

func TestCSVWarningsOnlyCanBeConfirmed(t *testing.T) {
	s, st := newTestServer(t)
	rec := upload(t, s, "x.csv", []byte(csvHeader+"10;F;A;\n20;F;B;oups\n"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	token := tokenFrom(t, rec.Body.String())
	if rec := confirm(s, token); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm = %d", rec.Code)
	}
	tot, _ := st.Totals(context.Background())
	if tot.Children != 2 || tot.Parents != 0 {
		t.Errorf("totals = %+v", tot)
	}
}

func TestCSVPreviewFamilyDeletions(t *testing.T) {
	s, _ := newTestServer(t)
	first := csvHeader + "10;F;DUPONT;a@example.fr\n20;F;NOMAIL;\n30;F;MARTIN;m@example.fr\n"
	confirm(s, tokenFrom(t, upload(t, s, "a.csv", []byte(first)).Body.String()))

	rec := upload(t, s, "b.csv", []byte(csvHeader+"10;F;DUPONT;a@example.fr\n"))
	body := rec.Body.String()
	for _, want := range []string{
		"Familles qui seront supprimées (2)",
		"N° famille", "Nom de facturation",
		"Pas d&#39;email valide",
		"m@example.fr",
		"supprimer 2 ligne(s) famille",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("preview does not contain %q", want)
		}
	}
}

func TestCSVEnglishPreview(t *testing.T) {
	s, _ := newTestServer(t)
	rec := uploadLang(t, s, "x.csv", []byte(csvHeader+"10;F;A;oups\n"), "en")
	body := rec.Body.String()
	for _, want := range []string{"File “x.csv”.", "Families to save (per address)", "Without a valid email", "Invalid payer email: oups."} {
		if !strings.Contains(body, want) {
			t.Errorf("preview does not contain %q", want)
		}
	}
}

func TestExcelInvalidEmailStillBlocks(t *testing.T) {
	s, _ := newTestServer(t)
	rec := upload(t, s, "x.xlsx", xlsx(t, [][]any{
		{"Nom parent", "Nom enfant", "Adresse email"},
		{"Jean", "Léa", ""},
		{"Jean", "Tom", "jean@gmailcom"},
	}))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(body, `name="token"`) {
		t.Fatalf("status = %d, token present = %v", rec.Code, strings.Contains(body, `name="token"`))
	}
	for _, want := range []string{"Adresse email manquante", "Adresse email invalide : jean@gmailcom", "Lignes valides"} {
		if !strings.Contains(body, want) {
			t.Errorf("body does not contain %q", want)
		}
	}
}

func TestXLSXWithFamilyHeaderImportsAsFamilies(t *testing.T) {
	s, _ := newTestServer(t)
	rec := upload(t, s, "x.xlsx", xlsx(t, [][]any{
		{"NUMFAM", "NOMFACT", "EMAIL PAYEUR"},
		{"10", "DUPONT", "a@example.fr"},
		{"20", "NOMAIL", ""},
	}))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Familles à enregistrer (par adresse)") || !strings.Contains(body, `name="token"`) {
		t.Fatalf("status = %d: %s", rec.Code, body)
	}
}

func TestImportFormAcceptsCSV(t *testing.T) {
	s, _ := newTestServer(t)
	body := do(s, httptest.NewRequest(http.MethodGet, "/import", nil)).Body.String()
	if !strings.Contains(body, ".csv") || !strings.Contains(body, "text/csv") {
		t.Errorf("file input does not accept csv: %s", body)
	}
}

func TestCSVOverExcelReplacesData(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	confirm(s, tokenFrom(t, upload(t, s, "a.xlsx", xlsx(t, [][]any{
		{"Nom parent", "Nom enfant", "Adresse email"},
		{"Marie", "Léa", "marie@example.fr"},
	})).Body.String()))

	rec := upload(t, s, "b.csv", []byte(csvHeader+"10;F;DUPONT;marie@example.fr\n"))
	confirm(s, tokenFrom(t, rec.Body.String()))
	tot, _ := st.Totals(ctx)
	if tot.Children != 1 || tot.Parents != 1 || tot.Families != 1 {
		t.Fatalf("totals = %+v", tot)
	}
	cs, _ := st.Children(ctx)
	if len(cs) != 1 || cs[0].Name != "DUPONT" || cs[0].FamilyNumber != "10" {
		t.Errorf("children = %+v", cs)
	}
}

func TestCSVTooManyColumns(t *testing.T) {
	s, st := newTestServer(t)
	content := []byte("NUMFAM;NOMFACT;EMAIL PAYEUR" + strings.Repeat(";", 250) + "\n1;A;a@example.fr\n")
	for lang, want := range map[string]string{
		"fr": "Le fichier a trop de colonnes.",
		"en": "The file has too many columns.",
	} {
		t.Run(lang, func(t *testing.T) {
			rec := uploadLang(t, s, "x.csv", content, lang)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), want) {
				t.Errorf("body does not contain %q", want)
			}
			if strings.Contains(rec.Body.String(), `name="token"`) {
				t.Error("importable")
			}
		})
	}
	if tot, _ := st.Totals(context.Background()); tot.Children != 0 {
		t.Errorf("data saved: %+v", tot)
	}
}

// issueRows counts the rows of the issue table that follows the section
// title (an issue row starts with a line-number cell; the change summary
// cells carry other classes or sit before the title).
func issueRows(body, title string) int {
	i := strings.Index(body, title)
	if i < 0 {
		return -1
	}
	rest := body[i:]
	if j := strings.Index(rest, "</section>"); j >= 0 {
		rest = rest[:j]
	}
	return strings.Count(rest, `<tr><td class="num">`)
}

func TestCSVPreviewIssueTruncation(t *testing.T) {
	wtitle := map[string]string{"fr": "Avertissements", "en": "Warnings"}
	etitle := map[string]string{"fr": "Erreurs", "en": "Errors"}
	s, _ := newTestServer(t)

	t.Run("warnings", func(t *testing.T) {
		var b strings.Builder
		b.WriteString(csvHeader)
		for i := 0; i < 130; i++ {
			fmt.Fprintf(&b, "%d;F;Name%d;bad%d\n", i, i, i)
		}
		for lang, want := range map[string][]string{
			"fr": {"Avertissements (130)", "… et 30 autres"},
			"en": {"Warnings (130)", "… and 30 more"},
		} {
			rec := uploadLang(t, s, "x.csv", []byte(b.String()), lang)
			body := rec.Body.String()
			for _, w := range want {
				if !strings.Contains(body, w) {
					t.Errorf("%s: body does not contain %q", lang, w)
				}
			}
			if n := issueRows(body, "<h2>"+wtitle[lang]); n != 100 {
				t.Errorf("%s: %d warning rows, want 100", lang, n)
			}
			if !strings.Contains(body, `name="token"`) {
				t.Errorf("%s: warnings only must stay importable", lang)
			}
		}
	})

	t.Run("exactly 100 warnings has no more line", func(t *testing.T) {
		var b strings.Builder
		b.WriteString(csvHeader)
		for i := 0; i < 100; i++ {
			fmt.Fprintf(&b, "%d;F;Name%d;bad%d\n", i, i, i)
		}
		body := upload(t, s, "x.csv", []byte(b.String())).Body.String()
		if strings.Contains(body, "autres") || issueRows(body, "<h2>Avertissements") != 100 {
			t.Errorf("rows=%d, more line present=%v", issueRows(body, "<h2>Avertissements"), strings.Contains(body, "autres"))
		}
	})

	t.Run("errors", func(t *testing.T) {
		var b strings.Builder
		b.WriteString(csvHeader)
		for i := 0; i < 107; i++ {
			fmt.Fprintf(&b, ";F;Name%d;a@example.fr\n", i) // missing NUMFAM
		}
		for lang, want := range map[string][]string{
			"fr": {"Erreurs", "(107)", "… et 7 autres"},
			"en": {"Errors", "(107)", "… and 7 more"},
		} {
			rec := uploadLang(t, s, "x.csv", []byte(b.String()), lang)
			body := rec.Body.String()
			for _, w := range want {
				if !strings.Contains(body, w) {
					t.Errorf("%s: body does not contain %q", lang, w)
				}
			}
			if n := issueRows(body, "<h2>"+etitle[lang]); n != 100 {
				t.Errorf("%s: %d error rows, want 100", lang, n)
			}
			if strings.Contains(body, `name="token"`) {
				t.Errorf("%s: importable despite errors", lang)
			}
		}
	})
}
