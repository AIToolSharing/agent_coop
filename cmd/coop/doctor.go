package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/config"
	"github.com/AIToolSharing/agent_coop/internal/pin"
)

// cmdDoctor checks everything an agent machine or an operator needs, in the order a connection
// is made, and says what to run for each thing that is missing.
func cmdDoctor(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: coop doctor")
		return 2
	}
	failed := false
	ok := func(line string) { fmt.Fprintln(stdout, "ok    "+line) }
	fail := func(line string) {
		failed = true
		fmt.Fprintln(stdout, "FAIL  "+line)
	}
	note := func(line string) { fmt.Fprintln(stdout, "--    "+line) }

	file := config.DefaultEnvFile()
	if info, err := os.Stat(file); err != nil {
		fail("no credential file " + file + ": run coop login <url> <token>")
	} else if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		fail(file + " is readable by others: chmod 600 " + file)
	} else {
		ok("credential file " + file)
	}
	env := environ()
	var warnings []string
	cfg := config.Load(env, file, func(s string) { warnings = append(warnings, s) }, cwd())
	for _, w := range warnings {
		fail(w)
	}
	if cfg.URL == "" {
		fail("no hub address: run coop login <url> <token>")
	} else {
		ok("hub address " + cfg.URL)
		if cfg.CertSHA256 != "" {
			ok("tls: the hub's certificate is pinned, " + pin.Format(cfg.CertSHA256))
		}
		client := hubHTTP(cfg, stderr)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := probeToken(ctx, client, cfg.URL, "none"); err != nil && pin.IsCertError(err) {
			fail("the hub's certificate is not trusted or has changed: run coop login " + cfg.URL + " <token> again")
		} else if err != nil && strings.Contains(err.Error(), "cannot reach") {
			fail(err.Error())
		} else {
			ok("the hub answers")
			check := func(token, want, key string) {
				if token == "" {
					note("no " + want + " token (" + key + "): coop login " + cfg.URL + " <" + want + " token>")
					return
				}
				role, err := probeToken(ctx, client, cfg.URL, token)
				switch {
				case err != nil:
					fail(key + ": " + err.Error())
				case role != want:
					fail(key + " is a " + role + " token, not a " + want + " token")
				default:
					ok(want + " token accepted")
				}
			}
			check(cfg.Token, "machine", "COOP_TOKEN")
			check(cfg.OperatorToken, "operator", "COOP_OPERATOR_TOKEN")
		}
	}
	switch {
	case env["COOP_SESSION"] != "":
		ok("session " + cfg.Session + " (COOP_SESSION)")
	case cfg.Session != "":
		path, _ := config.FindProjectFile(cwd())
		if path == "" {
			path, _ = config.FindProjectFile(env["CLAUDE_PROJECT_DIR"])
		}
		ok("session " + cfg.Session + " (" + path + ")")
	default:
		note("no session here: agents started in this directory get no coop tools; run coop session <name>")
	}
	if env["COOP_AGENT"] != "" {
		ok("agent name " + cfg.Agent + " (COOP_AGENT)")
	} else {
		ok("agent name " + cfg.Agent)
	}
	if _, err := lookPath("claude"); err != nil {
		fail("claude is not on the PATH")
	} else if out, err := runClaude("mcp", "get", "coop"); err != nil {
		fail("Claude Code has no MCP server named coop: run coop setup")
	} else if exe := executable(); !strings.Contains(out, exe) {
		fail("Claude Code starts another binary as coop, not " + exe + ": run coop setup")
	} else {
		ok("Claude Code starts " + exe + " mcp as coop")
	}
	if _, err := os.Stat(skillPath()); err != nil {
		note("no skill at " + skillPath() + ": run coop setup")
	} else {
		ok("skill " + skillPath())
	}
	if cfg.Push {
		ok("COOP_PUSH=1: this process advertises the channel")
	}
	note("pushes reach a Claude session only when it starts with the channel: coop claude")
	if failed {
		return 1
	}
	return 0
}
