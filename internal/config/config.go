package config

import (
	"cmp"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// ProjectFile is the per-project file. It holds the session and the agent name, never an
// address or a token.
const ProjectFile = ".coop"

// Config is what the coop command needs to reach the hub. An empty string means "not set".
type Config struct {
	URL           string // the hub address, with no "/" at the end
	Token         string // the machine token, COOP_TOKEN
	OperatorToken string // the operator token, COOP_OPERATOR_TOKEN
	CertSHA256    string // the pinned hub certificate, COOP_CERT_SHA256 (hex); "" = none
	Session       string // the session, or "" when the agent is in no session
	Agent         string // the agent name; never empty
	Push          bool   // true when Claude Code loads the server as a channel
	Gated         bool   // true when `coop claude` started the agent with the gate hook
}

// Load finds the configuration. For each key, a value in env wins, and an empty value counts as
// not set. COOP_SESSION and COOP_AGENT then come from the nearest .coop file (FindProjectFile)
// from cwd, or else from CLAUDE_PROJECT_DIR in env. COOP_URL, COOP_TOKEN, COOP_OPERATOR_TOKEN,
// COOP_CERT_SHA256 and COOP_PUSH then come from the credential file. A .coop file cannot set an address or a
// token, because a repository can hold one. The default agent name always comes from cwd.
//
// Load calls warn for a credential file that other users can read, for an invalid session
// name (Load then uses no session) and for an invalid agent name (Load then uses
// DefaultAgentName). A bad value in env does not fall back to the .coop file.
func Load(env map[string]string, file string, warn func(string), cwd string) Config {
	fromFile := ReadEnvFile(file, warn)
	projectFile, found := FindProjectFile(cwd)
	// Claude Code sets CLAUDE_PROJECT_DIR to the project directory when it starts an MCP server
	// (seen on Claude Code 2.1.287), and the server starts in that directory. When the working
	// directory is a different one and the search from it finds no .coop file, the search
	// starts again at CLAUDE_PROJECT_DIR. The TypeScript shim does not do this.
	if dir := env["CLAUDE_PROJECT_DIR"]; !found && dir != "" {
		projectFile, _ = FindProjectFile(dir)
	}
	fromProject := readProjectFile(projectFile)
	pick := func(key string, other map[string]string) string {
		return cmp.Or(env[key], other[key])
	}
	// where names the source of a bad value. A value that is not from env is from projectFile.
	where := func(key string) string {
		if env[key] != "" {
			return key
		}
		return key + " in " + projectFile
	}

	session := pick("COOP_SESSION", fromProject)
	if session != "" && !wire.IsToken(session) {
		// %q shows a control character as an escape, so a .coop file cannot send one to the
		// terminal.
		warn(fmt.Sprintf("%s %q is not a valid session name; ignoring it", where("COOP_SESSION"), session))
		session = ""
	}
	fallback := DefaultAgentName(cwd)
	agent := cmp.Or(pick("COOP_AGENT", fromProject), fallback)
	if !wire.IsAgentName(agent) {
		warn(fmt.Sprintf("%s %q is not a valid agent name; using %q", where("COOP_AGENT"), agent, fallback))
		agent = fallback
	}
	return Config{
		URL:           strings.TrimRight(pick("COOP_URL", fromFile), "/"),
		Token:         pick("COOP_TOKEN", fromFile),
		OperatorToken: pick("COOP_OPERATOR_TOKEN", fromFile),
		CertSHA256:    strings.ToLower(pick("COOP_CERT_SHA256", fromFile)),
		Session:       session,
		Agent:         agent,
		Push:          pick("COOP_PUSH", fromFile) == "1",
		// Only the launch environment says that the hook is there; a file cannot.
		Gated: env["COOP_GATED"] == "1",
	}
}

// FindGitRoot returns the nearest directory, from dir upwards, that holds .git. A .git file
// (a linked worktree or a submodule) counts too.
func FindGitRoot(dir string) (string, bool) {
	for d := range ancestors(dir) {
		if exists(filepath.Join(d, ".git")) {
			return d, true
		}
	}
	return "", false
}

// FindProjectFile returns the nearest .coop file from cwd upwards. The search stops at the git
// root, after it looks there: a .coop file above the repository does not apply to it.
func FindProjectFile(cwd string) (string, bool) {
	for d := range ancestors(cwd) {
		file := filepath.Join(d, ProjectFile)
		if exists(file) {
			return file, true
		}
		if exists(filepath.Join(d, ".git")) {
			return "", false
		}
	}
	return "", false
}

var notInAgentName = regexp.MustCompile(`[^a-z0-9_-]+`)

// DefaultAgentName returns the agent name when no setting gives one: the name of the directory
// cwd, made valid. Thus two agents in different projects on one machine get different names.
// The name is in lower case, each run of other characters becomes one "-", and no "-" stays
// at the start or the end. The name is then cut to 64 characters, so it can end with "-".
// When the result is empty or reserved, the name is "agent".
func DefaultAgentName(cwd string) string {
	name := notInAgentName.ReplaceAllString(strings.ToLower(filepath.Base(cwd)), "-")
	name = strings.Trim(name, "-")
	// All characters are ASCII now, so a byte cut is a character cut.
	name = name[:min(len(name), 64)]
	if !wire.IsAgentName(name) {
		return "agent"
	}
	return name
}

// readProjectFile reads a .coop file. A path that is empty or that cannot be read gives no
// values. Unlike the credential file, the mode is not checked: the file holds no secret.
func readProjectFile(path string) map[string]string {
	if path == "" {
		return map[string]string{}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}
	}
	return ParseEnv(string(b))
}

// ancestors yields dir and then each parent directory, up to the root.
func ancestors(dir string) iter.Seq[string] {
	return func(yield func(string) bool) {
		d := filepath.Clean(dir)
		for yield(d) {
			parent := filepath.Dir(d)
			if parent == d {
				return
			}
			d = parent
		}
	}
}

// exists reports whether path names a file or a directory. Like existsSync in Node, it follows
// a symbolic link and gives false on any error.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
