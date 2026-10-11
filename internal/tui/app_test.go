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
)

type fakeOp struct{ calls []string }

func (f *fakeOp) DeleteSession(_ context.Context, s string) error {
	f.calls = append(f.calls, "delete "+s)
	return nil
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
	contains(t, f, "coop · all sessions · transcript  follow ●  [connecting]", "SESSIONS", "all traffic", "● build-42 3 1 ask", "○ docs", "Please run the tests before you say done", "/ search · : command")
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
	contains(t, h.frame(), "AGENTS · build-42")
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
	// The activity view is gone: 3 changes nothing.
	h.keys("3")
	contains(t, h.frame(), "coop · build-42 · transcript")
}

func TestCommandDelete(t *testing.T) {
	h := start(t, nil)
	h.openSession()
	h.keys(":", "+delete", "enter")
	contains(t, h.frame(), "delete build-42 and its messages?")
	h.keys("n")
	if h.status() != "cancelled" || len(h.op.calls) != 0 {
		t.Fatalf("status %q calls %v", h.status(), h.op.calls)
	}
	h.keys(":", "+delete", "enter", "y")
	if fmt.Sprint(h.op.calls) != "[delete build-42]" {
		t.Fatalf("calls %v", h.op.calls)
	}
}

func TestCommandsCompleteAndTheRemovedOnesAreUnknown(t *testing.T) {
	h := start(t, nil)
	h.openSession()
	h.keys(":", "+fi", "tab")
	contains(t, h.frame(), ":filter ▏")
	h.keys("+ca", "tab")
	contains(t, h.frame(), ":filter carol@mac-3▏")
	h.keys("enter")
	if h.status() != "only carol@mac-3" {
		t.Fatalf("status %q", h.status())
	}
	for _, cmd := range []string{"kick bob", "allow bob", "forget bob", "pause", "resume", "close", "reopen", "withdraw #10", "brief", "new team-2"} {
		h.keys(":", "+"+cmd, "enter")
		if word, _, _ := strings.Cut(cmd, " "); h.status() != "unknown command :"+word+" (tab lists them)" {
			t.Fatalf(":%s: status %q", cmd, h.status())
		}
	}
	// The keys of the gate are gone: they do nothing.
	h.keys("tab", "down", "down", "p", "x", "P", "R")
	if len(h.op.calls) != 0 {
		t.Fatalf("calls %v", h.op.calls)
	}
}

// The operator only watches. The TUI has no composer, no attention line and no "for you"
// marks, and the keys that wrote to the agents do nothing.
func TestTheOperatorOnlyWatches(t *testing.T) {
	h := start(t, nil)
	h.openSession()
	gone := func(when string) {
		t.Helper()
		for _, text := range []string{"⚑", "for you", "to all", "reply to #", "m message", "a attention"} {
			if strings.Contains(h.frame(), text) {
				t.Errorf("%s: the frame shows %q:\n%s", when, text, h.frame())
			}
		}
	}
	gone("open")
	for _, k := range []string{"a", "m", "r"} {
		h.keys(k)
		gone("after " + k)
		h.keys("+hi", "enter", "esc")
	}
	if len(h.op.calls) != 0 {
		t.Fatalf("calls %v", h.op.calls)
	}
}

func TestHelpAndFiltersAndSidebarToggle(t *testing.T) {
	h := start(t, nil)
	h.keys("?")
	contains(t, h.frame(), "coop · help · esc back", "MOVE", ":delete", ":filter [agent]")
	for _, gone := range []string{"STEER", "WRITE", ":new", "attention", ":kick", ":pause", ":withdraw", ":brief", "activity"} {
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

func TestKeysThatArriveTogetherActOnTheLatestState(t *testing.T) {
	h := start(t, nil)
	h.keys("tab", "down", "enter", "tab", "down", "down", "enter")
	contains(t, h.frame(), "agent alice@mac-1 · esc back")
}
