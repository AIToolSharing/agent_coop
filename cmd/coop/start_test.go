package main

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// Each argument must reach coop on the other machine as it was given. The command line goes
// through a shell there, two times over SSH: so for any text, the shell gives it back whole.
func TestShellQuoteKeepsEachArgument(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	chars := `[^\x00]{0,12}`
	if runtime.GOOS == "windows" {
		// The sh of Git for Windows drops a carriage return from its command line.
		chars = `[^\x00\r]{0,12}`
	}
	rapid.Check(t, func(rt *rapid.T) {
		args := rapid.SliceOfN(rapid.StringMatching(chars), 1, 4).Draw(rt, "args")
		out, err := exec.Command("sh", "-c", "for a in "+shellJoin(args)+`; do printf '%s\0' "$a"; done`).Output()
		if err != nil {
			rt.Fatal(err)
		}
		got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
		if strings.Join(got, "\x00") != strings.Join(args, "\x00") {
			rt.Fatalf("got %q, want %q (line %s)", got, args, shellJoin(args))
		}
	})
}

// The agent runs over SSH as the agent user, in a login shell, in its directory, with no
// permission prompts. A path below the home directory needs no "~/": the shell starts there.
func TestStartRunsOverSSHAsTheAgentUser(t *testing.T) {
	prompt := `say it's done; $HOME "x"`
	o := startOpts{machine: "vps", dir: "~/my project", session: "build-42", agent: "reviewer", user: "agent", claudeArgs: []string{"-p", prompt}}
	ssh, err := planStart(o, true)
	want := []string{"ssh", "-t", "-l", "agent", "vps", "bash -lc " + shellQuote("cd 'my project' && exec coop --agent reviewer claude build-42 --permission-mode bypassPermissions -p "+shellQuote(prompt))}
	if err != nil || strings.Join(ssh, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%q %v\nwant %q", ssh, err, want)
	}
	// The home directory, an absolute path, and a path below the home directory.
	for dir, cd := range map[string]string{".": "cd . && ", "~": "cd . && ", "/srv/app": "cd /srv/app && ", "git/app": "cd git/app && "} {
		ssh, _ := planStart(startOpts{machine: "vps", dir: dir, session: "s", user: "agent"}, true)
		if ssh[5] != "bash -lc "+shellQuote(cd+"exec coop claude s --permission-mode bypassPermissions") {
			t.Errorf("%s: %q", dir, ssh)
		}
	}
	// With no terminal here, as from a tool of the orchestrator, SSH asks for none.
	ssh, _ = planStart(startOpts{machine: "vps", dir: "app", session: "s", user: "bob"}, false)
	if strings.Join(ssh[:4], " ") != "ssh -l bob vps" {
		t.Errorf("no terminal: %q", ssh)
	}
}

func TestStartRefusesBadNames(t *testing.T) {
	for _, o := range []startOpts{
		{machine: "vps", dir: "app", session: "Build 42"},
		{machine: "vps", dir: "app", session: "s", agent: "the reviewer"},
		{machine: "vps", dir: "app", session: "s", agent: "operator"},
		{machine: "-oProxyCommand=x", dir: "app", session: "s"},
		{machine: "vps", dir: "", session: "s"},
	} {
		if _, err := planStart(o, true); err == nil {
			t.Errorf("%+v: no error", o)
		}
	}
}

// A mode in the arguments wins: the agent gets only that one.
func TestStartKeepsAPermissionModeOfTheArguments(t *testing.T) {
	for _, args := range [][]string{{"--permission-mode", "auto"}, {"--permission-mode=plan"}, {"--dangerously-skip-permissions"}} {
		ssh, _ := planStart(startOpts{machine: "vps", dir: "app", session: "s", user: "agent", claudeArgs: args}, true)
		if remote := ssh[5]; strings.Contains(remote, "bypassPermissions") || !strings.Contains(remote, shellJoin(args)) {
			t.Errorf("%q: %s", args, remote)
		}
	}
}
