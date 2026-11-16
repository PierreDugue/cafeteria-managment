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

Status: ✅ implemented (`internal/importer`, `internal/store`, `/import` pages).

- Upload an `.xlsx` file, or the `.csv` export of the families software
  (§4.1.1), from the browser (max 20 MB; other formats such as `.xls` are
  refused with a hint to save as `.xlsx`). Only the first sheet is read.
  The layout (parent/child or family export) is chosen from the header
  row, whatever the file format.
- Show a **preview** before saving: rows read, valid rows, row errors,
  warnings, and the planned changes.
- **Row errors block the import**: missing parent name, missing child name,
  missing or invalid email. The user fixes the file and imports it again.
  (Importing only the valid rows would delete the children of the invalid
  rows, since the file is the source of truth.) These rules are those of
  the parent/child layout and stay as they are; the family export has its
  own rules (§4.1.1), where a missing or invalid payer email is only a
  warning.
- **Warnings don't block**: duplicate row (same child + email, ignored);
  same email with a different parent name (the first name is kept).
- Nothing is written to the database during the preview. All parsed data is
  saved **when the user confirms the preview**.
- The save runs in **one database transaction**: either the whole file is
  saved, or nothing changes (e.g. on error or crash, the previous data stays).
- **The Excel file is the source of truth.** Each import replaces the
  parents and children data:
  - Entries present in the file and already in the database get the values
    from the file (existing values are overwritten).
  - New entries in the file are added.
  - Entries in the database but missing from the file are deleted.
  - Matching rules: a parent is identified by their email address
    (case-insensitive); a child by their name + parent, the name compared
    without case, accents or extra spaces ("LEA  Dupont" = "Léa Dupont").
    A family entry (§4.1.1) by its family number + payer email, or by its
    family number alone when it has no valid payer email.
    Matching keeps database IDs stable between imports.
  - A child listed with two parents (two rows, two emails) is stored once
    per parent.
  - Both formats write the same data: a CSV import deletes the children of
    an earlier Excel import, and the other way round (shown in the preview).
  - In the counts, family entries (with or without a valid email) count as
    children; parents are the valid email addresses only.
- The preview shows what the import will change: N added, N updated,
  N deleted. Deletions are highlighted so the user can cancel.
- Email history (§4.5), templates and settings are **not** affected by an
  import. History stays readable even after a child or parent is deleted.
- Keep a record of each import (date, file name, counts).

- The preview runs the real import in a transaction and rolls it back, so
  what it shows is exactly what the confirmation will do. The checked rows
  wait in memory for 30 minutes under a token, used up only by a successful
  save (a failed save can be retried).
- If another import was saved between the preview and the confirmation, the
  confirmation is refused and the user must check the file again, so an
  import never applies changes its preview did not show.

**File format:** to be confirmed with a real file (see §10). Current
assumption: one row per child; the first non-empty row is the header; other
columns are ignored. Accepted header names (case, accents and spaces
ignored):

| Column | Accepted headers |
|---|---|
| Parent name | nom parent, nom du parent, nom parents, nom des parents, parent, parents, parent name |
| Child name | nom enfant, nom de l'enfant, enfant, child, child name |
| Email | adresse email, adresse e-mail, adresse mail, adresse courriel, email, e-mail, mail, courriel |

Sample file with fake data: `docs/exemple-familles.xlsx`.
Later improvement: a manual column-mapping step.

### 4.1.1 CSV import (family export)

Status: ✅ implemented
(`docs/plans/002-csv-import.md`).

The user can also import the CSV export of the families software, through the
same page, preview and confirmation as the Excel file (§4.1), with the same
"the file is the source of truth" rule.

**Format** (header line, `;`-separated; empty header cells are columns the
app ignores):

```
NUMFAM;NUMFACFACT;NOMFACT;;;;;;;;;;;NOM RL1;PRENOM RL1;;PORT RL1;EMAIL RL1;NOM RL2;PRENOM RL2;;PORT RL2;EMAIL RL2;;;;PORT PAYEUR;EMAIL PAYEUR
```

- `NUMFAM`: family number. **A family can appear on several lines.**
- `NOMFACT`: billing name. `NUMFACFACT`: billing number.
- `RL1` / `RL2`: legal guardians 1 and 2 (`NOM`, `PRENOM`, `PORT` = mobile,
  `EMAIL`). `PAYEUR`: the payer (`PORT`, `EMAIL`).

**Recipient rule** (given by the user):

- Emails are sent to **`EMAIL PAYEUR`** only.
- Lines are grouped by `NUMFAM`. Within a family:
  - if every line has the same `EMAIL PAYEUR` (case-insensitive), the family
    is saved **once**, and its email goes to that address;
  - if the lines have different `EMAIL PAYEUR` values, **each distinct
    address is kept**, and the email is sent to each of them.
- So one saved entry = one (`NUMFAM`, `EMAIL PAYEUR`) pair.

**Data kept** (data minimisation): family number, billing name, payer email.
Phone numbers, RL names/emails and other columns are not stored.

**Decisions** (confirmed by the user, 2026-11-16):

1. **Selection lists families**: the file has no child name, so the
   selection page (§4.2) shows one row per (family number, payer email):
   family number, billing name, payer email. A family with two different
   payer emails appears on **two rows**, side by side, ticked separately.
2. **Name**: `NOMFACT` is the name shown and used for `{{parent_name}}`.
3. **Missing `EMAIL PAYEUR`** (should not happen): the family is still
   imported, with a **warning on its line** in the preview. In the list it is
   shown with a warning ("email du payeur manquant") and **cannot be selected**:
   no email can be sent to it.
4. **Invalid `EMAIL PAYEUR`**: does **not** block the import. Warning on the
   line in the preview; the family is imported and treated like a missing
   email (shown with a warning, cannot be selected).
5. **Encoding** (UTF-8 or Windows-1252) is detected automatically.
6. **Two families with the same payer email**, both selected: **one** email to
   that address, mentioning both families (§4.4 rule).

**Assumed** (recommended by the architect, not yet confirmed):

- The Excel parent/child import (§4.1) stays available. Each import, CSV or
  Excel, replaces all the data.
- A line without `NUMFAM` or without `NOMFACT` blocks the import (a family
  cannot be identified or named without them).

### 4.2 Child list and selection

Status: ✅ implemented (plan `docs/plans/001-child-selection.md`). One row per
child + parent pair; search, selection and counter run in the page; at most
5,000 children per selection. Class filter not done (no class column yet).

- List all children with their parent(s) and email.
- Search by child or parent name; filter (e.g. by class, if present in the file).
- Select children with checkboxes; "select all / none" for the current filter.
- Show a counter: N children selected → M emails will be sent.

**Family data** (CSV export, §4.1.1; plan `docs/plans/002-csv-import.md`):
the list shows one row per family entry: family number, billing name, payer
email. Rows of one family stay adjacent (sorted by the family's name, then
family number). An entry without a valid payer email is shown with a
warning ("Email du payeur manquant", or "Email du payeur invalide : …" with
the value read, so the user can fix the file) and its checkbox is disabled;
a notice gives their number. The server refuses a selection that contains
one. An invalid payer email in a family that also has a valid one gives
that family an extra, unselectable row.

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

The main account is a **Microsoft 365 work account** (Exchange Online).

| Provider | How it sends | Authentication |
|---|---|---|
| Microsoft 365 (default) | Microsoft Graph API, `POST /me/sendMail` | OAuth2, "Sign in with Microsoft" |
| Gmail | SMTP `smtp.gmail.com:587` (STARTTLS) | App password (requires 2FA) |
| Custom SMTP | user-defined host/port/TLS | username + password |

### 5.1 Microsoft 365 via Microsoft Graph

Why Graph rather than SMTP: on Microsoft 365, SMTP sending (SMTP AUTH) is
often disabled by the organization, and Microsoft is retiring password-based
SMTP login. Graph works with modern sign-in only, and sent emails are saved in
the user's Outlook **Sent Items** folder.

- **App registration** (once, free) in Microsoft Entra ID:
  - Platform "Mobile and desktop applications" (public client, no secret).
  - Redirect URI: `http://localhost` (the app's own local server receives
    the login response).
  - Delegated permissions: `Mail.Send`, `User.Read`, `offline_access`.
- **Sign-in flow**: OAuth2 authorization code with PKCE. The user clicks
  "Se connecter avec Microsoft" in Settings, signs in on the Microsoft page,
  and is sent back to the app. The app shows which account is connected.
- **Tokens**: the refresh token is stored in the OS keychain and refreshed
  automatically; the user signs in again only if it expires or is revoked.
- **Sending**: one Graph `sendMail` call per email, with
  `saveToSentItems: true`. Plain HTTP + `golang.org/x/oauth2`; no Microsoft
  SDK needed.
- **Limits** (Exchange Online): about 30 messages per minute and
  10,000 recipients per day. The sending queue waits ~2 s between emails and
  backs off on HTTP 429 (`Retry-After`).
- **Admin consent risk**: the organization may block users from approving
  new apps. Then an IT admin must either grant consent once or register the
  app in the organization's own tenant (see §10).

### 5.2 Other providers (SMTP)

Gmail and custom servers use SMTP with `github.com/wneessen/go-mail`
(correct encoding of accents, STARTTLS, connection reuse).
Gmail limit: about 500 emails/day for a personal account.

### 5.3 Code design

One `Sender` interface (`Send(ctx, Message) error`) with two implementations:
`graphSender` and `smtpSender`. The sending queue, history and UI only know the
interface, so providers can be added without touching the rest of the app.
Tests use a fake sender; no real email is ever sent from tests.

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
parents        id, name, email (unique, lower-case, always valid), created_at, updated_at
children       id, parent_id (nullable), family_number, name, name_key,
               invalid_email, created_at, updated_at
               -- Excel child: family_number ''; unique (parent_id, name_key)
               -- family entry: name = NOMFACT; unique (parent_id, family_number)
               -- family entry without a valid payer email: parent_id NULL,
               --   invalid_email = value read ('' if missing);
               --   unique (family_number)
               -- deleted with their parent
imports        id, file_name, imported_at, rows_read,
               parents_added/updated/deleted, children_added/updated/deleted
templates      id, name, subject, body, created_at, updated_at
batches        id, subject, body, created_at, status
messages       id, batch_id, parent_id (nullable), parent_name, to_email,
               subject, body, status, error, sent_at
message_children  message_id, child_id (nullable), child_name
settings       key, value
```

- Schema versioned with embedded SQL migrations, applied on startup.
- `messages` stores the final rendered text, so the history shows exactly what
  was sent even if a template or a child record changes later.
- History tables keep a copy of names and emails, and their links to
  `parents` / `children` use `ON DELETE SET NULL`, so a re-import that
  deletes a child or parent never breaks or erases history.

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
| Email (Microsoft 365) | Microsoft Graph `sendMail` over `net/http` |
| Email (SMTP) | `github.com/wneessen/go-mail` |
| OAuth2 | `golang.org/x/oauth2` (authorization code + PKCE) |
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
2. ~~Outlook.com or Microsoft 365?~~ → Microsoft 365 work account.
   Still open: can users approve new apps themselves in this organization, or
   does IT need to grant admin consent / register the app? (Ask IT.)
3. Should attachments be supported (e.g. a PDF menu)?
4. Should the app sign and notarize the macOS binary (avoids security warnings,
   requires an Apple developer account)?

## 11. Milestones

1. **Skeleton** — Go module, HTTP server, embedded placeholder page, build scripts. ✅
2. **Storage** — SQLite, migrations, data folder. ✅
3. **Import** — Excel upload, preview, save. ✅ (minimal FR/EN i18n added
   with it, since no UI text may be hard-coded)
4. **Selection** — child list, search, selection. ✅
5. **Compose & send** — templates, placeholders, preview, sending queue
   (fake sender), history.
6. **Microsoft 365** — Microsoft sign-in and Graph sending.
6b. **SMTP** — Gmail / custom SMTP sender.
7. **i18n** — FR/EN translation files and language switch.
8. **Packaging** — Windows/macOS builds, open browser on start, app icon.

## 12. Out of scope (for now)

- Several users or several computers sharing data.
- Hosting the app online.
- Replies / reading the inbox.
- Scheduling emails for later.
