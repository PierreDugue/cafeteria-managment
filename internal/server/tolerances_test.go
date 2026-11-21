package server

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"cafeteria-management/internal/store"
)

// tolRows: family 10 has two payer emails, 30 has no email, 40 an invalid one.
var tolRows = []store.ImportRow{
	{ParentName: "DUPONT", ChildName: "DUPONT", Email: "a@example.fr", FamilyNumber: "10"},
	{ParentName: "DUPONT", ChildName: "DUPONT", Email: "b@example.fr", FamilyNumber: "10"},
	{ParentName: "MARTIN", ChildName: "MARTIN", Email: "m@example.fr", FamilyNumber: "20"},
	{ParentName: "NOMAIL", ChildName: "NOMAIL", FamilyNumber: "30"},
	{ParentName: "BAD", ChildName: "BAD", InvalidEmail: "oups", FamilyNumber: "40"},
}

var (
	tolBoxRE = regexp.MustCompile(`<input type="checkbox" class="tolerance" form="tolerance-form" name="family" value="([^"]*)"( checked)?`)
	tolRowRE = regexp.MustCompile(`<tr[^>]*data-family="([^"]*)"[^>]*>`)
)

func tolEnv(t *testing.T) (*Server, *store.Store, int64) {
	t.Helper()
	s, st := newTestServer(t)
	return s, st, seedList(t, st, "Cantine", tolRows)
}

func tolFlags(t *testing.T, st *store.Store, id int64) []string {
	t.Helper()
	m, err := st.Tolerances(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func tolSet(t *testing.T, st *store.Store, id int64, fams ...string) {
	t.Helper()
	if err := st.SetTolerances(context.Background(), id, versionOf(t, st, id), fams); err != nil {
		t.Fatal(err)
	}
}

func tolWant(t *testing.T, st *store.Store, id int64, want ...string) {
	t.Helper()
	if got := tolFlags(t, st, id); !slices.Equal(got, want) {
		t.Fatalf("flags = %v, want %v", got, want)
	}
}

// tolPost posts the tolerance form; ajax adds the htmx header.
func tolPost(s *Server, listID int64, body string, ajax bool, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/lists/%d/tolerances", listID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if ajax {
		req.Header.Set("HX-Request", "true")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	return do(s, req)
}

func tolBody(version int64, fams ...string) string {
	v := url.Values{"version": {fmt.Sprint(version)}}
	for _, f := range fams {
		v.Add("family", f)
	}
	return v.Encode()
}

func TestChildrenPageTolerance(t *testing.T) {
	s, st, id := tolEnv(t)
	tolSet(t, st, id, "20", "30")

	rec := get(s, fmt.Sprintf("/children?list=%d", id), "fr")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()

	// One checkbox per family, on its first row only.
	boxes := map[string]bool{}
	n := 0
	for _, m := range tolBoxRE.FindAllStringSubmatch(body, -1) {
		boxes[m[1]] = m[2] != ""
		n++
	}
	if n != 4 || len(boxes) != 4 {
		t.Fatalf("tolerance boxes = %d (%v), want one per family (4)", n, boxes)
	}
	for f, want := range map[string]bool{"10": false, "20": true, "30": true, "40": false} {
		if got, ok := boxes[f]; !ok || got != want {
			t.Errorf("family %s: present=%v checked=%v, want checked=%v", f, ok, got, want)
		}
	}

	// Every row of a flagged family is red, no-email rows included.
	rows := map[string]int{}
	red := map[string]int{}
	for _, m := range tolRowRE.FindAllStringSubmatch(body, -1) {
		rows[m[1]]++
		if strings.Contains(m[0], "row-tolerance") {
			red[m[1]]++
		}
	}
	if rows["10"] != 2 || rows["20"] != 1 || rows["30"] != 1 || rows["40"] != 1 {
		t.Fatalf("rows = %v", rows)
	}
	if red["20"] != 1 || red["30"] != 1 || red["10"] != 0 || red["40"] != 0 {
		t.Errorf("red rows = %v", red)
	}

	for _, want := range []string{`id="tolerance-form"`, `action="/lists/` + fmt.Sprint(id) + `/tolerances"`, "Tolérance", "Enregistrer les tolérances", `colspan="5"`, `name="version"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
	if strings.Contains(body, "Tolérances enregistrées.") {
		t.Error("saved notice without ?tolerance=1")
	}

	// The tolerance boxes are not selection boxes.
	if got := len(anyBox.FindAllString(body, -1)); got != 3 {
		t.Errorf("selection boxes = %d, want 3 (valid emails only)", got)
	}
}

func TestChildrenPageToleranceNoticeAndEnglish(t *testing.T) {
	s, _, id := tolEnv(t)
	rec := get(s, fmt.Sprintf("/children?list=%d&tolerance=1", id), "fr")
	if !strings.Contains(text(rec), "Tolérances enregistrées.") {
		t.Error("no French notice")
	}
	rec = get(s, fmt.Sprintf("/children?list=%d&tolerance=1", id), "en")
	body := text(rec)
	for _, want := range []string{"Tolerances saved.", "Save tolerances", "Tolerance for family 10"} {
		if !strings.Contains(body, want) {
			t.Errorf("English page does not contain %q", want)
		}
	}
	if strings.Contains(body, "Tolérance") {
		t.Error("French text in the English page")
	}
}

func TestChildrenPageExcelHasNoTolerance(t *testing.T) {
	s, st := newTestServer(t)
	id := seedList(t, st, "Excel", threeChildren)
	rec := get(s, fmt.Sprintf("/children?list=%d&tolerance=1", id), "fr")
	body := rec.Body.String()
	for _, bad := range []string{"tolerance-form", `class="tolerance"`, "row-tolerance", "tolerance-th", "Tolérance", "Tolérances enregistrées"} {
		if strings.Contains(body, bad) {
			t.Errorf("Excel page contains %q", bad)
		}
	}
	if !strings.Contains(body, `colspan="4"`) {
		t.Error("no-result colspan should be 4")
	}
}

func TestTolerancesSaveNoJS(t *testing.T) {
	s, st, id := tolEnv(t)
	v := versionOf(t, st, id)

	rec := tolPost(s, id, tolBody(v, "10", "30"), false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != fmt.Sprintf("/children?list=%d&tolerance=1", id) {
		t.Errorf("Location = %q", loc)
	}
	tolWant(t, st, id, "10", "30")

	// Duplicates and padding are ignored; empty values are skipped.
	rec = tolPost(s, id, tolBody(v, " 20 ", "20", "", "  "), false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	tolWant(t, st, id, "20")

	// No family: all flags cleared.
	rec = tolPost(s, id, tolBody(v), false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	tolWant(t, st, id)

	// Following the redirect shows the notice.
	rec = get(s, rec.Header().Get("Location"), "fr")
	if !strings.Contains(text(rec), "Tolérances enregistrées.") {
		t.Error("no notice after redirect")
	}
}

func TestTolerancesSaveAjax(t *testing.T) {
	s, st, id := tolEnv(t)
	rec := tolPost(s, id, tolBody(versionOf(t, st, id), "10", "20"), true)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	tolWant(t, st, id, "10", "20")
	rec = tolPost(s, id, tolBody(versionOf(t, st, id)), true)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("clear: status = %d", rec.Code)
	}
	tolWant(t, st, id)
}

func TestTolerancesErrors(t *testing.T) {
	big := strings.Repeat("x", 2<<20)
	tests := []struct {
		name   string
		body   func(v int64) string
		status int
		msg    string
	}{
		{"stale version", func(v int64) string { return tolBody(v+5, "20") }, http.StatusConflict, "Les données ont changé"},
		{"unknown family", func(v int64) string { return tolBody(v, "20", "77") }, http.StatusUnprocessableEntity, "Une famille cochée"},
		{"missing version", func(v int64) string { return "family=20" }, http.StatusUnprocessableEntity, "Une famille cochée"},
		{"bad version", func(v int64) string { return "version=abc&family=20" }, http.StatusUnprocessableEntity, "Une famille cochée"},
		{"negative version", func(v int64) string { return "version=-1&family=20" }, http.StatusUnprocessableEntity, "Une famille cochée"},
		{"oversized body", func(v int64) string { return tolBody(v, big) }, http.StatusUnprocessableEntity, "trop volumineuse"},
	}
	for _, ajax := range []bool{false, true} {
		for _, tt := range tests {
			t.Run(fmt.Sprintf("%s ajax=%v", tt.name, ajax), func(t *testing.T) {
				s, st, id := tolEnv(t)
				tolSet(t, st, id, "10")
				rec := tolPost(s, id, tt.body(versionOf(t, st, id)), ajax)
				if rec.Code != tt.status {
					t.Fatalf("status = %d, want %d: %.200s", rec.Code, tt.status, rec.Body.String())
				}
				if !strings.Contains(text(rec), tt.msg) {
					t.Errorf("body does not contain %q: %.300s", tt.msg, text(rec))
				}
				if ajax && strings.Contains(rec.Body.String(), "<html") {
					t.Error("ajax answer is a full page")
				}
				if !ajax && !strings.Contains(rec.Body.String(), "<html") {
					t.Error("plain form answer is not a page")
				}
				tolWant(t, st, id, "10")
			})
		}
	}
}

func TestTolerancesUnknownList(t *testing.T) {
	s, st, id := tolEnv(t)
	tolSet(t, st, id, "10")
	for _, ajax := range []bool{false, true} {
		for _, bad := range []string{"9999", "0", "abc", "-3"} {
			req := httptest.NewRequest(http.MethodPost, "/lists/"+bad+"/tolerances", strings.NewReader(tolBody(1, "10")))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if ajax {
				req.Header.Set("HX-Request", "true")
			}
			if rec := do(s, req); rec.Code != http.StatusNotFound {
				t.Errorf("list %q ajax=%v: status = %d", bad, ajax, rec.Code)
			}
		}
	}
	tolWant(t, st, id, "10")
}

func TestTolerancesMethodAndOrigin(t *testing.T) {
	s, st, id := tolEnv(t)
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/lists/%d/tolerances", id), nil)
	if rec := do(s, req); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d", rec.Code)
	}
	rec := tolPost(s, id, tolBody(versionOf(t, st, id), "10"), false, "Sec-Fetch-Site", "cross-site")
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site status = %d", rec.Code)
	}
	rec = tolPost(s, id, tolBody(versionOf(t, st, id), "10"), true, "Sec-Fetch-Site", "cross-site")
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site ajax status = %d", rec.Code)
	}
	tolWant(t, st, id)
}

func TestTolerancesSaveKeepsSelectionVersionAndLastList(t *testing.T) {
	ctx := context.Background()
	s, st, id := tolEnv(t)
	other := seedList(t, st, "Autre", tolRows)
	// "Cantine" is the list used last.
	if _, err := st.ListVersion(ctx, other); err != nil {
		t.Fatal(err)
	}
	cs, err := st.Children(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, c := range cs {
		if c.CanEmail() {
			ids = append(ids, c.ID)
		}
	}
	version := versionOf(t, st, id)
	subject := url.Values{"list": {fmt.Sprint(id)}, "version": {fmt.Sprint(version)}, "action": {"preview"}}
	for _, i := range ids[:2] {
		subject.Add("child", fmt.Sprint(i))
	}
	if rec := post(s, "/compose", subject); rec.Code != http.StatusOK {
		t.Fatalf("compose = %d", rec.Code)
	}
	selBefore, err := st.SavedSelection(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defBefore, err := st.DefaultList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pageBefore := get(s, fmt.Sprintf("/children?list=%d", id), "fr").Body.String()

	for _, ajax := range []bool{false, true} {
		if rec := tolPost(s, id, tolBody(versionOf(t, st, id), "10", "20"), ajax); rec.Code >= 400 {
			t.Fatalf("save: %d", rec.Code)
		}
	}

	if v := versionOf(t, st, id); v != version {
		t.Errorf("version %d -> %d", version, v)
	}
	selAfter, _ := st.SavedSelection(ctx, id)
	if !maps.Equal(selBefore, selAfter) || len(selAfter) != 2 {
		t.Errorf("selection %v -> %v", selBefore, selAfter)
	}
	defAfter, _ := st.DefaultList(ctx)
	if defAfter.ID != defBefore.ID || !defAfter.UsedAt.Equal(defBefore.UsedAt) {
		t.Errorf("last list used %+v -> %+v", defBefore, defAfter)
	}
	// Checked selection boxes of the page are the same; only the flags differ.
	pageAfter := get(s, fmt.Sprintf("/children?list=%d", id), "fr").Body.String()
	if a, b := checkedBox.FindAllString(pageBefore, -1), checkedBox.FindAllString(pageAfter, -1); !slices.Equal(a, b) {
		t.Errorf("checked boxes %v -> %v", a, b)
	}
	// The page's version is still accepted by the selection form.
	again := url.Values{"list": {fmt.Sprint(id)}, "version": {fmt.Sprint(version)}, "action": {"preview"}, "child": {fmt.Sprint(ids[0])}}
	if rec := post(s, "/compose", again); rec.Code != http.StatusOK {
		t.Errorf("compose after saving flags = %d", rec.Code)
	}
}

func TestComposeSendHistoryIgnoreTolerance(t *testing.T) {
	ctx := context.Background()
	s, st, id := tolEnv(t)
	tolSet(t, st, id, "10", "20")
	cs, _ := st.Children(ctx, id)
	var ids []int64
	for _, c := range cs {
		if c.CanEmail() {
			ids = append(ids, c.ID)
		}
	}
	vals := func(kv ...string) url.Values {
		v := composeVals(t, st, ids, kv...)
		v.Set("subject", "Menu")
		v.Set("body", "Bonjour")
		return v
	}
	noFlag := func(label, body string) {
		t.Helper()
		low := strings.ToLower(body)
		for _, bad := range []string{"olérance", "olerance"} {
			if strings.Contains(low, bad) {
				t.Errorf("%s shows tolerance (%q)", label, bad)
			}
		}
	}

	rec := post(s, "/compose", vals("action", "preview"))
	if rec.Code != http.StatusOK {
		t.Fatalf("compose = %d", rec.Code)
	}
	noFlag("compose", rec.Body.String())

	rec = post(s, "/compose", vals("action", "send"))
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm = %d", rec.Code)
	}
	noFlag("confirmation", rec.Body.String())
	tok := tokenOf(t, rec.Body.String())

	rec = post(s, "/send", vals("token", tok, "provider", "test"))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("send = %d: %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	noFlag("batch page", get(s, loc, "fr").Body.String())
	noFlag("history", get(s, "/history", "fr").Body.String())
	noFlag("history en", get(s, "/history", "en").Body.String())

	tolWant(t, st, id, "10", "20")
}

func tolCSV(lines ...string) []byte { return []byte(csvHeader + strings.Join(lines, "\n") + "\n") }

func TestImportPreviewTolerance(t *testing.T) {
	file := tolCSV("20;F;MARTIN;m@example.fr") // 10 (two rows), 30, 40 are gone
	t.Run("fr", func(t *testing.T) {
		s, st, id := tolEnv(t)
		tolSet(t, st, id, "10", "20")
		rec := uploadTo(t, s, "b.csv", file, url.Values{"list": {fmt.Sprint(id)}})
		body := text(rec)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if !strings.Contains(body, "1 famille(s) en tolérance ne sont plus dans le fichier : leur tolérance sera retirée.") {
			t.Errorf("no removed-flags line: %s", body)
		}
		if n := strings.Count(body, "badge-tolerance"); n != 2 {
			t.Errorf("marks = %d, want 2 (both rows of family 10)", n)
		}
		// The preview saves nothing.
		tolWant(t, st, id, "10", "20")

		confirm(s, tokenFrom(t, body))
		tolWant(t, st, id, "20")
	})
	t.Run("en", func(t *testing.T) {
		s, st, id := tolEnv(t)
		tolSet(t, st, id, "10", "20")
		req := uploadRequest(t, "b.csv", file, url.Values{"list": {fmt.Sprint(id)}})
		req.Host = "127.0.0.1:8080"
		req.Header.Set("Accept-Language", "en")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		body := text(rec)
		if !strings.Contains(body, "1 family(ies) with tolerance are no longer in the file: their tolerance will be removed.") {
			t.Errorf("no English line: %s", body)
		}
		if !strings.Contains(body, `badge-tolerance">Tolerance<`) {
			t.Error("no English mark")
		}
		if strings.Contains(body, "tolérance") {
			t.Error("French text in English preview")
		}
	})
	t.Run("none removed", func(t *testing.T) {
		s, st, id := tolEnv(t)
		tolSet(t, st, id, "20") // 20 stays in the file
		body := text(uploadTo(t, s, "b.csv", file, url.Values{"list": {fmt.Sprint(id)}}))
		if strings.Contains(body, "en tolérance ne sont plus") || strings.Contains(body, "badge-tolerance") {
			t.Error("removed-flags info shown without removed flags")
		}
		confirm(s, tokenFrom(t, body))
		tolWant(t, st, id, "20")
	})
	t.Run("no flags at all", func(t *testing.T) {
		s, _, id := tolEnv(t)
		body := text(uploadTo(t, s, "b.csv", file, url.Values{"list": {fmt.Sprint(id)}}))
		if strings.Contains(body, "en tolérance ne sont plus") || strings.Contains(body, "badge-tolerance") {
			t.Error("removed-flags info shown without flags")
		}
	})
}

func TestImportPreviewExcelReplacingFamiliesRemovesFlags(t *testing.T) {
	s, st, id := tolEnv(t)
	tolSet(t, st, id, "10", "20", "30")
	rec := uploadTo(t, s, "x.xlsx", excel(t, []any{"Marie", "Léa", "m@example.fr"}), url.Values{"list": {fmt.Sprint(id)}})
	body := text(rec)
	if !strings.Contains(body, "3 famille(s) en tolérance ne sont plus dans le fichier") {
		t.Errorf("no line for 3 flags: %s", body)
	}
	tolWant(t, st, id, "10", "20", "30")
	confirm(s, tokenFrom(t, body))
	tolWant(t, st, id)
}

func TestToleranceLogsHaveCountsOnly(t *testing.T) {
	logs := captureLogs(t)
	s, st := newTestServer(t)
	rows := []store.ImportRow{
		{ParentName: "ZED", ChildName: "ZED", Email: "z@example.fr", FamilyNumber: "7771234"},
		{ParentName: "YAN", ChildName: "YAN", Email: "y@example.fr", FamilyNumber: "7775678"},
	}
	id := seedList(t, st, "Logs", rows)

	if rec := tolPost(s, id, tolBody(versionOf(t, st, id), "7771234", "7775678"), false); rec.Code != http.StatusSeeOther {
		t.Fatalf("save = %d", rec.Code)
	}
	tolPost(s, id, tolBody(versionOf(t, st, id), "9990000"), true) // refused
	tolPost(s, id, tolBody(versionOf(t, st, id)+9, "7771234"), true)

	rec := uploadTo(t, s, "b.csv", tolCSV("7775678;F;YAN;y@example.fr"), url.Values{"list": {fmt.Sprint(id)}})
	confirm(s, tokenFrom(t, rec.Body.String()))
	tolWant(t, st, id, "7775678")

	out := logs.String()
	for _, secret := range []string{"7771234", "7775678", "9990000", "z@example.fr"} {
		if strings.Contains(out, secret) {
			t.Errorf("logs contain %q:\n%s", secret, out)
		}
	}
	for _, want := range []string{"tolerances saved", "flags=2", "tolerances_removed=1"} {
		if !strings.Contains(out, want) {
			t.Errorf("logs do not contain %q:\n%s", want, out)
		}
	}
}
