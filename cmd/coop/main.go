// Command coop is the agent cooperation tool: the operator's TUI, the agent-side MCP server
// (the shim), and the commands that set both up. One binary, no runtime to install.
// `coop mcp` is what Claude Code starts; everything else is for the person.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// version is set by the Makefile from git describe.
var version = "dev"

const usageText = `usage:
  coop login <url> <token>        store the hub address and a token (operator or machine)
  coop session <name> [--agent <a>]
                                  put this directory's agents into a session (writes .coop)
  coop claude [<session>] [args]  start Claude Code with the coop channel and the operator's
                                  gate (COOP_GATE=off in the environment: no gate)
  coop --agent <name> claude ...  the same, as the agent <name> (default: the name in .coop,
                                  then the directory name)
  coop tui [--url <url>] [--token <token>]
                                  watch and steer all sessions (operator token)
  coop setup                      register coop with Claude Code and install the skill
  coop doctor                     check the connection, the tokens, the session, the setup
  coop mcp                        the MCP server Claude Code starts (stdio)
  coop hook pretool               the gate check Claude Code runs before a tool call
  coop serve [--listen <addr>] [--data <dir>] [--auto-create=false] [--hold-new=false]
                                  run the hub (the server)
  coop admin token add [--operator] <name> | list | revoke <name>
                                  manage tokens on the hub's host
  coop version
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	// A global --agent <name> names the agent for this run, as COOP_AGENT does. It comes before
	// the command, because claude has an --agent flag of its own.
	if len(args) > 0 && (args[0] == "--agent" || strings.HasPrefix(args[0], "--agent=")) {
		name, inline := strings.CutPrefix(args[0], "--agent=")
		args = args[1:]
		if !inline {
			name = ""
			if len(args) > 0 {
				name, args = args[0], args[1:]
			}
		}
		// A token may start with "-"; a flag value that does is a mistake, not a name.
		if !wire.IsAgentName(name) || strings.HasPrefix(name, "-") {
			fmt.Fprintf(stderr, "coop: --agent needs an agent name: a-z, 0-9, - and _, and not %q or %q\n", wire.Operator, wire.Broadcast)
			return 2
		}
		os.Setenv("COOP_AGENT", name)
	}
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
	case "setup":
		return cmdSetup(args[1:], stdout, stderr)
	case "doctor":
		return cmdDoctor(args[1:], stdout, stderr)
	case "mcp":
		return cmdMCP(args[1:], stderr)
	case "hook":
		return cmdHook(args[1:], os.Stdin, stdout, stderr)
	case "serve":
		return cmdServe(args[1:], stderr)
	case "admin":
		return cmdAdmin(args[1:], stdout, stderr)
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
