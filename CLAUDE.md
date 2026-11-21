# Cafeteria Management

Small local app: import an Excel file of parents / children / parent emails,
select children, and email their parents with a custom message.
Single user, on their own computer (Windows or macOS).

**`docs/SPEC.md` is the source of truth** for features, data model and
technical choices. Read it before implementing anything, and update it when a
decision changes.

## Status

Done: SQLite storage with migrations, Excel import with preview (SPEC §4.1),
minimal FR/EN i18n, child list and selection (§4.2), CSV import of the
families software export (§4.1.1, plan `docs/plans/002-csv-import.md`),
email composition with templates and preview (§4.3, plan 003), named
family lists (§4.0, plan 004), sending in test mode with history (§4.4,
§4.5, plan 005), settings (§4.6, plan 006), "Tolérance" flags (§4.2,
plan 007). Microsoft 365 via Graph (§5.1, plan 008) is implemented and reviewed
(2026-11-21), waiting for IT to register the app: builds without the IDs
stay in test mode. Next: hand-test with the real IDs, then milestone 5c if
still needed.
See the milestones in `docs/SPEC.md` §11.

## Commands

```sh
make run        # start on http://127.0.0.1:8080 (go run ./cmd/cafeteria)
make test       # go test ./...
make build      # bin/cafeteria
make build-all  # Windows + macOS binaries in dist/
```

Before finishing a change: `gofmt -l .` (prints nothing), `go vet ./...`,
`go test -race ./...`.

## Layout

```
cmd/cafeteria/     entry point: flags, startup, shutdown (wiring only)
internal/server/   HTTP routes, handlers, localhost/CSRF protection
internal/store/    SQLite access; migrations in store/migrations/NNN_*.sql
internal/importer/ Excel parsing and row validation (no DB access)
internal/recipients/ groups selected children into one recipient per parent
internal/message/  placeholders: checks and rendering of the emails (no DB/HTTP)
internal/mailer/   Sender interface, test sender, background sending queue (no HTTP)
internal/keychain/ OS keychain storage, split into chunks (no HTTP/DB)
internal/microsoft/ Microsoft sign-in (OAuth2 + PKCE) and Graph sending
internal/i18n/     translations loader (Localizer.T in templates)
internal/textnorm/ name normalization for matching (case, accents, spaces)
web/templates/     layout.html + one file per page
web/locales/       fr.json, en.json (same keys, checked by a test)
web/static/        CSS
docs/SPEC.md       specification
docs/GO_CONVENTIONS.md  coding and testing rules
docs/plans/        one design plan per feature (written by archi)
```

## Key rules

- Go, standard library first. Dependencies must be **pure Go (no CGO)** so
  the Windows `.exe` can be cross-compiled from macOS.
- UI: server-rendered `html/template` + htmx. Server listens on `127.0.0.1` only.
- Keep the app switchable to a Wails desktop window later (SPEC §9.1): logic
  in `internal/`, plain forms, no popups or new tabs.
- UI text in French and English through `web/locales/*.json`, never
  hard-coded. Add every new key to both files.
- Excel import: the file is the source of truth for the chosen list. Each
  import creates a new list or replaces the chosen list only (other lists
  are never touched; replacing needs an explicit, server-checked
  confirmation). One transaction, saved only after the user confirms the
  preview; entries of that list missing from the file are deleted; email
  history is never affected (SPEC §4.0, §4.1).
- Children's data is personal: no names/emails in info logs, secrets in the
  OS keychain only.

## Tools

- `gopls` MCP server (configured in `.mcp.json`): prefer its tools to grep for
  Go code navigation (symbol references, package API, diagnostics), and check
  its diagnostics after editing Go files.

## Agents and workflow

Coding rules for everyone: `docs/GO_CONVENTIONS.md`.

Features go through four agents (`.claude/agents/`), in this order:

1. `archi` (Opus, high effort): designs the feature, writes
   `docs/plans/NNN-name.md`, gets the user's approval, then runs the others.
   It must be the main agent of the session (`claude --agent archi`),
   because subagents cannot start other subagents.
2. `goexp`: Go developer, implements production code from the plan.
   Never writes tests.
3. `tester`: writes unit tests (`*_test.go` only). Never changes production
   code; reports bugs back.
4. `reviewer`: read-only review, security first; also checks duplicated
   code and missed reuse of existing code. Verdict approved / changes
   required.


## Open points

- The real file is the families software's CSV export (SPEC §4.1.1); the
  Excel parent/child layout is still unconfirmed (SPEC §10).
- Email account is Microsoft 365: send via Microsoft Graph with OAuth2
  (SPEC §5.1). Unknown yet whether IT admin consent is needed.
