// Command coop is the agent cooperation tool: hub, shim and operator TUI in one binary.
//
// Stage 1a spike: only `coop mcp` exists, and it is the channel test server. See spike.go.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "mcp":
		if err := runSpike(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "coop mcp:", err)
			os.Exit(1)
		}
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: coop mcp    the channel spike server (stdio MCP)")
}
