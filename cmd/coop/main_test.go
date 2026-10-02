package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AIToolSharing/agent_coop/internal/config"
)

func TestUsageAndVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "coop login") {
		t.Fatalf("no args: code %d, stderr %q", code, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := run([]string{"nope"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), `unknown command "nope"`) {
		t.Fatalf("unknown: code %d, stderr %q", code, errOut.String())
	}
	out.Reset()
	if code := run([]string{"version"}, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "coop ") {
		t.Fatalf("version: code %d, stdout %q", code, out.String())
	}
}

func hub(t *testing.T, operator, machine string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch {
		case r.URL.Path != "/v1/admin/sessions":
			w.WriteHeader(404)
		case auth == operator:
			_, _ = w.Write([]byte(`{"sessions":[]}`))
		case auth == machine:
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"error":"forbidden","message":"this needs an operator token"}`))
		default:
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"unauthorized","message":"bad token"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProbeTokenTellsTheRole(t *testing.T) {
	srv := hub(t, "op.1", "mac.2")
	ctx := context.Background()
	if role, err := probeToken(ctx, srv.Client(), srv.URL, "op.1"); err != nil || role != "operator" {
		t.Fatal(role, err)
	}
	if role, err := probeToken(ctx, srv.Client(), srv.URL, "mac.2"); err != nil || role != "machine" {
		t.Fatal(role, err)
	}
	if _, err := probeToken(ctx, srv.Client(), srv.URL, "x"); !errors.Is(err, errBadToken) {
		t.Fatal(err)
	}
	if _, err := probeToken(ctx, srv.Client(), "http://127.0.0.1:1", "x"); err == nil || !strings.Contains(err.Error(), "cannot reach") {
		t.Fatal(err)
	}
}

func TestLoginStoresTheTokenUnderTheKeyOfItsRole(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srv := hub(t, "op.1", "mac.2")
	var out, errOut bytes.Buffer
	if code := run([]string{"login", srv.URL + "/", "op.1"}, &out, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	if code := run([]string{"login", srv.URL, "mac.2"}, &out, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	file := filepath.Join(home, ".config", "coop", "env")
	got := config.ReadEnvFile(file, func(s string) { t.Fatal(s) })
	if got["COOP_URL"] != srv.URL || got["COOP_OPERATOR_TOKEN"] != "op.1" || got["COOP_TOKEN"] != "mac.2" {
		t.Fatalf("env file %v", got)
	}
	if !strings.Contains(out.String(), "operator token for") || !strings.Contains(out.String(), "next:  coop tui") {
		t.Fatalf("stdout %q", out.String())
	}
	errOut.Reset()
	if code := run([]string{"login", srv.URL, "bad"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "refused") {
		t.Fatalf("bad token: code %d, stderr %q", code, errOut.String())
	}
	if code := run([]string{"login", "ftp://x", "t"}, &out, &errOut); code != 2 {
		t.Fatalf("bad scheme: code %d", code)
	}
}

func TestSessionWritesTheProjectFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	var out, errOut bytes.Buffer
	if code := run([]string{"session", "build-42", "--agent", "alice"}, &out, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	b, err := os.ReadFile(filepath.Join(dir, ".coop"))
	if err != nil || string(b) != "COOP_SESSION=build-42\nCOOP_AGENT=alice\n" {
		t.Fatalf("%q %v", b, err)
	}
	if !strings.Contains(out.String(), "added .coop to") {
		t.Fatalf("stdout %q", out.String())
	}
	if code := run([]string{"session", "Bad Name"}, &out, &errOut); code != 1 {
		t.Fatalf("bad name: code %d", code)
	}
	if code := run([]string{"session"}, &out, &errOut); code != 2 {
		t.Fatalf("no name: code %d", code)
	}
}

func TestClaudeCommandLine(t *testing.T) {
	session, argv := claudeCommand([]string{"build-42", "--model", "opus"}, map[string]string{})
	if session != "build-42" || strings.Join(argv, " ") != "claude --dangerously-load-development-channels server:coop --model opus" {
		t.Fatalf("%q %v", session, argv)
	}
	session, argv = claudeCommand([]string{"-p", "hi"}, map[string]string{"COOP_CHANNEL": "plugin:coop@coop"})
	if session != "" || strings.Join(argv, " ") != "claude --dangerously-load-development-channels plugin:coop@coop -p hi" {
		t.Fatalf("%q %v", session, argv)
	}
}
