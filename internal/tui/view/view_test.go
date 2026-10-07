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

// Found in use: three agents of a test run each joined for some seconds, and their "left" rows
// stayed in the sidebar and in the session count for ever (smoke-researcher, smoke2, eng-t1).
// An agent that left stays for five minutes. An agent that the operator forgot goes at once.
func TestAgentsThatLeftGoFromTheSidebar(t *testing.T) {
	s := store()
	now := modeltest.Now
	iso := func(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z07:00") }
	act := func(seq int64, a wire.Activity) {
		s.Apply(model.Update{Event: &wire.Event{Kind: wire.EventActivity, Seq: seq, SID: "build-42", From: "bob@vps-2", Activity: &a}})
	}
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
	listed := func(at time.Time) bool { return strings.Contains(sidebar(at), "bob@vps-2") }

	// bob leaves one minute before now.
	s.Apply(model.Update{Presence: &wire.PresenceUpdate{Key: "build-42.vps-2.bob", Revision: 1000}})
	act(500, wire.Activity{Kind: "left", Reason: "disconnected", At: iso(now.Add(-time.Minute))})
	if got := sidebar(now); !strings.Contains(got, "bob@vps-2 left") || counts(now) != "2/3" {
		t.Fatalf("one minute after the leave: counts %s, sidebar:\n%s", counts(now), got)
	}
	if at := now.Add(4*time.Minute - time.Second); !listed(at) {
		t.Fatalf("one second before five minutes after the leave, bob is not listed:\n%s", sidebar(at))
	}
	late := now.Add(4 * time.Minute)
	if listed(late) || counts(late) != "2/2" {
		t.Fatalf("five minutes after the leave: counts %s, sidebar:\n%s", counts(late), sidebar(late))
	}
	// The agent stays known, so that :forget and the history find it.
	if s.View("build-42").Agents["bob@vps-2"] == nil {
		t.Fatal("AgentList lost the agent that left")
	}

	// The operator forgets bob: he goes at once, also inside the five minutes.
	act(501, wire.Activity{Kind: "forgotten", At: iso(now)})
	if listed(now) || counts(now) != "2/2" {
		t.Fatalf("after the forget: counts %s, sidebar:\n%s", counts(now), sidebar(now))
	}

	// bob joins again: he is listed again.
	act(502, wire.Activity{Kind: "joined", Host: "vps-2", Cwd: "/srv/api", Client: wire.Client{Name: "codex", Version: "0.9"}, At: iso(now)})
	s.Apply(model.Update{Presence: &wire.PresenceUpdate{Key: "build-42.vps-2.bob", Revision: 1001, Record: &wire.PresenceRecord{Host: "vps-2", Cwd: "/srv/api", State: "idle", JoinedAt: iso(now)}}})
	if !listed(late) || counts(late) != "3/3" {
		t.Fatalf("after the new join: counts %s, sidebar:\n%s", counts(late), sidebar(late))
	}
}

// A held or paused agent shows its gate in place of its own state. It does not need the
// operator. An agent with no gate hook shows "soft": the gate is only advice to it. The name
// stays whole.
func TestTheSidebarShowsTheGate(t *testing.T) {
	s := store()
	now := modeltest.Now
	gate := func(seq int64, from, g string) {
		s.Apply(model.Update{Event: &wire.Event{Kind: wire.EventActivity, Seq: seq, SID: "build-42", From: from, Activity: &wire.Activity{Kind: "gate", Gate: g, At: modeltest.At(80)}}})
	}
	gate(500, "bob@vps-2", "held")
	gate(501, "carol@mac-3", "paused")
	// bob was started with the gate; carol was not.
	s.Apply(model.Update{Presence: &wire.PresenceUpdate{Key: "build-42.vps-2.bob", Revision: 1000, Record: &wire.PresenceRecord{Host: "vps-2", Cwd: "/srv/api", State: "working", JoinedAt: modeltest.At(2), Gated: true}}})
	v := s.View("build-42")
	agents := view.Listed(v, now)
	sums := view.Summaries(s, now)
	width := view.SidebarWidth(sums, agents, 40, now)
	sb := view.RenderSidebar(sums, agents, view.Selection{SID: "build-42", Cursor: -1}, width, now)
	got := strings.Join(texts(sb.Rendered), "\n")
	for _, want := range []string{"● alice@mac-1 working", "● bob@vps-2 held", "● carol@mac-3 paused (soft) ⏳"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "bob@vps-2 held (soft)") {
		t.Errorf("a gated agent shows as soft:\n%s", got)
	}
	// A held or paused agent does not need the operator.
	for _, it := range view.Attention(v, now) {
		if it.Address == "bob@vps-2" || it.Address == "carol@mac-3" {
			t.Errorf("a held or paused agent needs the operator: %+v", it)
		}
	}
	// The details say what the gate means, and that carol has no gate hook.
	if lines := strings.Join(texts(view.RenderAgent(v, "bob@vps-2", opts(110))), "\n"); !strings.Contains(lines, "held: it waits for the orchestrator's task (p lets it go)") || strings.Contains(lines, "no gate:") {
		t.Errorf("details of bob:\n%s", lines)
	}
	if lines := strings.Join(texts(view.RenderAgent(v, "carol@mac-3", opts(110))), "\n"); !strings.Contains(lines, "paused: it does no work until you resume it (p)") || !strings.Contains(lines, "no gate: this agent was not started with coop claude") {
		t.Errorf("details of carol:\n%s", lines)
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

// traced gives the fixture with a trace: Alice said what she does, ran the tests (the call
// ended), changed two files and runs a build now; Bob changed one of the same files and one
// of his calls failed; a person typed a prompt at Bob's terminal.
func traced() *model.Store {
	s := store()
	key := func(a wire.Address) string {
		return wire.BuildPresenceKey(wire.PresenceKey{SID: modeltest.SID, Agent: a})
	}
	words := "I will run the tests first, and then I will change the parser so that it reads the new header."
	s.Apply(model.Update{Trace: &wire.TraceUpdate{Boot: 1, Key: key(modeltest.Alice), Branch: "fix-parser", Items: []wire.TraceItem{
		// The hook reads the words after the tool call: they have the number 3 and stand
		// before the call.
		{N: 1, At: modeltest.At(201), Kind: wire.TraceToolStart, ID: "t1", Tool: "Bash", Text: "go test ./..."},
		{N: 2, At: modeltest.At(215), Kind: wire.TraceToolEnd, ID: "t1", Tool: "Bash", Text: "go test ./...", MS: 14200},
		{N: 3, At: modeltest.At(215), Kind: wire.TraceSay, ID: "u1", Text: words, Before: "t1"},
		{N: 6, At: modeltest.At(230), Kind: wire.TraceToolEnd, ID: "t2", Tool: "Edit", Text: "src/parser.go", MS: 40, File: "src/parser.go"},
		{N: 7, At: modeltest.At(286), Kind: wire.TraceToolStart, ID: "t3", Tool: "Bash", Text: "make build"},
	}, Files: []wire.TraceFile{{Path: "src/parser.go", Count: 2, At: modeltest.At(230)}, {Path: "README.md", Count: 1, At: modeltest.At(220)}}}})
	s.Apply(model.Update{Trace: &wire.TraceUpdate{Boot: 1, Key: key(modeltest.Bob), Items: []wire.TraceItem{
		{N: 4, At: modeltest.At(220), Kind: wire.TracePrompt, Text: "also fix the header"},
		{N: 5, At: modeltest.At(225), Kind: wire.TraceToolStart, ID: "t1", Tool: "Edit", Text: "src/parser.go"},
		{N: 8, At: modeltest.At(290), Kind: wire.TraceToolEnd, ID: "t1", Tool: "Edit", Text: "src/parser.go", MS: 65000, Failed: true},
		{N: 9, At: modeltest.At(291), Kind: wire.TraceToolStart, ID: "t9", Tool: "Read", Text: "go.mod"},
		{N: 10, At: modeltest.At(292), Kind: wire.TraceSay, Text: "Done.", Final: true},
	}, Files: []wire.TraceFile{{Path: "src/parser.go", Count: 1, At: modeltest.At(100)}}}})
	return s
}

// The operator's complaint: the TUI does not show what the agents do. The activity view
// shows each tool call one time with its result, and the words of an agent whole.
func TestActivityShowsToolCallsWithTheirResultAndWholeWords(t *testing.T) {
	v := traced().View(modeltest.SID)
	for _, width := range []int{110, 50} {
		r := view.RenderActivity(v, opts(width))
		checkShape(t, r, width)
		lines := texts(r)
		joined := strings.Join(lines, "\n")
		all := normalize(lines)
		for _, word := range strings.Fields("I will run the tests first, and then I will change the parser so that it reads the new header.") {
			if !strings.Contains(all, word) {
				t.Errorf("width %d: the word %q of the agent is not shown:\n%s", width, word, joined)
			}
		}
		if n := strings.Count(joined, "go test ./..."); n != 1 {
			t.Errorf("width %d: the tool call shows %d times, want 1:\n%s", width, n, joined)
		}
	}
	lines := texts(view.RenderActivity(v, opts(110)))
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"12:03:21  alice@mac-1  ✓ Bash: go test ./... 14s",  // ended: how long it ran
		"12:03:50  alice@mac-1  ✓ Edit: src/parser.go 40ms", // an end with no start in the trace
		"12:04:46  alice@mac-1  ▸ Bash: make build 14s …",   // runs now
		"12:03:40  bob@vps-2    » also fix the header",      // a prompt of a person
		"12:03:45  bob@vps-2    ✗ Edit: src/parser.go 1m5s", // failed
		"12:04:51  bob@vps-2    · Read: go.mod no result",   // the turn ended with no result of the call
		"12:04:52  bob@vps-2      Done.",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	// Oldest first, over all agents. The words that stand before a tool call come before it,
	// with its time.
	if !strings.Contains(joined, "12:03:21  alice@mac-1    I will run") || !(strings.Index(joined, "I will run") < strings.Index(joined, "go test") &&
		strings.Index(joined, "go test") < strings.Index(joined, "also fix") && strings.Index(joined, "also fix") < strings.Index(joined, "make build")) {
		t.Errorf("the items are not in the order of their numbers:\n%s", joined)
	}

	o := opts(110)
	o.Agent = "bob@vps-2"
	if joined := strings.Join(texts(view.RenderActivity(v, o)), "\n"); strings.Contains(joined, "alice") || !strings.Contains(joined, "also fix") {
		t.Errorf("the agent filter should leave only bob:\n%s", joined)
	}
	o = opts(110)
	o.Search = "MAKE"
	if lines := texts(view.RenderActivity(v, o)); len(lines) != 1 || !strings.Contains(lines[0], "make build") {
		t.Errorf("the search should leave one line: %q", lines)
	}
	if lines := texts(view.RenderActivity(store().View(modeltest.SID), opts(60))); len(lines) != 1 || !strings.Contains(lines[0], "no activity yet") {
		t.Errorf("no trace needs a note: %q", lines)
	}
	// In the view of all sessions, the session comes with the name.
	if joined := strings.Join(texts(view.RenderActivity(traced().View(model.AllSessions), opts(110))), "\n"); !strings.Contains(joined, "build-42/alice@mac-1") {
		t.Errorf("the view of all sessions should name the session:\n%s", joined)
	}
}

func TestTheSidebarShowsWhatAnAgentRunsNow(t *testing.T) {
	s := traced()
	v := s.View(modeltest.SID)
	sb := view.RenderSidebar(view.Summaries(s, modeltest.Now), v.AgentList(), view.Selection{SID: modeltest.SID, Cursor: -1}, 40, modeltest.Now)
	checkShape(t, sb.Rendered, 40)
	lines := texts(sb.Rendered)
	for i, l := range lines {
		if strings.Contains(l, "● alice@mac-1") {
			if !strings.Contains(lines[i+1], "▸ 14s Bash: make build") || sb.Rows[i+1] != nil {
				t.Fatalf("the line under alice is %q with row %v, want her running call and no row", lines[i+1], sb.Rows[i+1])
			}
		}
		// Bob's turn ended: his call with no result does not run.
		if strings.Contains(l, "go.mod") {
			t.Fatalf("the sidebar shows a call that does not run: %q", l)
		}
	}
	if !strings.Contains(strings.Join(lines, "\n"), "make build") {
		t.Fatalf("the sidebar does not show the running call:\n%s", strings.Join(lines, "\n"))
	}
}

func TestAgentDetailsShowTheTrace(t *testing.T) {
	v := traced().View(modeltest.SID)
	r := view.RenderAgent(v, "alice@mac-1", opts(90))
	checkShape(t, r, 90)
	joined := strings.Join(texts(r), "\n")
	for _, want := range []string{
		"branch       fix-parser",
		"now          Bash: make build for 14s",
		"CHANGED FILES (2)",
		"src/parser.go  ×2  1m ago  also bob@vps-2",
		"README.md  ×1  1m ago",
		"ACTIVITY",
		"12:03:21  ✓ Bash: go test ./... 14s", // no name: the details are of one agent
		"TIMELINE",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "README.md  ×1  1m ago  also") {
		t.Errorf("only alice changed README.md:\n%s", joined)
	}
	// Carol has no trace and no gate hook: the details say why there is no activity.
	if joined := strings.Join(texts(view.RenderAgent(v, "carol@mac-3", opts(90))), "\n"); !strings.Contains(joined, "activity     none: this agent was not started with coop claude") || strings.Contains(joined, "CHANGED FILES") {
		t.Errorf("carol's details:\n%s", joined)
	}
}

// A repaint draws the whole activity again. With the most trace that the model keeps for
// ten agents, that must stay far below the time of one frame.
func TestActivityRendersTheLargestTraceFast(t *testing.T) {
	s := store()
	var n int64
	for a := range 10 {
		u := wire.TraceUpdate{Boot: 1, Key: fmt.Sprintf("%s.mac-1.agent%d", modeltest.SID, a)}
		for range model.TraceMax {
			n++
			u.Items = append(u.Items, wire.TraceItem{N: n, At: modeltest.At(float64(n) / 100), Kind: wire.TraceSay, Text: strings.Repeat("some words of the agent ", 8)})
		}
		s.Apply(model.Update{Trace: &u})
	}
	v := s.View(modeltest.SID)
	start := time.Now()
	r := view.RenderActivity(v, opts(100))
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("one render of %d items took %s", n, took)
	} else {
		t.Logf("%d items, %d lines: %s", n, len(r.Lines), took)
	}
}

// The operator must see which agent may act for them.
func TestAnOrchestratorHasAMark(t *testing.T) {
	s := store()
	s.Apply(model.Update{Presence: &wire.PresenceUpdate{Key: "build-42.mac-1.alice", Revision: 1000, Record: &wire.PresenceRecord{Host: "mac-1", Cwd: "/src/app", State: "working", JoinedAt: modeltest.At(0), Gated: true, Role: wire.RoleOrchestrator}}})
	v := s.View("build-42")
	sb := view.RenderSidebar(view.Summaries(s, modeltest.Now), view.Listed(v, modeltest.Now), view.Selection{SID: "build-42", Cursor: -1}, 40, modeltest.Now)
	checkShape(t, sb.Rendered, 40)
	got := strings.Join(texts(sb.Rendered), "\n")
	if !strings.Contains(got, "● alice@mac-1★ working") || strings.Contains(got, "bob@vps-2★") {
		t.Fatalf("sidebar:\n%s", got)
	}
	if d := strings.Join(texts(view.RenderAgent(v, "alice@mac-1", opts(90))), "\n"); !strings.Contains(d, "role         orchestrator") {
		t.Fatalf("details:\n%s", d)
	}
}
