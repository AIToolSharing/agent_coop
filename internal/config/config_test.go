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

// noCred is a credential file that is not there: most tests here are about the project side.
const noCred = "/nonexistent/coop/env"

type testTree struct{ root, project, deep string }

// tree makes root/proj/.git and root/proj/src/lib. The .git directory stops each search for a
// .coop file inside the tree, so a file outside the test cannot change the result.
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

func TestProjectFileUpToTheGitRootSetsSessionAndAgent(t *testing.T) {
	tr := tree(t)
	writeFile(t, filepath.Join(tr.project, ".coop"), "COOP_SESSION=build-42\nCOOP_AGENT=reviewer\n", 0o644)
	c := config.Load(map[string]string{}, noCred, noWarn, tr.deep)
	if c.Session != "build-42" || c.Agent != "reviewer" {
		t.Fatalf("got %+v", c)
	}
}

func TestProjectFileAboveTheGitRootIsNotRead(t *testing.T) {
	tr := tree(t)
	writeFile(t, filepath.Join(tr.root, ".coop"), "COOP_SESSION=outside\n", 0o644)
	if c := config.Load(map[string]string{}, noCred, noWarn, tr.deep); c.Session != "" {
		t.Fatalf("session %q", c.Session)
	}
}

func TestEnvironmentWinsOverTheProjectFile(t *testing.T) {
	tr := tree(t)
	writeFile(t, filepath.Join(tr.project, ".coop"), "COOP_SESSION=from-file\nCOOP_AGENT=file-agent\n", 0o644)
	c := config.Load(map[string]string{"COOP_SESSION": "from-env", "COOP_AGENT": "env-agent"}, noCred, noWarn, tr.deep)
	if c.Session != "from-env" || c.Agent != "env-agent" {
		t.Fatalf("got %+v", c)
	}
	// An empty value in the environment counts as not set.
	c = config.Load(map[string]string{"COOP_SESSION": "", "COOP_AGENT": ""}, noCred, noWarn, tr.deep)
	if c.Session != "from-file" || c.Agent != "file-agent" {
		t.Fatalf("empty environment: got %+v", c)
	}
}

func TestProjectFileCannotSetTheAddressTheTokensOrPush(t *testing.T) {
	tr := tree(t)
	writeFile(t, filepath.Join(tr.project, ".coop"),
		"COOP_SESSION=s\nCOOP_URL=https://evil.example\nCOOP_TOKEN=m.secret\nCOOP_OPERATOR_TOKEN=o.secret\nCOOP_PUSH=1\n", 0o644)
	c := config.Load(map[string]string{}, noCred, noWarn, tr.deep)
	if c.URL != "" || c.Token != "" || c.OperatorToken != "" || c.Push {
		t.Fatalf("got %+v", c)
	}
	if c.Session != "s" {
		t.Fatalf("session %q", c.Session)
	}
}

func TestBadSessionNameInTheProjectFileIsReportedAndIgnored(t *testing.T) {
	tr := tree(t)
	file := filepath.Join(tr.project, ".coop")
	writeFile(t, file, "COOP_SESSION=Not Valid\n", 0o644)
	var warnings []string
	c := config.Load(map[string]string{}, noCred, collect(&warnings), tr.deep)
	if c.Session != "" {
		t.Fatalf("session %q", c.Session)
	}
	want := []string{"COOP_SESSION in " + file + ` "Not Valid" is not a valid session name; ignoring it`}
	if !slices.Equal(warnings, want) {
		t.Fatalf("warnings %q, want %q", warnings, want)
	}
}

func TestBadSessionNameInTheEnvironmentDoesNotFallBackToTheFile(t *testing.T) {
	tr := tree(t)
	writeFile(t, filepath.Join(tr.project, ".coop"), "COOP_SESSION=good\n", 0o644)
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

func TestDefaultAgentNameIsTheDirectoryNameAndTheFileAndEnvironmentWin(t *testing.T) {
	tr := tree(t)
	dir := filepath.Join(tr.project, "My App.v2")
	mkdir(t, dir)
	if c := config.Load(map[string]string{}, noCred, noWarn, dir); c.Agent != "my-app-v2" {
		t.Fatalf("no setting: agent %q", c.Agent)
	}
	writeFile(t, filepath.Join(tr.project, ".coop"), "COOP_AGENT=from-file\n", 0o644)
	if c := config.Load(map[string]string{}, noCred, noWarn, dir); c.Agent != "from-file" {
		t.Fatalf(".coop: agent %q", c.Agent)
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
	// The .coop agent does not take the place of a bad agent from the environment.
	writeFile(t, filepath.Join(tr.project, ".coop"), "COOP_AGENT=file-agent\n", 0o644)
	var warnings []string
	if c := config.Load(map[string]string{"COOP_AGENT": "Bad!"}, noCred, collect(&warnings), dir); c.Agent != "proj-x" {
		t.Fatalf("agent %q", c.Agent)
	}
	want := []string{`COOP_AGENT "Bad!" is not a valid agent name; using "proj-x"`}
	if !slices.Equal(warnings, want) {
		t.Fatalf("warnings %q, want %q", warnings, want)
	}
}

func TestReservedAgentNameInTheProjectFileIsReported(t *testing.T) {
	tr := tree(t)
	file := filepath.Join(tr.project, ".coop")
	writeFile(t, file, "COOP_AGENT=operator\n", 0o644)
	var warnings []string
	if c := config.Load(map[string]string{}, noCred, collect(&warnings), tr.project); c.Agent != "proj" {
		t.Fatalf("agent %q", c.Agent)
	}
	want := []string{"COOP_AGENT in " + file + ` "operator" is not a valid agent name; using "proj"`}
	if !slices.Equal(warnings, want) {
		t.Fatalf("warnings %q, want %q", warnings, want)
	}
}

func TestWarningsShowControlCharactersAsEscapes(t *testing.T) {
	tr := tree(t)
	writeFile(t, filepath.Join(tr.project, ".coop"), "COOP_SESSION=a\x1b[2Jb\n", 0o644)
	var warnings []string
	config.Load(map[string]string{}, noCred, collect(&warnings), tr.project)
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

func TestFindGitRoot(t *testing.T) {
	tr := tree(t)
	if got, ok := config.FindGitRoot(tr.deep); !ok || got != tr.project {
		t.Fatalf("from %s: %q %v", tr.deep, got, ok)
	}
	if got, ok := config.FindGitRoot(tr.project); !ok || got != tr.project {
		t.Fatalf("from the root: %q %v", got, ok)
	}
	// A linked worktree or a submodule has a .git file, not a directory.
	wt := filepath.Join(tr.root, "wt")
	mkdir(t, filepath.Join(wt, "a"))
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: /elsewhere\n", 0o644)
	if got, ok := config.FindGitRoot(filepath.Join(wt, "a")); !ok || got != wt {
		t.Fatalf("a .git file: %q %v", got, ok)
	}
}

func TestFindProjectFileIncludesTheGitRoot(t *testing.T) {
	tr := tree(t)
	if got, ok := config.FindProjectFile(tr.deep); ok {
		t.Fatalf("no .coop: %q", got)
	}
	file := filepath.Join(tr.project, ".coop")
	writeFile(t, file, "COOP_SESSION=s\n", 0o644)
	if got, ok := config.FindProjectFile(tr.deep); !ok || got != file {
		t.Fatalf("got %q %v, want %q", got, ok, file)
	}
	// The nearest file wins.
	near := filepath.Join(tr.deep, ".coop")
	writeFile(t, near, "COOP_SESSION=n\n", 0o644)
	if got, ok := config.FindProjectFile(tr.deep); !ok || got != near {
		t.Fatalf("got %q %v, want %q", got, ok, near)
	}
}

func TestProjectFileThatIsADirectoryStopsTheSearchAndGivesNoValues(t *testing.T) {
	tr := tree(t)
	writeFile(t, filepath.Join(tr.project, ".coop"), "COOP_SESSION=far\n", 0o644)
	mkdir(t, filepath.Join(tr.deep, ".coop"))
	if got, ok := config.FindProjectFile(tr.deep); !ok || got != filepath.Join(tr.deep, ".coop") {
		t.Fatalf("got %q %v", got, ok)
	}
	if c := config.Load(map[string]string{}, noCred, noWarn, tr.deep); c.Session != "" || c.Agent != "lib" {
		t.Fatalf("got %+v", c)
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

// claudeTree makes root/proj (with .git) and root/other (with .git): two projects, so a .coop
// file in one cannot apply to the other through the search upwards.
func claudeTree(t *testing.T) (project, other string) {
	t.Helper()
	root := t.TempDir()
	project = filepath.Join(root, "proj")
	other = filepath.Join(root, "other")
	mkdir(t, filepath.Join(project, ".git"))
	mkdir(t, filepath.Join(other, ".git"))
	return project, other
}

func TestProjectFileFromClaudeProjectDirWhenCwdHasNone(t *testing.T) {
	project, other := claudeTree(t)
	writeFile(t, filepath.Join(project, ".coop"), "COOP_SESSION=from-project\nCOOP_AGENT=planner\n", 0o644)
	c := config.Load(map[string]string{"CLAUDE_PROJECT_DIR": project}, noCred, noWarn, other)
	if c.Session != "from-project" || c.Agent != "planner" {
		t.Fatalf("got %+v", c)
	}
	// The search from CLAUDE_PROJECT_DIR also goes up to the git root.
	sub := filepath.Join(project, "sub")
	mkdir(t, sub)
	if c := config.Load(map[string]string{"CLAUDE_PROJECT_DIR": sub}, noCred, noWarn, other); c.Session != "from-project" {
		t.Fatalf("from a subdirectory: got %+v", c)
	}
}

func TestProjectFileFromCwdWinsOverClaudeProjectDir(t *testing.T) {
	project, other := claudeTree(t)
	writeFile(t, filepath.Join(project, ".coop"), "COOP_SESSION=from-project\n", 0o644)
	writeFile(t, filepath.Join(other, ".coop"), "COOP_SESSION=from-cwd\n", 0o644)
	c := config.Load(map[string]string{"CLAUDE_PROJECT_DIR": project}, noCred, noWarn, other)
	if c.Session != "from-cwd" {
		t.Fatalf("got %+v", c)
	}
}

func TestAgentNameDefaultStaysTheNameOfCwd(t *testing.T) {
	project, other := claudeTree(t)
	writeFile(t, filepath.Join(project, ".coop"), "COOP_SESSION=s\n", 0o644)
	c := config.Load(map[string]string{"CLAUDE_PROJECT_DIR": project}, noCred, noWarn, other)
	if c.Session != "s" || c.Agent != "other" {
		t.Fatalf("got %+v", c)
	}
}

func TestWarningNamesTheProjectFileFromClaudeProjectDir(t *testing.T) {
	project, other := claudeTree(t)
	file := filepath.Join(project, ".coop")
	writeFile(t, file, "COOP_SESSION=Not Valid\n", 0o644)
	var warnings []string
	config.Load(map[string]string{"CLAUDE_PROJECT_DIR": project}, noCred, collect(&warnings), other)
	want := []string{"COOP_SESSION in " + file + ` "Not Valid" is not a valid session name; ignoring it`}
	if !slices.Equal(warnings, want) {
		t.Fatalf("warnings %q, want %q", warnings, want)
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

// COOP_ROLE selects the token. Only the launch environment sets it: a credential file or a
// .coop file cannot make an agent an orchestrator.
func TestTheRoleSelectsItsTokenAndOnlyEnvSetsIt(t *testing.T) {
	dir := t.TempDir()
	cred := filepath.Join(dir, "env")
	writeFile(t, cred, "COOP_TOKEN=mac.m\nCOOP_ORCHESTRATOR_TOKEN=orch.o\nCOOP_REPORTER_TOKEN=rep.r\nCOOP_ROLE=orchestrator\n", 0o600)
	project := t.TempDir()
	writeFile(t, filepath.Join(project, ".coop"), "COOP_SESSION=build-42\nCOOP_ROLE=orchestrator\n", 0o644)
	none := func(string) {}
	if c := config.Load(map[string]string{}, cred, none, project); c.Role != "" || c.Token != "mac.m" || c.Session != "build-42" {
		t.Fatalf("no role in env: %+v", c)
	}
	if c := config.Load(map[string]string{"COOP_ROLE": "orchestrator"}, cred, none, project); c.Role != "orchestrator" || c.Token != "orch.o" || c.Session != "build-42" {
		t.Fatalf("orchestrator: %+v", c)
	}
	// A reporter is in no session, also when a .coop file names one.
	if c := config.Load(map[string]string{"COOP_ROLE": "reporter"}, cred, none, project); c.Role != "reporter" || c.Token != "rep.r" || c.Session != "" {
		t.Fatalf("reporter: %+v", c)
	}
	var warned string
	if c := config.Load(map[string]string{"COOP_ROLE": "operator"}, cred, func(s string) { warned = s }, project); c.Role != "" || c.Token != "mac.m" || !strings.Contains(warned, "COOP_ROLE") {
		t.Fatalf("a role that an agent cannot take: %+v, warning %q", c, warned)
	}
	// With no token of that role, the token is empty: the machine is not set up for it.
	if c := config.Load(map[string]string{"COOP_ROLE": "orchestrator"}, filepath.Join(dir, "none"), none, project); c.Token != "" {
		t.Fatalf("no orchestrator token: %+v", c)
	}
}
