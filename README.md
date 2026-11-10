# Cafeteria Management

Small desktop app to email parents of selected children, from an Excel file.
See [docs/SPEC.md](docs/SPEC.md) for the full specification.

## Requirements

- Go 1.25 or newer (`brew install go` on macOS)
- For Claude Code: gopls (`go install golang.org/x/tools/gopls@latest`),
  used by the MCP server in `.mcp.json`. `$(go env GOPATH)/bin` must be in
  your `PATH`.

## Development

```sh
make run     # start on http://127.0.0.1:8080
make test    # run tests
make build   # build bin/cafeteria
make build-all  # Windows + macOS binaries in dist/
```

Use `go run ./cmd/cafeteria -port 9000` to change the port.
