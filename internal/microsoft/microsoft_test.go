package microsoft

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cafeteria-management/internal/keychain"
	"cafeteria-management/internal/mailer"
)

// memSecrets is an in-memory Secrets.
type memSecrets struct {
	mu        sync.Mutex
	m         map[string]string
	saves     int
	loadErr   error
	saveErr   error
	deleteErr error
}

func newMemSecrets() *memSecrets { return &memSecrets{m: map[string]string{}} }

func (s *memSecrets) Load(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return "", s.loadErr
	}
	v, ok := s.m[name]
	if !ok {
		return "", keychain.ErrNotFound
	}
	return v, nil
}

func (s *memSecrets) Save(name, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.saves++
	s.m[name] = v
	return nil
}

func (s *memSecrets) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.m, name)
	return nil
}

func (s *memSecrets) get(name string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[name]
	return v, ok
}

const (
	testScope = "https://graph.microsoft.com/Mail.Send https://graph.microsoft.com/User.Read"
	testHost  = "127.0.0.1:8080"
)

// env is a Client wired to one httptest server standing for the token
// endpoint and Graph. Handlers are replaced per test.
type env struct {
	t       *testing.T
	srv     *httptest.Server
	c       *Client
	secrets *memSecrets
	clock   time.Time

	mu         sync.Mutex
	tokenForms []url.Values
	meCalls    int
	sendCalls  int
	sendBodies [][]byte
	sendAuths  []string

	// token answers the token endpoint; default answers by grant type.
	token func(w http.ResponseWriter, form url.Values)
	// send answers sendMail; default is 202.
	send func(w http.ResponseWriter, r *http.Request, n int)
	// me answers /me; default is a valid account.
	me func(w http.ResponseWriter, r *http.Request)

	refreshN atomic.Int32
}

func jsonReply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, secrets: newMemSecrets(), clock: time.Now().Truncate(time.Second)}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		// Client authentication goes in the form body, in one request.
		if r.PostForm.Get("client_id") != "client-123" {
			t.Errorf("client_id = %q in the token request body", r.PostForm.Get("client_id"))
		}
		if strings.HasPrefix(r.Header.Get("Authorization"), "Basic") {
			t.Error("token request uses HTTP Basic authentication")
		}
		e.mu.Lock()
		e.tokenForms = append(e.tokenForms, r.PostForm)
		e.mu.Unlock()
		if e.token != nil {
			e.token(w, r.PostForm)
			return
		}
		e.defaultToken(w, r.PostForm)
	})
	mux.HandleFunc("/me", func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.meCalls++
		e.mu.Unlock()
		if e.me != nil {
			e.me(w, r)
			return
		}
		jsonReply(w, 200, map[string]string{"displayName": "Léa Étienne", "mail": "lea@school.example", "userPrincipalName": "upn@school.example"})
	})
	mux.HandleFunc("/me/sendMail", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		e.sendCalls++
		n := e.sendCalls
		e.sendBodies = append(e.sendBodies, body)
		e.sendAuths = append(e.sendAuths, r.Header.Get("Authorization"))
		e.mu.Unlock()
		if e.send != nil {
			e.send(w, r, n)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	e.c = New(Config{
		ClientID: "client-123", TenantID: "tenant-456",
		AuthURL: e.srv.URL + "/authorize", TokenURL: e.srv.URL + "/token", GraphURL: e.srv.URL,
	}, e.secrets)
	e.c.now = func() time.Time { return e.clock }
	return e
}

func (e *env) defaultToken(w http.ResponseWriter, form url.Values) {
	switch form.Get("grant_type") {
	case "authorization_code":
		jsonReply(w, 200, map[string]any{"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 3600, "token_type": "Bearer", "scope": testScope})
	case "refresh_token":
		n := e.refreshN.Add(1)
		jsonReply(w, 200, map[string]any{"access_token": fmt.Sprintf("access-r%d", n), "refresh_token": fmt.Sprintf("refresh-r%d", n), "expires_in": 3600, "token_type": "Bearer"})
	default:
		jsonReply(w, 400, map[string]string{"error": "unsupported_grant_type"})
	}
}

func (e *env) forms() []url.Values {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]url.Values(nil), e.tokenForms...)
}

func (e *env) counts() (me, send int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.meCalls, e.sendCalls
}

// signIn runs a complete sign-in and returns nothing: the client is connected.
func (e *env) signIn() {
	e.t.Helper()
	authURL, err := e.c.BeginSignIn(testHost)
	if err != nil {
		e.t.Fatal(err)
	}
	u, _ := url.Parse(authURL)
	if _, err := e.c.FinishSignIn(context.Background(), u.Query().Get("state"), "code-1"); err != nil {
		e.t.Fatal(err)
	}
}

// expireAccess makes the cached access token due for refresh.
func (e *env) expireAccess() {
	e.c.stateMu.Lock()
	e.c.expiry = e.clock.Add(time.Minute)
	e.c.stateMu.Unlock()
}

func message() mailer.Message {
	return mailer.Message{
		To:      mail.Address{Name: `Éloïse "Lou" <D>`, Address: "eloise@example.fr"},
		Subject: "Menu de la cantine",
		Body:    "Bonjour,\nÀ bientôt",
	}
}

// ---- BeginSignIn ----

func TestBeginSignInURL(t *testing.T) {
	e := newEnv(t)
	raw, err := e.c.BeginSignIn("127.0.0.1:9090")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, e.srv.URL+"/authorize?") {
		t.Errorf("url = %s", raw)
	}
	q := u.Query()
	checks := map[string]string{
		"client_id":             "client-123",
		"response_type":         "code",
		"redirect_uri":          "http://localhost:9090/auth/microsoft/callback",
		"code_challenge_method": "S256",
		"prompt":                "select_account",
	}
	for k, want := range checks {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	scopes := strings.Fields(q.Get("scope"))
	for _, want := range []string{scopeMailSend, scopeUserRead, "offline_access"} {
		found := false
		for _, s := range scopes {
			found = found || s == want
		}
		if !found {
			t.Errorf("scope %q missing from %v", want, scopes)
		}
	}
	if q.Get("state") == "" || q.Get("code_challenge") == "" {
		t.Errorf("state or challenge empty: %v", q)
	}
}

func TestBeginSignInFreshState(t *testing.T) {
	e := newEnv(t)
	seen := map[string]bool{}
	for range 4 {
		raw, err := e.c.BeginSignIn(testHost)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(raw)
		st := u.Query().Get("state")
		if seen[st] {
			t.Fatalf("state %q repeated", st)
		}
		seen[st] = true
	}
}

func TestBeginSignInRedirectPorts(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		host    string
		want    string
		wantErr bool
	}{
		{"127.0.0.1:8080", "http://localhost:8080/auth/microsoft/callback", false},
		{"localhost:3000", "http://localhost:3000/auth/microsoft/callback", false},
		{"[::1]:8080", "http://localhost:8080/auth/microsoft/callback", false},
		{"localhost", "http://localhost/auth/microsoft/callback", false},
		{"127.0.0.1:0", "", true},
		{"127.0.0.1:99999", "", true},
		{"127.0.0.1:abc", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			raw, err := e.c.BeginSignIn(tt.host)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %s", raw)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(raw)
			if got := u.Query().Get("redirect_uri"); got != tt.want {
				t.Errorf("redirect_uri = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---- FinishSignIn ----

func TestFinishSignInSuccess(t *testing.T) {
	e := newEnv(t)
	raw, err := e.c.BeginSignIn("127.0.0.1:9090")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	state, challenge := u.Query().Get("state"), u.Query().Get("code_challenge")

	host, err := e.c.FinishSignIn(context.Background(), state, "the-code")
	if err != nil {
		t.Fatal(err)
	}
	if host != "127.0.0.1:9090" {
		t.Errorf("return host = %q", host)
	}

	forms := e.forms()
	if len(forms) != 1 {
		t.Fatalf("token calls = %d", len(forms))
	}
	f := forms[0]
	if f.Get("grant_type") != "authorization_code" || f.Get("code") != "the-code" {
		t.Errorf("form = %v", f)
	}
	if f.Get("redirect_uri") != u.Query().Get("redirect_uri") {
		t.Errorf("redirect_uri = %q", f.Get("redirect_uri"))
	}
	sum := sha256.Sum256([]byte(f.Get("code_verifier")))
	if f.Get("code_verifier") == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
		t.Error("code_verifier does not match the code_challenge")
	}
	if me, _ := e.counts(); me != 1 {
		t.Errorf("/me calls = %d", me)
	}

	acc, ok := e.c.Account()
	if !ok || acc != (Account{Name: "Léa Étienne", Address: "lea@school.example"}) {
		t.Errorf("Account = %+v, %v", acc, ok)
	}
	stored, ok := e.secrets.get(secretName)
	if !ok {
		t.Fatal("nothing in the keychain")
	}
	var st storedSignIn
	if err := json.Unmarshal([]byte(stored), &st); err != nil {
		t.Fatal(err)
	}
	if st != (storedSignIn{RefreshToken: "refresh-1", Name: "Léa Étienne", Address: "lea@school.example"}) {
		t.Errorf("stored = %+v", st)
	}
	if strings.Contains(stored, "access-1") {
		t.Error("access token must never be stored")
	}
}

func TestFinishSignInAccountFallbacks(t *testing.T) {
	tests := []struct {
		name string
		me   map[string]string
		want Account
		fail bool
	}{
		{"upn when no mail", map[string]string{"displayName": "Lea", "userPrincipalName": "upn@s.example"}, Account{"Lea", "upn@s.example"}, false},
		{"name defaults to address", map[string]string{"mail": "m@s.example"}, Account{"m@s.example", "m@s.example"}, false},
		{"no address", map[string]string{"displayName": "Lea"}, Account{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.me = func(w http.ResponseWriter, r *http.Request) { jsonReply(w, 200, tt.me) }
			raw, _ := e.c.BeginSignIn(testHost)
			u, _ := url.Parse(raw)
			_, err := e.c.FinishSignIn(context.Background(), u.Query().Get("state"), "c")
			if tt.fail {
				if err == nil {
					t.Fatal("want error")
				}
				if _, ok := e.secrets.get(secretName); ok {
					t.Error("nothing must be saved")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if acc, _ := e.c.Account(); acc != tt.want {
				t.Errorf("Account = %+v, want %+v", acc, tt.want)
			}
		})
	}
}

func TestFinishSignInMeFailureSavesNothing(t *testing.T) {
	e := newEnv(t)
	e.me = func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, 500, map[string]string{"x": "SECRET-GRAPH-TEXT"})
	}
	raw, _ := e.c.BeginSignIn(testHost)
	u, _ := url.Parse(raw)
	_, err := e.c.FinishSignIn(context.Background(), u.Query().Get("state"), "c")
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "SECRET-GRAPH-TEXT") {
		t.Error("error leaks the response body")
	}
	if _, ok := e.secrets.get(secretName); ok {
		t.Error("nothing must be saved")
	}
	if _, ok := e.c.Account(); ok {
		t.Error("must not be connected")
	}
}

func TestFinishSignInStateErrors(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.c.FinishSignIn(context.Background(), "nope", "c"); !errors.Is(err, ErrSignInExpired) {
			t.Errorf("err = %v", err)
		}
		if len(e.forms()) != 0 {
			t.Error("token endpoint must not be called")
		}
	})
	t.Run("reused", func(t *testing.T) {
		e := newEnv(t)
		raw, _ := e.c.BeginSignIn(testHost)
		u, _ := url.Parse(raw)
		state := u.Query().Get("state")
		if _, err := e.c.FinishSignIn(context.Background(), state, "c"); err != nil {
			t.Fatal(err)
		}
		if _, err := e.c.FinishSignIn(context.Background(), state, "c"); !errors.Is(err, ErrSignInExpired) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("too old", func(t *testing.T) {
		e := newEnv(t)
		raw, _ := e.c.BeginSignIn(testHost)
		u, _ := url.Parse(raw)
		e.clock = e.clock.Add(10 * time.Minute)
		if _, err := e.c.FinishSignIn(context.Background(), u.Query().Get("state"), "c"); !errors.Is(err, ErrSignInExpired) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("just under ten minutes", func(t *testing.T) {
		e := newEnv(t)
		raw, _ := e.c.BeginSignIn(testHost)
		u, _ := url.Parse(raw)
		e.clock = e.clock.Add(10*time.Minute - time.Second)
		if _, err := e.c.FinishSignIn(context.Background(), u.Query().Get("state"), "c"); err != nil {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("sixth pending drops the first", func(t *testing.T) {
		e := newEnv(t)
		var states []string
		for range 6 {
			raw, _ := e.c.BeginSignIn(testHost)
			u, _ := url.Parse(raw)
			states = append(states, u.Query().Get("state"))
		}
		if _, err := e.c.FinishSignIn(context.Background(), states[0], "c"); !errors.Is(err, ErrSignInExpired) {
			t.Errorf("first: err = %v", err)
		}
		if _, err := e.c.FinishSignIn(context.Background(), states[5], "c"); err != nil {
			t.Errorf("sixth: err = %v", err)
		}
		if _, err := e.c.FinishSignIn(context.Background(), states[1], "c"); err != nil {
			t.Errorf("second: err = %v", err)
		}
	})
}

func TestFinishSignInScopeCheck(t *testing.T) {
	tests := []struct {
		name  string
		scope string
		ok    bool
	}{
		{"full URLs", testScope, true},
		{"short names", "Mail.Send User.Read", true},
		{"case insensitive", "mail.send", true},
		{"only user read", "https://graph.microsoft.com/User.Read", false},
		{"lookalike", "Mail.SendAs", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.token = func(w http.ResponseWriter, f url.Values) {
				jsonReply(w, 200, map[string]any{"access_token": "a", "refresh_token": "r", "expires_in": 3600, "token_type": "Bearer", "scope": tt.scope})
			}
			raw, _ := e.c.BeginSignIn(testHost)
			u, _ := url.Parse(raw)
			_, err := e.c.FinishSignIn(context.Background(), u.Query().Get("state"), "c")
			if tt.ok {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, ErrConsent) {
				t.Errorf("err = %v, want ErrConsent", err)
			}
			if _, ok := e.secrets.get(secretName); ok {
				t.Error("nothing must be saved")
			}
			if _, ok := e.c.Account(); ok {
				t.Error("must not be connected")
			}
			if me, _ := e.counts(); me != 0 {
				t.Error("/me must not be called")
			}
		})
	}
}

func TestFinishSignInNoRefreshToken(t *testing.T) {
	e := newEnv(t)
	e.token = func(w http.ResponseWriter, f url.Values) {
		jsonReply(w, 200, map[string]any{"access_token": "a", "expires_in": 3600, "token_type": "Bearer", "scope": testScope})
	}
	raw, _ := e.c.BeginSignIn(testHost)
	u, _ := url.Parse(raw)
	if _, err := e.c.FinishSignIn(context.Background(), u.Query().Get("state"), "c"); err == nil {
		t.Fatal("want error")
	}
	if _, ok := e.secrets.get(secretName); ok {
		t.Error("nothing must be saved")
	}
}

func TestFinishSignInTokenEndpointErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		consent bool
	}{
		{"invalid grant", 400, `{"error":"invalid_grant","error_description":"SECRET-DESCRIPTION"}`, false},
		{"server error", 500, `SECRET-HTML-PAGE`, false},
		{"consent required", 400, `{"error":"invalid_grant","error_description":"AADSTS65001: The user has not consented"}`, true},
		{"admin approval", 400, `{"error":"access_denied","error_description":"AADSTS90094: admin approval"}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.token = func(w http.ResponseWriter, f url.Values) {
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			}
			raw, _ := e.c.BeginSignIn(testHost)
			u, _ := url.Parse(raw)
			_, err := e.c.FinishSignIn(context.Background(), u.Query().Get("state"), "c")
			if err == nil {
				t.Fatal("want error")
			}
			if errors.Is(err, ErrConsent) != tt.consent {
				t.Errorf("ErrConsent = %v, want %v (err %v)", errors.Is(err, ErrConsent), tt.consent, err)
			}
			for _, leak := range []string{"SECRET-DESCRIPTION", "SECRET-HTML-PAGE", "has not consented", "admin approval"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("error leaks Microsoft's text %q: %v", leak, err)
				}
			}
			if _, ok := e.secrets.get(secretName); ok {
				t.Error("nothing must be saved")
			}
			if _, ok := e.c.Account(); ok {
				t.Error("must not be connected")
			}
		})
	}
}

func TestFinishSignInKeychainErrorLeavesDisconnected(t *testing.T) {
	e := newEnv(t)
	e.secrets.saveErr = errors.New("keychain locked")
	raw, _ := e.c.BeginSignIn(testHost)
	u, _ := url.Parse(raw)
	if _, err := e.c.FinishSignIn(context.Background(), u.Query().Get("state"), "c"); err == nil {
		t.Fatal("want error")
	}
	if _, ok := e.c.Account(); ok {
		t.Error("must not be connected when the sign-in cannot be kept")
	}
}

func TestFinishSignInReplacesPreviousSignIn(t *testing.T) {
	e := newEnv(t)
	e.signIn()
	e.me = func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, 200, map[string]string{"displayName": "Other", "mail": "other@school.example"})
	}
	e.signIn()
	if acc, _ := e.c.Account(); acc.Address != "other@school.example" {
		t.Errorf("Account = %+v", acc)
	}
}

// ---- SignInError ----

func TestSignInError(t *testing.T) {
	tests := []struct {
		name, code, desc string
		is               error
		generic          bool
	}{
		{"denied", "access_denied", "AADSTS65004: The user declined", ErrDenied, false},
		{"denied no description", "access_denied", "", ErrDenied, false},
		{"consent 65001", "invalid_grant", "AADSTS65001: no consent", ErrConsent, false},
		{"admin 90094", "access_denied", "AADSTS90094: admin approval", ErrConsent, false},
		{"admin 90095", "access_denied", "AADSTS90095: admin", ErrConsent, false},
		{"generic", "server_error", "boom", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := SignInError(tt.code, tt.desc)
			if tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("err = %v, want %v", err, tt.is)
			}
			if tt.generic {
				if errors.Is(err, ErrDenied) || errors.Is(err, ErrConsent) {
					t.Errorf("err = %v is not generic", err)
				}
				if strings.Contains(err.Error(), "boom") {
					t.Error("description must not be in the error")
				}
			}
		})
	}
	t.Run("crafted code is not echoed", func(t *testing.T) {
		err := SignInError("<script>alert(1)</script>", "")
		if strings.Contains(err.Error(), "script") {
			t.Errorf("err = %v", err)
		}
	})
}

// ---- Send ----

func TestSendRequest(t *testing.T) {
	e := newEnv(t)
	e.signIn()
	if err := e.c.Send(context.Background(), message()); err != nil {
		t.Fatal(err)
	}
	if _, n := e.counts(); n != 1 {
		t.Fatalf("send calls = %d", n)
	}
	if e.sendAuths[0] != "Bearer access-1" {
		t.Errorf("Authorization = %q", e.sendAuths[0])
	}
	want := `{
		"message": {
			"subject": "Menu de la cantine",
			"body": {"contentType": "Text", "content": "Bonjour,\nÀ bientôt"},
			"toRecipients": [{"emailAddress": {"name": "Éloïse \"Lou\" <D>", "address": "eloise@example.fr"}}]
		},
		"saveToSentItems": true
	}`
	var got, exp any
	if err := json.Unmarshal(e.sendBodies[0], &got); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(want), &exp)
	if fmt.Sprint(got) != fmt.Sprint(exp) {
		t.Errorf("body = %s", e.sendBodies[0])
	}
}

func TestSendOmitsEmptyName(t *testing.T) {
	e := newEnv(t)
	e.signIn()
	m := message()
	m.To.Name = ""
	if err := e.c.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(e.sendBodies[0], []byte(`"name"`)) {
		t.Errorf("body = %s", e.sendBodies[0])
	}
}

func TestSendSuccessStatuses(t *testing.T) {
	for _, st := range []int{200, 201, 202, 204} {
		t.Run(fmt.Sprint(st), func(t *testing.T) {
			e := newEnv(t)
			e.signIn()
			e.send = func(w http.ResponseWriter, r *http.Request, n int) { w.WriteHeader(st) }
			if err := e.c.Send(context.Background(), message()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSendErrorMapping(t *testing.T) {
	graphJSON := `{"error":{"code":"ErrorInvalidRecipients","message":"bad recipient"}}`
	date := func(e *env, d time.Duration) string { return e.clock.Add(d).UTC().Format(http.TimeFormat) }
	tests := []struct {
		name       string
		status     int
		retryAfter func(e *env) string
		body       string
		check      func(t *testing.T, err error)
	}{
		{"429 seconds", 429, func(*env) string { return "7" }, "", func(t *testing.T, err error) { wantRate(t, err, 7*time.Second) }},
		{"429 http date", 429, func(e *env) string { return date(e, 30*time.Second) }, "", func(t *testing.T, err error) { wantRate(t, err, 30*time.Second) }},
		{"429 no header", 429, nil, "", func(t *testing.T, err error) { wantRate(t, err, 0) }},
		{"429 garbage header", 429, func(*env) string { return "soon" }, "", func(t *testing.T, err error) { wantRate(t, err, 0) }},
		{"429 negative", 429, func(*env) string { return "-5" }, "", func(t *testing.T, err error) { wantRate(t, err, 0) }},
		{"429 past date", 429, func(e *env) string { return date(e, -time.Hour) }, "", func(t *testing.T, err error) { wantRate(t, err, 0) }},
		{"503 is uncertain, never retried", 503, func(*env) string { return "3" }, "", wantKind(mailer.ErrUncertain)},
		{"503 no header", 503, nil, "", wantKind(mailer.ErrUncertain)},
		{"403", 403, nil, `{"error":{"code":"ErrorAccessDenied","message":"x"}}`, wantKind(mailer.ErrAccount)},
		{"401 twice", 401, nil, "", wantKind(mailer.ErrAccount)},
		{"500", 500, nil, "", wantKind(mailer.ErrUncertain)},
		{"502", 502, nil, "", wantKind(mailer.ErrUncertain)},
		{"504", 504, nil, "", wantKind(mailer.ErrUncertain)},
		{"400 graph json", 400, nil, graphJSON, func(t *testing.T, err error) {
			if err == nil {
				t.Fatal("want error")
			}
			if errors.Is(err, mailer.ErrAccount) || errors.Is(err, mailer.ErrUncertain) {
				t.Errorf("err = %v must be a plain failure", err)
			}
			var rl *mailer.RateLimitError
			if errors.As(err, &rl) {
				t.Error("must not be a rate limit")
			}
			if !strings.Contains(err.Error(), "ErrorInvalidRecipients") || !strings.Contains(err.Error(), "400") {
				t.Errorf("err = %v", err)
			}
		}},
		{"400 not json", 400, nil, "<html>oops</html>", func(t *testing.T, err error) {
			if err == nil || errors.Is(err, mailer.ErrAccount) || errors.Is(err, mailer.ErrUncertain) {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), "oops") || !strings.Contains(err.Error(), "400") {
				t.Errorf("err = %v", err)
			}
		}},
		{"404", 404, nil, "", func(t *testing.T, err error) {
			if err == nil || errors.Is(err, mailer.ErrAccount) || errors.Is(err, mailer.ErrUncertain) {
				t.Fatalf("err = %v", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.signIn()
			e.send = func(w http.ResponseWriter, r *http.Request, n int) {
				if tt.retryAfter != nil {
					w.Header().Set("Retry-After", tt.retryAfter(e))
				}
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			}
			tt.check(t, e.c.Send(context.Background(), message()))
		})
	}
}

func wantRate(t *testing.T, err error, d time.Duration) {
	t.Helper()
	var rl *mailer.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("err = %v, want RateLimitError", err)
	}
	if rl.RetryAfter != d {
		t.Errorf("RetryAfter = %v, want %v", rl.RetryAfter, d)
	}
	if errors.Is(err, mailer.ErrAccount) || errors.Is(err, mailer.ErrUncertain) {
		t.Errorf("rate limit must not be account/uncertain: %v", err)
	}
}

func wantKind(kind error) func(t *testing.T, err error) {
	return func(t *testing.T, err error) {
		t.Helper()
		if !errors.Is(err, kind) {
			t.Fatalf("err = %v, want %v", err, kind)
		}
		var rl *mailer.RateLimitError
		if errors.As(err, &rl) {
			t.Error("unexpected rate limit")
		}
	}
}

func TestSendErrorsKeepResponseBodyOut(t *testing.T) {
	// 401/403/5xx carry Graph's code, but the raw body must not leak when
	// it is not the Graph error JSON.
	for _, st := range []int{401, 403, 500, 429} {
		t.Run(fmt.Sprint(st), func(t *testing.T) {
			e := newEnv(t)
			e.signIn()
			e.send = func(w http.ResponseWriter, r *http.Request, n int) {
				w.WriteHeader(st)
				io.WriteString(w, "RAW-BODY-WITH-TOKEN-eyJ0")
			}
			err := e.c.Send(context.Background(), message())
			if err == nil || strings.Contains(err.Error(), "RAW-BODY") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestSend401ThenSuccessRefreshesOnce(t *testing.T) {
	e := newEnv(t)
	e.signIn()
	e.send = func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(202)
	}
	if err := e.c.Send(context.Background(), message()); err != nil {
		t.Fatal(err)
	}
	if e.refreshN.Load() != 1 {
		t.Errorf("refreshes = %d, want 1", e.refreshN.Load())
	}
	if _, n := e.counts(); n != 2 {
		t.Errorf("send calls = %d, want 2", n)
	}
	if e.sendAuths[0] != "Bearer access-1" || e.sendAuths[1] != "Bearer access-r1" {
		t.Errorf("auths = %v", e.sendAuths)
	}
}

func TestSend401TwiceIsAccountError(t *testing.T) {
	e := newEnv(t)
	e.signIn()
	e.send = func(w http.ResponseWriter, r *http.Request, n int) { w.WriteHeader(401) }
	err := e.c.Send(context.Background(), message())
	if !errors.Is(err, mailer.ErrAccount) {
		t.Fatalf("err = %v", err)
	}
	if _, n := e.counts(); n != 2 {
		t.Errorf("send calls = %d, want exactly 2 (no further retry)", n)
	}
	if e.refreshN.Load() != 1 {
		t.Errorf("refreshes = %d", e.refreshN.Load())
	}
}

func TestSendNotSignedInMakesNoCall(t *testing.T) {
	e := newEnv(t)
	err := e.c.Send(context.Background(), message())
	if !errors.Is(err, mailer.ErrAccount) {
		t.Fatalf("err = %v", err)
	}
	if _, n := e.counts(); n != 0 || len(e.forms()) != 0 {
		t.Error("no HTTP call expected")
	}
}

func TestSendConnectionRefusedIsAccountError(t *testing.T) {
	e := newEnv(t)
	e.signIn()
	e.srv.Close()
	err := e.c.Send(context.Background(), message())
	if !errors.Is(err, mailer.ErrAccount) {
		t.Fatalf("err = %v, want ErrAccount", err)
	}
	if errors.Is(err, mailer.ErrUncertain) {
		t.Error("nothing was written: not uncertain")
	}
}

func TestSendNoAnswerIsUncertain(t *testing.T) {
	e := newEnv(t)
	e.signIn()
	e.send = func(w http.ResponseWriter, r *http.Request, n int) { <-r.Context().Done() }
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := e.c.Send(ctx, message())
	if !errors.Is(err, mailer.ErrUncertain) {
		t.Fatalf("err = %v, want ErrUncertain", err)
	}
	if errors.Is(err, mailer.ErrAccount) {
		t.Error("must not be an account error")
	}
}

// ---- token refresh ----

func TestSendRefreshesNearExpiryAndKeepsRotatedToken(t *testing.T) {
	e := newEnv(t)
	e.signIn()
	e.expireAccess()
	if err := e.c.Send(context.Background(), message()); err != nil {
		t.Fatal(err)
	}
	if e.refreshN.Load() != 1 {
		t.Fatalf("refreshes = %d", e.refreshN.Load())
	}
	if n := len(e.forms()); n != 2 { // sign-in exchange + one refresh
		t.Errorf("token requests = %d, want 2", n)
	}
	forms := e.forms()
	last := forms[len(forms)-1]
	if last.Get("grant_type") != "refresh_token" || last.Get("refresh_token") != "refresh-1" {
		t.Errorf("refresh form = %v", last)
	}
	if e.sendAuths[0] != "Bearer access-r1" {
		t.Errorf("Authorization = %q", e.sendAuths[0])
	}
	stored, _ := e.secrets.get(secretName)
	var st storedSignIn
	_ = json.Unmarshal([]byte(stored), &st)
	if st.RefreshToken != "refresh-r1" || st.Address != "lea@school.example" || st.Name != "Léa Étienne" {
		t.Errorf("stored = %+v", st)
	}

	// The new access token is valid: no second refresh.
	if err := e.c.Send(context.Background(), message()); err != nil {
		t.Fatal(err)
	}
	if e.refreshN.Load() != 1 {
		t.Errorf("refreshes = %d, want still 1", e.refreshN.Load())
	}
}

func TestSendNoRefreshWhenTokenFresh(t *testing.T) {
	e := newEnv(t)
	e.signIn()
	for range 3 {
		if err := e.c.Send(context.Background(), message()); err != nil {
			t.Fatal(err)
		}
	}
	if e.refreshN.Load() != 0 {
		t.Errorf("refreshes = %d", e.refreshN.Load())
	}
}

func TestRefreshWithoutRotationKeepsStoredToken(t *testing.T) {
	e := newEnv(t)
	e.signIn()
	e.token = func(w http.ResponseWriter, f url.Values) {
		jsonReply(w, 200, map[string]any{"access_token": "access-x", "expires_in": 3600, "token_type": "Bearer"})
	}
	e.expireAccess()
	before := e.secrets.saves
	if err := e.c.Send(context.Background(), message()); err != nil {
		t.Fatal(err)
	}
	if e.secrets.saves != before {
		t.Error("keychain must not be rewritten without rotation")
	}
	if e.sendAuths[0] != "Bearer access-x" {
		t.Errorf("auth = %q", e.sendAuths[0])
	}
}

func TestRefreshRotationKeychainFailureStillSends(t *testing.T) {
	e := newEnv(t)
	e.signIn()
	e.secrets.saveErr = errors.New("keychain locked")
	e.expireAccess()
	if err := e.c.Send(context.Background(), message()); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestRefreshRefusedDeletesSignIn(t *testing.T) {
	for _, st := range []int{400, 401, 403} {
		t.Run(fmt.Sprint(st), func(t *testing.T) {
			e := newEnv(t)
			e.signIn()
			e.token = func(w http.ResponseWriter, f url.Values) {
				w.WriteHeader(st)
				io.WriteString(w, `{"error":"invalid_grant","error_description":"AADSTS70008 expired"}`)
			}
			e.expireAccess()
			err := e.c.Send(context.Background(), message())
			if !errors.Is(err, mailer.ErrAccount) {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), "AADSTS70008") {
				t.Errorf("err leaks Microsoft's text: %v", err)
			}
			if _, ok := e.secrets.get(secretName); ok {
				t.Error("keychain must be cleared")
			}
			if _, ok := e.c.Account(); ok {
				t.Error("Account() must be false")
			}
			if _, n := e.counts(); n != 0 {
				t.Error("no email must be attempted")
			}
		})
	}
}

func TestRefreshTemporaryFailureKeepsSignIn(t *testing.T) {
	for _, st := range []int{408, 429, 500, 503} {
		t.Run(fmt.Sprint(st), func(t *testing.T) {
			e := newEnv(t)
			e.signIn()
			e.token = func(w http.ResponseWriter, f url.Values) { w.WriteHeader(st) }
			e.expireAccess()
			err := e.c.Send(context.Background(), message())
			if !errors.Is(err, mailer.ErrAccount) {
				t.Fatalf("err = %v", err)
			}
			if _, ok := e.secrets.get(secretName); !ok {
				t.Error("keychain must be kept")
			}
			if _, ok := e.c.Account(); !ok {
				t.Error("must stay connected")
			}
		})
	}
}

func TestRefreshTokenEndpointDownKeepsSignIn(t *testing.T) {
	e := newEnv(t)
	e.signIn()
	e.expireAccess()
	e.srv.Close()
	err := e.c.Send(context.Background(), message())
	if !errors.Is(err, mailer.ErrAccount) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := e.secrets.get(secretName); !ok {
		t.Error("keychain must be kept")
	}
}

// ---- Load / SignOut ----

func storedJSON(t *testing.T, st storedSignIn) string {
	t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLoad(t *testing.T) {
	good := storedSignIn{RefreshToken: "rt", Name: "Lea", Address: "lea@school.example"}
	tests := []struct {
		name      string
		setup     func(t *testing.T, s *memSecrets)
		connected bool
		wantErr   bool
	}{
		{"stored", func(t *testing.T, s *memSecrets) { s.m[secretName] = storedJSON(t, good) }, true, false},
		{"missing", func(t *testing.T, s *memSecrets) {}, false, false},
		{"corrupted json", func(t *testing.T, s *memSecrets) { s.m[secretName] = "{not json" }, false, false},
		{"no refresh token", func(t *testing.T, s *memSecrets) {
			s.m[secretName] = storedJSON(t, storedSignIn{Name: "x", Address: "a@b.c"})
		}, false, false},
		{"no address", func(t *testing.T, s *memSecrets) {
			s.m[secretName] = storedJSON(t, storedSignIn{RefreshToken: "rt"})
		}, false, false},
		{"backend error", func(t *testing.T, s *memSecrets) { s.loadErr = errors.New("keychain locked") }, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			tt.setup(t, e.secrets)
			err := e.c.Load()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load err = %v", err)
			}
			acc, ok := e.c.Account()
			if ok != tt.connected {
				t.Fatalf("connected = %v", ok)
			}
			if ok && acc != (Account{Name: "Lea", Address: "lea@school.example"}) {
				t.Errorf("Account = %+v", acc)
			}
		})
	}
}

func TestLoadedSignInSendsAfterRefresh(t *testing.T) {
	e := newEnv(t)
	e.secrets.m[secretName] = storedJSON(t, storedSignIn{RefreshToken: "stored-rt", Name: "L", Address: "l@s.example"})
	if err := e.c.Load(); err != nil {
		t.Fatal(err)
	}
	if err := e.c.Send(context.Background(), message()); err != nil {
		t.Fatal(err)
	}
	forms := e.forms()
	if len(forms) != 1 || forms[0].Get("refresh_token") != "stored-rt" {
		t.Errorf("forms = %v", forms)
	}
}

func TestSignOut(t *testing.T) {
	e := newEnv(t)
	e.signIn()
	if err := e.c.SignOut(); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.secrets.get(secretName); ok {
		t.Error("keychain not cleared")
	}
	if _, ok := e.c.Account(); ok {
		t.Error("still connected")
	}
	if err := e.c.Send(context.Background(), message()); !errors.Is(err, mailer.ErrAccount) {
		t.Errorf("Send after sign out = %v", err)
	}
	if _, n := e.counts(); n != 0 {
		t.Error("no HTTP call after sign out")
	}
}

func TestSignOutKeychainErrorStillClearsMemory(t *testing.T) {
	e := newEnv(t)
	e.signIn()
	e.secrets.deleteErr = errors.New("locked")
	if err := e.c.SignOut(); err == nil {
		t.Fatal("want error")
	}
	if _, ok := e.c.Account(); ok {
		t.Error("memory must be cleared")
	}
}

func TestSignOutWhenNotSignedIn(t *testing.T) {
	e := newEnv(t)
	if err := e.c.SignOut(); err != nil {
		t.Fatal(err)
	}
}

// ---- concurrency ----

func TestConcurrentSendsWithExpiredTokenRefreshOnce(t *testing.T) {
	e := newEnv(t)
	e.signIn()
	e.expireAccess()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- e.c.Send(context.Background(), message())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Send = %v", err)
		}
	}
	if e.refreshN.Load() != 1 {
		t.Errorf("refreshes = %d, want 1", e.refreshN.Load())
	}
}

func TestConcurrentSendSignOutAndSignIn(t *testing.T) {
	e := newEnv(t)
	e.signIn()
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(3)
		go func() {
			defer wg.Done()
			_ = e.c.Send(context.Background(), message())
		}()
		go func() {
			defer wg.Done()
			_ = e.c.SignOut()
		}()
		go func() {
			defer wg.Done()
			raw, err := e.c.BeginSignIn(testHost)
			if err != nil {
				t.Error(err)
				return
			}
			u, _ := url.Parse(raw)
			_, _ = e.c.FinishSignIn(context.Background(), u.Query().Get("state"), "c")
			_, _ = e.c.Account()
		}()
	}
	wg.Wait()
}

func TestSignOutDuringRefreshIsNotUndone(t *testing.T) {
	e := newEnv(t)
	e.signIn()
	e.expireAccess()
	inRefresh, release := make(chan struct{}), make(chan struct{})
	e.token = func(w http.ResponseWriter, f url.Values) {
		close(inRefresh)
		<-release
		e.defaultToken(w, f)
	}
	done := make(chan error, 1)
	go func() { done <- e.c.Send(context.Background(), message()) }()
	<-inRefresh
	if err := e.c.SignOut(); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, mailer.ErrAccount) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := e.secrets.get(secretName); ok {
		t.Error("late refresh rewrote the keychain after sign out")
	}
	if _, ok := e.c.Account(); ok {
		t.Error("reconnected by a late refresh")
	}
}

// ---- logs ----

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestLogsNeverContainSecrets(t *testing.T) {
	buf := &syncBuf{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	e := newEnv(t)
	raw, _ := e.c.BeginSignIn(testHost)
	u, _ := url.Parse(raw)
	state := u.Query().Get("state")
	if _, err := e.c.FinishSignIn(context.Background(), state, "SECRET-CODE"); err != nil {
		t.Fatal(err)
	}
	// Rotation failing, refresh refused, Graph error text: every warning path.
	e.secrets.saveErr = errors.New("locked")
	e.expireAccess()
	_ = e.c.Send(context.Background(), message())
	e.send = func(w http.ResponseWriter, r *http.Request, n int) {
		jsonReply(w, 400, map[string]any{"error": map[string]string{"code": "BadThing", "message": "GRAPH-ERROR-TEXT"}})
	}
	_ = e.c.Send(context.Background(), message())
	e.token = func(w http.ResponseWriter, f url.Values) {
		jsonReply(w, 400, map[string]string{"error": "invalid_grant", "error_description": "TOKEN-ERROR-TEXT"})
	}
	e.expireAccess()
	_ = e.c.Send(context.Background(), message())
	_ = SignInError("access_denied", "DESC-TEXT")

	out := buf.String()
	for _, secret := range []string{
		"SECRET-CODE", state, "access-1", "access-r1", "refresh-1", "refresh-r1",
		"eloise@example.fr", "Éloïse", "lea@school.example", "Léa", "Menu de la cantine",
		"GRAPH-ERROR-TEXT", "TOKEN-ERROR-TEXT", "DESC-TEXT",
	} {
		if strings.Contains(out, secret) {
			t.Errorf("logs contain %q:\n%s", secret, out)
		}
	}
}

// ---- helpers ----

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{" 12 ", 12 * time.Second},
		{"0", 0},
		{"-1", 0},
		{"abc", 0},
		{now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0},
	}
	for _, tt := range tests {
		if got := parseRetryAfter(tt.in, now); got != tt.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestNewDefaults(t *testing.T) {
	c := New(Config{ClientID: "id", TenantID: "t"}, newMemSecrets())
	if c.graphURL != defaultGraphURL {
		t.Errorf("graphURL = %q", c.graphURL)
	}
	raw, err := c.BeginSignIn(testHost)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "https://login.microsoftonline.com/t/oauth2/v2.0/authorize?") {
		t.Errorf("url = %s", raw)
	}
}

// Compile-time check: Client is a mailer.Sender.
var _ mailer.Sender = (*Client)(nil)
