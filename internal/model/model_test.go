package model_test

import (
	"fmt"
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
	if !c.Online || c.State != "working" || c.Sent != 3 || c.Waiting == nil || c.Waiting.On != "bob@vps-2" || c.Waiting.ReplyTo != "18" {
		t.Fatalf("carol %+v", c)
	}
	msgs := v.Messages()
	var ids []string
	for _, m := range msgs {
		ids = append(ids, m.ID)
	}
	if fmt.Sprint(ids) != "[4 7 11 14 16 17 18]" {
		t.Fatalf("messages %v", ids)
	}
	if m := v.Msgs["7"]; len(m.Replies) != 1 || m.Replies[0].ID != "11" {
		t.Fatalf("replies of 7: %+v", m.Replies)
	}
	if m := v.Msgs["16"]; len(m.Replies) != 1 || m.Replies[0].ID != "17" {
		t.Fatalf("replies of 16: %+v", m.Replies)
	}
	if !v.Msgs["14"].Redacted || v.Msgs["11"].Redacted {
		t.Fatal("redaction")
	}
	asks := v.OpenAsks()
	if len(asks) != 1 || asks[0].ID != "18" || asks[0].From != "carol@mac-3" || asks[0].To != "bob@vps-2" || asks[0].Question.Text != "Can I change the users table?" {
		t.Fatalf("open asks %+v", asks)
	}
	if fy := v.ForYou(); len(fy) != 1 || fy[0].ID != "17" {
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
		"8 alice@mac-1 waits for bob@vps-2 (ask #7)",
		"10 bob@vps-2 is working: answering alice",
		"13 alice@mac-1 wait ended: message",
		"15 operator withdrew message #14",
		"19 carol@mac-3 waits for bob@vps-2 (ask #18)",
		"20 bob@vps-2 is blocked: waiting for CI",
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
