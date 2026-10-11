package config_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AIToolSharing/agent_coop/internal/config"
	"github.com/AIToolSharing/agent_coop/internal/wire"
	"pgregory.net/rapid"
)

func noWarn(string) {}

// noCred is a credential file that is not there.
const noCred = "/nonexistent/coop/env"

type testTree struct{ root, project, deep string }

// tree makes root/proj/.git and root/proj/src/lib.
func tree(t *testing.T) testTree {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "proj")
	deep := filepath.Join(project, "src", "lib")
	mkdir(t, filepath.Join(project, ".git"))
	mkdir(t, deep)
	return testTree{root, project, deep}
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func collect(warnings *[]string) func(string) {
	return func(m string) { *warnings = append(*warnings, m) }
}

// The .coop file of v0.10.0 and before is not read: the session and the agent name come from
// the environment, which coop claude sets.
func TestTheSessionAndTheAgentComeOnlyFromTheEnvironment(t *testing.T) {
	tr := tree(t)
	writeFile(t, filepath.Join(tr.project, ".coop"), "COOP_SESSION=from-file\nCOOP_AGENT=file-agent\n", 0o644)
	if c := config.Load(map[string]string{}, noCred, noWarn, tr.deep); c.Session != "" || c.Agent != "lib" {
		t.Fatalf("a .coop file applied: %+v", c)
	}
	c := config.Load(map[string]string{"COOP_SESSION": "from-env", "COOP_AGENT": "env-agent"}, noCred, noWarn, tr.deep)
	if c.Session != "from-env" || c.Agent != "env-agent" {
		t.Fatalf("got %+v", c)
	}
	// An empty value in the environment counts as not set.
	if c := config.Load(map[string]string{"COOP_SESSION": "", "COOP_AGENT": ""}, noCred, noWarn, tr.deep); c.Session != "" || c.Agent != "lib" {
		t.Fatalf("empty environment: got %+v", c)
	}
	// The credential file cannot set the session or the agent either.
	cred := filepath.Join(tr.root, "env")
	writeFile(t, cred, "COOP_SESSION=s\nCOOP_AGENT=a\n", 0o600)
	if c := config.Load(map[string]string{}, cred, noWarn, tr.deep); c.Session != "" || c.Agent != "lib" {
		t.Fatalf("the credential file applied: %+v", c)
	}
}

func TestBadSessionNameInTheEnvironmentIsReportedAndIgnored(t *testing.T) {
	tr := tree(t)
	var warnings []string
	c := config.Load(map[string]string{"COOP_SESSION": "Bad"}, noCred, collect(&warnings), tr.deep)
	if c.Session != "" {
		t.Fatalf("session %q", c.Session)
	}
	want := []string{`COOP_SESSION "Bad" is not a valid session name; ignoring it`}
	if !slices.Equal(warnings, want) {
		t.Fatalf("warnings %q, want %q", warnings, want)
	}
}

func TestDefaultAgentNameIsTheDirectoryNameAndTheEnvironmentWins(t *testing.T) {
	tr := tree(t)
	dir := filepath.Join(tr.project, "My App.v2")
	mkdir(t, dir)
	if c := config.Load(map[string]string{}, noCred, noWarn, dir); c.Agent != "my-app-v2" {
		t.Fatalf("no setting: agent %q", c.Agent)
	}
	if c := config.Load(map[string]string{"COOP_AGENT": "from-env"}, noCred, noWarn, dir); c.Agent != "from-env" {
		t.Fatalf("environment: agent %q", c.Agent)
	}
}

func TestDefaultAgentNameFallsBackToAgent(t *testing.T) {
	tr := tree(t)
	for _, name := range []string{"all", "operator", "...", "日本"} {
		dir := filepath.Join(tr.project, name)
		mkdir(t, dir)
		if c := config.Load(map[string]string{}, noCred, noWarn, dir); c.Agent != "agent" {
			t.Errorf("%q: agent %q", name, c.Agent)
		}
	}
}

func TestBadAgentNameIsReportedAndTheDefaultTakesItsPlace(t *testing.T) {
	tr := tree(t)
	dir := filepath.Join(tr.project, "proj-x")
	mkdir(t, dir)
	var warnings []string
	if c := config.Load(map[string]string{"COOP_AGENT": "Bad!"}, noCred, collect(&warnings), dir); c.Agent != "proj-x" {
		t.Fatalf("agent %q", c.Agent)
	}
	want := []string{`COOP_AGENT "Bad!" is not a valid agent name; using "proj-x"`}
	if !slices.Equal(warnings, want) {
		t.Fatalf("warnings %q, want %q", warnings, want)
	}
}

func TestWarningsShowControlCharactersAsEscapes(t *testing.T) {
	tr := tree(t)
	var warnings []string
	config.Load(map[string]string{"COOP_SESSION": "a\x1b[2Jb"}, noCred, collect(&warnings), tr.project)
	if len(warnings) != 1 || strings.Contains(warnings[0], "\x1b") || !strings.Contains(warnings[0], `"a\x1b[2Jb"`) {
		t.Fatalf("warnings %q", warnings)
	}
}

func TestLoadTakesTheAddressAndTokensFromTheCredentialFile(t *testing.T) {
	tr := tree(t)
	cred := filepath.Join(tr.root, "env")
	writeFile(t, cred, "COOP_URL=https://hub.example//\nCOOP_TOKEN=laptop.s\nCOOP_OPERATOR_TOKEN=op.s\n", 0o600)
	c := config.Load(map[string]string{}, cred, noWarn, tr.deep)
	if c.URL != "https://hub.example" || c.Token != "laptop.s" || c.OperatorToken != "op.s" {
		t.Fatalf("file: got %+v", c)
	}
	env := map[string]string{"COOP_URL": "http://env/", "COOP_TOKEN": "env.m", "COOP_OPERATOR_TOKEN": "env.o"}
	c = config.Load(env, cred, noWarn, tr.deep)
	if c.URL != "http://env" || c.Token != "env.m" || c.OperatorToken != "env.o" {
		t.Fatalf("environment: got %+v", c)
	}
	env = map[string]string{"COOP_URL": "", "COOP_TOKEN": "", "COOP_OPERATOR_TOKEN": ""}
	c = config.Load(env, cred, noWarn, tr.deep)
	if c.URL != "https://hub.example" || c.Token != "laptop.s" || c.OperatorToken != "op.s" {
		t.Fatalf("empty environment: got %+v", c)
	}
}

func TestPushIsOnOnlyForTheValue1(t *testing.T) {
	tr := tree(t)
	cases := []struct {
		env, file string
		want      bool
	}{
		{"1", "", true},
		{"", "1", true},
		{"0", "1", false},
		{"true", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		cred := filepath.Join(t.TempDir(), "env")
		writeFile(t, cred, "COOP_PUSH="+c.file+"\n", 0o600)
		got := config.Load(map[string]string{"COOP_PUSH": c.env}, cred, noWarn, tr.deep).Push
		if got != c.want {
			t.Errorf("environment %q, file %q: push %v", c.env, c.file, got)
		}
	}
}

func TestWarningsComeInTheOrderOfTheTypeScriptCode(t *testing.T) {
	unixModes(t) // the first warning is about the mode
	tr := tree(t)
	cred := filepath.Join(tr.root, "env")
	writeFile(t, cred, "COOP_TOKEN=m.s\n", 0o644)
	var warnings []string
	config.Load(map[string]string{"COOP_SESSION": "X", "COOP_AGENT": "Y"}, cred, collect(&warnings), tr.deep)
	if len(warnings) != 3 ||
		!strings.Contains(warnings[0], "chmod 600") ||
		!strings.HasPrefix(warnings[1], "COOP_SESSION ") ||
		!strings.HasPrefix(warnings[2], "COOP_AGENT ") {
		t.Fatalf("warnings %q", warnings)
	}
}

func TestDefaultAgentNameExamples(t *testing.T) {
	long := strings.Repeat("a", 70)
	cases := map[string]string{
		"/home/u/My App.v2":                    "my-app-v2",
		"/p/--x--":                             "x",
		"/p/a  +  b":                           "a-b",
		"/p/a_b-c":                             "a_b-c",
		"/p/x/":                                "x",
		"/p/" + long:                           long[:64],
		"/p/" + strings.Repeat("a", 63) + " b": strings.Repeat("a", 63) + "-",
		"/p/ALL":                               "agent",
		"/p/Operator":                          "agent",
		"/p/日本":                                "agent",
		"/":                                    "agent",
		"":                                     "agent",
		"relative":                             "relative",
	}
	for cwd, want := range cases {
		if got := config.DefaultAgentName(cwd, "/home/someone"); got != want {
			t.Errorf("DefaultAgentName(%q) = %q, want %q", cwd, got, want)
		}
	}
}

// Found in use: an agent that was started in the home directory of root got the name "root".
// The name of the home directory is the name of the unix user, and the unix user must not
// name the agent.
func TestDefaultAgentNameIsNotTheUnixUser(t *testing.T) {
	for _, c := range []struct{ cwd, home, want string }{
		{"/root", "/root", "agent"},
		{"/root/", "/root", "agent"},
		{"/home/matt", "/home/matt/", "agent"},
		{"/Users/matt", "/Users/matt", "agent"},
		// A project below the home directory keeps its own name, also when it has the
		// name of the user.
		{"/root/git/app", "/root", "app"},
		{"/home/matt/matt", "/home/matt", "matt"},
		// Another user's home directory is not known here: only this user's counts.
		{"/root", "/home/agent", "root"},
		{"/srv/app", "", "app"},
	} {
		if got := config.DefaultAgentName(c.cwd, c.home); got != c.want {
			t.Errorf("DefaultAgentName(%q, %q) = %q, want %q", c.cwd, c.home, got, c.want)
		}
	}
	// Load takes the home directory from the environment.
	c := config.Load(map[string]string{"HOME": "/root"}, noCred, noWarn, "/root")
	if c.Agent != "agent" {
		t.Errorf("Load in the home directory: agent %q, want agent", c.Agent)
	}
}

func TestDefaultAgentNameIsAlwaysAnAgentName(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		cwd := rapid.String().Draw(rt, "cwd")
		got := config.DefaultAgentName(cwd, rapid.String().Draw(rt, "home"))
		if !wire.IsAgentName(got) {
			rt.Fatalf("DefaultAgentName(%q) = %q", cwd, got)
		}
	})
}

func TestDefaultAgentNameKeepsAValidName(t *testing.T) {
	// A valid agent name with no "-" at an end is its own default.
	name := rapid.StringMatching(`^[a-z0-9_]([a-z0-9_-]{0,62}[a-z0-9_])?$`).Filter(wire.IsAgentName)
	rapid.Check(t, func(rt *rapid.T) {
		n := name.Draw(rt, "name")
		if got := config.DefaultAgentName("/x/"+n, "/home/someone"); got != n {
			rt.Fatalf("DefaultAgentName(/x/%s) = %q", n, got)
		}
	})
}

// Name gives "" or a name token, for any text. A valid name with no "-" at an end is its own
// Name.
func TestNameIsAlwaysATokenOrEmpty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		s := rapid.String().Draw(rt, "s")
		if got := config.Name(s); got != "" && !wire.IsToken(got) {
			rt.Fatalf("Name(%q) = %q", s, got)
		}
	})
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.StringMatching(`^[a-z0-9_]([a-z0-9_-]{0,62}[a-z0-9_])?$`).Draw(rt, "name")
		if got := config.Name(n); got != n {
			rt.Fatalf("Name(%q) = %q", n, got)
		}
	})
}

// Each form of a remote that git takes gives the name that `git clone` gives its directory.
func TestRepoNameIsTheNameThatGitCloneGives(t *testing.T) {
	for remote, want := range map[string]string{
		"git@github.com:AIToolSharing/agent_coop.git": "agent_coop",
		"https://github.com/map588/agents.git":        "agents",
		"https://github.com/map588/agents":            "agents",
		"https://github.com/map588/agents/":           "agents",
		"ssh://git@host:2222/org/My.Repo.git":         "My.Repo",
		"/srv/git/proj.git":                           "proj",
		"/srv/git/proj.git/":                          "proj",
		`C:\git\proj.git`:                             "proj",
		"proj":                                        "proj",
		"":                                            "",
	} {
		if got := config.RepoName(remote); got != want {
			t.Errorf("RepoName(%q) = %q, want %q", remote, got, want)
		}
	}
	if got := config.Name(config.RepoName("git@github.com:org/My.Repo.git")); got != "my-repo" {
		t.Errorf("the session of My.Repo is %q, want my-repo", got)
	}
}

func TestLoadTakesThePinnedCertificateFromTheCredentialFile(t *testing.T) {
	tr := tree(t)
	cred := filepath.Join(tr.root, "env")
	writeFile(t, cred, "COOP_URL=https://hub\nCOOP_CERT_SHA256=ABCD\n", 0o600)
	if c := config.Load(map[string]string{}, cred, noWarn, tr.deep); c.CertSHA256 != "abcd" {
		t.Fatalf("file: %q", c.CertSHA256)
	}
	if c := config.Load(map[string]string{"COOP_CERT_SHA256": "ef01"}, cred, noWarn, tr.deep); c.CertSHA256 != "ef01" {
		t.Fatalf("environment: %q", c.CertSHA256)
	}
}

// COOP_ROLE and COOP_ORCHESTRATOR_TOKEN were settings of v0.10.0 and before. A machine that
// still has them uses its machine token.
func TestTheSettingsOfTheOrchestratorChangeNothing(t *testing.T) {
	dir := t.TempDir()
	cred := filepath.Join(dir, "env")
	writeFile(t, cred, "COOP_TOKEN=mac.m\nCOOP_ORCHESTRATOR_TOKEN=orch.o\nCOOP_ROLE=orchestrator\n", 0o600)
	project := t.TempDir()
	var warned []string
	c := config.Load(map[string]string{"COOP_ROLE": "orchestrator", "COOP_SESSION": "build-42"}, cred, func(s string) { warned = append(warned, s) }, project)
	if c.Token != "mac.m" || c.Session != "build-42" || len(warned) != 0 {
		t.Fatalf("%+v, warnings %q", c, warned)
	}
}
