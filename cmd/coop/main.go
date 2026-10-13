// Command coop is the agent cooperation tool: the hub, the TUI that shows the sessions, the
// agent-side MCP server (the shim), and the commands that set them up. One binary, no runtime
// to install. `coop mcp` is what Claude Code starts; everything else is for the person.
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

// usageText lists the commands of a machine. The command of the hub's host (serve) is in
// the README.
const usageText = `usage:
  coop login <url> <secret>       store the hub address and its secret (the join line)
  coop claude [<session>] [args]  start Claude Code with the coop channel, in <session>
                                  (default: the name of the git repository, else of the
                                  directory)
  coop --agent <name> claude ...  the same, as the agent <name> (default: the name of the
                                  directory; a name in use gets -2, -3, ...)
  coop tui                        show the sessions
  coop setup                      remove what an older coop put into each Claude session
  coop doctor                     check the connection, the token, the session, the setup
  coop upgrade                    take the version of the hub: download the binary of this
                                  device from the hub, then run its setup
  coop mcp                        the MCP server Claude Code starts (stdio)
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
			fmt.Fprintf(stderr, "coop: --agent needs an agent name: a-z, 0-9, - and _, and not %q\n", wire.Broadcast)
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
		// coop session wrote a .coop file (v0.10.0 and before). coop claude takes the session
		// from the git repository now.
		fmt.Fprintln(stderr, "coop session is gone: coop claude takes the session from the git repository, or coop claude <session>")
		return 2
	case "claude":
		return cmdClaude(args[1:], stderr)
	case "tui":
		return cmdTUI(args[1:], stderr)
	case "setup":
		return cmdSetup(args[1:], stdout, stderr)
	case "doctor":
		return cmdDoctor(args[1:], stdout, stderr)
	case "upgrade":
		return cmdUpgrade(args[1:], stdout, stderr)
	case "mcp":
		return cmdMCP(args[1:], stderr)
	case "hook":
		// coop v0.10.0 and before put `coop hook <event>` into the Claude Code settings. coop
		// has no hooks now, and coop setup removes the entries. Until it runs, an old entry
		// must not stop the agent: exit 2 of a PreToolUse hook refuses each tool call.
		return 0
	case "serve":
		return cmdServe(args[1:], stderr)
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "coop", version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageText)
		return 0
	}
	return unknown(args[0], stderr)
}

// unknown answers for a command that does not exist.
func unknown(cmd string, stderr io.Writer) int {
	fmt.Fprintf(stderr, "coop: unknown command %q\n%s", cmd, usageText)
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
