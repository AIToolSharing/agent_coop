package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AIToolSharing/agent_coop/internal/config"
	"github.com/AIToolSharing/agent_coop/internal/pin"
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

// hub stands in for a hub that knows an operator token and a machine token, and the tokens
// orch.3 (orchestrator) and rep.4 (reporter).
func hub(t *testing.T, operator, machine string) *httptest.Server {
	t.Helper()
	roles := map[string]string{operator: "operator", machine: "machine", "orch.3": "orchestrator", "rep.4": "reporter"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch {
		case r.URL.Path != "/v1/whoami":
			w.WriteHeader(404)
		case roles[auth] != "":
			name, _, _ := strings.Cut(auth, ".")
			_, _ = w.Write([]byte(`{"name":"` + name + `","role":"` + roles[auth] + `"}`))
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
	if role, err := probeToken(ctx, srv.Client(), srv.URL, "orch.3"); err != nil || role != "orchestrator" {
		t.Fatal(role, err)
	}
	// A hub of a version before the roles has no whoami: the error says to upgrade it.
	old := httptest.NewServer(http.NotFoundHandler())
	defer old.Close()
	if _, err := probeToken(ctx, old.Client(), old.URL, "op.1"); err == nil || !strings.Contains(err.Error(), "older version") {
		t.Fatal(err)
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
	setHome(t, home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
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
	// The tokens of the two other roles get keys of their own: no token replaces another.
	for _, tok := range []string{"orch.3", "rep.4"} {
		if code := run([]string{"login", srv.URL, tok}, &out, &errOut); code != 0 {
			t.Fatalf("%s: code %d: %s", tok, code, errOut.String())
		}
	}
	got = config.ReadEnvFile(file, func(s string) { t.Fatal(s) })
	if got["COOP_ORCHESTRATOR_TOKEN"] != "orch.3" || got["COOP_REPORTER_TOKEN"] != "rep.4" || got["COOP_TOKEN"] != "mac.2" || got["COOP_OPERATOR_TOKEN"] != "op.1" {
		t.Fatalf("env file %v", got)
	}
	if !strings.Contains(out.String(), "coop --orchestrator claude") {
		t.Fatalf("stdout %q", out.String())
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
	session, argv, err := claudeCommand([]string{"build-42", "--model", "opus"}, map[string]string{}, "")
	if err != nil || session != "build-42" || strings.Join(argv, " ") != "claude --dangerously-load-development-channels server:coop --model opus" {
		t.Fatalf("%q %v %v", session, argv, err)
	}
	session, argv, err = claudeCommand([]string{"-p", "hi"}, map[string]string{"COOP_CHANNEL": "plugin:coop@coop"}, "")
	if err != nil || session != "" || strings.Join(argv, " ") != "claude --dangerously-load-development-channels plugin:coop@coop -p hi" {
		t.Fatalf("%q %v %v", session, argv, err)
	}
	// The settings with the gate hook come before the user's arguments.
	_, argv, err = claudeCommand([]string{"-p", "hi"}, map[string]string{}, `{"hooks":{}}`)
	if err != nil || strings.Join(argv, " ") != `claude --dangerously-load-development-channels server:coop --settings {"hooks":{}} -p hi` {
		t.Fatalf("%v %v", argv, err)
	}
	// Claude Code takes one --settings: a second one is refused, not dropped.
	for _, args := range [][]string{{"--settings", "x.json"}, {"-p", "hi", "--settings=x.json"}} {
		if _, _, err := claudeCommand(args, map[string]string{}, `{"hooks":{}}`); err == nil || !strings.Contains(err.Error(), "--settings") {
			t.Fatalf("%v: error %v", args, err)
		}
	}
}

// fakeClaude stands in for the claude command: it records calls and answers `mcp get coop`.
type fakeClaude struct {
	calls      []string
	registered string // the command line `mcp get coop` reports, or "" for not registered
}

func (f *fakeClaude) install(t *testing.T, exe string) {
	t.Helper()
	oldRun, oldLook, oldExe := runClaude, lookPath, executable
	t.Cleanup(func() { runClaude, lookPath, executable = oldRun, oldLook, oldExe })
	runClaude = func(args ...string) (string, error) {
		f.calls = append(f.calls, strings.Join(args, " "))
		if strings.Join(args, " ") == "mcp get coop" {
			if f.registered == "" {
				return "No MCP server found with name: coop", errors.New("exit 1")
			}
			return "coop:\n  Scope: User config\n  Command: " + f.registered + "\n  Args: mcp\n", nil
		}
		if len(args) > 1 && args[1] == "add" {
			f.registered = args[len(args)-2]
		}
		return "", nil
	}
	lookPath = func(name string) (string, error) { return "/usr/local/bin/" + name, nil }
	executable = func() string { return exe }
}

func TestSetupRegistersTheBinaryAndWritesTheSkill(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	f := &fakeClaude{}
	f.install(t, "/opt/coop/coop")
	var out, errOut bytes.Buffer
	if code := run([]string{"setup"}, &out, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	if strings.Join(f.calls, " | ") != "mcp get coop | mcp add --scope user coop -- /opt/coop/coop mcp" {
		t.Fatalf("calls %v", f.calls)
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude", "skills", "coop", "SKILL.md"))
	if err != nil || !strings.HasPrefix(string(b), "---\nname: coop") {
		t.Fatalf("skill: %v %q", err, b)
	}
	if !strings.Contains(out.String(), "registered coop with Claude Code") || !strings.Contains(out.String(), "coop login") {
		t.Fatalf("stdout %q", out.String())
	}
	// The hooks are in the user's settings: a session that another tool starts (a plain
	// claude) is then gated too, and reports what it does.
	settings := filepath.Join(home, ".claude", "settings.json")
	if text, err := os.ReadFile(settings); err != nil || len(hooksMissing(text, "/opt/coop/coop")) != 0 || !strings.Contains(out.String(), "added the gate hook and the activity hooks to "+settings) {
		t.Fatalf("settings: %v %q\nstdout %q", err, text, out.String())
	}
	// A second run with the same binary changes nothing.
	f.calls = nil
	out.Reset()
	if code := run([]string{"setup"}, &out, &errOut); code != 0 || strings.Join(f.calls, " | ") != "mcp get coop" {
		t.Fatalf("second run: code %d calls %v", code, f.calls)
	}
	if !strings.Contains(out.String(), "the gate hook and the activity hooks are in "+settings) {
		t.Fatalf("second run stdout %q", out.String())
	}
	// A registration that points elsewhere is replaced.
	f.registered = "/old/coop"
	f.calls = nil
	if code := run([]string{"setup"}, &out, &errOut); code != 0 || strings.Join(f.calls, " | ") != "mcp get coop | mcp remove coop -s user | mcp add --scope user coop -- /opt/coop/coop mcp" {
		t.Fatalf("replace: code %d calls %v", code, f.calls)
	}
}

func TestDoctorReportsEachStep(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("COOP_SESSION", "")
	t.Setenv("COOP_AGENT", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	dir := t.TempDir()
	t.Chdir(dir)
	f := &fakeClaude{}
	f.install(t, "/opt/coop/coop")
	var out, errOut bytes.Buffer
	// Nothing set up yet.
	if code := run([]string{"doctor"}, &out, &errOut); code != 1 {
		t.Fatalf("code %d:\n%s", code, out.String())
	}
	for _, want := range []string{"FAIL  no credential file", "FAIL  no hub address", "--    no session here", "FAIL  Claude Code has no MCP server named coop: run coop setup"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
	// Logged in, registered, in a session.
	srv := hub(t, "op.1", "mac.2")
	if code := run([]string{"login", srv.URL, "mac.2"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	if code := run([]string{"setup"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	if code := run([]string{"session", "build-42"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	out.Reset()
	if code := run([]string{"doctor"}, &out, &errOut); code != 0 {
		t.Fatalf("code %d:\n%s", code, out.String())
	}
	for _, want := range []string{"ok    credential file", "ok    hub address " + srv.URL, "ok    the hub answers", "ok    machine token accepted", "--    no operator token", "ok    session build-42 (" + filepath.Join(dir, ".coop") + ")", "ok    agent name " + filepath.Base(dir), "ok    Claude Code starts /opt/coop/coop mcp as coop", "ok    skill "} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
}

func TestMCPOptionsFollowTheConfiguration(t *testing.T) {
	cfg := config.Config{URL: "http://hub:8090", Token: "mac.1", Session: "build-42", Agent: "alice", Push: true}
	o := mcpOptions(cfg, nil)
	if o.URL != cfg.URL || o.Token != cfg.Token || o.Session != "build-42" || o.Agent != "alice" || !o.Push || o.Transport != nil || o.ClientName != "" {
		t.Fatalf("%+v", o)
	}
}

func TestLoginPinsASelfSignedHubAndDoctorUsesThePin(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("COOP_SESSION", "")
	t.Setenv("COOP_AGENT", "")
	srv := httptest.NewTLSServer(hub(t, "op.1", "mac.2").Config.Handler)
	defer srv.Close()
	sum := sha256.Sum256(srv.Certificate().Raw)
	fp := hex.EncodeToString(sum[:])
	var out, errOut bytes.Buffer
	if code := run([]string{"login", srv.URL, "op.1"}, &out, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "self-signed") || !strings.Contains(out.String(), pin.Format(fp)) {
		t.Fatalf("stdout %q", out.String())
	}
	got := config.ReadEnvFile(filepath.Join(home, ".config", "coop", "env"), func(s string) { t.Fatal(s) })
	if got["COOP_CERT_SHA256"] != fp || got["COOP_OPERATOR_TOKEN"] != "op.1" || got["COOP_URL"] != srv.URL {
		t.Fatalf("env file %v", got)
	}
	f := &fakeClaude{}
	f.install(t, "/opt/coop/coop")
	out.Reset()
	run([]string{"doctor"}, &out, &errOut)
	for _, want := range []string{"ok    tls: the hub's certificate is pinned, " + pin.Format(fp), "ok    the hub answers", "ok    operator token accepted"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
	// A login to a plain hub clears the pin.
	plain := hub(t, "op.1", "mac.2")
	if code := run([]string{"login", plain.URL, "mac.2"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	got = config.ReadEnvFile(filepath.Join(home, ".config", "coop", "env"), func(s string) { t.Fatal(s) })
	if got["COOP_CERT_SHA256"] != "" || got["COOP_TOKEN"] != "mac.2" {
		t.Fatalf("env file after a plain login %v", got)
	}
}

// `coop --agent <name> <command>` names the agent for the run, as COOP_AGENT does. The flag
// comes before the command, because claude has an --agent flag of its own.
func TestGlobalAgentFlagSetsTheAgentName(t *testing.T) {
	for _, args := range [][]string{{"--agent", "reviewer", "version"}, {"--agent=reviewer", "version"}} {
		t.Setenv("COOP_AGENT", "")
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "coop ") {
			t.Fatalf("%v: code %d, stdout %q, stderr %q", args, code, out.String(), errOut.String())
		}
		if got := os.Getenv("COOP_AGENT"); got != "reviewer" {
			t.Fatalf("%v: COOP_AGENT %q", args, got)
		}
	}
	for _, args := range [][]string{{"--agent"}, {"--agent", "Not A Name", "version"}, {"--agent=operator", "version"}, {"--agent", "reviewer"}} {
		t.Setenv("COOP_AGENT", "")
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 2 || errOut.Len() == 0 {
			t.Fatalf("%v: code %d, stderr %q", args, code, errOut.String())
		}
	}
}

// --orchestrator and --reporter go only with claude, and need the token of the role.
func TestRoleFlagsGoWithClaudeAndNeedTheirToken(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	t.Setenv("COOP_ORCHESTRATOR_TOKEN", "")
	t.Setenv("COOP_REPORTER_TOKEN", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	defer func() { launchRole = "" }()
	var out, errOut bytes.Buffer
	if code := run([]string{"--reporter", "tui"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "goes with claude") {
		t.Fatalf("--reporter tui: code %d stderr %q", code, errOut.String())
	}
	errOut.Reset()
	if code := run([]string{"--orchestrator", "--agent", "pm", "claude", "pipe-1"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "no orchestrator token (COOP_ORCHESTRATOR_TOKEN)") {
		t.Fatalf("no orchestrator token: code %d stderr %q", code, errOut.String())
	}
	errOut.Reset()
	if code := run([]string{"--reporter", "claude", "pipe-1"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "takes no session") {
		t.Fatalf("reporter with a session: code %d stderr %q", code, errOut.String())
	}
}

// setHome gives the test its own home directory: HOME on Unix, USERPROFILE on Windows. Without
// the second one, a test on Windows writes into the home directory of the developer.
func setHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}
