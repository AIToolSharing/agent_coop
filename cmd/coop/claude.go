package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"

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
	argv = append([]string{"claude", "--dangerously-load-development-channels", channel}, args...)
	return session, argv
}

// cmdClaude starts Claude Code with the coop channel enabled. Pushes reach the session only
// through that flag during the research preview of channels.
func cmdClaude(args []string, stderr io.Writer) int {
	session, argv := claudeCommand(args, environ())
	path, err := exec.LookPath("claude")
	if err != nil {
		fmt.Fprintln(stderr, "claude is not on the PATH: install Claude Code first")
		return 1
	}
	env := append(os.Environ(), "COOP_PUSH=1")
	if session != "" {
		env = append(env, "COOP_SESSION="+session)
	}
	if runtime.GOOS == "windows" {
		// Windows has no exec(2): run Claude Code as a child on the same console and
		// return its exit code.
		cmd := exec.Command(path, argv[1:]...)
		cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = env, os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return exit.ExitCode()
			}
			fmt.Fprintln(stderr, "cannot start claude:", err)
			return 1
		}
		return 0
	}
	if err := syscall.Exec(path, argv, env); err != nil {
		fmt.Fprintln(stderr, "cannot start claude:", err)
		return 1
	}
	return 0
}
