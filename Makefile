APP     := cafeteria
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# Microsoft 365 app registration IDs (public identifiers, not secrets). Empty:
# test mode only, "not available in this version".
MS_CLIENT_ID ?=
MS_TENANT_ID ?=
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.msClientID=$(MS_CLIENT_ID) -X main.msTenantID=$(MS_TENANT_ID)

.PHONY: run test build build-all clean

run:
	go run ./cmd/$(APP)

test:
	go test ./...

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(APP) ./cmd/$(APP)

# Cross-compile release binaries (pure Go, no CGO needed).
build-all:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/$(APP)-windows-amd64.exe ./cmd/$(APP)
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/$(APP)-macos-arm64 ./cmd/$(APP)
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/$(APP)-macos-amd64 ./cmd/$(APP)

clean:
	rm -rf bin dist
