package shim

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// The spawn tool starts a worker: a headless Claude Code (`coop claude -p`) on this machine,
// in this session, with its own task. The worker is one more agent: it joins with its own
// name, talks with send, and leaves when its task ends.

// allowedTools is the tool set of a worker. Claude Code asks nobody in a headless run: a tool
// outside this set is refused.
// ponytail: one fixed set; add an input of spawn when a worker needs other tools.
const allowedTools = "mcp__coop__*,Bash,Read,Edit,Write,Glob,Grep"

// spawnSpec is the process that spawn starts.
type spawnSpec struct {
	Argv  []string // the command line: <coop> claude -p --allowedTools ...
	Dir   string   // the working directory of the worker
	Env   []string // the environment: this process's, with the session and the name
	Stdin string   // the prompt
	Log   string   // the file that gets the output of the worker
}

type spawnIn struct {
	Prompt string `json:"prompt"`
	Name   string `json:"name"`
	Dir    string `json:"dir"`
}

// promptHead is the first part of a worker's prompt: who started it, and what to do at the
// end.
func promptHead(spawner, session string) string {
	return fmt.Sprintf("You are a worker that %s started in the session %s. When you are done, send your result to %s and call set_state with done.\nTask:\n", spawner, session, spawner)
}

// logDir is where the output of the workers goes: $XDG_STATE_HOME/coop, else
// ~/.local/state/coop, and on Windows %LOCALAPPDATA%\coop.
func logDir() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "coop"), nil
	}
	if runtime.GOOS == "windows" {
		if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
			return filepath.Join(dir, "coop"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "coop"), nil
}

// workerEnv gives the environment of a worker: env without the keys that name a session, an
// agent or a push, then the session and the name of the worker.
func workerEnv(env []string, session, name string) []string {
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if k == "COOP_SESSION" || k == "COOP_AGENT" || k == "COOP_PUSH" {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "COOP_SESSION="+session, "COOP_AGENT="+name)
}

// startWorker starts the process of spec, detached from this one, so that it runs on when
// this session ends. Its output goes to the log file. It gives the process id.
func startWorker(spec spawnSpec) (int, error) {
	if err := os.MkdirAll(filepath.Dir(spec.Log), 0o700); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(spec.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	cmd := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	cmd.Dir, cmd.Env = spec.Dir, spec.Env
	cmd.Stdin = strings.NewReader(spec.Stdin)
	cmd.Stdout, cmd.Stderr = f, f
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	// Wait reaps the process when it ends while this one runs; after that, init does.
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, nil
}

// spawn is the tool: it starts a worker in this session on this machine.
func (s *shim) spawn(ctx context.Context, in spawnIn) (any, error) {
	if !validText(in.Prompt) {
		return nil, invalid("prompt", fmt.Sprintf("1 to %d characters", wire.MaxText))
	}
	if in.Name != "" && !wire.IsAgentName(in.Name) {
		return nil, invalid("name", "an agent name: a-z, 0-9, - and _")
	}
	client, _, l, err := s.joined()
	if err != nil {
		return nil, err
	}
	dir := in.Dir
	if dir == "" {
		dir = client.join.cwd
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, agentError("dir: no such directory: " + dir)
	}
	me, _ := wire.ParseAddress(l.me)
	name := in.Name
	if name == "" {
		// The first <me>-<n> after the last one spawned here that no peer on this machine
		// uses now.
		v, err := client.view(ctx)
		if err != nil {
			return nil, err
		}
		used := map[string]bool{}
		for _, p := range v.Peers {
			if a, ok := wire.ParseAddress(p.Name); ok && a.Machine == me.Machine {
				used[a.Agent] = true
			}
		}
		s.mu.Lock()
		for {
			s.spawned++
			name = fmt.Sprintf("%s-%d", me.Agent, s.spawned)
			if !used[name] && wire.IsAgentName(name) {
				break
			}
		}
		s.mu.Unlock()
	}
	logs, err := logDir()
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	spec := spawnSpec{
		Argv:  []string{exe, "claude", "-p", "--allowedTools", allowedTools},
		Dir:   dir,
		Env:   workerEnv(os.Environ(), s.o.Session, name),
		Stdin: promptHead(l.me, s.o.Session) + in.Prompt,
		Log:   filepath.Join(logs, s.o.Session, name+".log"),
	}
	start := s.o.Start
	if start == nil {
		start = startWorker
	}
	pid, err := start(spec)
	if err != nil {
		s.logf("coop: spawn: %v", err)
		return nil, agentError("the worker did not start; see the log of this process")
	}
	return struct {
		Name string `json:"name"`
		PID  int    `json:"pid"`
		Log  string `json:"log"`
		Note string `json:"note"`
	}{name, pid, spec.Log, "the worker joins in a few seconds; wait with from set to its name, or ask it"}, nil
}
