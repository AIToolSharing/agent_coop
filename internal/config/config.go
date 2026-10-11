package config

import (
	"cmp"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// Config is what the coop command needs to reach the hub. An empty string means "not set".
type Config struct {
	URL           string // the hub address, with no "/" at the end
	Token         string // the machine token, COOP_TOKEN: the agents of this machine use it
	OperatorToken string // the operator token, COOP_OPERATOR_TOKEN
	CertSHA256    string // the pinned hub certificate, COOP_CERT_SHA256 (hex); "" = none
	Session       string // the session, or "" when the agent is in no session
	Agent         string // the agent name; never empty
	Push          bool   // true when Claude Code loads the server as a channel
}

// Load finds the configuration. For each key, a value in env wins, and an empty value counts as
// not set. COOP_SESSION, COOP_AGENT and COOP_PUSH come only from env: `coop claude` sets them
// for the Claude Code process that it starts. COOP_URL, the tokens (COOP_TOKEN,
// COOP_OPERATOR_TOKEN) and COOP_CERT_SHA256 then come from the credential file. The default
// agent name comes from cwd; in the home directory (HOME in env) it is "agent".
//
// Load calls warn for a credential file that other users can read, for an invalid session
// name (Load then uses no session) and for an invalid agent name (Load then uses
// DefaultAgentName).
func Load(env map[string]string, file string, warn func(string), cwd string) Config {
	fromFile := ReadEnvFile(file, warn)
	pick := func(key string) string { return cmp.Or(env[key], fromFile[key]) }
	session := env["COOP_SESSION"]
	if session != "" && !wire.IsToken(session) {
		// %q shows a control character as an escape, so a value cannot send one to the
		// terminal.
		warn(fmt.Sprintf("COOP_SESSION %q is not a valid session name; ignoring it", session))
		session = ""
	}
	fallback := DefaultAgentName(cwd, cmp.Or(env["HOME"], env["USERPROFILE"]))
	agent := cmp.Or(env["COOP_AGENT"], fallback)
	if !wire.IsAgentName(agent) {
		warn(fmt.Sprintf("COOP_AGENT %q is not a valid agent name; using %q", agent, fallback))
		agent = fallback
	}
	return Config{
		URL:           strings.TrimRight(pick("COOP_URL"), "/"),
		Token:         pick("COOP_TOKEN"),
		OperatorToken: pick("COOP_OPERATOR_TOKEN"),
		CertSHA256:    strings.ToLower(pick("COOP_CERT_SHA256")),
		Session:       session,
		Agent:         agent,
		Push:          env["COOP_PUSH"] == "1",
	}
}

var notInName = regexp.MustCompile(`[^a-z0-9_-]+`)

// Name makes a name token of s: lower case, each run of other characters one "-", no "-" at
// the start or the end. The name is then cut to 64 characters, so it can end with "-". The
// result is "" or a name token (wire.IsToken).
func Name(s string) string {
	name := strings.Trim(notInName.ReplaceAllString(strings.ToLower(s), "-"), "-")
	// All characters are ASCII now, so a byte cut is a character cut.
	return name[:min(len(name), 64)]
}

// DefaultAgentName returns the agent name when no setting gives one: the Name of the directory
// cwd. Thus two agents in different projects on one machine get different names. When the
// result is empty or reserved, the name is "agent".
//
// home is the home directory of the user, or "". An agent that starts there is "agent": the
// name of that directory is the name of the unix user, which says nothing about the agent
// (found in use: an agent named "root").
func DefaultAgentName(cwd, home string) string {
	if home != "" && filepath.Clean(cwd) == filepath.Clean(home) {
		return "agent"
	}
	name := Name(filepath.Base(cwd))
	if !wire.IsAgentName(name) {
		return "agent"
	}
	return name
}

// RepoName gives the name of a repository from the URL of its remote, as `git clone` names its
// directory: the last part of the path, without ".git". It takes each form that git takes:
// https://host/org/app.git, git@host:org/app.git, ssh://host/org/app, /srv/git/app.git.
func RepoName(remote string) string {
	s := strings.TrimRight(strings.TrimSpace(remote), "/\\")
	s = strings.TrimSuffix(s, ".git")
	if i := strings.LastIndexAny(s, "/\\:"); i >= 0 {
		s = s[i+1:]
	}
	return s
}
