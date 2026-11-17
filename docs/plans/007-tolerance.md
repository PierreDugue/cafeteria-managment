# 007 — "Tolérance" flag per family

Status: **approved (2026-11-17)** · SPEC §4.2 "Tolérance" (and §4.0, §4.1,
§7, §8)

Depends on plan 006 (migration 007 `settings`): this plan adds migration
**008**, so 006 is implemented first.

## Goal

On the selection page of a family list (CSV data, SPEC §4.1.1), each family
gets a **"Tolérance"** checkbox set by the user. A family "en tolérance" is
shown **in red** (all its rows), the flag is **remembered** (database), and
the user can **sort** the list by this column (flagged families first). The
flag is user data, independent of the selection used for sending.

## User answers (2026-11-17)

1. Meaning: reading (a) of plan 006 — a flag set by the user per family on
   the selection page, shown in red and remembered.
2. **One flag per family number**, not per row: a family with two payer
   emails (two rows) has one flag.
3. The selection list can be **sorted by this column**.

The spelling in the request was "Tolérence"; the UI uses the French word
**"Tolérance"**.

## Decisions

1. **A flag is a row `(list_id, family_number)` in a new `tolerances`
   table** (migration 008).
   - Keyed by family number, not by entry ID: a family whose payer email
     changes is deleted and re-added by the import (new entry, §4.1
     matching on number + email), and must keep its flag. A `child_id` key
     (like `last_selection`) would lose it.
   - Per list: the same family in two lists is two independent entries
     (§4.0), so it has two independent flags. Deleted with the list
     (`ON DELETE CASCADE`).
   - Not a column of `children`: the flag belongs to the family number,
     which spans several rows.
   - Rejected: storing it in `settings` (per-family data, not a setting).

2. **Re-import: the flag lives as long as the list has that family
   number.** The file is the source of truth for the list's families (§4.0,
   §4.1); the flag is the user's own data about a family *of the list*.
   - The family is still in the file (same or other payer email, other
     billing name): **flag kept**.
   - The family number is no longer in the file: the family is deleted from
     the list, and **its flag is deleted with it**, in the same import
     transaction. If the family comes back in a later file, it starts
     without the flag.
   - The **preview shows it** before anything is saved: "N famille(s) en
     tolérance ne sont plus dans le fichier : leur tolérance sera retirée",
     and a "Tolérance" mark on those families in the list of deleted
     entries. The user can cancel (as for any deletion). The preview's
     dry run computes this the same way as the real import.
   - Replacing a family list with an Excel file (no family numbers)
     therefore removes every flag of the list, also shown in the preview.
   - Why not keep flags forever: a flag left on a family that is no longer
     in the list is personal data with no use (data minimisation, §8), and
     it would reappear silently on a family number reused or re-added a
     year later. Why not drop all flags on every import: the user would
     have to set them again after each weekly export.

3. **Family data only.** The flag is per family number; an Excel
   parent/child list (§4.1) has no family number, so its selection page has
   no "Tolérance" column. (The Excel layout itself is still unconfirmed,
   §10.)

4. **One checkbox per family, on its first row.** Rows of a family are
   adjacent (§4.2), so the checkbox is on the first row of the family; the
   other rows of the family have an empty cell. **Every row of a flagged
   family has a red background** (`--danger-bg`); the checkbox uses the
   danger colour (`accent-color`). A family with only an entry without a
   valid email can be flagged too (the flag is not about sending).
   - The checkbox is separate from the selection checkbox: it is not named
     `child`, is not part of the selection form's data, never changes the
     counter, "select all / none" or a sending.

5. **Saving: one endpoint that replaces the list's flags,
   `POST /lists/{id}/tolerances`.** Fields: `version` (the list version of
   the page, as for the selection) and `family` (repeated: every checked
   family number). In one transaction: version re-checked (list gone or
   changed → stale), every posted number must be a family number of the
   list (else refused), duplicates ignored, then flags not posted are
   deleted and new ones inserted (existing ones keep their `created_at`).
   - The tolerance checkboxes belong to a **separate form**
     (`id="tolerance-form"`, through the `form` attribute of each checkbox,
     since forms cannot be nested in the selection form).
   - **Without JavaScript**: the form has a button "Enregistrer les
     tolérances"; the server answers **303** to `/children?list={id}&tolerance=1`
     with the notice "Tolérances enregistrées". (Unsent selection ticks are
     lost by that reload; acceptable for the no-JS fallback.)
   - **With JavaScript** (`children.js`): every change saves at once with
     `fetch` POST of the whole tolerance form and the header
     `HX-Request: true` (the htmx convention, so a later switch to htmx
     keeps the server side); the server answers **204**. The save button is
     hidden. Only one request is in flight; a change made meanwhile is sent
     right after with the latest state (the full state makes the last write
     win). On failure (409 stale, 422, network) the checkboxes and the red
     rows go back to the last saved state and an error line is shown:
     "La tolérance n'a pas pu être enregistrée. Rechargez la page."
   - Why the full set and not one flag per request: one endpoint and one
     store function serve both the no-JS and the JS paths, and the replace
     is idempotent.
   - Answers without JS: stale → **409** the children page with
     `compose.error.stale`; unknown family number or bad form → **422** the
     children page with `children.error.tolerance_invalid`; unknown list →
     404 as for other list routes. Nothing saved in these cases.
   - CSRF and localhost protection: existing middleware (a same-origin
     `fetch` passes `CrossOriginProtection`).
   - A save does not change the list version, the remembered selection,
     nor "last list used" (§4.0).

6. **Sort by tolerance: in the page (JS), not saved.** The "Tolérance"
   header is a button (plain text without JS) with `aria-sort`. A click
   toggles between the default order (§4.2) and **flagged families first**,
   each group in the default order. The sort moves whole families (rows of
   a family stay adjacent) and works together with the search filter.
   Changing a flag afterwards does not re-sort (rows do not jump under the
   cursor); clicking the header again does. The sort is not remembered
   (SPEC does not ask for it).

7. **Shown only on the selection page.** Not in compose, confirmation,
   history, emails or placeholders. Logs: counts only (never family
   numbers).

## Changes

### `internal/store`

- **`migrations/008_tolerances.sql`** (new):

  ```sql
  -- "Tolérance" flags set by the user, one per family number of a list
  -- (SPEC §4.2, §7). Not from the file: an import removes the flag of a
  -- family number the list no longer has; deleted with the list.
  CREATE TABLE tolerances (
      list_id       INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
      family_number TEXT NOT NULL CHECK (family_number <> ''),
      created_at    TEXT NOT NULL,
      PRIMARY KEY (list_id, family_number)
  );
  ```

- **`tolerances.go`** (new):

  ```go
  // ErrUnknownFamily is returned when a family number is not in the list.
  var ErrUnknownFamily = errors.New("family number not in the list")

  // Tolerances returns the flagged family numbers of a list.
  func (s *Store) Tolerances(ctx context.Context, listID int64) (map[string]bool, error)

  // SetTolerances makes families the flagged family numbers of the list, in
  // one transaction. ErrStaleSelection if the list is gone or its version is
  // not version; ErrUnknownFamily if a number is not a family number of the
  // list (nothing changed). Duplicates are ignored; kept flags keep
  // created_at.
  func (s *Store) SetTolerances(ctx context.Context, listID, version int64, families []string) error
  ```

  Reuse `listVersion` as `CreateBatch` does (`ErrNotFound` or mismatch →
  `ErrStaleSelection`). Only `tx` inside the transaction.

- **`families.go`** (`ImportFamilies`):
  - `ImportSummary` gains `TolerancesRemoved int`; `DeletedChild` gains
    `Tolerance bool` (true when that family's flag is removed by this
    import).
  - After the deletions, in the same transaction (dry run included):
    remove the list's flags whose family number has no remaining entry in
    the list (`DELETE … WHERE list_id = ? AND family_number NOT IN (SELECT
    family_number FROM children WHERE list_id = ?)`), count them, and mark
    the matching `DeletedChildren`. A new list has no flag. The import log
    line gets the count (no family numbers).

### `internal/server`

- **`children.go`**: read `Tolerances` with the rows; `childRow` gains
  `Tolerance bool` (family flagged: red row) and `FirstOfFamily bool`
  (checkbox shown); `childrenData` gains `ToleranceSaved bool` (notice from
  `?tolerance=1`). Only for family data.
- **`tolerances.go`** (new): `handleTolerances` for
  `POST /lists/{id}/tolerances` (decision 5): `MaxBytesReader` (1 MB, like
  the selection), `ParseForm`, `version` parsed as for compose, `family`
  values trimmed and non-empty; `HX-Request: true` → 204 on success, plain
  status codes on errors (body: the localized message); else 303 / 409 /
  422 pages. Log: list ID and number of flags.
- **`server.go`**: route `POST /lists/{id}/tolerances`.
- **`import.go`** / preview data: pass `TolerancesRemoved` and the marks to
  the template.

### `web/templates`

- `children.html` (family data only): `<form id="tolerance-form" method="post"
  action="/lists/{{.List.ID}}/tolerances">` placed outside the selection
  form, with hidden `version` and the no-JS save button; a "Tolérance"
  column (last) with, on the first row of each family, `<input
  type="checkbox" class="tolerance" form="tolerance-form" name="family"
  value="{{.FamilyNumber}}" aria-label="…">`; `data-family` and class
  `row-tolerance` (when flagged) on every row; the header as a sort button
  (hidden button / plain text pattern as for `list-show`); the notice and a
  hidden error line for JS (text from a `data-` attribute). The
  `no-result` row's `colspan` follows the column count.
- `import_preview.html`: the removed-flags line, and the "Tolérance" mark
  in the deleted entries table.

### `web/static`

- `children.js`:
  - `checkbox(row)` must select `input[name="child"]` (the row now has two
    checkboxes): counter, select all / none use only the selection boxes.
  - Tolerance: toggle `row-tolerance` on every row with the same
    `data-family`; serialized save (decision 5); revert + error on failure;
    hide the save button.
  - Sort (decision 6): reorder the `tbody` rows by family groups; keep the
    `no-result` row last; update `aria-sort`.
- `style.css`: `.row-tolerance td { background: var(--danger-bg); }`,
  `input.tolerance { accent-color: var(--danger); }`, the sort button
  styled as a header.

### `web/locales` (both files, same keys)

| Key | fr | en |
|---|---|---|
| `children.col.tolerance` | Tolérance | Tolerance |
| `children.sort_tolerance` | Tolérance (trier) | Tolerance (sort) |
| `children.tolerance_label` | Tolérance pour la famille %s | Tolerance for family %s |
| `children.tolerance_save` | Enregistrer les tolérances | Save tolerances |
| `children.tolerance_saved` | Tolérances enregistrées. | Tolerances saved. |
| `children.tolerance_error` | La tolérance n'a pas pu être enregistrée. Rechargez la page. | The tolerance could not be saved. Reload the page. |
| `children.error.tolerance_invalid` | Une famille cochée n'est pas dans cette liste. Rien n'a été enregistré. | A ticked family is not in this list. Nothing was saved. |
| `preview.tolerance_removed` | %d famille(s) en tolérance ne sont plus dans le fichier : leur tolérance sera retirée. | %d family(ies) with tolerance are no longer in the file: their tolerance will be removed. |
| `preview.tolerance_mark` | Tolérance | Tolerance |

Reused: `compose.error.stale`.

### Docs (applied by archi with this plan)

- SPEC §4.2 "Tolérance", §4.0 / §4.1 (flags and re-import), §7
  (`tolerances`), §8 (no family numbers in logs).

## Test plan

**`store`**
- Migration 008 on a v7 DB with data: rows untouched; fresh DB at version
  8; `foreign_key_check` empty.
- `SetTolerances` / `Tolerances`: set, replace (removed ones deleted, kept
  ones keep `created_at`), empty set clears, duplicates ignored; other
  lists untouched (same family number in two lists → independent flags).
- `SetTolerances` errors: stale version and deleted list →
  `ErrStaleSelection`; unknown family number (or a number of another list,
  or `""`) → `ErrUnknownFamily`; nothing changed in each case.
- Family number with only a no-email entry can be flagged.
- `ImportFamilies` (real and dry run, same result):
  - family still in the file with the same payer email → flag kept;
  - payer email changed (entry deleted and re-added) → flag kept, not
    counted as removed, not marked;
  - family with two payer emails, one of them removed → flag kept;
  - family number gone → flag deleted, `TolerancesRemoved` 1, its
    `DeletedChildren` rows marked;
  - dry run deletes nothing (flags still there after the preview);
  - Excel file replacing a family list → all flags removed and counted;
  - import into another list → flags of this list untouched;
  - new list → 0.
- `DeleteList` removes the list's flags (cascade).

**`server`** (`httptest`)
- Children page (family data): "Tolérance" column; checkbox only on the
  first row of each family; flagged family → every row has
  `row-tolerance`, checkbox checked; Excel data → no column; `colspan` of
  the no-result row correct; notice with `?tolerance=1`.
- `POST /lists/{id}/tolerances` without `HX-Request`: valid → 303 and the
  store updated; no `family` → all flags cleared; stale version → 409
  children page, nothing changed; unknown family → 422; bad version → 422
  (as compose); unknown list → 404; cross-site → 403; oversized body →
  422; GET → 405.
- Same with `HX-Request: true`: 204 on success; 409 / 422 / 404 status
  codes on errors; nothing changed.
- Saving does not change the remembered selection, the list version, nor
  "last list used".
- Import preview: removed-flags line and marks shown only when flags are
  removed; confirm then removes them.
- Logs: no family number in any tolerance or import log line.
- Locale key parity (existing test).

**JS** (no test framework in the project): checked by hand with `make
run`: toggle a flag (rows of the family turn red, reload keeps it), quick
toggles, failure path (stop the server, toggle → reverted with the error
line), select all / none and the counter ignore tolerance boxes, sort with
and without a search filter, page without JS (save button).

## Out of scope

- Filtering by tolerance, "select all en tolérance", showing it in
  compose, confirmation, history, emails or placeholders.
- Remembering the sort order.
- A flag for Excel parent/child lists.
- Importing the flag from the file.
