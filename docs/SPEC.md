# Cafeteria Management — Specification

Status: draft · Last updated: 2026-11-17

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

### 4.0 Family lists

Status: ✅ implemented (plan `docs/plans/004-family-lists.md`). Changes the
"one import replaces all the data" rule of §4.1 / §4.1.1.

The user can keep **several family lists**, each with a **name** (e.g.
"Cantine 2026-2027", "Garderie"). Every family entry (or child, with the
Excel layout) belongs to exactly one list.

- **Import into a list**: on the import page the user chooses either
  - **"Nouvelle liste…"**, and types its name, or
  - **an existing list**, which the import **replaces**.

  Once at least one list exists, the target **must be chosen every time**:
  there is no default choice (replacing the wrong list would delete its
  entries). With no list yet, only the name of the new list is asked.
- **Explicit confirmation to replace a list**: the preview of an import
  that replaces an existing list has a required checkbox "Je confirme
  remplacer la liste « X »." and a button "Remplacer la liste « X »" (red,
  with the number of deletions, when entries will be deleted; the list of
  deleted entries is shown as before). The **server checks** the checkbox
  on confirmation: without it, nothing is saved and the same preview is
  shown again with a message, still confirmable. Creating a new list needs
  no extra confirmation beyond the preview.
- The "file is the source of truth" rule (§4.1) now applies **within the
  chosen list only**: entries of that list present in the file are updated,
  new ones are added, the ones missing from the file are deleted. **Other
  lists are never touched by an import.**
- The preview (§4.1) says which list is concerned ("Nouvelle liste
  « Cantine »" or "Remplace la liste « Cantine »") and counts changes for
  that list only. Creating a new list shows only additions.
- A list keeps the layout of its last import (family export CSV or
  parent/child Excel). Replacing a list with a file of the other layout is
  allowed: the preview shows the replacement like any other.
- **List name**: required, at most 80 characters, unique ignoring case,
  accents and extra spaces (same rule as template names). The name is only
  shown in the app, never in emails.
- **The same family in two lists** (same number and payer email) is
  allowed: the two entries are independent.
- **Selection** (§4.2): the selection page shows **one list at a time**,
  chosen with a list selector. A selection, and so one sending, concerns
  one list only (no mixing of lists).
- **Last list used** (opened by default on the selection page): the list
  last imported into, or last continued from on the selection page
  ("Continuer"). Merely opening a list does not change it.
- **Remembered selection** (§4.2): kept **per list**.
- **Stale selection** (§4.2): tied to the version of the **selected list**
  (its last import), so importing into another list does not invalidate a
  selection in progress.
- **Home page**: one line per list, with its number of families (or
  children) and email addresses (or parents), its last import date, and the
  actions Choisir / Renommer / Supprimer.
- **Rename**: a list can be renamed (same name rules). Its entries,
  selection and version are kept.
- **Delete**: a list can be deleted after a confirmation page that shows
  how many entries and addresses will be deleted. Its entries,
  remembered selection and "Tolérance" flags (§4.2) are deleted; the
  import history is kept, with the last name of the list. Other lists and templates are not affected.
- **Import history** (§4.1) records the list of each import and shows its
  **current** name (after a rename), or its last name followed by
  "(supprimée)" once the list is deleted. Email history (§4.5) records the
  list name at sending time, so it stays readable after the list is renamed
  or deleted.
- **Existing data**: when this feature is installed, the data already
  imported becomes one list, named "Liste importée" (the user can rename
  it). This list is created only if there is data; a fresh install starts
  with no list.

Decisions confirmed by the user, 2026-11-16 (plan 004).

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
  parents and children data **of the list chosen for the import** (§4.0;
  other lists are not touched):
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
  - Both formats write the same data: within one list, a CSV import deletes
    the children of an earlier Excel import, and the other way round (shown
    in the preview).
  - In the counts, family entries (with or without a valid email) count as
    children; parents are the valid email addresses only.
- The preview shows what the import will change: N added, N updated,
  N deleted. Deletions are highlighted so the user can cancel.
- Email history (§4.5), templates and settings are **not** affected by an
  import. History stays readable even after a child or parent is deleted.
- "Tolérance" flags (§4.2) of the list are kept for the family numbers
  still in the file and removed for the others; the preview shows how many
  are removed.
- Keep a record of each import (date, file name, list, counts). The history
  shows the current name of the list, or its last name followed by
  "(supprimée)" if the list was deleted (§4.0).
- Replacing an existing list needs the explicit, server-checked
  confirmation of §4.0.

- The preview runs the real import in a transaction and rolls it back, so
  what it shows is exactly what the confirmation will do. The checked rows
  wait in memory for 30 minutes under a token, used up only by a successful
  save (a failed save can be retried).
- If another import was saved **into the same list** between the preview and
  the confirmation (or the list was deleted, or the new list name was taken
  meanwhile), the confirmation is refused and the user must check the file
  again, so an import never applies changes its preview did not show.

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
2. **Name**: `NOMFACT` is the name shown and used for `{{parent_name}}`
   (for an address shared by several families: see decision 6).
3. **Missing `EMAIL PAYEUR`** (should not happen): the family is still
   imported, with a **warning on its line** in the preview. In the list it is
   shown with a warning ("email du payeur manquant") and **cannot be selected**:
   no email can be sent to it.
4. **Invalid `EMAIL PAYEUR`**: does **not** block the import. Warning on the
   line in the preview; the family is imported and treated like a missing
   email (shown with a warning, cannot be selected).
5. **Encoding** (UTF-8 or Windows-1252) is detected automatically.
6. **Two families with the same payer email**, both selected: **one** email to
   that address, mentioning both families (§4.4 rule). Its
   `{{parent_name}}` is built from the billing names of the **selected**
   families for that address, without duplicates: "SILVA Ana et SILVA Rui"
   when both are selected, "SILVA Rui" when only that family is (not the
   first `NOMFACT` stored for the address). Confirmed by the user,
   2026-11-16 (plan 003).

**Assumed** (recommended by the architect, not yet confirmed):

- The Excel parent/child import (§4.1) stays available. Each import, CSV or
  Excel, replaces the data of the chosen list (§4.0).
- A line without `NUMFAM` or without `NOMFACT` blocks the import (a family
  cannot be identified or named without them).

### 4.2 Child list and selection

Status: ✅ implemented (plan `docs/plans/001-child-selection.md`). One row per
child + parent pair; search, selection and counter run in the page; at most
5,000 children per selection. Class filter not done (no class column yet).

- **One list at a time** (§4.0): the list name is shown at the top; a
  selector (shown only when there are 2 or more lists) chooses the list.
  The last list used (last import or last "Continuer", §4.0) opens by
  default; an unknown list (e.g. deleted) opens the default list with a
  message.
- List all children with their parent(s) and email.
- Search by child or parent name; filter (e.g. by class, if present in the file).
- Select children with checkboxes; "select all / none" for the current filter.
- Show a counter: N children selected → M emails will be sent.
- **The last selection is remembered**: when the user continues to the
  message page with a valid selection, the selected entries are saved; the
  next time the list opens they are pre-checked (never an entry without a
  valid email). The saved selection of an entry removed by an import
  disappears with it. Nothing is saved when the page is only opened.
  The remembered selection is kept **per list** (§4.0).

**Family data** (CSV export, §4.1.1; plan `docs/plans/002-csv-import.md`):
the list shows one row per family entry: family number, billing name, payer
email. Rows of one family stay adjacent (sorted by the family's name, then
family number). An entry without a valid payer email is shown with a
warning ("Email du payeur manquant", or "Email du payeur invalide : …" with
the value read, so the user can fix the file) and its checkbox is disabled;
a notice gives their number. The server refuses a selection that contains
one. An invalid payer email in a family that also has a valid one gives
that family an extra, unselectable row.

**Tolérance** (family data only; plan `docs/plans/007-tolerance.md`,
confirmed by the user 2026-11-17):

- Each family has a **"Tolérance"** checkbox set by the user: **one flag
  per family number** of the list (a family with two payer emails, so two
  rows, has one flag). The checkbox is on the family's first row; every row
  of a flagged family has a **red background**. A family whose only entry
  has no valid email can be flagged too.
- The flag is **remembered** in the database, per list (the same family in
  two lists has two independent flags). It is saved at once when ticked
  (a "save" button without JavaScript). It is separate from the selection:
  it never changes the selection, the counter or a sending, and is shown
  nowhere else (not in emails, compose or history).
- **Sorting**: a click on the "Tolérance" header shows the flagged
  families first (each group in the usual order), and again goes back to
  the usual order. Families stay together; the sort is not remembered.
- **Re-import** (§4.0, §4.1): the flag lives as long as the list has that
  family number. A family still in the file keeps its flag (even if its
  payer email or billing name changed); a family number no longer in the
  file loses its flag with its entries. The preview says how many flags
  will be removed and marks those families in the deleted entries.
- Excel parent/child lists have no family number, so no "Tolérance"
  column.

**Stale selection** (plan 003, confirmed 2026-11-16): the selection is tied
to the data version, sent with the form. With several lists (§4.0) this is
the version of the **selected list** (its last import ID): importing into
another list does not affect it. If an import was saved into that list (or
the list was deleted) between the selection and the composition (or the
preview, or later the sending), the request is refused and the user selects again
("les données ont changé depuis votre sélection"). This replaces the former
"N selected rows no longer exist" notice: after a re-import a stale ID
could point to another child.

### 4.3 Email composition

Status: ✅ implemented (plan `docs/plans/003-email-composition.md`).

- Subject and body written by the user, plain text first (HTML/rich text later).
- **Placeholders** replaced per recipient. The names are English, written
  `{{name}}` (spaces inside the braces and case ignored); the page lists the
  placeholders of the current data with a description in the interface
  language. No French aliases.

  | Placeholder | Children data (Excel) | Family data (CSV, §4.1.1) |
  |---|---|---|
  | `{{parent_name}}` | parent name | billing names of the selected families for that address, joined ("SILVA Ana et SILVA Rui") |
  | `{{family_number}}` | not available | family numbers of the selected families for that address, joined ("1234 et 1250") |

  - Joining: "A", "A et B", "A, B et C"; the word ("et" / "and") follows
    the interface language (§6). Duplicate billing names or family numbers
    of one address appear once.
  - A placeholder "not available" for the imported data is an error, never
    replaced by an empty or substitute value.
  - Child placeholders (`{{child_name}}`, `{{children}}`) are removed for
    now because the CSV export has no child names; they are reported as
    unknown placeholders.
- **Rendering rules**: the text is read once, left to right; values are
  inserted as plain text and never read again as placeholders. Line endings
  are normalized to `\n`; the subject is trimmed; trailing whitespace at
  the end of the body is removed.
- **Errors block the preview and the sending** and are listed with the
  field concerned: unknown placeholder, placeholder not available for this
  data, badly written placeholder (`{{` or
  `}}` without its pair, line break or braces inside, more than 40
  characters), empty subject or body, too long, forbidden character
  (control characters; in the subject also line breaks and tabs). An email
  is never sent with a raw `{{…}}` left in it.
- **Limits**: subject 255 characters, body 20,000 characters, template name
  80 characters. All three are required for a template. The subject limit
  also applies **after placeholders are replaced**, for each recipient: a
  longer rendered subject blocks the preview of that recipient and the
  sending (with the number of recipients concerned and one example
  address); it is never cut (plan 005).
- **Templates**: save a message as a named template (from the compose page
  or the templates pages), reuse and edit it later, delete it with a
  confirmation. Names are unique ignoring case, accents and extra spaces.
  A template is checked like a message when saved (unknown or badly written
  placeholders refused; "not available" is not checked, since a template is
  not tied to one kind of data). Loading a template replaces the current
  subject and body. Imports never touch templates.
- **Preview** the final email (to, subject, body) for any one recipient
  before sending.
- The selection and the text travel in the page's form (no draft is saved);
  the selection must still be current (§4.2 "stale selection").

### 4.4 Sending

Status: plan `docs/plans/005-sending.md` approved (milestone 5b, test mode
only). Decisions confirmed by the user, 2026-11-16.

- Grouping: if several selected children share a parent, that parent receives
  **one** email mentioning all of them.
- **Confirmation step**: the compose page has an "Envoyer…" button. The
  server checks the selection (§4.2 "stale selection") and the message
  (§4.3), then shows a confirmation page: "Envoyer 12 email(s) ?" (family
  data: "…, un par adresse ?"), the list name, the subject and body, the
  first recipient's email as an example, the recipients, and "Gardez
  l'application ouverte pendant l'envoi (environ N minute(s))". Buttons
  "Envoyer les N emails" and "Modifier le message". Nothing is stored
  before the final confirmation.
- **The final confirmation re-checks everything** (selection still current,
  message without problems) and stores the batch and all its emails in one
  transaction, with the final rendered text of each email.
- **Never twice by accident**: each confirmation page carries a one-time
  token; a double-click, a reload, a back-and-resubmit or a duplicated tab
  of the same confirmation page lead to the same batch. Two confirmations
  made separately (two clicks on "Envoyer…") are two batches, with no
  extra warning.
- Sending runs **in the background**, one email at a time, with a small
  delay between emails to stay within provider rate limits (§5.1) and a
  wait when the provider asks for one (HTTP 429). **One batch at a time**,
  in confirmation order; a later batch shows "En attente : un autre envoi
  est en cours".
- **Progress page** (the batch page, §4.5): refreshed every 3 s by the page
  itself (plain meta refresh, no JavaScript; htmx later) while the batch is
  sending, with a progress bar and the sent / failed / pending counts.
- Each email gets a status: `pending`, `sending` (being sent), `sent`,
  `failed` (with an error type and the provider's error text).
- **Stop and resume**: "Arrêter l'envoi" stops after the email in flight;
  "Reprendre l'envoi" sends the pending emails.
- **Failed emails can be retried** ("Renvoyer les N emails en échec"):
  the same stored text goes to the same stored address, even after a
  re-import or the deletion of the list.
- **App closed or crashed during a sending**: the sending is **not**
  resumed automatically. At the next start, if emails remain to send, the
  batch shows "Interrompu" and the home page says "N envoi(s)
  interrompu(s)"; the user clicks "Reprendre l'envoi". The email that was being sent at that moment is
  marked failed with "état inconnu" (maybe delivered, maybe not): the user
  checks their sent items before retrying it. When the app is closed
  normally, the email in flight is finished and recorded first.
- If the email account cannot send at all (not signed in, access revoked),
  the batch stops with that reason and nothing is marked failed; the user
  resumes after fixing the account (the real provider comes in milestone 6).
- **Test mode** (milestone 5b): the only sender until Microsoft 365 is
  added. It sends nothing and only records the emails in the history (no
  `.eml` files). It cannot be missed: a banner on the confirmation and
  batch pages, a "Test" badge in the history, and the status "Enregistré
  (test)" instead of "Envoyé". Each batch records its provider.

### 4.5 History

Status: plan `docs/plans/005-sending.md` approved.

- **History list** (nav item "Historique"): one line per batch, newest
  first, the last 100 (with a note when there are more): date, subject as
  typed, list name, sent / failed / pending counts, status, "Test" badge.
- **Batch page** (also the progress page, §4.4): status, counts, the
  actions that apply (stop, resume, retry), date, list name, subject and
  body as typed, and one line per recipient: name, address, families or
  children concerned, status, error type and the provider's error text,
  sent time, and a link to the email.
- **Email page**: one email exactly as stored (to, subject, body).
- History keeps copies of the list name (at sending time), names, family
  numbers, addresses and the final texts, so it stays complete after a
  re-import, a list rename or a list deletion (§4.0, §4.1). Imports never
  change or erase it. Deleting history is not available.

### 4.6 Settings

Status: plan `docs/plans/006-settings.md` approved (2026-11-17).

A **"Paramètres"** page (nav item), plain form. Settings are stored in the
database (§7), so they survive a restart and are backed up with `app.db`.
Secrets never go there (§8).

Now (plan 006):

- **Interface language**: "Automatique (langue du navigateur : …)"
  (default), "Français" or "English" (§6).
- **Sending** (read-only): how emails are sent today. In test mode
  (§4.4) the page shows the test-mode banner and says the email account
  will be chosen here once real sending exists.

With the real sending (milestone 6, Microsoft 365, §5.1), later:

- "Se connecter avec Microsoft", and the connected account shown (the
  sender is that account: no sender name or address to set).
- "Envoyer un email de test" button.

SMTP providers (Gmail, custom server) and their fields are not planned for
now (§5.4, §12).

## 5. Email providers

The main account is a **Microsoft 365 work account** (Exchange Online).

| Provider | How it sends | Authentication |
|---|---|---|
| Microsoft 365 (default) | Microsoft Graph API, `POST /me/sendMail` | OAuth2, "Sign in with Microsoft" |
| ~~Gmail~~ (not planned, §12) | SMTP `smtp.gmail.com:587` (STARTTLS) | App password (requires 2FA) |
| ~~Custom SMTP~~ (not planned, §12) | user-defined host/port/TLS | username + password |

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

**Not planned for now** (decided 2026-11-17, §12): §5.4 rules out sending
work emails through a personal account (Gmail) and password SMTP login to
Microsoft 365, and no approved SMTP relay is known. Kept for reference:

Gmail and custom servers would use SMTP with `github.com/wneessen/go-mail`
(correct encoding of accents, STARTTLS, connection reuse).
Gmail limit: about 500 emails/day for a personal account.

### 5.3 Code design

Package `internal/mailer` owns the `Sender` interface and the sending queue
(plan 005).

- One `Sender` interface (`Send(ctx, Message) error`). `Message` has a
  structured recipient (`net/mail.Address`: name and address, used through
  the provider's own fields, never a header built by concatenation), a
  one-line subject and a plain-text body. No `From`: the sender is the
  connected account.
- Errors: `RateLimitError` (provider asks to wait, with `RetryAfter`; the
  email was not sent) and `ErrAccount` (the account cannot send at all).
  Any other error fails that email only.
- Implementations: `TestSender` (milestone 5b, sends nothing), then
  `graphSender` (6). The queue, history and UI only
  know the interface, so providers are added without touching the rest of
  the app.
- The queue is one goroutine; the database is the queue (messages
  `pending` → `sending` → `sent` / `failed`), so nothing is lost or sent
  twice by itself across a restart.
- Tests use a fake sender; no real email is ever sent from tests.

### 5.4 Workarounds without app registration

Status: specified (2026-11-17), not implemented. Which option to build first
depends on the open question below.

**Context**: the account is confirmed to be **Microsoft 365**
(`ville-nice.fr`: MX `*.mail.protection.outlook.com`, `MS=` verification
record, autodiscover at `autodiscover.outlook.com`). The app is not an
officially approved application, so registering it in the organization's
Entra ID (§5.1) is not possible for now. Until it is, the app prepares the
emails and the user sends them with **tools already allowed**: Word,
Outlook (classic or web).

**Ruled out**: anything that gets around the organization's controls, such
as reusing another application's Microsoft client ID, password-based SMTP
login to Microsoft 365, or sending work emails through a personal account.
These would bypass the employer's security rules and route residents'
personal data through unapproved channels.

The selection (§4.2), placeholders and preview (§4.3) stay exactly as they
are; each option below replaces only the final "send" step (§4.4).

#### Option 1 — Export for Word mail merge (publipostage)

- On the compose page, **"Exporter pour publipostage"** saves an `.xlsx`
  file with one row per email of the current selection, after the usual
  checks (§4.3: no unknown placeholder; §4.0 / §4.2: current list and
  version; entries without a valid email left out). Columns: email address,
  name (`{{parent_name}}` value), family number(s) (`{{family_number}}`
  value), plus the rendered subject and body.
- The user then runs Word: **Publipostage → Démarrer la fusion → Messages
  électroniques**, picks the file, inserts the fields, and **Terminer et
  fusionner → Envoyer des messages électroniques**. Outlook sends one email
  per row, from the user's own address; they appear in "Éléments envoyés".
- The history (§4.5) records the export (date, list, number of rows) as a
  batch with status "exporté", so the user knows what was prepared.
- **Requires classic Outlook on Windows** (desktop) and desktop Word. Does
  **not** work with Outlook on the web, the "new Outlook", Word on the web or
  Outlook for Mac.
- The exported file contains personal data: the app reminds the user to
  delete it after use; it is written only where the user saves it.

#### Option 2 — "Ouvrir dans Outlook", one click per recipient

- On a page listing the rendered emails, each recipient has a button that
  opens a new message **already filled in** (to, subject, body); the user
  checks it and clicks **Envoyer** in Outlook.
- Outlook on the web: the official compose link
  `https://outlook.office.com/mail/deeplink/compose?to=…&subject=…&body=…`
  (all values URL-encoded). Classic or new Outlook, Outlook for Mac: a
  `mailto:` link with the same fields.
- The app marks each recipient "ouvert dans Outlook" (stored in the
  history, §4.5), so the user can stop and continue later.
- **Limits**:
  - Long bodies may be cut by the link length (about 2,000 characters);
    the app warns before opening when a link is too long.
  - The address and text are in the link, so they end up in the browser
    history (acceptable on the user's work computer; stated on the page).
  - It opens a new tab, which needs adapting if the app later runs in a
    Wails window (§9.1).
- Works with **every** Outlook, including the web version.

#### Option 3 — One email, all recipients in hidden copy (Cci)

- For messages that need no personalization (general announcements), the
  app shows the selected addresses ready to copy, separated by `;`, in
  groups of at most **500** (usual Outlook limit per message), to paste in
  the **Cci** field. Placeholders are refused in this mode.
- Addresses stay hidden from each other. Works with every Outlook.

#### Option 4 — Official approval, later

The app runs only on the user's computer, sends nothing to the internet
except the emails, and stores only family number, billing name and payer
email. With the user's manager and the city's data protection officer
(DPO), this may be enough to get it approved; then §5.1 (Graph) applies
unchanged, with the existing test mode and queue (plan 005).

**Recommendation**: with classic Outlook available, build option 1, plus
option 3. With only Outlook on the web (or the new Outlook / Mac), build
option 2, plus option 3.

**Open question**: which Outlook is available on the work computer: classic
Outlook for Windows, the new Outlook, Outlook on the web only, or Outlook
for Mac?

## 6. Localization

- French at launch, English as second language; more languages possible.
- All UI strings come from translation files (`fr.json`, `en.json`) embedded in
  the binary. No hard-coded text in templates.
- Language chosen in Settings (§4.6, plan 006): "Automatique" (default:
  the browser's language, i.e. the system's in a desktop window),
  "Français" or "English". Automatic falls back to French. The choice is
  stored in the database; a stored language that is no longer supported
  counts as automatic.
- Email content is written by the user, so it is not translated. The joining
  word of placeholders that list several values (in family data `{{parent_name}}`
  and `{{family_number}}`: "et" / "and")
  follows the interface language.

## 7. Data model (SQLite)

Planned for family lists (§4.0, plan 004, not implemented yet; migration
005 rebuilds `parents`, `children` and `last_selection` keeping every ID):

```
lists          id (AUTOINCREMENT, never reused), name, name_key (unique),
               created_at, updated_at, used_at (last import or "Continuer")
parents        + list_id (ON DELETE CASCADE); unique (list_id, email),
               unique (id, list_id)
children       + list_id (ON DELETE CASCADE);
               (parent_id, list_id) references parents (id, list_id):
               an entry and its parent are always in the same list;
               no-email key becomes unique (list_id, family_number)
imports        + list_id (ON DELETE SET NULL), list_name (copy at import time)
last_selection unchanged (child_id only): the selection of a list is the
               join with children.list_id, so no redundant list_id
```

The list version (stale selection, §4.2) is the highest `imports.id` of the
list. Existing data, if any, moves to a list named "Liste importée".

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
last_selection child_id (PK, references children, ON DELETE CASCADE)
               -- entries selected at the last "Continuer"; replaced as a whole;
               -- the cascade drops entries deleted by an import
templates      id, name, name_key (unique), subject, body, created_at, updated_at
               -- name_key = name without case/accents/extra spaces; body plain text
batches        id (AUTOINCREMENT, never reused), token (unique, one-time
               confirmation token), list_id (nullable, ON DELETE SET NULL),
               list_name (copy at sending time), list_version, families,
               provider ('test', later 'graph', 'smtp'), subject, body
               (as typed, with placeholders), status (sending | stopped |
               done), stop_reason ('' | user | interrupted | account),
               created_at, finished_at
messages       id, batch_id (ON DELETE CASCADE), parent_id (nullable,
               ON DELETE SET NULL), to_name (the {{parent_name}} value),
               to_email, subject, body (final rendered text), status
               (pending | sending | sent | failed), error_code ('' | send |
               rate_limited | account | unknown), error (provider text,
               cleaned, at most 500 characters), attempts, sent_at,
               updated_at
               -- indexes on (batch_id, status), (status, batch_id, id),
               --   parent_id
message_children  message_id (ON DELETE CASCADE), position, child_id
               (nullable, ON DELETE SET NULL), child_name (child name or
               billing name, copy), family_number (copy)
               -- primary key (message_id, position); index on child_id
settings       key (PK), value, updated_at
               -- keys: language ('' = automatic, 'fr', 'en'); never secrets
tolerances     list_id (ON DELETE CASCADE), family_number (not ''),
               created_at; primary key (list_id, family_number)
               -- "Tolérance" flags (§4.2); an import removes the flags of
               --   family numbers the list no longer has
```

`settings` comes with migration 007 (plan 006), `tolerances` with
migration 008 (plan 007).

The `batches`, `messages` and `message_children` tables come with migration
006 (plan 005). Batch counts are computed from `messages`, never stored.
The indexes on `messages.parent_id` and `message_children.child_id` keep the
`SET NULL` of an import's deletions fast.

- Schema versioned with embedded SQL migrations, applied on startup.
- `messages` stores the final rendered text, so the history shows exactly what
  was sent even if a template or a child record changes later.
- History tables keep a copy of names and emails, and their links to
  `parents` / `children` use `ON DELETE SET NULL`, so a re-import that
  deletes a child or parent never breaks or erases history.

## 8. Security and privacy

- Server listens on `127.0.0.1` only.
- Children's data never leaves the computer except inside the emails sent.
- Secrets (OAuth refresh token) stored in the OS keychain
  (macOS Keychain, Windows Credential Manager), not in the database (not
  in `settings` either).
- "Tolérance" flags (§4.2) are personal data: logs carry counts only,
  never family numbers.
- Email history (names, addresses, texts, provider error texts) lives in the
  database only. Logs carry IDs, counts and error codes only: never names,
  addresses, subjects, bodies or provider error texts. URLs carry IDs only.
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
| Email (SMTP) | `github.com/wneessen/go-mail` (not planned for now, §5.2) |
| OAuth2 | `golang.org/x/oauth2` (authorization code + PKCE) |
| Secrets | `github.com/zalando/go-keyring` |

Project layout:

```
cmd/cafeteria/        entry point (flags, startup, open browser)
internal/server/      HTTP routes and handlers
internal/store/       database access and migrations
internal/importer/    Excel parsing and validation
internal/recipients/  one recipient per parent for the selected children
internal/message/     placeholders, checks and rendering of one email
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
2. ~~Outlook.com or Microsoft 365?~~ → Microsoft 365 work account
   (`ville-nice.fr`, confirmed by its DNS records). App registration is not
   possible for now (the app is not officially approved): see the
   workarounds in §5.4. Still open: which Outlook is on the work computer
   (§5.4).
3. Should attachments be supported (e.g. a PDF menu)?
4. Should the app sign and notarize the macOS binary (avoids security warnings,
   requires an Apple developer account)?

## 11. Milestones

1. **Skeleton** — Go module, HTTP server, embedded placeholder page, build scripts. ✅
2. **Storage** — SQLite, migrations, data folder. ✅
3. **Import** — Excel upload, preview, save. ✅ (minimal FR/EN i18n added
   with it, since no UI text may be hard-coded)
4. **Selection** — child list, search, selection. ✅
4b. **Family lists** — several named lists, import into a new or an
   existing list, selection per list (§4.0; plan 004). ✅
5. **Compose & send**
   - 5a. **Compose** — templates, placeholders, preview (plan 003). ✅
   - 5b. **Send** — confirmation, sending queue with a test-mode sender
     (records only), stop / resume / retry, history (plan 005). ✅
5c. **Send without registration** — workaround options of §5.4 (mail
   merge export, "Ouvrir dans Outlook", Cci), depending on the Outlook
   available.
6. **Microsoft 365** — Microsoft sign-in and Graph sending (only once the
   app is approved and registered, §5.4 option 4).
7. **i18n** — FR/EN translation files and language switch.
8. **Packaging** — Windows/macOS builds, open browser on start, app icon.

## 12. Out of scope (for now)

- Several users or several computers sharing data.
- Hosting the app online.
- Replies / reading the inbox.
- Scheduling emails for later.
- SMTP sending (Gmail, custom SMTP server) and its settings (host, port,
  TLS mode, username, sender name and address): not for now (§5.2, §5.4;
  decided 2026-11-17). Could come back if an approved SMTP relay exists.
