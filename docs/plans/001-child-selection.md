# 001 — Child list and selection

Status: done (2026-11-16) · reviewed: approved, 1 fix round · SPEC §4.2 (and the grouping rule of §4.4)

## Goal

The user sees every child with their parent and email, searches the list,
ticks children, and sees live how many emails that means. Submitting shows the
list of recipients, one per parent, which is where the email form (§4.3) will
be added next.

## Decisions

1. **Search and selection run in the browser** (small vanilla JS file), on a
   list rendered once by the server.
   - Why: a school has a few hundred children at most, so filtering in the
     page is instant, and ticked boxes stay ticked while the user changes the
     search. Doing it server-side with htmx would need to carry the selection
     across searches (hidden inputs or server state) for no gain.
   - Without JavaScript the page still works: full list, checkboxes, submit.
     Only the search box, the select buttons and the live counter need JS.
2. **One row per child + parent pair**, as stored (SPEC §4.1). A child listed
   with two parents appears on two rows, each showing its parent and email.
   - Why: merging rows by child name could merge two different children with
     the same name and email the wrong family about them. Showing the parent
     on each row keeps the choice explicit.
3. **Accent-insensitive search** on child name, parent name and email. The
   server computes each row's search text with `textnorm.Key` (reuse); the JS
   only normalizes what the user types (NFD + strip diacritics + lower case).
4. **"Select all / none" act on the visible (filtered) rows only.**
5. **Counter**: "N enfant(s) sélectionné(s) → M email(s)". M = number of
   distinct parent emails among ticked rows (the §4.4 rule: one email per
   parent). The texts come from the locale files through `data-` attributes;
   no text is hard-coded in JS.
6. **Submit = `POST /compose`** with the ticked `child` IDs (POST, so the
   selection is not in the URL or browser history). The page shows the
   recipients grouped by parent: parent name, email, their selected children.
   §4.3 will add the message form to this page. Until then it ends with a
   "back to the list" link.
7. **Grouping logic lives in a new package `internal/recipients`**, because
   the sending step (§4.4) will reuse it. It has no DB or HTTP code.
8. **Selected IDs that no longer exist** (an import ran in between) are
   ignored and the page says how many were dropped. If none remain, back to
   the list with an error.
9. Class filter (§4.2 mentions it "if present in the file") is **out of
   scope**: the file has no class column for now.

## Changes

### `internal/store`
- New file `children.go`:
  - `type Child struct { ID int64; Name string; ParentID int64; ParentName, Email string }`
  - `func (s *Store) Children(ctx) ([]Child, error)`: all children joined with
    their parent, ordered by `children.name_key`, then `parents.email`.
  - `func (s *Store) ChildrenByIDs(ctx, ids []int64) ([]Child, error)`: same
    shape and order, only the given IDs; unknown IDs are silently skipped;
    empty `ids` → empty result without querying. Use a parameterized
    `IN (?, ?, …)` built from the number of IDs (placeholders only, never
    values, in the SQL string).
- No migration needed.

### `internal/recipients` (new)
- `type Recipient struct { ParentID int64; ParentName, Email string; Children []string }`
- `func Group(children []store.Child) []Recipient`: one recipient per
  `ParentID`, children names in the input order, recipients ordered by
  parent name key (`textnorm.Key`), then email.

### `internal/server`
- `GET /children` → `handleChildren`: renders `children.html` with all
  children; each row carries `data-search` (= `textnorm.Key` of child name,
  parent name and email joined by spaces) and `data-email`.
- `POST /compose` → `handleCompose`:
  - `http.MaxBytesReader` (1 MB) on the body.
  - Parse every `child` value as a positive int64; invalid values are
    ignored; duplicates removed.
  - Load with `ChildrenByIDs`, group with `recipients.Group`, render
    `compose.html`.
  - No valid child left → render `children.html` with status 422 and the
    error `children.error.none_selected`.
- Add `children` and `compose` to `pageNames`.
- Nav: new item "Enfants" (`/children`), `Active` = `"children"`; the
  compose page also highlights it.

### `web/templates`
- `children.html`: title, search input, "Tout sélectionner / Tout
  désélectionner" buttons, counter, table (checkbox, child, parent, email),
  submit button "Continuer". Empty database → message + link to `/import`.
  A "no result" line shown by JS when the search matches nothing.
- `compose.html`: title, "M emails will be sent", optional "N children no
  longer exist" notice, table of recipients (parent, email, children), link
  back to `/children`.
- `layout.html`: nav item.
- `home.html`: when there are children, add a link to `/children`.

### `web/static`
- `children.js` (vanilla, no dependency, loaded with `defer` only on the
  children page): filtering, select visible / none, counter update, "no
  result" line. Uses `textContent`, never `innerHTML`.
- `style.css`: styles for search bar, counter, hidden rows.

### `web/locales` (both files)
`nav.children`, `children.title`, `children.search_label`,
`children.search_placeholder`, `children.select_all`, `children.select_none`,
`children.counter` (with `{children}` and `{emails}` placeholders, replaced in
JS), `children.no_result`, `children.empty`, `children.continue`,
`children.col.child`, `children.col.parent`, `children.col.email`,
`children.error.none_selected`, `home.children_button`, `compose.title`,
`compose.emails_count`, `compose.dropped`, `compose.col.parent`,
`compose.col.email`, `compose.col.children`, `compose.back`,
`compose.next_step` (short note that the message form comes next).

## Test plan

**store**
- `Children`: order by name ignoring accents/case (Émile before Zoé, "léa"
  and "Léa" adjacent), then by email; includes parent fields; empty DB →
  empty slice.
- `ChildrenByIDs`: subset only; unknown IDs skipped; empty input → empty
  result; same ordering as `Children`.

**recipients**
- Two children of one parent → one recipient with both names, input order.
- Same child name under two parents → two recipients.
- Ordering by parent name key, then email; empty input → empty result.

**server**
- `GET /children`: lists rows with correct `data-search` (accents removed)
  and `data-email`; empty DB shows the import link; English page in English.
- `POST /compose`: groups correctly (3 children, 2 parents → 2 recipients);
  invalid and duplicate IDs ignored; deleted IDs reported as dropped;
  nothing valid → 422 with the error message; cross-site POST rejected
  (403); oversized body rejected.
- Locale files keep identical keys (existing test).

**Not unit-tested**: `children.js` behavior (no JS test setup). To be covered
later by Playwright e2e tests.

## Out of scope

- Class or other filters, sorting by column, pagination.
- Remembering the selection across visits.
- The email form, templates and sending (§4.3–4.4).

## Resolved questions

- A child with two parents stays **two rows** (decision 2), confirmed by the
  user.

## Implementation notes

Files changed
- `internal/store/children.go` (new): `Child`, `Children`, `ChildrenByIDs`. Order: `c.name_key, p.email, c.id` (id added as a stable tie-break).
- `internal/recipients/recipients.go` (new): `Recipient`, `Group`.
- `internal/server/children.go` (new): `handleChildren`, `handleCompose`, `parseIDs`, view types.
- `internal/server/server.go`: pages `children`, `compose`; routes `GET /children`, `POST /compose`.
- `web/templates/children.html`, `compose.html` (new); `layout.html` (nav item + `{{block "scripts"}}` in `<head>`); `home.html` (children button when `Totals.Children > 0`).
- `web/static/children.js` (new), `web/static/style.css` (toolbar, counter, `[hidden]`, `.alert-warning`, `.visually-hidden`).
- `web/locales/fr.json`, `en.json`: all plan keys plus one extra, `children.col.select` (accessible header of the checkbox column).

Deviations
- Extra key `children.col.select` (screen-reader header); no other deviation.
- `compose.dropped` count = valid distinct requested IDs minus IDs found.
- A form parse error on `/compose` (e.g. body over 1 MB) is answered like "nothing selected": 422 and `children.error.none_selected`.

Notes for the tester
- Toolbar and counter are `hidden` in the HTML and un-hidden by JS, so the no-JS page is just list + checkboxes + submit. The "no result" row is always in the HTML, `hidden`.
- `data-search` = `textnorm.Key(child + " " + parent + " " + email)`.
- Counter text template is in `data-template` of `#counter` (`{children}` / `{emails}` unreplaced).
- `ChildrenByIDs(nil)` returns an empty non-nil slice without querying. Compose with only invalid/unknown IDs gives 422 (children page).
- `Recipient.Children` keeps the store order (child name key, then email).
- Existing tests all pass; none needed changes.

### Fix round 1

- `POST /compose`: more than `maxSelectedChildren` (5000) distinct valid IDs gives 422 on the children page with `children.error.too_many`; `ChildrenByIDs` unchanged.
- `ParseForm` error: `*http.MaxBytesError` gives 422 with new `children.error.too_large`; other errors keep `none_selected` (logged with `slog.Warn`, error only). This replaces the earlier deviation note.
- `children.js`: normalization comment clarified (not identical to `textnorm.Key` for all Unicode case).
- New keys in `fr.json` and `en.json`: `children.error.too_many`, `children.error.too_large`.
