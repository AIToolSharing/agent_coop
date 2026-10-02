package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
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
	o := mcpOptions(cfg, log)
	o.HTTPClient = hubHTTP(cfg, stderr)
	if err := shim.Serve(ctx, o); err != nil {
		fmt.Fprintln(stderr, "coop mcp:", err)
		return 1
	}
	return 0
}
