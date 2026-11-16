# 005 — Sending and history

Status: **done (2026-11-16) · reviewed: approved (5 minor points open)** · SPEC §4.4,
§4.5 (and §4.0, §4.2 "stale selection", §4.3, §5.3, §7, §8, §11 milestone 5b)

## Goal

From the compose page the user clicks "Envoyer…", sees a confirmation page
("Envoyer 12 emails ?"), confirms, and lands on the progress page of the new
**batch**, which updates by itself while the emails go out one by one in the
background (SPEC §4.4). Every email gets a status (pending, sent, failed with
its error). Failed emails can be sent again, a sending can be stopped and
resumed, and nothing is ever sent twice by accident. The history lists every
batch and shows, for each one, its recipients, the families or children
concerned, the status and the exact text sent, even after a re-import or the
deletion of the list (SPEC §4.5, §7).

This plan is milestone **5b**: the whole pipeline runs with a **test-mode
sender** that sends nothing and only records the emails in the history, so
the user can try it end to end. Microsoft 365 (Graph + OAuth, milestone 6)
and SMTP (6b) plug into the same `Sender` interface in the next plans.

## User answers (2026-11-16)

All eight recommendations were accepted:

1. This plan is milestone 5b only, with a test-mode sender; Graph + OAuth
   come in the next plan (decision 1).
2. Test mode only records the emails in the history; no `.eml` files
   (decision 1).
3. After an interrupted sending there is no automatic resume: the user
   clicks "Reprendre l'envoi"; the email cut off by a crash is marked
   "état inconnu" (decision 8).
4. An "Arrêter l'envoi" button on the progress page (decision 12).
5. A subject longer than 255 characters after placeholder replacement
   **blocks** the sending; it is never cut (decision 4).
6. Two separately confirmed sendings of the same message are both allowed,
   with no extra warning (decision 5).
7. The provider's error text is stored and shown on the batch page only,
   never logged (decision 10).
8. The progress page refreshes every 3 s with a plain meta refresh; htmx
   later (decision 11).

## Decisions

1. **Scope: milestone 5b only, with a test-mode sender** (answer 1).
   - What ships: confirmation, batch tables, background queue, progress,
     stop/resume, retry, history pages, and `mailer.TestSender`, which
     accepts every email and sends nothing. It is the only sender in 5b.
   - Why not Graph now: Graph needs an Entra app registration and maybe an
     IT admin consent (SPEC §10.2, still open), the OAuth flow, keychain
     storage and a Settings page. Each of those can block or need its own
     review. The queue, idempotency and history are the risky part and can
     be built, tested and tried by the user without any account. The
     `Sender` contract (decision 9) already covers what Graph needs
     (structured recipient, 429 / `Retry-After`, account not connected).
   - Test mode is made **impossible to miss**: a banner on the confirmation
     page ("Mode test : aucun email ne sera envoyé…"), on the progress page
     and on every test batch in the history; the status of a test email is
     "Enregistré (test)", never "Envoyé". The batch records its provider
     (`test`), so test batches stay recognizable after Graph exists.
   - Rejected: writing `.eml` files to a folder (answer 2). It needs a
     hand-written MIME encoder (RFC 2047 headers, quoted-printable) that no
     real provider uses (Graph takes JSON fields, go-mail encodes itself),
     and it copies children's data into loose files outside the database.
     The history page already shows exactly what would be sent.

2. **Confirmation step = a new `action=send` of `POST /compose`.**
   - The compose form gets a primary button "Envoyer…" (`action=send`)
     where the `compose.next_step` note is today. The hidden default
     `preview` button stays first, so Enter never opens the confirmation.
   - `action=send` runs the shared selection checks (decision 3), then
     `message.RenderAll` (decision 4). Problems → the compose page with the
     problems listed, **422**, text kept (same as preview problems, but 422
     because the action is refused). No problem → the **confirmation page**
     `send_confirm.html` (200). Nothing is written (no state change before
     the final POST).
   - The confirmation page shows: "Envoyer %d email(s) ?" (families: "… un
     par adresse"), the list name, the test-mode banner, the subject and
     body as typed, the rendered email of the first recipient as an
     example, the recipients table in `<details>` (same table as compose),
     and "Gardez l'application ouverte pendant l'envoi (environ %d
     minute(s))." The minutes are `ceil(count × queue.Interval() / 1 min)`,
     at least 1. Its form carries the hidden `list`, `version`, `child`
     IDs, `subject`, `body` and a fresh one-time `token` (decision 5).
     Buttons: "Envoyer les %d emails" (submit to `/send`) and "Modifier le
     message" (`formaction="/compose"`, no action → compose page with the
     text kept). Plain form, no JS: works in Wails.

3. **`POST /send` re-checks everything, then creates the batch in one
   transaction.** Steps:
   1. `MaxBytesReader` 1 MB, `ParseForm` (errors as compose).
   2. The **selection checks shared with compose**, factored out of
      `handleCompose` into one helper (`readSelection`, decision under
      Changes): list/IDs/version parsing, the too-many / none-selected 422,
      `SelectedChildren` (stale → 409 children page `compose.error.stale`),
      any entry without a valid email → 422 `children.error.no_email`, list
      read for its name. Compose and send use the same code (reviewer:
      no duplicated rules).
   3. `token` missing or not of the expected form (1–64 chars of
      `[A-Z2-7]`, the `rand.Text` alphabet) → 422 on the compose page with
      `send.error.no_token` ("confirm again").
   4. `message.RenderAll` once (check once, render many, decision 4);
      problems → 422 compose page with problems (only a tampered form or a
      race with nothing else changed can get here).
   5. `store.CreateBatch` (decision 6) in one transaction: token already
      used → returns the existing batch (no new one); else list version
      re-checked (`ErrStaleSelection` → 409 children page); batch, messages
      and message children inserted with status `pending`.
   6. `queue.Wake()`, then **303** to `/history/{id}?started=1` (a reload of
      the result page is a GET; no personal data in the URL). An already
      used token gives 303 to `/history/{id}` without `started`.
   - Logs: batch ID, list ID, recipient count. Never names, addresses or
     text.

4. **Check once, render many** (plan 003 note). New
   `message.RenderAll(subject, body, rs, mode, and) ([]Email, []Problem,
   error)`: `Check` once; problems → returned, no emails. Then the subject
   and body are scanned **once** and the segments substituted per
   recipient. Then the **rendered subject cap**: a rendered subject longer
   than `MaxSubjectLen` (255 runes, also Outlook's limit) is a blocking
   problem of the new kind `rendered_too_long` (field subject, with the
   address of the first recipient concerned and the number of recipients
   concerned), never a silent cut (answer 5). The length is counted in
   runes on the final subject (after `headerSafe`). Typical cause:
   `{{parent_name}}` of an address with many families. `Render` becomes a
   wrapper over the same code (or is removed if unused; goexp checks with
   gopls). The compose preview uses `RenderAll` for its one recipient, so
   it shows the same problem. `Email` gains `FamilyNumbers []string`
   (parallel to `ChildIDs`/`ChildNames`) for the history.

5. **Idempotency: a one-time token per confirmation page, unique in the
   database.**
   - The token (`crypto/rand.Text`) is generated when the confirmation page
     is rendered and posted with it. `batches.token` is `UNIQUE`. Inside the
     `CreateBatch` transaction the token is looked up **first**: if a batch
     has it, that batch is returned and nothing is inserted.
   - Covered: double-click on "Envoyer", reload of the POST, browser back
     then resubmit, a duplicated tab of the confirmation page — all post the
     same token → one batch, and the user lands on its page. The token is
     checked before the version, so a resubmit after a later import still
     shows the existing batch instead of a 409.
   - No server memory of issued tokens is needed (unlike `pendingImports`):
     a token only becomes "used" when a batch is created, and an unused one
     is harmless. Forging one only lets a local page confirm a send, which
     `CrossOriginProtection` + `localhostOnly` already block for other
     sites.
   - The button is not disabled after submit (that needs JS); the token is
     the guarantee.
   - Two confirmation pages opened separately (two clicks on "Envoyer…")
     are two confirmations and give two batches, with no extra warning
     (answer 6). The worker
     never sends a message twice by itself (decision 8).

6. **Batch persistence: migration 006** (exact SQL under Changes), close to
   SPEC §7 with what the queue and history need:
   - `batches`: `id AUTOINCREMENT` (history URLs never point to another
     batch), `token UNIQUE`, `list_id` (`ON DELETE SET NULL`) + `list_name`
     copy at sending time + `list_version`, `families` (data mode, for the
     columns of the history), `provider` (`test` now), `subject` / `body`
     as typed (normalized, with placeholders), `status`, `stop_reason`,
     `created_at`, `finished_at`.
   - `messages`: `batch_id` (cascade), `parent_id` (`ON DELETE SET NULL`),
     `to_name` + `to_email` copies, **final rendered** `subject` / `body`,
     `status`, `error_code`, `error`, `attempts`, `sent_at`, `updated_at`.
     SPEC §7's `parent_name` becomes `to_name` (in family mode it is the
     joined billing names, not the parent record's name).
   - `message_children`: `message_id` (cascade), `position`, `child_id`
     (`ON DELETE SET NULL`), `child_name` + `family_number` copies.
   - Indexes on `messages.parent_id` and `message_children.child_id`: the
     `SET NULL` of every child or parent an import deletes would otherwise
     scan the whole history.
   - History is never touched by imports, list rename or list delete
     (copies + `SET NULL`). Parent and child IDs are reused by SQLite after
     a deletion, but `SET NULL` has cleared the old link first, so a history
     row never points to another family.
   - Batch status: `sending` (the worker processes its pending messages),
     `stopped` (pending messages wait for "Reprendre"), `done` (no pending
     message left). `stop_reason`: `user`, `interrupted` (app closed or
     crashed during the sending), `account` (the sender cannot send at all;
     for Graph). `stop_reason` is `''` unless the batch is `stopped`; it is
     cleared, with `finished_at`, whenever the batch goes back to
     `sending`. Counts (sent / failed / pending) are computed from
     `messages`, never stored twice.
   - A batch is **waiting** when it is `sending` and an older batch (lower
     ID) is `sending` with a `pending` or `sending` message.
   - Message status: `pending`, `sending` (claimed by the worker, being
     sent), `sent`, `failed`. `sending` is new compared with SPEC §4.4: it is
     what lets a crash never cause a silent resend (decision 8).

7. **Background sending: one worker goroutine, the database is the queue.**
   - `mailer.Queue` (new package `internal/mailer`, SPEC §9) is built in
     `main` with the store, the sender, the provider name and the interval.
     `main` runs `q.Run(ctx)` in a goroutine with the signal context and
     **waits for it to return before closing the store**. The server only
     gets a small interface (`Wake()`, `Provider()`, `Interval()`), defined
     in `internal/server`.
   - Loop: claim the next message (`pending`, in a `sending` batch, lowest
     batch ID then message ID; set `sending`, `attempts+1`, in one short
     transaction) → send **outside any transaction** → record the result
     (sent / failed / back to pending) and set the batch `done` when nothing
     is pending or sending any more, in one transaction → wait the
     interval. Nothing to claim → wait for `Wake()` or the end of `ctx`.
     `Wake` is a non-blocking send on a channel of capacity 1. Only the
     idle wait listens to `Wake`; the interval and rate-limit waits end
     only on their timer or on `ctx`.
   - **One batch at a time**, in creation order: a batch confirmed while
     another is sending waits; its page says "En attente : un autre envoi
     est en cours". A retry of an older batch slots in after the current
     message.
   - **Rate limit**: `Interval` between two sends (SPEC §5.1: ~2 s for
     Graph). Test mode uses 500 ms so the progress page is visible without
     long waits; tests use 0. The wait is a timer in a `select` with
     `ctx.Done()`.
   - **Backoff**: a `*mailer.RateLimitError` puts the message back to
     `pending` (not sent, so safe), waits `RetryAfter` (60 s if 0 or
     negative, at most 15 min) and tries again; when the claim that got the
     rate limit was its 5th attempt (`attempts >= 5`) the message fails
     with code `rate_limited` instead. `mailer.ErrAccount` (not signed in, consent revoked)
     puts the message back to `pending` and stops the batch with reason
     `account`: nothing was sent, and the user resumes after reconnecting.
     Any other error fails the message (code `send`) and the queue goes on.
   - **Graceful stop**: the claim uses `ctx` (nothing is claimed after
     the end). Once claimed, the `Send` **and the recording of its result**
     use `context.WithTimeout(context.WithoutCancel(ctx), SendTimeout)`
     (60 s), so the email in flight when the app closes finishes and is
     recorded; then `Run` returns and the remaining messages stay
     `pending`. `main` waits for the
     HTTP shutdown (5 s, as today) and for `Run` (at most one send timeout).

8. **Restart: never resend on its own** (answer 3).
   - Before the HTTP server starts, `main` calls `q.Recover(ctx)` (store
     `RecoverInterrupted`), once: batches still `sending` become `stopped`
     with reason `interrupted`; a message still `sending` (the app was
     killed during its send: maybe delivered, maybe not) becomes `failed`
     with code `unknown`. Running it before listening means a batch created
     after the start is never mistaken for an interrupted one.
   - Rejected: resuming automatically at start. The user may have closed the
     app on purpose to stop a wrong sending, and emails would leave without
     anyone looking. The batch page shows "Interrompu (l'application a été
     fermée)" with "Reprendre l'envoi (%d en attente)"; the home page shows
     "%d envoi(s) interrompu(s)" with a link to the history.
   - The `unknown` failures are shown as "État inconnu (l'application a été
     fermée pendant l'envoi) : vérifiez vos éléments envoyés avant de
     renvoyer". They are included in "Renvoyer les emails en échec", after
     that warning: the user decides.

9. **`Sender` interface** (SPEC §5.3), in `internal/mailer`:

   ```go
   // Message is one email for a Sender. To is structured: a provider must
   // use its name and address fields (Graph emailAddress, go-mail
   // AddrFullName), never a header built by string concatenation.
   type Message struct {
       To      mail.Address // net/mail
       Subject string       // one line, at most 255 runes (checked at batch creation)
       Body    string       // plain text, "\n" line endings
   }

   // Sender sends one email. It returns nil once the provider accepted it.
   type Sender interface {
       Send(ctx context.Context, m Message) error
   }

   // RateLimitError: the provider asks to wait before the next email
   // (HTTP 429 / Retry-After). The email was not sent.
   type RateLimitError struct { RetryAfter time.Duration; Err error }

   // ErrAccount (wrapped): the account cannot send at all (not signed in,
   // token revoked). The email was not sent.
   var ErrAccount = errors.New("email account cannot send")
   ```

   - The sender address is the account's own (Graph `/me`, SMTP settings),
     so `Message` has no `From`; settings come with each provider's plan.
   - `TestSender.Send` returns nil and keeps nothing (no file, no log of the
     content). Provider name constant `ProviderTest = "test"`.
   - Graph (6) and SMTP (6b) add a type each and the wiring in `main`; the
     queue, store, history and UI do not change.

10. **Errors stored, shown and logged** (answer 7).
    - `messages.error_code` is one of `send`, `rate_limited`, `account`,
      `unknown`; it is localized in the history (`history.error.*`).
    - `messages.error` keeps the provider's text (`err.Error()`), control
      characters replaced by spaces and cut to 500 runes, for diagnosis. It
      may contain an address; it lives in the database next to the same
      address anyway, and is shown only on the batch detail page
      (HTML-escaped).
    - Logs (`slog.Warn`): batch ID, message ID, error code. **Never** the
      error text, names, addresses or content.

11. **Progress page = the batch detail page, refreshed with a meta tag**
    (answer 8).
    - `GET /history/{id}` is both. While the batch is `sending`, the page
      head gets `<meta http-equiv="refresh" content="3">` (through the
      layout's existing `scripts` block) and an "Actualiser" link. No JS, no
      htmx (not vendored yet), works in a Wails webview; a refresh is a GET
      that changes nothing. When the batch is `stopped` or `done` there is
      no refresh. Rejected for now: vendoring htmx only for polling.
    - Top of the page: status line, a `<progress value=… max=…>` with
      "%d envoyé(s) · %d en échec · %d en attente", the test banner, the
      waiting notice (decision 7), the `started` / action notices, and the
      buttons that apply: "Arrêter l'envoi" (sending), "Reprendre l'envoi"
      (stopped with pending messages), "Renvoyer les %d emails en échec"
      (any failed message).
    - Notices come from the query string after a redirect, like the
      existing `?renamed=1` pattern: `started=1`, `stopped=1`,
      `resumed=1`, `retried=N` (N re-read as an integer, ignored if bad).
    - Dates use the existing `Localizer.Date` (store times via
      `parseTime`).
    - Then: date, list name (the copy at sending time), subject and body as
      typed, and the recipients table: name, address, families or children
      concerned, status (and error), sent time, and a link to the message.
    - `GET /history/{id}/messages/{mid}` shows one message exactly as
      stored (to, subject, body in a `pre-wrap` box). Kept on its own page
      so the batch page stays small with 300 bodies of up to 20,000 chars.

12. **Stop, resume, retry** (all POST, 303 back to `/history/{id}` with a
    notice; unknown batch → 404 on the history page).
    - `POST /history/{id}/stop` (answer 4): `sending` → `stopped`
      (`user`). The email in flight finishes and is recorded (if it was the
      last one, the batch becomes `done`); the next one is not claimed. A
      batch already `stopped` or `done` is left as it is.
    - `POST /history/{id}/resume`: `stopped` → `sending` if it has pending
      messages (else nothing changes), then `Wake`.
    - `POST /history/{id}/retry`: every `failed` message of the batch →
      `pending` (same rendered content and address; `error_code`, `error`
      cleared; `attempts` reset to 0 so the rate-limit cap counts from the
      retry), batch → `sending` (from any status), then `Wake`. A stopped batch is
      therefore resumed too (the button text says so when it applies). A
      second click finds no failed message and changes nothing. Retry works
      after the list is deleted: the stored address and text are what the
      user confirmed.
    - A test batch is retried with the test sender, like any other.

13. **History list** (`GET /history`, new nav item "Historique"): one line
    per batch, newest first, the last 100 (constant; note when there are
    more): date,
    subject as typed, list name, sent / failed / pending counts, status
    (with "Test" badge), link to the detail. Empty state with a link to the
    selection. The home page shows "%d envoi(s) interrompu(s)" with a link
    when batches are `stopped` with reason `interrupted` (decision 8).

14. **Settings (§4.6): none in this plan.** The only sender is test mode,
    so there is nothing to choose. The Settings page (account, sender name,
    "send test email", test mode switch) comes with Graph (plan 006). The
    "Tolérence" line of §4.6 is out of scope.

15. **Privacy**: history keeps names, addresses and texts in the database
    only (same file as the rest, SPEC §3, §8). No personal data in URLs
    (IDs only), in logs (IDs, counts, codes) or in files.

## Changes

### `internal/store`

- **`migrations/006_batches.sql`** (new):

  ```sql
  -- Sending batches and email history (SPEC §4.4, §4.5, §7). History keeps
  -- copies of names, addresses and texts; links to lists, parents and
  -- children are cleared (SET NULL) when those are deleted, so imports and
  -- list deletion never change or erase history.
  CREATE TABLE batches (
      id           INTEGER PRIMARY KEY AUTOINCREMENT, -- never reused (history URLs)
      token        TEXT NOT NULL UNIQUE,  -- one-time token of the confirmation page
      list_id      INTEGER REFERENCES lists(id) ON DELETE SET NULL,
      list_name    TEXT NOT NULL,         -- copy at sending time
      list_version INTEGER NOT NULL,
      families     INTEGER NOT NULL,      -- 1: family entries (CSV), 0: children (Excel)
      provider     TEXT NOT NULL,         -- 'test' (5b); later 'graph', 'smtp'
      subject      TEXT NOT NULL,         -- as typed (placeholders), normalized
      body         TEXT NOT NULL,
      status       TEXT NOT NULL CHECK (status IN ('sending', 'stopped', 'done')),
      stop_reason  TEXT NOT NULL DEFAULT '' CHECK (stop_reason IN ('', 'user', 'interrupted', 'account')),
      created_at   TEXT NOT NULL,
      finished_at  TEXT NOT NULL DEFAULT ''
  );

  CREATE TABLE messages (
      id         INTEGER PRIMARY KEY,
      batch_id   INTEGER NOT NULL REFERENCES batches(id) ON DELETE CASCADE,
      parent_id  INTEGER REFERENCES parents(id) ON DELETE SET NULL,
      to_name    TEXT NOT NULL,   -- {{parent_name}} value, header-safe
      to_email   TEXT NOT NULL,
      subject    TEXT NOT NULL,   -- final rendered text
      body       TEXT NOT NULL,
      status     TEXT NOT NULL CHECK (status IN ('pending', 'sending', 'sent', 'failed')),
      error_code TEXT NOT NULL DEFAULT '',  -- '', 'send', 'rate_limited', 'account', 'unknown'
      error      TEXT NOT NULL DEFAULT '',  -- provider text, cleaned, <= 500 runes
      attempts   INTEGER NOT NULL DEFAULT 0,
      sent_at    TEXT NOT NULL DEFAULT '',
      updated_at TEXT NOT NULL
  );
  CREATE INDEX messages_batch ON messages (batch_id, status);
  CREATE INDEX messages_status ON messages (status, batch_id, id); -- next pending
  CREATE INDEX messages_parent ON messages (parent_id);            -- SET NULL on parent delete

  CREATE TABLE message_children (
      message_id    INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
      position      INTEGER NOT NULL,  -- order of the recipient's entries
      child_id      INTEGER REFERENCES children(id) ON DELETE SET NULL,
      child_name    TEXT NOT NULL,     -- child name or billing name (copy)
      family_number TEXT NOT NULL DEFAULT '',
      PRIMARY KEY (message_id, position)
  );
  CREATE INDEX message_children_child ON message_children (child_id); -- SET NULL on child delete
  ```

  goexp checks the index names against migrations 001–005.

- **`batches.go`** (new). Types (goexp may adjust names, not behavior):

  ```go
  // Batch and message statuses, stop reasons and error codes (SPEC §4.4).
  const (
      BatchSending = "sending"; BatchStopped = "stopped"; BatchDone = "done"
      StopUser = "user"; StopInterrupted = "interrupted"; StopAccount = "account"
      MsgPending = "pending"; MsgSending = "sending"; MsgSent = "sent"; MsgFailed = "failed"
      ErrCodeSend = "send"; ErrCodeRateLimited = "rate_limited"; ErrCodeAccount = "account"; ErrCodeUnknown = "unknown"
  )

  type NewBatch struct {
      Token         string
      ListID        int64
      Version       int64 // list version of the selection
      Families      bool
      Provider      string
      Subject, Body string // as typed, normalized
      Messages      []NewMessage
  }
  type NewMessage struct {
      ParentID                         int64
      ToName, ToEmail, Subject, Body   string // rendered
      Children                         []MessageChild
  }
  type MessageChild struct {
      ChildID      int64 // 0 once the entry is deleted
      Name         string
      FamilyNumber string
  }

  type Batch struct {
      ID                    int64
      ListID                int64  // 0 if the list was deleted
      ListName              string // copy at sending time
      Families              bool
      Provider              string
      Subject, Body         string
      Status, StopReason    string
      CreatedAt, FinishedAt time.Time
      Total, Sent, Failed, Pending int // Pending counts pending + sending
      Waiting               bool       // sending, but an older batch is sending first
  }
  type Message struct {
      ID, BatchID, ParentID int64
      ToName, ToEmail, Subject, Body string
      Status, ErrorCode, Error       string
      Attempts                       int
      SentAt                         time.Time
      Children                       []MessageChild
  }
  // QueuedMessage is a message claimed by the queue.
  type QueuedMessage struct {
      ID, BatchID                  int64
      ToName, ToEmail, Subject, Body string
      Attempts                     int
  }
  ```

  Methods:

  ```go
  // CreateBatch stores a batch and its messages as pending in one
  // transaction. If a batch already has b.Token it returns that batch's ID
  // with created=false and writes nothing. Otherwise the list version is
  // checked (ErrStaleSelection if the list is gone or changed) and the list
  // name copied.
  func (s *Store) CreateBatch(ctx context.Context, b NewBatch) (id int64, created bool, err error)

  func (s *Store) Batches(ctx context.Context, limit int) ([]Batch, int, error) // newest first; total count
  func (s *Store) Batch(ctx context.Context, id int64) (Batch, error)            // ErrNotFound
  func (s *Store) BatchMessages(ctx context.Context, batchID int64) ([]Message, error) // id order, with Children (body left empty)
  func (s *Store) Message(ctx context.Context, batchID, id int64) (Message, error)    // ErrNotFound (also wrong batch)
  func (s *Store) InterruptedBatches(ctx context.Context) (int, error)           // stopped + interrupted, for home

  // Queue side.
  func (s *Store) ClaimNextMessage(ctx context.Context) (QueuedMessage, bool, error)
  func (s *Store) MarkSent(ctx context.Context, id int64) error
  func (s *Store) MarkFailed(ctx context.Context, id int64, code, detail string) error
  func (s *Store) ReleaseMessage(ctx context.Context, id int64) error // sending -> pending (not sent)
  func (s *Store) StopBatch(ctx context.Context, id int64, reason string) error // ErrNotFound; only from sending
  func (s *Store) ResumeBatch(ctx context.Context, id int64) (bool, error)      // ErrNotFound; only from stopped; false if nothing pending
  func (s *Store) RetryFailed(ctx context.Context, id int64) (int, error)       // ErrNotFound; failed -> pending (attempts 0), batch -> sending if n > 0
  func (s *Store) RecoverInterrupted(ctx context.Context) error
  ```

  - `CreateBatch`: token lookup first, then `listVersion` (reuse;
    `ErrNotFound` or mismatch → `ErrStaleSelection`), list name read in the
    tx, inserts with `position` = index in `Children`. Empty `Messages` or
    empty token → programming error (plain `error`). Only `tx` inside.
  - `MarkSent` / `MarkFailed`: in one transaction, update the message
    (`sent_at`, `updated_at`, cleaned/cut detail for `MarkFailed`) and set
    the batch `done` with `finished_at` when it has no `pending` / `sending`
    message left (from `sending` or `stopped`). Only a message in `sending`
    is updated.
  - `ClaimNextMessage`: `pending` message of a `sending` batch, `ORDER BY
    batch_id, id LIMIT 1`, set `sending`, `attempts = attempts + 1`, one
    transaction (single connection: no race with the HTTP side). A
    `sending` batch with no pending message left (all failed between) is
    set `done` there too, so a batch never stays `sending` forever.
  - `RecoverInterrupted` (one transaction): messages `sending` →
    `failed` / `unknown`; then batches `sending` with a pending message →
    `stopped` / `interrupted`, and batches `sending` with nothing pending →
    `done` (their `unknown` failure shows in the history counts and the
    retry button).
  - `ResumeBatch` and `RetryFailed` clear `stop_reason` and `finished_at`
    when they set the batch `sending`.
  - Times are stored as the existing helpers do (`parseTime` on read).
  - The detail cleaning (control chars → space, 500 runes) lives in one
    unexported helper in the store, so no caller can store raw text.

### `internal/message`

- `KindRenderedTooLong Kind = "rendered_too_long"`; `Problem` gains
  `Recipient string` (first address concerned) and `Count int` (recipients
  concerned); both zero for other kinds (the struct stays comparable).
- `Email` gains `FamilyNumbers []string` (copy, parallel to `ChildIDs`).
- `RenderAll(subject, body string, rs []recipients.Recipient, m Mode, and
  string) ([]Email, []Problem, error)` (decision 4). Error only for a bad
  mode. `Render` shares the code (wrapper) or is removed.

### `internal/mailer` (new; no HTTP)

- `mailer.go`: `Message`, `Sender`, `RateLimitError` (with `Error`,
  `Unwrap`), `ErrAccount`, `TestSender`, `ProviderTest` (decision 9).
- `queue.go`:

  ```go
  // Store is what the queue needs from the database (satisfied by *store.Store).
  type Store interface {
      ClaimNextMessage(ctx context.Context) (store.QueuedMessage, bool, error)
      MarkSent(ctx context.Context, id int64) error
      MarkFailed(ctx context.Context, id int64, code, detail string) error
      ReleaseMessage(ctx context.Context, id int64) error
      StopBatch(ctx context.Context, id int64, reason string) error
      RecoverInterrupted(ctx context.Context) error
  }

  type Options struct {
      Provider    string        // recorded on new batches
      Interval    time.Duration // between two sends
      SendTimeout time.Duration // per email; 60 s if 0
  }

  func NewQueue(st Store, s Sender, o Options) *Queue
  func (q *Queue) Provider() string
  func (q *Queue) Interval() time.Duration
  func (q *Queue) Wake()                          // non-blocking
  func (q *Queue) Recover(ctx context.Context) error // once, before serving
  func (q *Queue) Run(ctx context.Context)        // until ctx ends; owner: main
  ```

  - Unexported `step(ctx) (worked bool, err error)`: claim, send, record;
    `Run` loops on it with the interval and the wake channel. `step` is
    what unit tests drive (no sleeps).
  - Rate-limit wait and retry count per decision 7 (constants
    `maxAttempts = 5`, `defaultRetryAfter = 60 * time.Second`,
    `maxRetryAfter = 15 * time.Minute`). A failure to write a result is
    logged and the loop goes on after the interval (never a busy loop); a
    `sending` message left by it is handled at the next start.
  - `To` is built as `mail.Address{Name: m.ToName, Address: m.ToEmail}`.
  - `step` reports the wait it wants (interval, or the capped
    `RetryAfter`) so tests can check it without sleeping; the timer
    function used by `Run` (`time.After`-like) is a field tests can
    replace.
  - `Interval()` returns `Options.Interval` (the server uses it for the
    "about %d minute(s)" estimate).

### `internal/server`

- **`compose.go`**:
  - Factor `readSelection(w, r) (selection, bool)` out of
    `handleCompose`: body limit, `ParseForm`, list/IDs/version, the
    existing 409/422 answers, no-email refusal, list read. `selection`
    holds `listID`, `version`, `list`, `children`, `group`, `mode`.
    `handleCompose` keeps `SaveSelection` (only compose saves it).
  - `action=send` (decision 2). `action=preview` uses `RenderAll` for one
    recipient.
  - `problemLines` handles `rendered_too_long` (limit, address, count).
- **`send.go`** (new): `handleSend` (decision 3) and the confirmation page
  data (`sendConfirmData`: compose fields needed for the hidden inputs,
  `Count`, `Families`, `ListName`, `Example *message.Email`, `Recipients`,
  `Token`, `Test bool`, `Minutes int`). `isToken(string) bool`.
- **`history.go`** (new): `GET /history`, `GET /history/{id}`,
  `GET /history/{id}/messages/{mid}`, `POST /history/{id}/stop`,
  `/resume`, `/retry` (decisions 11–13). Reuse `parsePathID`; parse
  `{mid}` with the same rule (generalize `parsePathID` to take the path
  name if needed, one helper). `MaxBytesReader` on the POSTs. Unknown or
  bad IDs → 404 with the history page and `history.error.not_found`.
- **`server.go`**: `New(version string, st *store.Store, q queue)` with
  `type queue interface { Wake(); Provider() string; Interval() time.Duration }`;
  test mode on the pages is `q.Provider() == mailer.ProviderTest` for the
  confirmation page and `batch.Provider == mailer.ProviderTest` for history;
  pages
  `send_confirm`, `history`, `history_batch`, `history_message`; routes:

  ```
  POST /send
  GET  /history
  GET  /history/{id}
  GET  /history/{id}/messages/{mid}
  POST /history/{id}/stop
  POST /history/{id}/resume
  POST /history/{id}/retry
  ```

  Active nav item `history` for history pages; `children` for the
  confirmation page.
- **`lists.go`** (`renderHome`): interrupted batches count.

### `cmd/cafeteria/main.go`

- Build `mailer.NewQueue(st, mailer.TestSender{}, mailer.Options{Provider:
  mailer.ProviderTest, Interval: 500 * time.Millisecond})`, call
  `q.Recover(ctx)` **before** `ListenAndServe`, start `go q.Run(ctx)` with a
  done channel, pass `q` to `server.New`. On shutdown: HTTP `Shutdown`,
  then wait for the queue's done channel, then the deferred `st.Close`.
  Wiring only.

### `go.mod` (plan 003 note)

- The new POST routes rely on `http.MaxBytesReader`. goexp runs gopls
  `go_vulncheck`; if GO-2026-6859 (`MaxBytesReader` header race) is still
  reported, add a `toolchain` line for the first fixed Go release (CI
  already builds with 1.27.x) and say so in the report. No other
  dependency change: `internal/mailer` uses the standard library only.

### `web/templates`

- `compose.html`: "Envoyer…" primary button (`action=send`) replacing the
  `compose.next_step` note (key removed).
- `send_confirm.html` (new), `history.html` (new), `history_batch.html`
  (new, with the meta refresh in the `scripts` block while sending),
  `history_message.html` (new).
- `home.html`: interrupted notice. `layout.html`: nav item `nav.history`.
- All values through `html/template`; no `template.HTML`.

### `web/static/style.css`

Test banner (warning colors), `progress` width, status badges
(sent/failed/pending/test), reuse `.preview`, `.preview-body`.

### `web/locales` (both files, same keys)

Remove: `compose.next_step`.

Add (goexp may add a few more following the same pattern, in both files):

| Key | fr | en |
|---|---|---|
| `nav.history` | Historique | History |
| `compose.send_button` | Envoyer… | Send… |
| `send.title` | Confirmer l'envoi | Confirm sending |
| `send.question` | Envoyer %d email(s) ? | Send %d email(s)? |
| `send.question_families` | Envoyer %d email(s), un par adresse ? | Send %d email(s), one per address? |
| `send.test_banner` | Mode test : aucun email ne sera envoyé. Les emails sont seulement enregistrés dans l'historique. | Test mode: no email will be sent. The emails are only recorded in the history. |
| `send.example` | Exemple pour %s | Example for %s |
| `send.keep_open` | Gardez l'application ouverte pendant l'envoi (environ %d minute(s)). | Keep the app open while sending (about %d minute(s)). |
| `send.confirm_button` | Envoyer les %d emails | Send the %d emails |
| `send.edit_button` | Modifier le message | Edit the message |
| `send.error.no_token` | La confirmation a expiré ou est incomplète. Cliquez à nouveau sur « Envoyer… ». | The confirmation is missing or incomplete. Click “Send…” again. |
| `problem.rendered_too_long` | Trop long une fois les champs remplacés (%d caractères au maximum) pour %d destinataire(s), par exemple %s. | Too long once placeholders are replaced (%d characters at most) for %d recipient(s), e.g. %s. |
| `history.title` | Historique des envois | Sending history |
| `history.empty` | Aucun envoi pour l'instant. | No sending yet. |
| `history.more` | Seuls les %d derniers envois sont affichés. | Only the last %d sendings are shown. |
| `history.col.date` | Date | Date |
| `history.col.subject` | Objet | Subject |
| `history.col.list` | Liste | List |
| `history.col.counts` | Envoyés / échecs / en attente | Sent / failed / pending |
| `history.col.status` | État | Status |
| `history.col.recipient` | Destinataire | Recipient |
| `history.col.sent_at` | Envoyé le | Sent at |
| `history.test_badge` | Test | Test |
| `history.batch_title` | Envoi du %s | Sending of %s |
| `history.progress` | %d envoyé(s) · %d en échec · %d en attente | %d sent · %d failed · %d pending |
| `history.waiting` | En attente : un autre envoi est en cours. | Waiting: another sending is in progress. |
| `history.refresh` | Actualiser | Refresh |
| `history.started` | Envoi lancé. | Sending started. |
| `history.batch.sending` | Envoi en cours | Sending |
| `history.batch.done` | Terminé | Finished |
| `history.batch.stopped_user` | Arrêté | Stopped |
| `history.batch.stopped_interrupted` | Interrompu (l'application a été fermée) | Interrupted (the app was closed) |
| `history.batch.stopped_account` | Arrêté : le compte email ne peut pas envoyer | Stopped: the email account cannot send |
| `history.status.pending` | En attente | Pending |
| `history.status.sending` | En cours | Sending |
| `history.status.sent` | Envoyé | Sent |
| `history.status.sent_test` | Enregistré (test) | Recorded (test) |
| `history.status.failed` | Échec | Failed |
| `history.error.send` | Refusé par le fournisseur | Refused by the provider |
| `history.error.rate_limited` | Trop d'emails : le fournisseur a demandé d'attendre | Too many emails: the provider asked to wait |
| `history.error.account` | Le compte email ne peut pas envoyer | The email account cannot send |
| `history.error.unknown` | État inconnu (l'application a été fermée pendant l'envoi) : vérifiez vos éléments envoyés avant de renvoyer | Unknown (the app was closed while sending): check your sent items before sending again |
| `history.stop` | Arrêter l'envoi | Stop sending |
| `history.resume` | Reprendre l'envoi (%d en attente) | Resume sending (%d pending) |
| `history.retry` | Renvoyer les %d email(s) en échec | Send the %d failed email(s) again |
| `history.stopped_notice` | Envoi arrêté. | Sending stopped. |
| `history.resumed_notice` | Envoi repris. | Sending resumed. |
| `history.retried_notice` | %d email(s) remis en file d'envoi. | %d email(s) queued again. |
| `history.view_message` | Voir | View |
| `history.message_title` | Email envoyé | Email sent |
| `history.back` | Retour à l'envoi | Back to the sending |
| `history.error.not_found` | Cet envoi n'existe pas. | This sending does not exist. |
| `home.interrupted` | %d envoi(s) interrompu(s) : voir l'historique. | %d interrupted sending(s): see the history. |

Labels reuse `compose.subject`, `compose.body`, `compose.col.email`,
`compose.col.children`, `compose.col.families`, `compose.preview_to`.

### Docs (already applied by archi; goexp does not edit them)

- `CLAUDE.md`: status line and layout (`internal/mailer/`).
- `docs/SPEC.md`:
  - §4.3: the 255-character subject limit also applies after placeholders
    are replaced, and blocks the sending.
  - §4.4: confirmation flow and wording; test mode (milestone 5b); one batch
    at a time in order; stop / resume; no automatic resume after a restart;
    `sending` status and the "unknown" failure; rendered subject cap 255
    (blocking); one-time confirmation token; retry rules.
  - §4.5: history list (last 100) and batch detail, message page, list name
    copied at sending time, test badge; home notice for interrupted batches.
  - §5.3: `Message` with a structured `mail.Address`; `RateLimitError`,
    `ErrAccount`; `TestSender`; `internal/mailer` owns the queue.
  - §7: `batches` (token, list_id/list_name/list_version, families,
    provider, status values, stop_reason, finished_at, AUTOINCREMENT),
    `messages` (`to_name` instead of `parent_name`, error_code, attempts,
    updated_at, status `sending`), `message_children` (position,
    family_number), the two `SET NULL` indexes.
  - §8: logs carry IDs, counts and codes only; the provider error text stays
    in the database.
  - §11: milestone 5b references plan 005 ("✅" is added after delivery,
    with the CLAUDE.md status line).

## Test plan

**`message`**
- `RenderAll`: same output as rendering each recipient alone (ChildMode and
  FamilyMode, SILVA case); Check problems → problems and no emails; bad
  mode → error; `FamilyNumbers` copied.
- Rendered subject: exactly 255 runes ok; 256 → one `rendered_too_long`
  problem with the first address and the count (2 of 3 recipients);
  counted after `headerSafe`; runes not bytes ("é").
- Existing `Render` / `Check` tests still pass.

**`store`**
- Migration 006 on a v5 DB with data (lists, entries, imports, templates):
  existing rows untouched; fresh DB at version 6; `PRAGMA
  foreign_key_check` empty.
- `CreateBatch`: batch, messages, children stored with positions and
  copies; list name copy; status pending/sending; same token twice → same
  ID, `created=false`, nothing added (also after a later import into the
  list: token wins over staleness); stale version / deleted list →
  `ErrStaleSelection`, nothing written; empty messages / token → error.
- History survives: re-import that deletes an entry and its parent →
  `child_id` / `parent_id` NULL, names and texts kept; rename list → batch
  keeps the old name; delete list → batch kept, `ListID` 0.
- A reused child ID (delete then re-import) is not linked to the old
  message.
- Queue functions: claim order (batch then message ID; stopped batches
  skipped; `attempts` incremented); `MarkSent` / `MarkFailed` set batch
  `done` with `finished_at` only when nothing is pending/sending (also for
  a stopped batch); `MarkFailed` cleans and cuts the detail (control
  chars, 501 runes); `ReleaseMessage` back to pending; only `sending`
  messages change.
- `ClaimNextMessage` on a `sending` batch whose messages are all
  failed/sent → no claim, batch set `done`.
- Stop while a message is `sending`, then `MarkSent` on it: message sent;
  batch `done` if it was the last one, else stays `stopped` and the next
  message is not claimed.
- `StopBatch` (only from sending; stopped/done untouched; reason stored),
  `ResumeBatch` (only from stopped; false with nothing pending; reason and
  `finished_at` cleared), `RetryFailed` (only failed, count returned,
  error code and text cleared, attempts reset to 0, batch sending from
  done or stopped, reason and `finished_at` cleared; second call 0 and
  nothing changes); `ErrNotFound` for unknown IDs.
- `RecoverInterrupted`: sending message → failed/unknown; sending batch
  with pending messages → stopped/interrupted; sending batch with nothing
  pending → done; stopped and done batches untouched; idempotent.
- `Batches` order, limit and total; `Batch` counts (sending counted as
  pending) and `Waiting` (true only behind an older sending batch with work
  left; false for the oldest one and for stopped batches);
  `BatchMessages` with children in position order and empty bodies;
  `Message` with a wrong batch → `ErrNotFound`; `InterruptedBatches`
  counts only stopped/interrupted.

**`mailer`** (fake sender recording calls; real temp store or a fake
`Store`; driven through `step`, no sleeps)
- Sends pending messages in order with `To` as `mail.Address{Name,
  Address}` (a name with quotes, `<`, `,` and accents passes unchanged in
  the structured field); marks them sent; batch done.
- Generic error → failed `send` with detail; the next message is still
  sent.
- `RateLimitError`: message back to pending, wait = RetryAfter (0 or
  negative → 60 s, huge → 15 min) as reported by `step`; on the 5th
  attempt → failed `rate_limited`; `errors.As` works on a wrapped one.
- After a send, the wait reported is the interval; with nothing to claim,
  no wait (idle until `Wake`).
- `ErrAccount` (wrapped): message pending, batch stopped `account`, nothing
  marked failed.
- Stop between two messages: the next one is not sent.
- Cancelled `ctx` during a send: the send still gets a live context
  (`WithoutCancel`) and its result is recorded (the store call gets a live
  context too); `Run` returns; the rest stays pending. Cancelled `ctx`
  before a claim: nothing is claimed.
- A store error while recording a result: logged, no panic, the loop goes
  on.
- `Wake` never blocks (many calls); `Run` with nothing to do returns when
  `ctx` ends (no goroutine leak; `-race`); `Run` wakes on `Wake` and sends
  a newly created batch.
- `Recover` delegates to the store. `TestSender` returns nil.
  `Provider()` and `Interval()` return the options.

**`server`** (`httptest`, fake queue counting `Wake`, or real queue with a
fake sender)
- Compose `action=send`: valid → confirmation page with count (children
  and families wording), list name, test banner, example email, hidden
  fields, a token of the `rand.Text` form (two renders → two different
  tokens) and the minutes estimate (e.g. 1 for 3 emails at 500 ms, 3 for
  300 emails at 500 ms); "Modifier le message" posts to `/compose` without
  an action; problems → 422 compose page with problems; rendered
  subject too long → 422 with the address; stale → 409; no-email entry →
  422; nothing written in any case.
- `POST /send`: creates one batch and messages, `Wake` called, 303 to
  `/history/{id}?started=1`; same token again → 303 to the same batch, one
  batch in the DB, `Wake` not required; missing/invalid token → 422; stale
  version / deleted list → 409 and no batch; tampered subject with an
  unknown placeholder → 422 and no batch; IDs of another list dropped (as
  compose); cross-site POST → 403; oversized body → 422.
- Compose and send share `readSelection` (same answers for the same bad
  inputs: table-driven over both routes).
- History list: empty state; lines with counts, status, test badge; limit
  note; nav active.
- Batch page: meta refresh present only while sending; progress counts;
  waiting notice; buttons per state; error codes localized and detail
  escaped; families vs children columns; list name copy after the list is
  deleted; FR and EN.
- Message page: exact stored subject/body, HTML-escaped; wrong batch or
  bad IDs → 404.
- Stop / resume / retry: state changes + notices + 303; `Wake` on resume
  and retry; 404 unknown; GET on these paths → 405; cross-site → 403.
- Test status wording: a sent message of a test batch shows "Enregistré
  (test)", never "Envoyé"; the test banner appears on the batch page; an
  `unknown` failure shows its warning text.
- Home: interrupted notice only when there are interrupted batches.
- Logs: no names, addresses, subjects or provider error text in any log
  line of send / history / queue.
- Locale key parity (existing test).

**`cmd/cafeteria`**: no unit test (wiring only); covered by `make run` by
hand: confirm a send, watch progress, stop, resume, close the app during a
send and restart.

**Existing tests to update**: `server.New` gains the queue argument
(every server test helper); compose tests that assert `compose.next_step`;
`message.Render` tests if `Render` is removed (move their cases to
`RenderAll`).

## Out of scope

- Real providers (Graph + OAuth, SMTP), the Settings page, sender name and
  address, "send test email", the test mode switch (plan 006 / 6b).
- The "Tolérence" line of SPEC §4.6.
- `.eml` export, attachments, HTML bodies, scheduling, deleting history.
- A warning or block for a second, separately confirmed, identical batch.
- Showing the rate-limit wait on the progress page (useful with Graph;
  plan 006).
- Daily quotas (Exchange 10,000 recipients/day, Gmail 500/day).

## Implementation notes

Files: `internal/store/migrations/006_batches.sql`, `internal/store/batches.go`,
`internal/message/message.go` (RenderAll, `KindRenderedTooLong`, `Problem.Recipient/Count`,
`Email.FamilyNumbers`; `Render` kept as a wrapper), `internal/mailer/{mailer,queue}.go`,
`internal/server/{send,history}.go`, `compose.go` (`readSelection`, `composeBase`,
`renderCompose`, `action=send`), `server.go` (`New(version, st, q)`, routes, template funcs),
`lists.go` (interrupted count), `templates.go` (`pathInt`), `cmd/cafeteria/main.go`, templates
(`send_confirm`, `history`, `history_batch`, `history_message`, compose/home/layout), CSS, both locales.

Deviations and tester notes:
- `step(ctx) (worked bool, wait time.Duration, err error)`: errors are returned, `Run` logs them once.
  After a store error the wait is 5 s (`errorWait`), never a busy loop. `Queue.after` is the timer seam.
- Send and recording use two separate `WithoutCancel` + `SendTimeout` contexts.
- `closeFinishedBatches` is shared by claim, `MarkSent/MarkFailed` (scoped to the message's batch,
  also closes a stopped batch) and `RecoverInterrupted` (sending batches only).
- `StopBatch` does not close a batch; `MarkSent` does.
- `readSelection` runs before the token lookup, so a resubmit after a later import or list deletion
  shows the 409 stale page instead of the existing batch (no double send). Store-level token-first
  behavior is as planned.
- `SaveSelection` still runs on every compose action, including `send` (a convenience, not part of sending).
- The test sender cannot fail; failure/"unknown" cases were checked by editing a temp DB.
