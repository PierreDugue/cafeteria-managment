# Go conventions

Coding rules for this project. Shared by the `goexp`, `tester` and `reviewer`
agents, and by humans. `docs/SPEC.md` stays the source of truth for *what* to
build; this file is about *how*.

## General

- Prefer the smallest change that fully solves the task. No features,
  options or abstractions that were not asked for.
- Read the existing code you touch and follow its patterns.

## Style

- Code is `gofmt`-clean and passes `go vet ./...`.
- Follow Effective Go and the Go Code Review Comments: short clear names,
  `MixedCaps`, no stutter (`store.Store`, not `store.StoreService`), acronyms
  in one case (`ID`, `URL`, `HTTP`).
- Doc comments on every exported identifier, starting with its name.
- Comments explain *why*, not *what*.

## Design

- Standard library first. Add a dependency only if the spec lists it or it
  clearly saves real work; it must be pure Go (no CGO) to keep cross-compiling.
- Packages by responsibility under `internal/`. `cmd/` only wires things.
- Small interfaces, defined by the package that uses them. Accept interfaces,
  return concrete types.
- No package-level mutable state. Pass dependencies explicitly (constructors).
- Zero values should be useful where it is reasonable.

## Errors

- Return errors, never `panic` for expected failures.
- Wrap with context: `fmt.Errorf("import row %d: %w", n, err)`.
- Use `errors.Is` / `errors.As`; define sentinel errors or types only when
  callers need to branch on them.
- Handle each error once: either return it or log it, not both.

## Context and concurrency

- `context.Context` is the first parameter of anything doing I/O
  (DB, email, HTTP). Respect cancellation.
- Every goroutine has a clear owner and a way to stop. No leaks.
- Protect shared state with a mutex or channels; tests run with `-race`.

## HTTP and UI

- `net/http` with Go 1.22+ routing patterns (`"GET /children/{id}"`).
- `html/template` only (auto-escaping). Never build HTML by string
  concatenation.
- Validate and limit input (`http.MaxBytesReader` for uploads).
- Listen on `127.0.0.1` only; keep the localhost and cross-origin protection
  in `internal/server`.
- Keep the UI compatible with a future Wails window: plain forms + htmx, no
  new tabs or popups (SPEC §9.1).
- All UI text goes through `web/locales/{fr,en}.json`, never hard-coded.
  Every new key goes in both files.

## Database (SQLite, `modernc.org/sqlite`)

- Parameterized queries only, never `fmt.Sprintf` into SQL.
- Multi-step writes run in one transaction with `defer tx.Rollback()` and an
  explicit `Commit`. Inside a transaction, use only `tx` (the pool has a
  single connection: using `s.db` would deadlock).
- Schema changes go in a new numbered file in `internal/store/migrations/`.
  Never edit a migration that has already shipped.
- Close `rows`, check `rows.Err()`.

## Security and privacy

- Children's data is personal: never log names or emails at info level;
  log counts and IDs.
- Secrets (passwords, OAuth tokens) live in the OS keychain, never in the
  DB, logs or code.

## Logging

- `log/slog` with structured key/value pairs.

## Tests

- Test files are `*_test.go` next to the code, in the same package.
- Table-driven tests with `t.Run`; `t.Helper()` in helpers.
- `httptest` for handlers; a temporary SQLite file (`t.TempDir()`) for
  storage, never the user's real database.
- Build Excel fixtures in memory with excelize.
- Test behavior, not implementation details. No `time.Sleep`.
- Fake external services (email) behind an interface; never send real email.

## Checks

All of these must pass before work is handed over:

```sh
gofmt -l .        # must print nothing
go vet ./...
go test -race ./...
go build ./...
```

If a command cannot run, say so clearly instead of claiming success.
