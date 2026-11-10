# Cafeteria Management

Small local app: import an Excel file of parents / children / parent emails,
select children, and email their parents with a custom message.
Single user, on their own computer (Windows or macOS).

**`docs/SPEC.md` is the source of truth** for features, data model and
technical choices. Read it before implementing anything, and update it when a
decision changes.

## Status

Skeleton only: HTTP server + placeholder page. No features yet. See the
milestones in `docs/SPEC.md` §11.

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
internal/server/   HTTP routes and handlers
internal/...       store, importer, mailer, i18n (to come, see SPEC §9)
web/               templates and static files, embedded in the binary (web.FS)
docs/SPEC.md       specification
```

## Key rules

- Go, standard library first. Dependencies must be **pure Go (no CGO)** so
  the Windows `.exe` can be cross-compiled from macOS.
- UI: server-rendered `html/template` + htmx. Server listens on `127.0.0.1` only.
- Keep the app switchable to a Wails desktop window later (SPEC §9.1): logic
  in `internal/`, plain forms, no popups or new tabs.
- UI text in French and English through i18n files, never hard-coded
  (the placeholder page is the only exception for now).
- Excel import: the file is the source of truth. One transaction, saved only
  after the user confirms the preview; entries missing from the file are
  deleted; email history is never affected (SPEC §4.1).
- Children's data is personal: no names/emails in info logs, secrets in the
  OS keychain only.

## Tools

- `gopls` MCP server (configured in `.mcp.json`): prefer its tools to grep for
  Go code navigation (symbol references, package API, diagnostics), and check
  its diagnostics after editing Go files.

## Agents

- `goexp` (`.claude/agents/goexp.md`): Go expert with the project's coding
  practices. Use it for writing or reviewing Go code.

## Open points

- Excel file format not confirmed yet (SPEC §10).
- Outlook needs OAuth2 (Microsoft is phasing out password SMTP login).
