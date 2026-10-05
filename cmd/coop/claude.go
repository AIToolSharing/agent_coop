package main

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"slices"
	"strings"
	"syscall"

	"github.com/AIToolSharing/agent_coop/internal/config"
	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// defaultChannel is the channel entry for a server registered with `claude mcp add`.
const defaultChannel = "server:coop"

// claudeCommand builds the Claude Code command line: the channel flag, the settings that hold
// the hooks of coop, then the user's arguments. A first argument that is a session name selects
// the session for this run. settings "" adds no settings.
func claudeCommand(args []string, env map[string]string, settings string) (session string, argv []string, err error) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") && wire.IsToken(args[0]) {
		session, args = args[0], args[1:]
	}
	channel := env["COOP_CHANNEL"]
	if channel == "" {
		channel = defaultChannel
	}
	argv = []string{"claude", "--dangerously-load-development-channels", channel}
	if settings != "" {
		for _, a := range args {
			if a == "--settings" || strings.HasPrefix(a, "--settings=") {
				// Claude Code takes one --settings. A second one would drop the gate or the
				// user's settings without a word.
				return "", nil, errors.New("coop claude sets --settings itself, for the operator's gate; put your settings in a settings file of the project or the user, or start with COOP_GATE=off claude")
			}
		}
		argv = append(argv, "--settings", settings)
	}
	return session, append(argv, args...), nil
}

// launchEnv gives the environment of the Claude Code process. It names the session and the
// agent, so that the shim and the gate hook of this process read the same two values, in
// whatever directory the agent works later. role is "" for an agent, or orchestrator or
// reporter. With no role it removes COOP_ROLE: an agent that an orchestrator starts with
// `coop claude` must not take the orchestrator's role from its environment.
func launchEnv(env map[string]string, session, role string, cfg func(map[string]string) config.Config) []string {
	env = maps.Clone(env)
	env["COOP_PUSH"], env["COOP_GATED"] = "1", "1"
	delete(env, "COOP_ROLE")
	if role != "" {
		env["COOP_ROLE"] = role
	}
	if session != "" {
		env["COOP_SESSION"] = session
	}
	if c := cfg(env); c.Session != "" {
		env["COOP_SESSION"], env["COOP_AGENT"] = c.Session, c.Agent
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}

// cmdClaude starts Claude Code with the coop channel and the gate hook. Pushes reach the
// session only through the channel flag during the research preview of channels. The hook
// asks the hub before each tool call whether the operator lets the agent work.
func cmdClaude(args []string, stderr io.Writer) int {
	// The settings file of the user can hold the hooks already (coop setup). A second copy
	// from --settings would ask the hub two times for each tool call. So --settings gets only
	// the hooks that the file does not have.
	settings := ""
	exe := executable()
	if missing := hooksMissing(readUserSettings(), exe); len(missing) > 0 {
		settings = hookSettings(exe, missing)
	}
	session, argv, err := claudeCommand(args, environ(), settings)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if launchRole == wire.RoleReporter && session != "" {
		fmt.Fprintln(stderr, "coop --reporter claude takes no session: a reporter reads every session")
		return 2
	}
	if launchRole != "" {
		env := environ()
		env["COOP_ROLE"] = launchRole
		if cfg := config.Load(env, config.DefaultEnvFile(), func(string) {}, cwd()); cfg.Token == "" {
			key := map[string]string{wire.RoleOrchestrator: "COOP_ORCHESTRATOR_TOKEN", wire.RoleReporter: "COOP_REPORTER_TOKEN"}[launchRole]
			fmt.Fprintf(stderr, "no %s token (%s): coop login <url> <%s token>\n", launchRole, key, launchRole)
			return 1
		}
	}
	path, err := exec.LookPath("claude")
	if err != nil {
		fmt.Fprintln(stderr, "claude is not on the PATH: install Claude Code first")
		return 1
	}
	env := launchEnv(environ(), session, launchRole, func(env map[string]string) config.Config {
		return config.Load(env, config.DefaultEnvFile(), func(string) {}, cwd())
	})
	return replaceProcess(path, argv, env, stderr)
}

// replaceProcess runs the program at path in place of coop, for `coop claude` and for the SSH
// of `coop start`. It returns only when the program does not start or, on Windows, with the
// exit code of the program.
func replaceProcess(path string, argv, env []string, stderr io.Writer) int {
	if runtime.GOOS == "windows" {
		// Windows has no exec(2): run the program as a child on the same console and
		// return its exit code.
		cmd := exec.Command(path, argv[1:]...)
		cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = env, os.Stdin, os.Stdout, os.Stderr
		// Ctrl-C on a Windows console goes to each process on it. Claude Code uses Ctrl-C to
		// stop a turn and stays open. Without this line Go ends coop at the first Ctrl-C:
		// the shell then reads the console at the same time as Claude Code.
		signal.Ignore(os.Interrupt)
		if err := cmd.Run(); err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return exit.ExitCode()
			}
			fmt.Fprintf(stderr, "cannot start %s: %v\n", argv[0], err)
			return 1
		}
		return 0
	}
	if err := syscall.Exec(path, argv, env); err != nil {
		fmt.Fprintf(stderr, "cannot start %s: %v\n", argv[0], err)
		return 1
	}
	return 0
}
