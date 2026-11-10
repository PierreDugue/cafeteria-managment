# Cafeteria Management — Specification

Status: draft · Last updated: 2026-11-10

## 1. Goal

A small desktop tool to email the parents of selected children.

The user imports an Excel file that lists parents, children and parent email
addresses. They select some children, write (or pick) a message, and the app
sends one email to each concerned parent.

## 2. Users and context

- One person, on their own computer (Windows or macOS).
- Non-technical user: installing and starting the app must be trivial.
- No server, no internet hosting. Data stays on the computer.

## 3. Installation and startup

- Delivered as **a single executable file** per platform (Windows `.exe`, macOS binary).
- No runtime, database server or other dependency to install.
- On start, the app runs a local web server on `127.0.0.1` only (never exposed
  to the network) and opens the default browser on the app page.
- Data is stored in one SQLite file in the user's app data folder
  (e.g. `%APPDATA%\CafeteriaManagement\app.db`, `~/Library/Application Support/CafeteriaManagement/app.db`).
  Backing up = copying that file.

## 4. Features

### 4.1 Excel import

- Upload an `.xlsx` file from the browser.
- Show a **preview** before saving: rows read, rows with errors (missing
  email, invalid email, empty names), duplicates.
- On confirm, insert or update data:
  - A parent is identified by their email address (case-insensitive).
  - A child is identified by first name + last name + parent.
  - Re-importing a file updates existing records instead of duplicating them.
- Keep a record of each import (date, file name, counts).

**Expected file format:** to be confirmed (see §10). Working assumption: one row
per child, with columns for parent name, child name and parent email. Column
mapping should be tolerant (header names matched case/accents-insensitively),
with a manual column-mapping step as a later improvement.

### 4.2 Child list and selection

- List all children with their parent(s) and email.
- Search by child or parent name; filter (e.g. by class, if present in the file).
- Select children with checkboxes; "select all / none" for the current filter.
- Show a counter: N children selected → M emails will be sent.

### 4.3 Email composition

- Subject and body written by the user, plain text first (HTML/rich text later).
- Placeholders replaced per recipient:
  - `{{parent_name}}`
  - `{{child_name}}` — for several children: "Léa et Tom" / "Léa and Tom"
  - `{{children}}` — list of the selected children for that parent
- **Templates**: save a message as a named template, reuse and edit it later.
- **Preview** the final email for any one recipient before sending.

### 4.4 Sending

- Grouping: if several selected children share a parent, that parent receives
  **one** email mentioning all of them.
- Confirmation step before sending ("Send 12 emails?").
- Sending runs in the background with progress shown in the page.
- Small delay between emails to stay within provider rate limits.
- Each email gets a status: `pending`, `sent`, `failed` (with error message).
- Failed emails can be retried.

### 4.5 History

- List of sending batches: date, subject, number sent / failed.
- Detail per batch: recipients, children concerned, status.

### 4.6 Settings

- **Email account**, with a provider choice:
  - Outlook / Microsoft 365 (default)
  - Gmail
  - Other (custom SMTP: host, port, TLS mode, username)
- Sender name and sender address.
- "Send test email" button.
- Interface language.

## 5. Email providers

| Provider | Server | Authentication |
|---|---|---|
| Outlook / Microsoft 365 | `smtp.office365.com:587` (STARTTLS) | OAuth2 (see below) |
| Gmail | `smtp.gmail.com:587` (STARTTLS) | App password (requires 2FA), or OAuth2 |
| Custom SMTP | user-defined | username + password |

**Outlook risk:** Microsoft is phasing out plain username/password SMTP login
for Outlook.com and Microsoft 365 in favor of OAuth2 ("modern auth"). Plan:

1. Implement OAuth2 for Microsoft (sign in with Microsoft in the browser,
   store the refresh token, send via SMTP XOAUTH2 or the Microsoft Graph
   `sendMail` API). This requires registering an app in Microsoft Entra ID
   (free).
2. Keep plain SMTP login available for providers that still support it.

Code design: one `Sender` interface with one implementation per auth method,
so providers can be added without touching the rest of the app.

Gmail limit: about 500 emails/day for a personal account. Outlook.com: about
300/day. Both are enough for this use case.

## 6. Localization

- French at launch, English as second language; more languages possible.
- All UI strings come from translation files (`fr.json`, `en.json`) embedded in
  the binary. No hard-coded text in templates.
- Language selected in settings, defaulting to the system/browser language,
  falling back to French.
- Email content is written by the user, so it is not translated. The joining
  word for `{{child_name}}` ("et" / "and") follows the interface language.

## 7. Data model (SQLite)

```
parents        id, name, email (unique, lower-case), created_at, updated_at
children       id, first_name, last_name, class (nullable), created_at, updated_at
child_parents  child_id, parent_id                    -- a child can have 2 parents
imports        id, file_name, imported_at, rows_total, rows_ok, rows_error
templates      id, name, subject, body, created_at, updated_at
batches        id, subject, body, created_at, status
messages       id, batch_id, parent_id, to_email, subject, body, status, error, sent_at
message_children  message_id, child_id
settings       key, value
```

- Schema versioned with embedded SQL migrations, applied on startup.
- `messages` stores the final rendered text, so the history shows exactly what
  was sent even if a template or a child record changes later.

## 8. Security and privacy

- Server listens on `127.0.0.1` only.
- Children's data never leaves the computer except inside the emails sent.
- Secrets (SMTP password, OAuth refresh token) stored in the OS keychain
  (macOS Keychain, Windows Credential Manager), not in the database.
- No telemetry.

## 9. Technical choices

| Area | Choice |
|---|---|
| Language | Go (single static binary, easy cross-compilation) |
| HTTP | standard library `net/http` |
| UI | server-rendered `html/template` + htmx, assets embedded with `embed` |
| Excel | `github.com/xuri/excelize/v2` |
| Database | SQLite via `modernc.org/sqlite` (pure Go, no CGO) |
| Email | `github.com/wneessen/go-mail` (SMTP, STARTTLS, XOAUTH2) |
| OAuth2 | `golang.org/x/oauth2` |
| Secrets | `github.com/zalando/go-keyring` |

Project layout:

```
cmd/cafeteria/        entry point (flags, startup, open browser)
internal/server/      HTTP routes and handlers
internal/store/       database access and migrations
internal/importer/    Excel parsing and validation
internal/mailer/      email providers and sending queue
internal/i18n/        translations
web/templates/        HTML templates
web/static/           CSS, JS (htmx)
web/locales/          fr.json, en.json
docs/                 this spec
```

### 9.1 Possible switch to a desktop window (Wails)

The app starts as a browser tab. It may later run in its own window with
[Wails](https://wails.io), which can serve the same `http.Handler`
(`internal/server`) through its asset server. To keep that switch cheap:

- Keep all logic in `internal/`; `cmd/` only wires things together.
- Build the UI with plain HTML forms + htmx (file upload works in the webview).
- Avoid browser-only behavior: new tabs, popups, `window.open`.
- Treat file downloads (exports) as a special case: they need a Wails save
  dialog instead of a browser download.

## 10. Open questions

1. Exact Excel layout: one row per child? Can a child have two parents / two
   emails? Extra columns (class, etc.)? → a sample file with fake data is needed.
2. Is the Outlook account personal (Outlook.com) or a school Microsoft 365
   account? This changes the OAuth2 setup.
3. Should attachments be supported (e.g. a PDF menu)?
4. Should the app sign and notarize the macOS binary (avoids security warnings,
   requires an Apple developer account)?

## 11. Milestones

1. **Skeleton** — Go module, HTTP server, embedded placeholder page, build scripts. ✅
2. **Storage** — SQLite, migrations, data folder.
3. **Import** — Excel upload, preview, save.
4. **Selection** — child list, search, selection.
5. **Compose & send** — templates, placeholders, preview, SMTP sending, history.
6. **Outlook OAuth2** — Microsoft sign-in.
7. **i18n** — FR/EN translation files and language switch.
8. **Packaging** — Windows/macOS builds, open browser on start, app icon.

## 12. Out of scope (for now)

- Several users or several computers sharing data.
- Hosting the app online.
- Replies / reading the inbox.
- Scheduling emails for later.
