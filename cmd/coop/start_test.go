package main

import (
	"os/exec"
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
	rapid.Check(t, func(rt *rapid.T) {
		args := rapid.SliceOfN(rapid.StringMatching(`[^\x00]{0,12}`), 1, 4).Draw(rt, "args")
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

const machines = "id1\tbasedmatrix\tssh://agent@vps\tdefault\tenabled\nid2\toffline\tssh://agent@old\tdefault\tdisabled\n"

func TestStartGoesToHerdrWhenHerdrKnowsTheMachine(t *testing.T) {
	p, err := planStart(startOpts{machine: "basedmatrix", dir: "git/app", session: "build-42", user: "agent", claudeArgs: []string{"--model", "opus"}}, machines, true)
	if err != nil || !p.herdr || p.cwd != "~/git/app" || p.label != "build-42/app" || p.command != "coop claude build-42 --model opus" {
		t.Fatalf("%+v %v", p, err)
	}
	// The home directory, an absolute path, a path with "~/", and an agent name.
	for dir, cwd := range map[string]string{".": "~", "/srv/app": "/srv/app", "~/git/app": "~/git/app"} {
		p, _ := planStart(startOpts{machine: "basedmatrix", dir: dir, session: "build-42", agent: "reviewer"}, machines, true)
		if p.cwd != cwd || p.label != "build-42/reviewer" || p.command != "coop --agent reviewer claude build-42" {
			t.Errorf("%s: %+v", dir, p)
		}
	}
}

func TestStartUsesSSHWhenAskedOrWhenHerdrDoesNotHaveTheMachine(t *testing.T) {
	prompt := `say it's done; $HOME "x"`
	for _, o := range []startOpts{
		{machine: "basedmatrix", sshOnly: true},
		{machine: "offline"},
		{machine: "vps"},
	} {
		o.dir, o.session, o.agent, o.user, o.claudeArgs = "~/my project", "build-42", "reviewer", "agent", []string{"-p", prompt}
		p, err := planStart(o, machines, true)
		want := []string{"ssh", "-t", "-l", "agent", o.machine, "bash -lc " + shellQuote("cd 'my project' && exec coop --agent reviewer claude build-42 -p "+shellQuote(prompt))}
		if err != nil || p.herdr || strings.Join(p.ssh, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s: %+v %v\nwant %q", o.machine, p, err, want)
		}
	}
	// With no terminal here, as from a tool of the orchestrator, SSH asks for none.
	p, _ := planStart(startOpts{machine: "vps", dir: "app", session: "s", user: "bob"}, "", false)
	if strings.Join(p.ssh[:4], " ") != "ssh -l bob vps" {
		t.Errorf("no terminal: %q", p.ssh)
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
		if _, err := planStart(o, machines, true); err == nil {
			t.Errorf("%+v: no error", o)
		}
	}
}
