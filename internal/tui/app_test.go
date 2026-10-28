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
	"github.com/AIToolSharing/agent_coop/internal/tui/view"
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
	contains(t, f, "coop · all sessions · transcript  follow ●  [connecting]", "SESSIONS", "all traffic", "● build-42 3 1 ask", "○ docs", "Please run the tests before you say done", "/ search · n N · : command")
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
	// Enter on an agent opens the preview under the transcript; the transcript stays.
	h.keys("tab", "down", "down", "enter")
	contains(t, h.frame(), "coop · build-42 · transcript", "alice@mac-1  ● online", "state        working", "Please run the tests")
	// j in the sidebar moves the preview to the next agent.
	h.keys("j")
	contains(t, h.frame(), "bob@vps-2  ● online", "state        blocked")
	h.keys("esc")
	if strings.Contains(h.frame(), "state        blocked") {
		t.Error("esc did not close the preview")
	}
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
}

// / jumps to the next message whose text has the search, n and N repeat, and the search
// goes around the end. Nothing is hidden.
func TestSearchJumpsAndWraps(t *testing.T) {
	h := start(t, nil)
	h.openSession()
	h.keys("/", "+users", "enter")
	contains(t, h.frame(), "[/users]", "I take src/users.ts and the tests", "Please run the tests")
	// The cursor was on the newest message (#14, which matches); the next match from there
	// is the first one, around the end.
	if h.app.msgID != "4" || h.status() != "search hit BOTTOM, continuing at TOP" {
		t.Fatalf("after /users: cursor %q, status %q", h.app.msgID, h.status())
	}
	h.keys("n")
	if h.app.msgID != "5" || h.status() != "/users" {
		t.Fatalf("after n: cursor %q, status %q", h.app.msgID, h.status())
	}
	h.keys("N", "N")
	if h.app.msgID != "14" || h.status() != "search hit TOP, continuing at BOTTOM" {
		t.Fatalf("after N N: cursor %q, status %q", h.app.msgID, h.status())
	}
	h.keys("/", "+nothing here", "enter")
	if h.status() != "no match: nothing here" || h.app.msgID != "14" {
		t.Fatalf("a miss: cursor %q, status %q", h.app.msgID, h.status())
	}
	h.keys("esc")
	if strings.Contains(h.frame(), "[/nothing") {
		t.Error("esc did not clear the search")
	}
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
	// gg: the top, the cursor on the first message; G: the bottom, following again.
	h.keys("g", "g")
	if strings.Contains(h.frame(), "follow ●") || h.app.msgID != "4" || h.app.top != (view.Anchor{}) {
		t.Fatalf("after gg: cursor %q, top %+v:\n%s", h.app.msgID, h.app.top, h.frame())
	}
	contains(t, h.frame(), "── 2026-09-30 ──")
	h.keys("G")
	contains(t, h.frame(), "follow ●")
	// ctrl-u moves half a page up and puts the cursor on the first message in the window;
	// ctrl-d brings it back down; ctrl-e scrolls one line and leaves the cursor.
	h.send(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	up := h.app.top
	if strings.Contains(h.frame(), "follow ●") || up == (view.Anchor{}) {
		t.Fatalf("after ctrl-u: top %+v:\n%s", up, h.frame())
	}
	cursor := h.app.msgID
	h.send(tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	if h.app.msgID != cursor || !view.Before(up, h.app.top) {
		t.Fatalf("after ctrl-e: cursor %q (was %q), top %+v (was %+v)", h.app.msgID, cursor, h.app.top, up)
	}
	h.send(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	if h.app.top != up {
		t.Fatalf("after ctrl-y: top %+v, want %+v", h.app.top, up)
	}
	h.send(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if !view.Before(up, h.app.top) {
		t.Fatalf("after ctrl-d: top %+v, want after %+v", h.app.top, up)
	}
	// j from the newest message does nothing; k goes up one message and reveals it.
	h.keys("G", "j")
	if h.app.msgID != "14" {
		t.Fatalf("j past the end: cursor %q", h.app.msgID)
	}
	h.keys("k")
	if h.app.msgID != "13" || strings.Contains(h.frame(), "follow ●") {
		t.Fatalf("k: cursor %q", h.app.msgID)
	}
	h.keys("end")
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
	y := line + 1
	h.send(tea.MouseClickMsg{X: s.sidebarW + 5, Y: y, Button: tea.MouseLeft})
	if h.app.msgID != "12" {
		t.Fatalf("selected %q", h.app.msgID)
	}
	h.send(tea.MouseClickMsg{X: s.sidebarW + 5, Y: y, Button: tea.MouseLeft})
	contains(t, h.frame(), "coop · build-42 · transcript", "from         alice@mac-1", "REPLIES (1)")
}

// Enter opens the preview of the message under the cursor under the transcript. The
// transcript keeps every key: j k move the cursor and the preview follows. Enter again or esc
// closes it.
func TestEnterOpensThePreview(t *testing.T) {
	h := start(t, nil)
	h.openSession()
	h.keys("up", "up", "enter")
	f := h.frame()
	contains(t, f, "coop · build-42 · transcript", "#12", "from         alice@mac-1", "REPLIES (1)", "12:00:10  carol@mac-3 → all")
	rule := strings.Index(f, "│────────────")
	if rule < 0 || strings.Index(f, "from         alice@mac-1") < rule || strings.Index(f, "12:00:55  bob@vps-2 → alice@mac-1") > rule {
		t.Fatalf("the preview is not under the transcript:\n%s", f)
	}
	h.keys("k")
	contains(t, h.frame(), "#10", "from         carol@mac-3")
	h.keys("2")
	contains(t, h.frame(), "coop · build-42 · threads", "from         carol@mac-3")
	h.keys("1", "enter")
	if strings.Contains(h.frame(), "from         carol@mac-3") {
		t.Error("enter did not close the preview")
	}
	h.keys("enter", "esc")
	if strings.Contains(h.frame(), "from         carol@mac-3") {
		t.Error("esc did not close the preview")
	}
}

func TestKeysThatArriveTogetherActOnTheLatestState(t *testing.T) {
	h := start(t, nil)
	h.keys("tab", "down", "enter", "tab", "down", "down", "enter")
	contains(t, h.frame(), "alice@mac-1  ● online")
}
