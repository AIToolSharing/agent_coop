package main

import (
	"fmt"
	"io"

	"github.com/AIToolSharing/agent_coop/internal/config"
)

// cmdSession writes .coop in the working directory: agents started here join that session.
func cmdSession(args []string, stdout, stderr io.Writer) int {
	name, agent := "", ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--agent" && i+1 < len(args):
			i++
			agent = args[i]
		case args[i] == "--agent":
			fmt.Fprintln(stderr, "usage: coop session <name> [--agent <agent>]")
			return 2
		case name == "":
			name = args[i]
		default:
			fmt.Fprintln(stderr, "usage: coop session <name> [--agent <agent>]")
			return 2
		}
	}
	if name == "" {
		fmt.Fprintln(stderr, "usage: coop session <name> [--agent <agent>]")
		return 2
	}
	file, gitignore, err := config.WriteProjectFile(cwd(), name, agent)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if agent == "" {
		fmt.Fprintf(stdout, "wrote %s: session %s (agent name: this directory's name)\n", file, name)
	} else {
		fmt.Fprintf(stdout, "wrote %s: session %s, agent %s\n", file, name, agent)
	}
	if gitignore != "" {
		fmt.Fprintf(stdout, "added .coop to %s\n", gitignore)
	}
	fmt.Fprintln(stdout, "next:  coop claude   # Claude Code in this directory, with the coop channel")
	return 0
}
