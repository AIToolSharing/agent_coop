package model_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/model"
	"github.com/AIToolSharing/agent_coop/internal/wire"
	"pgregory.net/rapid"
)

const sid = "build-42"

var (
	alice = wire.Address{Agent: "alice", Machine: "mac-1"}
	bob   = wire.Address{Agent: "bob", Machine: "vps-2"}
	carol = wire.Address{Agent: "carol", Machine: "mac-3"}
	t0    = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
)

func at(s float64) string {
	return t0.Add(time.Duration(s * float64(time.Second))).UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// fixture is the session of packages/tui/test/fixture.ts: alice asks bob, bob answers, carol
// broadcasts, the operator steps in, a message is withdrawn, one question stays open.
func fixture() []model.Update {
	client := wire.Client{Name: "claude-code", Version: "2.1"}
	var seq int64
	var updates []model.Update
	ev := func(e wire.Event) string {
		seq++
		e.Seq = seq
		e.SID = sid
		updates = append(updates, model.Update{Event: &e})
		return fmt.Sprint(seq)
	}
	act := func(from wire.Address, a wire.Activity) {
		ev(wire.Event{Kind: wire.EventActivity, From: from.String(), Activity: &a})
	}
	msg := func(from, to, text, replyTo, sentAt string) string {
		return ev(wire.Event{Kind: wire.EventMsg, From: from, To: to, Text: text, ReplyTo: replyTo, SentAt: sentAt})
	}
	updates = append(updates,
		model.Update{Session: &wire.SessionUpdate{SID: sid, Record: &wire.SessionRecord{Status: "open", CreatedAt: at(-60)}}},
		model.Update{Session: &wire.SessionUpdate{SID: "docs", Record: &wire.SessionRecord{Status: "closed", CreatedAt: at(-600), ClosedAt: at(-60)}}},
	)
	act(alice, wire.Activity{Kind: "joined", Host: "mac-1", Cwd: "/src/app", Client: client, At: at(0)})
	act(bob, wire.Activity{Kind: "joined", Host: "vps-2", Cwd: "/srv/api", Client: wire.Client{Name: "codex", Version: "0.9"}, At: at(2)})
	act(carol, wire.Activity{Kind: "joined", Host: "mac-3", Cwd: "/src/app", Client: client, At: at(3)})
	plan := msg(carol.String(), "all", "I take src/users.ts and the tests", "", at(10))
	act(alice, wire.Activity{Kind: "delivered", ID: plan, Via: "push", At: at(10.12)})
	act(bob, wire.Activity{Kind: "delivered", ID: plan, Via: "pull", At: at(14)})
	q := msg(alice.String(), bob.String(), "What is the shape of GET /users?", "", at(20))
	act(alice, wire.Activity{Kind: "wait_start", From: bob.String(), ReplyTo: q, TimeoutS: 300, At: at(20.05)})
	act(bob, wire.Activity{Kind: "delivered", ID: q, Via: "pull", At: at(21)})
	act(bob, wire.Activity{Kind: "state", State: "working", Note: "answering alice", At: at(21.5)})
	a := msg(bob.String(), alice.String(), "{ id: number, name: string, email: string }", q, at(30))
	act(alice, wire.Activity{Kind: "delivered", ID: a, Via: "ask", At: at(30.09)})
	act(alice, wire.Activity{Kind: "wait_end", Result: "message", At: at(30.1)})
	wrong := msg(carol.String(), bob.String(), "the password is hunter2", "", at(40))
	ev(wire.Event{Kind: wire.EventRedact, ID: wrong, At: at(45)})
	op := msg(wire.Operator, "all", "Please run the tests before you say done", "", at(50))
	msg(bob.String(), wire.Operator, "Will do; CI is running", op, at(55))
	q2 := msg(carol.String(), bob.String(), "Can I change the users table?", "", at(60))
	act(carol, wire.Activity{Kind: "wait_start", From: bob.String(), ReplyTo: q2, TimeoutS: 300, At: at(60.02)})
	act(bob, wire.Activity{Kind: "state", State: "blocked", Note: "waiting for CI", At: at(70)})

	presence := func(a wire.Address, state, note string, waiting *wire.Waiting) model.Update {
		return model.Update{Presence: &wire.PresenceUpdate{
			Key:    wire.BuildPresenceKey(wire.PresenceKey{SID: sid, Agent: a}),
			Record: &wire.PresenceRecord{Host: a.Machine, Cwd: "/src/app", Client: client, State: state, Note: note, JoinedAt: at(0), Waiting: waiting},
		}}
	}
	updates = append(updates,
		presence(alice, "working", "parser", nil),
		presence(bob, "blocked", "waiting for CI", nil),
		presence(carol, "working", "users.ts", &wire.Waiting{On: bob.String(), ReplyTo: q2, Since: at(60.02)}),
	)
	return updates
}

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
