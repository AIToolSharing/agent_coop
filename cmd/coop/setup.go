package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/AIToolSharing/agent_coop/internal/config"
)

// The commands that talk to Claude Code and the file system go through these variables, so
// tests can replace them.
var (
	runClaude = func(args ...string) (string, error) {
		out, err := exec.Command("claude", args...).CombinedOutput()
		return string(out), err
	}
	lookPath   = exec.LookPath
	executable = func() string {
		exe, err := os.Executable()
		if err != nil {
			return "coop"
		}
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			return real
		}
		return exe
	}
)

// oldSkillPath is where coop v0.12.0 and older wrote their skill. Claude Code listed it in
// every session.
func oldSkillPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "skills", "coop", "SKILL.md")
}

// registered gives the command of the MCP server `coop` that Claude Code has in user scope,
// or "" when it has none.
func registered() string {
	out, err := runClaude("mcp", "get", "coop")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		if cmd, ok := strings.CutPrefix(strings.TrimSpace(line), "Command:"); ok {
			return strings.TrimSpace(cmd)
		}
	}
	return "?"
}

// register puts `exe mcp` into Claude Code as the MCP server `coop` of user scope. The
// channel (the push) finds a server only in the user, local or project scope, not one that
// --mcp-config gives to one process. A plain claude starts the server too; with no
// COOP_SESSION it offers no tools and joins nothing.
func register(exe string, stdout, stderr io.Writer) error {
	have := registered()
	if have == exe {
		fmt.Fprintln(stdout, "Claude Code has the server coop: "+exe+" mcp")
		return nil
	}
	if have != "" {
		if out, err := runClaude("mcp", "remove", "coop", "-s", "user"); err != nil {
			return fmt.Errorf("cannot remove the server coop (%s): %v\n%s", have, err, out)
		}
	}
	if out, err := runClaude("mcp", "add", "--scope", "user", "coop", "--", exe, "mcp"); err != nil {
		return fmt.Errorf("cannot add the server coop: %v\n%s", err, out)
	}
	fmt.Fprintln(stdout, "added the server coop to Claude Code: "+exe+" mcp (idle in a plain claude)")
	return nil
}

// cmdSetup puts coop into Claude Code: the MCP server `coop` of user scope, which runs this
// binary. It also removes the skill and the hooks of an older coop. It is safe to run again.
func cmdSetup(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: coop setup")
		return 2
	}
	if _, err := lookPath("claude"); err != nil {
		fmt.Fprintln(stderr, "claude is not on the PATH: install Claude Code first")
		return 1
	}
	if err := register(executable(), stdout, stderr); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if path := oldSkillPath(); path != "" {
		switch err := os.Remove(path); {
		case err == nil:
			// The directory goes only when it is empty: a file of the user stays.
			_ = os.Remove(filepath.Dir(path))
			fmt.Fprintln(stdout, "removed the skill of an older coop: "+path)
		case !os.IsNotExist(err):
			fmt.Fprintf(stderr, "cannot remove the skill of an older coop: %v\n", err)
			return 1
		}
	}
	if settings := claudeSettingsPath(); settings != "" {
		changed, err := removeHookFile(settings)
		switch {
		case err != nil:
			fmt.Fprintf(stderr, "cannot remove the hooks of an older coop: %v\n", err)
			return 1
		case changed:
			fmt.Fprintf(stdout, "removed the hooks of an older coop from %s\n", settings)
		}
	}
	cfg := config.Load(environ(), config.DefaultEnvFile(), func(string) {}, cwd())
	fmt.Fprintln(stdout, "next:")
	if cfg.Token == "" {
		fmt.Fprintln(stdout, "  coop login <url> <token>   # a token from the host of the hub")
	}
	fmt.Fprintln(stdout, "  cd <project> && coop claude   # Claude Code in the session of the git repository")
	fmt.Fprintln(stdout, "  coop doctor                   # when something does not connect")
	return 0
}
