# 003 — Email composition

Status: **done (2026-11-16) · reviewed: approved after 1 fix round** (user answers 2026-11-16,
every recommendation accepted) · SPEC §4.3 (and §4.1.1, §4.2, §6, §7)

## Goal

After choosing children (or families) on `/children`, the user writes a
subject and a plain-text body, can start from a saved template, uses
placeholders that are replaced per recipient, and previews the final email
for any one recipient (SPEC §4.3). Templates can be listed, created, edited
and deleted on their own pages, and saved from the compose page.

The page ends where the send step (§4.4) will plug in: the same form, posted
to a future `/send` route, carries everything the sender needs, and the code
that turns (selection, subject, body, language) into ready-to-send emails is
written now, so §4.4 only adds the confirmation, the batch tables and the
queue.

## Decisions

1. **Placeholder set, per data mode** (user decision 2). After an import the
   data is either all children (Excel) or all family entries (CSV) (plan
   002, decision 10). The mode of a compose request is **family** if any
   selected row has a family number, else **child**.

   | Placeholder | Child mode (Excel) | Family mode (CSV) |
   |---|---|---|
   | `{{parent_name}}` | the parent's name | billing names (`NOMFACT`) of the **selected** families for that address, without duplicates, joined (decision 2) |
   | `{{child_name}}` | selected children, joined on one line: "Léa", "Léa et Tom", "Léa, Tom et Zoé" | not available |
   | `{{children}}` | selected children, one per line, each line starting with `- ` (user decision 4) | not available |
   | `{{family_number}}` | not available | family numbers of the selected families for that address, without duplicates, joined: "1234" or "1234 et 1250" |

   - Joining: 1 → `A`; 2 → `A et B`; 3+ → `A, B et C` (no comma before the
     last word). The word is the locale key `message.join_and` of the
     request ("et" / "and", SPEC §6); the separator is `", "` in both.
   - `{{children}}` for Léa and Tom is `"- Léa\n- Tom"` (no trailing line
     break). It is meant to sit on its own line in the body.
   - Order of names and numbers: the order of `recipients.Group` (store
     order).
   - Deduplication: billing names by `textnorm.Key` (first spelling kept),
     family numbers by exact value. Child names are not deduplicated (two
     children with the same name are two children).
   - Every value is non-empty by construction (import requires parent name,
     child name, `NOMFACT`, `NUMFAM`; a recipient has at least one entry), so
     there are **no per-recipient problems**: a message that checks fine
     for the mode renders fine for every recipient.
   - A placeholder not available in the current mode is a **problem**
     (`unavailable`), never an empty string or a substitute. Rejected:
     filling `{{child_name}}` with family names in family mode ("le compte
     de {{child_name}}" would name the payer, which reads as a mistake).
   - Names stay English as in the SPEC, shown on the page with a French (or
     English) description each; no French aliases such as `{{nom_parent}}`,
     no `{{email}}` (user decision 5).

2. **Two families sharing a payer address (the "SILVA" case)** (user
   decision 1, option B). Today `parents.name` keeps the first `NOMFACT`
   seen, so the "SILVA Rui" family would be greeted as "SILVA Ana". In
   family mode `{{parent_name}}` is therefore built from the **selected
   entries of that recipient**, not from `parents.name`: only "SILVA Rui"
   selected → "SILVA Rui"; both → "SILVA Ana et SILVA Rui"; same name twice
   → once. This keeps "`NOMFACT` is the name used" (§4.1.1 decision 2) and
   "one email per address" (§4.1.1 decision 6). No import or schema change.
   - Rejected: A (keep `parents.name`: wrong greeting); C (one email per
     family: reverses §4.1.1 decision 6, two near-identical emails to one
     inbox); D (store a payer name per entry and "pick the right one": there
     is none when both are selected).
   - Consequence on the compose page in family mode: the "Payeur" column
     (it showed `parents.name`) is removed; the recipients table shows the
     email and the families concerned ("10 DUPONT, 20 MARTIN").

3. **Rendering is a hand-written, single-pass scanner** (new package
   `internal/message`). No `text/template`, no `strings.Replacer` (it cannot
   report unknown tokens).
   - Token: `{{`, then 1 to 40 characters other than `{`, `}`, `\n`, then
     `}}`. The inside is trimmed and compared case-insensitively:
     `{{ Parent_Name }}` = `{{parent_name}}` (forgiving for a non-technical
     user). An inside that is empty after trimming is `malformed`.
   - The text is scanned once, left to right; values are appended to the
     output and **never scanned again**, so a name containing `{{x}}` is
     sent literally and user data can never act as template code.
   - `{{` not followed by a valid token (no closing `}}`, a line break, a
     `{` or `}` inside, or more than 40 characters before `}}`), and a `}}`
     outside a token, are `malformed` (catches `{{parent_name}` typos).
     Single `{` or `}` are plain text. No escape syntax for a literal `{{`.

4. **Problems are blocking and always shown** (never send `{{xxx}}`). One
   list, each problem with a field (subject / body), a kind and, when it has
   one, the token as written (cut to 40 runes + "…"):
   - `unknown`: `{{parent_nam}}`.
   - `unavailable`: known, but not for this mode (decision 1). Never
     reported in `AnyMode` (templates).
   - `not_in_subject`: `{{children}}` in the subject (it is multi-line).
   - `malformed`: decision 3.
   - `empty`: subject or body empty after normalization (no token).
   - `too_long`: subject > 255 runes, body > 20,000 runes (user decision 6;
     no token; the message shows the limit).
   - `invalid_char`: invalid UTF-8, NUL or another control character
     (`\t` and `\n` allowed in the body; **any** control character, `\t` and
     `\n` included, refused in the subject). No token (never echo control
     characters).
   Order: subject problems first, then body; inside a field, order of
   appearance; each (field, kind, token) once. When the text is not valid
   UTF-8 or too long, only that problem is reported for that field (no
   token scan). The preview shows the problems instead of the rendered text
   when there are any; the future send step calls the same check and
   refuses.

5. **Text normalization** (`message.Normalize`, idempotent), applied before
   checks, storage and rendering: `\r\n` and lone `\r` → `\n`; subject
   trimmed of spaces; body: trailing whitespace at the very end removed,
   the rest kept as typed (leading spaces/indentation kept). `Check` and
   `Render` normalize their input themselves, so a caller cannot forget;
   the server also normalizes to redisplay and store the cleaned text.
   Header safety: the rendered subject and `ToName` go through
   `headerSafe` (any control character → space, spaces collapsed, trimmed)
   as defense in depth (the subject template already refuses control
   characters and names are cleaned at import). `Render` is plain text only
   and says so in its doc: an HTML body (later) must escape values at
   substitution time.

6. **The selection travels in the form, with a data version; any import in
   between forces re-selection** (user decision 3).
   - Every compose POST carries the hidden `child` IDs, a hidden `version`
     and the current subject/body. POST bodies only: no personal data in
     URLs. The existing `CrossOriginProtection` + `localhostOnly` cover
     every new POST. IDs are opaque integers.
   - `version` = last import ID (`COALESCE(MAX(imports.id), 0)`), the same
     "data version" `ImportFamilies` already uses as `Base`: data only
     changes through imports, and `imports` rows are never deleted, so the
     value only grows. `/children` renders it in its form.
   - `/compose` with a missing, non-integer or different `version` →
     **409**, the `/children` page with `compose.error.stale` ("the data
     changed since your selection, select again"). Why stricter than
     today's "N rows dropped" notice: SQLite reuses the highest rowid after
     a deletion (no `AUTOINCREMENT`), so after a re-import a stale ID can
     point to **another** child. The `Dropped` field and the
     `compose.dropped` key are removed.
   - With a matching version, an unknown ID can only come from a tampered
     form: ignored silently (as today's invalid IDs).
   - The version check and the load run in one read transaction
     (`store.SelectedChildren`), so an import cannot slip between them.
   - Rejected: an in-memory draft under a token like the pending import
     (lost on restart or after a TTL while the user writes; the form must
     post subject/body anyway). Rejected: a DB "draft batch" now (drafts to
     clean up, history must hide them). §4.4 creates the batch once, at
     confirmation, in one transaction that re-checks `version`.

7. **One compose route, several buttons.** `POST /compose` with `action`:
   - `""` or any unknown value (arriving from `/children`): page with an
     empty message (or the posted subject/body if any).
   - `load`: replace subject/body with the chosen template. The template
     `<select>` starts on an empty "choose" option and is reset to it on
     every response; `load` with the empty option is a silent no-op (text
     kept). Why: pressing Enter in a text field submits the form with its
     **first** submit button, which is `load` (the picker is at the top);
     this way Enter can never overwrite the text unless a template was
     chosen and not loaded. The page says next to the button that loading
     replaces the current text.
   - `preview`: check, then render for the chosen recipient.
   - `save_template`: save the current text as a new template.
   Plain HTML form, no JS, no htmx (htmx is not vendored yet); works in a
   Wails window. Updating an existing template from the compose page is out
   of scope (done on `/templates/{id}`).

8. **Templates** (migration 003, CRUD pages).
   - Table `templates (id, name, name_key UNIQUE, subject, body, created_at,
     updated_at)`. `name_key = textnorm.Key(name)`: names are unique
     ignoring case, accents and spaces ("Menu" = " ménu "), as elsewhere in
     the app. `name` stored `textnorm.Clean`ed.
   - Limits (user decision 6): name 1–80 runes after cleaning, valid UTF-8,
     no control character; subject and body as decision 4. All three
     required. Same message check as compose in `AnyMode` (unknown and
     malformed placeholders refused at save, so a saved template is always
     usable in at least one mode).
   - Pages: list, new, edit, delete with a confirmation page. Errors
     re-render the form with the typed values (422). Unknown ID → 404 with
     the list page and `templates.error.not_found`. After save/delete,
     redirect (303) with `?saved=1` / `?deleted=1` (no names in URLs).
   - Not affected by imports (SPEC §4.1).

9. **Data ready for §4.4.** `message.Render` returns a `message.Email` per
   recipient: parent ID, display name, address, final subject, final body,
   child IDs and child names (for `messages` / `message_children`, SPEC
   §7). §4.4 will post the same form to `/send` → re-check version and
   problems → "Send N emails?" → one transaction (version check, render
   all, insert batch + messages `pending`) → queue. The joining word comes
   from the request's locale (same browser, same `Accept-Language`).

## Changes

### `internal/store`

- **`migrations/003_templates.sql`** (new):

  ```sql
  -- Saved message templates (SPEC §4.3, §7). Not touched by imports.
  CREATE TABLE templates (
      id         INTEGER PRIMARY KEY,
      name       TEXT NOT NULL,          -- textnorm.Clean(name)
      name_key   TEXT NOT NULL UNIQUE,   -- textnorm.Key(name): unique ignoring case/accents/spaces
      subject    TEXT NOT NULL,
      body       TEXT NOT NULL,          -- plain text, "\n" line endings
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
  );
  ```

  Timestamps written with the store's existing `timestamp()` helper and
  format, like `imports.imported_at`.

- **`templates.go`** (new):

  ```go
  type Template struct {
      ID                   int64
      Name, Subject, Body  string
      CreatedAt, UpdatedAt time.Time
  }

  var (
      ErrNotFound  = errors.New("not found")
      ErrNameTaken = errors.New("template name already used")
  )

  // Templates returns all templates ordered by name_key, then id.
  func (s *Store) Templates(ctx context.Context) ([]Template, error)
  // Template returns one template, or ErrNotFound.
  func (s *Store) Template(ctx context.Context, id int64) (Template, error)
  // CreateTemplate stores a new template and returns its ID (ErrNameTaken).
  func (s *Store) CreateTemplate(ctx context.Context, name, subject, body string) (int64, error)
  // UpdateTemplate replaces a template (ErrNotFound, ErrNameTaken).
  func (s *Store) UpdateTemplate(ctx context.Context, id int64, name, subject, body string) error
  // DeleteTemplate removes a template (ErrNotFound).
  func (s *Store) DeleteTemplate(ctx context.Context, id int64) error
  ```

  - The store cleans the name and computes `name_key`; length and content
    limits are checked by the server (`message` + name validation).
  - Create/update run in one transaction: look up **another** row with the
    same `name_key` first → `ErrNameTaken` (no parsing of driver error
    strings); the UNIQUE index stays as a safety net. Updating a template
    with its own name (even re-cased) is fine. Inside the transaction, use
    only `tx` (single-connection pool).
  - Check `ErrNotFound` with `RowsAffected` for update/delete.

- **`children.go`**:
  - `var ErrStaleSelection = errors.New("data changed since the selection")`.
  - Unexported `type querier interface { QueryContext(...); QueryRowContext(...) }`
    (satisfied by `*sql.DB` and `*sql.Tx`). `queryChildren` takes a
    `querier` instead of always using `s.db`; the `IN (?,?,…)` builder of
    `ChildrenByIDs` moves to a small helper reused by `SelectedChildren`.
  - Unexported `lastImportID(ctx, q querier) (int64, error)`:
    `SELECT COALESCE(MAX(id), 0) FROM imports`. Used by `DataVersion`,
    `SelectedChildren` **and** `ImportFamilies` (replaces its inline query;
    one copy only).
  - `DataVersion(ctx) (int64, error)`: the current data version.
  - `SelectedChildren(ctx, version int64, ids []int64) ([]Child, error)`:
    one read transaction: `lastImportID` ≠ `version` → `ErrStaleSelection`;
    otherwise the same rows and order as `ChildrenByIDs`. Empty `ids` →
    still checks the version, then returns an empty slice.
  - `ChildrenByIDs` stays (used by tests and maybe §4.4) or is removed if
    nothing uses it any more; goexp checks references with gopls.

### `internal/recipients`

- `Recipient` gains `ChildIDs []int64` and `FamilyNumbers []string`,
  parallel to `Children` (same order and length; doc comment says so;
  `FamilyNumbers` entries are `""` for children data). Filled in `Group`.
  Nothing else changes.

### `internal/message` (new; no DB, no HTTP)

Package doc: renders a plain-text email for one recipient from a subject
and body with placeholders (SPEC §4.3). API (goexp may adjust names, not
behavior):

```go
type Mode int

const (
    AnyMode    Mode = iota // template save: every known placeholder accepted
    ChildMode              // Excel children data
    FamilyMode             // CSV family data
)

const (
    ParentName   = "parent_name"
    ChildName    = "child_name"
    Children     = "children"
    FamilyNumber = "family_number"
)

const (
    MaxSubjectLen = 255   // runes
    MaxBodyLen    = 20000 // runes
)

// Placeholders returns the placeholder names usable in m, in display order
// (parent_name, child_name, children, family_number; all four for AnyMode).
func Placeholders(m Mode) []string

// ModeOf returns FamilyMode if any child has a family number, else ChildMode.
func ModeOf(children []store.Child) Mode
```

(`ModeOf` may instead take `[]recipients.Recipient`; one place decides the
mode, the server uses it.)

```go
type Field string // FieldSubject = "subject", FieldBody = "body"
type Kind string  // KindUnknown "unknown", KindUnavailable "unavailable",
                  // KindNotInSubject "not_in_subject", KindMalformed "malformed",
                  // KindEmpty "empty", KindTooLong "too_long", KindInvalidChar "invalid_char"

type Problem struct {
    Field Field
    Kind  Kind
    Token string // as written, cut to 40 runes; "" for empty/too_long/invalid_char
}

// Normalize applies decision 5 to the typed subject and body. Idempotent.
func Normalize(subject, body string) (string, string)

// Check normalizes subject and body and returns every problem for mode m
// (decision 4 order), or nil.
func Check(subject, body string, m Mode) []Problem

// Email is one rendered email, ready for the sender (SPEC §4.4, §5.3).
type Email struct {
    ParentID   int64
    ToName     string // value of {{parent_name}}, header-safe
    To         string
    Subject    string // one line, header-safe
    Body       string // plain text, "\n" line endings
    ChildIDs   []int64
    ChildNames []string // family mode: billing names of the entries
}

// ErrProblems is returned by Render when Check reports problems.
var ErrProblems = errors.New("message has problems")

// Render renders subject and body for r in mode m (ChildMode or FamilyMode;
// AnyMode is an error). and is the joining word of the interface language
// ("et" / "and"). Plain text only: values are not escaped.
func Render(subject, body string, r recipients.Recipient, m Mode, and string) (Email, error)
```

Unexported: the scanner (decision 3) shared by `Check` and `Render`;
`join(items []string, and string) string`; `values(r, m, and)
map[string]string`; `headerSafe(string) string`; dedup helpers (decision 1).
`ToName` is always the `{{parent_name}}` value, even if the message does not
use it.

### `internal/server`

- **`compose.go`** (new; move `handleCompose`, `composeData`, `parseIDs`,
  `maxComposeBytes`, `maxSelectedChildren` out of `children.go`):
  - `composeData`:
    ```go
    type composeData struct {
        Version      int64
        IDs          []int64
        Families     bool
        Recipients   []recipients.Recipient
        Subject      string
        Body         string
        Templates    []store.Template // picker
        Placeholders []placeholderView // name + localized description
        PreviewTo    int64             // parent ID selected in the preview picker
        Preview      *message.Email
        Problems     []problemView     // localized lines
        TemplateName string            // "save as template" input
        Error        string
        Notice       string
        MaxSubject   int
        MaxBody      int
        MaxName      int
    }
    ```
  - Steps of `handleCompose`:
    1. `MaxBytesReader` 1 MB (unchanged; enough for 5,000 IDs and a
       20,000-rune body) → `ParseForm`; errors as today.
    2. `parseIDs(child)`; > 5,000 → 422 `children.error.too_many` (as
       today); **no IDs → 422 `children.error.none_selected`** (as today,
       before the version check, so "nothing ticked" keeps its message).
    3. `version`: missing or not a non-negative integer → stale (step 4).
    4. `SelectedChildren(version, ids)`; `ErrStaleSelection` → 409,
       `/children` page with `compose.error.stale`. Empty result (only
       unknown IDs) → 422 `none_selected`. An entry without email → 422
       `children.error.no_email` + the existing warning log with the ID
       (unchanged).
    5. Mode (`message.ModeOf`), `recipients.Group`, `message.Normalize` of
       the posted `subject` / `body`, `Templates` for the picker.
    6. `action`:
       - `load`: `template` empty or not a positive integer → no-op.
         Otherwise `Template(id)`; `ErrNotFound` → page error
         `templates.error.not_found` (text kept, 200); found → subject/body
         replaced, notice `compose.loaded`.
       - `preview`: `Check(mode)`; problems → shown, no rendered text;
         none → `Render` for the recipient whose `ParentID` = `preview_to`
         (first recipient if missing/unknown). Status 200 either way.
       - `save_template`: `validateTemplate(name, subject, body)` (below);
         failure → 422 with name/subject/body kept and the errors shown;
         `ErrNameTaken` → 422 `templates.error.name_taken`; success → 200,
         notice `compose.saved` with the cleaned name, picker refreshed.
       - anything else → plain page.
  - Logging: only IDs, counts, action names. Never names, emails or text.
- **`templates.go`** (new): handlers for the routes below.
  - `MaxBytesReader` 1 MB on POSTs (too large → 422 form error).
  - `const maxTemplateNameLen = 80`.
  - `validateTemplate(loc, name, subject, body) (cleanName string, errs
    []string, problems []problemView)` (or similar): **one** function used
    by both the template form and compose's `save_template`. Name rules:
    valid UTF-8, no control character (`templates.error.name_invalid`),
    non-empty after `textnorm.Clean` (`name_required`), ≤ 80 runes after
    cleaning (`name_too_long`); then `message.Check(AnyMode)`.
  - `problemView` and the localization of problems (`problem.*` keys,
    field label from `compose.subject` / `compose.body`, limit for
    `too_long`) live in one helper shared with compose.
  - `parseTemplateID(r)`: `{id}` must be a positive integer, else 404.
- **`children.go`**: `childrenData.Version` from `DataVersion`, read
  **before** `Children` (an import in between can only make the version
  look stale, never the reverse). `renderChildren` does this for every
  status (also the 409/422 re-renders).
- **`server.go`**: parse the new pages `templates`, `template_form`,
  `template_delete`; routes:

  ```
  GET  /templates                 list (+ ?saved=1, ?deleted=1 notices)
  GET  /templates/new             empty form
  POST /templates                 create → 303 /templates?saved=1
  GET  /templates/{id}            edit form
  POST /templates/{id}            update → 303 /templates?saved=1
  GET  /templates/{id}/delete     confirmation page
  POST /templates/{id}/delete     delete → 303 /templates?deleted=1
  ```

  Active nav item for these pages: `templates`. The compose page keeps
  `children` as active item.

### `web/templates`

- `children.html`: hidden `<input type="hidden" name="version">` in the
  selection form. Keep the uncommitted top "Continuer" button (working tree
  change, `children.continue`).
- `compose.html` (rewritten), one `<form method="post" action="/compose">`:
  1. Title, error/notice alerts, top "back to the list" link (keep the
     uncommitted `actions-top` link).
  2. Hidden `version` and one hidden `child` input per ID.
  3. Recipients summary (`compose.emails_count` /
     `compose.emails_count_families`) and the recipients table inside
     `<details>` (summary `compose.recipients_show`). Child mode: parent,
     email, children. Family mode: email, families as "number name"
     joined by ", " (no payer column).
  4. Template picker: `<select name="template">` with an empty first option
     (`compose.template_choose`) + button `action=load`
     (`compose.template_load`) + note `compose.template_load_note`. If
     there are no templates: `compose.no_templates` instead. Always a link
     `compose.manage_templates` → `/templates`.
  5. Subject `<input type="text" name="subject" maxlength=MaxSubject>`,
     body `<textarea name="body" maxlength=MaxBody rows=14>`.
  6. Placeholders for the mode: `{{name}}` in `<code>` + description
     (`compose.placeholders_title`, `compose.placeholders_help`).
  7. Preview: `<select name="preview_to">` (one option per recipient:
     email, then names) + button `action=preview`. Then either the problems
     list (`problem.title`) or the rendered email: "To" (name + address),
     subject, body in a `white-space: pre-wrap` box.
  8. Save as template: name input `template_name` (maxlength MaxName) +
     button `action=save_template`.
  9. Note `compose.next_step` (where the §4.4 "Send" button will go) and
     the bottom back link.
  All values through `html/template` (no `template.HTML`).
- `templates.html`: notices, "new template" button, table (name, subject,
  last updated via `$L.Date`, edit / delete links), empty state.
- `template_form.html`: one form for new and edit (`action` = `/templates`
  or `/templates/{id}`): name, subject, body (same maxlengths), errors and
  problems list, placeholder help listing all four placeholders with their
  description and where they work (`placeholder.only_children` /
  `placeholder.only_families`), save + cancel.
- `template_delete.html`: name + subject, `templates.delete_confirm`,
  danger button (POST), cancel link.
- `layout.html`: nav item `nav.templates` → `/templates`, active
  `templates`.

### `web/static/style.css`

Textarea and text input width, preview box (`pre-wrap`, border, padding),
problems list, `details`/`summary`, `code` for placeholders. No JS.

### `web/locales` (both files, same keys)

Remove: `compose.dropped`, `compose.col.payer`.

Change:

| Key | fr | en |
|---|---|---|
| `compose.title` | Écrire le message | Write the message |
| `compose.next_step` | L'envoi sera ajouté à l'étape suivante. | Sending will be added in the next step. |

Add (goexp may add a few more following the same pattern, in both files):

| Key | fr | en |
|---|---|---|
| `nav.templates` | Modèles | Templates |
| `message.join_and` | et | and |
| `compose.recipients_show` | Voir les destinataires | Show recipients |
| `compose.subject` | Objet | Subject |
| `compose.body` | Message | Message |
| `compose.template_label` | Partir d'un modèle | Start from a template |
| `compose.template_choose` | — Choisir un modèle — | — Choose a template — |
| `compose.template_load` | Utiliser ce modèle | Use this template |
| `compose.template_load_note` | Remplace l'objet et le message actuels. | Replaces the current subject and message. |
| `compose.no_templates` | Aucun modèle enregistré. | No saved templates. |
| `compose.manage_templates` | Gérer les modèles | Manage templates |
| `compose.loaded` | Modèle « %s » chargé. | Template “%s” loaded. |
| `compose.placeholders_title` | Champs automatiques | Placeholders |
| `compose.placeholders_help` | Écrivez-les tels quels, accolades comprises : ils sont remplacés pour chaque destinataire. | Type them as shown, braces included: they are replaced for each recipient. |
| `compose.preview_title` | Aperçu | Preview |
| `compose.preview_for` | Aperçu pour | Preview for |
| `compose.preview_button` | Afficher l'aperçu | Show preview |
| `compose.preview_to` | À | To |
| `compose.save_title` | Enregistrer comme modèle | Save as a template |
| `compose.save_button` | Enregistrer le modèle | Save template |
| `compose.saved` | Modèle « %s » enregistré. | Template “%s” saved. |
| `compose.error.stale` | Les données ont changé depuis votre sélection (un import a eu lieu). Sélectionnez à nouveau. | The data changed since your selection (an import took place). Please select again. |
| `placeholder.parent_name` | Nom du parent | Parent name |
| `placeholder.parent_name_families` | Nom de facturation des familles sélectionnées pour cette adresse (ex. « SILVA Ana et SILVA Rui ») | Billing name of the selected families for this address (e.g. “SILVA Ana and SILVA Rui”) |
| `placeholder.parent_name_any` | Nom du parent ; données familles : nom de facturation des familles sélectionnées | Parent name; family data: billing name of the selected families |
| `placeholder.child_name` | Noms des enfants sélectionnés, sur une ligne (ex. « Léa et Tom ») | Names of the selected children, on one line (e.g. “Léa and Tom”) |
| `placeholder.children` | Liste des enfants sélectionnés, un par ligne commençant par « - » (sur une ligne seule, pas dans l'objet) | List of the selected children, one per line starting with “- ” (on its own line, not in the subject) |
| `placeholder.family_number` | Numéros des familles sélectionnées pour cette adresse (ex. « 1234 et 1250 ») | Numbers of the selected families for this address (e.g. “1234 and 1250”) |
| `placeholder.only_children` | données enfants (Excel) uniquement | children data (Excel) only |
| `placeholder.only_families` | données familles (CSV) uniquement | family data (CSV) only |
| `problem.title` | À corriger | To fix |
| `problem.line` | %s : %s | %s: %s |
| `problem.unknown` | « %s » n'est pas un champ automatique connu. | “%s” is not a known placeholder. |
| `problem.unavailable` | « %s » n'est pas disponible avec les données importées. | “%s” is not available with the imported data. |
| `problem.not_in_subject` | « %s » ne peut pas être dans l'objet (il fait plusieurs lignes). | “%s” cannot be used in the subject (it spans several lines). |
| `problem.malformed` | « %s » est mal écrit : un champ s'écrit entre {{ et }}. | “%s” is badly written: a placeholder goes between {{ and }}. |
| `problem.empty` | Ce champ est obligatoire. | This field is required. |
| `problem.too_long` | Trop long : %d caractères au maximum. | Too long: %d characters at most. |
| `problem.invalid_char` | Contient un caractère non autorisé (saut de ligne dans l'objet, ou caractère invisible). | Contains a character that is not allowed (line break in the subject, or an invisible character). |
| `templates.title` | Modèles de message | Message templates |
| `templates.new` | Nouveau modèle | New template |
| `templates.empty` | Aucun modèle pour l'instant. | No templates yet. |
| `templates.col.name` | Nom | Name |
| `templates.col.updated` | Modifié le | Last updated |
| `templates.edit` | Modifier | Edit |
| `templates.delete` | Supprimer | Delete |
| `templates.edit_title` | Modifier le modèle | Edit template |
| `templates.name` | Nom du modèle | Template name |
| `templates.save` | Enregistrer | Save |
| `templates.cancel` | Annuler | Cancel |
| `templates.saved` | Modèle enregistré. | Template saved. |
| `templates.deleted` | Modèle supprimé. | Template deleted. |
| `templates.delete_title` | Supprimer le modèle | Delete template |
| `templates.delete_confirm` | Supprimer définitivement le modèle « %s » ? Les emails déjà envoyés ne changent pas. | Delete the template “%s” for good? Emails already sent are not affected. |
| `templates.error.name_required` | Le nom du modèle est obligatoire. | The template name is required. |
| `templates.error.name_too_long` | Le nom du modèle fait au plus %d caractères. | The template name is at most %d characters long. |
| `templates.error.name_invalid` | Le nom du modèle contient un caractère non autorisé. | The template name contains a character that is not allowed. |
| `templates.error.name_taken` | Un modèle porte déjà ce nom. | A template with this name already exists. |
| `templates.error.not_found` | Ce modèle n'existe pas (ou plus). | This template does not exist (any more). |

Field labels in problems and on the template form reuse `compose.subject`
/ `compose.body`; the template list subject column reuses
`compose.subject`.

## Test plan

**`message`**
- Scanner: known token replaced; `{{ Parent_Name }}` accepted; text without
  token unchanged; adjacent tokens; token at start and end; single `{` and
  `}` kept as text.
- Single pass: a value containing `{{child_name}}` is output literally.
- Problems: `{{parent_nam}}` → unknown; `{{child_name}}` / `{{children}}`
  in FamilyMode and `{{family_number}}` in ChildMode → unavailable, none of
  them in AnyMode; `{{children}}` in subject → not_in_subject (also in
  AnyMode); `{{parent_name}`, `parent_name}}`, `{{` alone, `{{}}`,
  `{{  }}`, `{{a\nb}}`, `{{a{b}}`, 41-char inside → malformed; 40-char
  inside is a token (unknown, not malformed); empty / spaces-only subject
  and body → empty; subject 255 runes ok, 256 → too_long; body 20,000 ok,
  20,001 → too_long (runes: "é" counts one); CR, LF, `\t`, NUL in subject
  and NUL / other control char in body → invalid_char; `\t` and `\n` in
  body fine; invalid UTF-8 in each field; same problem twice reported once;
  subject problems before body problems; long token cut to 40 runes.
- `Normalize`: CRLF and lone CR → LF; subject trimmed; body trailing
  whitespace removed, leading indentation kept; idempotent.
- Joining FR and EN: 1, 2, 3 and 4 items.
- Values, ChildMode: `parent_name` = parent name; `child_name` "Léa et Tom";
  `children` = `"- Léa\n- Tom"` (one child: `"- Léa"`).
- Values, FamilyMode: one selected entry of a shared address → its own
  billing name (SILVA Rui case, even if `ParentName` is "SILVA Ana"); both
  → "SILVA Ana et SILVA Rui"; same name with other case/spaces → once;
  `family_number` joined and deduplicated.
- `Render`: `ErrProblems` when Check fails; AnyMode → error; `Email`
  fields (ParentID, To, ChildIDs, ChildNames, ToName); `ToName` and
  `Subject` header-safe when a value holds a control character; body keeps
  `\n`.
- `Placeholders` per mode; `ModeOf`.

**`recipients`**
- `ChildIDs` and `FamilyNumbers` parallel to `Children`, input order, for
  children and family data; existing tests still pass.

**`store`**
- Migration 003 applies on a DB that already has data (children kept);
  fresh DB has the table.
- Templates CRUD round trip (name cleaned, timestamps set, `updated_at`
  changes on update); list ordered by key ("b", "Á", "c" → "Á", "b", "c").
- `ErrNameTaken` on create and update for "Menu" vs " menu " vs "Ménu";
  update keeping its own name (or re-casing it) is fine.
- `ErrNotFound` on get / update / delete of a missing ID.
- An import (Excel and CSV) leaves templates untouched.
- `DataVersion`: 0 on an empty DB, then the import ID after each import;
  a preview (rolled back) does not change it.
- `SelectedChildren`: current version → same rows as before; older version
  → `ErrStaleSelection`; empty IDs with right/wrong version; `ImportFamilies`
  stale check still works (shared `lastImportID`).

**`server`**
- `/children` form carries `name="version"` with the current version (and
  on 409/422 re-renders).
- `/compose` stale cases (409, children page, `compose.error.stale`, FR and
  EN): version missing, not a number, older than an import done after the
  list.
- `/compose` valid: recipients listed; placeholders for the mode; family
  mode shows no "Payeur"/"Payer" column, no child placeholders, families as
  "10 DUPONT, 20 MARTIN"; hidden `version` and `child` inputs present.
- `action=preview`: renders for the chosen `preview_to`; first recipient by
  default and for an unknown `preview_to`; unknown placeholder → problem
  shown and no rendered body; rendered text HTML-escaped (`<b>` in body
  shown as text); English page joins with "and"; SILVA case through HTTP.
- `action=load`: subject/body replaced and notice; empty `template` →
  text kept, no error; missing template → error, text kept.
- `action=save_template`: created (visible in the picker and on
  `/templates`); duplicate name → 422 with name and text kept; unknown
  placeholder → 422; empty / 81-rune / control-char name → 422.
- Templates pages: empty state; create, edit, delete flows with 303
  redirects and notices; validation errors 422 with values kept; 404 for
  unknown and non-numeric IDs (`/templates/abc`, `/templates/0`); cross-site
  POST → 403 on `POST /templates`, `/templates/{id}`,
  `/templates/{id}/delete`; oversized body rejected; nav item active.
- Logs: no template text, names or emails in logs of compose/templates.

**Existing tests to update because of decision 6** (`internal/server/children_test.go`):
- Helpers `form` / `postCompose` / `manyIDs`: every compose POST must carry
  the current `version` (e.g. a helper that reads `st.DataVersion`).
- `TestCompose/deleted ids reported as dropped` → becomes "import after the
  selection is stale": 409, `compose.error.stale` text, nothing listed.
- `TestCompose/dropped notice in english` → stale message in English.
- `TestCompose/groups by parent` and `invalid non-positive and duplicate
  ids ignored`: drop the "existe(nt) plus" assertions.
- `TestCompose/nothing valid gives 422`: cases with IDs need the version to
  still reach `none_selected`; the no-ID cases are unchanged (422 before
  the version check).
- `TestCompose/exactly max ids accepted`, `too many ids*`: add the version.
- `TestComposeFamilies`: no "Payeur" / "Payer"; expect the families as
  "10 DUPONT, 20 MARTIN" (number + name).
- Locale files keep identical keys (existing test).

## Out of scope

- Sending, confirmation, `batches` / `messages` tables, history (§4.4,
  §4.5).
- HTML/rich text, attachments, French placeholder aliases, literal `{{`.
- Updating a template from the compose page; duplicating templates.
- Keeping the selection or the text when going back to `/children`.
- Live (htmx/JS) preview; inserting placeholders by clicking.

## Implementation notes

Files changed: `internal/store/migrations/003_templates.sql`,
`store/templates.go` (new), `store/children.go` (`querier`, `DataVersion`,
`SelectedChildren`, `ErrStaleSelection`, shared `lastImportID`),
`store/families.go` (uses `lastImportID`), `recipients/recipients.go`
(`ChildIDs`, `FamilyNumbers`), `internal/message/message.go` (new),
`server/compose.go` (new), `server/templates.go` (new), `server/children.go`,
`server/server.go`, templates `compose`, `children`, `layout`, `templates`,
`template_form`, `template_delete`, `style.css`, both locale files.

Deviations (small):
- `message.ModeOf` takes `[]recipients.Recipient` (plan allowed it).
- `composeData.Error` is `Errors []string`; `problemView` is a plain
  `[]string` of localized lines (`problemLines`).
- `ChildrenByIDs` kept (existing tests use it); it shares the helper with
  `SelectedChildren`.
- A control character in a field gives `invalid_char` and the token scan
  still runs; only empty / invalid UTF-8 / too long stop at one problem.
- A malformed token reports the text from `{{` to the point of failure
  (`{{parent_name}` includes the lone `}`); `{{` + 41+ valid chars + `}}` is
  one malformed problem. A stray `}}` is reported as token `}}`.
- Subject normalization trims only U+0020 (a trailing line break therefore
  gives `invalid_char`).

Tester notes: `message.scan` is shared by `Check` and `Render`; slog Debug
line in compose logs only action and counts. Existing tests failing because
of intended changes (all in `internal/server/children_test.go`, all because
the POST has no `version` -> 409): TestCompose (groups by parent, invalid
non-positive and duplicate ids, deleted ids reported as dropped, dropped
notice in english, nothing valid gives 422 with ids),
TestComposeRefusesEntryWithoutEmail (all subtests, log check), TestComposeFamilies.

### Fix round 1

- Compose form: a hidden default `preview` submit button (class
  `visually-hidden`) is now the first submit button, so Enter in a text field
  no longer triggers "load". The empty-option no-op for load is kept.
- `message.cutToken` maps control characters and invalid UTF-8 to `?` before
  the 40-rune cut (unknown, unavailable, malformed and stray `}}` problems).
- `<textarea>` in `compose.html` and `template_form.html`: a newline follows
  the opening tag so a leading line break survives a round trip.
- `template_delete.html`: the subject label uses the new locale key
  `common.label_value` (`%s : %s` in fr, `%s: %s` in en).

### Notes for §4.4

- Check once, then render many: `Render` re-runs `Check` per recipient.
- Build the To header with `net/mail.Address.String()` or Graph's structured
  name/address, never string concatenation (`ToName` may contain quotes,
  `<`, `>`, commas from NOMFACT).
- Cap the rendered subject length at send time.
- Bump the Go toolchain for GO-2026-6859 (`net/http` MaxBytesReader header
  race).

## Follow-up changes, 2026-11-16

1. **Child placeholders removed (for now).** The CSV export has no child
   names, so `{{child_name}}` and `{{children}}` are gone in every mode:
   `message.Placeholders` no longer lists them, `Check` reports them as
   unknown, `Render` no longer substitutes them. Removed with them: the
   `KindNotInSubject` problem, the `placeholder.child_name`,
   `placeholder.children`, `placeholder.only_children` and
   `problem.not_in_subject` locale keys. `{{parent_name}}` (incl. the shared
   address greeting) and `{{family_number}}` are unchanged. The Excel import
   still works. SPEC §4.3 and §6 updated.
2. **Last selection remembered.** Migration `004_last_selection.sql`
   (`last_selection(child_id)` with `ON DELETE CASCADE`, foreign keys are on
   in the DSN). `store.SaveSelection` replaces the saved set in one
   transaction (only existing entries with a parent);
   `store.SavedSelection` reads it. `handleCompose` saves after the
   version check, `SelectedChildren` and the no-email refusal passed (a save
   error is logged and does not block). `/children` pre-checks the saved
   rows that can be emailed. SPEC §4.2 and §7 updated.
