# 004 — Family lists

Status: **done (2026-11-16) · reviewed: approved after 1 fix round** ·
SPEC §4.0 (and §4.1, §4.1.1, §4.2, §4.3, §7, §11 milestone 4b)

## Goal

The user keeps several named family lists ("Cantine 2026-2027",
"Garderie"). Each import either creates a new list or replaces one existing
list. Other lists are never touched (SPEC §4.0, §4.1). Replacing an existing
list needs an explicit, server-checked confirmation (§4.0). The selection
page shows one list at a time and opens the last list used by default. The
remembered selection and the "stale selection" check work per list (§4.2).
The import history records the list. The home page shows one line per list.
Lists can be renamed and deleted. The data already in the database becomes
one list named "Liste importée".

## User answers (2026-11-16)

All eight questions were answered "yes, as recommended":

1. Lists can be **renamed and deleted** (delete with a confirmation page;
   the import history is kept). → decision 11.
2. Once lists exist, the **import target is picked every time** (no
   default). **New requirement**: replacing an existing list needs an
   **explicit confirmation that cannot be skipped**. → decisions 7 and 13.
3. **Home page: one line per list**. → decision 10.
4. **One list per selection** (no mixing). → decisions 8, 9.
5. **Same family in two lists = independent entries**. → decision 1.
6. **Only an import or "Continuer" updates "last list used"**; opening a
   list does not. → decision 8.
7. **"Liste importée" only created if data exists**. → decision 3.
8. **Import history shows the current list name** after a rename (a
   deleted list shows its last name and "(supprimée)"). → decision 11.

## Decisions

1. **A list is a row in a new `lists` table. `parents` and `children` get a
   `list_id`.** A parent (email) belongs to one list. The same email in two
   lists is two `parents` rows, so the same family in two lists is two
   independent entries (answer 5).
   - Parent email is unique **per list**: `UNIQUE (list_id, email)`.
   - The child keys stay `(parent_id, name_key)` and
     `(parent_id, family_number)`. A parent already belongs to one list, so
     these keys are per list already. The no-email key becomes
     `(list_id, family_number)`.
   - **A child and its parent are always in the same list.** This is
     enforced by the database: `children (parent_id, list_id)` references
     `parents (id, list_id)`. It is defense in depth against an import bug
     linking an entry to another list's parent. A NULL `parent_id` (entry
     without email) is not checked, as SQL requires.
   - `lists.id` is `AUTOINCREMENT`, so a deleted list's ID is never reused.
     An old `/children?list=7` bookmark or an old form never opens another
     list.
   - Rejected: a `list_id` on `children` only, with the parent's list
     derived from its children. Parents without children could not be
     placed in a list, and the email key could not be per list.

2. **Migration 005 rebuilds `parents`, `children` and `last_selection`
   together, keeping every ID** (exact SQL under Changes). The constraints
   are:
   - Foreign keys are ON (DSN), and `PRAGMA foreign_keys` is a no-op
     inside the migration transaction. With foreign keys on, `DROP TABLE`
     runs an implicit `DELETE` that fires `ON DELETE CASCADE`. Dropping the
     old `parents` would delete every child, and dropping the old `children`
     would delete every remembered selection.
   - The safe order is:
     1. Create `lists`, `parents_new`, `children_new` (referencing
        `parents_new`) and `last_selection_new` (referencing
        `children_new`).
     2. Copy all rows with their IDs.
     3. Drop the old tables, dependents first: `last_selection`, then
        `children`, then `parents`. At each drop, no remaining table
        references the dropped one, so nothing cascades.
     4. Rename the `*_new` tables. SQLite ≥ 3.26 (modernc ships 3.4x)
        rewrites the foreign-key references from `parents_new` to `parents`
        and from `children_new` to `children`.
     5. Recreate the indexes.
   - Everything runs inside the existing per-migration transaction, so a
     failure leaves the DB at version 4. No change to the migration runner
     is needed: no `PRAGMA foreign_keys=OFF` and no special marker.
   - **IDs are preserved**, so `last_selection` rows survive (they are
     copied) and the selection pre-checked before the upgrade is still
     pre-checked after it.
   - `imports` is only extended with `ALTER TABLE ADD COLUMN`. Its
     `list_id` is nullable with a NULL default, so a `REFERENCES` clause is
     allowed while foreign keys are on.
   - Rejected: turning foreign keys off in the runner for one migration.
     It changes shared code, and a mistake leaves the connection without
     foreign keys. Rejected: `ADD COLUMN` on `parents` and `children`. The
     `UNIQUE` on `parents.email` cannot be dropped without a rebuild, and an
     added `REFERENCES` column cannot be `NOT NULL`.

3. **"Liste importée" is created only when the DB holds data** (answer 7),
   meaning at least one row in `parents`, `children` or `imports`. A fresh
   install starts with no list, and the first import necessarily creates
   one. All existing parents, children and imports go into that list.
   - Its `name_key` is the literal `'liste importee'`, which equals
     `textnorm.Key("Liste importée")`. The tester checks this equality.
   - The name is stored data, not UI text: it stays French in an English
     UI, and the user can rename it.

4. **`last_selection` gets no `list_id`.** This deviates from the first
   SPEC §7 draft (SPEC §7 is updated accordingly). An entry belongs to
   exactly one list, so "the selection of list L" is
   `last_selection JOIN children WHERE list_id = L`. Saving for list L
   deletes only the rows of L's entries. This avoids a redundant column
   that could disagree with `children.list_id`. Deleting a list cascades
   through `children` into `last_selection`.

5. **List version = last import ID of that list**:
   `SELECT COALESCE(MAX(id), 0) FROM imports WHERE list_id = ?`, after
   checking that the list exists (`ErrNotFound` otherwise).
   - It only grows: imports are never deleted, and a deleted list's imports
     lose their `list_id` while the list itself disappears.
   - Renaming does not change it, and neither does an import into another
     list (§4.2 "Stale selection").
   - It replaces the global `DataVersion` in the selection form,
     `/compose` and `ImportFamilies.Base`.

6. **`ImportFamilies` is scoped to one target list.**
   `ImportRequest` gets `ListID int64` (existing list) **or**
   `NewListName string` (create). Exactly one must be set; otherwise it
   returns a programming error (plain `error`, not a sentinel).
   - Existing list: in the transaction, check that the list exists (else
     `ErrNotFound`). `Base` is that list's version, and a mismatch gives
     `ErrStaleImport`. Parents and children are loaded `WHERE list_id = ?`,
     so matching, deletions and counts only see that list. Inserts set
     `list_id`.
   - New list: the name is cleaned and keyed and must still be free (else
     `ErrNameTaken`, which also covers "taken meanwhile" at confirm, §4.1).
     The list row is inserted **inside the import transaction**, also in a
     dry run, which rolls it back. The preview of a new list therefore only
     shows additions, by construction. `Base` is 0 and is not checked.
   - A real import sets `lists.updated_at` and `lists.used_at`. It writes
     `imports.list_id` and `imports.list_name`, the latter a copy of the
     name at import time. `DeleteList` refreshes it to the list's last name
     (after renames) before deleting.
   - `ImportSummary` gains `ListID` (created or replaced) and `ListName`
     (cleaned name), which the preview shows.

7. **The import target is chosen explicitly on the import page** (answer 2).
   - The page has one `<select name="list">` with an empty "— Choisir —"
     first option (selected by default), then "Nouvelle liste…" (value
     `new`), then the lists by name. It also has a text field `list_name`,
     used only with `new` (ignored otherwise).
   - When no list exists, the select is replaced by a hidden `list=new`
     and the name field alone.
   - No default target: replacing the wrong list deletes its entries, so
     the user must pick one.
   - Both fields are re-shown with the typed values on any error.
   - The target is validated **before** parsing the file (cheap; errors
     re-render the form with 422). The checks are: missing choice; `new`
     with an invalid name (same rules as template names, decision 11); an
     unknown or invalid list ID; a new name already taken.
   - **The pending import carries the target** (`listID` or `newListName`),
     the `base` of the preview and the preview page data. The confirm form
     posts the token (and, for a replacement, the confirmation checkbox,
     decision 13), so the target cannot change between preview and confirm.
   - Confirm outcomes, after the checks of decision 13:
     - `ErrStaleImport` gives 409 with `import.error.stale`.
     - `ErrNotFound` (list deleted meanwhile) gives 409 with
       `import.error.list_gone`.
     - `ErrNameTaken` (name taken meanwhile) gives 409 with
       `import.error.list_name_taken`.
     - In all three cases the token is removed and the user checks the
       file again (§4.1).
     - A rename of the target list between preview and confirm does not
       block: the list is identified by its ID and its version is
       unchanged.
   - Success redirects to `/import?done=<importID>`. The page says "Import
     of « f » saved in the list « L »" (current name) with a link to
     `/children?list=ID` (no link if the list was deleted since).

8. **The selection page shows one list** (§4.2, answer 4).
   - `GET /children?list=<id>`. The ID is opaque, with no personal data.
   - Without `list`, the page opens the **default list**: the largest
     `used_at`, then the largest `id`.
   - An unknown or invalid `list` shows the default list with 404 and
     `lists.error.not_found`. With no list at all, the page shows the
     current empty state (200 without `list`, 404 with a `list` value).
   - Shown: the list name as page subtitle and, when there are 2 or more
     lists, a plain GET form (`<select name="list">` with the current list
     selected, and a "Show" button). It works without JS and in Wails.
   - The selection form posts a hidden `list` with `version`.
   - **`used_at`** ("last list used", answer 6) is updated by an import
     into the list and by "Continue" (`SaveSelection`), both POST. A GET
     that only opens a list writes nothing. This keeps GET free of side
     effects, which `CrossOriginProtection` does not cover.

9. **`/compose` checks the list and its version together.**
   - A missing or invalid `list` or `version` gives 409 with
     `compose.error.stale`.
   - `SelectedChildren(ctx, listID, version, ids)` runs in one read
     transaction. A missing list or a different version gives
     `ErrStaleSelection`. It then reads the IDs **with
     `AND c.list_id = ?`**, so an ID of another list is dropped like an
     unknown ID (only a tampered form can post one). An entry of another
     list can never reach the recipients, even with a matching version.
   - `SaveSelection(ctx, listID, ids)` replaces only that list's saved
     rows and sets its `used_at`.
   - The compose form carries the hidden `list`. "Back" goes to
     `/children?list=ID`. The page shows "Liste « L »" under the title.
   - Error re-renders go to the posted list when it exists, else to the
     default list.
   - **`recipients` and `message` do not change.** A selection holds
     entries of one list, and within a list an import deletes the entries
     of the other layout (§4.1). `message.ModeOf` on the selected
     recipients is therefore already "the mode of the list". §4.4 batches
     will copy the list name (SPEC §4.5); that is out of scope here.

10. **Home page: one line per list** (answer 3).
    - Table columns: name, entries (families or children), addresses (email
      addresses or parents), last import date. Each line has actions:
      "Choose" (`/children?list=ID`, shown only if the list has entries),
      "Rename", "Delete".
    - Lists are sorted by name (`name_key`, then `id`). Below the table:
      the import button. With no list: the current empty message and the
      import button.
    - `store.Totals` is no longer used and is removed (goexp checks
      references with gopls). The home keys left unused are removed:
      `home.last_import`, `home.children_button`, `home.families_button`
      (checked with grep on `web/templates` and `internal`).

11. **Rename and delete** (answers 1 and 8). These work like the templates
    pages: plain forms, POST only for changes, and a confirmation page for
    delete.
    - Name rules: the same as template names (required, ≤ 80 runes after
      `textnorm.Clean`, valid UTF-8, no control characters, unique by
      `textnorm.Key`). The rule check is **factored out of
      `validateTemplate`** into `checkName(name) (clean string, problem
      string)`, with `problem` ∈ `"" | "invalid" | "required" |
      "too_long"`. Templates map it to `templates.error.name_*` and lists
      to `lists.error.name_*`, so the rule is written once. The constant
      `maxTemplateNameLen` becomes `maxNameLen` (shared). The store's
      `checkNameFree` is generalized to take its query constant (template
      or list), and `ErrNameTaken` becomes "name already used", shared.
    - Renaming keeps the version, the entries and the selection. A
      re-cased name of the same list is allowed.
    - Deleting the list runs `DELETE FROM lists WHERE id = ?`, which
      cascades to its parents, its children and its remembered selection.
      Its `imports` rows are **kept** (`list_id` becomes NULL; `list_name`
      is set to the list's last name, in the same transaction).
    - The import history shows the **current** name of a live list, or the
      last name (copied at deletion) with "(supprimée)" for a deleted list (answer 8).
    - Templates are not affected. A pending import or a compose form aimed
      at a deleted list is refused (decisions 7, 9).
    - The delete page shows the name and the number of entries and
      addresses that will be deleted, and says that the import history is
      kept. Its button is red (`button-danger`).
    - After rename or delete: 303 to `/?renamed=1` or `/?deleted=1`, with no
      name in the URL. An unknown ID gives 404 with the home page and
      `lists.error.not_found`.

12. **Logs**: list IDs and counts only, never list names. A name such as
    "Famille Dupont" could be personal.

13. **Explicit confirmation to replace an existing list** (answer 2, new
    requirement). It cannot be skipped: the server checks it.
    - The preview of an import into an **existing** list (no row errors,
      so a token exists) shows, in the confirm form, a checkbox
      `<input type="checkbox" name="confirm_replace" value="1" required>`
      labelled "Je confirme remplacer la liste « X »." (`preview.replace_check`).
      `required` is only a convenience; the server is the authority.
    - The button says "Remplacer la liste « X »"
      (`preview.confirm_replace`). When the replacement deletes entries,
      the existing deletion list stays, the button stays red
      (`button-danger`) and says "Remplacer la liste « X » et supprimer N
      enfant(s)" / "… N ligne(s) famille"
      (`preview.confirm_replace_deletions`,
      `preview.confirm_replace_deletions_families`).
    - The checkbox is required for **every** replacement, including one
      with no changes: one simple rule, nothing to get wrong.
    - `POST /import/confirm`, in this order:
      1. Unknown or expired token: 422 on the import form with
         `import.error.expired` (unchanged).
      2. Pending target is an existing list and
         `r.PostFormValue("confirm_replace") != "1"` (missing, empty, `on`,
         `0`, anything else): **422**, the stored preview page is rendered
         again with an error alert `import.error.replace_unconfirmed` (list
         name as shown in the preview), **the token is kept** and the same
         token is in the form. Nothing is read from or written to the DB.
      3. Otherwise `ImportFamilies` runs, with the outcomes of decision 7.
    - Creating a new list needs no checkbox; a posted `confirm_replace` is
      ignored. The new-list button keeps `preview.confirm`; a new list
      never has deletions.
    - The preview page data needed for the 422 re-render is stored in the
      pending import (`preview importPreviewData`, stored without its
      `Token`; the handler sets `Token` to the posted token and `Error`
      when re-rendering), so the page is the same as the one the user saw.
      Rejected: re-running the dry run on the 422
      (it could show different numbers than the preview being confirmed,
      while the confirm is still bound to the old `base`); rejected: a
      separate confirmation page (one more step, same safety).
    - The old button keys `preview.confirm_deletions` and
      `preview.confirm_deletions_families` become unused (deletions only
      happen on a replacement) and are removed.

## Changes

### `internal/store`

- **`migrations/005_family_lists.sql`** (new, exact SQL):

  ```sql
  -- Family lists (SPEC §4.0, §7). Every entry moves into one list.
  -- parents, children and last_selection are rebuilt with their IDs.
  -- Foreign keys are ON and cannot be turned off inside this transaction,
  -- and DROP TABLE cascades like DELETE: the old tables are dropped
  -- dependents first, once nothing references them any more. The new
  -- tables reference each other under their *_new names; RENAME rewrites
  -- those references (SQLite >= 3.26).

  CREATE TABLE lists (
      id         INTEGER PRIMARY KEY AUTOINCREMENT, -- never reused
      name       TEXT NOT NULL,                     -- textnorm.Clean(name)
      name_key   TEXT NOT NULL UNIQUE,              -- textnorm.Key(name)
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      used_at    TEXT NOT NULL  -- last import or "Continue": the default list
  );

  -- Existing data, if any, becomes one list.
  INSERT INTO lists (name, name_key, created_at, updated_at, used_at)
      SELECT 'Liste importée', 'liste importee', ts, ts, ts
      FROM (SELECT strftime('%Y-%m-%dT%H:%M:%SZ', 'now') AS ts)
      WHERE EXISTS (SELECT 1 FROM parents) OR EXISTS (SELECT 1 FROM children)
         OR EXISTS (SELECT 1 FROM imports);

  CREATE TABLE parents_new (
      id         INTEGER PRIMARY KEY,
      list_id    INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
      name       TEXT NOT NULL,
      email      TEXT NOT NULL,  -- lower-case; identifies the parent in its list
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      UNIQUE (list_id, email),
      UNIQUE (id, list_id)       -- target of children (parent_id, list_id)
  );

  CREATE TABLE children_new (
      id            INTEGER PRIMARY KEY,
      list_id       INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
      -- NULL: family entry without a valid payer email (cannot be emailed)
      parent_id     INTEGER,
      family_number TEXT NOT NULL DEFAULT '',  -- NUMFAM; '' for Excel children
      name          TEXT NOT NULL,
      name_key      TEXT NOT NULL,             -- textnorm.Key(name)
      invalid_email TEXT NOT NULL DEFAULT '',  -- rejected payer email; '' if missing
      created_at    TEXT NOT NULL,
      updated_at    TEXT NOT NULL,
      -- an entry and its parent are always in the same list
      FOREIGN KEY (parent_id, list_id) REFERENCES parents_new (id, list_id) ON DELETE CASCADE,
      CHECK (parent_id IS NOT NULL OR family_number <> ''),
      CHECK (parent_id IS NULL OR invalid_email = '')
  );

  CREATE TABLE last_selection_new (
      child_id INTEGER PRIMARY KEY REFERENCES children_new(id) ON DELETE CASCADE
  );

  -- At most one list exists here; with no data nothing is copied.
  INSERT INTO parents_new (id, list_id, name, email, created_at, updated_at)
      SELECT id, (SELECT id FROM lists), name, email, created_at, updated_at FROM parents;
  INSERT INTO children_new (id, list_id, parent_id, family_number, name, name_key,
                            invalid_email, created_at, updated_at)
      SELECT id, (SELECT id FROM lists), parent_id, family_number, name, name_key,
             invalid_email, created_at, updated_at FROM children;
  INSERT INTO last_selection_new (child_id) SELECT child_id FROM last_selection;

  DROP TABLE last_selection;
  DROP TABLE children;
  DROP TABLE parents;
  ALTER TABLE parents_new RENAME TO parents;
  ALTER TABLE children_new RENAME TO children;
  ALTER TABLE last_selection_new RENAME TO last_selection;

  CREATE INDEX children_list ON children (list_id);
  CREATE UNIQUE INDEX children_child_key ON children (parent_id, name_key)
      WHERE family_number = '';
  CREATE UNIQUE INDEX children_entry_key ON children (parent_id, family_number)
      WHERE family_number <> '' AND parent_id IS NOT NULL;
  CREATE UNIQUE INDEX children_no_email_key ON children (list_id, family_number)
      WHERE parent_id IS NULL;

  -- Import history keeps the list, and its name if the list is deleted.
  ALTER TABLE imports ADD COLUMN list_id INTEGER REFERENCES lists(id) ON DELETE SET NULL;
  ALTER TABLE imports ADD COLUMN list_name TEXT NOT NULL DEFAULT '';
  UPDATE imports SET list_id = (SELECT id FROM lists), list_name = (SELECT name FROM lists);
  CREATE INDEX imports_list ON imports (list_id);
  ```

  The timestamp format matches `Store.timestamp()` (RFC 3339, UTC, `Z`).
  goexp checks the index names against migrations 001–004 (same names as
  the dropped ones, or new ones; no clash with an existing index).

- **`lists.go`** (new):

  ```go
  // List is a named family list (SPEC §4.0) with its current counts.
  type List struct {
      ID         int64
      Name       string
      Parents    int       // valid email addresses
      Children   int       // children or family entries
      Families   int       // distinct family numbers (0 for Excel data)
      LastImport time.Time // zero if the list has no import
      UsedAt     time.Time
  }

  func (s *Store) Lists(ctx context.Context) ([]List, error)                 // ordered by name_key, id
  func (s *Store) List(ctx context.Context, id int64) (List, error)          // ErrNotFound
  func (s *Store) DefaultList(ctx context.Context) (List, error)             // max used_at, then max id; ErrNotFound if none
  func (s *Store) RenameList(ctx context.Context, id int64, name string) error // ErrNotFound, ErrNameTaken
  func (s *Store) DeleteList(ctx context.Context, id int64) error            // ErrNotFound
  func (s *Store) ListVersion(ctx context.Context, id int64) (int64, error)  // ErrNotFound
  ```

  - The counts in `Lists`/`List`/`DefaultList` come from correlated
    subqueries per list (small data). `LastImport` is the `imported_at` of
    the list's highest import ID.
  - The store cleans the name and computes the key, and an empty cleaned
    name is an error. Length and content limits are checked in the server,
    as for templates.
  - Unexported `listVersion(ctx, q querier, id) (int64, error)` checks
    existence and then reads the max import ID. It is shared by
    `ListVersion`, `SelectedChildren` and `ImportFamilies`, and replaces
    `lastImportID`.

- **`families.go`**:
  - `ImportRequest` gains `ListID int64` and `NewListName string`
    (decision 6). Doc: exactly one is set; `Base` applies to `ListID`
    only.
  - `ImportSummary` gains `ListID int64` and `ListName string`.
  - `loadParents(ctx, tx, listID)` and `loadChildren(ctx, tx, listID)` add
    `WHERE list_id = ?`. Parent and child inserts write `list_id`. The
    import record writes `list_id` and `list_name`. A real import updates
    `lists.updated_at` and `used_at`.
  - `ImportRecord` gains `ListID int64` (0 when the list was deleted),
    `ListName string` (current name, or the copy), and `ListDeleted bool`.
    `Imports` reads them with
    `LEFT JOIN lists l ON l.id = i.list_id`,
    `COALESCE(l.name, i.list_name)`, `l.id IS NULL`.
  - `Totals` and its type are removed (decision 10).

- **`children.go`**:
  - `Children(ctx, listID)`, `ChildrenByIDs(ctx, listID, ids)`, and
    `SelectedChildren(ctx, listID, version, ids)` all filter on
    `c.list_id = ?` before `childrenOrder`. The window partition then only
    sees that list.
  - `DataVersion` is removed, replaced by `ListVersion`.
  - `SaveSelection(ctx, listID, ids)` runs these statements in one
    transaction:

    ```sql
    DELETE FROM last_selection WHERE child_id IN (SELECT id FROM children WHERE list_id = ?);
    INSERT INTO last_selection (child_id) SELECT id FROM children
      WHERE list_id = ? AND parent_id IS NOT NULL AND id IN (...);
    UPDATE lists SET used_at = ? WHERE id = ?;
    ```

    A missing list gives `ErrNotFound` (the `UPDATE` affects 0 rows; the
    transaction is rolled back).
  - `SavedSelection(ctx, listID)` reads the join on `children.list_id`.

- **`templates.go`**:
  - `checkNameFree(ctx, tx, query, key, exceptID)` takes a constant query
    (`templateNameQuery` / `listNameQuery`, no string building).
  - `ErrNameTaken`'s message becomes "name already used", with a doc saying
    it is shared by templates and lists.

### `internal/server`

- **`names.go`** (new): `checkName` and `maxNameLen` (decision 11), moved
  with `hasControl`. `validateTemplate` uses `checkName`. Add
  `validateListName(loc, name) (clean string, errMsg string)`.
- **`pending.go`**: `pendingImport` gains `listID int64`,
  `newListName string` and `preview importPreviewData` (decision 13).
  `base` is kept. Exactly one of `listID` / `newListName` is set.
- **`import.go`**:
  - `importFormData` gains `Lists []store.List`, `Target string` (`""`,
    `"new"` or an ID as typed), and `NewName string`.
    `renderImportForm` loads `Lists` and gets the typed values (the
    confirm-side callers pass empty values).
  - `handleImportPreview` reads `list` and `list_name` with `r.FormValue`
    after `FormFile` and validates them (decision 7) before parsing the
    file.
  - `ImportFamilies` dry run with the target. A store `ErrNotFound` gives
    422 with `lists.error.not_found`, and `ErrNameTaken` gives 422 with
    `lists.error.name_taken`, both on the import form with typed values.
  - `importPreviewData` gains `NewList bool`, `ListName string` and
    `Error string` (set only on the decision-13 re-render).
  - `handleImportConfirm`: the order of decision 13, then the outcomes of
    decision 7. Log `list_id`, not the name.
  - History and `Done`: list name column and "done" message with list
    (decision 7).
- **`children.go`**:
  - `renderChildren(w, r, status, listID int64, errMsg)`. `listID == 0`
    means the default list. An unknown list falls back to the default with
    404 and `lists.error.not_found`.
  - `childrenData` gains `List store.List` and `Lists []store.List`
    (selector shown if `len > 1`).
  - Version first (`ListVersion`), then `Children`, `SavedSelection`, and
    `Lists`.
  - `handleChildren` parses `?list=`: absent gives 0; a non-positive or
    non-integer value counts as unknown.
- **`compose.go`**:
  - Parse `list` (positive integer, else 409 stale on the default list).
  - `SelectedChildren(ctx, listID, version, ids)` and
    `SaveSelection(ctx, listID, ...)`.
  - `composeData` gains `ListID` and `ListName`, read with `store.List`
    for the "Liste « L »" line (display only).
  - Every `renderChildren` call passes the posted `listID`.
- **`lists.go`** (new), modeled on `templates.go`:
  - `GET /lists/{id}/rename` shows a form.
  - `POST /lists/{id}/rename` validates (422 with typed value) and
    redirects with 303 to `/?renamed=1`.
  - `GET /lists/{id}/delete` shows the confirmation page.
  - `POST /lists/{id}/delete` deletes and redirects with 303 to
    `/?deleted=1`.
  - A body limit (`MaxBytesReader`, 1 MB) applies to each POST.
  - The `{id}` path value is parsed by the shared positive-ID helper:
    rename `parseTemplateID` to `parsePathID` (gopls rename) and reuse it.
  - Unknown ID: 404 on the home page with `lists.error.not_found`.
- **`server.go`**:
  - Pages `list_rename` and `list_delete`, plus the 4 routes.
  - `handleHome` uses `Lists` and the `renamed` / `deleted` notices
    (`lists.renamed`, `lists.deleted`). Factor out a
    `renderHome(w, r, status, errMsg, notice)` like `renderTemplates`.

### `web/templates`

- **`home.html`**:
  - A lists table (decision 10) with entries/addresses labels per list:
    `common.families` and `common.emails` when `Families > 0`, else
    `common.children` and `common.parents`.
  - Notice and error alerts.
  - The empty state keeps `home.empty` and the import button.
- **`import.html`**:
  - Target select and name field (decision 7), with labels. The
    `import.replace_note` text is updated.
  - The history gets a "List" column showing `import.list_deleted` for
    deleted lists.
  - The "done" message names the list and links to it.
- **`import_preview.html`**:
  - Under the title, `preview.target_new` or `preview.target_replace` with
    the list name.
  - An error alert when `.Error` is set (decision 13).
  - Confirm form: for a replacement, the required checkbox with its label,
    and the replace button texts; for a new list, the current
    `preview.confirm` button (decision 13). The deletion list and red
    button rule are unchanged.
- **`children.html`**:
  - A subtitle with the list name and the GET selector form (2 or more
    lists), using the existing `.toolbar` / `.inline` styles.
  - A hidden `list` input in the selection form.
  - The "import" link in the empty state is unchanged.
- **`compose.html`**: hidden `list`, the "Liste « L »" line, and the back
  link `/children?list={{.ListID}}`.
- **`list_rename.html`**, **`list_delete.html`** (new), modeled on
  `template_form.html` / `template_delete.html`.
- `web/static/children.js`: **no change**. The selector is a separate GET
  form, and the script only touches the selection table.
- CSS: reuse existing classes; add a style only if the checkbox label needs
  spacing (`web/static`).

### `web/locales` (both files, same keys)

New:

| Key | fr | en |
|---|---|---|
| `lists.title` | Listes de familles | Family lists |
| `lists.col.name` | Liste | List |
| `lists.col.entries` | Enfants / familles | Children / families |
| `lists.col.addresses` | Parents / adresses | Parents / addresses |
| `lists.col.last_import` | Dernier import | Last import |
| `lists.no_import` | — | — |
| `lists.choose` | Choisir | Choose |
| `lists.rename` | Renommer | Rename |
| `lists.delete` | Supprimer | Delete |
| `lists.name_label` | Nom de la liste | List name |
| `lists.rename_title` | Renommer la liste | Rename the list |
| `lists.save` | Enregistrer | Save |
| `lists.cancel` | Annuler | Cancel |
| `lists.renamed` | Liste renommée. | List renamed. |
| `lists.delete_title` | Supprimer la liste | Delete the list |
| `lists.delete_confirm` | Supprimer la liste « %s » ? Ses %d ligne(s) (enfants ou familles) et %d adresse(s) email seront supprimées. L'historique des imports est conservé. | Delete the list “%s”? Its %d row(s) (children or families) and %d email address(es) will be deleted. The import history is kept. |
| `lists.delete_button` | Supprimer la liste | Delete the list |
| `lists.deleted` | Liste supprimée. | List deleted. |
| `lists.error.not_found` | Cette liste n'existe pas (ou plus). | This list does not exist (any more). |
| `lists.error.name_required` | Le nom de la liste est obligatoire. | The list name is required. |
| `lists.error.name_too_long` | Le nom de la liste est trop long (maximum %d caractères). | The list name is too long (maximum %d characters). |
| `lists.error.name_invalid` | Le nom de la liste contient des caractères non autorisés. | The list name contains characters that are not allowed. |
| `lists.error.name_taken` | Une liste porte déjà ce nom. | A list already has this name. |
| `import.target_label` | Liste | List |
| `import.target_choose` | — Choisir — | — Choose — |
| `import.target_new` | Nouvelle liste… | New list… |
| `import.new_name_label` | Nom de la nouvelle liste | Name of the new list |
| `import.new_name_note` | Seulement pour une nouvelle liste. | Only for a new list. |
| `import.error.list_required` | Choisissez la liste à remplacer, ou créez une nouvelle liste. | Choose the list to replace, or create a new list. |
| `import.error.list_gone` | La liste choisie a été supprimée entre-temps. Importez le fichier à nouveau. | The chosen list was deleted meanwhile. Import the file again. |
| `import.error.list_name_taken` | Une liste porte déjà ce nom (créée entre-temps). Importez le fichier à nouveau avec un autre nom. | A list already has this name (created meanwhile). Import the file again with another name. |
| `import.error.replace_unconfirmed` | Cochez la case de confirmation pour remplacer la liste « %s ». | Tick the confirmation box to replace the list “%s”. |
| `import.col.list` | Liste | List |
| `import.list_deleted` | %s (supprimée) | %s (deleted) |
| `import.see_list` | Voir la liste | See the list |
| `preview.target_new` | Nouvelle liste « %s » | New list “%s” |
| `preview.target_replace` | Remplace la liste « %s » | Replaces the list “%s” |
| `preview.replace_check` | Je confirme remplacer la liste « %s ». | I confirm I want to replace the list “%s”. |
| `preview.confirm_replace` | Remplacer la liste « %s » | Replace the list “%s” |
| `preview.confirm_replace_deletions` | Remplacer la liste « %s » et supprimer %d enfant(s) | Replace the list “%s” and delete %d child(ren) |
| `preview.confirm_replace_deletions_families` | Remplacer la liste « %s » et supprimer %d ligne(s) famille | Replace the list “%s” and delete %d family row(s) |
| `children.list_label` | Liste | List |
| `children.list_show` | Afficher | Show |
| `children.list_current` | Liste « %s » | List “%s” |
| `compose.list` | Liste « %s » | List “%s” |

`lists.col.entries` / `lists.col.addresses` have the same French text as
`import.col.entries` / `import.col.addresses` but a different meaning
(current counts vs. changes); keep them separate so either can change.

Changed:
- `import.replace_note` becomes "Le fichier remplace la liste choisie :
  ce qui n'est plus dans le fichier en sera supprimé. Les autres listes ne
  changent pas. Rien n'est enregistré avant votre confirmation." (en: "The
  file replaces the chosen list: whatever is no longer in the file is
  deleted from it. Other lists do not change. Nothing is saved before you
  confirm.")
- `import.done` becomes "Import de « %s » enregistré dans la liste
  « %s »." (2 arguments; en: "Import of “%s” saved in the list “%s”.").

Removed (unused after this change): `home.last_import`,
`home.children_button`, `home.families_button`,
`preview.confirm_deletions`, `preview.confirm_deletions_families`.
goexp confirms each is unused before removing it.

## Test plan

Store (temporary DB files; old schemas are built by running migration files
001–004 by hand, as in `TestMigration004KeepsExistingData`, with
`foreign_keys(1)`):

1. **Migration 005 with data**. Start from a v4 DB holding Excel children,
   family entries with and without email, two `last_selection` rows, two
   imports and a template. Check:
   - one list "Liste importée" with key = `textnorm.Key("Liste importée")`;
   - every parent and child keeps its ID and values, with that `list_id`;
   - `last_selection` is unchanged;
   - the imports have the `list_id` and `list_name`;
   - the template is untouched;
   - `PRAGMA foreign_key_check` is empty;
   - the stored schema (`sqlite_master.sql`) of `children` references
     `parents`, not `parents_new`, and `last_selection` references
     `children`;
   - a re-import into that list keeps the IDs.
2. **Migration 005 on an empty v4 DB**: no list is created, and the tables
   exist. A fresh DB is at version 5 with no list. `Open` twice is
   idempotent.
3. **Cascades after migration**: deleting a parent deletes its children;
   deleting a child deletes its `last_selection` row; deleting a list
   deletes its parents, children and selection rows only, while its imports
   remain with `list_id` NULL and `list_name` kept.
4. **Constraints**:
   - the same email in two lists is allowed, and twice in one list is
     refused;
   - the same no-email family number in two lists is allowed, and twice in
     one list is refused;
   - a child whose `parent_id` belongs to another list is refused by the
     composite foreign key.
5. **`ImportFamilies` scoping**:
   - importing into list A never changes B (rows, IDs, counts, `used_at`);
   - the counts and `DeletedChildren` only cover A;
   - the same family in A and B gives independent entries;
   - a dry run equals the real import for an existing list and for a new
     list;
   - a new list has only additions;
   - a dry run of a new list leaves no list row behind;
   - a real import sets the list's `used_at` and `updated_at`, and writes
     `imports.list_id` / `list_name`;
   - neither or both of `ListID` / `NewListName` is an error, and nothing
     is written.
6. **Stale and conflicts**:
   - `Base` mismatch after an import into the same list gives
     `ErrStaleImport`, while an import into another list does not;
   - a deleted target list gives `ErrNotFound`;
   - a new name taken in between (also re-cased or accented differently)
     gives `ErrNameTaken`, and nothing is written.
7. **List version**:
   - it is 0 for a list without imports and grows with the list's imports;
   - it is unchanged by an import into another list and by a rename;
   - it gives `ErrNotFound` after a delete;
   - a recreated list never reuses the deleted ID (AUTOINCREMENT).
8. **`SelectedChildren`**:
   - a stale version or a deleted list gives `ErrStaleSelection`;
   - IDs of another list are dropped even when the version matches.
   **`Children` / `ChildrenByIDs`**: only the given list's entries.
9. **`SaveSelection` / `SavedSelection`**:
   - saving for A leaves B's saved rows intact;
   - IDs of B posted with A are not saved;
   - no-email entries are not saved;
   - `used_at` is updated; a deleted list gives `ErrNotFound`.
10. **`Lists` / `List` / `DefaultList`**:
    - ordering by name key, counts, `LastImport` and `UsedAt`;
    - Excel and family counts;
    - default list: last import or last saved selection wins, with an `id`
      tie-break; `ErrNotFound` with no list.

    **Rename and delete**:
    - not found, name taken (also re-cased/accented), a re-cased own name
      is allowed;
    - name cleaned (extra spaces).
11. **`Imports`**: the list name of a live list (the current name after a
    rename), and the copied name with `ListDeleted` after a delete.

Server (`httptest`):

12. **Import form**:
    - no list gives the name field only (hidden `list=new`);
    - with lists, the select has the empty option selected by default, then
      "new", then the lists by name.

    **Preview errors** (422 with typed values kept and escaped), each
    checked before reading the file (a request with an invalid target and
    no file reports the target error):
    - missing choice;
    - `new` with an empty, too long (81 runes) or control-character name;
    - a name already taken (also re-cased);
    - an unknown, non-numeric or negative list ID.
13. **Preview text**:
    - "Nouvelle liste « X »" shows additions only, no checkbox, and the
      `preview.confirm` button;
    - "Remplace la liste « X »" counts only that list.

    **Replace confirmation** (decision 13):
    - the replace preview has the checkbox `name="confirm_replace"
      value="1" required`, its label and the "Remplacer la liste « X »"
      button; with deletions, the deletion list, the red button and the
      count in its text (children and family variants); the list name is
      escaped in label and button;
    - also shown for a replacement with no changes;
    - confirm of a replacement without `confirm_replace`, and with `""`,
      `"0"`, `"on"`, `"true"`: 422, the preview page with
      `import.error.replace_unconfirmed` (FR and EN), the **same token**
      in the form; the DB is unchanged (rows, list version, imports count,
      `used_at`);
    - then the same token with `confirm_replace=1` gives 303 and saves;
    - confirm of a new list without the field gives 303;
    - an expired/unknown token without the field gives the expired message
      (token check first);
    - with the field, a stale replacement still gives 409.

    **Confirm**:
    - creates the list or replaces only the target;
    - the target comes from the token, not the form (a posted `list` or
      `list_name` on confirm is ignored);
    - each case gives 409, the token is used up, and nothing is saved:
      stale (import into the same list), list deleted, name taken
      meanwhile;
    - an import into another list between preview and confirm does
      **not** block; neither does a rename of the target list.
14. **Import history**: shows the list name (current name after a rename),
    and "(supprimée)" after a delete. The "done" message names the list
    and links to it.
15. **`/children`**:
    - with no `list`, the default list (last import or last Continue) is
      shown;
    - with `?list=ID`, that list is shown;
    - an unknown, non-numeric or negative ID shows the default list with
      404 and an error;
    - with no list at all, the page shows the empty state;
    - the selector appears only with 2 or more lists, with the current one
      selected;
    - the form carries `list` and the list version;
    - only that list's remembered selection is pre-checked;
    - opening a list with GET does not change the default.
16. **`/compose`**:
    - a missing or invalid `list` gives 409;
    - a version of another list gives 409;
    - an import into the same list gives 409, while an import into
      another list still allows composing;
    - a deleted list gives 409;
    - IDs of another list are dropped and never become recipients;
    - preview and template actions keep `list`;
    - "back" links to the list;
    - the saved selection is per list, and "Continue" makes the list the
      default.
17. **Rename and delete pages**:
    - GET shows the form or confirmation (with counts), and nothing changes
      on GET;
    - POST renames or deletes with a 303 and a notice;
    - 422 on invalid or taken names, with the typed value escaped;
    - 404 for unknown and bad IDs;
    - cross-site POSTs and other hosts are refused;
    - an oversized body is refused;
    - after a delete, the home page, selection and import history behave
      as in items 3 and 11.
18. **Home**:
    - one line per list with its counts and last import;
    - "Choose" appears only for a non-empty list;
    - the empty state when there is no list;
    - French and English.
19. **Logs**: no list names, child names or emails at info level (import
    confirm, rename, delete).
20. **Locales**: the existing key-parity test passes, and the new keys
    exist in both files.

Existing tests will need updates for the new signatures (`Children`,
`ChildrenByIDs`, `SelectedChildren`, `SaveSelection`, `SavedSelection`,
`ImportFamilies` target, `DataVersion` → `ListVersion`, `Totals` removed,
`parseTemplateID` → `parsePathID`). Server tests will need the `list` /
`list_name` fields on `/import/preview`, `confirm_replace=1` on confirms of
a replacement, `list` on `/compose`, and the new `import.done` and button
wording.

## Out of scope

- Mixing several lists in one selection, and moving or copying entries
  between lists.
- Sending, batches and email history (§4.4, §4.5). For §4.4: `batches`
  stores `list_id` (`ON DELETE SET NULL`) and a `list_name` copy, and the
  send step re-checks the list version like `/compose`.
- Merging lists, archiving, per-list templates, list-level settings.
- The "Tolérence" line in SPEC §4.6 (to be specified separately).

## SPEC changes (applied 2026-11-16)

- §4.0: answers recorded, open questions removed; import target chosen
  explicitly; explicit replace confirmation; "Liste importée" only if data
  exists; "last list used"; rename/delete rules.
- §4.1: history shows the list (current name, or old name marked deleted);
  replace confirmation.
- §4.2: selector only with 2 or more lists; unknown list falls back to the
  default; what updates the default list.
- §7: planned schema, with `last_selection` keeping `child_id` only
  (decision 4).
- §11: milestone 4b references this plan. Mark it done (and update the
  CLAUDE.md status line) after delivery.

## Implementation notes

Files changed: `internal/store/migrations/005_family_lists.sql` (new, the
plan's SQL verbatim), `internal/store/lists.go` (new), `store/families.go`,
`store/children.go`, `store/templates.go`, `internal/server/names.go` (new),
`server/lists.go` (new: home, rename, delete handlers), `server/import.go`,
`server/pending.go`, `server/children.go`, `server/compose.go`,
`server/templates.go`, `server/server.go`, templates `home`, `import`,
`import_preview`, `children`, `compose`, `list_rename` (new), `list_delete`
(new), `web/locales/{fr,en}.json`, `web/static/style.css`.

Deviations and details:
- `handleImportPreview` calls `ParseMultipartForm` first, then reads
  `list` / `list_name`, validates the target, and only then calls `FormFile`.
  The plan said "FormValue after FormFile", which cannot report a target error
  when the file is missing (test plan item 12 requires that).
- The "new name already taken" check runs in the handler from `Lists`
  (before the file is parsed). The dry run's `ErrNameTaken` / `ErrNotFound`
  are still mapped to 422 (`lists.error.name_taken` / `not_found`) as a
  backstop.
- `ImportSummary.ListID` is set for every existing-list import (dry run
  included) and for a real import of a new list; it is 0 only for a dry run
  of a new list (the row is rolled back).
- `renderChildren(w, r, status, listID, errMsg)`: `listID == 0` is the
  default list; `handleChildren` passes -1 for a present but invalid `?list=`
  (matches no row, so 404 + `lists.error.not_found`). An unknown list with a
  caller-supplied error (compose 409/422) keeps that status and error and
  shows the default list.
- `/compose`: `listID` is parsed right after `ParseForm` and used by every
  error re-render; the "none selected / too many" 422 checks keep their
  place before the list/version check (a missing or invalid `list` is a 409
  after them, like a bad `version`). A tampered ID of another list is dropped
  by the store; if it was the only ID the answer is 422 `none_selected`.
- `/import/confirm` body is limited to 1 MB (`maxConfirmBytes`).
- Import form: the name field has `maxlength="80"` (literal, mirrors
  `maxNameLen`; the server is the authority).
- CSS: three rules added (`.card form .field`, `.confirm-check`).
- Existing migration index names are reused for the rebuilt `children`
  indexes; new ones are `children_list` and `imports_list`.
- Not done (archi's follow-up): SPEC §11 / CLAUDE.md status update.

For the tester:
- Migration 005 was verified by hand on a v4 DB (parents/children/family
  entry/no-email entry/last_selection/import/template): one list "Liste
  importée" (`name_key = 'liste importee'`), same IDs, `last_selection`
  kept, imports got `list_id` / `list_name`, template untouched,
  `PRAGMA foreign_key_check` empty, `integrity_check` ok, stored schema
  references `"parents"` / `"children"`.
- `pendingImport.preview` is stored with an empty `Token`; the 422 re-render
  sets `Token` and `Error`.
- `store.checkNameFree` now takes the query constant; `ErrNameTaken` text is
  "name already used".
- `server.checkName` returns `"" | "invalid" | "required" | "too_long"`
  (constants `nameInvalid`, `nameRequired`, `nameTooLong`).
- Test files that no longer compile (signature changes): store
  `children_test.go`, `family_entries_test.go`, `last_selection_test.go`,
  `store_test.go` (Totals/DataVersion), `templates_test.go`; server
  `children_test.go`, `compose_test.go`, `csv_import_test.go`,
  `server_test.go`. Tests that compile but will need the new fields
  (`list` / `list_name` on preview, `confirm_replace=1`, `list` on compose,
  new `import.done` / button wording) are in `server/last_selection_test.go`,
  `server/templates_test.go` and the files above.

### Fix round 1

- `handleListRename` now uses `loadList` first (404 before 422); the
  `ErrNotFound` from `RenameList` is still handled for the race.
- `store.DeleteList` runs in one transaction: `UPDATE imports SET list_name =
  (SELECT name FROM lists WHERE id = ?) WHERE list_id = ?`, then the
  `DELETE` (RowsAffected 0 gives `ErrNotFound`). History shows the last
  name after rename then delete (answer 8). Plan decisions 6/11 and SPEC
  §4.0 updated.
- `pendingImports.put` caps the map at `maxPendingImports` = 8: expired
  entries are dropped, then the entry with the earliest expiry is evicted.
- Tester: `internal/server/lists_test.go` (~645-650) still expects the old
  copied name and must be updated; add tests for the cap and for the
  rename-then-delete history.
