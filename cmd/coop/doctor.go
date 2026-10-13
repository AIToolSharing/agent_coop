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

// cmdDoctor checks everything a machine needs, in the order a connection is made, and says
// what to run for each thing that is missing.
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
		fail("no credential file " + file + ": run the join line of the hub, or coop login <url> <secret>")
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
		fail("no hub address: run the join line of the hub, or coop login <url> <secret>")
	} else {
		ok("hub address " + cfg.URL)
		if cfg.CertSHA256 != "" {
			ok("tls: the hub's certificate is pinned, " + pin.Format(cfg.CertSHA256))
		}
		client := hubHTTP(cfg, stderr)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := probeToken(ctx, client, cfg.URL, "none"); err != nil && pin.IsCertError(err) {
			fail("the hub's certificate is not trusted or has changed: run coop login " + cfg.URL + " <secret> again")
		} else if err != nil && strings.Contains(err.Error(), "cannot reach") {
			fail(err.Error())
		} else {
			ok("the hub answers")
			// The hub tells its version to a token that it accepts.
			hubVersion, accepted := "", false
			if cfg.Token == "" {
				note("no secret (COOP_TOKEN): coop login " + cfg.URL + " <secret>")
			} else if me, err := whoami(ctx, client, cfg.URL, cfg.Token); err != nil {
				fail("COOP_TOKEN: " + err.Error())
			} else {
				ok("secret accepted: this machine is " + me.Machine + " to the hub")
				hubVersion, accepted = me.Version, true
			}
			// The hub and its devices must have one version: a part drops a message that only a
			// newer schema allows. A build from a clone is never equal, so this is a note.
			switch {
			case !accepted:
			case hubVersion == version:
				ok("coop " + version + ", the version of the hub")
			case hubVersion == "":
				note("the hub is older than this coop (" + version + "): upgrade the hub")
			default:
				note("this coop is " + version + ", the hub is " + hubVersion + ": run coop upgrade")
			}
		}
	}
	if cfg.Session != "" {
		ok("session " + cfg.Session + " (COOP_SESSION)")
	} else if session, repo := repoSession(cwd()); repo {
		ok("coop claude here joins the session " + session + " (the name of the git repository)")
	} else {
		ok("coop claude here joins the session " + session + " (the name of the directory; not in a git repository)")
	}
	if env["COOP_AGENT"] != "" {
		ok("agent name " + cfg.Agent + " (COOP_AGENT)")
	} else {
		ok("agent name " + cfg.Agent)
	}
	if _, err := lookPath("claude"); err != nil {
		fail("claude is not on the PATH")
	} else if _, err := runClaude("mcp", "get", "coop"); err == nil {
		note("an older coop setup loads coop into each Claude session: run coop setup")
	} else {
		ok("a plain claude has no coop; coop claude loads " + executable() + " mcp")
	}
	if _, err := os.Stat(oldSkillPath()); err == nil {
		note("an older coop setup left a skill in " + oldSkillPath() + ": run coop setup")
	}
	if path := claudeSettingsPath(); path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			if _, old, _ := removeHooks(raw); old {
				note("hooks of an older coop in " + path + ": run coop setup to remove them")
			}
		}
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
