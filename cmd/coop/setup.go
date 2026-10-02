package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/AIToolSharing/agent_coop/internal/config"
	"github.com/AIToolSharing/agent_coop/internal/skill"
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

func skillPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "skills", "coop", "SKILL.md")
}

// cmdSetup registers this binary with Claude Code as the MCP server `coop` (user scope) and
// writes the skill. It is safe to run again: it replaces a registration that points elsewhere.
func cmdSetup(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: coop setup")
		return 2
	}
	if _, err := lookPath("claude"); err != nil {
		fmt.Fprintln(stderr, "claude is not on the PATH: install Claude Code first")
		return 1
	}
	exe := executable()
	if out, err := runClaude("mcp", "get", "coop"); err == nil && strings.Contains(out, exe) {
		fmt.Fprintf(stdout, "Claude Code already starts %s mcp as coop\n", exe)
	} else {
		if err == nil {
			if out, err := runClaude("mcp", "remove", "coop", "-s", "user"); err != nil {
				fmt.Fprintf(stderr, "cannot replace the old coop server: %v\n%s", err, out)
				return 1
			}
		}
		if out, err := runClaude("mcp", "add", "--scope", "user", "coop", "--", exe, "mcp"); err != nil {
			fmt.Fprintf(stderr, "claude mcp add failed: %v\n%s", err, out)
			return 1
		}
		fmt.Fprintf(stdout, "registered coop with Claude Code (user scope): %s mcp\n", exe)
	}
	path := skillPath()
	if path == "" {
		fmt.Fprintln(stderr, "cannot find the home directory for the skill")
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := os.WriteFile(path, []byte(skill.Text), 0o644); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s\n", path)
	cfg := config.Load(environ(), config.DefaultEnvFile(), func(string) {}, cwd())
	fmt.Fprintln(stdout, "next:")
	if cfg.Token == "" {
		fmt.Fprintln(stdout, "  coop login <url> <machine token>   # the operator gives you the token")
	}
	fmt.Fprintln(stdout, "  cd <project> && coop session <name>  # the project's agents join that session")
	fmt.Fprintln(stdout, "  coop claude                          # Claude Code with the coop channel (pushes)")
	fmt.Fprintln(stdout, "  coop doctor                          # when something does not connect")
	return 0
}
