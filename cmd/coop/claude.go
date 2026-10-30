package main

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"

	"github.com/AIToolSharing/agent_coop/internal/config"
)

// claudeCommand builds the Claude Code command line: the channel flag for the server `coop`
// (coop setup put it into Claude Code), then the user's arguments. The channel finds a server
// only in the user, local or project scope: a server that --mcp-config gives to one process
// gets no push (Claude Code 2.1.285 to 2.1.314, at least).
func claudeCommand(args []string) []string {
	return append([]string{"claude", "--dangerously-load-development-channels", "server:coop"}, args...)
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

// gitOut runs git in dir and gives its output without the line end. Tests replace it.
var gitOut = func(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// repoSession gives the session of dir: the name of the git repository, or the name of the
// directory when dir is in no repository (repo is then false). The name of a repository is
// the name of its remote origin, as `git clone` names the directory, so that the clones on
// each machine meet in one session; a repository with no origin gives the name of its main
// directory, also from a worktree.
func repoSession(dir string) (session string, repo bool) {
	if remote, err := gitOut(dir, "remote", "get-url", "origin"); err == nil {
		if s := config.Name(config.RepoName(remote)); s != "" {
			return s, true
		}
	}
	if common, err := gitOut(dir, "rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil && common != "" {
		if s := config.Name(filepath.Base(filepath.Dir(common))); s != "" {
			return s, true
		}
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if s := config.Name(filepath.Base(dir)); s != "" {
		return s, false
	}
	return "session", false
}

// cmdClaude starts Claude Code with the coop channel. Pushes reach the session only through
// the channel flag during the research preview of channels. The session is COOP_SESSION
// (coop -s <session> claude sets it), else the name of the git repository or of the directory
// (repoSession).
func cmdClaude(args []string, stderr io.Writer) int {
	argv := claudeCommand(args)
	var session string
	if os.Getenv("COOP_SESSION") == "" {
		session, _ = repoSession(cwd())
	}
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

// replaceProcess runs the program at path in place of coop, for `coop claude`. It returns only
// when the program does not start or, on Windows, with the exit code of the program.
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
