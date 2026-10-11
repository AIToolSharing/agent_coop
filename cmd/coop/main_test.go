package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AIToolSharing/agent_coop/internal/config"
	"github.com/AIToolSharing/agent_coop/internal/pin"
	"pgregory.net/rapid"
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

// hub stands in for a hub that knows two tokens. To each client the hub calls each token a
// machine token, also a token that may watch.
func hub(t *testing.T, watch, machine string) *httptest.Server {
	t.Helper()
	roles := map[string]string{watch: "machine", machine: "machine"}
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

func TestProbeTokenChecksTheToken(t *testing.T) {
	srv := hub(t, "me.1", "mac.2")
	ctx := context.Background()
	for _, tok := range []string{"me.1", "mac.2"} {
		if err := probeToken(ctx, srv.Client(), srv.URL, tok); err != nil {
			t.Fatal(tok, err)
		}
	}
	// A hub of a version before the roles has no whoami: the error says to upgrade it.
	old := httptest.NewServer(http.NotFoundHandler())
	defer old.Close()
	if err := probeToken(ctx, old.Client(), old.URL, "me.1"); err == nil || !strings.Contains(err.Error(), "older version") {
		t.Fatal(err)
	}
	if err := probeToken(ctx, srv.Client(), srv.URL, "x"); !errors.Is(err, errBadToken) {
		t.Fatal(err)
	}
	if err := probeToken(ctx, srv.Client(), "http://127.0.0.1:1", "x"); err == nil || !strings.Contains(err.Error(), "cannot reach") {
		t.Fatal(err)
	}
}

// A machine has one token, COOP_TOKEN, also the machine of the person who watches. login
// removes the second token of coop v0.13.0 and before, and its line.
func TestLoginStoresTheOneTokenOfTheMachine(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	srv := hub(t, "me.1", "mac.2")
	file := filepath.Join(home, ".config", "coop", "env")
	if err := config.UpdateEnvFile(file, map[string]string{"COOP_TOKEN": "mac.2", "COOP_OPERATOR_TOKEN": "op.9"}); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"login", srv.URL + "/", "me.1"}, &out, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	got := config.ReadEnvFile(file, func(s string) { t.Fatal(s) })
	if !maps.Equal(got, map[string]string{"COOP_URL": srv.URL, "COOP_TOKEN": "me.1"}) {
		t.Fatalf("env file %v", got)
	}
	if text, _ := os.ReadFile(file); strings.Contains(string(text), "OPERATOR") {
		t.Fatalf("env file %q", text)
	}
	if !strings.Contains(out.String(), "the token for "+srv.URL) || strings.Contains(out.String(), "operator") {
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

// git runs git in dir for a test, with a fixed identity, and fails the test on an error.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// coop claude with no session takes the name of the git repository: of its origin, as git
// clone names the directory, so that clones with other directory names meet; else of its main
// directory, also from a worktree or a subdirectory.
func TestTheSessionOfARepositoryIsItsName(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on the PATH")
	}
	root := t.TempDir()
	clone := filepath.Join(root, "Some Clone")
	if err := os.Mkdir(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, clone, "init", "-q")
	sub := filepath.Join(clone, "src")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{clone, sub} {
		if s, err := repoSession(dir); err != nil || s != "some-clone" {
			t.Fatalf("no origin, %s: %q %v", dir, s, err)
		}
	}
	git(t, clone, "commit", "-q", "--allow-empty", "-m", "x")
	wt := filepath.Join(root, "wt-1")
	git(t, clone, "worktree", "add", "-q", wt)
	if s, err := repoSession(wt); err != nil || s != "some-clone" {
		t.Fatalf("a worktree: %q %v", s, err)
	}
	git(t, clone, "remote", "add", "origin", "git@github.com:AIToolSharing/agent_coop.git")
	for _, dir := range []string{clone, sub, wt} {
		if s, err := repoSession(dir); err != nil || s != "agent_coop" {
			t.Fatalf("origin, %s: %q %v", dir, s, err)
		}
	}
	if s, err := repoSession(t.TempDir()); err == nil {
		t.Fatalf("not a repository: %q", s)
	}
}

// Outside a git repository, coop claude with no session starts nothing and says what to do.
func TestClaudeOutsideARepositoryNeedsASession(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("COOP_SESSION", "")
	var out, errOut bytes.Buffer
	if code := run([]string{"claude", "-p", "hi"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "coop claude <session>") {
		t.Fatalf("code %d stderr %q", code, errOut.String())
	}
}

// coop session wrote a .coop file (v0.10.0 and before). It is gone; the message says what
// takes its place.
func TestTheSessionCommandIsGone(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"session", "build-42"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "coop claude") {
		t.Fatalf("code %d stderr %q", code, errOut.String())
	}
}

func TestClaudeCommandLine(t *testing.T) {
	const server = `--mcp-config {"mcpServers":{"coop":{"command":"/opt/coop/coop","args":["mcp"]}}}`
	session, argv := claudeCommand([]string{"build-42", "--model", "opus"}, "/opt/coop/coop")
	if session != "build-42" || strings.Join(argv, " ") != "claude "+server+" --dangerously-load-development-channels server:coop --model opus" {
		t.Fatalf("%q %v", session, argv)
	}
	session, argv = claudeCommand([]string{"-p", "hi"}, "/opt/coop/coop")
	if session != "" || strings.Join(argv, " ") != "claude "+server+" --dangerously-load-development-channels server:coop -p hi" {
		t.Fatalf("%q %v", session, argv)
	}
	// The user's own --settings goes to Claude Code as it is.
	if _, argv := claudeCommand([]string{"--settings", "x.json"}, "/opt/coop/coop"); strings.Join(argv, " ") != "claude "+server+" --dangerously-load-development-channels server:coop --settings x.json" {
		t.Fatalf("%v", argv)
	}
}

// coop claude gives the MCP server only to the Claude process that it starts: a plain claude
// has no coop. For any path of the binary (Windows paths have backslashes, paths can have
// spaces and quotes), the JSON after --mcp-config names that path, and a flag follows it, so
// that the variadic --mcp-config takes no argument of the user.
func TestClaudeCommandGivesTheServerToThisProcessOnly(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		exe := rapid.String().Draw(t, "exe")
		_, argv := claudeCommand(nil, exe)
		if len(argv) < 5 || argv[1] != "--mcp-config" || !strings.HasPrefix(argv[3], "-") {
			t.Fatalf("argv %q", argv)
		}
		var cfg struct {
			MCPServers map[string]struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(argv[2]), &cfg); err != nil {
			t.Fatalf("%v: %s", err, argv[2])
		}
		if c, ok := cfg.MCPServers["coop"]; len(cfg.MCPServers) != 1 || !ok || c.Command != exe || !slices.Equal(c.Args, []string{"mcp"}) {
			t.Fatalf("servers %+v, want coop: %q mcp", cfg.MCPServers, exe)
		}
	})
}

// The shim of one Claude Code process must use the same session and agent name in whatever
// directory the agent works later. coop claude puts both in the environment.
func TestLaunchEnvNamesTheSessionAndTheAgent(t *testing.T) {
	load := func(env map[string]string) config.Config {
		// As config.Load does: the environment wins, then the project (here: "from-dir").
		c := config.Config{Session: env["COOP_SESSION"], Agent: env["COOP_AGENT"]}
		if c.Agent == "" {
			c.Agent = "from-dir"
		}
		return c
	}
	env := launchEnv(map[string]string{"PATH": "/bin", "COOP_SESSION": "old"}, "build-42", load)
	for _, kv := range []string{"PATH=/bin", "COOP_PUSH=1", "COOP_SESSION=build-42", "COOP_AGENT=from-dir"} {
		if !slices.Contains(env, kv) {
			t.Errorf("missing %s in %v", kv, env)
		}
	}
	// Each name one time: a second COOP_SESSION could win in the child.
	seen := map[string]int{}
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		seen[k]++
	}
	for k, n := range seen {
		if n != 1 {
			t.Errorf("%s is %d times in the environment", k, n)
		}
	}
	// No session: nothing is named, and the agent gets no tools.
	env = launchEnv(map[string]string{"PATH": "/bin"}, "", load)
	if slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, "COOP_SESSION=") || e == "COOP_AGENT=from-dir" }) {
		t.Errorf("no session, but %v", env)
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
		if len(args) > 1 && args[1] == "remove" {
			f.registered = ""
		}
		return "", nil
	}
	lookPath = func(name string) (string, error) { return "/usr/local/bin/" + name, nil }
	executable = func() string { return exe }
}

// coop loads into no Claude session except the one that coop claude starts. coop setup removes
// what an older coop put into Claude Code for every session: the MCP server of user scope, the
// skill, and the hooks. It adds nothing.
func TestSetupRemovesWhatAnOlderCoopPutIntoEachSession(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	f := &fakeClaude{}
	f.install(t, "/opt/coop/coop")
	var out, errOut bytes.Buffer
	// Nothing of an older coop: setup only looks.
	if code := run([]string{"setup"}, &out, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	if strings.Join(f.calls, " | ") != "mcp get coop" {
		t.Fatalf("calls %v", f.calls)
	}
	skillDir := filepath.Join(home, ".claude", "skills", "coop")
	settings := filepath.Join(home, ".claude", "settings.json")
	for _, path := range []string{skillDir, settings} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("setup made %s: %v", path, err)
		}
	}
	if !strings.Contains(out.String(), "coop login") || !strings.Contains(out.String(), "coop claude") {
		t.Fatalf("stdout %q", out.String())
	}
	// The setup of an older coop: the server of user scope, the skill, a hook.
	f.registered = "/old/coop"
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: coop\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := `{"model":"opus","hooks":{"PreToolUse":[{"matcher":"","hooks":[{"type":"command","command":"'/opt/coop/coop' hook pretool","timeout":30}]}]}}`
	if err := os.WriteFile(settings, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	out.Reset()
	if code := run([]string{"setup"}, &out, &errOut); code != 0 || strings.Join(f.calls, " | ") != "mcp get coop | mcp remove coop -s user" {
		t.Fatalf("code %d calls %v: %s", code, f.calls, errOut.String())
	}
	if _, err := os.Stat(skillDir); !os.IsNotExist(err) {
		t.Fatalf("the skill of an older coop is still there: %v", err)
	}
	if text, _ := os.ReadFile(settings); strings.Contains(string(text), "hook") || !strings.Contains(string(text), "opus") {
		t.Fatalf("settings %s", text)
	}
	for _, want := range []string{"removed the coop server of every Claude session", "removed the skill of an older coop", "removed the hooks of an older coop from " + settings} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %q", want, out.String())
		}
	}
	// A second run finds nothing to remove.
	f.calls = nil
	if code := run([]string{"setup"}, &out, &errOut); code != 0 || strings.Join(f.calls, " | ") != "mcp get coop" {
		t.Fatalf("second run: code %d calls %v", code, f.calls)
	}
	// A file of the user next to the old skill stays, with its directory.
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(skillDir, "notes.md")
	_ = os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("old"), 0o644)
	_ = os.WriteFile(mine, []byte("mine"), 0o644)
	if code := run([]string{"setup"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	if _, err := os.Stat(mine); err != nil {
		t.Fatalf("the file of the user is gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("the old skill is still there: %v", err)
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
	for _, want := range []string{"FAIL  no credential file", "FAIL  no hub address", "--    not in a git repository: coop claude <session> names the session", "ok    a plain claude has no coop; coop claude loads /opt/coop/coop mcp"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
	// The setup of an older coop loads coop into each Claude session.
	f.registered = "/opt/coop/coop"
	out.Reset()
	run([]string{"doctor"}, &out, &errOut)
	if want := "--    an older coop setup loads coop into each Claude session: run coop setup"; !strings.Contains(out.String(), want) {
		t.Errorf("missing %q in\n%s", want, out.String())
	}
	// Logged in, set up, in a session.
	srv := hub(t, "op.1", "mac.2")
	if code := run([]string{"login", srv.URL, "mac.2"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	if code := run([]string{"setup"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	git(t, dir, "init", "-q")
	git(t, dir, "remote", "add", "origin", "https://github.com/org/build-42.git")
	out.Reset()
	if code := run([]string{"doctor"}, &out, &errOut); code != 0 {
		t.Fatalf("code %d:\n%s", code, out.String())
	}
	for _, want := range []string{"ok    credential file", "ok    hub address " + srv.URL, "ok    the hub answers", "ok    token accepted", "ok    coop claude here joins the session build-42 (the name of the git repository)", "ok    agent name " + filepath.Base(dir), "ok    a plain claude has no coop; coop claude loads /opt/coop/coop mcp"} {
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
	srv := httptest.NewTLSServer(hub(t, "me.1", "mac.2").Config.Handler)
	defer srv.Close()
	sum := sha256.Sum256(srv.Certificate().Raw)
	fp := hex.EncodeToString(sum[:])
	var out, errOut bytes.Buffer
	if code := run([]string{"login", srv.URL, "me.1"}, &out, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "self-signed") || !strings.Contains(out.String(), pin.Format(fp)) {
		t.Fatalf("stdout %q", out.String())
	}
	got := config.ReadEnvFile(filepath.Join(home, ".config", "coop", "env"), func(s string) { t.Fatal(s) })
	if got["COOP_CERT_SHA256"] != fp || got["COOP_TOKEN"] != "me.1" || got["COOP_URL"] != srv.URL {
		t.Fatalf("env file %v", got)
	}
	f := &fakeClaude{}
	f.install(t, "/opt/coop/coop")
	out.Reset()
	run([]string{"doctor"}, &out, &errOut)
	for _, want := range []string{"ok    tls: the hub's certificate is pinned, " + pin.Format(fp), "ok    the hub answers", "ok    token accepted"} {
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
	for _, args := range [][]string{{"--agent"}, {"--agent", "Not A Name", "version"}, {"--agent=all", "version"}, {"--agent", "reviewer"}} {
		t.Setenv("COOP_AGENT", "")
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 2 || errOut.Len() == 0 {
			t.Fatalf("%v: code %d, stderr %q", args, code, errOut.String())
		}
	}
}

// The flag --orchestrator of v0.10.0 and before is gone; an old `coop hook` entry in the
// settings of Claude Code exits 0, so that it does not stop the agent.
func TestTheOrchestratorFlagIsGoneAndAnOldHookPasses(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--orchestrator", "claude", "pipe-1"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), `unknown command "--orchestrator"`) {
		t.Fatalf("--orchestrator: code %d stderr %q", code, errOut.String())
	}
	for _, arg := range oldHookArgs {
		if code := run([]string{"hook", arg}, &out, &errOut); code != 0 {
			t.Fatalf("hook %s: code %d", arg, code)
		}
	}
}

// setHome gives the test its own home directory: HOME on Unix, USERPROFILE on Windows. Without
// the second one, a test on Windows writes into the home directory of the developer.
func setHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}
