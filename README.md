# Cafeteria Management

Small desktop app to email parents of selected children, from an Excel file.
See [docs/SPEC.md](docs/SPEC.md) for the full specification.

## Requirements

- Go 1.26 or newer (`brew install go` on macOS)
- For Claude Code: gopls (`go install golang.org/x/tools/gopls@latest`),
  used by the MCP server in `.mcp.json`. `$(go env GOPATH)/bin` must be in
  your `PATH`.

## Development

```sh
make run     # start on http://127.0.0.1:8080 (data in your app data folder)
make test    # run tests
make build   # build bin/cafeteria
make build-all  # Windows + macOS binaries in dist/
```

Use `go run ./cmd/cafeteria -port 9000` to change the port.

## Continuous integration and releases

- Every push runs formatting, `go vet` and the tests (`.github/workflows/checks.yml`).
- To publish a version, tag a commit and push the tag:

  ```sh
  git tag v0.1.0
  git push origin v0.1.0
  ```

  GitHub then runs the tests, builds the Windows `.exe` and the macOS
  binaries, and attaches them with a `SHA256SUMS.txt` file to a new Release
  (`.github/workflows/release.yml`). The binaries are not signed: Windows and
  macOS show a security warning on first launch.
