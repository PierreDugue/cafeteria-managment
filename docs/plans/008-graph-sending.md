# 008 — Microsoft 365 sending via Microsoft Graph

Status: **approved (2026-11-17), on hold — not to be implemented until IT
approves the app registration (SPEC §5.4 option 4)** · SPEC §5.1 (and
§4.4, §4.6, §5.3, §7, §8, §9, §11 milestone 6)

Order: after plan 007 (Tolérance, migration 008). **This plan adds no
migration** (see decision 11), so it does not compete for a number; if a
migration turns out to be needed during implementation, it is the next free
number at that time (009 or later).

**On hold**: the app cannot be registered in the organization's Entra ID
for now (SPEC §5.4). The design is approved so it is ready on the day IT
registers the app (answer 8); nothing is built before. When it is built, a
release without the IDs still works exactly as today: test mode only, and
the Settings page says Microsoft 365 is not available in this version
(decision 2). Before starting, re-check the library versions (decision 3,
`go.mod` changes) and the Microsoft redirect URI rules (decision 1), and
give IT the checklist at the end of this plan.

## User answers (2026-11-17)

1. Sign-in: authorization code + PKCE with the
   `http://localhost/auth/microsoft/callback` redirect (decision 1).
2. Client ID / tenant ID built into the release binary, with dev flags
   (decision 2).
3. A "Mode test / Microsoft 365" switch in Settings, default test, never
   changed by sign-in or sign-out (decision 7).
4. Emails are sent from the user's own mailbox (no shared mailbox).
5. "No connection" stops the batch like an account problem (resumed by
   hand); "no answer after sending" marks the email "état inconnu", never
   retried automatically (decision 5).
6. The test email goes only to the connected account's own address and is
   not recorded in the history (decision 9).
7. Sign-out is local only (decision 9).
8. **Do not build it now**: the plan waits for the app registration.

## Goal

The user gets real sending through their Microsoft 365 work account (SPEC
§5.1), from the Settings page (§4.6):

- **"Se connecter avec Microsoft"**: the usual Microsoft sign-in page, then
  back to the app, which shows the connected account (the sender: no sender
  name or address to set).
- **"Envoyer un email de test"**: one real email to the connected account's
  own address.
- **"Se déconnecter"**.
- A **sending mode**: "Mode test" (today's behaviour, still the default) or
  "Microsoft 365" (real sending). Test mode stays available at any time.

Batches confirmed in Microsoft 365 mode go out through Graph `sendMail`, one
at a time, with the existing queue, stop / resume / retry and history (plan
005). Sent emails land in the user's Outlook "Éléments envoyés".

## Decisions

1. **Sign-in: OAuth2 authorization code + PKCE, with the app's own local
   server as the loopback redirect.** Device code flow rejected.

   | | Authorization code + PKCE (loopback) | Device code |
   |---|---|---|
   | User experience | one click; reuses the browser's Microsoft session (often no password at all); MFA as usual | open microsoft.com/devicelogin, type a code shown by the app, then sign in |
   | Organization policy | the standard flow for desktop apps (what MSAL does) | often **blocked** by Conditional Access ("authentication flows" policy); Microsoft recommends blocking it because it is a known phishing vector, so IT is likely to refuse it |
   | App registration | platform "Mobile and desktop applications" + one redirect URI | "Allow public client flows" = Yes, no redirect URI |
   | Code in the app | a callback route, state + PKCE check | a polling loop, a page that shows the code and waits |
   | Future Wails window (§9.1) | needs the system browser and a loopback listener then (out of scope) | works anywhere |

   - **Redirect URI**: `http://localhost/auth/microsoft/callback`, served by
     the app's existing server. Entra **ignores the port of `localhost`
     redirect URIs** (RFC 8252 §7.3), so `-port` can change without a new
     registration, and the URI can be added in the Azure portal as is.
     `http://127.0.0.1/...` was rejected: the portal refuses it (manifest
     edit only) and Microsoft documents the port exception for `localhost`
     only. Path is case-sensitive: IT must copy it exactly.
   - The request goes out with `redirect_uri=http://localhost:<port>/auth/microsoft/callback`,
     `<port>` taken from the `Host` of the sign-in request (already checked
     as loopback by `localhostOnly`). The callback's `Host` is
     `localhost:<port>`, which `localhostOnly` accepts. It is a top-level
     `GET` from Microsoft (`response_mode=query`, the default for code), so
     `http.CrossOriginProtection` lets it through; `form_post` is not used
     (it would be a cross-site POST, blocked).
   - **state** (`crypto/rand.Text()`) protects the callback against login
     CSRF (someone else's code connecting the wrong account); **PKCE S256**
     (`oauth2.GenerateVerifier`, `S256ChallengeOption`, `VerifierOption`)
     protects the code. Pending sign-ins live in memory only: one-time,
     10-minute TTL, at most 5 (oldest dropped). Each keeps the verifier and
     the return host (`127.0.0.1:8080` as seen on the sign-in POST), so the
     callback sends the user back to the origin they came from:
     `http://<return host>/settings?...`. No cookie needed.
   - `prompt=select_account`, so the user picks the work account if the
     browser knows several.
   - The sign-in page opens **in the same tab** (303 redirect, no popup, no
     new tab: §9.1).
   - Rejected: MSAL for Go (`AcquireTokenInteractive` opens a new browser
     tab with its own listener, and its persistent cache needs an extension
     package that uses CGO on macOS; `x/oauth2` is what SPEC §9 lists and is
     enough); Windows WAM broker (CGO/COM, Windows only).

2. **Client ID and tenant ID: set at build time, with command-line flags to
   override.** No field in Settings.
   - `-ldflags "-X main.msClientID=… -X main.msTenantID=…"` (Makefile
     variables `MS_CLIENT_ID`, `MS_TENANT_ID`; the release workflow reads
     GitHub repository *variables*: these IDs are public identifiers of a
     public client, not secrets). Flags `-ms-client-id`, `-ms-tenant-id`
     default to the built-in values, so a developer can point a dev build at
     a test tenant.
   - Both empty (today's builds) → Microsoft 365 is **not configured**: no
     Graph sender, the Settings page shows test mode and "not available in
     this version". One set without the other → startup error (clear
     message), never a half-configured app.
   - Authority: `microsoft.AzureADEndpoint(tenantID)` (single-tenant app,
     registered in the city's tenant). `common` / `organizations` are not
     used: only accounts of that tenant can sign in.
   - Why not a Settings field: a non-technical user would have to type two
     GUIDs given by IT (typos → cryptic AADSTS errors), for values that
     change only if the registration is recreated. Rejected also: a config
     file next to `app.db` (one more thing to install and back up).

3. **Tokens: refresh token in the OS keychain only, access token in memory
   only.**
   - Library `github.com/zalando/go-keyring` v0.2.8 (SPEC §9): pure Go on
     every target — macOS through `/usr/bin/security` (password written on
     its stdin, not on the command line), Windows Credential Manager through
     `github.com/danieljoos/wincred` (`x/sys/windows` syscalls). **Checked**:
     a program importing it and `golang.org/x/oauth2` v0.38.0 builds with
     `CGO_ENABLED=0` for windows/amd64, darwin/arm64 and darwin/amd64 (the
     release workflow builds on Linux, also fine). Rejected:
     `99designs/keyring` (CGO on macOS).
   - **Size limits**: Windows refuses a secret over **2,560 bytes**; macOS
     (go-keyring) refuses a command over 4,096 bytes after base64 (≈ 3 KB
     of secret). Entra refresh tokens are opaque, their size is not
     documented and has grown before. So the new package `internal/keychain`
     **splits a secret into chunks of at most 1,500 bytes** (entries
     `<name>.1`, `<name>.2`, …) plus a header entry `<name>` holding the
     chunk count and a SHA-256 of the whole secret. Write: chunks first,
     then the header, then delete leftover chunks of a longer old value.
     Read: header, chunks, hash check; a missing chunk or a hash mismatch
     (e.g. crash mid-write) reads as "not found" (= signed out), never as a
     wrong token. At most 32 chunks (48 KB).
   - Service name `CafeteriaManagement`; one secret `microsoft` holding JSON
     `{"refresh_token": …, "name": …, "address": …}`: the account shown in
     Settings lives with its token, so a database copied to another
     computer never claims a connected account (§3 backup = `app.db` only;
     after a restore on a new computer the user signs in again).
   - **Refresh**: before each email, if the access token expires within
     5 minutes, it is refreshed with the refresh token
     (`oauth2.Config.TokenSource(...).Token()`, under the client's mutex so
     two refreshes never race). Entra **rotates** refresh tokens: a new one
     is saved to the keychain at once; if that save fails it is logged
     (no token in the log) and kept in memory, the old one stays valid
     until its own expiry.
   - Refresh refused (`*oauth2.RetrieveError`, HTTP 4xx: `invalid_grant`,
     `interaction_required`, `consent_required`, …: expired after inactivity,
     revoked, password changed, new MFA / Conditional Access requirement) →
     the stored sign-in is deleted (it cannot be used again) and the email
     gets `ErrAccount`: the batch stops, nothing is marked failed (§4.4),
     Settings shows "not connected". Token endpoint unreachable or 5xx →
     `ErrAccount` too, but the sign-in is kept.
   - **Startup**: the keychain is read once (`Load`). An unreadable keychain
     is logged and the app starts "not connected"; it never prevents the app
     from starting.

4. **Permissions (delegated)**: `https://graph.microsoft.com/Mail.Send`,
   `https://graph.microsoft.com/User.Read` (for `GET /me`: name and address
   of the account), `offline_access` (refresh token). Nothing else: no
   read access to the mailbox. After the code exchange the granted `scope`
   of the token response must contain `Mail.Send`; otherwise the sign-in is
   refused with the "consent" message (decision 9) and nothing is saved.

5. **The Graph sender** (`internal/microsoft`, type `Client`, implements
   `mailer.Sender`):
   - `POST https://graph.microsoft.com/v1.0/me/sendMail`, JSON built with
     `encoding/json` structs (never by string concatenation):
     `{"message": {"subject": …, "body": {"contentType": "Text", "content": …},
     "toRecipients": [{"emailAddress": {"name": To.Name, "address": To.Address}}]},
     "saveToSentItems": true}`. Success = **202 Accepted**.
   - One recipient per call (one email per parent, §4.4); the sender is
     `/me`, so `Message` keeps no `From` (§5.3).
   - Dedicated `http.Client` (no cookie jar), response bodies read up to
     64 KiB. The base URLs (token, authorize, Graph) are fields of the
     config so tests point them at `httptest` servers.
   - **Error mapping**:

     | Graph / network result | Returned | Queue effect (existing, plan 005) |
     |---|---|---|
     | 202 | `nil` | sent |
     | 429 | `*mailer.RateLimitError` (`Retry-After` in seconds or HTTP date) | back to pending, wait, retry (≤ 5 claims) |
     | 503 | `*mailer.RateLimitError` (`Retry-After` or default) | same (the service did not take the request) |
     | 401 | refresh the token and retry **once**; still 401 → `ErrAccount` | batch stopped `account` |
     | 403 (`ErrorAccessDenied`, sending blocked, `Mail.Send` withdrawn) | `ErrAccount` | batch stopped `account` |
     | not signed in / refresh refused | `ErrAccount` | batch stopped `account` |
     | request **not sent** (DNS, connection refused, no network: detected with `httptrace.WroteRequest`) | `ErrAccount` | batch stopped `account`; resume when back online |
     | 400 (`ErrorInvalidRecipients`, …), 413, other 4xx | plain error `graph: HTTP 400 ErrorInvalidRecipients: <message>` | this email `failed` (`send`), queue goes on |
     | 500, 502, 504, timeout or connection lost **after** the request was sent | `mailer.ErrUncertain` (new, wrapped with the detail) | this email `failed` with code `unknown` ("maybe sent: check Sent Items") |

   - Why `ErrUncertain`: `sendMail` is not idempotent. Retrying a request
     that may have been processed is how a parent gets the same email twice,
     which §4.4 forbids. The existing `unknown` code already means "maybe
     delivered"; its text is generalized (i18n below). No new code, no
     migration (`error_code` has no CHECK constraint, but `unknown` fits).
   - Why "not sent" → `ErrAccount`: the email surely did not leave, and a
     network outage would otherwise fail every remaining email one by one.
     Stopping the batch (nothing failed, "Reprendre l'envoi" later) is what
     §4.4 asks for an account that cannot send. The `stopped_account` text
     is broadened to cover "no connection" (i18n below), so no new stop
     reason and no table rebuild.
   - **Invalid recipient**: Graph only rejects malformed addresses (400).
     A well-formed address with no mailbox is accepted (202) and the
     non-delivery report arrives later in the user's Outlook inbox; the app
     cannot see it (reading the inbox is out of scope, §12). Stated in the
     SPEC (§5.1), not in the UI.
   - **Interval**: 2 s between two Graph emails (≈ 30 per minute, the
     Exchange Online limit, §5.1), plus the existing 429 backoff.

6. **The queue sends each batch with the provider it was confirmed with.**
   - Today the queue has one sender. Once two exist, a test batch stopped
     and resumed in Microsoft 365 mode would **really send** emails the user
     confirmed as a test, and a Graph batch resumed in test mode would be
     "sent" without leaving. Both are unacceptable.
   - `mailer.Options` gets `Providers map[string]Provider`
     (`Provider{Sender, Interval}`), keyed by the name stored in
     `batches.provider` (`test`, `graph`). The claimed message carries its
     batch's provider (`store.QueuedMessage.Provider`, from the join that
     already exists). The wait after an email is that provider's interval.
   - A batch whose provider has no sender in this run (e.g. a `graph` batch
     after starting a build without the IDs) → treated like `ErrAccount`:
     the message goes back to pending and the batch stops with reason
     `account`; logged with the provider name and batch ID.
   - `Queue.Provider()` disappears (the current mode is a setting, decision
     7); `Queue.Interval()` becomes `Interval(provider string)` (0 if
     unknown), used by the confirmation page's time estimate.

7. **Sending mode is a setting; test mode stays the default.**
   - New `store.Settings.Provider` (key `provider`): `""` = test (default),
     `"graph"` = Microsoft 365. The server keeps it in memory with the
     language (same mutex, same "save first, then update memory" rule, plan
     006 decision 3). An unknown stored value counts as test.
   - Settings form: a "Mode d'envoi" radio group in the existing
     `POST /settings` form (`sending=test|graph`). `graph` is accepted only
     if Microsoft 365 is configured **and** an account is connected;
     otherwise 422 with a message, nothing saved. Without configuration the
     radio group is not shown at all.
   - Signing in **does not** switch the mode, signing out **does not**
     switch it back: the user chooses. After sign-in the notice says to pick
     "Microsoft 365" to send for real. (The test-mode banners of plan 005
     make the current mode impossible to miss.)
   - Mode `graph` with no account (signed out, or the refresh was refused)
     → the "Envoyer…" step refuses with "connectez-vous dans Paramètres ou
     choisissez le mode test" (422 on the compose page), at the confirmation
     page **and** again at the final confirmation.
   - **Mode changed between the confirmation page and the final click**:
     the confirmation form carries the provider it was shown with (hidden
     field `provider`). If it differs from the current mode at the final
     POST, no batch is created and the confirmation page is shown again for
     the current mode with a notice. So a page that said "test" can never
     create a real batch, and the other way round.
   - The confirmation page in Graph mode shows "Envoyé depuis : Nom
     <adresse>" instead of the test banner, and the time estimate uses the
     2 s interval.
   - Rejected: "connected = real, not connected = test" with no switch (the
     user could no longer try a message in test mode without signing out);
     a command-line flag (not for a non-technical user).

8. **Settings page additions** (plain forms, no JS, Wails-compatible).
   Section "Envoi des emails":
   - Not configured: test banner + "L'envoi par Microsoft 365 n'est pas
     disponible dans cette version (application pas encore enregistrée
     auprès de Microsoft)." (replaces `settings.sending.later`).
   - Configured, not connected: "Aucun compte Microsoft 365 connecté." and
     a form button **"Se connecter avec Microsoft"** (`POST
     /settings/microsoft/signin`), with a help line ("vous allez passer par
     la page de connexion Microsoft, puis revenir ici").
   - Connected: "Connecté : Nom (adresse)", buttons **"Envoyer un email de
     test"** (`POST /settings/microsoft/test`) and **"Se déconnecter"**
     (`POST /settings/microsoft/signout`), each its own small form.
   - Mode radio group (decision 7), inside the main settings form, with the
     test banner when the mode is test.
   - Notices after redirect (codes only in the URL, never names or
     addresses): `?connected=1`, `?signedout=1`, `?tested=1`,
     `?signin=denied|consent|expired|failed`. The `?tested=1` notice names
     the address from the current account, not from the URL.

9. **Routes and handlers** (`internal/server/microsoft.go`, new):
   - `POST /settings/microsoft/signin` → `BeginSignIn(returnHost)` → 303 to
     the Microsoft authorize URL (fixed authority + query built by
     `x/oauth2`: no open redirect). 404 if not configured.
   - `GET /auth/microsoft/callback` → `FinishSignIn(ctx, state, code)` with
     a 30 s timeout: code exchange, scope check, `GET /me?$select=displayName,mail,userPrincipalName`
     (address = `mail`, else `userPrincipalName`), keychain save, then 303
     to `http://<return host>/settings?connected=1`.
     - Unknown / used / expired `state` → 303 to `/settings?signin=expired`
       (relative: the return host is unknown).
     - `error=access_denied` → `signin=denied`. `error_description`
       containing `AADSTS65001` (consent required) or `AADSTS90094` /
       `AADSTS90095` (admin approval required), or `Mail.Send` missing
       from the granted scopes → `signin=consent`. Anything else →
       `signin=failed`.
     - A new sign-in replaces the stored one (another account included).
   - `POST /settings/microsoft/signout` → delete the keychain secret and
     the in-memory token → 303 `/settings?signedout=1`. Local only: the
     refresh token is not revoked at Microsoft (Graph only offers
     `revokeSignInSessions`, which signs the user out **everywhere**).
     A Graph batch sending at that moment stops on its next email
     (`ErrAccount`), which is also a way to stop it.
   - `POST /settings/microsoft/test` → `Send` directly (not through the
     queue, not in the history: it is not a batch) to the connected
     account's own address, subject and body from the locale files, 30 s
     timeout. Success → 303 `/settings?tested=1`. Failure → the settings
     page with status 502 and the error class (account / rate limited /
     uncertain / refused) plus the Graph error text (HTML-escaped; it is the
     user's own data). Not connected → 409.
   - All POSTs go through the existing CSRF and localhost middleware.

10. **Privacy and logs** (SPEC §8).
    - Never logged: tokens, the authorization code, state, verifier, the
      account's name or address, Graph or token-endpoint error texts, URLs
      with their query (the existing `serverError` logs `r.URL.Path` only;
      keep it so, the callback query holds the code).
    - Logged: events and codes only — `slog.Info("microsoft sign-in",
      "result", "ok"|"denied"|…)`, the OAuth `error` code and the AADSTS
      number, HTTP status codes, batch / message IDs.
    - The code in the callback URL stays in the browser history: it is
      one-time and bound to the PKCE verifier, so useless afterwards.
    - Nothing about the account goes in the database; the keychain holds the
      refresh token and the account's name and address.

11. **No migration.** `settings` is key/value (plan 006); `batches.provider`
    already takes `graph` (comment of migration 006); the provider of a
    claimed message comes from the existing join; `error_code` and
    `stop_reason` keep their values (`unknown`, `account`). Plan 007's
    migration 008 is not affected.

12. **Package layout.**
    - `internal/keychain` (new): chunked secrets on top of go-keyring, no
      knowledge of Microsoft. Backend behind a small interface so tests use
      a map (go-keyring's own `MockInit` is package-level state: not used).
    - `internal/microsoft` (new): OAuth2 sign-in, token refresh, `GET /me`,
      Graph `sendMail`, error mapping. Imports `mailer` (Message, errors),
      never `server` or `store`. Defines the keychain interface it needs.
    - `internal/mailer`: providers map, `ErrUncertain`, `ProviderGraph`.
      Still no HTTP.
    - `internal/server`: routes and pages; uses a small `microsoftAccount`
      interface it defines (fakes in tests).
    - `cmd/cafeteria`: IDs, flags, wiring only.

## Changes

### `go.mod`

- Add `github.com/zalando/go-keyring v0.2.8` (brings
  `github.com/danieljoos/wincred`, `github.com/godbus/dbus/v5` on Linux
  only), `golang.org/x/oauth2 v0.38.0` (with its `microsoft` endpoint
  package). Both pure Go (decision 3).

### `internal/keychain` (new)

```go
// Backend is the OS keychain (satisfied by OS, which wraps go-keyring).
type Backend interface {
    Get(service, user string) (string, error) // ErrNotFound if absent
    Set(service, user, secret string) error
    Delete(service, user string) error // nil if absent
}

// OS is the real keychain: macOS Keychain, Windows Credential Manager.
type OS struct{}

// ErrNotFound means no complete, intact secret is stored under the name.
var ErrNotFound = errors.New("secret not found")

// Keychain stores secrets of any size (up to 48 KB) under one service,
// split into chunks that every OS accepts (decision 3).
type Keychain struct { /* service, backend */ }

func New(service string, b Backend) *Keychain
func (k *Keychain) Load(name string) (string, error)   // ErrNotFound if missing or corrupted
func (k *Keychain) Save(name, secret string) error     // empty secret refused
func (k *Keychain) Delete(name string) error           // header and every chunk
```

- Constants: chunk size 1,500 bytes, at most 32 chunks. Header value
  `v1:<count>:<sha256 hex>`. Chunks split on byte boundaries (the secret is
  JSON/ASCII; go-keyring base64-encodes on macOS anyway).
- No context parameter: the backend calls are local and blocking, with no
  cancellation (go-keyring has none). Errors wrap the entry name, never
  the secret.

### `internal/mailer`

- `mailer.go`:
  - `const ProviderGraph = "graph"`.
  - `var ErrUncertain = errors.New("email may have been sent")`: the
    provider may have processed the email; it must not be retried
    automatically.
  - `type Provider struct { Sender Sender; Interval time.Duration }`.
- `queue.go`:
  - `Options{Providers map[string]Provider; SendTimeout time.Duration}`
    (fields `Provider`, `Interval` removed).
  - `NewQueue(st Store, o Options)` (the single `Sender` argument goes away:
    senders are in `Providers`).
  - `func (q *Queue) Interval(provider string) time.Duration`; `Provider()`
    removed.
  - `step`: pick `q.opts.Providers[msg.Provider]`; missing → handled as
    `ErrAccount` (decision 6), with `slog.Warn("no sender for provider",
    "provider", msg.Provider, "batch", msg.BatchID)`.
  - `record`: new case `errors.Is(sendErr, ErrUncertain)` → `MarkFailed`
    with `store.ErrCodeUnknown` (after the rate-limit and account cases,
    before the default). Wait = that provider's interval.

### `internal/store`

- `QueuedMessage.Provider string`, filled by `ClaimNextMessage` from
  `b.provider` (same query, one more column).
- `Settings.Provider string` (key `provider`, `""` = test); saved by
  `SaveSettings` with the language (same upsert loop). Doc comment: not
  validated here; never secrets.

### `internal/microsoft` (new)

```go
// Config identifies the app registration (decision 2). Zero URLs mean
// Microsoft's real endpoints; tests set them.
type Config struct {
    ClientID, TenantID string
    AuthURL, TokenURL, GraphURL string
}

// Account is the connected Microsoft 365 account: the sender of every email.
type Account struct {
    Name    string // displayName
    Address string // mail, else userPrincipalName
}

// Secrets is where the sign-in is kept (satisfied by *keychain.Keychain).
type Secrets interface {
    Load(name string) (string, error)
    Save(name, secret string) error
    Delete(name string) error
}

// Client signs in to Microsoft 365 and sends emails through Graph.
// Safe for concurrent use. It implements mailer.Sender.
type Client struct { /* cfg, oauth2.Config, http.Client, secrets, mu, token, account, pending sign-ins, now func() time.Time */ }

func New(cfg Config, s Secrets) *Client
func (c *Client) Load() error                                   // reads the keychain once at start
func (c *Client) Account() (Account, bool)                      // in memory, no I/O
func (c *Client) BeginSignIn(returnHost string) (authURL string, err error)
func (c *Client) FinishSignIn(ctx context.Context, state, code string) (returnHost string, err error)
func (c *Client) SignOut() error
func (c *Client) Send(ctx context.Context, m mailer.Message) error

// Sign-in errors the server maps to notices (decision 9).
var (
    ErrSignInExpired = errors.New("sign-in request unknown or expired")
    ErrDenied        = errors.New("sign-in cancelled by the user")
    ErrConsent       = errors.New("the organization has not granted the permissions")
)

// SignInError maps the error and error_description parameters of the
// callback to ErrDenied, ErrConsent or a generic error.
func SignInError(code, description string) error
```

- The callback handler checks `error` first and calls `FinishSignIn` only
  with a code; `SignInError` keeps the AADSTS knowledge in this package.
  On an `error` callback the pending sign-in is not consumed; it simply
  expires after 10 minutes (no extra method).
- Token refresh, rotation save, refresh-refused sign-out, 401 retry once:
  decisions 3 and 5. The HTTP client for `x/oauth2` is passed with
  `context.WithValue(ctx, oauth2.HTTPClient, c.http)`.
- "Request not sent" detection: `httptrace.WithClientTrace` with
  `WroteRequest` setting a flag; error before the flag → `ErrAccount`,
  after → `ErrUncertain`.
- `Retry-After`: integer seconds or HTTP date (`http.ParseTime`), relative
  to `c.now()`; invalid → 0 (the queue's default wait applies).
- Error texts returned for plain failures: `graph: HTTP <status> <code>:
  <message>` from the Graph error JSON (`{"error":{"code","message"}}`),
  or the status line if the body is not that JSON.

### `internal/server`

- `send.go`: `queue` interface becomes `Wake()` + `Interval(provider
  string) time.Duration`. Batch provider = current setting
  (`s.provider()`). Graph mode + no account → 422 on compose with
  `send.error.not_connected` (confirmation and final step). Hidden field
  `provider` on the confirmation form; mismatch at the final POST →
  confirmation page again with `send.error.mode_changed`, no batch.
  `sendConfirmData` gets `From string` (account "Nom <adresse>" in Graph
  mode); `Test` = provider is test; `Minutes` from `Interval(provider)`.
- `server.go`: `New(ctx, version, st, q, ms microsoftAccount)`; `ms` is nil
  when not configured (goexp: pass an untyped `nil`, never a nil
  `*microsoft.Client` inside the interface). In-memory `provider` next to
  `lang`. Routes of decision 9. `microsoftAccount`:

  ```go
  type microsoftAccount interface {
      Account() (microsoft.Account, bool)
      BeginSignIn(returnHost string) (string, error)
      FinishSignIn(ctx context.Context, state, code string) (string, error)
      SignOut() error
      Send(ctx context.Context, m mailer.Message) error
  }
  ```

- `settings.go`: `settingsData` gains `Configured`, `Connected`,
  `Account microsoft.Account`, `Provider` (`test`/`graph`, checked radio);
  `handleSettingsSave` validates `sending` (decision 7) and saves it with
  the language in one `SaveSettings`.
- `microsoft.go` (new): the four handlers of decision 9.
- `history.go`: unchanged (the `Test` badge already follows
  `batch.Provider`).

### `cmd/cafeteria/main.go`

- `var msClientID, msTenantID string` (set by `-ldflags -X`); flags
  `-ms-client-id`, `-ms-tenant-id` defaulting to them; exactly one empty →
  error.
- If configured: `kc := keychain.New("CafeteriaManagement", keychain.OS{})`,
  `ms := microsoft.New(microsoft.Config{ClientID, TenantID}, kc)`,
  `ms.Load()` (error logged, not fatal); `Providers[mailer.ProviderGraph] =
  {Sender: ms, Interval: 2 * time.Second}`.
- `Providers[mailer.ProviderTest] = {Sender: mailer.TestSender{}, Interval:
  500 * time.Millisecond}` always.
- Log at start: `"microsoft 365", "configured", true|false` (no IDs needed,
  but they are not secret either).

### `Makefile`, `.github/workflows/release.yml`

- `LDFLAGS += -X main.msClientID=$(MS_CLIENT_ID) -X main.msTenantID=$(MS_TENANT_ID)`
  (empty by default). Release: `make build-all ... MS_CLIENT_ID="${{ vars.MS_CLIENT_ID }}"
  MS_TENANT_ID="${{ vars.MS_TENANT_ID }}"`; unset variables give a
  test-mode-only build, as today.

### `web/templates`

- `settings.html`: section "Envoi des emails" of decision 8 (account
  block, three small forms, mode radios in the main form).
- `send_confirm.html`: "Envoyé depuis : …" line in Graph mode; hidden
  `provider` field.
- `compose.html`: nothing new (errors already listed).

### `web/locales` (both files, same keys)

| Key | fr | en |
|---|---|---|
| `settings.sending.unavailable` | L'envoi par Microsoft 365 n'est pas disponible dans cette version (application pas encore enregistrée auprès de Microsoft). | Sending through Microsoft 365 is not available in this version (the app is not registered with Microsoft yet). |
| `settings.sending.mode.legend` | Mode d'envoi | Sending mode |
| `settings.sending.mode.test` | Mode test : aucun email n'est envoyé, ils sont seulement enregistrés dans l'historique | Test mode: no email is sent, they are only recorded in the history |
| `settings.sending.mode.graph` | Microsoft 365 : les emails partent vraiment, depuis le compte connecté | Microsoft 365: emails are really sent, from the connected account |
| `settings.sending.error.needs_account` | Connectez-vous d'abord à Microsoft 365 pour choisir ce mode : rien n'a été enregistré. | Sign in to Microsoft 365 first to choose this mode: nothing was saved. |
| `settings.microsoft.title` | Compte Microsoft 365 | Microsoft 365 account |
| `settings.microsoft.not_connected` | Aucun compte Microsoft 365 connecté. | No Microsoft 365 account connected. |
| `settings.microsoft.connected` | Connecté : %s (%s) | Connected: %s (%s) |
| `settings.microsoft.signin` | Se connecter avec Microsoft | Sign in with Microsoft |
| `settings.microsoft.signin_help` | Vous allez passer par la page de connexion Microsoft, puis revenir ici. | You will go through the Microsoft sign-in page, then come back here. |
| `settings.microsoft.signout` | Se déconnecter | Sign out |
| `settings.microsoft.signout_help` | Supprime la connexion enregistrée sur cet ordinateur. | Removes the sign-in saved on this computer. |
| `settings.microsoft.test` | Envoyer un email de test | Send a test email |
| `settings.microsoft.test_help` | Envoie un email à votre propre adresse. | Sends an email to your own address. |
| `settings.microsoft.test_subject` | Cafeteria Management : email de test | Cafeteria Management: test email |
| `settings.microsoft.test_body` | Cet email confirme que Cafeteria Management peut envoyer des emails depuis votre compte Microsoft 365. | This email confirms that Cafeteria Management can send emails from your Microsoft 365 account. |
| `settings.microsoft.connected_notice` | Connecté à Microsoft 365. Choisissez « Microsoft 365 » dans le mode d'envoi pour envoyer vraiment. | Connected to Microsoft 365. Choose “Microsoft 365” as the sending mode to really send. |
| `settings.microsoft.signedout_notice` | Déconnecté de Microsoft 365. | Signed out of Microsoft 365. |
| `settings.microsoft.tested_notice` | Email de test envoyé à %s. Vérifiez votre boîte de réception. | Test email sent to %s. Check your inbox. |
| `settings.microsoft.error.denied` | Connexion annulée. | Sign-in cancelled. |
| `settings.microsoft.error.consent` | Votre organisation n'a pas encore autorisé cette application à envoyer des emails. Demandez à votre service informatique d'accorder le consentement. | Your organization has not allowed this app to send emails yet. Ask your IT department to grant consent. |
| `settings.microsoft.error.expired` | La demande de connexion a expiré. Recommencez. | The sign-in request expired. Please try again. |
| `settings.microsoft.error.failed` | La connexion à Microsoft a échoué. Réessayez. | Signing in to Microsoft failed. Please try again. |
| `settings.microsoft.error.not_connected` | Aucun compte Microsoft 365 connecté. | No Microsoft 365 account connected. |
| `settings.microsoft.error.test_failed` | L'email de test n'a pas pu être envoyé (%s) : %s | The test email could not be sent (%s): %s |
| `send.from` | Envoyé depuis : %s | Sent from: %s |
| `send.error.not_connected` | Le mode d'envoi est Microsoft 365, mais aucun compte n'est connecté. Connectez-vous dans Paramètres, ou choisissez le mode test. | The sending mode is Microsoft 365, but no account is connected. Sign in in Settings, or choose test mode. |
| `send.error.mode_changed` | Le mode d'envoi a changé depuis l'affichage de cette page : vérifiez puis confirmez à nouveau. | The sending mode changed since this page was shown: check, then confirm again. |

Changed texts:

| Key | fr | en |
|---|---|---|
| `history.batch.stopped_account` | Arrêté : envoi impossible avec le compte email (non connecté, session expirée ou pas de connexion internet). Vérifiez Paramètres, puis reprenez l'envoi. | Stopped: the email account cannot send (not signed in, session expired or no internet connection). Check Settings, then resume. |
| `history.error.unknown` | État inconnu : l'email est peut-être parti (application fermée ou pas de réponse du fournisseur). Vérifiez vos éléments envoyés avant de renvoyer. | Unknown state: the email may have been sent (app closed or no answer from the provider). Check your sent items before sending again. |

Removed: `settings.sending.later`. Reused: `send.test_banner`,
`lists.save`, `history.error.*` names for the test-email error class.

### Docs (goexp does not edit them)

SPEC changes **applied by archi on 2026-11-17** (decisions recorded; status
"designed, on hold"): §4.4, §4.6, §5.1, §5.3, §5.4 option 4, §7, §8, §9
(library versions), §10, §11.
Still to do **when the plan is implemented**: the §9 project layout,
the "implemented" status lines, and `CLAUDE.md` (layout lines for the two
new packages, status line). The list of what changes:

- **§4.4**: test mode is no longer "the only sender": it is the default
  sending mode, chosen in Settings; each batch is sent with the provider it
  was confirmed with (resume / retry included); mode changed between the
  confirmation page and the final click → confirm again; Graph mode needs a
  connected account; "cannot send at all" includes "no connection".
- **§4.6**: Microsoft section as in decision 8 (sign in / out, account,
  test email, sending mode); "not available in this version" when the
  build has no IDs.
- **§5.1**: redirect URI `http://localhost/auth/microsoft/callback`
  (port ignored by Entra); single-tenant authority; `prompt=select_account`;
  scope check; refresh rotation; error mapping table (decision 5);
  `ErrUncertain`; invalid recipients only detected when malformed; client /
  tenant IDs at build time with flags; Microsoft SDK and MSAL not used.
- **§5.3**: `Provider{Sender, Interval}` map in the queue; `ErrUncertain`;
  `graphSender` is `microsoft.Client`.
- **§7**: `settings` keys: `language`, `provider` (`''` = test,
  `graph`); no migration for milestone 6.
- **§8**: keychain contents (refresh token + account name and address,
  chunked); access token in memory only; what is never logged (decision
  10); sign-out is local.
- **§9**: versions `golang.org/x/oauth2` v0.38.0, `github.com/zalando/go-keyring`
  v0.2.8; layout gains `internal/keychain`, `internal/microsoft`.
- **§10**: question 2 updated with the IT checklist (below).
- **§11**: milestone 6 "code ready, waiting for registration".
- `CLAUDE.md`: layout lines for the two new packages; status line after
  delivery.

## Test plan

No test ever talks to Microsoft: token, authorize and Graph endpoints are
`httptest` servers; the keychain is a map backend.

**`keychain`**
- Save / Load round trip for 1 byte, exactly 1,500, 1,501, 10,000 bytes,
  and non-ASCII text.
- Overwrite a 5-chunk secret with a 1-chunk one → leftover chunks deleted;
  Load gives the new value.
- Missing chunk, extra garbage chunk, changed chunk (hash mismatch), bad
  header → `ErrNotFound`.
- Empty secret and a secret over 48 KB → error, nothing written.
- Delete removes header and chunks; Delete of nothing → nil.
- Backend errors are returned (wrapped) and never contain the secret.
- No chunk handed to the backend exceeds 1,500 bytes.

**`microsoft`** (`httptest` token + Graph servers, map keychain, injected
clock)
- `BeginSignIn`: URL has `client_id`, `response_type=code`, the localhost
  `redirect_uri` with the port of the return host, the three scopes,
  `code_challenge_method=S256`, `prompt=select_account`, a fresh `state`
  each time.
- `FinishSignIn`: exchange sends the matching `code_verifier` and the same
  `redirect_uri`; `/me` called; keychain holds token + account; `Account()`
  set; returns the return host. Unknown state, reused state, state older
  than 10 minutes, 6th pending sign-in dropping the 1st →
  `ErrSignInExpired`. Granted scope without `Mail.Send` → `ErrConsent`,
  nothing saved. Token endpoint error → error, nothing saved.
- `SignInError`: `access_denied` → denied; descriptions with AADSTS65001,
  AADSTS90094 → `ErrConsent`; other → generic.
- `Send`: request body JSON exactly as decision 5 (name and address in
  fields, `contentType: Text`, `saveToSentItems: true`), bearer header;
  202 → nil.
- Error mapping, one row per line of the decision 5 table: 429 with
  `Retry-After: 7` and with an HTTP date → `RateLimitError` with the right
  duration; 503 → `RateLimitError`; 401 then 202 → nil with one refresh;
  401 twice → `ErrAccount`; 403 → `ErrAccount`; 400 with Graph error JSON
  → plain error containing the Graph code, not `ErrAccount` /
  `ErrUncertain`; 500 / 502 / 504 → `ErrUncertain`; server closed before
  the call (connection refused) → `ErrAccount`; handler that never answers
  with a short ctx deadline → `ErrUncertain`.
- Not signed in → `ErrAccount`, no HTTP call.
- Access token near expiry → refreshed before sending; rotated refresh
  token saved to the keychain; refresh refused (`invalid_grant`) →
  `ErrAccount`, keychain cleared, `Account()` false; token endpoint 500 →
  `ErrAccount`, keychain kept.
- `Load`: stored sign-in → connected; corrupted / missing → not connected,
  no error; backend error → not connected, error returned.
- `SignOut` clears keychain and memory; a following `Send` → `ErrAccount`.
- Concurrent `Send`, `SignOut`, `FinishSignIn` under `-race`; two
  concurrent `Send` with an expired token → one refresh.
- Logs (captured `slog` handler) never contain the token, code, state,
  address, name or Graph error text.

**`mailer`**
- Messages of a `test` batch go to the test sender and of a `graph` batch
  to the graph sender, both providers registered; a test batch claimed
  while the graph sender exists never reaches it.
- Batch with a provider not in the map → message back to pending, batch
  stopped `account`, nothing failed.
- `ErrUncertain` (wrapped) → failed with code `unknown`, queue goes on.
- Wait after an email = its provider's interval; `Interval("graph")`,
  `Interval("nope")` = 0.
- Existing queue tests adapted to `Options.Providers`.

**`store`**
- `ClaimNextMessage` returns the batch's provider.
- `Settings.Provider` round trip with the language; `""` default; both
  saved in one call.

**`server`** (fake `microsoftAccount`, fake queue)
- Settings page: not configured (nil) → unavailable text, no mode radios,
  no sign-in button; configured not connected → sign-in button, `graph`
  radio refused on save (422, nothing saved); connected → account shown,
  test and sign-out buttons, mode radios, current mode checked.
- Save `sending=graph` connected → stored, next confirmation is in Graph
  mode; `sending=bogus` → 422.
- `POST /settings/microsoft/signin` → 303 to the fake URL; return host =
  request `Host`; not configured → 404; cross-site POST → 403.
- `GET /auth/microsoft/callback` with `Host: localhost:8080`: success →
  303 to `http://127.0.0.1:8080/settings?connected=1`; expired state →
  `/settings?signin=expired`; `error=access_denied` → `signin=denied`;
  consent → `signin=consent`; the `code` never appears in logs.
- Sign-out → 303 `?signedout=1`; test email → 303 `?tested=1` with the
  notice naming the account address; test failure → 502 with class and
  text escaped; not connected → 409.
- Compose → confirmation in Graph mode, connected: no test banner, "Envoyé
  depuis" line, minutes from the 2 s interval, hidden `provider=graph`;
  final POST creates a batch with provider `graph`.
- Graph mode not connected → 422 at the confirmation step and at the final
  step, no batch.
- Mode switched after the confirmation page → final POST shows the
  confirmation again with `send.error.mode_changed`, no batch; same for
  graph → test.
- Restart (`New` on the same DB) keeps the mode; unknown stored mode →
  test.
- Locale key parity (existing test).

**Existing tests to update**: `fakeQueue` (`Interval(provider)`, no
`Provider()`), `server.New` helpers (new `ms` argument),
`mailer.NewQueue` calls (`Options.Providers`), settings tests for the
replaced `settings.sending.later` text.

**By hand** (once the registration exists, with fake data and the user's
own or test addresses only):
`make run` with `-ms-client-id` / `-ms-tenant-id`; sign in on macOS and
Windows (check the Windows browser reaches `localhost` when it resolves to
`::1` first); send a test email; send a 3-email batch to own addresses;
check Sent Items; stop / resume; sign out during a batch; unplug the
network during a batch; restart and check the account is still connected;
check Credential Manager / Keychain Access shows the
`CafeteriaManagement` entries; cross-compile with `make build-all`.

## Out of scope

- Sending from a shared mailbox (`Mail.Send.Shared`,
  `/users/{id}/sendMail`): the sender is the signed-in account (answer 4).
- Recording which account sent each batch (would need a migration).
- Detecting non-delivery reports (reading the inbox, §12).
- Attachments (SPEC §10.3), HTML bodies.
- The Wails window's sign-in (system browser + own loopback listener).
- Device code flow, MSAL, revoking the session at Microsoft on sign-out.
- The §5.4 workarounds (milestone 5c).
- Any change to plan 007.

## What IT must provide (hand this list to IT)

- Register an app in Microsoft Entra ID ("App registrations"), name e.g.
  "Cafeteria Management", **single tenant** (accounts in this
  organizational directory only).
- Platform **"Mobile and desktop applications"** (not Web, not SPA), redirect
  URI exactly `http://localhost/auth/microsoft/callback`. No client secret,
  no certificate. "Allow public client flows" can stay **No** (only device
  code would need it).
- Microsoft Graph **delegated** permissions: `Mail.Send`, `User.Read`,
  `offline_access`, and **grant admin consent** for the organization (or
  confirm users may consent themselves).
- Optionally: "Assignment required" with only the user assigned, so nobody
  else can use the registration.
- Send back the **Application (client) ID** and the **Directory (tenant)
  ID**.
- Confirm Conditional Access allows this sign-in from the user's computer,
  and say how often a new sign-in will be required (sign-in frequency,
  refresh token lifetime).
- Confirm the mailbox may send to **external** addresses (parents) about
  30 emails per minute, up to a few hundred per batch, and that no
  outbound policy blocks Graph `sendMail`.
