# 002 — CSV import (family export)

Status: done (2026-11-16) · reviewed: approved after 1 fix round
(source of truth: "Decisions" and "Assumed"), §4.1, §4.2, §4.4, §7

## Goal

The user imports the `;`-separated CSV export of the families software
through the same page, preview and confirmation as the Excel file (SPEC
§4.1, §4.1.1). Each import, CSV or Excel, replaces all stored data (file =
source of truth, one transaction, saved only on confirmation, email history
untouched).

From the CSV the app keeps, per **entry**: family number (`NUMFAM`), billing
name (`NOMFACT`) and payer email (`EMAIL PAYEUR`). An entry is either one
(`NUMFAM`, valid payer email) pair, or the single "no valid email" entry of a
family (§4.1.1 decisions 3–4). Nothing else is read: no phones, no RL1/RL2
names or emails, no `NUMFACFACT`.

The selection page (§4.2) then lists entries instead of children. Entries
without a valid email are shown with a warning and cannot be selected; the
server refuses them too. The existing grouping (§4.4) sends one email per
payer address, mentioning every selected family that shares it.

Header of the real export (0-based index; empty header cells are ignored):

| Index | Header | Used |
|---|---|---|
| 0 | NUMFAM | yes: family number |
| 1 | NUMFACFACT | no |
| 2 | NOMFACT | yes: billing name |
| 13–22 | NOM/PRENOM/PORT/EMAIL RL1, RL2 | **no** (never mapped) |
| 26 | PORT PAYEUR | **no** |
| 27 | EMAIL PAYEUR | yes: the only recipient address |
| others | (empty) | ignored |

## Decisions

1. **Reuse the existing tables: payer email = `parents` row, entry =
   `children` row** with new columns `family_number` and `invalid_email`
   (migration 002).
   - `parents`: unchanged. One row per distinct **valid** email (still
     `NOT NULL UNIQUE`, lower-case); name = `NOMFACT` of the first entry with
     that email.
   - `children`: one row per entry; `name` = the entry's `NOMFACT`,
     `family_number` = `NUMFAM`. Excel-imported children have
     `family_number = ''`.
   - Why: `store.ImportFamilies` (matching, dry run, stale check, counts,
     deletion list), `Children`/`ChildrenByIDs`, `recipients.Group`,
     `/children`, `/compose`, the pending import and the import history all
     keep working with a few extra fields. "One email per address" (§4.1.1
     decision 6) comes for free from `recipients.Group` on `ParentID`.
   - Rejected: a separate `families` table (second replace-all import,
     second list query, second grouping and selection page: duplicated
     code; cross-table wiping between formats).
   - Cost accepted: in code, a family entry is a `store.Child`. The UI shows
     family wording whenever the data is family data (decision 10), and the
     type doc comments say so.

2. **Entries without a valid payer email: `children.parent_id` becomes
   nullable.** Such an entry has `parent_id = NULL`, no `parents` row, and
   keeps the rejected value in `children.invalid_email` (`''` when the cell
   was empty).
   - Why: least disruption. `parents` keeps its meaning and its
     `email NOT NULL UNIQUE` invariant (every parent is reachable), so
     `recipients.Group`, the counter and the future sending step only ever
     see real addresses. Only `children` changes, and migration 002 rebuilds
     it anyway (decision 4).
   - Rejected: nullable `parents.email` or an `email_status` on `parents`.
     Parents are identified by email; a parent without one would need another
     identity (the family number), two such families would need two
     "parents", and every reader of `parents` would have to filter them.
   - Rejected: a separate table for families without email (second query and
     a union on the selection page).
   - **The invalid value is stored and shown** (recommended): the selection
     page says "Email du payeur invalide : jean.dupont@gmailcom", so the user
     can find and fix the source file. It is the same kind of personal data
     as a valid email already stored, never logged, and replaced or deleted
     by the next import. Stored cleaned as `normalizeEmail` does (trim,
     lower-case, `mailto:` removed), cut to 254 characters (RFC 5321 address
     limit) with a final `…` if longer, so a broken cell cannot bloat pages.
     Alternative rejected: store only a "missing/invalid" flag (the user
     would have to hunt for the line in the file).

3. **Matching keys** (stable IDs across re-imports):
   - Parent: email (unchanged).
   - Excel child: (parent, `name_key`) (unchanged).
   - Family entry with a valid email: (parent, `family_number`). A changed
     `NOMFACT` is an *update* (name and `name_key`), not delete + add.
   - Family entry without a valid email: (`family_number`) among entries
     with `parent_id IS NULL`. A changed `NOMFACT` or a changed invalid value
     is an *update*.
   - A family that moves between "valid email X", "valid email Y" and "no
     valid email" changes key: one entry deleted, one added (an entry is the
     pair, SPEC §4.1.1).

4. **Migration 002 rebuilds `children`** (SQLite cannot drop a table
   constraint nor make a column nullable): new table, copy rows, drop old,
   rename, partial unique indexes (SQL below). Safe because no table
   references `children` yet; it must ship before §4.5 adds
   `message_children`. **`parents` is not rebuilt**: with
   `foreign_keys(1)` (set in the DSN, cannot be changed inside the migration
   transaction) dropping `parents` would cascade-delete every child.
   Migration 001 is not edited.

5. **One importer, two readers, layout chosen from the header.**
   - Readers produce `record{Line int; Cells []string}`: `.xlsx` via
     excelize (line = sheet row), `.csv` via `encoding/csv` (line = file
     line where the record starts, from `csv.Reader.FieldPos(0)`, so it
     matches a text editor even after blank lines or multi-line quoted
     fields).
   - One `parseRecords` maps headers with `textnorm.Key` (empty and unknown
     headers ignored, positions irrelevant, first duplicate wins) and picks
     the layout: **family layout** if the header contains any of `numfam`,
     `nomfact`, `email payeur`; otherwise the **child layout** (SPEC §4.1
     table, unchanged). Missing columns are reported for the chosen layout.
   - So the export saved as `.xlsx` imports like the CSV, and a
     `nom parent;nom enfant;email` CSV imports like the Excel file.
     Rejected: a separate CSV importer (duplicated validation).

6. **Family layout rules.** Pass 1, per line in file order:
   1. `NUMFAM` empty → blocking `missing_family_number`.
   2. `NOMFACT` empty → blocking `missing_billing_name`.
      A line with a blocking issue contributes nothing else.
   3. Payer email, normalized as today (`normalizeEmail`), is classified:
      - valid (`validEmail`) → belongs to the entry (`NUMFAM`, email);
      - empty → warning `missing_payer_email` (value = `NUMFAM`);
      - invalid (anything else, including `-` or `néant`) → warning
        `invalid_payer_email` (value = the cleaned, cut value).

   Pass 2, per family (`NUMFAM`, exact string after `textnorm.Clean`):
   - One entry per distinct valid email.
   - **Plus one "no valid email" entry** if the family has at least one
     invalid line, or if it has no valid email at all (only empty lines).
     Its `InvalidEmail` is the first invalid value (`''` if only empty
     lines). So:
     - an empty cell in a family that has a valid email on another line
       only produces the warning (an empty cell names no other payer);
     - an invalid value is treated as another payer who cannot be reached:
       its own unselectable row next to the family's valid rows (consistent
       with "two payer emails → two rows", §4.1.1 decision 1).
   - The lines **making up an entry** are: for a valid entry, the lines with
     that email; for the no-email entry, the family's invalid lines if any,
     otherwise its empty lines. The entry's `Line` and `NOMFACT` come from
     the first of them. A later line of the same entry with another
     `NOMFACT` (by `textnorm.Key`) → warning `billing_name_conflict`
     (value = that name), first kept. Several lines per entry is the normal
     case: no `duplicate_row` warning in this layout.
   - Same valid email in **another** family with another `NOMFACT` →
     existing `parent_name_conflict` warning (parent name = first one; each
     entry keeps its own `NOMFACT`).
   - `Result.Rows` is ordered by `Line`.
   - None of these warnings blocks: a file without blocking issues is
     importable (§4.1.1 decisions 3–4).

7. **Import diff counts** (preview and history use the existing counters):
   - Every entry, with or without a valid email, counts in the `Children`
     counters (shown as "Familles (par adresse)" in family mode).
   - Entries without a valid email never create, update or keep a parent:
     `Parents` counters only count valid email addresses.
   - Updated = an existing entry whose `NOMFACT` or stored invalid value
     changed; otherwise Unchanged.
   - Moving between keys (decision 3) = 1 deleted + 1 added; a parent left
     with no entry is deleted, as today.
   - `RowsRead` = non-empty data lines, as today. No new history columns.

8. **Excel layout rules stay unchanged** (SPEC §4.1): missing or invalid
   email still blocks. Why: in that layout the email is the only identity of
   a parent and there is no family number to key an unreachable entry on;
   the user's decisions are about the CSV export; and the Excel file is made
   by hand, so fixing it is easy. Noted in SPEC §4.1.

9. **Selection and sending.**
   - A family with two payer emails = two rows (plus a third, unselectable,
     if it also has an invalid value), **adjacent**: family entries sort by
     the smallest `name_key` of their family, then family number, then
     `name_key`, then valid emails before the no-email entry, then email,
     then ID. So rows stay together even when the two payers have different
     `NOMFACT`. Excel children keep today's order (name, email).
   - Rows without a valid email: checkbox `disabled` and **without a
     `name`** (never submitted), row marked with a warning text (missing or
     invalid with the value); "select all" skips them; a notice above the
     list gives their count.
   - `POST /compose`: if any posted ID is an entry without a valid email,
     the **whole request is refused** (422, list shown again with an
     error). Only a tampered form can post one (such IDs are never rendered
     as submittable, and a family that loses its email gets a new ID).
     Log a warning with the ID only.
   - `recipients.Group` also skips children with `ParentID == 0`, so the
     future sending step can never address an empty email (defense in
     depth, one `continue`).
   - Two families sharing a payer email, both selected → one recipient
     listing both (§4.1.1 decision 6), already done by `Group`.

10. **Display mode.** After a confirmed import the data is either all
    children or all family entries (each import replaces everything).
    Pages switch labels with a `Families bool`:
    - `/children`: `Families` = any row has a family number. Columns:
      select, family number, billing name, payer email (no parent column:
      it repeats the name).
    - `/compose`: same flag from the selected rows; column "Familles
      concernées", "Payeur" instead of "Parent".
    - home: `store.Totals.Families` (distinct family numbers); when > 0 the
      stats show "Familles" and "Adresses email", and the button "Choisir
      des familles".
    - preview: `importer.Result.Layout`; family labels for the counts table,
      "entries to save" instead of "valid rows", a "without valid email"
      stat, family number column in the deletion list. The one-time switch
      from Excel data shows deleted children under family labels with an
      empty number: accepted.
    - nav and import history headers: neutral wording, no mode.

11. **CSV decoding** (no new dependency: `encoding/csv` and
    `golang.org/x/text/encoding/charmap`, `golang.org/x/text` already in
    `go.mod`):
    - Read at most `maxCSVBytes` (20 MB, same as the upload cap) through
      `io.LimitReader(r, maxCSVBytes+1)`; more → `ErrTooLarge`.
    - UTF-8 BOM → stripped; valid UTF-8 (`utf8.Valid`) → as is; otherwise
      decoded as Windows-1252 (§4.1.1 decision 5). Any NUL byte →
      `ErrUnreadable` (UTF-16 or a binary file renamed `.csv`).
    - Delimiter: `;` or `,`, whichever occurs more on the first non-empty
      line (`;` on a tie). The export uses `;`; English Excel writes `,`.
    - `LazyQuotes = true`, `FieldsPerRecord = -1`; quoted `;` and line
      breaks, CRLF/LF work. A `csv.ParseError` → `ErrUnreadable`.

12. **Upload** accepts `.xlsx` and `.csv` by extension (case-insensitive;
    content type not trusted); other formats get the updated format error.

## Changes

### `internal/store`

- **`migrations/002_family_entries.sql`** (outline, exact SQL):

  ```sql
  -- Family entries from the CSV export (SPEC §4.1.1, §7). Rebuilds
  -- children: parent_id becomes nullable, new family columns and keys.
  CREATE TABLE children_new (
      id            INTEGER PRIMARY KEY,
      -- NULL: family entry without a valid payer email (cannot be emailed)
      parent_id     INTEGER REFERENCES parents(id) ON DELETE CASCADE,
      family_number TEXT NOT NULL DEFAULT '',  -- NUMFAM; '' for Excel children
      name          TEXT NOT NULL,
      name_key      TEXT NOT NULL,             -- textnorm.Key(name)
      invalid_email TEXT NOT NULL DEFAULT '',  -- rejected payer email; '' if missing
      created_at    TEXT NOT NULL,
      updated_at    TEXT NOT NULL,
      CHECK (parent_id IS NOT NULL OR family_number <> ''),
      CHECK (parent_id IS NULL OR invalid_email = '')
  );
  INSERT INTO children_new (id, parent_id, name, name_key, created_at, updated_at)
      SELECT id, parent_id, name, name_key, created_at, updated_at FROM children;
  DROP TABLE children;
  ALTER TABLE children_new RENAME TO children;
  CREATE UNIQUE INDEX children_child_key ON children (parent_id, name_key)
      WHERE family_number = '';
  CREATE UNIQUE INDEX children_entry_key ON children (parent_id, family_number)
      WHERE family_number <> '' AND parent_id IS NOT NULL;
  CREATE UNIQUE INDEX children_no_email_key ON children (family_number)
      WHERE parent_id IS NULL;
  ```

- **`families.go`**:
  - `ImportRow` gains `FamilyNumber string` and `InvalidEmail string`.
    Doc: for a family entry `ChildName` is its billing name (`NOMFACT`) and
    `ParentName` too; `Email == ""` means the entry has no valid payer email
    (then `InvalidEmail` holds the rejected value or `""`), allowed only when
    `FamilyNumber != ""`.
  - `ImportFamilies`: at the start of the loop, a row with `Email == ""` and
    `FamilyNumber == ""` → return an error (programming error; the DB
    `CHECK` also rejects it). Rows with `Email == ""` skip the parent part
    entirely (no insert, no update, no `keptParents`) and use
    `parentID = 0` in the key; insert with `parent_id = NULL`
    (`sql.NullInt64`) and `invalid_email`.
  - `childKey{parentID int64; family string; nameKey string}`: Excel child →
    `{parentID, "", nameKey}`; family entry → `{parentID or 0, family, ""}`.
    `loadChildren` selects `parent_id` (scanned as `sql.NullInt64`, NULL →
    0), `family_number`, `invalid_email` and builds the same key.
  - `childRec` gains `invalidEmail string`. Update when `name` or
    `invalid_email` differs; the `UPDATE` sets `name`, `name_key` and
    `invalid_email`.
  - Deletion: `byID[key.parentID]` is absent for an entry without email; do
    not dereference a nil parent (`ParentName` and `Email` stay `""`).
  - `DeletedChild` gains `FamilyNumber string`. Doc: `Email == ""` for an
    entry without a valid email.
  - `Totals` gains `Families int`:
    `(SELECT COUNT(DISTINCT family_number) FROM children WHERE family_number <> '')`.
- **`children.go`**:
  - `Child` gains `FamilyNumber string` and `InvalidEmail string`. Doc: for a
    family entry `Name` is the billing name; `ParentID == 0` and
    `Email == ""` when it has no valid payer email.
  - `func (c Child) CanEmail() bool { return c.ParentID != 0 }` with a doc
    comment.
  - `childrenSelect`: `LEFT JOIN parents p`, selecting
    `c.id, c.name, c.family_number, c.invalid_email, COALESCE(p.id, 0),
    COALESCE(p.name, ''), COALESCE(p.email, '')`.
  - `childrenOrder` (decision 9):

    ```sql
     ORDER BY CASE WHEN c.family_number = '' THEN c.name_key
                   ELSE MIN(c.name_key) OVER (PARTITION BY c.family_number) END,
              c.family_number, c.name_key, p.id IS NULL, p.email, c.id
    ```

    (In `ChildrenByIDs` the window only sees the selected rows: fine, the
    order only matters within the result.)

### `internal/importer`

- Rename `Parse` → `ParseXLSX` (gopls rename; one caller). Package doc:
  "reads the families file (.xlsx or .csv)".
- `record{Line int; Cells []string}`; `parseLines` → `parseRecords([]record)
  (*Result, error)`; `ParseXLSX` builds records with `Line = i + 1`.
- New columns: `ColFamilyNumber Column = "family_number"` (`numfam`),
  `ColBillingName = "billing_name"` (`nomfact`), `ColPayerEmail =
  "payer_email"` (`email payeur`, `mail payeur`, `e-mail payeur`). Two column
  lists: `childColumns` (existing) and `familyColumns`. RL and phone headers
  are not in any alias table. `mapColumns(header, layout)`.
- `type Layout int` with `LayoutChildren` (zero value) and `LayoutFamilies`;
  `Result.Layout Layout`; `func (r *Result) Families() bool` (layout is
  families), used by the preview template.
- `Row` gains `FamilyNumber string` and `InvalidEmail string` (same meaning
  as in `store.ImportRow`). For a family entry `ParentName` = `ChildName` =
  `NOMFACT`; `Email == ""` for the no-email entry.
- New codes: blocking `IssueMissingFamilyNumber = "missing_family_number"`,
  `IssueMissingBillingName = "missing_billing_name"`; warnings
  `IssueBillingNameConflict = "billing_name_conflict"`,
  `IssueMissingPayerEmail = "missing_payer_email"`,
  `IssueInvalidPayerEmail = "invalid_payer_email"`. Each with a doc comment.
- `maxInvalidEmailLen = 254` and an unexported `cut(s string, n int)
  string` (rune-safe, appends `…` when cut).
- Errors: `ErrUnreadable` text becomes "file is not readable"; new
  `ErrTooLarge = errors.New("file is too large")`.
- New **`csv.go`**: `ParseCSV(r io.Reader) (*Result, error)`, plus unexported
  `decodeText([]byte) (string, error)` and `detectDelimiter(string) rune`.
  `Result.Sheet` stays `""` for CSV.
- Family rules of decision 6 in their own function (e.g.
  `parseFamilies(records, idx)`), the child rules stay as they are.

### `internal/recipients`

- `Group`: skip children with `ParentID == 0` (doc comment says why).

### `internal/server`

- **`import.go`**:
  - Dispatch on extension: `.xlsx` → `importer.ParseXLSX`, `.csv` →
    `importer.ParseCSV`, else `import.error.format`.
  - `errors.Is(err, importer.ErrTooLarge)` → `import.error.too_large`.
  - `toImportRows` copies `FamilyNumber` and `InvalidEmail`.
  - `importPreviewData.NoEmail int`: number of `res.Rows` with
    `Email == ""` (computed in the handler).
  - Log lines unchanged (counts only; never the invalid value).
- **`children.go`**:
  - `childrenData` gains `Families bool` and `NoEmail int` (rows with
    `!CanEmail()`).
  - `childRow.Search` = `textnorm.Key(Name + " " + FamilyNumber + " " +
    ParentName + " " + Email + " " + InvalidEmail)`.
  - `composeData` gains `Families bool`.
  - `handleCompose`: after `ChildrenByIDs`, if any child has
    `!c.CanEmail()` → `slog.Warn("compose: entry without a valid email
    selected", "id", c.ID)` and `renderChildren(422,
    loc.T("children.error.no_email"))`. Then the existing flow.
- **`server.go`** (home): no code change beyond what the template reads
  (`Totals.Families` comes with `store.Totals`).
- No new route.

### `web/templates`

- **`import.html`**: `accept=".xlsx,.csv,application/vnd.openxmlformats-officedocument.spreadsheetml.sheet,text/csv"`;
  history headers use `import.col.entries` / `import.col.addresses`.
- **`import_preview.html`**:
  - `preview.file` when `.Result.Sheet` is set, else `preview.file_csv`.
  - Family layout (test `.Result.Families`, not a raw layout number): stat
    label `preview.entries_valid` instead of
    `preview.rows_valid`; extra stat `preview.no_email` with `.NoEmail`
    when > 0; counts table rows `common.family_entries` /
    `common.emails`; deletion section title `preview.deleted_title_families`,
    columns `preview.col.family_number`, `preview.col.billing_name`,
    `preview.col.email` (cell shows `common.no_valid_email` when the email
    is empty); confirm button `preview.confirm_deletions_families`.
  - Child layout: unchanged.
- **`children.html`**:
  - Title `children.title_families`, placeholder
    `children.search_placeholder_families`, counter template
    `children.counter_families` when `.Families`.
  - Notice `<div class="alert alert-warning" role="status">` with
    `children.no_email_notice .NoEmail` when > 0.
  - Family columns: select, `children.col.family_number`,
    `children.col.billing_name`, `children.col.payer_email`; `colspan` of
    the no-result row follows the column count.
  - Row without a valid email: `class="row-disabled"`, `data-email=""`,
    `<input type="checkbox" disabled aria-label="…">` with no `name` and no
    `value`; email cell shows `children.payer_email_missing` or
    `children.payer_email_invalid .InvalidEmail` in
    `<span class="warning-text">`.
  - Child layout: unchanged.
- **`compose.html`**: when `.Families`, headers `compose.col.payer`,
  `compose.col.email`, `compose.col.families` and text
  `compose.emails_count_families`.
- **`home.html`**: when `.Totals.Families` > 0, stats `common.families`
  (`.Totals.Families`) and `common.emails` (`.Totals.Parents`) and button
  `home.families_button`; otherwise unchanged.

### `web/static`

- **`children.js`**: `setVisible` skips disabled checkboxes. Nothing else.
- **`style.css`**: `.row-disabled td { color: var(--muted); }` and
  `.warning-text` (uses the existing warning colors). No other change.

### `web/locales` (both files, same keys)

Changed:

| Key | fr | en |
|---|---|---|
| `home.empty` | Aucune donnée pour l'instant. Commencez par importer le fichier des familles. | No data yet. Start by importing the families file. |
| `home.import_button` | Importer un fichier | Import a file |
| `import.help` | Importez l'export CSV du logiciel des familles (colonnes « NUMFAM », « NOMFACT », « EMAIL PAYEUR »), ou un fichier Excel (.xlsx) avec les colonnes « nom parent », « nom enfant » et « adresse email ». Les autres colonnes sont ignorées. | Import the CSV export of the families software (columns “NUMFAM”, “NOMFACT”, “EMAIL PAYEUR”), or an Excel file (.xlsx) with the columns “nom parent”, “nom enfant” and “adresse email” (or “parent name”, “child name”, “email”). Other columns are ignored. |
| `import.replace_note` | Le fichier remplace les données actuelles : ce qui n'est plus dans le fichier sera supprimé. Rien n'est enregistré avant votre confirmation. | The file replaces the current data: whatever is missing from the file will be deleted. Nothing is saved until you confirm. |
| `import.file_label` | Fichier (.csv ou .xlsx) | File (.csv or .xlsx) |
| `import.error.format` | Seuls les fichiers .csv et Excel .xlsx sont acceptés. Dans Excel : Fichier › Enregistrer sous › Classeur Excel (.xlsx). | Only .csv and Excel .xlsx files are accepted. In Excel: File › Save As › Excel Workbook (.xlsx). |
| `import.error.unreadable` | Ce fichier ne peut pas être lu. Vérifiez qu'il s'agit d'un fichier .csv ou Excel .xlsx. | This file cannot be read. Check that it is a .csv or Excel .xlsx file. |
| `nav.children` | Sélection | Selection |
| `children.empty` | Rien à sélectionner pour l'instant. Importez d'abord le fichier des familles. | Nothing to select yet. Import the families file first. |
| `children.error.none_selected` | Sélectionnez au moins une ligne. | Select at least one row. |
| `children.error.too_many` | Trop de lignes sélectionnées (maximum %d). | Too many rows selected (maximum %d). |
| `compose.dropped` | %d ligne(s) sélectionnée(s) n'existe(nt) plus (un import a eu lieu entre-temps) et a(ont) été ignorée(s). | %d selected row(s) no longer exist (an import happened in between) and were ignored. |

New:

| Key | fr | en |
|---|---|---|
| `common.families` | Familles | Families |
| `common.emails` | Adresses email | Email addresses |
| `common.family_entries` | Familles (par adresse) | Families (per address) |
| `common.no_valid_email` | Pas d'email valide | No valid email |
| `import.col.entries` | Enfants / familles | Children / families |
| `import.col.addresses` | Parents / adresses | Parents / addresses |
| `import.column.family_number` | NUMFAM | NUMFAM |
| `import.column.billing_name` | NOMFACT | NOMFACT |
| `import.column.payer_email` | EMAIL PAYEUR | EMAIL PAYEUR |
| `import.issue.missing_family_number` | Numéro de famille (NUMFAM) manquant | Family number (NUMFAM) missing |
| `import.issue.missing_billing_name` | Nom de facturation (NOMFACT) manquant | Billing name (NOMFACT) missing |
| `import.issue.billing_name_conflict` | Même famille et même email qu'une ligne précédente mais autre nom de facturation (%s) : le premier est conservé | Same family and email as an earlier row but another billing name (%s): the first one is kept |
| `import.issue.missing_payer_email` | Email du payeur manquant (famille %s) | Payer email missing (family %s) |
| `import.issue.invalid_payer_email` | Email du payeur invalide : %s. La famille est importée mais ne pourra pas recevoir d'email à cette adresse. | Invalid payer email: %s. The family is imported but cannot be emailed at this address. |
| `preview.file_csv` | Fichier « %s ». | File “%s”. |
| `preview.entries_valid` | Familles à enregistrer (par adresse) | Families to save (per address) |
| `preview.no_email` | Sans email valide | Without a valid email |
| `preview.deleted_title_families` | Familles qui seront supprimées | Families that will be deleted |
| `preview.col.family_number` | N° famille | Family no. |
| `preview.col.billing_name` | Nom de facturation | Billing name |
| `preview.confirm_deletions_families` | Confirmer l'import et supprimer %d ligne(s) famille | Confirm import and delete %d family row(s) |
| `home.families_button` | Choisir des familles | Choose families |
| `children.title_families` | Familles | Families |
| `children.search_placeholder_families` | Nom, n° de famille ou email | Name, family no. or email |
| `children.counter_families` | {children} famille(s) sélectionnée(s) → {emails} email(s) | {children} family row(s) selected → {emails} email(s) |
| `children.col.family_number` | N° famille | Family no. |
| `children.col.billing_name` | Nom de facturation | Billing name |
| `children.col.payer_email` | Email du payeur | Payer email |
| `children.payer_email_missing` | Email du payeur manquant | Payer email missing |
| `children.payer_email_invalid` | Email du payeur invalide : %s | Invalid payer email: %s |
| `children.no_email_notice` | %d ligne(s) sans adresse email valide : elles ne peuvent pas être sélectionnées. Corrigez le fichier puis importez-le à nouveau. | %d row(s) without a valid email address: they cannot be selected. Fix the file, then import it again. |
| `children.error.no_email` | La sélection contient une famille sans adresse email valide. | The selection contains a family without a valid email address. |
| `compose.col.payer` | Payeur | Payer |
| `compose.col.families` | Familles concernées | Families concerned |
| `compose.emails_count_families` | %d email(s) seront envoyés, un par adresse. | %d email(s) will be sent, one per address. |

The old `preview.rows_valid`, `preview.confirm_deletions`, `children.title`
etc. stay for the child layout.

### `docs` (done by archi with this plan)

- SPEC §4.1, §4.2, §7 aligned with this design; CLAUDE.md Status/Open
  points.

## Test plan

Fixtures use fake data only (never a real export). Build CSV fixtures as
strings/bytes in the tests; `.xlsx` fixtures in memory with excelize.

**importer — CSV reading (`ParseCSV`)**
- The exact real header (with its empty cells) + fake lines parses; empty
  headers ignored; shuffled columns still mapped.
- UTF-8, UTF-8 + BOM, Windows-1252 (`0xC9` "É", `0xE9` "é") give the same
  `NOMFACT`; the BOM does not break the `NUMFAM` header.
- NUL byte → `ErrUnreadable`; empty input and header-only →
  `ErrEmpty`; more than `maxCSVBytes` → `ErrTooLarge`.
- `;` and `,` detected (tie → `;`); quoted `;` and quoted line break; `""`
  escape; stray quote (lazy); CRLF; ragged lines (fewer cells than header).
- Line numbers = file lines, after a blank line and after a multi-line
  quoted field.
- A `nom parent;nom enfant;email` CSV uses the child layout and its rules.

**importer — family layout (via CSV, and once via an `.xlsx` with the same
header to prove layout detection is reader-independent)**
- 3 lines, same `NUMFAM`, same email (case differs) → 1 entry, no warning,
  `Line` = first line, `Layout` = families, `Families()` true.
- Same `NUMFAM`, 2 distinct valid emails → 2 entries.
- Same (`NUMFAM`, email), other `NOMFACT` → `billing_name_conflict` on the
  later line, first name kept; same name with other case/accents → no
  warning.
- 2 families, same email, other names → 2 entries + `parent_name_conflict`.
- Family whose only line has an empty email → 1 entry with `Email ""`,
  `InvalidEmail ""`, warning `missing_payer_email` (value = `NUMFAM`);
  file importable (`Errors` empty).
- Family whose only line has `jean@gmailcom` → 1 entry with `Email ""`,
  `InvalidEmail "jean@gmailcom"`, warning `invalid_payer_email` with the
  value; `-` and `néant` behave the same.
- Family with a valid line + an empty line → 1 valid entry only, warning on
  the empty line.
- Family with a valid line + an invalid line → 1 valid entry + 1 no-email
  entry (with the invalid value), warning on the invalid line.
- Family with two different invalid values → one no-email entry with the
  first value; two warnings.
- Family with empty + invalid lines (no valid) → one no-email entry with the
  invalid value and the invalid line's `Line`/`NOMFACT`.
- Invalid value longer than 254 characters → cut, ends with `…`, valid UTF-8.
- Missing `NUMFAM`, missing `NOMFACT` → blocking with line numbers; such a
  line yields no entry and no email warning.
- `Rows` ordered by `Line`.
- RL emails, RL names and phones never appear anywhere in `Result` (assert
  the fixture's RL/phone values are absent from `Rows`, `Errors`,
  `Warnings`).
- Header with `NUMFAM` but no `EMAIL PAYEUR` → `MissingColumnsError` with
  the family columns; header with neither layout → the child layout's.
- Excel layout unchanged: missing/invalid email still blocking (existing
  tests pass after the `Parse` → `ParseXLSX` rename).

**store**
- Migration 002 on a DB created with migration 001 only and holding Excel
  data: rows, IDs, parents kept; `family_number`/`invalid_email` = `''`.
- Family entries: add; same `NUMFAM` with new `NOMFACT` → Updated (name and
  `name_key`), same ID; new payer email → 1 deleted + 1 added; two families
  on one email → one parent, two children.
- No-email entries: inserted with `parent_id NULL`, no parent created,
  `Parents` counters untouched; re-import identical → Unchanged, same ID;
  changed invalid value or `NOMFACT` → Updated, same ID; family gets a valid
  email → no-email entry deleted, valid entry added (and the reverse);
  removed from the file → deleted and listed in `DeletedChildren` with
  `FamilyNumber`, empty `Email` (no panic).
- Valid entry and no-email entry of the same family coexist.
- Dry run gives the same summary as the real import and writes nothing.
- `ImportRow` with empty `Email` and empty `FamilyNumber` → error, nothing
  saved.
- Excel import over family data deletes all entries (and vice versa);
  shared email keeps its parent ID.
- DB constraints: duplicate Excel (parent, name_key), duplicate family
  (parent, number), two no-email entries for one number, `parent_id NULL`
  with `family_number ''`, `invalid_email` set with a parent → all rejected
  (raw inserts in the test).
- `Children`: `FamilyNumber`, `InvalidEmail`, `CanEmail()`; a family with
  two payers named differently ("ZOLA…" and "ADAM…") appears on adjacent
  rows, no-email entry right after its family's valid rows; Excel order
  unchanged. `ChildrenByIDs` returns no-email entries too.
- `Totals.Families` counts distinct family numbers (two entries of one
  family = 1); 0 for Excel data.

**recipients**
- `Group` skips a child with `ParentID == 0`; two families on one parent →
  one recipient with both names.

**server** (real store in `t.TempDir()`)
- `.csv` upload → preview (family labels, "without valid email" stat when
  relevant, CSV file line without sheet), confirm → saved; `.CSV`
  accepted; `.xls`/`.txt` → 422 with the format error.
- CSV with only warnings (missing/invalid payer emails) → preview has a
  token and can be confirmed.
- `/children` with family data: family columns; no-email row has a
  disabled checkbox without `name`, the warning text (invalid value shown,
  HTML-escaped), and the notice with the count; child data unchanged.
- `POST /compose` with a valid entry ID + a no-email entry ID → 422 with
  `children.error.no_email`, no recipients page; with only valid entries of
  two families sharing an email → one recipient listing both; family
  labels on the page.
- Home with family data shows families/email-addresses stats.
- Locale files keep identical keys (existing test).

## Out of scope

- RL1/RL2 emails as recipients (also as a fallback for a missing payer
  email), phones, `NUMFACFACT`, addresses.
- Placeholders for families in §4.3 (`{{child_name}}` / `{{children}}` for
  an entry): decided in the compose plan.
- Changing the Excel layout rules (decision 8).
- Manual column mapping, `sep=;` line, UTF-16, Mac Roman.
- Ticking all rows of a family at once.
- SPEC §9 (CSV row) and §10.1 wording: to update when the feature ships.

## Open questions

None blocking. The real export is not available to the team; the alias
table, delimiter and encoding rules may need a small follow-up once a fake
copy of a real file is tested.

## Implementation notes

Files changed (production only, no `*_test.go` touched):
- `internal/store/migrations/002_family_entries.sql` (new): plan SQL as is.
- `internal/store/families.go`: `ImportRow`/`DeletedChild` family fields,
  `childKey{parentID, family, nameKey}`, entries without email
  (`parent_id NULL`), update on name or invalid value, nil-safe deletion,
  `Totals.Families`.
- `internal/store/children.go`: `Child.FamilyNumber/InvalidEmail`, `CanEmail`,
  LEFT JOIN, new ordering.
- `internal/importer/importer.go`: `Parse` -> `ParseXLSX`, `record`,
  `parseRecords` (layout from header), `parseChildren` (rules unchanged),
  `parseFamilies`, new columns/issues/`Layout`/`ErrTooLarge`, `cut`.
- `internal/importer/csv.go` (new): `ParseCSV`, `decodeText`, `detectDelimiter`.
- `internal/recipients/recipients.go`: `Group` skips `ParentID == 0`.
- `internal/server/import.go`, `children.go`: extension dispatch, `NoEmail`,
  `Families`, 422 on a no-email ID in `/compose`.
- Templates (`children`, `compose`, `home`, `import`, `import_preview`),
  `web/static/children.js`, `style.css`, `web/locales/{fr,en}.json`.

Deviations / details:
- `mapColumns` now returns `(Layout, map, error)`; one shared `headerAliases`
  table holds all aliases (layout = family if any family column is found).
- `cut` keeps 254 runes in total (the `…` included).
- Added a `--warning-text` CSS variable (no existing warning text color).
- Warnings are sorted by line in the family layout.
- Deleted-children sort adds family number as last tie-breaker.

For the tester:
- Existing tests that now fail: `importer_test.go` (uses `Parse`, now
  `ParseXLSX`); `store_test.go` (unkeyed `ImportRow{...}` literal, 3 values);
  server tests asserting old French/English wording (`TestChildrenPage`,
  `TestCompose`, `TestImportRejected`: "Seuls les fichiers Excel .xlsx",
  "Impossible de lire ce fichier", "Sélectionnez au moins un enfant.",
  "enfant(s) sélectionné(s) n'existe(nt) plus", "Select at least one child.",
  "too_many" message, children empty message) because of the plan's locale
  changes.
- A family with only empty-email lines is one no-email entry; a valid entry
  plus empty lines yields no no-email entry.
- Manual run done (Windows-1252 CSV, `;`): preview, confirm, `/children`,
  `/compose` 422 with a no-email ID, 200 with valid IDs, identical re-import.

### Fix round 1

- `importer.go`: limits `maxRecords = 50000` (non-blank lines after the
  header), `maxFields = 200`, `maxFamilyNumberLen = 64`,
  `maxBillingNameLen = 255` (runes). New `ErrTooManyColumns`, new issue
  `value_too_long` (blocking; `Value` is the constant column name `NUMFAM` or
  `NOMFACT`, not file data). `parseRecords` returns `ErrTooLarge` above
  `maxRecords` non-blank data records (so XLSX is covered too, after
  excelize has read the sheet).
- `csv.go`: the csv.Reader cannot cap fields itself (it builds the whole
  record first), so `tooManyColumns` pre-scans lines and counts delimiters
  (> 200 -> `ErrTooManyColumns`) before reading. Then `ReuseRecord = true`,
  blank records skipped at read time, the header is mapped immediately
  (missing columns reported at once), and only the mapped columns of each
  record are kept (strings cloned). The kept header is compacted the same
  way, so `parseRecords` still picks the layout from it. More than
  `maxRecords` data records -> `ErrTooLarge`. Line numbers unchanged
  (`FieldPos`).
- `server/import.go`: `ErrTooManyColumns` -> `import.error.too_many_columns`;
  `importPreviewData` gets `Errors/MoreErrors/Warnings/MoreWarnings`
  (`firstIssues`, max 100 each); `import_preview.html` lists those and shows
  `preview.more_issues` ("... and N more"). The error/warning titles still
  show the full count.
- Locales (fr+en): `import.error.too_many_columns`, `import.issue.value_too_long`,
  `preview.more_issues`.
- Measured (ParseCSV, TotalAlloc): 16 MB of ";\n" -> ErrEmpty, 0.37 s, 53 MB;
  10 MB single line of ";" -> ErrTooManyColumns, 3 ms, 34 MB; header + 16 MB
  of ";\n" -> ErrEmpty, 0.37 s, 53 MB; 60000 rows -> ErrTooLarge, 6 ms.
  50000 identical-family rows: 913 MB total alloc (not live), caused by
  family pass 2 with one huge family; unrealistic, not addressed.
- No existing test broke.
