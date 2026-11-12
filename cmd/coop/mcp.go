package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/AIToolSharing/agent_coop/internal/config"
	"github.com/AIToolSharing/agent_coop/internal/shim"
)

// mcpOptions maps the configuration to the shim. The client name and version come from the
// MCP handshake, so they stay empty here.
func mcpOptions(cfg config.Config, log func(string, ...any)) shim.Options {
	return shim.Options{
		URL: cfg.URL, Token: cfg.Token,
		Session: cfg.Session, Agent: cfg.Agent, Push: cfg.Push,
		Log: log,
	}
}

// realAgent reports whether this shim serves the agent of a `coop claude`, and not a Claude
// process that the agent started (the agents view, claude --bg, claude in a shell). Such a
// process inherits the session and the agent name. It is the agent when no launcher named an
// owner, when its parent is the owner, or when its conversation joined before (nameFile is
// there): a restart of Claude Code can give the conversation a new process.
func realAgent(env map[string]string, ppid int, nameFile string) bool {
	owner := env["COOP_OWNER"]
	if owner == "" || owner == strconv.Itoa(ppid) {
		return true
	}
	if nameFile == "" {
		return false
	}
	_, err := os.Stat(nameFile)
	return err == nil
}

// cmdMCP serves the agent's MCP server over stdio. Claude Code starts it; it ends when Claude
// Code closes the pipe or on SIGTERM.
func cmdMCP(args []string, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: coop mcp")
		return 2
	}
	warn := func(s string) { fmt.Fprintln(stderr, "coop mcp:", s) }
	cfg := config.Load(environ(), config.DefaultEnvFile(), warn, cwd())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := func(format string, a ...any) { fmt.Fprintf(stderr, format+"\n", a...) }
	env := environ()
	names := ""
	if dir, err := shim.StateDir(); err == nil {
		names = filepath.Join(dir, "names")
	}
	if cfg.Session != "" && !realAgent(env, os.Getppid(), shim.NameFile(names, env["CLAUDE_CODE_SESSION_ID"])) {
		warn("the agent of the session " + cfg.Session + " started this Claude Code; it stays out of the session")
		cfg.Session = ""
	}
	o := mcpOptions(cfg, log)
	o.Names, o.Conversation = names, env["CLAUDE_CODE_SESSION_ID"]
	o.HTTPClient = hubHTTP(cfg, stderr)
	if err := shim.Serve(ctx, o); err != nil {
		fmt.Fprintln(stderr, "coop mcp:", err)
		return 1
	}
	return 0
}
