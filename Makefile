# coop: one Go binary for the hub, the shim and the operator's TUI.
# `make build` builds for this machine into dist/coop. `make release` cross-compiles the three
# platforms the project runs on. `make check` is the gate every commit must pass.

BIN     := dist/coop
PKG     := ./cmd/coop
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build release check contract test clean install

# install puts `coop` on the PATH as a link to the build, so `make build` updates it in place.
BINDIR ?= $(HOME)/.local/bin
install: build
	mkdir -p $(BINDIR)
	ln -sf $(abspath $(BIN)) $(BINDIR)/coop
	@echo "installed $(BINDIR)/coop -> $(abspath $(BIN))"

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
	test -z "$$(gofmt -l cmd internal deploy)"
	go vet ./...
	staticcheck ./...
	@if command -v shellcheck >/dev/null; then echo shellcheck deploy/*.sh; shellcheck deploy/*.sh; else echo "shellcheck is not installed: the scripts in deploy/ are not checked"; fi
	go test -race ./...

test:
	go test ./...

# contract runs Schemathesis (positive and negative data) against the hub's own /openapi.json.
# It needs uvx and takes a few minutes. Run it after any change to the API or the document.
contract:
	COOP_CONTRACT=1 go test -count=1 -run TestContract -v ./internal/api/

clean:
	rm -rf dist
