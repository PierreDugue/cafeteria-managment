// Package microsoft signs the user in to Microsoft 365 (OAuth2
// authorization code + PKCE through the app's own localhost redirect) and
// sends emails with Microsoft Graph (SPEC §5.1). It implements
// mailer.Sender. The refresh token lives in the OS keychain only; the
// access token stays in memory.
//
// Nothing here logs tokens, codes, state, the account or the text of
// Microsoft's errors (SPEC §8).
package microsoft

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/microsoft"

	"cafeteria-management/internal/keychain"
	"cafeteria-management/internal/mailer"
)

const (
	defaultGraphURL = "https://graph.microsoft.com/v1.0"
	// secretName is the keychain entry holding the sign-in.
	secretName = "microsoft"
	// callbackPath must match the redirect URI registered by IT.
	callbackPath = "/auth/microsoft/callback"

	scopeMailSend = "https://graph.microsoft.com/Mail.Send"
	scopeUserRead = "https://graph.microsoft.com/User.Read"

	pendingTTL = 10 * time.Minute
	maxPending = 5
	// refreshMargin: an access token expiring sooner is refreshed first.
	refreshMargin = 5 * time.Minute
	// maxBody bounds the response bodies read.
	maxBody = 64 << 10
)

// Config identifies the app registration. Zero URLs mean Microsoft's real
// endpoints; tests set them.
type Config struct {
	ClientID, TenantID          string
	AuthURL, TokenURL, GraphURL string
}

// Account is the connected Microsoft 365 account: the sender of every email.
type Account struct {
	Name    string // displayName
	Address string // mail, else userPrincipalName
}

// Secrets is where the sign-in is kept (satisfied by *keychain.Keychain).
// Load returns keychain.ErrNotFound when nothing intact is stored.
type Secrets interface {
	Load(name string) (string, error)
	Save(name, secret string) error
	Delete(name string) error
}

// Sign-in errors the server maps to notices.
var (
	ErrSignInExpired = errors.New("sign-in request unknown or expired")
	ErrDenied        = errors.New("sign-in cancelled by the user")
	ErrConsent       = errors.New("the organization has not granted the permissions")
)

// storedSignIn is the JSON kept in the keychain.
type storedSignIn struct {
	RefreshToken string `json:"refresh_token"`
	Name         string `json:"name"`
	Address      string `json:"address"`
}

type pendingSignIn struct {
	state       string
	verifier    string
	returnHost  string
	redirectURL string
	created     time.Time
}

// Client signs in to Microsoft 365 and sends emails through Graph. Safe for
// concurrent use. It implements mailer.Sender.
type Client struct {
	oauth    oauth2.Config
	graphURL string
	http     *http.Client
	secrets  Secrets
	now      func() time.Time

	// stateMu protects the sign-in state below. Keychain writes happen
	// under it, so a sign-out can never be undone by a late refresh.
	stateMu sync.RWMutex
	signed  bool
	refresh string
	access  string
	expiry  time.Time
	account Account
	gen     uint64 // bumped on every sign-in and sign-out

	// refreshMu serializes token refreshes (no network I/O under stateMu).
	refreshMu sync.Mutex

	pendingMu sync.Mutex
	pending   []pendingSignIn // oldest first
}

// New builds a Client. Call Load once to read a previous sign-in.
func New(cfg Config, s Secrets) *Client {
	ep := microsoft.AzureADEndpoint(cfg.TenantID)
	// A public client has no secret: Entra rejects Basic auth, so skip the
	// auto-detection that would send a failing request first.
	ep.AuthStyle = oauth2.AuthStyleInParams
	if cfg.AuthURL != "" {
		ep.AuthURL = cfg.AuthURL
	}
	if cfg.TokenURL != "" {
		ep.TokenURL = cfg.TokenURL
	}
	graph := cfg.GraphURL
	if graph == "" {
		graph = defaultGraphURL
	}
	return &Client{
		oauth: oauth2.Config{
			ClientID: cfg.ClientID,
			Endpoint: ep,
			Scopes:   []string{scopeMailSend, scopeUserRead, "offline_access"},
		},
		graphURL: strings.TrimRight(graph, "/"),
		http:     &http.Client{},
		secrets:  s,
		now:      time.Now,
	}
}

// Load reads the keychain once, at start. A missing or corrupted sign-in is
// not an error (the client is simply not connected); a keychain that cannot
// be read is.
func (c *Client) Load() error {
	raw, err := c.secrets.Load(secretName)
	if errors.Is(err, keychain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load microsoft sign-in: %w", err)
	}
	var st storedSignIn
	if json.Unmarshal([]byte(raw), &st) != nil || st.RefreshToken == "" || st.Address == "" {
		return nil
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.signed, c.refresh = true, st.RefreshToken
	c.access, c.expiry = "", time.Time{}
	c.account = Account{Name: st.Name, Address: st.Address}
	c.gen++
	return nil
}

// Account returns the connected account. No I/O.
func (c *Client) Account() (Account, bool) {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.account, c.signed
}

// BeginSignIn starts a sign-in and returns the Microsoft page to send the
// browser to. returnHost (host:port of the request, already checked as
// loopback) gives the port of the redirect URI and is where FinishSignIn
// sends the user back.
func (c *Client) BeginSignIn(returnHost string) (string, error) {
	redirect, err := redirectURL(returnHost)
	if err != nil {
		return "", err
	}
	state := rand.Text()
	verifier := oauth2.GenerateVerifier()

	c.pendingMu.Lock()
	now := c.now()
	c.dropExpired(now)
	if len(c.pending) >= maxPending {
		c.pending = c.pending[len(c.pending)-maxPending+1:]
	}
	c.pending = append(c.pending, pendingSignIn{state, verifier, returnHost, redirect, now})
	c.pendingMu.Unlock()

	cfg := c.oauth
	cfg.RedirectURL = redirect
	return cfg.AuthCodeURL(state,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("prompt", "select_account"),
	), nil
}

// redirectURL builds the registered redirect URI. Entra ignores the port of
// localhost URIs, so any port of the running app matches.
func redirectURL(host string) (string, error) {
	_, port, err := net.SplitHostPort(host)
	if err != nil {
		port = ""
	}
	if port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("invalid host port %q", port)
		}
		return "http://localhost:" + port + callbackPath, nil
	}
	return "http://localhost" + callbackPath, nil
}

func (c *Client) dropExpired(now time.Time) {
	kept := c.pending[:0]
	for _, p := range c.pending {
		if now.Sub(p.created) < pendingTTL {
			kept = append(kept, p)
		}
	}
	c.pending = kept
}

// takePending removes and returns the pending sign-in of state.
func (c *Client) takePending(state string) (pendingSignIn, bool) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	c.dropExpired(c.now())
	for i, p := range c.pending {
		if p.state == state {
			c.pending = append(c.pending[:i], c.pending[i+1:]...)
			return p, true
		}
	}
	return pendingSignIn{}, false
}

// FinishSignIn completes the sign-in of a callback: it exchanges the code,
// checks the granted permissions, reads the account and keeps the sign-in
// in the keychain. It returns the host to send the user back to. A new
// sign-in replaces the stored one.
func (c *Client) FinishSignIn(ctx context.Context, state, code string) (string, error) {
	p, ok := c.takePending(state)
	if !ok {
		return "", ErrSignInExpired
	}
	cfg := c.oauth
	cfg.RedirectURL = p.redirectURL
	tok, err := cfg.Exchange(c.withHTTP(ctx), code, oauth2.VerifierOption(p.verifier))
	if err != nil {
		return "", c.tokenError("token exchange", err)
	}
	if !hasScope(tok, "Mail.Send") {
		return "", ErrConsent
	}
	if tok.RefreshToken == "" {
		return "", errors.New("token exchange: no refresh token")
	}
	acc, err := c.fetchAccount(ctx, tok.AccessToken)
	if err != nil {
		return "", err
	}

	raw, err := json.Marshal(storedSignIn{RefreshToken: tok.RefreshToken, Name: acc.Name, Address: acc.Address})
	if err != nil {
		return "", err
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if err := c.secrets.Save(secretName, string(raw)); err != nil {
		return "", fmt.Errorf("keep microsoft sign-in: %w", err)
	}
	c.signed, c.refresh = true, tok.RefreshToken
	c.access, c.expiry = tok.AccessToken, tok.Expiry
	c.account = acc
	c.gen++
	return p.returnHost, nil
}

// hasScope reports whether the token response granted scope (Graph answers
// with or without the resource prefix, in any case).
func hasScope(tok *oauth2.Token, scope string) bool {
	granted, _ := tok.Extra("scope").(string)
	for s := range strings.FieldsSeq(granted) {
		if i := strings.LastIndex(s, "/"); i >= 0 {
			s = s[i+1:]
		}
		if strings.EqualFold(s, scope) {
			return true
		}
	}
	return false
}

// SignOut forgets the sign-in on this computer (the refresh token is not
// revoked at Microsoft). The in-memory state is cleared even if the
// keychain cannot be written.
func (c *Client) SignOut() error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.clearLocked()
	if err := c.secrets.Delete(secretName); err != nil {
		return fmt.Errorf("delete microsoft sign-in: %w", err)
	}
	return nil
}

func (c *Client) clearLocked() {
	c.signed, c.refresh, c.access = false, "", ""
	c.expiry = time.Time{}
	c.account = Account{}
	c.gen++
}

func (c *Client) withHTTP(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, c.http)
}

// tokenError turns an error of the token endpoint into one without
// Microsoft's text: only the HTTP status and OAuth error code are kept.
func (c *Client) tokenError(what string, err error) error {
	var re *oauth2.RetrieveError
	if !errors.As(err, &re) {
		return fmt.Errorf("%s: request failed", what)
	}
	status := 0
	if re.Response != nil {
		status = re.Response.StatusCode
	}
	if isConsentText(string(re.Body)) {
		return fmt.Errorf("%s: %w", what, ErrConsent)
	}
	return fmt.Errorf("%s: HTTP %d %s", what, status, safeCode(re.ErrorCode))
}

// SignInError maps the error and error_description parameters of the
// callback to ErrDenied, ErrConsent or a generic error.
func SignInError(code, description string) error {
	switch {
	case isConsentText(description):
		return ErrConsent
	case code == "access_denied":
		return ErrDenied
	default:
		return fmt.Errorf("sign-in refused: %s", safeCode(code))
	}
}

// isConsentText detects "consent required" (AADSTS65001) and "admin
// approval required" (AADSTS90094, AADSTS90095).
func isConsentText(s string) bool {
	return strings.Contains(s, "AADSTS65001") || strings.Contains(s, "AADSTS90094") || strings.Contains(s, "AADSTS90095")
}

// safeCode keeps an OAuth error code only if it looks like one, so a
// crafted callback cannot put arbitrary text in an error or a log.
func safeCode(code string) string {
	if code == "" || len(code) > 64 {
		return "unknown"
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && r != '_' {
			return "unknown"
		}
	}
	return code
}

// accessToken returns a valid access token, refreshing it when it expires
// within refreshMargin. stale is an access token that Graph just refused:
// it forces a refresh unless another call already replaced it.
func (c *Client) accessToken(ctx context.Context, stale string) (string, error) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()

	c.stateMu.RLock()
	signed, refresh, access, expiry, gen := c.signed, c.refresh, c.access, c.expiry, c.gen
	c.stateMu.RUnlock()
	if !signed {
		return "", fmt.Errorf("%w: not signed in", mailer.ErrAccount)
	}
	if access != "" && access != stale && c.now().Add(refreshMargin).Before(expiry) {
		return access, nil
	}

	tok, err := c.oauth.TokenSource(c.withHTTP(ctx), &oauth2.Token{RefreshToken: refresh}).Token()
	if err != nil {
		return "", c.refreshFailed(gen, err)
	}

	// The keychain write below stays under the write lock on purpose: a
	// sign-out can then never be undone by a late refresh. The cost is
	// latency for Account() while the (local) keychain is written.
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.gen != gen { // signed out (or in again) during the refresh
		return "", fmt.Errorf("%w: sign-in changed", mailer.ErrAccount)
	}
	c.access, c.expiry = tok.AccessToken, tok.Expiry
	if tok.RefreshToken != "" && tok.RefreshToken != c.refresh {
		// Entra rotates refresh tokens: keep the new one at once.
		c.refresh = tok.RefreshToken
		raw, err := json.Marshal(storedSignIn{RefreshToken: c.refresh, Name: c.account.Name, Address: c.account.Address})
		if err == nil {
			err = c.secrets.Save(secretName, string(raw))
		}
		if err != nil {
			// The old token stays valid until its own expiry.
			slog.Warn("microsoft: cannot keep the new refresh token in the keychain")
		}
	}
	return c.access, nil
}

// refreshFailed handles a refused refresh. A definitive refusal (HTTP 4xx
// other than 408 and 429: invalid_grant, interaction_required, ...) deletes
// the sign-in, which cannot be used again; anything else keeps it.
func (c *Client) refreshFailed(gen uint64, err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) && re.Response != nil {
		st := re.Response.StatusCode
		slog.Warn("microsoft token refresh refused", "status", st, "code", safeCode(re.ErrorCode))
		if st >= 400 && st < 500 && st != http.StatusRequestTimeout && st != http.StatusTooManyRequests {
			c.stateMu.Lock()
			if c.gen == gen {
				c.clearLocked()
				if derr := c.secrets.Delete(secretName); derr != nil {
					slog.Warn("microsoft: cannot delete the sign-in from the keychain")
				}
			}
			c.stateMu.Unlock()
		}
		return fmt.Errorf("%w: token refresh refused (HTTP %d)", mailer.ErrAccount, st)
	}
	slog.Warn("microsoft token endpoint unreachable")
	return fmt.Errorf("%w: token endpoint unreachable", mailer.ErrAccount)
}

// graphError is the error of a Graph response.
type graphError struct {
	Status        int
	Code, Message string
}

func (e *graphError) Error() string {
	switch {
	case e.Code != "" && e.Message != "":
		return fmt.Sprintf("graph: HTTP %d %s: %s", e.Status, e.Code, e.Message)
	case e.Code != "":
		return fmt.Sprintf("graph: HTTP %d %s", e.Status, e.Code)
	default:
		return fmt.Sprintf("graph: HTTP %d %s", e.Status, http.StatusText(e.Status))
	}
}

func newGraphError(status int, body []byte) *graphError {
	ge := &graphError{Status: status}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil {
		ge.Code, ge.Message = env.Error.Code, env.Error.Message
	}
	return ge
}

type (
	sendMailRequest struct {
		Message         sendMailMessage `json:"message"`
		SaveToSentItems bool            `json:"saveToSentItems"`
	}
	sendMailMessage struct {
		Subject      string      `json:"subject"`
		Body         itemBody    `json:"body"`
		ToRecipients []recipient `json:"toRecipients"`
	}
	itemBody struct {
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	}
	recipient struct {
		EmailAddress emailAddress `json:"emailAddress"`
	}
	emailAddress struct {
		Name    string `json:"name,omitempty"`
		Address string `json:"address"`
	}
)

// Send implements mailer.Sender with Graph sendMail (see SPEC §5.1 for
// the error mapping). It never retries a request that may have been
// processed, except after a 401 that was refused before any processing.
func (c *Client) Send(ctx context.Context, m mailer.Message) error {
	payload, err := json.Marshal(sendMailRequest{
		Message: sendMailMessage{
			Subject:      m.Subject,
			Body:         itemBody{ContentType: "Text", Content: m.Body},
			ToRecipients: []recipient{{EmailAddress: emailAddress{Name: m.To.Name, Address: m.To.Address}}},
		},
		SaveToSentItems: true,
	})
	if err != nil {
		return fmt.Errorf("graph: build request: %w", err)
	}

	access, err := c.accessToken(ctx, "")
	if err != nil {
		return err
	}
	res, err := c.post(ctx, access, payload)
	if err == nil && res.status == http.StatusUnauthorized {
		access, err = c.accessToken(ctx, access)
		if err != nil {
			return err
		}
		res, err = c.post(ctx, access, payload)
	}
	if err != nil {
		return err
	}
	return c.mapResult(res)
}

type graphResult struct {
	status     int
	body       []byte
	retryAfter time.Duration
}

// post sends the request. A transport error is already mapped: before the
// request was written the email surely did not leave (ErrAccount), after
// it we cannot know (ErrUncertain).
func (c *Client) post(ctx context.Context, access string, payload []byte) (graphResult, error) {
	var wrote atomic.Bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest: func(i httptrace.WroteRequestInfo) {
			if i.Err == nil {
				wrote.Store(true)
			}
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.graphURL+"/me/sendMail", bytes.NewReader(payload))
	if err != nil {
		return graphResult{}, fmt.Errorf("graph: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// The transport's error text holds the URL without query: no secret.
		if !wrote.Load() {
			return graphResult{}, fmt.Errorf("%w: request not sent: %w", mailer.ErrAccount, err)
		}
		return graphResult{}, fmt.Errorf("%w: no answer: %w", mailer.ErrUncertain, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil && resp.StatusCode/100 == 2 {
		// Accepted, then the connection broke: the email is on its way.
		body = nil
	} else if err != nil {
		return graphResult{}, fmt.Errorf("%w: answer cut: %w", mailer.ErrUncertain, err)
	}
	return graphResult{
		status:     resp.StatusCode,
		body:       body,
		retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), c.now()),
	}, nil
}

// mapResult applies the error table of SPEC §5.1 to a Graph response.
func (c *Client) mapResult(r graphResult) error {
	switch {
	case r.status/100 == 2:
		return nil
	case r.status == http.StatusTooManyRequests:
		return &mailer.RateLimitError{RetryAfter: r.retryAfter, Err: newGraphError(r.status, r.body)}
	case r.status == http.StatusUnauthorized, r.status == http.StatusForbidden:
		// 401 here means the refreshed token was refused too.
		return fmt.Errorf("%w: %w", mailer.ErrAccount, newGraphError(r.status, r.body))
	case r.status >= 500:
		// 500, 502, 503, 504, ...: sendMail is not idempotent, so the email
		// may have been processed and must not be retried automatically.
		return fmt.Errorf("%w: %w", mailer.ErrUncertain, newGraphError(r.status, r.body))
	default:
		return newGraphError(r.status, r.body)
	}
}

// parseRetryAfter reads a Retry-After header (seconds or HTTP date).
// Invalid or past values give 0: the queue's default wait applies.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 {
			return 0
		}
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0)
	}
	return 0
}

// fetchAccount reads the name and address of the signed-in account.
func (c *Client) fetchAccount(ctx context.Context, access string) (Account, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.graphURL+"/me?$select=displayName,mail,userPrincipalName", nil)
	if err != nil {
		return Account{}, err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := c.http.Do(req)
	if err != nil {
		return Account{}, errors.New("read account: request failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil || resp.StatusCode != http.StatusOK {
		return Account{}, fmt.Errorf("read account: HTTP %d", resp.StatusCode)
	}
	var me struct {
		DisplayName       string `json:"displayName"`
		Mail              string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"`
	}
	if err := json.Unmarshal(body, &me); err != nil {
		return Account{}, errors.New("read account: invalid answer")
	}
	addr := me.Mail
	if addr == "" {
		addr = me.UserPrincipalName
	}
	if addr == "" {
		return Account{}, errors.New("read account: no address")
	}
	name := me.DisplayName
	if name == "" {
		name = addr
	}
	return Account{Name: name, Address: addr}, nil
}
