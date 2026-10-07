package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/AIToolSharing/agent_coop/internal/model"
	"github.com/AIToolSharing/agent_coop/internal/model/modeltest"
	"github.com/AIToolSharing/agent_coop/internal/wire"
)

type fakeOp struct{ calls []string }

func (f *fakeOp) CreateSession(_ context.Context, s string) error {
	f.calls = append(f.calls, "create "+s)
	return nil
}
func (f *fakeOp) CloseSession(_ context.Context, s string) error {
	f.calls = append(f.calls, "close "+s)
	return nil
}
func (f *fakeOp) ReopenSession(_ context.Context, s string) error {
	f.calls = append(f.calls, "reopen "+s)
	return nil
}
func (f *fakeOp) DeleteSession(_ context.Context, s string) error {
	f.calls = append(f.calls, "delete "+s)
	return nil
}
func (f *fakeOp) Kick(_ context.Context, s, t string) error {
	f.calls = append(f.calls, "kick "+s+" "+t)
	return nil
}
func (f *fakeOp) Unkick(_ context.Context, s, t string) error {
	f.calls = append(f.calls, "unkick "+s+" "+t)
	return nil
}
func (f *fakeOp) Forget(_ context.Context, s, t string) error {
	f.calls = append(f.calls, "forget "+s+" "+t)
	return nil
}
func (f *fakeOp) SetGate(_ context.Context, s, t, g string) error {
	f.calls = append(f.calls, "gate "+s+" "+t+" "+g)
	return nil
}
func (f *fakeOp) Redact(_ context.Context, s, id string) (bool, error) {
	f.calls = append(f.calls, "redact "+s+" "+id)
	return true, nil
}
func (f *fakeOp) Send(_ context.Context, s, to, text, replyTo string) (string, error) {
	c := "send " + s + " " + to + " " + text
	if replyTo != "" {
		c += " ↩" + replyTo
	}
	f.calls = append(f.calls, c)
	return "99", nil
}

type harness struct {
	t   *testing.T
	app *App
	op  *fakeOp
}

func start(t *testing.T, store *model.Store) *harness {
	t.Helper()
	if store == nil {
		store = model.New()
		for _, u := range modeltest.Fixture() {
			store.Apply(u)
		}
	}
	op := &fakeOp{}
	app := New(Options{Store: store, Op: op, Now: func() time.Time { return modeltest.Now }, Loc: time.UTC})
	app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return &harness{t: t, app: app, op: op}
}

func keyOf(name string) tea.KeyPressMsg {
	switch name {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "alt+enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "pgdn":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	r := []rune(name)
	return tea.KeyPressMsg{Code: r[0], Text: name}
}

// send delivers one message and runs the command it returns, at once.
func (h *harness) send(msg tea.Msg) {
	h.t.Helper()
	_, cmd := h.app.Update(msg)
	if cmd != nil {
		if out := cmd(); out != nil {
			if _, isQuit := out.(tea.QuitMsg); !isQuit {
				h.app.Update(out)
			}
		}
	}
}

// keys types key names, or the characters of a word prefixed with a plus sign ("+hello").
func (h *harness) keys(names ...string) {
	h.t.Helper()
	for _, n := range names {
		if strings.HasPrefix(n, "+") {
			for _, r := range n[1:] {
				h.send(keyOf(string(r)))
			}
			continue
		}
		h.send(keyOf(n))
	}
}

func (h *harness) frame() string { return ansi.Strip(h.app.View().Content) }

func (h *harness) status() string {
	lines := strings.Split(h.frame(), "\n")
	return strings.TrimSpace(lines[len(lines)-2])
}

func (h *harness) openSession() {
	h.t.Helper()
	h.keys("tab", "down", "enter")
	if !strings.Contains(h.frame(), "coop · build-42 · transcript") {
		h.t.Fatalf("the session did not open:\n%s", h.frame())
	}
}

func contains(t *testing.T, frame string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(frame, w) {
			t.Errorf("missing %q in\n%s", w, frame)
		}
	}
}

func TestShowsTheSidebarTheTranscriptAndTheHints(t *testing.T) {
	h := start(t, nil)
	f := h.frame()
	contains(t, f, "coop · all sessions · transcript  follow ●  [connecting]", "SESSIONS", "all traffic", "● build-42 3/3 1 ask", "✕ docs", "Please run the tests before you say done", "m message · r reply · a attention")
	if n := len(strings.Split(f, "\n")); n != 30 {
		t.Errorf("%d rows, want 30", n)
	}
	for i, line := range strings.Split(f, "\n") {
		if w := ansi.StringWidth(line); w != 100 {
			t.Errorf("row %d has width %d: %q", i, w, line)
		}
	}
}

func TestTheSidebarMovesAndOpens(t *testing.T) {
	h := start(t, nil)
	h.openSession()
	contains(t, h.frame(), "AGENTS · build-42", "⚑ 1 for you · 1 ask waiting 4m · 1 blocked")
	h.keys("tab", "down", "down", "enter")
	contains(t, h.frame(), "agent alice@mac-1 · esc back", "state        working")
	h.keys("esc")
	contains(t, h.frame(), "coop · build-42 · transcript")
}

func TestViews(t *testing.T) {
	h := start(t, nil)
	h.openSession()
	h.keys("2")
	contains(t, h.frame(), "coop · build-42 · threads", "OPEN ASKS (1)")
	h.keys("1")
	contains(t, h.frame(), "coop · build-42 · transcript")
	// The activity view follows the newest item, as the transcript does.
	h.app.store.Apply(model.Update{Trace: &wire.TraceUpdate{Boot: 1, Key: "build-42.mac-1.alice", Items: []wire.TraceItem{
		{N: 1, At: modeltest.At(290), Kind: wire.TraceToolStart, ID: "t1", Tool: "Bash", Text: "go test ./..."},
	}}})
	h.keys("3")
	contains(t, h.frame(), "coop · build-42 · activity  follow ●", "▸ Bash: go test ./...", "1 transcript · 2 threads")
	h.keys("space")
	if strings.Contains(h.frame(), "follow ●") {
		t.Fatalf("space should stop the follow mode in the activity view:\n%s", h.frame())
	}
}

func TestComposeToAllToOneAgentAndWithAName(t *testing.T) {
	h := start(t, nil)
	h.openSession()
	h.keys("m")
	contains(t, h.frame(), "to all (tab: next) ›")
	h.keys("+hi", "enter")
	h.keys("m", "tab")
	contains(t, h.frame(), "to any (tab: next) ›")
	h.keys("+go", "enter")
	h.keys("m", "tab", "tab")
	contains(t, h.frame(), "to alice@mac-1 (tab: next) ›")
	h.keys("+yo", "enter")
	h.keys("m", "+@bob hi", "enter")
	want := []string{"send build-42 all hi", "send build-42 any go", "send build-42 alice@mac-1 yo", "send build-42 bob@vps-2 hi"}
	if fmt.Sprint(h.op.calls) != fmt.Sprint(want) {
		t.Fatalf("calls %v", h.op.calls)
	}
	if h.status() != "sent #99" {
		t.Errorf("status %q", h.status())
	}
}

func TestReplyGoesToTheSenderInItsThread(t *testing.T) {
	h := start(t, nil)
	h.openSession()
	h.keys("r")
	contains(t, h.frame(), "reply to #14 from carol@mac-3 ›")
	h.keys("+ok", "enter")
	if fmt.Sprint(h.op.calls) != "[send build-42 carol@mac-3 ok ↩14]" {
		t.Fatalf("calls %v", h.op.calls)
	}
}

func TestAltEnterAddsALine(t *testing.T) {
	h := start(t, nil)
	h.openSession()
	h.keys("m", "+a", "alt+enter", "+b", "enter")
	if fmt.Sprint(h.op.calls) != "[send build-42 all a\nb]" {
		t.Fatalf("calls %q", h.op.calls)
	}
}

func TestAWalksThroughWhatNeedsTheOperator(t *testing.T) {
	h := start(t, nil)
	h.openSession()
	h.keys("a")
	if h.status() != "for you: #13 from bob@vps-2" || h.app.msgID != "13" {
		t.Fatalf("status %q selected %q", h.status(), h.app.msgID)
	}
	h.keys("a")
	if h.status() != "carol@mac-3 waits for bob@vps-2 (ask #14)" || h.app.msgID != "14" {
		t.Fatalf("status %q selected %q", h.status(), h.app.msgID)
	}
	h.keys("a")
	contains(t, h.frame(), "agent bob@vps-2 · esc back")
	if h.status() != "bob@vps-2 is blocked: waiting for CI" {
		t.Fatalf("status %q", h.status())
	}
}

func TestCommandsNewCloseDelete(t *testing.T) {
	h := start(t, nil)
	h.openSession()
	h.keys(":", "+new team-2", "enter")
	h.keys(":", "+delete", "enter")
	if h.status() != "close the session first (:close)" {
		t.Fatalf("status %q", h.status())
	}
	h.keys(":", "+close", "enter")
	contains(t, h.frame(), "close build-42?")
	h.keys("y")
	if fmt.Sprint(h.op.calls) != "[create team-2 close build-42]" {
		t.Fatalf("calls %v", h.op.calls)
	}
}

func TestCommandsCompleteAndKickAsksFirst(t *testing.T) {
	h := start(t, nil)
	h.openSession()
	h.keys(":", "+ki", "tab")
	contains(t, h.frame(), ":kick ▏")
	h.keys("+bo", "tab")
	contains(t, h.frame(), ":kick bob@vps-2▏")
	h.keys("enter")
	contains(t, h.frame(), "remove bob@vps-2 from build-42?")
	h.keys("n")
	if len(h.op.calls) != 0 || h.status() != "cancelled" {
		t.Fatalf("calls %v status %q", h.op.calls, h.status())
	}
	h.keys(":", "+allow carol", "enter")
	if fmt.Sprint(h.op.calls) != "[unkick build-42 carol@mac-3]" {
		t.Fatalf("calls %v", h.op.calls)
	}
}

// storeWhereBobLeft gives the fixture after Bob left.
func storeWhereBobLeft() *model.Store {
	s := model.New()
	for _, u := range modeltest.Fixture() {
		s.Apply(u)
	}
	s.Apply(model.Update{Presence: &wire.PresenceUpdate{Key: "build-42.vps-2.bob", Revision: 1000}})
	s.Apply(model.Update{Event: &wire.Event{Kind: wire.EventActivity, Seq: 500, SID: "build-42", From: "bob@vps-2", Activity: &wire.Activity{Kind: "left", Reason: "disconnected", At: modeltest.At(80)}}})
	return s
}

func TestForgetTakesAnAgentThatLeft(t *testing.T) {
	h := start(t, storeWhereBobLeft())
	h.openSession()
	// An agent in the session cannot be forgotten.
	h.keys(":", "+forget alice", "enter")
	if len(h.op.calls) != 0 || h.status() != "alice@mac-1 is in the session; :kick removes it" {
		t.Fatalf("calls %v status %q", h.op.calls, h.status())
	}
	h.keys(":", "+fo", "tab")
	contains(t, h.frame(), ":forget ▏")
	h.keys("+bo", "tab")
	contains(t, h.frame(), ":forget bob@vps-2▏")
	h.keys("enter")
	if fmt.Sprint(h.op.calls) != "[forget build-42 bob@vps-2]" || h.status() != "forgot bob@vps-2" {
		t.Fatalf("calls %v status %q", h.op.calls, h.status())
	}
}

func TestForgetWithoutANameTakesEachAgentThatLeftAndAsksFirst(t *testing.T) {
	h := start(t, storeWhereBobLeft())
	h.openSession()
	h.keys(":", "+forget", "enter")
	contains(t, h.frame(), "forget 1 agents that left build-42?")
	h.keys("n")
	if len(h.op.calls) != 0 {
		t.Fatalf("calls %v", h.op.calls)
	}
	h.keys(":", "+forget", "enter", "y")
	if fmt.Sprint(h.op.calls) != "[forget build-42 bob@vps-2]" {
		t.Fatalf("calls %v", h.op.calls)
	}
	// With every agent in the session there is nothing to forget.
	h2 := start(t, nil)
	h2.openSession()
	h2.keys(":", "+forget", "enter")
	if len(h2.op.calls) != 0 || h2.status() != "no agent that left in build-42" {
		t.Fatalf("calls %v status %q", h2.op.calls, h2.status())
	}
}

// storeWithGates gives the fixture where Bob is held (an orchestrator was in the session when
// he joined) and Carol is paused. Alice works. Bob and Carol were started with the gate.
func storeWithGates() *model.Store {
	s := model.New()
	for _, u := range modeltest.Fixture() {
		s.Apply(u)
	}
	gate := func(seq int64, from, g string) {
		s.Apply(model.Update{Event: &wire.Event{Kind: wire.EventActivity, Seq: seq, SID: "build-42", From: from, Activity: &wire.Activity{Kind: "gate", Gate: g, At: modeltest.At(80)}}})
	}
	gate(500, "bob@vps-2", "held")
	gate(501, "carol@mac-3", "paused")
	for i, key := range []string{"build-42.vps-2.bob", "build-42.mac-3.carol"} {
		s.Apply(model.Update{Presence: &wire.PresenceUpdate{Key: key, Revision: int64(800 + i), Record: &wire.PresenceRecord{
			Host: "h", Cwd: "/src/app", Client: wire.Client{Name: "claude-code", Version: "2.1"}, State: "idle", JoinedAt: modeltest.At(0), Gated: true,
		}}})
	}
	s.Apply(model.Update{Session: &wire.SessionUpdate{SID: "build-42", Revision: 900, Record: &wire.SessionRecord{Status: "open", CreatedAt: modeltest.At(-60)}}})
	return s
}

// The operator's complaint: no control surface. The gate keys act on the agent under the
// sidebar cursor: p pauses or resumes, x stops. No key of the operator releases: g does nothing.
func TestGateKeysSteerTheSelectedAgent(t *testing.T) {
	h := start(t, storeWithGates())
	h.openSession()
	contains(t, h.frame(), "bob@vps-2 held", "carol@mac-3 paused")
	// With no agent selected the keys do nothing to an agent.
	h.keys("g", "p", "x")
	if len(h.op.calls) != 0 {
		t.Fatalf("calls with no agent selected: %v", h.op.calls)
	}
	// tab puts the cursor on the session row; the rows below are docs, alice, bob, carol.
	h.keys("tab", "down", "down")
	contains(t, h.frame(), "p pause/resume · x stop")
	// alice works: g does nothing, p pauses her.
	h.keys("g")
	if len(h.op.calls) != 0 || h.status() != "" {
		t.Fatalf("g: calls %v status %q", h.op.calls, h.status())
	}
	h.keys("p")
	// bob is held: p lets him go.
	h.keys("down", "p")
	if h.status() != "resumed bob@vps-2" {
		t.Fatalf("status %q", h.status())
	}
	// carol is paused: p resumes her; x asks before it removes her.
	h.keys("down", "p", "x")
	contains(t, h.frame(), "remove carol@mac-3 from build-42?")
	h.keys("y")
	want := "[gate build-42 alice@mac-1 paused" +
		" gate build-42 bob@vps-2 run" +
		" gate build-42 carol@mac-3 run kick build-42 carol@mac-3]"
	if fmt.Sprint(h.op.calls) != want {
		t.Fatalf("calls\n got %v\nwant %s", h.op.calls, want)
	}
}

func TestResumeFromTheDetails(t *testing.T) {
	h := start(t, storeWithGates())
	h.openSession()
	h.keys("tab", "down", "down", "down", "enter")
	contains(t, h.frame(), "agent bob@vps-2", "held: it waits for the orchestrator's task (p lets it go)")
	h.keys("p")
	if fmt.Sprint(h.op.calls) != "[gate build-42 bob@vps-2 run]" || h.status() != "resumed bob@vps-2" {
		t.Fatalf("calls %v status %q", h.op.calls, h.status())
	}
}

func TestSessionKeysAndCommandsSetGates(t *testing.T) {
	h := start(t, storeWithGates())
	h.openSession()
	// P pauses each agent that works (alice), R resumes each held or paused one (bob, carol).
	h.keys("P")
	h.keys("R")
	// :resume with a name resumes that agent.
	h.keys(":", "+resume carol", "enter")
	want := "[gate build-42 alice@mac-1 paused" +
		" gate build-42 bob@vps-2 run gate build-42 carol@mac-3 run" +
		" gate build-42 carol@mac-3 run]"
	if fmt.Sprint(h.op.calls) != want {
		t.Fatalf("calls\n got %v\nwant %s", h.op.calls, want)
	}
	h.keys(":", "+pause alice now", "enter")
	if h.status() != "usage: :pause [agent]" {
		t.Fatalf("status %q", h.status())
	}
	h.keys(":", "+resume alice", "enter")
	if h.status() != "alice@mac-1 is run already" {
		t.Fatalf("status %q", h.status())
	}
	// The commands of the hold are gone.
	for _, cmd := range []string{"go", "go bob", "hold on", "hold off"} {
		h.keys(":", "+"+cmd, "enter")
		if word, _, _ := strings.Cut(cmd, " "); h.status() != "unknown command :"+word+" (tab lists them)" {
			t.Fatalf(":%s: status %q", cmd, h.status())
		}
	}
	if len(h.op.calls) != 4 {
		t.Fatalf("a command of the hold acted: %v", h.op.calls)
	}
}

// A held or paused agent waits for the orchestrator or for nobody, not for the operator: `a`
// does not go to it.
func TestAttentionSkipsAHeldOrPausedAgent(t *testing.T) {
	h := start(t, storeWithGates())
	h.openSession()
	for range 6 {
		h.keys("a")
		if strings.Contains(h.frame(), "agent bob@vps-2") || strings.Contains(h.frame(), "agent carol@mac-3") {
			t.Fatalf("a opened a held or paused agent:\n%s", h.frame())
		}
		h.keys("esc")
	}
}

func TestWithdrawAsksFirst(t *testing.T) {
	h := start(t, nil)
	h.openSession()
	h.keys(":", "+withdraw #10", "enter")
	contains(t, h.frame(), "withdraw #10?")
	h.keys("y")
	if fmt.Sprint(h.op.calls) != "[redact build-42 10]" || h.status() != "withdrew #10" {
		t.Fatalf("calls %v status %q", h.op.calls, h.status())
	}
}

func TestHelpAndFiltersAndSidebarToggle(t *testing.T) {
	h := start(t, nil)
	h.keys("?")
	contains(t, h.frame(), "coop · help · esc back", "MOVE", "STEER")
	// The help is longer than the screen: it scrolls.
	h.keys("pgdn")
	contains(t, h.frame(), ":withdraw [#id]", ":pause [agent]  :resume [agent]")
	for _, gone := range []string{":go", ":hold", "H\t", "g\t"} {
		if strings.Contains(h.frame(), gone) {
			t.Errorf("the help still shows %q", gone)
		}
	}
	h.keys("esc")
	contains(t, h.frame(), "coop · all sessions · transcript")
	h.keys("[")
	if strings.Contains(h.frame(), "SESSIONS") {
		t.Error("the sidebar is still shown")
	}
	h.keys("[")
	contains(t, h.frame(), "SESSIONS")
	h.openSession()
	h.keys(":", "+filter carol", "enter")
	contains(t, h.frame(), "[carol@mac-3]")
	if strings.Contains(h.frame(), "What is the shape of GET /users?") {
		t.Error("the agent filter did not apply")
	}
	h.keys("esc")
	contains(t, h.frame(), "What is the shape of GET /users?")
	h.keys("/", "+users", "enter")
	contains(t, h.frame(), "[/users]")
}

func TestScrollingAndFollowing(t *testing.T) {
	h := start(t, nil)
	h.app.Update(tea.WindowSizeMsg{Width: 60, Height: 14})
	h.openSession()
	contains(t, h.frame(), "follow ●")
	h.keys("pgup")
	if strings.Contains(h.frame(), "follow ●") {
		t.Error("page up should stop following")
	}
	h.keys("end")
	contains(t, h.frame(), "follow ●")
	h.send(tea.MouseWheelMsg{X: 50, Y: 5, Button: tea.MouseWheelUp})
	if strings.Contains(h.frame(), "follow ●") {
		t.Error("the wheel should stop following")
	}
	for range 20 {
		h.send(tea.MouseWheelMsg{X: 50, Y: 5, Button: tea.MouseWheelDown})
	}
	contains(t, h.frame(), "follow ●")
}

func TestClicksSelectAndOpen(t *testing.T) {
	h := start(t, nil)
	// Row 3 of the screen is the build-42 session row (title, SESSIONS, all traffic, build-42).
	h.send(tea.MouseClickMsg{X: 2, Y: 3, Button: tea.MouseLeft})
	contains(t, h.frame(), "coop · build-42 · transcript")
	// Find the header line of message #12 in the main pane and click it twice.
	s := h.app.last
	line := -1
	for i, id := range s.main.IDs {
		if id == "12" {
			line = i
		}
	}
	if line < 0 {
		t.Fatal("no header for #12")
	}
	y := line - h.app.viewOffset(s) + 1
	if s.attn != nil {
		y++
	}
	h.send(tea.MouseClickMsg{X: s.sidebarW + 5, Y: y, Button: tea.MouseLeft})
	if h.app.msgID != "12" {
		t.Fatalf("selected %q", h.app.msgID)
	}
	h.send(tea.MouseClickMsg{X: s.sidebarW + 5, Y: y, Button: tea.MouseLeft})
	contains(t, h.frame(), "message #12 · esc back", "REPLIES (1)")
}

func TestEnterOpensTheMessageDetails(t *testing.T) {
	h := start(t, nil)
	h.openSession()
	h.keys("up", "up", "enter")
	contains(t, h.frame(), "message #12 · esc back", "Please run the tests before you say done")
	h.keys("esc")
	contains(t, h.frame(), "coop · build-42 · transcript")
}

func TestAMessageToAnAgentThatLeftIsSentWithANote(t *testing.T) {
	store := model.New()
	for _, u := range modeltest.Fixture() {
		store.Apply(u)
	}
	store.Apply(model.Update{Presence: &wire.PresenceUpdate{Key: "build-42.vps-2.bob", Revision: 0}})
	h := start(t, store)
	h.openSession()
	h.keys("m", "+@bob hi", "enter")
	if fmt.Sprint(h.op.calls) != "[send build-42 bob@vps-2 hi]" {
		t.Fatalf("calls %v", h.op.calls)
	}
	if !strings.Contains(h.status(), "bob@vps-2 is away") {
		t.Fatalf("status %q", h.status())
	}
}

func TestKeysThatArriveTogetherActOnTheLatestState(t *testing.T) {
	h := start(t, nil)
	h.keys("tab", "down", "enter", "tab", "down", "down", "enter")
	contains(t, h.frame(), "agent alice@mac-1 · esc back")
}

// :brief runs a local Claude Code run over the shown session and shows the summary in the
// main pane. The TUI gives it the agents and the newest messages; the hub is not involved.
// `every` repeats it on the tick; `off` and a change of the shown session end the repeats.
func TestBriefRunsALocalSummaryAndShowsIt(t *testing.T) {
	h := start(t, nil)
	var inputs []string
	h.app.brief = func(_ context.Context, input string) (string, error) {
		inputs = append(inputs, input)
		return "Alice parses. Bob waits for CI.\nCarol waits on Bob.", nil
	}
	h.keys(":", "+brief", "enter")
	if h.status() != "pick a session first (tab, then ↑↓)" || len(inputs) != 0 {
		t.Fatalf("with no session: status %q, %d runs", h.status(), len(inputs))
	}
	h.openSession()
	h.keys(":", "+brief", "enter")
	if len(inputs) != 1 || !strings.Contains(inputs[0], "alice@mac-1: online, working") || !strings.Contains(inputs[0], "Can I change the users table?") {
		t.Fatalf("inputs %q", inputs)
	}
	contains(t, h.frame(), "Bob waits for CI.")
	if h.status() != "brief of build-42" {
		t.Fatalf("status %q", h.status())
	}
	h.keys("esc")
	if strings.Contains(h.frame(), "Bob waits for CI.") {
		t.Fatal("esc did not close the brief")
	}
	// every: one run now, the next ones on the tick after the interval.
	h.keys(":", "+brief", "space", "+every", "space", "+10m", "enter")
	if len(inputs) != 2 {
		t.Fatalf("%d runs after :brief every", len(inputs))
	}
	h.keys("esc")
	now := modeltest.Now
	h.app.now = func() time.Time { return now }
	if cmd := h.app.briefTick(now); cmd != nil {
		t.Fatal("a tick before the interval ran a brief")
	}
	now = now.Add(11 * time.Minute)
	if cmd := h.app.briefTick(now); cmd == nil {
		t.Fatal("no brief after the interval")
	} else {
		h.app.Update(cmd())
	}
	if len(inputs) != 3 {
		t.Fatalf("%d runs after the interval", len(inputs))
	}
	h.keys("esc", ":", "+brief", "space", "+off", "enter")
	now = now.Add(11 * time.Minute)
	if cmd := h.app.briefTick(now); cmd != nil || h.status() != "brief: repeats off" {
		t.Fatalf("brief off: cmd %v status %q", cmd != nil, h.status())
	}
	h.keys(":", "+brief", "space", "+every", "space", "+soon", "enter")
	if h.status() != "usage: :brief [every <N>m | off]" {
		t.Fatalf("status %q", h.status())
	}
	// A failure of the run goes to the status line.
	h.app.brief = func(context.Context, string) (string, error) { return "", errors.New("claude is not on this machine") }
	h.keys(":", "+brief", "enter")
	if h.status() != "brief: claude is not on this machine" {
		t.Fatalf("status %q", h.status())
	}
}
