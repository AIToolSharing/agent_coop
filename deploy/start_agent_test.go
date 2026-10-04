// Package deploy_test checks the scripts of this directory that can run without root.
package deploy_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// rig is a directory with stand-ins for ssh, coop and herdr, first on the PATH. The stand-in
// for ssh runs the command string as the shell on the other machine would, in a stand-in home
// directory. The stand-in for coop prints its directory and each argument. The stand-in for
// herdr knows one enabled machine, basedmatrix, and one that is not enabled, and records the
// calls that change something.
type rig struct {
	t   *testing.T
	dir string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	dir := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "bin", name), []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{"bin", "home/git/app", "home/my project"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("ssh", `#!/bin/sh
# ssh -t -l <user> <host> <command>
echo "ssh user=$3 host=$4" >>"$RIG/calls"
cd "$RIG/home" && exec sh -c "$5"
`)
	write("coop", `#!/bin/sh
echo "cwd=$(basename "$PWD")"
for a in "$@"; do echo "arg=[$a]"; done
`)
	write("herdr", `#!/bin/sh
if [ "$1 $2" = "machine list" ]; then
  printf 'id1\tbasedmatrix\tssh://agent@vps\tdefault\tenabled\n'
  printf 'id2\toffline\tssh://agent@old\tdefault\tdisabled\n'
  exit 0
fi
printf 'herdr' >>"$RIG/calls"
for a in "$@"; do printf ' [%s]' "$a" >>"$RIG/calls"; done
printf '\n' >>"$RIG/calls"
if [ "$3 $4" = "workspace create" ]; then
  echo '{"id":"cli","result":{"type":"workspace_created","root_pane":{"pane_id":"w7:p1"}}}'
fi
`)
	return &rig{t: t, dir: dir}
}

// run runs start-agent.sh with args. It gives the output, the exit code and the recorded calls.
func (r *rig) run(args ...string) (out string, code int, calls string) {
	r.t.Helper()
	_ = os.Remove(filepath.Join(r.dir, "calls"))
	cmd := exec.Command("bash", append([]string{"start-agent.sh"}, args...)...)
	cmd.Env = append(os.Environ(),
		"PATH="+filepath.Join(r.dir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RIG="+r.dir, "COOP_AGENT_USER=")
	b, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		r.t.Fatal(err)
	}
	c, _ := os.ReadFile(filepath.Join(r.dir, "calls"))
	return string(b), code, string(c)
}

// Over SSH, each argument must reach coop on the other machine as it was given: a directory
// with a space, and a prompt with a quote, a semicolon, a dollar sign and double quotes.
func TestStartAgentOverSSHKeepsEachArgument(t *testing.T) {
	r := newRig(t)
	prompt := `say it's done; $HOME "x"`
	out, code, calls := r.run("-a", "reviewer", "vps", "~/my project", "build-42", "--model", "opus", "-p", prompt)
	want := "cwd=my project\narg=[--agent]\narg=[reviewer]\narg=[claude]\narg=[build-42]\narg=[--model]\narg=[opus]\narg=[-p]\narg=[" + prompt + "]\n"
	if code != 0 || out != want {
		t.Fatalf("code %d\n got %q\nwant %q", code, out, want)
	}
	if calls != "ssh user=agent host=vps\n" {
		t.Fatalf("calls %q", calls)
	}
	// -u names another unix user; with no -a, coop gets no agent name.
	out, _, calls = r.run("-u", "bob", "vps", "git/app", "build-42")
	if out != "cwd=app\narg=[claude]\narg=[build-42]\n" || calls != "ssh user=bob host=vps\n" {
		t.Fatalf("out %q calls %q", out, calls)
	}
}

// A machine that Herdr knows gets the agent in a new Herdr workspace: only there does the
// agent run in a Herdr pane. The directory goes to Herdr as a path below "~".
func TestStartAgentUsesHerdrForAMachineThatHerdrKnows(t *testing.T) {
	r := newRig(t)
	out, code, calls := r.run("basedmatrix", "git/app", "build-42", "--model", "opus")
	wantCalls := "herdr [--machine] [basedmatrix] [workspace] [create] [--cwd] [~/git/app] [--label] [build-42/app] [--focus]\n" +
		"herdr [--machine] [basedmatrix] [pane] [run] [w7:p1] [coop claude build-42 --model opus]\n"
	if code != 0 || calls != wantCalls || !strings.Contains(out, "started in herdr on basedmatrix, pane w7:p1") {
		t.Fatalf("code %d out %q\ncalls %q\n want %q", code, out, calls, wantCalls)
	}
	// The home directory itself, an absolute path, and an agent name in the label.
	for project, cwd := range map[string]string{".": "~", "/srv/app": "/srv/app", "~/git/app": "~/git/app"} {
		_, _, calls := r.run("-a", "reviewer", "basedmatrix", project, "build-42")
		if !strings.Contains(calls, "[--cwd] ["+cwd+"] [--label] [build-42/reviewer]") || !strings.Contains(calls, "[coop --agent reviewer claude build-42]") {
			t.Errorf("project %q: calls %q", project, calls)
		}
	}
}

func TestStartAgentUsesSSHWhenAskedOrWhenHerdrDoesNotHaveTheMachine(t *testing.T) {
	r := newRig(t)
	// -s asks for SSH, also for a machine that Herdr knows.
	if out, _, calls := r.run("-s", "basedmatrix", "git/app", "build-42"); calls != "ssh user=agent host=basedmatrix\n" || !strings.HasPrefix(out, "cwd=app\n") {
		t.Fatalf("-s: out %q calls %q", out, calls)
	}
	// A machine that is not enabled in Herdr, and one that Herdr does not have.
	for _, host := range []string{"offline", "vps"} {
		if _, _, calls := r.run(host, "git/app", "build-42"); calls != "ssh user=agent host="+host+"\n" {
			t.Fatalf("%s: calls %q", host, calls)
		}
	}
}

func TestStartAgentRefusesBadNamesAndShowsWithoutRunning(t *testing.T) {
	r := newRig(t)
	for _, args := range [][]string{
		{"vps", "git/app", "Build 42"},
		{"-a", "the reviewer", "vps", "git/app", "build-42"},
		{"vps", "git/app"},
	} {
		if out, code, calls := r.run(args...); code != 2 || calls != "" {
			t.Errorf("%q: code %d calls %q out %q", args, code, calls, out)
		}
	}
	// -n shows what would run and runs nothing, in both ways.
	if out, code, calls := r.run("-n", "basedmatrix", "git/app", "build-42"); code != 0 || calls != "" || out != "in a new herdr workspace on basedmatrix, directory ~/git/app: coop claude build-42\n" {
		t.Errorf("-n with herdr: code %d calls %q out %q", code, calls, out)
	}
	if out, code, calls := r.run("-n", "vps", "git/app", "build-42"); code != 0 || calls != "" || !strings.HasPrefix(out, "on vps as agent: cd git/app && exec coop claude build-42\n") {
		t.Errorf("-n with ssh: code %d calls %q out %q", code, calls, out)
	}
}
