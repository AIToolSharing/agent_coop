package view_test

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/model"
	"github.com/AIToolSharing/agent_coop/internal/model/modeltest"
	"github.com/AIToolSharing/agent_coop/internal/tui/view"
	"github.com/AIToolSharing/agent_coop/internal/wire"
	"pgregory.net/rapid"
)

func store() *model.Store {
	s := model.New()
	for _, u := range modeltest.Fixture() {
		s.Apply(u)
	}
	return s
}

func opts(width int) view.Options {
	return view.Options{Width: width, Now: modeltest.Now, Loc: time.UTC, System: true}
}

// render gives every line of a buffer, as a window of any height would.
func render(b view.Buffer, o view.Options) view.Rendered {
	p := &view.Pane{Buf: b, O: o}
	r, _ := p.Window(view.Anchor{}, 1<<20)
	return r
}

func texts(r view.Rendered) []string {
	out := make([]string, len(r.Lines))
	for i, l := range r.Lines {
		out[i] = view.Plain(l)
	}
	return out
}

func checkShape(t *testing.T, r view.Rendered, width int) {
	t.Helper()
	if len(r.Lines) != len(r.IDs) {
		t.Fatalf("%d lines, %d ids", len(r.Lines), len(r.IDs))
	}
	for i, l := range r.Lines {
		p := view.Plain(l)
		if w := view.Width(p); w != width {
			t.Errorf("line %d has width %d, want %d: %q", i, w, width, p)
		}
		if strings.ContainsAny(p, "\n\t") {
			t.Errorf("line %d has a control character: %q", i, p)
		}
	}
}

func TestTranscriptShowsWholeMessagesAndFoldsSystemLines(t *testing.T) {
	v := store().View(modeltest.SID)
	for _, width := range []int{110, 60, 24} {
		var tr view.Transcript
		o := opts(width)
		r := render(tr.Render(v, o), o)
		checkShape(t, r, width)
		all := normalize(texts(r))
		// Every message body is there whole, however narrow the pane.
		for _, m := range v.Messages() {
			body := normalize(view.Wrap(m.Text, width-2))
			if !strings.Contains(all, body) {
				t.Errorf("width %d: message #%s is not shown whole:\n%s", width, m.ID, strings.Join(texts(r), "\n"))
			}
		}
		// Header lines carry the ids, in sequence order; nothing else does.
		var ids []string
		for _, id := range r.IDs {
			if id != "" {
				ids = append(ids, id)
			}
		}
		if fmt.Sprint(ids) != "[4 5 8 10 12 13 14]" {
			t.Errorf("width %d: ids %v", width, ids)
		}
	}
	var tr view.Transcript
	lines := texts(render(tr.Render(v, opts(110)), opts(110)))
	if !strings.HasPrefix(lines[0], "── 2026-09-30 ──") {
		t.Errorf("no day separator: %q", lines[0])
	}
	if !strings.Contains(lines[1], "12:00:00  · alice@mac-1 joined from mac-1 (claude-code 2.1) · bob@vps-2 joined") {
		t.Errorf("joins are not folded into one line: %q", lines[1])
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"12:00:10  carol@mac-3 → all", "#4",
		"  ↩ #5 alice@mac-1: What is the shape of GET /users?",
		"  I renamed the users fixture",
		"12:00:50  alice@mac-1 → all",
		"12:00:55  bob@vps-2 → alice@mac-1",
		"↳1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
}

func TestTranscriptFilters(t *testing.T) {
	v := store().View(modeltest.SID)
	o := opts(100)
	o.Agent = "carol@mac-3"
	var tr view.Transcript
	ids := strings.Join(nonEmpty(render(tr.Render(v, o), o).IDs), " ")
	if ids != "4 10 12 14" {
		t.Errorf("carol's messages: %s", ids)
	}
	o = opts(100)
	o.System = false
	var tr3 view.Transcript
	for _, line := range texts(render(tr3.Render(v, o), o)) {
		if strings.Contains(line, " · ") {
			t.Errorf("system line shown with System off: %q", line)
		}
	}
}

// normalize joins lines into one string with single spaces, without the padding of Fit.
func normalize(lines []string) string {
	parts := make([]string, 0, len(lines))
	for _, l := range lines {
		parts = append(parts, strings.TrimSpace(l))
	}
	return strings.Join(parts, " ")
}

func nonEmpty(ids []string) []string {
	var out []string
	for _, id := range ids {
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

// The cache extends its blocks per new item; the result must equal a build from scratch.
func TestTranscriptCacheEqualsAFreshRender(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		updates := modeltest.Fixture()
		// Some extra traffic after the fixture, in a random order of arrival.
		extra := extraTraffic(rapid.IntRange(0, 12).Draw(rt, "extra"), rapid.Int64().Draw(rt, "seed"))
		s := model.New()
		for _, u := range updates {
			s.Apply(u)
		}
		v := s.View(modeltest.SID)
		o := opts(rapid.SampledFrom([]int{40, 80, 120}).Draw(rt, "width"))
		o.System = rapid.Bool().Draw(rt, "system")
		var cached view.Transcript
		cached.Render(v, o)
		for _, u := range extra {
			s.Apply(u)
			if rapid.Bool().Draw(rt, "repaint") {
				cached.Render(v, o)
			}
		}
		got := texts(render(cached.Render(v, o), o))
		var fresh view.Transcript
		want := texts(render(fresh.Render(v, o), o))
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			rt.Fatalf("cached render differs\n got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	})
}

func extraTraffic(n int, seed int64) []model.Update {
	r := rand.New(rand.NewSource(seed))
	var out []model.Update
	seq := int64(100)
	agents := []wire.Address{modeltest.Alice, modeltest.Bob, modeltest.Carol}
	for i := 0; i < n; i++ {
		seq++
		from := agents[r.Intn(3)]
		at := modeltest.At(float64(80 + i*7))
		var e wire.Event
		switch r.Intn(4) {
		case 0:
			e = wire.Event{Kind: wire.EventActivity, Seq: seq, SID: modeltest.SID, From: from.String(), Activity: &wire.Activity{Kind: "state", State: "working", Note: fmt.Sprint("step ", i), At: at}}
		case 1:
			e = wire.Event{Kind: wire.EventMsg, Seq: seq, SID: modeltest.SID, From: from.String(), To: "all", Text: strings.Repeat("word ", r.Intn(40)+1), ReplyTo: "4", SentAt: at}
		case 2:
			e = wire.Event{Kind: wire.EventMsg, Seq: seq, SID: modeltest.SID, From: from.String(), To: "all", Text: "re", ReplyTo: "5", SentAt: at}
		default:
			e = wire.Event{Kind: wire.EventMsg, Seq: seq, SID: modeltest.SID, From: from.String(), To: modeltest.Carol.String(), Text: fmt.Sprint("note ", i, " ", strings.Repeat("x", r.Intn(90))), SentAt: at}
		}
		out = append(out, model.Update{Event: &e})
	}
	r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// Found in use: three agents of a test run each joined for some seconds, and their "left" rows
// stayed in the sidebar and in the session count for ever (smoke-researcher, smoke2, eng-t1).
// Later: agents that left stayed as "ghosts". An agent that left goes from the list at once.
func TestAnAgentThatLeftGoesFromTheSidebar(t *testing.T) {
	s := store()
	now := modeltest.Now
	iso := func(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z07:00") }
	act := func(seq int64, a wire.Activity) {
		s.Apply(model.Update{Event: &wire.Event{Kind: wire.EventActivity, Seq: seq, SID: "build-42", From: "bob@vps-2", Activity: &a}})
	}
	sidebar := func() string {
		v := s.View("build-42")
		sb := view.RenderSidebar(view.Summaries(s, now), view.Listed(v), view.Selection{SID: "build-42", Cursor: -1}, 60, now)
		return strings.Join(texts(sb.Rendered), "\n")
	}
	online := func() int {
		for _, sum := range view.Summaries(s, now) {
			if sum.SID == "build-42" {
				return sum.Online
			}
		}
		return -1
	}
	// bob leaves.
	s.Apply(model.Update{Presence: &wire.PresenceUpdate{Key: "build-42.vps-2.bob", Revision: 1000}})
	act(500, wire.Activity{Kind: "left", Reason: "disconnected", At: iso(now.Add(-time.Second))})
	if got := sidebar(); strings.Contains(got, "bob@vps-2") || online() != 2 {
		t.Fatalf("after the leave: online %d, sidebar:\n%s", online(), got)
	}
	// The agent stays known, so that the history and a message to him find him.
	if s.View("build-42").Agents["bob@vps-2"] == nil {
		t.Fatal("AgentList lost the agent that left")
	}
	// bob joins again: he is listed again.
	act(502, wire.Activity{Kind: "joined", Host: "vps-2", Cwd: "/srv/api", Client: wire.Client{Name: "codex", Version: "0.9"}, At: iso(now)})
	s.Apply(model.Update{Presence: &wire.PresenceUpdate{Key: "build-42.vps-2.bob", Revision: 1001, Record: &wire.PresenceRecord{Host: "vps-2", Cwd: "/srv/api", State: "idle", JoinedAt: iso(now)}}})
	if got := sidebar(); !strings.Contains(got, "bob@vps-2") || online() != 3 {
		t.Fatalf("after the new join: online %d, sidebar:\n%s", online(), got)
	}
}

func TestSidebarListsSessionsThenAgents(t *testing.T) {
	s := store()
	sums := view.Summaries(s, modeltest.Now)
	if len(sums) != 3 || sums[0].SID != model.AllSessions || sums[1].SID != "build-42" || sums[2].SID != "docs" {
		t.Fatalf("summaries %+v", sums)
	}
	if sums[1].Online != 3 || sums[1].OpenAsks != 1 || sums[2].Online != 0 {
		t.Fatalf("build-42 summary %+v", sums[1])
	}
	v := s.View("build-42")
	width := view.SidebarWidth(sums, v.AgentList(), 36, modeltest.Now)
	sb := view.RenderSidebar(sums, v.AgentList(), view.Selection{SID: "build-42", Cursor: 4}, width-1, modeltest.Now)
	checkShape(t, sb.Rendered, width-1)
	lines := texts(sb.Rendered)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"SESSIONS", "all traffic", "● build-42 3 1 ask", "○ docs 0", "AGENTS · build-42", "● alice", "working", "● bob", "blocked", "● carol", "⏳"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	if sb.Rows[1] == nil || sb.Rows[1].SID != model.AllSessions || sb.Rows[2].SID != "build-42" || sb.Rows[3].SID != "docs" {
		t.Errorf("rows %+v", sb.Rows)
	}
	agentRows := 0
	for _, r := range sb.Rows {
		if r != nil && r.Address != "" {
			agentRows++
		}
	}
	if agentRows != 3 {
		t.Errorf("%d agent rows", agentRows)
	}
}

func TestThreadsShowWholeMessagesByDepth(t *testing.T) {
	v := store().View("build-42")
	var th view.Threads
	r := render(th.Render(v, opts(50)), opts(50))
	checkShape(t, r, 50)
	lines := texts(r)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"OPEN ASKS (1)", "carol@mac-3 → bob@vps-2", "Can I change the users table?", "THREADS", "└─ #8 bob@vps-2 → alice@mac-1", "{ id: number, name: string, email: string }", "└─ #13 bob@vps-2 → alice@mac-1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	if ids := strings.Join(nonEmpty(r.IDs), " "); ids != "14 4 5 8 10 12 13 14" {
		t.Errorf("ids %s", ids)
	}
}

func TestAgentAndMessageDetails(t *testing.T) {
	v := store().View("build-42")
	var av view.AgentView
	a := render(av.Render(v, "bob@vps-2", opts(70)), opts(70))
	checkShape(t, a, 70)
	joined := strings.Join(texts(a), "\n")
	for _, want := range []string{"bob@vps-2  ● online", "state        blocked", "note         waiting for CI", "host         vps-2", "client       codex 0.9", "sent 2", "TIMELINE", "sent #8 → alice@mac-1", "got #4 ← carol@mac-3", "is blocked: waiting for CI"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "REACHED") || strings.Contains(joined, "reach time") {
		t.Error("delivery data was dropped from the design")
	}
	m := render(view.RenderMessage(v, "12", opts(70)), opts(70))
	checkShape(t, m, 70)
	joined = strings.Join(texts(m), "\n")
	for _, want := range []string{"#12", "from         alice@mac-1", "to           all", "sent         2026-09-30 12:00:50 (4m ago)", "Please run the tests before you say done", "REPLIES (1)", "#13 bob@vps-2", "Will do; CI is running"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	if texts(render(view.RenderMessage(v, "nope", opts(40)), opts(40)))[0] == "" {
		t.Error("an unknown id needs a note")
	}
}

func TestWrapAndFit(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		width := rapid.IntRange(1, 80).Draw(rt, "width")
		text := rapid.StringMatching(`^[a-zA-Z0-9 .,;'"()\-_/]{0,300}$`).Draw(rt, "text")
		for _, line := range view.Wrap(text, width) {
			if view.Width(line) > width {
				rt.Fatalf("wrapped line %q is wider than %d", line, width)
			}
		}
		l := view.Fit(view.Line{view.S(text), view.Bold("x")}, width)
		if w := view.Width(view.Plain(l)); w != width {
			rt.Fatalf("fit gave width %d, want %d: %q", w, width, view.Plain(l))
		}
	})
	if view.Age(modeltest.At(0), modeltest.Now) != "5m" || view.Age(modeltest.At(290), modeltest.Now) != "10s" {
		t.Error("age")
	}
	if view.Clock(modeltest.At(20), time.UTC) != "12:00:20" {
		t.Error("clock")
	}
}
