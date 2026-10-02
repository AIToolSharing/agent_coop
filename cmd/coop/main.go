// Command coop is the agent cooperation tool: the operator's TUI, the agent-side MCP server
// (the shim), and the commands that set both up. One binary, no runtime to install.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// version is set by the Makefile from git describe.
var version = "dev"

const usageText = `usage:
  coop login <url> <token>        store the hub address and a token (operator or machine)
  coop session <name> [--agent <a>]
                                  put this directory's agents into a session (writes .coop)
  coop claude [<session>] [args]  start Claude Code with the coop channel enabled
  coop tui [--url <url>] [--token <token>]
                                  watch and steer all sessions (operator token)
  coop mcp                        the MCP server Claude Code starts (stdio)
  coop version
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	switch args[0] {
	case "login":
		return cmdLogin(args[1:], stdout, stderr)
	case "session":
		return cmdSession(args[1:], stdout, stderr)
	case "claude":
		return cmdClaude(args[1:], stderr)
	case "tui":
		return cmdTUI(args[1:], stderr)
	case "mcp":
		if err := runSpike(args[1:]); err != nil {
			fmt.Fprintln(stderr, "coop mcp:", err)
			return 1
		}
		return 0
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "coop", version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageText)
		return 0
	}
	fmt.Fprintf(stderr, "coop: unknown command %q\n%s", args[0], usageText)
	return 2
}

// environ is the process environment as a map.
func environ() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env
}

func cwd() string {
	d, err := os.Getwd()
	if err != nil {
		return "."
	}
	return d
}
