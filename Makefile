# coop: one Go binary for the hub, the shim and the operator's TUI.
# `make build` builds for this machine into dist/coop. `make release` cross-compiles the three
# platforms the project runs on. `make check` is the gate every commit must pass.

BIN     := dist/coop
PKG     := ./cmd/coop
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build release check test clean

# On macOS an unsigned binary asks for local-network access on every rebuild. An ad-hoc
# signature with a fixed identifier keeps one approval across rebuilds.
IDENTIFIER := com.aitoolsharing.coop

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) $(PKG)
	@if [ "$$(uname)" = Darwin ]; then codesign -s - -f --identifier $(IDENTIFIER) $(BIN); fi

release:
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/coop-darwin-arm64 $(PKG)
	@if [ "$$(uname)" = Darwin ]; then codesign -s - -f --identifier $(IDENTIFIER) dist/coop-darwin-arm64; fi
	GOOS=linux  GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/coop-linux-amd64  $(PKG)
	GOOS=linux  GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/coop-linux-arm64  $(PKG)

check:
	test -z "$$(gofmt -l cmd internal)"
	go vet ./...
	staticcheck ./...
	go test -race ./...

test:
	go test ./...

clean:
	rm -rf dist
