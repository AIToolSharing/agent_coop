package tui

import (
	"context"
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
func (f *fakeOp) SetHold(_ context.Context, s string, hold bool) error {
	f.calls = append(f.calls, fmt.Sprintf("hold %s %v", s, hold))
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
}

func TestComposeToAllToOneAgentAndWithAName(t *testing.T) {
	h := start(t, nil)
	h.openSession()
	h.keys("m")
	contains(t, h.frame(), "to all (tab: next) ›")
	h.keys("+hi", "enter")
	h.keys("m", "tab")
	contains(t, h.frame(), "to alice@mac-1 (tab: next) ›")
	h.keys("+yo", "enter")
	h.keys("m", "+@bob hi", "enter")
	want := []string{"send build-42 all hi", "send build-42 alice@mac-1 yo", "send build-42 bob@vps-2 hi"}
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

// storeWithGates gives the fixture where Bob is held (he joined a session that holds new
// agents) and Carol is paused. Alice works. Bob and Carol were started with the gate.
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
	s.Apply(model.Update{Session: &wire.SessionUpdate{SID: "build-42", Revision: 900, Record: &wire.SessionRecord{Status: "open", CreatedAt: modeltest.At(-60), Hold: true}}})
	return s
}

// The operator's complaint: no control surface. The gate keys act on the agent under the
// sidebar cursor: g releases with an optional task, p pauses or resumes, x stops.
func TestGateKeysSteerTheSelectedAgent(t *testing.T) {
	h := start(t, storeWithGates())
	h.openSession()
	contains(t, h.frame(), "bob@vps-2 held", "carol@mac-3 paused", "new agents are held (H)", "1 held")
	// With no agent selected the keys do nothing to an agent.
	h.keys("g", "p", "x")
	if len(h.op.calls) != 0 {
		t.Fatalf("calls with no agent selected: %v", h.op.calls)
	}
	// tab puts the cursor on the session row; the rows below are docs, alice, bob, carol.
	h.keys("tab", "down", "down")
	contains(t, h.frame(), "g release · p pause/resume · x stop")
	// alice works: g has nothing to release, p pauses her.
	h.keys("g")
	if h.status() != "alice@mac-1 is not held" {
		t.Fatalf("status %q", h.status())
	}
	h.keys("p")
	// bob is held: p does not release him, g does, with a task.
	h.keys("down", "p")
	if h.status() != "bob@vps-2 is held; g releases it" {
		t.Fatalf("status %q", h.status())
	}
	h.keys("g")
	contains(t, h.frame(), "release bob@vps-2 · task (enter: none) ›")
	h.keys("+fix the parser", "enter")
	if h.status() != "released bob@vps-2 with its task" {
		t.Fatalf("status %q", h.status())
	}
	// carol is paused: p resumes her; x asks before it removes her.
	h.keys("down", "p", "x")
	contains(t, h.frame(), "remove carol@mac-3 from build-42?")
	h.keys("y")
	want := "[gate build-42 alice@mac-1 paused" +
		" send build-42 bob@vps-2 fix the parser gate build-42 bob@vps-2 run" +
		" gate build-42 carol@mac-3 run kick build-42 carol@mac-3]"
	if fmt.Sprint(h.op.calls) != want {
		t.Fatalf("calls\n got %v\nwant %s", h.op.calls, want)
	}
}

func TestReleaseWithNoTaskAndFromTheDetails(t *testing.T) {
	h := start(t, storeWithGates())
	h.openSession()
	h.keys("tab", "down", "down", "down", "enter")
	contains(t, h.frame(), "agent bob@vps-2", "held: it does no work until you release it (g)")
	h.keys("g", "enter")
	if fmt.Sprint(h.op.calls) != "[gate build-42 bob@vps-2 run]" || h.status() != "released bob@vps-2" {
		t.Fatalf("calls %v status %q", h.op.calls, h.status())
	}
}

func TestSessionKeysAndCommandsSetGatesAndHold(t *testing.T) {
	h := start(t, storeWithGates())
	h.openSession()
	// P pauses each agent that works (alice), R resumes each paused one (carol): a held
	// agent (bob) stays held in both cases.
	h.keys("P")
	h.keys("R")
	// H turns the hold of the session off; the command turns it on.
	h.keys("H")
	h.keys(":", "+hold on", "enter")
	// :go with no name releases each held agent; with a name and words, the words are the task.
	h.keys(":", "+go", "enter")
	h.keys(":", "+go carol check the tests", "enter")
	want := "[gate build-42 alice@mac-1 paused" +
		" gate build-42 carol@mac-3 run" +
		" hold build-42 false hold build-42 true" +
		" gate build-42 bob@vps-2 run" +
		" send build-42 carol@mac-3 check the tests gate build-42 carol@mac-3 run]"
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
	h.keys(":", "+hold maybe", "enter")
	if h.status() != "usage: :hold on|off" {
		t.Fatalf("status %q", h.status())
	}
}

// The next thing that needs the operator includes an agent that waits for its release.
func TestAttentionGoesToAHeldAgent(t *testing.T) {
	h := start(t, storeWithGates())
	h.openSession()
	for range 6 {
		h.keys("a")
		if strings.Contains(h.frame(), "agent bob@vps-2") {
			if h.status() != "bob@vps-2 is held: g releases it" {
				t.Fatalf("status %q", h.status())
			}
			return
		}
		h.keys("esc")
	}
	t.Fatalf("a never opened the held agent:\n%s", h.frame())
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
	contains(t, h.frame(), ":withdraw [#id]", ":go [agent] [task]", ":hold on|off")
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
