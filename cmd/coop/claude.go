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

// claudeCommand builds the Claude Code command line: the channel flag, then the user's
// arguments. A first argument that is a session name selects the session for this run.
func claudeCommand(args []string, env map[string]string) (session string, argv []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") && wire.IsToken(args[0]) {
		session, args = args[0], args[1:]
	}
	channel := env["COOP_CHANNEL"]
	if channel == "" {
		channel = defaultChannel
	}
	argv = []string{"claude", "--dangerously-load-development-channels", channel}
	return session, append(argv, args...)
}

// launchEnv gives the environment of the Claude Code process. It names the session and the
// agent, so that the shim of this process reads the same two values in whatever directory the
// agent works later.
func launchEnv(env map[string]string, session string, cfg func(map[string]string) config.Config) []string {
	env = maps.Clone(env)
	env["COOP_PUSH"] = "1"
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

// cmdClaude starts Claude Code with the coop channel. Pushes reach the session only through
// the channel flag during the research preview of channels.
func cmdClaude(args []string, stderr io.Writer) int {
	session, argv := claudeCommand(args, environ())
	path, err := exec.LookPath("claude")
	if err != nil {
		fmt.Fprintln(stderr, "claude is not on the PATH: install Claude Code first")
		return 1
	}
	env := launchEnv(environ(), session, func(env map[string]string) config.Config {
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
