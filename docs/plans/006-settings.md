# 006 — Settings (storage, page, interface language)

Status: **approved (2026-11-17)** · SPEC §4.6 (and §6, §7, §8, §5.4)

Implement before plan 007: this plan adds migration **007**, plan 007 adds
migration 008.

## Goal

The user gets a **Paramètres** page (new nav item) where they choose the
**interface language**: "Automatique (langue du navigateur)", "Français" or
"English" (SPEC §4.6, §6). The choice is saved in the database, so it
survives a restart and is backed up with `app.db` (SPEC §3). The same page
says how emails are sent today (test mode, plan 005), so the "email
account" part of §4.6 has its place, ready for Microsoft 365 (milestone 6).

Under the hood this plan adds the `settings` table listed in SPEC §7 (never
created so far) and a small store API to read and save settings, which
milestone 6 will extend.

## User answers (2026-11-17)

1. "Tolérence" = a flag set by the user **per family number** on the
   selection page, shown in red, remembered, sortable. → its own plan,
   `docs/plans/007-tolerance.md` (decision 6).
2. Scope as recommended: settings storage, page, language, read-only
   sending line now; provider, sender and test email deferred.
3. Language: Automatique (default), Français, English (today's behaviour
   as the default).
4. Keep the read-only "Envoi des emails" line showing test mode.
5. **No new milestone** (no 5d); the milestone list stays as it is.
6. **No SMTP for now**: milestone 6b and the SMTP fields of §4.6 are
   removed from the SPEC; SMTP is listed in §12 (out of scope for now).

## Scope decision

SPEC §4.6 listed: provider choice (Microsoft 365, Gmail, custom SMTP: host,
port, TLS mode, username), sender name and address, "Send test email",
interface language, and a rough "Tolérence" line. Since plan 005, SPEC §5.4
changed the picture:

- **Microsoft 365 / Graph (milestone 6)** cannot be used for now: the app
  cannot be registered in the organization's Entra ID (§5.4). With Graph,
  the sender is the connected account itself (`Message` has no `From`,
  §5.3), so "sender name / address" is not a setting there.
- **SMTP**: password SMTP to Microsoft 365 and sending work emails through
  a personal account (Gmail) are ruled out by §5.4, and no approved relay
  is known. Dropped for now (answer 6).
- **The §5.4 workarounds (milestone 5c)** send from the user's own Outlook:
  no provider, sender or password setting at all.
- **"Send test email"** needs a real sender: with the test sender it would
  only add a row to the history, which proves nothing.

| §4.6 item | Here? | Why |
|---|---|---|
| Settings storage (`settings` table, store API) | **yes** | needed by every item; small; already in SPEC §7 |
| Settings page + nav item | **yes** | the place every later item goes |
| Interface language | **yes** | useful now, independent of providers; SPEC §6 says "chosen in settings" |
| Current sending mode (read-only: test mode) | **yes** | one line; tells the user why there is no account to set (answer 4) |
| Microsoft sign-in, connected account, "send test email" | no | milestone 6 |
| Provider choice, SMTP fields, sender name and address | no | SMTP not planned (answer 6); Graph needs no sender setting |
| Passwords, OAuth tokens | no | OS keychain only (§8), with milestone 6 |
| "Tolérance" | no | plan 007 (answer 1) |

## Decisions

1. **Settings are key / value rows in a new `settings` table**
   (migration 007), as SPEC §7 lists (`settings key, value`), plus
   `updated_at` like the other tables.
   - Adding a setting later (Microsoft account display…) needs no
     migration.
   - The store exposes a **typed** `Settings` struct, so callers never
     handle raw keys; unknown keys in the table are ignored (a downgrade
     never breaks), missing keys give the zero value (= default).
   - **Secrets never go in this table** (§8); stated in the migration
     comment and the type's doc comment.
   - Rejected: a single-row table with one column per setting (each new
     setting is a migration; SPEC §7 says key/value); a JSON file next to
     `app.db` (a second file to back up, no transaction).

2. **Language values**: `""` (automatic, the default), `"fr"`, `"en"`.
   - Automatic = today's behaviour: the browser's `Accept-Language`
     (`i18n.Bundle.Match`), French as fallback. In a browser tab the
     browser language is what the app sees; in a Wails window it is the
     webview's, i.e. the system's (SPEC §6).
   - A fixed language wins over the browser's.
   - The store keeps whatever string it is given; **validation is in
     `i18n`** (`i18n.Parse`), which knows the supported languages. A stored
     value that is not supported any more (hand-edited DB, downgrade) is
     treated as automatic, never an error.
   - The language also drives what already follows the interface
     language: `<html lang>`, dates (`Localizer.Date`), and the joining word
     "et" / "and" of `{{parent_name}}` / `{{family_number}}` (§4.3, §6).
     Emails already confirmed keep their stored text (plan 005): changing
     the language never changes history.

3. **The server keeps the language in memory, loaded once at start.**
   - `localizer(r)` is called several times per request; reading the
     database each time would add queries to every page for a value that
     only this process changes.
   - `server.New` gains a `ctx` first parameter (I/O, conventions) and
     reads `Settings` once; a read failure makes `New` fail (as a broken
     database does today at startup).
   - `POST /settings` saves to the store **first**, then updates the
     in-memory value only if the save succeeded. The value is guarded by a
     `sync.RWMutex` (or an `atomic.Value`; goexp's choice), since handlers
     run concurrently (`-race`).
   - Rejected: a DB read in every `localizer` call (N reads per page, and
     noise in logs when the DB fails); a cookie (not in the backup, unclear
     persistence in a future Wails webview, and SPEC §6 puts the choice in
     settings).

4. **Settings page: `GET /settings`, `POST /settings`.**
   - Section "Langue de l'interface": a radio group (`name="language"`,
     values `auto`, `fr`, `en`), the current choice checked. The automatic
     option shows what the browser asks for: "Automatique (langue du
     navigateur : Français)". The language names are shown in their own
     language ("Français", "English") in both locale files. A short help
     line says the choice also sets the word "et" / "and" of placeholders.
   - Section "Envoi des emails" (read-only): when `queue.Provider()` is the
     test provider, the existing `send.test_banner` text, plus one line
     saying the email account will be chosen here once real sending exists.
     No form field.
   - `POST /settings`: `MaxBytesReader` (a small limit, e.g. 64 KiB),
     `ParseForm`; `language` must be `auto`, `fr` or `en` (`auto` is stored
     as `""`); anything else, or a parse error → **422**, the page again
     with "Formulaire invalide" and the **current** (unchanged) choice;
     nothing saved. Valid → `SaveSettings`, update the in-memory value,
     **303** to `/settings?saved=1` (existing notice-after-redirect
     pattern); that GET is already rendered in the new language.
   - Plain form, no JS, no popup: Wails-compatible (§9.1). CSRF and
     localhost protection come from the existing middleware.
   - Nav item "Paramètres" / "Settings" last in the nav, active item
     `settings`.
   - Log on save: `slog.Info("settings saved")` with the language code only.

5. **Milestones unchanged** (answer 5). This plan is referenced from SPEC
   §4.6 and §6 only. Milestone 6b (SMTP) is removed (answer 6).

6. **"Tolérance" is plan 007**, not this one: it is on the selection page,
   per family number, and touches the import and the selection page JS.

## Changes

### `internal/store`

- **`migrations/007_settings.sql`** (new):

  ```sql
  -- App settings (SPEC §4.6, §7): one row per key, read through the typed
  -- store.Settings. Not touched by imports. Secrets (passwords, OAuth
  -- tokens) never go here: OS keychain only (SPEC §8).
  CREATE TABLE settings (
      key        TEXT PRIMARY KEY,
      value      TEXT NOT NULL,
      updated_at TEXT NOT NULL
  );
  ```

- **`settings.go`** (new):

  ```go
  // Settings are the user's app settings (SPEC §4.6). The zero value is the
  // default for every field. Secrets never go here (SPEC §8).
  type Settings struct {
      // Language is the interface language code ("fr", "en"), or "" to
      // follow the browser. Not validated here: see i18n.Parse.
      Language string
  }

  // Settings reads the settings. Missing keys keep their zero value;
  // unknown keys are ignored.
  func (s *Store) Settings(ctx context.Context) (Settings, error)

  // SaveSettings writes every field of st in one transaction (upsert per
  // key, updated_at set).
  func (s *Store) SaveSettings(ctx context.Context, st Settings) error
  ```

  - Key constant (unexported): `settingLanguage = "language"`.
  - Upsert with `INSERT … ON CONFLICT(key) DO UPDATE SET value = excluded.value,
    updated_at = excluded.updated_at`; parameterized; only `tx` inside the
    transaction; time from `s.timestamp()`.

### `internal/i18n`

- `Parse(code string) (Lang, bool)`: true only for a supported language,
  exact lower-case code (`"fr"`, `"en"`). Used for the stored value and the
  form value. No other change (`Match`, `Localizer` unchanged).

### `internal/server`

- **`server.go`**:
  - `New(ctx context.Context, version string, st *store.Store, q queue)`:
    reads `st.Settings(ctx)`, keeps the language (`i18n.Lang`, empty =
    automatic) in a mutex-guarded field (decision 3).
  - `localizer(r)`: the stored language if `i18n.Parse` accepts it, else
    `Match(Accept-Language)` as today.
  - Page `settings` in `pageNames`; routes `GET /settings`,
    `POST /settings`.
- **`settings.go`** (new): `handleSettings`, `handleSettingsSave`,
  `settingsData` (`Language string` — `auto`/`fr`/`en` for the checked
  radio; `BrowserLang string` — localized name of `Match(Accept-Language)`;
  `Test bool` — `s.queue.Provider() == mailer.ProviderTest`; `Notice`,
  `Error`). Constant `maxSettingsFormBytes`.

### `cmd/cafeteria/main.go`

- `server.New(ctx, version, st, q)`. Wiring only.

### `web/templates`

- `settings.html` (new): title, notice / error alerts, the language form
  (fieldset + radios + save button), the read-only sending section.
- `layout.html`: nav item `nav.settings` (`/settings`, active `settings`).

### `web/static/style.css`

- Only if needed for the radio group spacing (reuse `.card`, `.field`,
  `.actions`).

### `web/locales` (both files, same keys)

| Key | fr | en |
|---|---|---|
| `nav.settings` | Paramètres | Settings |
| `settings.title` | Paramètres | Settings |
| `settings.language.legend` | Langue de l'interface | Interface language |
| `settings.language.auto` | Automatique (langue du navigateur : %s) | Automatic (browser language: %s) |
| `settings.language.fr` | Français | Français |
| `settings.language.en` | English | English |
| `settings.language.help` | La langue choisie s'applique aussi au mot « et » des champs qui listent plusieurs valeurs. | The chosen language also sets the word “and” in placeholders that list several values. |
| `settings.sending.title` | Envoi des emails | Sending emails |
| `settings.sending.later` | Le compte email utilisé pour l'envoi se choisira ici quand l'envoi réel sera disponible. | The email account used for sending will be chosen here once real sending is available. |
| `settings.saved` | Paramètres enregistrés. | Settings saved. |
| `settings.error.invalid` | Formulaire invalide : rien n'a été enregistré. | Invalid form: nothing was saved. |

Reused: `lists.save` ("Enregistrer" / "Save"), `send.test_banner`.

### Docs (applied by archi, 2026-11-17; goexp does not edit them)

- SPEC §4.6 rewritten: status (plan 006), settings page, language,
  read-only sending line; Microsoft sign-in and "send test email" later
  (milestone 6); SMTP not planned; the "Tolérence" line moved to §4.2
  (plan 007).
- §5 (provider table), §5.2, §5.3, §9: SMTP marked not planned.
- §6: the three language choices, stored in the database.
- §7: `settings` key (PK), value, updated_at; keys; never secrets
  (migration 007).
- §8: secrets list without the SMTP password.
- §11: milestone 6b removed; nothing added.
- §12: SMTP sending and its settings out of scope for now.
- `CLAUDE.md` status line: after delivery.

## Test plan

**`store`**
- Migration 007 on a v6 DB with data (lists, entries, templates, batches):
  existing rows untouched; fresh DB at version 7; `PRAGMA
  foreign_key_check` empty.
- `Settings` on a fresh DB → zero value, no error.
- `SaveSettings` then `Settings` → same value; saving twice (upsert) keeps
  one row per key with the last value and a new `updated_at`; saving `""`
  stores `""` (back to automatic).
- An unknown key inserted by hand is ignored by `Settings` and kept by
  `SaveSettings`.
- A value is stored as given (no validation in the store), e.g. `"de"`.
- Settings survive `Close` + `Open` of the same file.

**`i18n`**
- `Parse`: `"fr"`, `"en"` → ok; `""`, `"auto"`, `"de"`, `"FR"`, `" fr"`,
  `"fr-FR"` → not ok.

**`server`** (`httptest`, temp store)
- `GET /settings`: 200, nav item active; with nothing stored the
  automatic radio is checked and shows the browser language (FR and EN
  `Accept-Language`); with `en` stored, the English radio is checked.
- `POST /settings` `language=en` with `Accept-Language: fr` → 303 to
  `/settings?saved=1`; store has `en`; the next GET of any page is in
  English (`<html lang="en">`, nav text) and shows the notice.
- `language=auto` after `en` → store has `""`; pages follow
  `Accept-Language` again.
- Invalid value (`de`, empty, missing field) → 422, error shown, store and
  in-memory language unchanged (next page still in the old language).
- Oversized body → 422, nothing saved.
- Cross-site POST → 403 (existing middleware); other methods → 405.
- A stored unsupported value (`de`, written through the store) → pages
  follow `Accept-Language`, no error.
- A new `Server` built on the same DB (restart) starts in the saved
  language.
- The joining word follows the setting: family data with two families on
  one address, browser `fr`, setting `en` → compose preview shows
  "… and …".
- The sending section shows the test banner with the test provider.
- Concurrent `GET` pages and `POST /settings` under `-race`: no race.
- Logs on save carry only the language code.
- Locale key parity (existing test).

**Existing tests to update**: `server.New` gains the `ctx` argument (the
two helpers in `server_test.go` and `send_test.go`).

**`cmd/cafeteria`**: no unit test; by hand with `make run`: change the
language, restart, check it is kept.

## Out of scope

- Microsoft sign-in, connected account, "send test email", keychain
  (milestone 6).
- SMTP providers and their settings (SPEC §12).
- A test-mode switch (there is no other sender yet).
- The "Tolérance" flag (plan 007).
- Languages other than French and English.
- Other settings not in the SPEC.

## Implementation notes

Files: `internal/store/migrations/007_settings.sql`, `internal/store/settings.go`,
`internal/i18n/i18n.go` (`Parse`), `internal/server/server.go` (`New(ctx, ...)`,
RWMutex-guarded `lang`, `localizer`, routes, page), `internal/server/settings.go`,
`web/templates/settings.html`, `web/templates/layout.html` (nav),
`web/locales/{fr,en}.json`, `cmd/cafeteria/main.go`.

No deviation from the plan. No CSS change was needed.

For the tester:
- `New` now takes `ctx`; the two helpers `server_test.go` and `send_test.go` were updated only for that.
- `TestMigration006OnV5Database` and `TestFreshDatabaseIsAtVersion6` (`internal/store/batches_test.go`)
  fail because the schema is now at version 7: expected, to update.
- The language shown on `/settings` for "automatic" comes from the `settings.language.fr|en` keys.
- `auto` is stored as `""`; an unsupported stored value is treated as automatic at start.
