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
		r := tr.Render(v, opts(width))
		checkShape(t, r, width)
		all := normalize(texts(r))
		// Every message body is there whole, however narrow the pane.
		for _, m := range v.Messages() {
			if m.Redacted {
				continue
			}
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
	lines := texts(tr.Render(v, opts(110)))
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
		"  [withdrawn]",
		"12:00:50  operator → all",
		"12:00:55  bob@vps-2 → operator  for you",
		"↳1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "hunter2") {
		t.Error("a withdrawn message shows its text")
	}
}

func TestTranscriptFilters(t *testing.T) {
	v := store().View(modeltest.SID)
	o := opts(100)
	o.Agent = "carol@mac-3"
	var tr view.Transcript
	ids := strings.Join(nonEmpty(tr.Render(v, o).IDs), " ")
	if ids != "4 10 12 14" {
		t.Errorf("carol's messages: %s", ids)
	}
	o = opts(100)
	o.Search = "users"
	var tr2 view.Transcript
	if ids := strings.Join(nonEmpty(tr2.Render(v, o).IDs), " "); ids != "4 5 14" {
		t.Errorf("search: %s", ids)
	}
	o = opts(100)
	o.System = false
	var tr3 view.Transcript
	for _, line := range texts(tr3.Render(v, o)) {
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

// The cache extends its output per new item; the result must equal a render from scratch.
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
		got := texts(cached.Render(v, o))
		var fresh view.Transcript
		want := texts(fresh.Render(v, o))
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
			e = wire.Event{Kind: wire.EventRedact, Seq: seq, SID: modeltest.SID, ID: "4", At: at}
		default:
			e = wire.Event{Kind: wire.EventMsg, Seq: seq, SID: modeltest.SID, From: from.String(), To: "operator", Text: fmt.Sprint("note ", i, " ", strings.Repeat("x", r.Intn(90))), SentAt: at}
		}
		out = append(out, model.Update{Event: &e})
	}
	r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// Found in use: agents the operator removed stayed in the sidebar and in the session count
// for ever, marked "removed". Asked for after it: a removed agent that tried to join in the
// last five minutes is listed, so that the operator sees that it waits for :allow.
func TestRemovedAgentsLeaveTheSidebarUnlessTheyTriedToJoin(t *testing.T) {
	s := store()
	// bob goes offline, then the operator removes him.
	s.Apply(model.Update{Presence: &wire.PresenceUpdate{Key: "build-42.vps-2.bob", Revision: 1000}})
	s.Apply(model.Update{Kick: &wire.KickUpdate{Key: "build-42.kick.vps-2.bob", Revision: 1001, Record: &wire.KickRecord{At: modeltest.At(100)}}})
	now := modeltest.Now
	sidebar := func(at time.Time) string {
		v := s.View("build-42")
		sb := view.RenderSidebar(view.Summaries(s, at), view.Listed(v, at), view.Selection{SID: "build-42", Cursor: -1}, 60, at)
		return strings.Join(texts(sb.Rendered), "\n")
	}
	counts := func(at time.Time) string {
		for _, sum := range view.Summaries(s, at) {
			if sum.SID == "build-42" {
				return fmt.Sprintf("%d/%d", sum.Online, sum.Agents)
			}
		}
		return "?"
	}
	if got := sidebar(now); strings.Contains(got, "bob@vps-2") || !strings.Contains(got, "alice@mac-1") || counts(now) != "2/2" {
		t.Fatalf("after the removal: counts %s, sidebar:\n%s", counts(now), got)
	}
	// The removed agent stays known, so that :allow finds it.
	known := false
	for _, a := range s.View("build-42").AgentList() {
		known = known || (a.Address == "bob@vps-2" && a.Kicked)
	}
	if !known {
		t.Fatal("AgentList lost the removed agent")
	}
	// bob tries to join two minutes before now: the hub refuses him and records it.
	tried := now.Add(-2 * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z07:00")
	s.Apply(model.Update{Event: &wire.Event{Kind: wire.EventActivity, Seq: 500, SID: "build-42", From: "bob@vps-2", Activity: &wire.Activity{Kind: "refused", Reason: "removed", At: tried}}})
	if got := sidebar(now); !strings.Contains(got, "bob@vps-2 refused 2m ago") || counts(now) != "2/3" {
		t.Fatalf("after the refused join: counts %s, sidebar:\n%s", counts(now), got)
	}
	// The agent's details show the try as a line of its timeline.
	if lines := strings.Join(texts(view.RenderAgent(s.View("build-42"), "bob@vps-2", opts(110))), "\n"); !strings.Contains(lines, "tried to join; it is removed (:allow lets it back)") {
		t.Fatalf("the agent's timeline has no line for the refused join:\n%s", lines)
	}
	// Five minutes after the try the row goes again.
	later := now.Add(4 * time.Minute)
	if got := sidebar(later); strings.Contains(got, "bob@vps-2") || counts(later) != "2/2" {
		t.Fatalf("six minutes after the try: counts %s, sidebar:\n%s", counts(later), got)
	}
}

// Asked for by the operator: a second session that asks for a name in use shows under the agent
// that holds the name, for five minutes after its last try.
func TestADuplicateSessionShowsUnderTheAgentThatHoldsTheName(t *testing.T) {
	s := store()
	now := modeltest.Now
	sidebar := func(at time.Time) []string {
		v := s.View("build-42")
		return texts(view.RenderSidebar(view.Summaries(s, at), view.Listed(v, at), view.Selection{SID: "build-42", Cursor: -1}, 60, at).Rendered)
	}
	under := func(lines []string, name string) string {
		for i, l := range lines {
			if strings.Contains(l, name) && i+1 < len(lines) {
				return strings.TrimSpace(lines[i+1])
			}
		}
		return ""
	}
	if got := strings.Join(sidebar(now), "\n"); strings.Contains(got, "duplicate") {
		t.Fatalf("a duplicate line before any try:\n%s", got)
	}
	iso := func(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z07:00") }
	tried := now.Add(-2 * time.Minute)
	s.Apply(model.Update{Event: &wire.Event{Kind: wire.EventActivity, Seq: 500, SID: "build-42", From: "alice@mac-1", Activity: &wire.Activity{Kind: "refused", Reason: "taken", At: iso(tried)}}})
	lines := sidebar(now)
	if got := under(lines, "alice@mac-1"); got != "↳ duplicate refused 2m ago" {
		t.Fatalf("under alice: %q in\n%s", got, strings.Join(lines, "\n"))
	}
	for _, sum := range view.Summaries(s, now) {
		if sum.SID == "build-42" && (sum.Online != 3 || sum.Agents != 3) {
			t.Fatalf("counts %d/%d, want 3/3: a duplicate is no agent of its own", sum.Online, sum.Agents)
		}
	}
	if details := strings.Join(texts(view.RenderAgent(s.View("build-42"), "alice@mac-1", opts(110))), "\n"); !strings.Contains(details, "is in use: a second session tried to join with this name") {
		t.Fatalf("the agent's timeline has no line for the duplicate:\n%s", details)
	}
	// Five minutes after the last try the line goes.
	if got := strings.Join(sidebar(now.Add(4*time.Minute)), "\n"); strings.Contains(got, "duplicate") {
		t.Fatalf("six minutes after the try:\n%s", got)
	}
	// When a session joins under the name after the try (the waiting one got in), the line
	// goes at once.
	s.Apply(model.Update{Event: &wire.Event{Kind: wire.EventActivity, Seq: 501, SID: "build-42", From: "alice@mac-1", Activity: &wire.Activity{Kind: "joined", Host: "mac-1", Cwd: "/src/app", Client: wire.Client{Name: "claude-code", Version: "2.1"}, At: iso(tried.Add(time.Minute))}}})
	if got := strings.Join(sidebar(now), "\n"); strings.Contains(got, "duplicate") {
		t.Fatalf("after a join under the name:\n%s", got)
	}
}

func TestSidebarListsSessionsThenAgents(t *testing.T) {
	s := store()
	sums := view.Summaries(s, modeltest.Now)
	if len(sums) != 3 || sums[0].SID != model.AllSessions || sums[1].SID != "build-42" || sums[2].SID != "docs" {
		t.Fatalf("summaries %+v", sums)
	}
	if sums[1].Online != 3 || sums[1].Agents != 3 || sums[1].OpenAsks != 1 || sums[2].Status != "closed" {
		t.Fatalf("build-42 summary %+v", sums[1])
	}
	v := s.View("build-42")
	width := view.SidebarWidth(sums, v.AgentList(), 36, modeltest.Now)
	sb := view.RenderSidebar(sums, v.AgentList(), view.Selection{SID: "build-42", Cursor: 4}, width-1, modeltest.Now)
	checkShape(t, sb.Rendered, width-1)
	lines := texts(sb.Rendered)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"SESSIONS", "all traffic", "● build-42 3/3 1 ask", "✕ docs", "closed", "AGENTS · build-42", "● alice", "working", "● bob", "blocked", "● carol", "⏳"} {
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

func TestAttention(t *testing.T) {
	v := store().View("build-42")
	items := view.Attention(v, modeltest.Now)
	var kinds []string
	for _, it := range items {
		kinds = append(kinds, it.Kind+":"+it.ID+it.Address)
	}
	if fmt.Sprint(kinds) != "[for_you:13 ask:14 blocked:bob@vps-2]" {
		t.Fatalf("items %v", kinds)
	}
	line := view.Plain(view.RenderAttention(items, modeltest.Now))
	if !strings.Contains(line, "1 for you") || !strings.Contains(line, "1 ask waiting 4m") || !strings.Contains(line, "1 blocked") {
		t.Errorf("attention line %q", line)
	}
	if view.RenderAttention(nil, modeltest.Now) != nil {
		t.Error("nothing to say should give no line")
	}
	if d := view.Describe(items[1]); d != "carol@mac-3 waits for bob@vps-2 (ask #14)" {
		t.Errorf("describe %q", d)
	}
}

func TestThreadsShowWholeMessagesByDepth(t *testing.T) {
	v := store().View("build-42")
	r := view.RenderThreads(v, opts(50))
	checkShape(t, r, 50)
	lines := texts(r)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"OPEN ASKS (1)", "carol@mac-3 → bob@vps-2", "Can I change the users table?", "THREADS", "└─ #8 bob@vps-2 → alice@mac-1", "{ id: number, name: string, email: string }", "└─ #13 bob@vps-2 → operator"} {
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
	a := view.RenderAgent(v, "bob@vps-2", opts(70))
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
	m := view.RenderMessage(v, "12", opts(70))
	checkShape(t, m, 70)
	joined = strings.Join(texts(m), "\n")
	for _, want := range []string{"#12", "from         operator", "to           all", "sent         2026-09-30 12:00:50 (4m ago)", "Please run the tests before you say done", "REPLIES (1)", "#13 bob@vps-2", "Will do; CI is running"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	if texts(view.RenderMessage(v, "nope", opts(40)))[0] == "" {
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
