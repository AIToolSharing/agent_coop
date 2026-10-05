# coop: one Go binary for the hub, the shim and the operator's TUI.
# `make build` builds for this machine into dist/coop. `make release` cross-compiles the
# platforms the project runs on. `make check` is the gate every commit must pass.
# On Windows, run make in Git Bash: the recipes use sh.

ifeq ($(OS),Windows_NT)
EXE := .exe
endif
BIN     := dist/coop$(EXE)
PKG    := ./cmd/coop
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build release hub check contract test clean install

# install puts `coop` on the PATH as a link to the build, so `make build` updates it in place.
# On Windows it copies the file, because a user cannot make a link there: run it after a build.
BINDIR ?= $(HOME)/.local/bin
install: build
	mkdir -p $(BINDIR)
ifdef EXE
	cp $(BIN) $(BINDIR)/coop$(EXE)
else
	ln -sf $(abspath $(BIN)) $(BINDIR)/coop
endif
	@echo "installed $(BINDIR)/coop$(EXE) -> $(abspath $(BIN))"

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
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/coop-windows-amd64.exe $(PKG)

# hub puts this version on the host of the hub: the binary of the hub, and the release binaries
# that the hub gives to its devices. HOST is an SSH host or alias with a root login, or with a
# sudo that asks for no password. After it, each device runs `coop upgrade`.
hub: release
	@test -n "$(HOST)" || { echo "usage: make hub HOST=<ssh host>"; exit 2; }
	ssh $(HOST) 'install -d -m 700 .cache/coop-hub && rm -f .cache/coop-hub/*'
	scp -C dist/coop-*-* deploy/install.sh deploy/coop.service $(HOST):.cache/coop-hub/
	ssh $(HOST) .cache/coop-hub/install.sh

# The scripts of the hub's host, and the script that a device runs to install coop.
SCRIPTS := deploy/*.sh internal/api/install.sh

check:
	test -z "$$(gofmt -l cmd internal deploy)"
	go vet ./...
	staticcheck ./...
	@if command -v shellcheck >/dev/null; then echo shellcheck $(SCRIPTS); shellcheck $(SCRIPTS); else echo "shellcheck is not installed: the scripts are not checked"; fi
	go test -race ./...

test:
	go test ./...

# contract runs Schemathesis (positive and negative data) against the hub's own /openapi.json.
# It needs uvx and takes a few minutes. Run it after any change to the API or the document.
contract:
	COOP_CONTRACT=1 go test -count=1 -run TestContract -v ./internal/api/

clean:
	rm -rf dist
