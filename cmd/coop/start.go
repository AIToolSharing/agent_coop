package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/AIToolSharing/agent_coop/internal/wire"
)

const startUsage = `usage: coop start [-n] [-u <user>] [-a <agent>] <machine> <directory> <session> [claude args...]

Start an agent on another machine over SSH: in <directory> there, as ` + "`coop --agent <agent> claude <session>`" + `.
The session comes from the command line, so the directory needs no .coop file. The agent runs
in this terminal.

  <machine>    an SSH host or alias
  <directory>  the project directory on that machine: absolute, or below the user's home
  <session>    the coop session
  -a <agent>   the agent name (default: the name of the directory)
  -u <user>    the unix user for SSH (default: agent, or COOP_AGENT_USER)
  -n           show what would run, and run nothing
Arguments after the session go to claude, for example --model opus or -p "<prompt>".
The agent runs with --permission-mode bypassPermissions: no permission prompts, because nobody
sits at its terminal. coop's gate still pauses and stops it. Give --permission-mode to
choose another mode.
`

// startOpts is a start that the command line asks for.
type startOpts struct {
	machine, dir, session, agent, user string
	claudeArgs                         []string
}

// shellQuote quotes s for a POSIX shell: each character stays as it is.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./:=@,+") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func shellJoin(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shellQuote(a)
	}
	return strings.Join(q, " ")
}

// planStart gives the ssh command of a start. tty says that this terminal is one: SSH then
// gives the agent a terminal too, so that it runs interactive.
func planStart(o startOpts, tty bool) ([]string, error) {
	if !wire.IsToken(o.session) {
		return nil, fmt.Errorf("%q is not a session name: a-z, 0-9, _ and -, up to 64", o.session)
	}
	if o.agent != "" && !wire.IsAgentName(o.agent) {
		return nil, fmt.Errorf("%q is not an agent name: a-z, 0-9, _ and -, up to 64", o.agent)
	}
	if o.machine == "" || strings.HasPrefix(o.machine, "-") || o.dir == "" {
		return nil, fmt.Errorf("give a machine and a directory")
	}
	// The remote shell starts in the home directory: a path below it needs no "~/", and the
	// home directory itself is ".". A quoted "~" would not expand.
	dir := strings.TrimPrefix(o.dir, "~/")
	if dir == "~" {
		dir = "."
	}
	coop := []string{"coop"}
	if o.agent != "" {
		coop = append(coop, "--agent", o.agent)
	}
	coop = append(coop, "claude", o.session)
	// An agent that coop start places has no person at its terminal to answer a permission
	// prompt. coop's gate pauses and stops it instead. Arguments that set the mode
	// win.
	if !slices.ContainsFunc(o.claudeArgs, func(a string) bool {
		return a == "--permission-mode" || strings.HasPrefix(a, "--permission-mode=") || a == "--dangerously-skip-permissions"
	}) {
		coop = append(coop, "--permission-mode", "bypassPermissions")
	}
	coop = append(coop, o.claudeArgs...)
	remote := "cd " + shellQuote(dir) + " && exec " + shellJoin(coop)
	ssh := []string{"ssh"}
	if tty {
		ssh = append(ssh, "-t")
	}
	// A login shell: ~/.local/bin, where claude is, is on the PATH only there.
	ssh = append(ssh, "-l", o.user, o.machine, "bash -lc "+shellQuote(remote))
	return ssh, nil
}

// cmdStart starts an agent on another machine.
func cmdStart(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("coop start", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dry := fs.Bool("n", false, "")
	user := fs.String("u", "", "")
	agent := fs.String("a", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() < 3 {
		fmt.Fprint(stderr, startUsage)
		return 2
	}
	o := startOpts{
		machine: fs.Arg(0), dir: fs.Arg(1), session: fs.Arg(2), agent: *agent,
		user: *user, claudeArgs: fs.Args()[3:],
	}
	if o.user == "" {
		o.user = environ()["COOP_AGENT_USER"]
	}
	if o.user == "" {
		o.user = "agent"
	}
	// A terminal: SSH gives the agent one too. From a tool of another agent there is none.
	info, err := os.Stdin.Stat()
	tty := err == nil && info.Mode()&os.ModeCharDevice != 0
	ssh, err := planStart(o, tty)
	if err != nil {
		fmt.Fprintln(stderr, "coop start:", err)
		return 2
	}
	if *dry {
		fmt.Fprintf(stdout, "on %s as %s: %s\n", o.machine, o.user, shellJoin(ssh))
		return 0
	}
	bin, err := exec.LookPath("ssh")
	if err != nil {
		fmt.Fprintln(stderr, "coop start: ssh is not on the PATH")
		return 1
	}
	// The agent runs in this terminal: ssh takes the place of this process.
	return replaceProcess(bin, ssh, os.Environ(), stderr)
}
