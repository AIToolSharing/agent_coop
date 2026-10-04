package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"slices"
	"strings"
	"syscall"

	"github.com/AIToolSharing/agent_coop/internal/herdr"
	"github.com/AIToolSharing/agent_coop/internal/wire"
)

const startUsage = `usage: coop start [-n] [-s] [-u <user>] [-a <agent>] <machine> <directory> <session> [claude args...]

Start an agent on another machine: in <directory> there, as ` + "`coop --agent <agent> claude <session>`" + `.
The session comes from the command line, so the directory needs no .coop file.

  <machine>    the label of a machine that Herdr knows, or an SSH host or alias
  <directory>  the project directory on that machine: absolute, or below the user's home
  <session>    the coop session
  -a <agent>   the agent name (default: the name of the directory)
  -s           use SSH, also when Herdr knows the machine
  -u <user>    the unix user for SSH (default: agent, or COOP_AGENT_USER)
  -n           show what would run, and run nothing
Arguments after the session go to claude, for example --model opus or -p "<prompt>".
The agent runs with --permission-mode bypassPermissions: no permission prompts, because nobody
sits at its terminal. coop's gate still holds, pauses and stops it. Give --permission-mode to
choose another mode.

When Herdr knows the machine (herdr machine list), the agent starts in a new Herdr workspace
there: a pause then stops its turn at once, and the TUI's o goes to its pane. Else it starts
over SSH in this terminal.
`

// startOpts is a start that the command line asks for.
type startOpts struct {
	machine, dir, session, agent, user string
	sshOnly                            bool
	claudeArgs                         []string
}

// startPlan is how a start runs: in a Herdr workspace on the machine, or over SSH.
type startPlan struct {
	herdr bool
	// Herdr: the directory (absolute, or below "~", which the machine expands), the label
	// of the workspace, and the command line for the pane.
	cwd, label, command string
	// SSH: the command to run here.
	ssh []string
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

// herdrKnows reports whether the output of `herdr machine list` has an enabled machine with
// this label. The columns are id, label, address, profile and state, separated by tabs.
func herdrKnows(list, label string) bool {
	for _, line := range strings.Split(list, "\n") {
		f := strings.Split(line, "\t")
		if len(f) >= 5 && f[1] == label && f[4] == "enabled" {
			return true
		}
	}
	return false
}

// planStart gives how to run a start. machines is the output of `herdr machine list`, or ""
// when Herdr is not there. tty says that this terminal is one: SSH then gives the agent a
// terminal too, so that it runs interactive.
func planStart(o startOpts, machines string, tty bool) (startPlan, error) {
	if !wire.IsToken(o.session) {
		return startPlan{}, fmt.Errorf("%q is not a session name: a-z, 0-9, _ and -, up to 64", o.session)
	}
	if o.agent != "" && !wire.IsAgentName(o.agent) {
		return startPlan{}, fmt.Errorf("%q is not an agent name: a-z, 0-9, _ and -, up to 64", o.agent)
	}
	if o.machine == "" || strings.HasPrefix(o.machine, "-") || o.dir == "" {
		return startPlan{}, fmt.Errorf("give a machine and a directory")
	}
	// The remote shell starts in the home directory: a path below it needs no "~/".
	dir := strings.TrimPrefix(o.dir, "~/")
	coop := []string{"coop"}
	if o.agent != "" {
		coop = append(coop, "--agent", o.agent)
	}
	coop = append(coop, "claude", o.session)
	// An agent that coop start places has no person at its terminal to answer a permission
	// prompt. coop's gate holds, pauses and stops it instead. Arguments that set the mode
	// win.
	if !slices.ContainsFunc(o.claudeArgs, func(a string) bool {
		return a == "--permission-mode" || strings.HasPrefix(a, "--permission-mode=") || a == "--dangerously-skip-permissions"
	}) {
		coop = append(coop, "--permission-mode", "bypassPermissions")
	}
	coop = append(coop, o.claudeArgs...)
	command := shellJoin(coop)
	if !o.sshOnly && herdrKnows(machines, o.machine) {
		cwd := "~/" + dir
		switch {
		case strings.HasPrefix(dir, "/"):
			cwd = dir
		case dir == "." || dir == "~":
			cwd = "~"
		}
		name := o.agent
		if name == "" {
			name = path.Base(strings.TrimSuffix(dir, "/"))
		}
		return startPlan{herdr: true, cwd: cwd, label: o.session + "/" + name, command: command}, nil
	}
	remote := "cd " + shellQuote(dir) + " && exec " + command
	ssh := []string{"ssh"}
	if tty {
		ssh = append(ssh, "-t")
	}
	// A login shell: ~/.local/bin, where claude is, is on the PATH only there.
	ssh = append(ssh, "-l", o.user, o.machine, "bash -lc "+shellQuote(remote))
	return startPlan{ssh: ssh}, nil
}

// cmdStart starts an agent on another machine.
func cmdStart(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("coop start", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dry := fs.Bool("n", false, "")
	sshOnly := fs.Bool("s", false, "")
	user := fs.String("u", "", "")
	agent := fs.String("a", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() < 3 {
		fmt.Fprint(stderr, startUsage)
		return 2
	}
	env := environ()
	o := startOpts{
		machine: fs.Arg(0), dir: fs.Arg(1), session: fs.Arg(2), agent: *agent,
		user: *user, sshOnly: *sshOnly, claudeArgs: fs.Args()[3:],
	}
	if o.user == "" {
		o.user = env["COOP_AGENT_USER"]
	}
	if o.user == "" {
		o.user = "agent"
	}
	run := herdr.Command(env)
	ctx := context.Background()
	machines := ""
	if !o.sshOnly {
		if out, err := run(ctx, "machine", "list"); err == nil {
			machines = string(out)
		}
	}
	// A terminal: SSH gives the agent one too. From a tool of another agent there is none.
	info, err := os.Stdin.Stat()
	tty := err == nil && info.Mode()&os.ModeCharDevice != 0
	p, err := planStart(o, machines, tty)
	if err != nil {
		fmt.Fprintln(stderr, "coop start:", err)
		return 2
	}
	if p.herdr {
		if *dry {
			fmt.Fprintf(stdout, "in a new herdr workspace on %s, directory %s: %s\n", o.machine, p.cwd, p.command)
			return 0
		}
		out, err := run(ctx, "--machine", o.machine, "workspace", "create", "--cwd", p.cwd, "--label", p.label, "--focus")
		var made struct {
			Result struct {
				RootPane struct {
					PaneID string `json:"pane_id"`
				} `json:"root_pane"`
			} `json:"result"`
		}
		if err == nil {
			err = json.Unmarshal(out, &made)
		}
		pane := made.Result.RootPane.PaneID
		if err == nil && !herdr.IsPaneID(pane) {
			err = fmt.Errorf("herdr gave no pane: %s", out)
		}
		if err != nil {
			fmt.Fprintf(stderr, "coop start: herdr made no workspace on %s: %v\n", o.machine, err)
			return 1
		}
		if _, err := run(ctx, "--machine", o.machine, "pane", "run", pane, p.command); err != nil {
			fmt.Fprintf(stderr, "coop start: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "started in herdr on %s, pane %s: %s\n", o.machine, pane, p.command)
		return 0
	}
	if *dry {
		fmt.Fprintf(stdout, "on %s as %s: %s\n", o.machine, o.user, shellJoin(p.ssh))
		return 0
	}
	bin, err := exec.LookPath("ssh")
	if err != nil {
		fmt.Fprintln(stderr, "coop start: ssh is not on the PATH")
		return 1
	}
	// The agent runs in this terminal: ssh takes the place of this process.
	if err := syscall.Exec(bin, p.ssh, os.Environ()); err != nil {
		fmt.Fprintln(stderr, "coop start:", err)
		return 1
	}
	return 0
}
