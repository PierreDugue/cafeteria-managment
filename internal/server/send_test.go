package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"cafeteria-management/internal/mailer"
	"cafeteria-management/internal/store"
)

// fakeQueue counts Wake calls.
type fakeQueue struct {
	mu       sync.Mutex
	wakes    int
	provider string
	interval time.Duration
}

func (f *fakeQueue) Wake()                   { f.mu.Lock(); f.wakes++; f.mu.Unlock() }
func (f *fakeQueue) Provider() string        { return f.provider }
func (f *fakeQueue) Interval() time.Duration { return f.interval }
func (f *fakeQueue) wakeCount() int          { f.mu.Lock(); defer f.mu.Unlock(); return f.wakes }

func newServerQ(t *testing.T, q *fakeQueue) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s, err := New("test", st, q)
	if err != nil {
		t.Fatal(err)
	}
	return s, st
}

var tokenInput = regexp.MustCompile(`name="token" value="([^"]*)"`)

func tokenOf(t *testing.T, body string) string {
	t.Helper()
	m := tokenInput.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no token in page: %s", body)
	}
	return m[1]
}

// sendEnv is a server with the threeChildren list (3 children, 2 emails).
type sendEnv struct {
	s    *Server
	st   *store.Store
	q    *fakeQueue
	list int64
	ids  map[string]int64
}

func newSendEnv(t *testing.T) *sendEnv {
	t.Helper()
	q := &fakeQueue{provider: mailer.ProviderTest, interval: 500 * time.Millisecond}
	s, st := newServerQ(t, q)
	list := seedList(t, st, "Cantine CE1", threeChildren)
	return &sendEnv{s: s, st: st, q: q, list: list, ids: idsIn(t, st, list)}
}

func (e *sendEnv) vals(kv ...string) url.Values {
	v := url.Values{"list": {fmt.Sprint(e.list)}, "version": {fmt.Sprint(e.version())}}
	for _, id := range e.ids {
		v.Add("child", fmt.Sprint(id))
	}
	v.Set("subject", "Menu pour {{parent_name}}")
	v.Set("body", "Bonjour {{parent_name}},\nÀ bientôt")
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

func (e *sendEnv) version() int64 {
	v, err := e.st.ListVersion(context.Background(), e.list)
	if err != nil {
		panic(err)
	}
	return v
}

func (e *sendEnv) confirmPage(t *testing.T, kv ...string) *httptest.ResponseRecorder {
	t.Helper()
	v := e.vals(kv...)
	v.Set("action", "send")
	return post(e.s, "/compose", v)
}

// start posts the confirmation and then /send; returns the redirect.
func (e *sendEnv) start(t *testing.T) (location string, token string) {
	t.Helper()
	rec := e.confirmPage(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm = %d: %s", rec.Code, rec.Body.String())
	}
	token = tokenOf(t, rec.Body.String())
	rec = post(e.s, "/send", e.vals("token", token))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("send = %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Header().Get("Location"), token
}

func (e *sendEnv) counts(t *testing.T) (batches, messages int) {
	t.Helper()
	bs, total, err := e.st.Batches(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bs {
		messages += b.Total
	}
	return total, messages
}

func TestConfirmationPage(t *testing.T) {
	e := newSendEnv(t)
	rec := e.confirmPage(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := text(rec)
	for _, want := range []string{
		"Mode test : aucun email ne sera envoyé",
		"Envoyer 2 email(s) ?",
		"Envoyer les 2 emails",
		"Cantine CE1",
		"environ 1 minute(s)",
		"Exemple pour anne@example.com",
		"Menu pour Anne Martin",
		"Modifier le message",
		`action="/send"`, `formaction="/compose"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
	for _, id := range e.ids {
		if !strings.Contains(body, fmt.Sprintf(`name="child" value="%d"`, id)) {
			t.Errorf("hidden child %d missing", id)
		}
	}
	if !strings.Contains(body, fmt.Sprintf(`name="version" value="%d"`, versionOf(t, e.st, e.list))) {
		t.Error("hidden version missing")
	}
	tok := tokenOf(t, rec.Body.String())
	if !isToken(tok) || len(tok) != 26 {
		t.Errorf("token = %q", tok)
	}
	if tok2 := tokenOf(t, e.confirmPage(t).Body.String()); tok2 == tok {
		t.Error("two renders gave the same token")
	}
	if b, m := e.counts(t); b != 0 || m != 0 {
		t.Errorf("confirmation wrote %d batches, %d messages", b, m)
	}
	if e.q.wakeCount() != 0 {
		t.Error("confirmation woke the queue")
	}
}

func TestConfirmationPageEnglishAndNoTestBanner(t *testing.T) {
	e := newSendEnv(t)
	v := e.vals()
	v.Set("action", "send")
	rec := send(e.s, http.MethodPost, "/compose", v, "en")
	body := text(rec)
	for _, want := range []string{"Test mode: no email will be sent", "Send 2 email(s)?", "Send the 2 emails", "Edit the message", "about 1 minute(s)"} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q", want)
		}
	}

	// A real provider: no banner.
	q := &fakeQueue{provider: "graph", interval: time.Second}
	s, st := newServerQ(t, q)
	list := seedList(t, st, "L", threeChildren)
	ids := idsIn(t, st, list)
	v = url.Values{"list": {fmt.Sprint(list)}, "version": {fmt.Sprint(versionOf(t, st, list))}, "action": {"send"}, "subject": {"S"}, "body": {"B"}}
	for _, id := range ids {
		v.Add("child", fmt.Sprint(id))
	}
	if rec := post(s, "/compose", v); strings.Contains(text(rec), "Mode test") {
		t.Error("test banner shown for a real provider")
	}
}

func TestConfirmationMinutesEstimate(t *testing.T) {
	tests := []struct {
		n        int
		interval time.Duration
		want     int
	}{
		{1, 0, 1}, {3, 500 * time.Millisecond, 1}, {120, 500 * time.Millisecond, 1}, {121, 500 * time.Millisecond, 2}, {300, 500 * time.Millisecond, 3}, {30, 2 * time.Second, 1}, {31, 2 * time.Second, 2},
	}
	for _, tt := range tests {
		if got := sendMinutes(tt.n, tt.interval); got != tt.want {
			t.Errorf("sendMinutes(%d, %v) = %d, want %d", tt.n, tt.interval, got, tt.want)
		}
	}
}

func TestConfirmationFamiliesWording(t *testing.T) {
	q := &fakeQueue{provider: mailer.ProviderTest}
	s, st := newServerQ(t, q)
	list := seedList(t, st, "Familles", silvaRows)
	ids := idsIn(t, st, list)
	v := url.Values{"list": {fmt.Sprint(list)}, "version": {fmt.Sprint(versionOf(t, st, list))}, "action": {"send"},
		"subject": {"Famille {{family_number}}"}, "body": {"Cher {{parent_name}}"}}
	for _, id := range ids {
		v.Add("child", fmt.Sprint(id))
	}
	rec := post(s, "/compose", v)
	if rec.Code != http.StatusOK || !strings.Contains(text(rec), "Envoyer 2 email(s), un par adresse ?") {
		t.Fatalf("status = %d, body: %s", rec.Code, text(rec))
	}
	if !strings.Contains(text(rec), "Famille 10") {
		t.Error("example not rendered for the first address")
	}
}

func TestConfirmationRefusals(t *testing.T) {
	tests := []struct {
		name string
		edit func(e *sendEnv, v url.Values)
		code int
		want string
	}{
		{"unknown placeholder", func(_ *sendEnv, v url.Values) { v.Set("body", "{{nope}}") }, 422, "{{nope}}"},
		{"empty subject", func(_ *sendEnv, v url.Values) { v.Set("subject", "") }, 422, ""},
		{"stale version", func(_ *sendEnv, v url.Values) { v.Set("version", "999999") }, 409, "ont changé"},
		{"no children", func(_ *sendEnv, v url.Values) { v.Del("child") }, 422, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newSendEnv(t)
			v := e.vals()
			v.Set("action", "send")
			tt.edit(e, v)
			rec := post(e.s, "/compose", v)
			if rec.Code != tt.code || !strings.Contains(strings.ToLower(text(rec)), strings.ToLower(tt.want)) {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tt.code, text(rec))
			}
			if strings.Contains(rec.Body.String(), `name="token"`) {
				t.Error("a token was issued for a refused request")
			}
			if b, _ := e.counts(t); b != 0 {
				t.Errorf("batches = %d", b)
			}
		})
	}
}

func TestConfirmationRenderedSubjectTooLong(t *testing.T) {
	q := &fakeQueue{provider: mailer.ProviderTest}
	s, st := newServerQ(t, q)
	rows := []store.ImportRow{
		{ParentName: strings.Repeat("é", 300), ChildName: "A", Email: "long@example.com"},
		{ParentName: "Court", ChildName: "B", Email: "short@example.com"},
	}
	list := seedList(t, st, "L", rows)
	v := url.Values{"list": {fmt.Sprint(list)}, "version": {fmt.Sprint(versionOf(t, st, list))}, "subject": {"{{parent_name}}"}, "body": {"B"}}
	for _, id := range idsIn(t, st, list) {
		v.Add("child", fmt.Sprint(id))
	}
	v.Set("action", "send")
	rec := post(s, "/compose", v)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(text(rec), "long@example.com") || strings.Contains(rec.Body.String(), `name="token"`) {
		t.Fatalf("compose: status = %d, body: %s", rec.Code, text(rec))
	}
	// Also blocked at /send, and nothing is created.
	v.Set("token", "ABCDEFGH")
	rec = post(s, "/send", v)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(text(rec), "long@example.com") {
		t.Fatalf("send: status = %d", rec.Code)
	}
	if bs, _, _ := st.Batches(context.Background(), 10); len(bs) != 0 {
		t.Error("batch created")
	}
	// English wording.
	v.Set("action", "send")
	if rec := send(s, http.MethodPost, "/compose", v, "en"); !strings.Contains(text(rec), "Too long once placeholders are replaced") {
		t.Errorf("english problem missing: %s", text(rec))
	}
}

func TestSendCreatesBatchAndRedirects(t *testing.T) {
	e := newSendEnv(t)
	loc, _ := e.start(t)
	if !regexp.MustCompile(`^/history/\d+\?started=1$`).MatchString(loc) {
		t.Fatalf("location = %q", loc)
	}
	bs, total, err := e.st.Batches(context.Background(), 10)
	if err != nil || total != 1 || len(bs) != 1 {
		t.Fatalf("batches = %+v, %v", bs, err)
	}
	b := bs[0]
	if loc != fmt.Sprintf("/history/%d?started=1", b.ID) || b.Total != 2 || b.Pending != 2 || b.Provider != mailer.ProviderTest ||
		b.ListName != "Cantine CE1" || b.Subject != "Menu pour {{parent_name}}" || b.Families || b.Status != store.BatchSending {
		t.Errorf("batch = %+v", b)
	}
	if e.q.wakeCount() != 1 {
		t.Errorf("wakes = %d", e.q.wakeCount())
	}
	ms, _ := e.st.BatchMessages(context.Background(), b.ID)
	m, _ := e.st.Message(context.Background(), b.ID, ms[0].ID)
	if m.ToEmail != "anne@example.com" || m.Subject != "Menu pour Anne Martin" || m.Body != "Bonjour Anne Martin,\nÀ bientôt" || len(m.Children) != 2 || m.Children[0].ChildID == 0 {
		t.Errorf("message = %+v", m)
	}
}

func TestSendFamilyBatchStoresFamilyNumbers(t *testing.T) {
	q := &fakeQueue{provider: mailer.ProviderTest}
	s, st := newServerQ(t, q)
	list := seedList(t, st, "Familles", silvaRows)
	v := url.Values{"list": {fmt.Sprint(list)}, "version": {fmt.Sprint(versionOf(t, st, list))}, "subject": {"F {{family_number}}"}, "body": {"B"}, "token": {"ABCDEFGH2345"}}
	for _, id := range idsIn(t, st, list) {
		v.Add("child", fmt.Sprint(id))
	}
	if rec := post(s, "/send", v); rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	bs, _, _ := st.Batches(context.Background(), 1)
	ms, _ := st.BatchMessages(context.Background(), bs[0].ID)
	if !bs[0].Families || len(ms) != 2 {
		t.Fatalf("batch = %+v, %d messages", bs[0], len(ms))
	}
	var silva store.Message
	for _, m := range ms {
		if m.ToEmail == "silva@example.com" {
			silva = m
		}
	}
	if len(silva.Children) != 2 || silva.Children[0].FamilyNumber != "1234" || silva.Children[1].FamilyNumber != "1250" {
		t.Errorf("children = %+v", silva.Children)
	}
}

func TestSendSameTokenTwiceOneBatch(t *testing.T) {
	e := newSendEnv(t)
	loc, token := e.start(t)
	rec := post(e.s, "/send", e.vals("token", token))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	if got, want := rec.Header().Get("Location"), strings.TrimSuffix(loc, "?started=1"); got != want {
		t.Errorf("location = %q, want %q", got, want)
	}
	if b, m := e.counts(t); b != 1 || m != 2 {
		t.Errorf("batches = %d, messages = %d", b, m)
	}
}

func TestSendTwoConfirmationsTwoBatches(t *testing.T) {
	e := newSendEnv(t)
	e.start(t)
	e.start(t)
	if b, _ := e.counts(t); b != 2 {
		t.Errorf("batches = %d", b)
	}
}

func TestSendInvalidTokens(t *testing.T) {
	for _, tok := range []string{"", "abc", "AB CD", "A1B", "A8", "ÉCOLE", strings.Repeat("A", 65), "AB-CD"} {
		t.Run(fmt.Sprintf("%q", tok), func(t *testing.T) {
			e := newSendEnv(t)
			rec := post(e.s, "/send", e.vals("token", tok))
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(text(rec), "Cliquez à nouveau sur « Envoyer… »") {
				t.Fatalf("status = %d, body: %s", rec.Code, text(rec))
			}
			if b, _ := e.counts(t); b != 0 {
				t.Errorf("batches = %d", b)
			}
		})
	}
	t.Run("64 chars accepted", func(t *testing.T) {
		e := newSendEnv(t)
		if rec := post(e.s, "/send", e.vals("token", strings.Repeat("A", 64))); rec.Code != http.StatusSeeOther {
			t.Errorf("status = %d", rec.Code)
		}
	})
}

func TestSendStaleAndDeleted(t *testing.T) {
	t.Run("stale version", func(t *testing.T) {
		e := newSendEnv(t)
		v := e.vals("token", "ABCD", "version", "999")
		if rec := post(e.s, "/send", v); rec.Code != http.StatusConflict {
			t.Errorf("status = %d", rec.Code)
		}
		if b, _ := e.counts(t); b != 0 {
			t.Errorf("batches = %d", b)
		}
	})
	t.Run("import after confirmation page", func(t *testing.T) {
		e := newSendEnv(t)
		v := e.vals("token", "ABCD")
		replaceRows(t, e.st, e.list, threeChildren)
		if rec := post(e.s, "/send", v); rec.Code != http.StatusConflict {
			t.Errorf("status = %d", rec.Code)
		}
		if b, _ := e.counts(t); b != 0 {
			t.Errorf("batches = %d", b)
		}
	})
	t.Run("deleted list", func(t *testing.T) {
		e := newSendEnv(t)
		v := e.vals("token", "ABCD")
		if err := e.st.DeleteList(context.Background(), e.list); err != nil {
			t.Fatal(err)
		}
		if rec := post(e.s, "/send", v); rec.Code != http.StatusConflict {
			t.Errorf("status = %d", rec.Code)
		}
	})
}

func replaceRows(t *testing.T, st *store.Store, list int64, rows []store.ImportRow) {
	t.Helper()
	if _, err := st.ImportFamilies(context.Background(), store.ImportRequest{FileName: "b.xlsx", RowsRead: len(rows), Rows: rows, ListID: list}); err != nil {
		t.Fatal(err)
	}
}

func TestSendRefusals(t *testing.T) {
	t.Run("no-email entry", func(t *testing.T) {
		q := &fakeQueue{provider: mailer.ProviderTest}
		s, st := newServerQ(t, q)
		list := seedList(t, st, "F", []store.ImportRow{
			{ParentName: "A", ChildName: "A", Email: "a@example.com", FamilyNumber: "1"},
			{ParentName: "B", ChildName: "B", FamilyNumber: "2", InvalidEmail: "bad@"},
		})
		v := url.Values{"list": {fmt.Sprint(list)}, "version": {fmt.Sprint(versionOf(t, st, list))}, "subject": {"S"}, "body": {"B"}, "token": {"ABCD"}}
		for _, id := range idsIn(t, st, list) {
			v.Add("child", fmt.Sprint(id))
		}
		for _, path := range []string{"/send", "/compose"} {
			if rec := post(s, path, v); rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("%s status = %d", path, rec.Code)
			}
		}
		if bs, _, _ := st.Batches(context.Background(), 10); len(bs) != 0 {
			t.Error("batch created")
		}
	})
	t.Run("unknown placeholder tampered", func(t *testing.T) {
		e := newSendEnv(t)
		if rec := post(e.s, "/send", e.vals("token", "ABCD", "subject", "{{secret}}")); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("status = %d", rec.Code)
		}
		if b, _ := e.counts(t); b != 0 {
			t.Error("batch created")
		}
	})
	t.Run("ids of another list dropped", func(t *testing.T) {
		e := newSendEnv(t)
		other := seedList(t, e.st, "Autre", []store.ImportRow{{ParentName: "Zed", ChildName: "Z", Email: "z@example.com"}})
		v := e.vals("token", "ABCD")
		for _, id := range idsIn(t, e.st, other) {
			v.Add("child", fmt.Sprint(id))
		}
		if rec := post(e.s, "/send", v); rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d", rec.Code)
		}
		bs, _, _ := e.st.Batches(context.Background(), 1)
		ms, _ := e.st.BatchMessages(context.Background(), bs[0].ID)
		for _, m := range ms {
			if m.ToEmail == "z@example.com" {
				t.Error("entry of another list was sent to")
			}
		}
	})
	t.Run("cross-site", func(t *testing.T) {
		e := newSendEnv(t)
		req := httptest.NewRequest(http.MethodPost, "/send", strings.NewReader(e.vals("token", "ABCD").Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		if rec := do(e.s, req); rec.Code != http.StatusForbidden {
			t.Errorf("status = %d", rec.Code)
		}
		if b, _ := e.counts(t); b != 0 {
			t.Error("batch created")
		}
	})
	t.Run("oversized body", func(t *testing.T) {
		e := newSendEnv(t)
		v := e.vals("token", "ABCD", "body", strings.Repeat("x", 2<<20))
		if rec := post(e.s, "/send", v); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("status = %d", rec.Code)
		}
		if b, _ := e.counts(t); b != 0 {
			t.Error("batch created")
		}
	})
	t.Run("GET not allowed", func(t *testing.T) {
		e := newSendEnv(t)
		if rec := get(e.s, "/send", "fr"); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d", rec.Code)
		}
	})
}

// Compose and send refuse the same bad selections the same way.
func TestComposeAndSendShareSelectionChecks(t *testing.T) {
	tests := []struct {
		name string
		edit func(v url.Values)
		code int
	}{
		{"no list", func(v url.Values) { v.Del("list") }, 409},
		{"bad list", func(v url.Values) { v.Set("list", "x") }, 409},
		{"no version", func(v url.Values) { v.Del("version") }, 409},
		{"negative version", func(v url.Values) { v.Set("version", "-1") }, 409},
		{"stale", func(v url.Values) { v.Set("version", "99999") }, 409},
		{"none selected", func(v url.Values) { v.Del("child") }, 422},
		{"garbage ids", func(v url.Values) { v.Del("child"); v.Set("child", "abc") }, 422},
		{"unknown ids", func(v url.Values) { v.Del("child"); v.Set("child", "99999") }, 422},
		{"too many", func(v url.Values) {
			v.Del("child")
			for i := 1; i <= 5001; i++ {
				v.Add("child", fmt.Sprint(i))
			}
		}, 422},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newSendEnv(t)
			v := e.vals("token", "ABCD")
			tt.edit(v)
			c := post(e.s, "/compose", v)
			s := post(e.s, "/send", v)
			if c.Code != tt.code || s.Code != tt.code {
				t.Errorf("compose = %d, send = %d, want %d", c.Code, s.Code, tt.code)
			}
			if b, _ := e.counts(t); b != 0 {
				t.Error("batch created")
			}
		})
	}
}

func TestComposePageHasSendButtonAndNoNextStep(t *testing.T) {
	e := newSendEnv(t)
	v := e.vals()
	rec := post(e.s, "/compose", v)
	body := text(rec)
	if !strings.Contains(body, `value="send"`) || !strings.Contains(body, "Envoyer…") {
		t.Errorf("send button missing: %s", body)
	}
	if strings.Contains(body, "L'envoi sera ajouté") || strings.Contains(body, "compose.next_step") {
		t.Error("old next-step note still shown")
	}
	// The hidden default button is still preview (Enter never opens the confirmation).
	i, j := strings.Index(body, `value="preview"`), strings.Index(body, `value="send"`)
	if i < 0 || j < 0 || i > j {
		t.Errorf("preview button must come before send (%d, %d)", i, j)
	}
}

func TestComposePreviewShowsRenderedSubjectTooLong(t *testing.T) {
	q := &fakeQueue{provider: mailer.ProviderTest}
	s, st := newServerQ(t, q)
	list := seedList(t, st, "L", []store.ImportRow{{ParentName: strings.Repeat("é", 300), ChildName: "A", Email: "long@example.com"}})
	v := url.Values{"list": {fmt.Sprint(list)}, "version": {fmt.Sprint(versionOf(t, st, list))}, "subject": {"{{parent_name}}"}, "body": {"B"}, "action": {"preview"}}
	for _, id := range idsIn(t, st, list) {
		v.Add("child", fmt.Sprint(id))
	}
	rec := post(s, "/compose", v)
	if !strings.Contains(text(rec), "long@example.com") {
		t.Errorf("preview does not show the problem: %s", text(rec))
	}
}
