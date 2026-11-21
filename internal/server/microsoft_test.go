package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"cafeteria-management/internal/mailer"
	"cafeteria-management/internal/microsoft"
	"cafeteria-management/internal/store"
)

// fakeMS is a microsoftAccount: connected or not, with canned answers.
type fakeMS struct {
	mu        sync.Mutex
	acc       microsoft.Account
	connected bool

	beginHosts []string
	beginURL   string
	beginErr   error

	finishCalls [][2]string // state, code
	finishHost  string
	finishErr   error

	signOutCalls int
	signOutErr   error

	sent    []mailer.Message
	sendErr error
}

func (f *fakeMS) Account() (microsoft.Account, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acc, f.connected
}

func (f *fakeMS) BeginSignIn(host string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.beginHosts = append(f.beginHosts, host)
	return f.beginURL, f.beginErr
}

func (f *fakeMS) FinishSignIn(_ context.Context, state, code string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finishCalls = append(f.finishCalls, [2]string{state, code})
	return f.finishHost, f.finishErr
}

func (f *fakeMS) SignOut() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signOutCalls++
	if f.signOutErr == nil {
		f.connected = false
	}
	return f.signOutErr
}

func (f *fakeMS) Send(_ context.Context, m mailer.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return f.sendErr
}

func (f *fakeMS) setConnected(v bool) {
	f.mu.Lock()
	f.connected = v
	f.mu.Unlock()
}

var testAccount = microsoft.Account{Name: "Léa Étienne", Address: "lea@school.example"}

// graphEnv is a send environment with Microsoft 365 configured, connected
// and chosen as the sending mode.
type graphEnv struct {
	*sendEnv
	ms *fakeMS
}

func newMSEnv(t *testing.T, connected bool) *graphEnv {
	t.Helper()
	ms := &fakeMS{acc: testAccount, connected: connected, beginURL: "https://login.example/authorize?x=1", finishHost: "127.0.0.1:8080"}
	q := &fakeQueue{interval: 500 * time.Millisecond, intervals: map[string]time.Duration{mailer.ProviderGraph: 2 * time.Second}}
	s, st := newServerMS(t, q, ms)
	list := seedList(t, st, "Cantine CE1", threeChildren)
	return &graphEnv{sendEnv: &sendEnv{s: s, st: st, q: q, list: list, ids: idsIn(t, st, list)}, ms: ms}
}

// newGraphEnv is connected with the Graph mode saved.
func newGraphEnv(t *testing.T) *graphEnv {
	t.Helper()
	e := newMSEnv(t, true)
	if rec := postSettings(e.s, "fr", url.Values{"language": {"auto"}, "sending": {"graph"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("saving graph mode = %d: %s", rec.Code, rec.Body.String())
	}
	return e
}

func storedProvider(t *testing.T, st *store.Store) string {
	t.Helper()
	got, err := st.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return got.Provider
}

// ---- settings page ----

func TestSettingsPageNotConfigured(t *testing.T) {
	s, _ := newTestServer(t)
	body := text(get(s, "/settings", "fr"))
	if !strings.Contains(body, "pas disponible dans cette version") {
		t.Errorf("unavailable text missing: %s", body)
	}
	for _, absent := range []string{`name="sending"`, "/settings/microsoft/signin", "/settings/microsoft/signout", "/settings/microsoft/test"} {
		if strings.Contains(body, absent) {
			t.Errorf("page contains %q", absent)
		}
	}
}

func TestSettingsPageConfiguredNotConnected(t *testing.T) {
	e := newMSEnv(t, false)
	body := text(get(e.s, "/settings", "fr"))
	for _, want := range []string{`action="/settings/microsoft/signin"`, `name="sending" value="test" checked`, `name="sending" value="graph"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
	for _, absent := range []string{"/settings/microsoft/signout", "/settings/microsoft/test", testAccount.Address, "pas disponible dans cette version"} {
		if strings.Contains(body, absent) {
			t.Errorf("page contains %q", absent)
		}
	}
}

func TestSettingsPageConnected(t *testing.T) {
	e := newMSEnv(t, true)
	body := text(get(e.s, "/settings", "en"))
	for _, want := range []string{"Connected: Léa Étienne (lea@school.example)", `action="/settings/microsoft/test"`,
		`action="/settings/microsoft/signout"`, `name="sending" value="test" checked`, `name="sending" value="graph"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
	if strings.Contains(body, "/settings/microsoft/signin") {
		t.Error("sign-in button shown while connected")
	}
}

func TestSettingsPageGraphModeChecked(t *testing.T) {
	e := newGraphEnv(t)
	body := text(get(e.s, "/settings", "fr"))
	if !strings.Contains(body, `name="sending" value="graph" checked`) || strings.Contains(body, `name="sending" value="test" checked`) {
		t.Errorf("graph radio not the only one checked: %s", body)
	}
	if strings.Contains(body, "test-banner") {
		t.Error("test banner shown in Graph mode")
	}
}

func TestSettingsNoticesFromQuery(t *testing.T) {
	e := newMSEnv(t, true)
	tests := []struct{ query, want string }{
		{"connected=1", "Connected to Microsoft 365"},
		{"signedout=1", "Signed out of Microsoft 365."},
		{"tested=1", "Test email sent to lea@school.example"},
		{"signin=denied", "Sign-in cancelled."},
		{"signin=consent", "Ask your IT department"},
		{"signin=expired", "The sign-in request expired"},
		{"signin=failed", "Signing in to Microsoft failed"},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			rec := get(e.s, "/settings?"+tt.query, "en")
			if rec.Code != http.StatusOK || !strings.Contains(text(rec), tt.want) {
				t.Errorf("status %d, want %q in %s", rec.Code, tt.want, text(rec))
			}
		})
	}
	if body := text(get(e.s, "/settings?signin=<b>x</b>", "en")); strings.Contains(body, "alert-error") {
		t.Error("unknown signin code produced an error notice")
	}
	// Not configured: these codes mean nothing.
	s, _ := newTestServer(t)
	if body := text(get(s, "/settings?connected=1&signin=denied", "en")); strings.Contains(body, "alert-") {
		t.Errorf("notice shown without Microsoft 365: %s", body)
	}
}

// ---- saving the sending mode ----

func TestSettingsSaveSendingModes(t *testing.T) {
	t.Run("graph when connected", func(t *testing.T) {
		e := newMSEnv(t, true)
		rec := postSettings(e.s, "fr", url.Values{"language": {"auto"}, "sending": {"graph"}})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings?saved=1" {
			t.Fatalf("status %d, location %q", rec.Code, rec.Header().Get("Location"))
		}
		if got := storedProvider(t, e.st); got != "graph" {
			t.Errorf("stored = %q", got)
		}
		// The next confirmation is in Graph mode.
		if body := text(e.confirmPage(t)); !strings.Contains(body, `name="provider" value="graph"`) {
			t.Errorf("confirmation not in Graph mode: %s", body)
		}
	})
	t.Run("graph refused when not connected", func(t *testing.T) {
		e := newMSEnv(t, false)
		rec := postSettings(e.s, "fr", url.Values{"language": {"en"}, "sending": {"graph"}})
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(text(rec), "Connectez-vous d'abord") {
			t.Fatalf("status %d: %s", rec.Code, text(rec))
		}
		if got := storedProvider(t, e.st); got != "" {
			t.Errorf("stored provider = %q", got)
		}
		if got := storedLanguage(t, e.st); got != "" {
			t.Errorf("nothing must be saved, language = %q", got)
		}
	})
	t.Run("bogus", func(t *testing.T) {
		e := newMSEnv(t, true)
		rec := postSettings(e.s, "fr", url.Values{"language": {"en"}, "sending": {"bogus"}})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status %d", rec.Code)
		}
		if got := storedProvider(t, e.st); got != "" || storedLanguage(t, e.st) != "" {
			t.Error("something was saved")
		}
	})
	t.Run("back to test", func(t *testing.T) {
		e := newGraphEnv(t)
		rec := postSettings(e.s, "fr", url.Values{"language": {"auto"}, "sending": {"test"}})
		if rec.Code != http.StatusSeeOther || storedProvider(t, e.st) != "" {
			t.Fatalf("status %d, stored %q", rec.Code, storedProvider(t, e.st))
		}
		if !strings.Contains(text(e.confirmPage(t)), `name="provider" value="test"`) {
			t.Error("confirmation not in test mode")
		}
	})
	t.Run("missing field keeps the current mode", func(t *testing.T) {
		e := newGraphEnv(t)
		rec := postSettings(e.s, "fr", url.Values{"language": {"en"}})
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status %d", rec.Code)
		}
		if got := storedProvider(t, e.st); got != "graph" {
			t.Errorf("stored provider = %q, want graph kept", got)
		}
		if got := storedLanguage(t, e.st); got != "en" {
			t.Errorf("language = %q", got)
		}
	})
	t.Run("missing field in test mode stays test", func(t *testing.T) {
		e := newMSEnv(t, true)
		if rec := postSettings(e.s, "fr", url.Values{"language": {"en"}}); rec.Code != http.StatusSeeOther {
			t.Fatalf("status %d", rec.Code)
		}
		if got := storedProvider(t, e.st); got != "" {
			t.Errorf("stored provider = %q", got)
		}
	})
	t.Run("ignored when not configured", func(t *testing.T) {
		s, st := newTestServer(t)
		for _, v := range []string{"graph", "bogus", "test"} {
			rec := postSettings(s, "fr", url.Values{"language": {"en"}, "sending": {v}})
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("%s: status %d", v, rec.Code)
			}
			if got := storedProvider(t, st); got != "" {
				t.Errorf("%s: stored provider = %q", v, got)
			}
		}
	})
}

func TestStoredGraphModeWithoutMicrosoftCountsAsTest(t *testing.T) {
	st, err := store.Open(context.Background(), t.TempDir()+"/app.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.SaveSettings(context.Background(), store.Settings{Provider: "graph"}); err != nil {
		t.Fatal(err)
	}
	s, err := New(context.Background(), "test", st, &fakeQueue{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := text(get(s, "/settings", "fr"))
	if !strings.Contains(body, "test-banner") {
		t.Error("not in test mode")
	}
	// Saving writes test, not graph.
	if rec := postSettings(s, "fr", url.Values{"language": {"fr"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d", rec.Code)
	}
	if got := storedProvider(t, st); got != "" {
		t.Errorf("stored provider = %q, want test", got)
	}
}

func TestRestartKeepsModeAndUnknownStoredModeIsTest(t *testing.T) {
	e := newGraphEnv(t)
	s2, err := New(context.Background(), "test", e.st, e.q, e.ms)
	if err != nil {
		t.Fatal(err)
	}
	if body := text(get(s2, "/settings", "fr")); !strings.Contains(body, `name="sending" value="graph" checked`) {
		t.Error("mode lost after restart")
	}
	if err := e.st.SaveSettings(context.Background(), store.Settings{Provider: "carrier-pigeon"}); err != nil {
		t.Fatal(err)
	}
	s3, err := New(context.Background(), "test", e.st, e.q, e.ms)
	if err != nil {
		t.Fatal(err)
	}
	if body := text(get(s3, "/settings", "fr")); !strings.Contains(body, `name="sending" value="test" checked`) {
		t.Error("unknown stored mode must count as test")
	}
}

// ---- sign-in, callback, sign-out, test email ----

func TestMicrosoftSignIn(t *testing.T) {
	e := newMSEnv(t, false)
	rec := post(e.s, "/settings/microsoft/signin", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != e.ms.beginURL {
		t.Fatalf("status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}
	if len(e.ms.beginHosts) != 1 || e.ms.beginHosts[0] != "127.0.0.1:8080" {
		t.Errorf("return host = %v, want the request Host", e.ms.beginHosts)
	}
	// Another loopback name and port are passed as they are.
	req := httptest.NewRequest(http.MethodPost, "/settings/microsoft/signin", nil)
	req.Host = "localhost:9191"
	rec = httptest.NewRecorder()
	e.s.ServeHTTP(rec, req)
	if e.ms.beginHosts[1] != "localhost:9191" {
		t.Errorf("return host = %v", e.ms.beginHosts)
	}
}

func TestMicrosoftSignInErrors(t *testing.T) {
	t.Run("begin fails", func(t *testing.T) {
		e := newMSEnv(t, false)
		e.ms.beginErr = errors.New("invalid host port")
		if rec := post(e.s, "/settings/microsoft/signin", nil); rec.Code != http.StatusInternalServerError {
			t.Errorf("status %d", rec.Code)
		}
	})
	t.Run("cross-site", func(t *testing.T) {
		e := newMSEnv(t, false)
		req := httptest.NewRequest(http.MethodPost, "/settings/microsoft/signin", nil)
		req.Host = "127.0.0.1:8080"
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		rec := httptest.NewRecorder()
		e.s.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || len(e.ms.beginHosts) != 0 {
			t.Errorf("status %d, begin calls %d", rec.Code, len(e.ms.beginHosts))
		}
	})
	t.Run("get not allowed", func(t *testing.T) {
		e := newMSEnv(t, false)
		if rec := get(e.s, "/settings/microsoft/signin", "fr"); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status %d", rec.Code)
		}
	})
}

func TestMicrosoftRoutesNotConfigured(t *testing.T) {
	s, _ := newTestServer(t)
	for _, tt := range []struct{ method, path string }{
		{"POST", "/settings/microsoft/signin"},
		{"GET", "/auth/microsoft/callback?code=c&state=s"},
		{"POST", "/settings/microsoft/signout"},
		{"POST", "/settings/microsoft/test"},
	} {
		if rec := send(s, tt.method, tt.path, nil, "fr"); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d", tt.method, tt.path, rec.Code)
		}
	}
}

func callback(s *Server, host, query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/auth/microsoft/callback?"+query, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestMicrosoftCallback(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		finishErr  error
		wantLoc    string
		wantFinish bool
	}{
		{"success", "code=THE-CODE&state=THE-STATE", nil, "http://127.0.0.1:8080/settings?connected=1", true},
		{"expired", "code=c&state=s", microsoft.ErrSignInExpired, "/settings?signin=expired", true},
		{"consent from finish", "code=c&state=s", fmt.Errorf("token exchange: %w", microsoft.ErrConsent), "/settings?signin=consent", true},
		{"other failure", "code=c&state=s", errors.New("boom"), "/settings?signin=failed", true},
		{"access denied", "error=access_denied&error_description=user+said+no&state=s", nil, "/settings?signin=denied", false},
		{"consent in description", "error=access_denied&error_description=AADSTS90094+admin", nil, "/settings?signin=consent", false},
		{"unknown error", "error=server_error", nil, "/settings?signin=failed", false},
		{"no code", "state=s", nil, "/settings?signin=failed", false},
		{"no state", "code=c", nil, "/settings?signin=failed", false},
		{"nothing", "", nil, "/settings?signin=failed", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newMSEnv(t, false)
			e.ms.finishErr = tt.finishErr
			rec := callback(e.s, "localhost:8080", tt.query)
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != tt.wantLoc {
				t.Fatalf("status %d, location %q, want %q", rec.Code, rec.Header().Get("Location"), tt.wantLoc)
			}
			if got := len(e.ms.finishCalls) == 1; got != tt.wantFinish {
				t.Errorf("FinishSignIn called = %v", got)
			}
			if tt.name == "success" && e.ms.finishCalls[0] != [2]string{"THE-STATE", "THE-CODE"} {
				t.Errorf("finish args = %v", e.ms.finishCalls[0])
			}
		})
	}
}

func TestMicrosoftCallbackReturnsToHostOfSignIn(t *testing.T) {
	e := newMSEnv(t, false)
	e.ms.finishHost = "localhost:9191"
	rec := callback(e.s, "localhost:8080", "code=c&state=s")
	if rec.Header().Get("Location") != "http://localhost:9191/settings?connected=1" {
		t.Errorf("location = %q", rec.Header().Get("Location"))
	}
}

func TestMicrosoftCallbackNeverLogsCodeOrState(t *testing.T) {
	logs := captureLogs(t)
	e := newMSEnv(t, false)
	callback(e.s, "localhost:8080", "code=SECRET-CODE-1&state=SECRET-STATE-1")
	e.ms.finishErr = errors.New("exchange failed")
	callback(e.s, "localhost:8080", "code=SECRET-CODE-2&state=SECRET-STATE-2")
	callback(e.s, "localhost:8080", "error=access_denied&error_description=SECRET-DESC&state=SECRET-STATE-3")
	callback(e.s, "localhost:8080", "error=%3Cscript%3E&code=SECRET-CODE-4")
	out := logs.String()
	if out == "" {
		t.Fatal("no log captured")
	}
	for _, secret := range []string{"SECRET-CODE", "SECRET-STATE", "SECRET-DESC", "script"} {
		if strings.Contains(out, secret) {
			t.Errorf("logs contain %q:\n%s", secret, out)
		}
	}
}

func TestMicrosoftSignOut(t *testing.T) {
	e := newMSEnv(t, true)
	rec := post(e.s, "/settings/microsoft/signout", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings?signedout=1" {
		t.Fatalf("status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}
	if e.ms.signOutCalls != 1 {
		t.Errorf("SignOut calls = %d", e.ms.signOutCalls)
	}
	e.ms.signOutErr = errors.New("keychain")
	if rec := post(e.s, "/settings/microsoft/signout", nil); rec.Code != http.StatusInternalServerError {
		t.Errorf("failing sign-out: status %d", rec.Code)
	}
}

func TestMicrosoftTestEmail(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		e := newMSEnv(t, true)
		rec := send(e.s, http.MethodPost, "/settings/microsoft/test", nil, "en")
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings?tested=1" {
			t.Fatalf("status %d, location %q", rec.Code, rec.Header().Get("Location"))
		}
		if len(e.ms.sent) != 1 {
			t.Fatalf("sent = %d", len(e.ms.sent))
		}
		m := e.ms.sent[0]
		if m.To.Address != testAccount.Address || m.To.Name != testAccount.Name {
			t.Errorf("to = %+v, want the account's own address", m.To)
		}
		if m.Subject != "Cafeteria Management: test email" || !strings.Contains(m.Body, "Microsoft 365") {
			t.Errorf("message = %+v", m)
		}
		// The notice names the account address.
		if body := text(get(e.s, "/settings?tested=1", "en")); !strings.Contains(body, "Test email sent to lea@school.example") {
			t.Errorf("notice missing: %s", body)
		}
		// Not in the history.
		if _, total, _ := e.st.Batches(context.Background(), 10); total != 0 {
			t.Error("test email recorded in the history")
		}
	})
	t.Run("french", func(t *testing.T) {
		e := newMSEnv(t, true)
		post(e.s, "/settings/microsoft/test", nil)
		if !strings.Contains(e.ms.sent[0].Subject, "email de test") {
			t.Errorf("subject = %q", e.ms.sent[0].Subject)
		}
	})
	t.Run("not connected", func(t *testing.T) {
		e := newMSEnv(t, false)
		rec := send(e.s, http.MethodPost, "/settings/microsoft/test", nil, "en")
		if rec.Code != http.StatusConflict || !strings.Contains(text(rec), "No Microsoft 365 account connected.") {
			t.Fatalf("status %d: %s", rec.Code, text(rec))
		}
		if len(e.ms.sent) != 0 {
			t.Error("an email was sent")
		}
	})
	failures := []struct {
		name string
		err  error
		want string
	}{
		{"plain", errors.New("graph: HTTP 400 BadThing: <b>bad</b>"), "Refused by the provider"},
		{"rate limit", &mailer.RateLimitError{RetryAfter: time.Second, Err: errors.New("graph: 429")}, "Too many emails"},
		{"account", fmt.Errorf("%w: not signed in", mailer.ErrAccount), "account"},
		{"uncertain", fmt.Errorf("%w: no answer", mailer.ErrUncertain), "unknown"},
	}
	for _, tt := range failures {
		t.Run("failure "+tt.name, func(t *testing.T) {
			e := newMSEnv(t, true)
			e.ms.sendErr = tt.err
			rec := send(e.s, http.MethodPost, "/settings/microsoft/test", nil, "en")
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status %d", rec.Code)
			}
			if strings.Contains(rec.Body.String(), "<b>bad</b>") {
				t.Error("provider text not escaped")
			}
			if !strings.Contains(strings.ToLower(text(rec)), strings.ToLower(tt.want)) {
				t.Errorf("want %q in %s", tt.want, text(rec))
			}
		})
	}
}

// ---- compose and send in Graph mode ----

func TestConfirmationGraphMode(t *testing.T) {
	e := newGraphEnv(t)
	rec := send(e.s, http.MethodPost, "/compose", withAction(e.vals(), "send"), "fr")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, text(rec))
	}
	body := text(rec)
	if strings.Contains(body, "Mode test") {
		t.Error("test banner shown in Graph mode")
	}
	for _, want := range []string{"Envoyé depuis", "lea@school.example", `name="provider" value="graph"`,
		"environ 1 minute(s)"} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
	if b, _ := e.counts(t); b != 0 {
		t.Error("confirmation wrote a batch")
	}
}

// BUG (send.go renderSendConfirm): mail.Address.String encodes a non-ASCII
// name as an RFC 2047 word ("=?utf-8?q?L=C3=A9a...?="), unreadable on the page.
func TestConfirmationGraphModeShowsAccentedAccountNameReadably(t *testing.T) {
	e := newGraphEnv(t)
	body := text(e.confirmPage(t))
	if strings.Contains(body, "=?utf-8?") || !strings.Contains(body, "Léa Étienne") {
		i := strings.Index(body, "Envoyé depuis")
		t.Errorf("sender line not readable: %s", body[i:i+80])
	}
}

func TestConfirmationGraphModeASCIIAccountName(t *testing.T) {
	e := newGraphEnv(t)
	e.ms.acc = microsoft.Account{Name: "School Cafeteria", Address: "cafe@school.example"}
	body := text(e.confirmPage(t))
	if !strings.Contains(body, "Envoyé depuis : ") || !strings.Contains(body, "School Cafeteria") || !strings.Contains(body, "<cafe@school.example>") {
		t.Errorf("sender line missing: %s", body)
	}
}

func TestConfirmationGraphModeMinutesUseGraphInterval(t *testing.T) {
	e := newGraphEnv(t)
	e.q.intervals[mailer.ProviderGraph] = 40 * time.Second // 2 emails: 80 s, 2 minutes
	if body := text(e.confirmPage(t)); !strings.Contains(body, "environ 2 minute(s)") {
		t.Errorf("minutes not from the Graph interval: %s", body)
	}
}

func withAction(v url.Values, a string) url.Values { v.Set("action", a); return v }

func TestGraphModeStartCreatesGraphBatch(t *testing.T) {
	e := newGraphEnv(t)
	rec := e.confirmPage(t)
	token := tokenOf(t, rec.Body.String())
	rec = post(e.s, "/send", e.vals("token", token, "provider", "graph"))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rec.Code, text(rec))
	}
	bs, _, err := e.st.Batches(context.Background(), 10)
	if err != nil || len(bs) != 1 || bs[0].Provider != "graph" {
		t.Fatalf("batches = %+v, %v", bs, err)
	}
	if e.q.wakeCount() != 1 {
		t.Errorf("wakes = %d", e.q.wakeCount())
	}
}

func TestSendBlockedWhenNotConnected(t *testing.T) {
	e := newGraphEnv(t)
	// A confirmation page shown while connected, then the account goes away.
	token := tokenOf(t, e.confirmPage(t).Body.String())
	e.ms.setConnected(false)

	t.Run("confirmation step", func(t *testing.T) {
		rec := e.confirmPage(t)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(text(rec), "Connectez-vous") {
			t.Fatalf("status %d: %s", rec.Code, text(rec))
		}
		if strings.Contains(rec.Body.String(), `name="token"`) {
			t.Error("a token was issued")
		}
	})
	t.Run("final step", func(t *testing.T) {
		rec := post(e.s, "/send", e.vals("token", token, "provider", "graph"))
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(text(rec), "Connectez-vous") {
			t.Fatalf("status %d: %s", rec.Code, text(rec))
		}
		if b, _ := e.counts(t); b != 0 {
			t.Errorf("batches = %d", b)
		}
		if e.q.wakeCount() != 0 {
			t.Error("queue woken")
		}
	})
	t.Run("preview and test mode are not blocked", func(t *testing.T) {
		rec := send(e.s, http.MethodPost, "/compose", withAction(e.vals(), "preview"), "fr")
		if rec.Code != http.StatusOK {
			t.Errorf("preview status %d", rec.Code)
		}
	})
}

func TestModeChangedAfterConfirmation(t *testing.T) {
	t.Run("test to graph", func(t *testing.T) {
		e := newMSEnv(t, true)
		token := tokenOf(t, e.confirmPage(t).Body.String()) // shown in test mode
		postSettings(e.s, "fr", url.Values{"language": {"auto"}, "sending": {"graph"}})
		rec := post(e.s, "/send", e.vals("token", token, "provider", "test"))
		if rec.Code != http.StatusConflict {
			t.Fatalf("status %d: %s", rec.Code, text(rec))
		}
		body := text(rec)
		if !strings.Contains(body, "mode d'envoi a changé") && !strings.Contains(body, "mode") {
			t.Errorf("no mode_changed message: %s", body)
		}
		if !strings.Contains(body, `name="provider" value="graph"`) || !strings.Contains(body, "Envoyé depuis") {
			t.Error("confirmation not shown again in the new mode")
		}
		if newTok := tokenOf(t, rec.Body.String()); newTok == token {
			t.Error("the token must be new")
		}
		if b, _ := e.counts(t); b != 0 {
			t.Errorf("batches = %d", b)
		}
	})
	t.Run("graph to test", func(t *testing.T) {
		e := newGraphEnv(t)
		token := tokenOf(t, e.confirmPage(t).Body.String())
		postSettings(e.s, "fr", url.Values{"language": {"auto"}, "sending": {"test"}})
		rec := post(e.s, "/send", e.vals("token", token, "provider", "graph"))
		if rec.Code != http.StatusConflict {
			t.Fatalf("status %d: %s", rec.Code, text(rec))
		}
		body := text(rec)
		if !strings.Contains(body, `name="provider" value="test"`) || !strings.Contains(body, "Mode test") {
			t.Error("confirmation not shown again in test mode")
		}
		if b, _ := e.counts(t); b != 0 {
			t.Errorf("batches = %d", b)
		}
	})
	t.Run("missing provider field", func(t *testing.T) {
		e := newSendEnv(t)
		token := tokenOf(t, e.confirmPage(t).Body.String())
		v := e.vals("token", token)
		v.Del("provider")
		rec := post(e.s, "/send", v)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status %d", rec.Code)
		}
		if b, _ := e.counts(t); b != 0 {
			t.Errorf("batches = %d", b)
		}
	})
	t.Run("english message", func(t *testing.T) {
		e := newMSEnv(t, true)
		token := tokenOf(t, e.confirmPage(t).Body.String())
		postSettings(e.s, "fr", url.Values{"language": {"auto"}, "sending": {"graph"}})
		rec := send(e.s, http.MethodPost, "/send", e.vals("token", token, "provider", "test"), "en")
		if rec.Code != http.StatusConflict || !strings.Contains(text(rec), "sending mode") {
			t.Errorf("status %d: %s", rec.Code, text(rec))
		}
	})
}
