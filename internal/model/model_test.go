package model_test

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/AIToolSharing/agent_coop/internal/model"
	"github.com/AIToolSharing/agent_coop/internal/model/modeltest"
	"github.com/AIToolSharing/agent_coop/internal/wire"
	"pgregory.net/rapid"
)

const sid = modeltest.SID

var (
	alice = modeltest.Alice
	bob   = modeltest.Bob
	carol = modeltest.Carol
)

func at(s float64) string { return modeltest.At(s) }

func fixture() []model.Update { return modeltest.Fixture() }

func load(updates []model.Update) *model.Store {
	s := model.New()
	for _, u := range updates {
		s.Apply(u)
	}
	return s
}

// snapshot is a stable text form of everything the views read.
func snapshot(s *model.Store) string {
	var b strings.Builder
	ids := s.SessionIDs()
	fmt.Fprintf(&b, "sessions %v link %s\n", ids, s.Link)
	for _, id := range append([]string{model.AllSessions}, ids...) {
		v := s.View(id)
		fmt.Fprintf(&b, "[%s]\n", id)
		for _, a := range v.AgentList() {
			w := ""
			if a.Waiting != nil {
				w = " waits " + a.Waiting.On + "#" + a.Waiting.ReplyTo
			}
			l := ""
			if a.Left != nil {
				l = " left:" + a.Left.Reason
			}
			if a.Forgotten {
				l += " forgotten"
			}
			if a.Gate != "run" {
				l += " gate:" + a.Gate
			}
			if a.Gated {
				l += " gated"
			}
			if tr := a.Trace; len(tr.Items) > 0 || len(tr.Files) > 0 || tr.Branch != "" {
				l += fmt.Sprintf(" trace:%s:%v:%v:%v", tr.Branch, tr.Items, tr.FileList(), tr.Running())
			}
			fmt.Fprintf(&b, " agent %s/%s online=%v kicked=%v state=%s since=%s note=%q sent=%d joined=%s host=%s client=%q%s%s\n",
				a.SID, a.Address, a.Online, a.Kicked, a.State, a.StateSince, a.Note, a.Sent, a.JoinedAt, a.Host, a.Client, w, l)
		}
		for _, m := range v.Messages() {
			var r []string
			for _, x := range m.Replies {
				r = append(r, x.ID)
			}
			fmt.Fprintf(&b, " msg #%s %s>%s %q reply=%s redacted=%v replies=%v\n", m.ID, m.From, m.To, m.Text, m.ReplyTo, m.Redacted, r)
		}
		for _, q := range v.OpenAsks() {
			fmt.Fprintf(&b, " ask #%s %s>%s since=%s %q\n", q.ID, q.From, q.To, q.Since, q.Question.Text)
		}
		for _, m := range v.ForYou() {
			fmt.Fprintf(&b, " for-you #%s\n", m.ID)
		}
		for _, it := range v.Timeline {
			if it.Msg != nil {
				fmt.Fprintf(&b, " t %d msg #%s\n", it.Seq, it.Msg.ID)
			} else {
				fmt.Fprintf(&b, " t %d sys %s %s\n", it.Seq, it.Sys.Who, it.Sys.Text)
			}
		}
	}
	return b.String()
}

func TestFixtureDerivesWhatTheTypeScriptModelDerived(t *testing.T) {
	s := load(fixture())
	if got := s.SessionIDs(); fmt.Sprint(got) != "[build-42 docs]" {
		t.Fatalf("sessions %v", got)
	}
	if s.Sessions["docs"].Status != "closed" {
		t.Fatal("docs is closed")
	}
	v := s.View(sid)
	agents := v.AgentList()
	if len(agents) != 3 || agents[0].Address != "alice@mac-1" || agents[1].Address != "bob@vps-2" || agents[2].Address != "carol@mac-3" {
		t.Fatalf("agents %+v", agents)
	}
	a, b, c := agents[0], agents[1], agents[2]
	if !a.Online || a.State != "working" || a.Note != "parser" || a.Sent != 1 || a.JoinedAt != at(0) || a.Host != "mac-1" || a.Client != "claude-code 2.1" || a.Waiting != nil {
		t.Fatalf("alice %+v", a)
	}
	if !b.Online || b.State != "blocked" || b.Note != "waiting for CI" || b.Sent != 2 || b.Client != "codex 0.9" {
		t.Fatalf("bob %+v", b)
	}
	if !c.Online || c.State != "working" || c.Sent != 3 || c.Waiting == nil || c.Waiting.On != "bob@vps-2" || c.Waiting.ReplyTo != "14" {
		t.Fatalf("carol %+v", c)
	}
	msgs := v.Messages()
	var ids []string
	for _, m := range msgs {
		ids = append(ids, m.ID)
	}
	if fmt.Sprint(ids) != "[4 5 8 10 12 13 14]" {
		t.Fatalf("messages %v", ids)
	}
	if m := v.Msgs["5"]; len(m.Replies) != 1 || m.Replies[0].ID != "8" {
		t.Fatalf("replies of 5: %+v", m.Replies)
	}
	if m := v.Msgs["12"]; len(m.Replies) != 1 || m.Replies[0].ID != "13" {
		t.Fatalf("replies of 12: %+v", m.Replies)
	}
	if !v.Msgs["10"].Redacted || v.Msgs["8"].Redacted {
		t.Fatal("redaction")
	}
	asks := v.OpenAsks()
	if len(asks) != 1 || asks[0].ID != "14" || asks[0].From != "carol@mac-3" || asks[0].To != "bob@vps-2" || asks[0].Question.Text != "Can I change the users table?" {
		t.Fatalf("open asks %+v", asks)
	}
	if fy := v.ForYou(); len(fy) != 1 || fy[0].ID != "13" {
		t.Fatalf("for you %+v", fy)
	}
	if len(v.Timeline) != 16 {
		t.Fatalf("timeline has %d items", len(v.Timeline))
	}
	var sys []string
	for _, it := range v.Timeline {
		if it.Sys != nil {
			sys = append(sys, fmt.Sprintf("%d %s %s", it.Seq, it.Sys.Who, it.Sys.Text))
		}
	}
	want := []string{
		"1 alice@mac-1 joined from mac-1 (claude-code 2.1)",
		"2 bob@vps-2 joined from vps-2 (codex 0.9)",
		"3 carol@mac-3 joined from mac-3 (claude-code 2.1)",
		"6 alice@mac-1 waits for bob@vps-2 (ask #5)",
		"7 bob@vps-2 is working: answering alice",
		"9 alice@mac-1 wait ended: message",
		"11 operator withdrew message #10",
		"15 carol@mac-3 waits for bob@vps-2 (ask #14)",
		"16 bob@vps-2 is blocked: waiting for CI",
	}
	if fmt.Sprint(sys) != fmt.Sprint(want) {
		t.Fatalf("system lines\n got %v\nwant %v", sys, want)
	}
	all := s.View(model.AllSessions)
	if len(all.Timeline) != 16 || len(all.AgentList()) != 3 || all.AgentList()[0].SID != sid {
		t.Fatalf("all view: %d items, %d agents", len(all.Timeline), len(all.AgentList()))
	}
	if s.View("docs").Timeline != nil {
		t.Fatal("docs has no traffic")
	}
}

func TestTheOrderOfArrivalDoesNotMatter(t *testing.T) {
	want := snapshot(load(fixture()))
	rapid.Check(t, func(rt *rapid.T) {
		updates := fixture()
		perm := rapid.Permutation(updates).Draw(rt, "order")
		if got := snapshot(load(perm)); got != want {
			rt.Fatalf("snapshot differs\n got:\n%s\nwant:\n%s", got, want)
		}
	})
}

// forgetBob gives the fixture, then: Bob leaves and the operator forgets him. With rejoin, Bob
// then joins again.
func forgetBob(rejoin bool) []model.Update {
	// Bob has no presence record: he is not in the session. The fixture's presence records
	// have no revision, so a removal could not follow them in each order of arrival.
	updates := slices.DeleteFunc(fixture(), func(u model.Update) bool {
		return u.Presence != nil && u.Presence.Key == "build-42.vps-2.bob"
	})
	act := func(seq int64, a wire.Activity) {
		updates = append(updates, model.Update{Event: &wire.Event{Kind: wire.EventActivity, Seq: seq, SID: sid, From: bob.String(), Activity: &a}})
	}
	act(500, wire.Activity{Kind: "left", Reason: "disconnected", At: at(80)})
	act(501, wire.Activity{Kind: "forgotten", At: at(90)})
	if rejoin {
		act(502, wire.Activity{Kind: "joined", Host: "vps-2", Cwd: "/srv/api", Client: wire.Client{Name: "codex", Version: "0.9"}, At: at(100)})
	}
	return updates
}

func bobOf(s *model.Store) *model.Agent {
	return s.View(sid).Agents[bob.String()]
}

// An agent is forgotten from the forgotten record until its next join, in each order of
// arrival of the facts.
func TestAnAgentIsForgottenUntilItJoinsAgain(t *testing.T) {
	if b := bobOf(load(forgetBob(false))); !b.Forgotten || b.Online {
		t.Fatalf("after the forgotten record: forgotten=%v online=%v", b.Forgotten, b.Online)
	}
	if b := bobOf(load(forgetBob(true))); b.Forgotten || b.Left != nil || b.State != "idle" {
		t.Fatalf("after the new join: forgotten=%v left=%v state=%s", b.Forgotten, b.Left, b.State)
	}
	if b := bobOf(load(fixture())); b.Forgotten {
		t.Fatal("forgotten with no forgotten record")
	}
	for _, rejoin := range []bool{false, true} {
		want := snapshot(load(forgetBob(rejoin)))
		if !strings.Contains(want, "sys bob@vps-2 forgotten by the operator") {
			t.Fatalf("no timeline line for the forgotten record:\n%s", want)
		}
		rapid.Check(t, func(rt *rapid.T) {
			perm := rapid.Permutation(forgetBob(rejoin)).Draw(rt, "order")
			if got := snapshot(load(perm)); got != want {
				rt.Fatalf("rejoin=%v: snapshot differs\n got:\n%s\nwant:\n%s", rejoin, got, want)
			}
		})
	}
}

// The gate of an agent is what its last gate record says, in each order of arrival. A
// forgotten agent has no gate: the hub gives it a new one at its next join.
func TestTheGateOfAnAgentIsItsLastGateRecord(t *testing.T) {
	gates := func(records ...string) []model.Update {
		updates := fixture()
		for i, g := range records {
			a := wire.Activity{Kind: "gate", Gate: g, At: at(float64(80 + i))}
			if g == "forgotten" {
				a = wire.Activity{Kind: "forgotten", At: at(float64(80 + i))}
			}
			updates = append(updates, model.Update{Event: &wire.Event{Kind: wire.EventActivity, Seq: int64(500 + i), SID: sid, From: bob.String(), Activity: &a}})
		}
		return updates
	}
	for _, c := range []struct {
		records []string
		want    string
	}{
		{nil, "run"},
		{[]string{"held"}, "held"},
		{[]string{"held", "run"}, "run"},
		{[]string{"held", "run", "paused"}, "paused"},
		{[]string{"held", "forgotten"}, "run"},
		{[]string{"paused", "forgotten", "held"}, "held"},
	} {
		if got := bobOf(load(gates(c.records...))).Gate; got != c.want {
			t.Fatalf("records %v: gate %q, want %q", c.records, got, c.want)
		}
	}
	want := snapshot(load(gates("held", "run", "paused")))
	if !strings.Contains(want, "gate:paused") || !strings.Contains(want, "sys bob@vps-2 is held until the operator releases it") ||
		!strings.Contains(want, "sys bob@vps-2 released by the operator") || !strings.Contains(want, "sys bob@vps-2 paused by the operator") {
		t.Fatalf("the snapshot lacks the gate or its timeline lines:\n%s", want)
	}
	// A change by an orchestrator names it, so that the operator sees who released whom.
	byOrch := fixture()
	byOrch = append(byOrch,
		model.Update{Event: &wire.Event{Kind: wire.EventActivity, Seq: 600, SID: sid, From: bob.String(), Activity: &wire.Activity{Kind: "gate", Gate: "run", At: at(90), By: "orch"}}},
		model.Update{Event: &wire.Event{Kind: wire.EventKick, Seq: 601, SID: sid, Target: carol.String(), At: at(91), By: "orch"}},
	)
	if got := snapshot(load(byOrch)); !strings.Contains(got, "sys bob@vps-2 released by the orchestrator orch") || !strings.Contains(got, "sys carol@mac-3 removed by the orchestrator orch") {
		t.Fatalf("the timeline does not name the orchestrator:\n%s", got)
	}
	rapid.Check(t, func(rt *rapid.T) {
		perm := rapid.Permutation(gates("held", "run", "paused")).Draw(rt, "order")
		if got := snapshot(load(perm)); got != want {
			rt.Fatalf("snapshot differs\n got:\n%s\nwant:\n%s", got, want)
		}
	})
}

// An agent is gated while it is online and its presence record says so.
func TestGatedComesFromThePresenceRecord(t *testing.T) {
	s := load(fixture())
	if bobOf(s).Gated {
		t.Fatal("gated with a presence record that does not say so")
	}
	rec := wire.PresenceRecord{Host: "vps-2", Cwd: "/srv/api", State: "idle", JoinedAt: at(2), Gated: true}
	s.Apply(model.Update{Presence: &wire.PresenceUpdate{Key: "build-42.vps-2.bob", Revision: 1000, Record: &rec}})
	if !bobOf(s).Gated {
		t.Fatal("not gated with a presence record that says so")
	}
	s.Apply(model.Update{Presence: &wire.PresenceUpdate{Key: "build-42.vps-2.bob", Revision: 1001}})
	if b := bobOf(s); b.Gated || b.Online {
		t.Fatalf("offline: gated=%v online=%v", b.Gated, b.Online)
	}
}

func TestApplyingTheSameFactsAgainChangesNothing(t *testing.T) {
	updates := fixture()
	want := snapshot(load(updates))
	s := load(updates)
	for _, u := range updates {
		s.Apply(u)
	}
	if got := snapshot(s); got != want {
		t.Fatalf("\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAnOlderRevisionNeverReplacesANewerOne(t *testing.T) {
	s := load(fixture())
	key := wire.BuildPresenceKey(wire.PresenceKey{SID: sid, Agent: bob})
	rec := func(state string) *wire.PresenceRecord {
		return &wire.PresenceRecord{Host: "vps-2", Cwd: "/srv", Client: wire.Client{Name: "codex", Version: "1"}, State: state, JoinedAt: at(0)}
	}
	s.Apply(model.Update{Presence: &wire.PresenceUpdate{Key: key, Revision: 5, Record: rec("done")}})
	s.Apply(model.Update{Presence: &wire.PresenceUpdate{Key: key, Revision: 3, Record: rec("idle")}})
	if got := s.View(sid).Agents["bob@vps-2"].State; got != "done" {
		t.Fatalf("state %s", got)
	}
	s.Apply(model.Update{Presence: &wire.PresenceUpdate{Key: key, Revision: 6, Record: nil}})
	if a := s.View(sid).Agents["bob@vps-2"]; a.Online || a.State != "blocked" {
		t.Fatalf("after presence left: online=%v state=%s", a.Online, a.State)
	}
}

func TestDeletingASessionRemovesItsTraffic(t *testing.T) {
	s := load(fixture())
	other := wire.Event{Kind: wire.EventMsg, Seq: 99, SID: "docs", From: alice.String(), To: "all", Text: "hi", SentAt: at(100)}
	s.Apply(model.Update{Event: &other})
	if len(s.View(model.AllSessions).Timeline) != 17 {
		t.Fatal("docs traffic should be in the all view")
	}
	s.Apply(model.Update{Session: &wire.SessionUpdate{SID: sid, Record: nil}})
	if got := s.SessionIDs(); fmt.Sprint(got) != "[docs]" {
		t.Fatalf("sessions %v", got)
	}
	all := s.View(model.AllSessions)
	if len(all.Timeline) != 1 || all.Timeline[0].Seq != 99 || len(all.AgentList()) != 1 {
		t.Fatalf("all view kept %d items, %d agents", len(all.Timeline), len(all.AgentList()))
	}
	if v := s.View(sid); len(v.Timeline) != 0 {
		t.Fatal("the deleted session still has items")
	}
}

func TestFactsAboutAMessageMayArriveBeforeIt(t *testing.T) {
	s := model.New()
	s.Apply(model.Update{Session: &wire.SessionUpdate{SID: sid, Record: &wire.SessionRecord{Status: "open", CreatedAt: at(0)}}})
	s.Apply(model.Update{Event: &wire.Event{Kind: wire.EventRedact, Seq: 10, SID: sid, ID: "12", At: at(5)}})
	s.Apply(model.Update{Event: &wire.Event{Kind: wire.EventMsg, Seq: 13, SID: sid, From: bob.String(), To: "all", Text: "re", ReplyTo: "12", SentAt: at(4)}})
	s.Apply(model.Update{Event: &wire.Event{Kind: wire.EventMsg, Seq: 12, SID: sid, From: alice.String(), To: "all", Text: "first", SentAt: at(3)}})
	v := s.View(sid)
	m := v.Msgs["12"]
	if m == nil || !m.Redacted || len(m.Replies) != 1 || m.Replies[0].ID != "13" {
		t.Fatalf("%+v", m)
	}
	if seqs := []int64{v.Timeline[0].Seq, v.Timeline[1].Seq, v.Timeline[2].Seq}; fmt.Sprint(seqs) != "[10 12 13]" {
		t.Fatalf("timeline order %v", seqs)
	}
	sorted := sort.SliceIsSorted(v.Messages(), func(i, j int) bool { return v.Messages()[i].Seq < v.Messages()[j].Seq })
	if !sorted {
		t.Fatal("messages are not in sequence order")
	}
}

func TestASnapshotRemovesWhatTheFeedDidNotSendAgain(t *testing.T) {
	s := load(fixture())
	bobKey := wire.BuildPresenceKey(wire.PresenceKey{SID: sid, Agent: bob})
	s.Apply(model.Update{Snapshot: &model.Snapshot{Bucket: "presence", Seen: map[string]bool{
		wire.BuildPresenceKey(wire.PresenceKey{SID: sid, Agent: alice}): true,
		wire.BuildPresenceKey(wire.PresenceKey{SID: sid, Agent: carol}): true,
	}}})
	if _, ok := s.Presence[bobKey]; ok || s.View(sid).Agents["bob@vps-2"].Online {
		t.Fatal("bob's presence should be gone")
	}
	s.Apply(model.Update{Snapshot: &model.Snapshot{Bucket: "sessions", Seen: map[string]bool{"docs": true}}})
	if got := s.SessionIDs(); fmt.Sprint(got) != "[docs]" {
		t.Fatalf("sessions %v", got)
	}
	if len(s.View(model.AllSessions).Timeline) != 0 {
		t.Fatal("the deleted session's traffic is still in the all view")
	}
}

func TestReshapedCountsChangesToWhatWasAlreadyShown(t *testing.T) {
	s := model.New()
	s.Apply(model.Update{Session: &wire.SessionUpdate{SID: sid, Record: &wire.SessionRecord{Status: "open", CreatedAt: at(0)}}})
	v := s.View(sid)
	s.Apply(model.Update{Event: &wire.Event{Kind: wire.EventMsg, Seq: 5, SID: sid, From: alice.String(), To: "all", Text: "a", SentAt: at(1)}})
	s.Apply(model.Update{Event: &wire.Event{Kind: wire.EventMsg, Seq: 6, SID: sid, From: bob.String(), To: "all", Text: "b", SentAt: at(2)}})
	if v.Reshaped != 0 {
		t.Fatalf("appends reshaped: %d", v.Reshaped)
	}
	s.Apply(model.Update{Event: &wire.Event{Kind: wire.EventMsg, Seq: 4, SID: sid, From: bob.String(), To: "all", Text: "late", SentAt: at(0.5)}})
	if v.Reshaped != 1 {
		t.Fatalf("an insert before the end reshaped: %d", v.Reshaped)
	}
	s.Apply(model.Update{Event: &wire.Event{Kind: wire.EventMsg, Seq: 7, SID: sid, From: bob.String(), To: "all", Text: "re", ReplyTo: "5", SentAt: at(3)}})
	if v.Reshaped != 2 {
		t.Fatalf("a reply to a shown message reshaped: %d", v.Reshaped)
	}
	s.Apply(model.Update{Event: &wire.Event{Kind: wire.EventRedact, Seq: 8, SID: sid, ID: "6", At: at(4)}})
	if v.Reshaped != 3 {
		t.Fatalf("a withdrawal reshaped: %d", v.Reshaped)
	}
}

// traceRun is the trace updates that one run of a hub sends for Alice and Bob: one update for
// each report, and now and then the whole trace of an agent, as a feed gets it when it
// connects. It also gives what the model must hold after all of them.
func traceRun(rt *rapid.T, boot int64, label string) (updates []model.Update, want map[wire.Address]*model.Trace) {
	type log struct {
		items  []wire.TraceItem
		files  map[string]wire.TraceFile
		branch string
	}
	logs := map[wire.Address]*log{alice: {files: map[string]wire.TraceFile{}}, bob: {files: map[string]wire.TraceFile{}}}
	var n int64
	kinds := []string{wire.TraceToolStart, wire.TraceToolEnd, wire.TraceSay, wire.TracePrompt}
	for range rapid.IntRange(0, 40).Draw(rt, label+" reports") {
		who := rapid.SampledFrom([]wire.Address{alice, bob}).Draw(rt, label+" agent")
		l := logs[who]
		u := wire.TraceUpdate{Boot: boot, Key: wire.BuildPresenceKey(wire.PresenceKey{SID: sid, Agent: who})}
		if b := rapid.SampledFrom([]string{"", "main", "fix"}).Draw(rt, label+" branch"); b != "" {
			l.branch = b
		}
		u.Branch = l.branch
		for range rapid.IntRange(1, 20).Draw(rt, label+" items") {
			n++
			it := wire.TraceItem{
				N: n, At: at(float64(n)), Kind: rapid.SampledFrom(kinds).Draw(rt, label+" kind"),
				ID:    "t" + fmt.Sprint(rapid.IntRange(1, 6).Draw(rt, label+" id")),
				Final: rapid.Bool().Draw(rt, label+" final"),
			}
			l.items = append(l.items, it)
			u.Items = append(u.Items, it)
			if it.Kind == wire.TraceToolEnd {
				path := rapid.SampledFrom([]string{"a.go", "b.go", "c.go"}).Draw(rt, label+" file")
				f := wire.TraceFile{Path: path, Count: l.files[path].Count + 1, At: it.At}
				l.files[path] = f
				u.Files = append(u.Files, f)
			}
		}
		updates = append(updates, model.Update{Trace: &u})
		if rapid.IntRange(0, 4).Draw(rt, label+" dump") == 0 {
			dump := wire.TraceUpdate{Boot: boot, Key: u.Key, Branch: l.branch, Items: l.items[max(0, len(l.items)-model.TraceMax):]}
			for _, f := range l.files {
				dump.Files = append(dump.Files, f)
			}
			updates = append(updates, model.Update{Trace: &dump})
		}
	}
	want = map[wire.Address]*model.Trace{}
	for who, l := range logs {
		want[who] = &model.Trace{Items: l.items[max(0, len(l.items)-model.TraceMax):], Files: l.files, Branch: l.branch}
	}
	return updates, want
}

func traceText(t *model.Trace) string {
	return fmt.Sprintf("%s:%v:%v", t.Branch, t.Items, t.FileList())
}

// The trace comes over the same feed as the rest, so the same rule holds: the order of
// arrival does not matter, and a fact that arrives two times counts one time. After a hub
// starts again, only the trace of its new run counts.
func TestTheTraceDoesNotDependOnTheOrderOfArrival(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		old, _ := traceRun(rt, 1000, "old")
		updates, want := traceRun(rt, 2000, "new")
		if len(updates) == 0 {
			// With no update of the new run, the old run is the newest one.
			return
		}
		all := append(append(fixture(), old...), updates...)
		for _, i := range rapid.SliceOfN(rapid.IntRange(0, len(all)-1), 0, 10).Draw(rt, "repeats") {
			all = append(all, all[i])
		}
		s := load(rapid.Permutation(all).Draw(rt, "order"))
		for _, view := range []string{sid, model.AllSessions} {
			for who, tr := range want {
				key := who.String()
				if view == model.AllSessions {
					key = sid + "/" + key
				}
				a := s.View(view).Agents[key]
				if a == nil {
					rt.Fatalf("view %s has no agent %s", view, key)
				}
				if got, want := traceText(a.Trace), traceText(tr); got != want {
					rt.Fatalf("view %s, %s:\n got %s\nwant %s", view, who, got, want)
				}
			}
		}
	})
}

func TestRunningToolCallsAreTheOnesWithNoEnd(t *testing.T) {
	item := func(n int64, kind, id string, final bool) wire.TraceItem {
		return wire.TraceItem{N: n, Kind: kind, ID: id, Final: final}
	}
	ids := func(tr *model.Trace) string {
		var out []string
		for _, it := range tr.Running() {
			out = append(out, it.ID)
		}
		return strings.Join(out, " ")
	}
	tr := &model.Trace{Items: []wire.TraceItem{
		item(1, wire.TraceToolStart, "a", false),
		item(2, wire.TraceToolStart, "b", false),
		item(3, wire.TraceSay, "", false),
		item(4, wire.TraceToolEnd, "a", false),
		item(5, wire.TraceToolStart, "c", false),
	}}
	if got := ids(tr); got != "b c" {
		t.Fatalf("running %q, want b c", got)
	}
	// The person at the terminal stopped the turn: the calls report no end. The next prompt,
	// or the words that end the turn, end them.
	for _, last := range []wire.TraceItem{item(6, wire.TracePrompt, "", false), item(6, wire.TraceSay, "", true)} {
		stopped := &model.Trace{Items: append(slices.Clone(tr.Items), last)}
		if got := ids(stopped); got != "" {
			t.Fatalf("running %q after %s, want none", got, last.Kind)
		}
	}
}

func TestDeletingASessionRemovesItsTrace(t *testing.T) {
	s := load(fixture())
	key := wire.BuildPresenceKey(wire.PresenceKey{SID: sid, Agent: alice})
	s.Apply(model.Update{Trace: &wire.TraceUpdate{Boot: 1, Key: key, Items: []wire.TraceItem{{N: 1, Kind: wire.TraceSay, Text: "hi"}}}})
	if n := len(s.View(sid).Agents[alice.String()].Trace.Items); n != 1 {
		t.Fatalf("%d trace items, want 1", n)
	}
	if acts := s.View(model.AllSessions).Activity(); len(acts) != 1 || acts[0].Agent.Address != alice.String() {
		t.Fatalf("activity of the all view %+v, want the one item of alice", acts)
	}
	s.Apply(model.Update{Session: &wire.SessionUpdate{SID: sid, Record: nil}})
	s.Apply(model.Update{Session: &wire.SessionUpdate{SID: sid, Revision: 0, Record: &wire.SessionRecord{Status: "open", CreatedAt: at(500)}}})
	s.Apply(model.Update{Presence: &wire.PresenceUpdate{Key: key, Record: &wire.PresenceRecord{Host: "mac-1", State: "idle", JoinedAt: at(501)}}})
	if n := len(s.View(sid).Agents[alice.String()].Trace.Items); n != 0 {
		t.Fatalf("%d trace items in a session with the name of a deleted one, want 0", n)
	}
}
