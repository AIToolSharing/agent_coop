# coop: one Go binary for the hub, the shim and the operator's TUI.
# `make build` builds for this machine into dist/coop. `make release` cross-compiles the
# platforms the project runs on. `make check` is the gate every commit must pass.
# On Windows, `make build` and `make install` run in each shell: PowerShell, cmd and Git Bash.
# The recipes of the other targets use sh: run those in Git Bash.

ifeq ($(OS),Windows_NT)
EXE := .exe
# PowerShell and cmd have no HOME. A path with forward slashes passes in each shell.
BINDIR ?= $(subst \,/,$(USERPROFILE))/.local/bin
else
BINDIR ?= $(HOME)/.local/bin
# On macOS an unsigned binary asks for local-network access on every rebuild. An ad-hoc
# signature with a fixed identifier keeps one approval across rebuilds.
ifeq ($(shell uname),Darwin)
SIGN := codesign -s - -f --identifier com.aitoolsharing.coop
endif
endif
BIN     := dist/coop$(EXE)
PKG    := ./cmd/coop
# No redirection, and double quotes below: make on Windows runs these lines in cmd when it
# finds no sh, and cmd has no /dev/null and no single quotes.
VERSION := $(shell git describe --tags --always --dirty || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOBUILD := go build -trimpath -ldflags "$(LDFLAGS)"

.PHONY: build release hub check contract test clean install

# install puts `coop` on the PATH. On Linux and macOS it is a link to the build, so
# `make build` updates it in place. On Windows a user cannot make a link, so the build goes
# straight to BINDIR: run it again after each change. The go command also replaces a coop.exe
# that runs (an agent, the TUI): it gives the old file the name coop.exe~ first, and removes
# that file at a later install.
ifdef EXE
install:
	$(GOBUILD) -o "$(BINDIR)/coop$(EXE)" $(PKG)
	@echo installed $(BINDIR)/coop$(EXE)
else
install: build
	mkdir -p $(BINDIR)
	ln -sf $(abspath $(BIN)) $(BINDIR)/coop
	@echo "installed $(BINDIR)/coop -> $(abspath $(BIN))"
endif

build:
	$(GOBUILD) -o $(BIN) $(PKG)
ifdef SIGN
	@$(SIGN) $(BIN)
endif

release:
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 $(GOBUILD) -o dist/coop-darwin-arm64 $(PKG)
ifdef SIGN
	@$(SIGN) dist/coop-darwin-arm64
endif
	GOOS=linux  GOARCH=amd64 CGO_ENABLED=0 $(GOBUILD) -o dist/coop-linux-amd64  $(PKG)
	GOOS=linux  GOARCH=arm64 CGO_ENABLED=0 $(GOBUILD) -o dist/coop-linux-arm64  $(PKG)
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GOBUILD) -o dist/coop-windows-amd64.exe $(PKG)

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
