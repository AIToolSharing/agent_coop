package herdr_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/AIToolSharing/agent_coop/internal/herdr"
)

// fake records the calls of the herdr command and answers `agent get` with a status.
type fake struct {
	calls  []string
	status string
	fail   map[string]error // by the first two words of the call
}

func (f *fake) run(_ context.Context, args ...string) ([]byte, error) {
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)
	for prefix, err := range f.fail {
		if strings.HasPrefix(call, prefix) {
			return nil, err
		}
	}
	if strings.HasPrefix(call, "agent get") {
		// The shape of Herdr 0.9.3: {"id":..,"result":{"type":"agent_info","agent":{..}}}.
		return []byte(fmt.Sprintf(`{"id":"cli","result":{"type":"agent_info","agent":{"pane_id":"w1:p3","agent_status":%q,"focused":false}}}`, f.status)), nil
	}
	return []byte(`{"id":"cli","result":{"type":"ok"}}`), nil
}

var inHerdr = map[string]string{"HERDR_ENV": "1", "HERDR_PANE_ID": "w1:p3"}

func TestFromEnvNeedsAHerdrPane(t *testing.T) {
	f := &fake{}
	for name, env := range map[string]map[string]string{
		"no herdr":    {},
		"no pane":     {"HERDR_ENV": "1"},
		"not managed": {"HERDR_PANE_ID": "w1:p3"},
		"bad pane":    {"HERDR_ENV": "1", "HERDR_PANE_ID": "w1:p3; rm -rf /"},
	} {
		if p := herdr.FromEnv(env, f.run); p != nil {
			t.Errorf("%s: a pane %+v", name, p)
		}
	}
	if p := herdr.FromEnv(inHerdr, f.run); p == nil || p.PaneID() != "w1:p3" {
		t.Fatalf("in herdr: %+v", p)
	}
}

// With no Herdr, every method does nothing and gives no error.
func TestANilPaneDoesNothing(t *testing.T) {
	var p *herdr.Pane
	ctx := context.Background()
	if sent, err := p.Interrupt(ctx); sent || err != nil {
		t.Fatalf("Interrupt: %v %v", sent, err)
	}
	if err := p.Show(ctx, "s", "a", "held"); err != nil {
		t.Fatal(err)
	}
	if err := p.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if p.PaneID() != "" {
		t.Fatal("a nil pane has an id")
	}
}

// Escape goes to the pane only when the agent works: for an idle agent it would clear what
// the human typed, and for a blocked one it would answer the question on the screen.
func TestInterruptSendsEscapeOnlyToAnAgentThatWorks(t *testing.T) {
	ctx := context.Background()
	for status, want := range map[string]bool{"working": true, "idle": false, "blocked": false, "done": false, "unknown": false} {
		f := &fake{status: status}
		sent, err := herdr.FromEnv(inHerdr, f.run).Interrupt(ctx)
		if err != nil || sent != want {
			t.Errorf("%s: sent %v err %v", status, sent, err)
		}
		wantCalls := "agent get w1:p3"
		if want {
			wantCalls += "|agent send-keys w1:p3 esc"
		}
		if got := strings.Join(f.calls, "|"); got != wantCalls {
			t.Errorf("%s: calls %q, want %q", status, got, wantCalls)
		}
	}
	// A Herdr that fails sends no key and gives the error.
	f := &fake{status: "working", fail: map[string]error{"agent get": errors.New("no server")}}
	if sent, err := herdr.FromEnv(inHerdr, f.run).Interrupt(ctx); sent || err == nil || len(f.calls) != 1 {
		t.Fatalf("sent %v err %v calls %v", sent, err, f.calls)
	}
}

func TestShowAndClearReportTheTitleAndTheTokens(t *testing.T) {
	ctx := context.Background()
	f := &fake{}
	p := herdr.FromEnv(inHerdr, f.run)
	if err := p.Show(ctx, "build-42", "eng-t1", "held"); err != nil {
		t.Fatal(err)
	}
	if err := p.Show(ctx, "build-42", "eng-t1", "run"); err != nil {
		t.Fatal(err)
	}
	if err := p.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	// The argument order that herdr 0.9.3 takes: the pane id first. With the id last, the
	// real command answered "unknown option: coop:shim".
	want := []string{
		"pane report-metadata w1:p3 --source coop:shim --title coop build-42/eng-t1 · held --token coop=build-42/eng-t1 --token gate=held",
		"pane report-metadata w1:p3 --source coop:shim --title coop build-42/eng-t1 --token coop=build-42/eng-t1 --token gate=run",
		"pane report-metadata w1:p3 --source coop:shim --clear-title --clear-token coop --clear-token gate",
	}
	if fmt.Sprint(f.calls) != fmt.Sprint(want) {
		t.Fatalf("\n got %q\nwant %q", f.calls, want)
	}
}

func TestFocusOnThisMachineAndOnASavedMachine(t *testing.T) {
	ctx := context.Background()
	f := &fake{}
	if err := herdr.Focus(ctx, f.run, "", "w1:p3"); err != nil {
		t.Fatal(err)
	}
	if err := herdr.Focus(ctx, f.run, "basedmatrix", "w2:p1"); err != nil {
		t.Fatal(err)
	}
	if err := herdr.Focus(ctx, f.run, "", "--help"); err == nil {
		t.Fatal("a pane id that is a flag passed")
	}
	want := []string{"agent focus w1:p3", "--machine basedmatrix agent focus w2:p1"}
	if fmt.Sprint(f.calls) != fmt.Sprint(want) {
		t.Fatalf("\n got %q\nwant %q", f.calls, want)
	}
}

// Found in use: on a machine that Herdr reaches over SSH, herdr is in ~/.local/bin, which is
// not on the PATH of a pane's shell. The shim must still find it.
func TestBinaryFallsBackToTheLocalInstall(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no herdr on the PATH
	there := func(string) bool { return true }
	gone := func(string) bool { return false }
	if got := herdr.Binary(map[string]string{"HERDR_BIN_PATH": "/opt/herdr", "HOME": "/home/agent"}, there); got != "/opt/herdr" {
		t.Errorf("with HERDR_BIN_PATH: %q", got)
	}
	if got := herdr.Binary(map[string]string{"HOME": "/home/agent"}, there); got != "/home/agent/.local/bin/herdr" {
		t.Errorf("not on the PATH, installed in the home directory: %q", got)
	}
	if got := herdr.Binary(map[string]string{"HOME": "/home/agent"}, gone); got != "herdr" {
		t.Errorf("not installed: %q", got)
	}
	if got := herdr.Binary(map[string]string{}, there); got != "herdr" {
		t.Errorf("no home directory: %q", got)
	}
}

func TestIsPaneID(t *testing.T) {
	for id, want := range map[string]bool{
		"w1:p1": true, "w12:p340": true, "a.b_c-d": true,
		"": false, "-w1": false, "--help": false, "w1 p1": false, "w1:p1;x": false, "$(x)": false, strings.Repeat("a", 65): false,
	} {
		if herdr.IsPaneID(id) != want {
			t.Errorf("IsPaneID(%q) = %v", id, !want)
		}
	}
}
