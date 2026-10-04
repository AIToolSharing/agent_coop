package api_test

// The behaviour of the hub, ported one test to one test from packages/hub/test/hub.int.test.ts.
// Each describe block of that file is a subtest of TestHub; the tests run in the order of that
// file, one at a time, against one hub.

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// hubSuite is what the tests share: one hub, three machines, the operator and the session
// counter.
type hubSuite struct {
	h                *harness
	op               operator // h.op in hub.int.test.ts
	mac1, vps2, mac3 api
	n                int
}

// session creates a fresh open session per test, so tests do not see each other's traffic.
func (x *hubSuite) session(t *testing.T) string {
	t.Helper()
	x.n++
	sid := fmt.Sprintf("s%d", x.n)
	x.op.createSession(t, sid)
	return sid
}

func TestHub(t *testing.T) {
	// High limits here; the rate limit test below uses its own hub with low limits.
	high := limit{burst: 10_000, perSecond: 10_000}
	h := startHub(t, limits{join: high, msg: high, activity: high}, options{})
	x := &hubSuite{
		h:    h,
		op:   operator{admin{h.base, h.operatorToken("op")}},
		mac1: api{h.base, h.token("mac-1")},
		vps2: api{h.base, h.token("vps-2")},
		mac3: api{h.base, h.token("mac-3")},
	}
	t.Run("authentication", x.authentication)
	t.Run("joining", x.joining)
	t.Run("messages", x.messages)
	t.Run("rate limits", rateLimits)
	t.Run("resume", x.resume)
	t.Run("operator actions reach agents", x.operatorActions)
	t.Run("peer presence", x.peerPresence)
	t.Run("peers that left", x.peersThatLeft)
	t.Run("activity and presence", x.activityAndPresence)
	t.Run("session auto-create", x.autoCreate)
	t.Run("admin API", x.adminAPI)
	t.Run("admin feed", x.adminFeed)
	t.Run("contract document", x.contractDocument)
}

func (x *hubSuite) authentication(t *testing.T) {
	t.Run("a request without a valid token gets 401", func(t *testing.T) {
		sid := x.session(t)
		wantStatus(t, 401)(api{x.h.base, "nope"}.view(sid, "a"))
		wantStatus(t, 401)(api{x.h.base, "mac-1.wrong-secret"}.view(sid, "a"))
		wantStatus(t, 401)(fetch(x.h.base + "/v1/sessions/" + sid))
	})

	t.Run("an operator token cannot act as an agent: 403 on the agent routes", func(t *testing.T) {
		sid := x.session(t)
		op := api{x.h.base, x.h.operatorToken("matt")}
		body := wantStatus(t, 403)(op.view(sid, "a"))
		if m := parse[errBody](t, body).Message; !strings.Contains(m, "operator token") {
			t.Fatalf("message %q", m)
		}
		wantStatus(t, 403)(op.stream(sid, "a").result())
		roles := x.h.tokenRoles()
		if roles["matt"] != "operator" {
			t.Fatalf("role of matt: %q", roles["matt"])
		}
		if roles["mac-1"] != "machine" {
			t.Fatalf("role of mac-1: %q", roles["mac-1"])
		}
	})
}

func (x *hubSuite) joining(t *testing.T) {
	t.Run("the first event names the agent; the join is recorded", func(t *testing.T) {
		sid := x.session(t)
		s := x.mac1.stream(sid, "alice")
		defer s.close()
		wantStatus(t, 200)(s.status(), s.body)
		got := data[joinedEvent](t, s.wait(t, nil))
		if want := (joinedEvent{Me: "alice@mac-1", Session: sid, Gate: "run"}); got != want {
			t.Fatalf("joined %+v, want %+v", got, want)
		}
		if _, ok := x.h.presence(sid + ".mac-1.alice"); !ok {
			t.Fatal("no presence record")
		}
	})

	t.Run("an unknown session is 404 and a closed session is 403", func(t *testing.T) {
		wantStatus(t, 404)(x.mac1.stream("nosuch", "alice").result())
		sid := x.session(t)
		x.op.closeSession(t, sid)
		wantStatus(t, 403)(x.mac1.stream(sid, "alice").result())
	})

	t.Run("a taken name is 409; the same instance takes over its old stream", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		wantStatus(t, 409)(x.mac1.stream(sid, "alice").result())
		again := x.mac1.stream(sid, "alice", withInstance(a.instance))
		defer again.close()
		wantStatus(t, 200)(again.status(), again.body)
		if err := a.ended(0); err != nil {
			t.Fatal(err)
		}
	})

	// Found by the shim tests: two joins at the same moment both got the name.
	t.Run("two joins of one name at the same moment: exactly one wins", func(t *testing.T) {
		sid := x.session(t)
		for round := range 5 {
			name := fmt.Sprintf("race%d", round)
			var a, b *stream
			var wg sync.WaitGroup
			wg.Go(func() { a = x.mac1.stream(sid, name) })
			wg.Go(func() { b = x.mac1.stream(sid, name) })
			wg.Wait()
			codes := []int{a.status(), b.status()}
			a.close()
			b.close()
			slices.Sort(codes)
			if !slices.Equal(codes, []int{200, 409}) {
				t.Fatalf("round %d: statuses %v, want [200 409]", round, codes)
			}
		}
	})

	// Asked for by the operator: a session that is refused because the name is in use must
	// show, as a removed agent that tries to join does.
	t.Run("a join refused for a name in use leaves a refused record; a repeat soon after leaves none", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		taken := func() int {
			n := 0
			for _, e := range x.h.events(sid) {
				if e.Kind == wire.EventActivity && e.From == "alice@mac-1" && e.Activity != nil && e.Activity.Kind == "refused" && e.Activity.Reason == "taken" {
					n++
				}
			}
			return n
		}
		wantStatus(t, 409)(x.mac1.stream(sid, "alice").result())
		if n := taken(); n != 1 {
			t.Fatalf("%d refused records after the first try, want 1", n)
		}
		wantStatus(t, 409)(x.mac1.stream(sid, "alice").result())
		if n := taken(); n != 1 {
			t.Fatalf("%d refused records after a second try soon after, want 1", n)
		}
		// The agent that holds the name is not disturbed.
		a.none(t, eventIs("notice"), 200*time.Millisecond)
	})

	t.Run("the same name on two machines is allowed", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "agent")
		defer a.close()
		b := x.vps2.stream(sid, "agent")
		defer b.close()
		if got := []int{a.status(), b.status()}; !slices.Equal(got, []int{200, 200}) {
			t.Fatalf("statuses %v, want [200 200]", got)
		}
	})
}

func (x *hubSuite) messages(t *testing.T) {
	t.Run("a broadcast reaches others, not the sender, with the sender stamped by the hub", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		b := x.vps2.stream(sid, "bob")
		defer b.close()
		a.wait(t, nil)
		b.wait(t, nil)
		t0 := time.Now()
		body := wantStatus(t, 200)(x.mac1.send(sid, "alice", "all", "hello all", ""))
		sent := parse[sendResponse](t, body)
		got := data[apiMessage](t, b.wait(t, isMsg))
		took := time.Since(t0)
		if got.ID != sent.ID || got.From != "alice@mac-1" || got.To != "all" || got.Text != "hello all" {
			t.Fatalf("message %+v; sent %+v", got, sent)
		}
		if took >= 250*time.Millisecond {
			t.Fatalf("the message took %v; the limit is 250 ms", took)
		}
		a.none(t, isMsg, 500*time.Millisecond)
	})

	t.Run("a direct message reaches only its target, in stream and in history", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		b := x.vps2.stream(sid, "bob")
		defer b.close()
		c := x.mac3.stream(sid, "carol")
		defer c.close()
		for _, s := range []*stream{a, b, c} {
			s.wait(t, nil)
		}
		r := parse[sendResponse](t, jsonOf(x.mac1.send(sid, "alice", "bob", "secret", "")))
		if r.To != "bob@vps-2" {
			t.Fatalf("to %q", r.To)
		}
		if m := data[apiMessage](t, b.wait(t, isMsg)); m.Text != "secret" {
			t.Fatalf("text %q", m.Text)
		}
		c.none(t, isMsg, 500*time.Millisecond)
		hb := parse[historyResponse](t, jsonOf(x.vps2.history(sid, "bob", "")))
		hc := parse[historyResponse](t, jsonOf(x.mac3.history(sid, "carol", "")))
		if ids := messageIDs(hb.Messages); !slices.Contains(ids, r.ID) {
			t.Fatalf("history of bob %v has no %s", ids, r.ID)
		}
		if ids := messageIDs(hc.Messages); slices.Contains(ids, r.ID) {
			t.Fatalf("history of carol %v has %s", ids, r.ID)
		}
	})

	t.Run("a body with a from field is refused", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		wantStatus(t, 422)(x.mac1.req(http.MethodPost, "/v1/sessions/"+sid+"/messages", map[string]any{
			"agent": "alice",
			"to":    "all",
			"text":  "x",
			"from":  "bob@vps-2",
		}))
	})

	t.Run("an agent name without a live stream of this token is 403", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		wantStatus(t, 403)(x.vps2.send(sid, "alice", "all", "spoof", ""))
		if m := parse[errBody](t, jsonOf(x.vps2.send(sid, "alice", "all", "spoof", ""))).Message; m != "not in a session" {
			t.Fatalf("message %q", m)
		}
	})

	t.Run("unknown, ambiguous, self, and full-name recipients", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		x1 := x.vps2.stream(sid, "x")
		defer x1.close()
		x3 := x.mac3.stream(sid, "x")
		defer x3.close()
		for _, s := range []*stream{a, x1, x3} {
			s.wait(t, nil)
		}
		unknown := wantStatus(t, 404)(x.mac1.send(sid, "alice", "zed", "hi", ""))
		if m := parse[errBody](t, unknown).Message; !strings.Contains(m, "x@vps-2") {
			t.Fatalf("message %q", m)
		}
		status, amb := x.mac1.send(sid, "alice", "x", "hi", "")
		if e := parse[errBody](t, amb); status != 409 || e.Error != "ambiguous" {
			t.Fatalf("got [%d %q], want [409 ambiguous]", status, e.Error)
		}
		wantStatus(t, 409)(x.mac1.send(sid, "alice", "alice", "hi", ""))
		wantStatus(t, 409)(x.mac1.send(sid, "alice", "alice@mac-1", "hi", ""))
		wantStatus(t, 200)(x.mac1.send(sid, "alice", "operator", "hi", ""))
		// Found by Schemathesis: a schema-valid peer that is the caller is a conflict, not bad input.
		wantStatus(t, 409)(x.mac1.history(sid, "alice", "&with=alice%40mac-1"))
		wantStatus(t, 200)(x.mac1.send(sid, "alice", "x@mac-3", "hi", ""))
		if m := data[apiMessage](t, x3.wait(t, isMsg)); m.From != "alice@mac-1" {
			t.Fatalf("from %q", m.From)
		}
	})

	// Added with v0.3.0: the sender learns the recipient's state and can decide to wait.
	t.Run("send tells the recipient's state: live state, away, or none for all and operator", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		b := x.vps2.stream(sid, "bob")
		defer b.close()
		a.wait(t, nil)
		b.wait(t, nil)
		wantStatus(t, 204)(x.vps2.activity(sid, map[string]any{"kind": "state", "agent": "bob", "state": "blocked", "note": "need API"}))
		r := parse[sendResponse](t, wantStatus(t, 200)(x.mac1.send(sid, "alice", "bob", "one", "")))
		if !r.Online || r.State != "blocked" {
			t.Fatalf("send to a live peer: %+v, want online true and state blocked", r)
		}
		b.close()
		a.wait(t, eventIs("notice"))
		r = parse[sendResponse](t, wantStatus(t, 200)(x.mac1.send(sid, "alice", "bob", "two", "")))
		if r.Online || r.State != "away" {
			t.Fatalf("send to an away peer: %+v, want online false and state away", r)
		}
		for _, to := range []string{"all", "operator"} {
			r = parse[sendResponse](t, wantStatus(t, 200)(x.mac1.send(sid, "alice", to, "three", "")))
			if !r.Online || r.State != "" {
				t.Fatalf("send to %s: %+v, want online true and no state", to, r)
			}
		}
	})

	t.Run("size limits: text over 8000 is 422, a body over 32 KiB is 413", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		wantStatus(t, 422)(x.mac1.send(sid, "alice", "all", strings.Repeat("x", 8001), ""))
		wantStatus(t, 413)(x.mac1.send(sid, "alice", "all", strings.Repeat("x", 40_000), ""))
	})
}

func rateLimits(t *testing.T) {
	t.Run("bursts over the limit get 429, per machine", func(t *testing.T) {
		low := startHub(t, limits{
			join: limit{burst: 2, perSecond: 0.001},
			msg:  limit{burst: 3, perSecond: 0.001},
		}, options{})
		op := operator{admin{low.base, low.operatorToken("op")}}
		op.createSession(t, "rl")
		mac := api{low.base, low.token("mac-1")}
		other := api{low.base, low.token("vps-2")}
		a := mac.stream("rl", "alice")
		defer a.close()
		a.wait(t, nil)
		var codes []int
		for range 5 {
			status, _ := mac.send("rl", "alice", "all", "x", "")
			codes = append(codes, status)
		}
		if want := []int{200, 200, 200, 429, 429}; !slices.Equal(codes, want) {
			t.Fatalf("statuses %v, want %v", codes, want)
		}
		bob := mac.stream("rl", "bob")
		defer bob.close()
		wantStatus(t, 429)(mac.stream("rl", "carol").result())
		dave := other.stream("rl", "dave")
		defer dave.close()
		wantStatus(t, 200)(dave.status(), dave.body)
	})
}

func (x *hubSuite) resume(t *testing.T) {
	t.Run("Last-Event-ID resumes after a drop: the gap once, nothing twice", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		b := x.vps2.stream(sid, "bob")
		defer b.close()
		a.wait(t, nil)
		b.wait(t, nil)
		x.mac1.send(sid, "alice", "all", "m1", "")
		m1 := b.wait(t, isMsg)
		b.close()
		// bob is away: a broadcast still goes into the log.
		wantStatus(t, 200)(x.mac1.send(sid, "alice", "all", "m2", ""))
		b2 := x.vps2.stream(sid, "bob", withInstance(b.instance), withLastEventID(m1.id))
		defer b2.close()
		b2.wait(t, eventIs("joined"))
		if m := data[apiMessage](t, b2.wait(t, isMsg)); m.Text != "m2" {
			t.Fatalf("text %q", m.Text)
		}
		b2.none(t, isMsg, 500*time.Millisecond)
	})
}

func (x *hubSuite) operatorActions(t *testing.T) {
	t.Run("kick: notice, stream ends, rejoin and sends refused", func(t *testing.T) {
		sid := x.session(t)
		b := x.vps2.stream(sid, "bob")
		defer b.close()
		b.wait(t, nil)
		x.op.kick(t, sid, "bob@vps-2")
		if n := data[noticeEvent](t, b.wait(t, eventIs("notice"))); n.Kind != "kicked" {
			t.Fatalf("notice %+v", n)
		}
		if err := b.ended(0); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, 403)(x.vps2.stream(sid, "bob").result())
		wantStatus(t, 403)(x.vps2.send(sid, "bob", "all", "x", ""))
	})

	t.Run("close and reopen: notices, and sends refused while closed", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		x.op.closeSession(t, sid)
		if n := data[noticeEvent](t, a.wait(t, eventIs("notice"))); n.Kind != "closed" {
			t.Fatalf("notice %+v", n)
		}
		status, body := x.mac1.send(sid, "alice", "all", "x", "")
		if e := parse[errBody](t, body); status != 403 || e.Message != "session closed" {
			t.Fatalf("got [%d %q], want [403 session closed]", status, e.Message)
		}
		x.op.reopenSession(t, sid)
		if n := data[noticeEvent](t, a.wait(t, eventIs("notice"))); n.Kind != "reopened" {
			t.Fatalf("notice %+v", n)
		}
		wantStatus(t, 200)(x.mac1.send(sid, "alice", "all", "x", ""))
	})

	t.Run("redact: only agents that got the message get a notice; history drops it", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		b := x.vps2.stream(sid, "bob")
		defer b.close()
		c := x.mac3.stream(sid, "carol")
		defer c.close()
		for _, s := range []*stream{a, b, c} {
			s.wait(t, nil)
		}
		id := parse[sendResponse](t, jsonOf(x.mac1.send(sid, "alice", "bob", "oops", ""))).ID
		b.wait(t, isMsg)
		if !x.op.redact(t, sid, id) {
			t.Fatal("redact gave false")
		}
		if n := data[noticeEvent](t, b.wait(t, eventIs("notice"))); n.Kind != "redacted" || n.ID != id {
			t.Fatalf("notice %+v, want kind redacted and id %s", n, id)
		}
		c.none(t, eventIs("notice"), 500*time.Millisecond)
		hist := parse[historyResponse](t, jsonOf(x.vps2.history(sid, "bob", "")))
		if ids := messageIDs(hist.Messages); slices.Contains(ids, id) {
			t.Fatalf("history %v has %s", ids, id)
		}
	})

	// Found in the end-to-end run: an agent could not answer the operator ("send can't address operator").
	t.Run("an agent can answer the operator; no other agent receives it", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		b := x.vps2.stream(sid, "bob")
		defer b.close()
		a.wait(t, nil)
		b.wait(t, nil)
		body := wantStatus(t, 200)(x.mac1.send(sid, "alice", "operator", "ONLINE", ""))
		if to := parse[sendResponse](t, body).To; to != "operator" {
			t.Fatalf("to %q", to)
		}
		b.none(t, isMsg, 500*time.Millisecond)
		mine := parse[historyResponse](t, jsonOf(x.mac1.history(sid, "alice", ""))).Messages
		if !slices.ContainsFunc(mine, func(m apiMessage) bool { return m.To == "operator" && m.Text == "ONLINE" }) {
			t.Fatalf("history of alice %+v has no [operator ONLINE]", mine)
		}
		theirs := parse[historyResponse](t, jsonOf(x.vps2.history(sid, "bob", ""))).Messages
		if slices.ContainsFunc(theirs, func(m apiMessage) bool { return m.Text == "ONLINE" }) {
			t.Fatalf("history of bob %+v has ONLINE", theirs)
		}
	})

	t.Run("operator messages arrive from operator", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		x.op.send(t, sid, "alice@mac-1", "hi from the user")
		if m := data[apiMessage](t, a.wait(t, isMsg)); m.From != "operator" {
			t.Fatalf("from %q", m.From)
		}
	})

	t.Run("revoking a token ends its streams and its requests", func(t *testing.T) {
		tmp := api{x.h.base, x.h.token("temp")}
		sid := x.session(t)
		s := tmp.stream(sid, "tmp")
		defer s.close()
		s.wait(t, nil)
		x.h.revoke("temp")
		if err := s.ended(0); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, 401)(tmp.view(sid, "tmp"))
	})
}

func (x *hubSuite) peerPresence(t *testing.T) {
	t.Run("a peer's leave is a notice to the others; a join is not", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		b := x.vps2.stream(sid, "bob")
		defer b.close()
		b.wait(t, nil)
		a.none(t, eventIs("notice"), 300*time.Millisecond)
		b.close()
		n := data[noticeEvent](t, a.wait(t, eventIs("notice")))
		if n.Kind != "peer_left" || n.Peer != "bob@vps-2" {
			t.Fatalf("notice %+v, want kind peer_left and peer bob@vps-2", n)
		}
	})
}

func (x *hubSuite) peersThatLeft(t *testing.T) {
	notice := eventIs("notice")

	t.Run("a direct message to a peer that left is kept, and replayed when it joins again", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		x.mac1.send(sid, "alice", "all", "before bob", "")
		b := x.vps2.stream(sid, "bob")
		defer b.close()
		b.wait(t, nil)
		x.vps2.activity(sid, map[string]any{"kind": "state", "agent": "bob", "state": "done", "note": "shipped"})
		b.close()
		if n := data[noticeEvent](t, a.wait(t, notice)); n.Kind != "peer_left" {
			t.Fatalf("notice %+v", n)
		}
		body := wantStatus(t, 200)(x.mac1.send(sid, "alice", "bob", "while away", ""))
		if r := parse[sendResponse](t, body); r.To != "bob@vps-2" || r.Online {
			t.Fatalf("send %+v, want to bob@vps-2 and online false", r)
		}
		x.mac1.send(sid, "alice", "all", "news while away", "")
		v := parse[sessionView](t, jsonOf(x.mac1.view(sid, "alice")))
		if want := []peer{{Name: "bob@vps-2", State: "done", Note: "shipped", Online: false}}; !slices.Equal(v.Peers, want) {
			t.Fatalf("peers %+v, want %+v", v.Peers, want)
		}
		// A new process without Last-Event-ID gets what it missed, in order, and nothing older.
		b2 := x.vps2.stream(sid, "bob")
		defer b2.close()
		b2.wait(t, eventIs("joined"))
		if m := data[apiMessage](t, b2.wait(t, isMsg)); m.Text != "while away" {
			t.Fatalf("text %q", m.Text)
		}
		if m := data[apiMessage](t, b2.wait(t, isMsg)); m.Text != "news while away" {
			t.Fatalf("text %q", m.Text)
		}
		b2.none(t, isMsg, 300*time.Millisecond)
		live := parse[sendResponse](t, jsonOf(x.mac1.send(sid, "alice", "bob", "live", "")))
		if !live.Online {
			t.Fatal("online false, want true")
		}
		if m := data[apiMessage](t, b2.wait(t, isMsg)); m.Text != "live" {
			t.Fatalf("text %q", m.Text)
		}
	})

	t.Run("the replay stops at the newest 100 missed messages", func(t *testing.T) {
		// The TS test has a time limit of 20 s.
		start := time.Now()
		t.Cleanup(func() {
			if took := time.Since(start); took > 20*time.Second {
				t.Errorf("the test took %v; the limit is 20 s", took)
			}
		})
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		b := x.vps2.stream(sid, "bob")
		defer b.close()
		b.wait(t, nil)
		b.close()
		a.wait(t, notice)
		for i := 1; i <= 120; i++ {
			x.mac1.send(sid, "alice", "bob", fmt.Sprintf("m%d", i), "")
		}
		b2 := x.vps2.stream(sid, "bob")
		defer b2.close()
		b2.wait(t, eventIs("joined"))
		if m := data[apiMessage](t, b2.wait(t, isMsg)); m.Text != "m21" {
			t.Fatalf("text %q", m.Text)
		}
		n := 1
		for {
			if _, err := b2.next(isMsg, 300*time.Millisecond); err != nil {
				break
			}
			n++
		}
		if n != 100 {
			t.Fatalf("%d messages, want 100", n)
		}
	})

	t.Run("after a hub restart, the hub still knows who left", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		b := x.vps2.stream(sid, "bob")
		defer b.close()
		b.wait(t, nil)
		b.close()
		a.wait(t, notice)
		a.close()
		x.h.restart()
		a2 := x.mac1.stream(sid, "alice")
		defer a2.close()
		a2.wait(t, nil)
		body := wantStatus(t, 200)(x.mac1.send(sid, "alice", "bob", "after restart", ""))
		if parse[sendResponse](t, body).Online {
			t.Fatal("online true, want false")
		}
		b2 := x.vps2.stream(sid, "bob")
		defer b2.close()
		b2.wait(t, eventIs("joined"))
		if m := data[apiMessage](t, b2.wait(t, isMsg)); m.Text != "after restart" {
			t.Fatalf("text %q", m.Text)
		}
	})
}

func (x *hubSuite) activityAndPresence(t *testing.T) {
	t.Run("state, waits and deliveries show in the view, presence, and the stream", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		b := x.vps2.stream(sid, "bob")
		defer b.close()
		a.wait(t, nil)
		b.wait(t, nil)
		wantStatus(t, 204)(x.vps2.activity(sid, map[string]any{
			"kind":  "state",
			"agent": "bob",
			"state": "blocked",
			"note":  "need API",
		}))
		x.vps2.activity(sid, map[string]any{"kind": "wait_start", "agent": "bob", "from": "alice", "timeout_s": 60})
		v := parse[sessionView](t, jsonOf(x.mac1.view(sid, "alice")))
		want := []peer{{
			Name:      "bob@vps-2",
			State:     "blocked",
			Note:      "need API",
			Online:    true,
			WaitingOn: "alice@mac-1",
		}}
		if !slices.Equal(v.Peers, want) {
			t.Fatalf("peers %+v, want %+v", v.Peers, want)
		}
		wantStatus(t, 200)(x.mac1.send(sid, "alice", "bob", "answer", ""))
		b.wait(t, isMsg)
		x.vps2.activity(sid, map[string]any{"kind": "wait_end", "agent": "bob", "result": "message"})
		rec, ok := x.h.presence(sid + ".vps-2.bob")
		if !ok || rec.State != "blocked" {
			t.Fatalf("presence %v %+v, want state blocked", ok, rec)
		}
		if rec.Waiting != nil {
			t.Fatalf("waiting %+v, want none", rec.Waiting)
		}
		var kinds []string
		for _, e := range x.h.events(sid) {
			if e.Kind == wire.EventActivity && e.Activity != nil {
				kinds = append(kinds, e.Activity.Kind)
			}
		}
		for _, k := range []string{"joined", "state", "wait_start", "wait_end"} {
			if !slices.Contains(kinds, k) {
				t.Fatalf("activity kinds %v have no %s", kinds, k)
			}
		}
	})

	t.Run("a wait on the operator is recorded and shown to the peers", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		b := x.vps2.stream(sid, "bob")
		defer b.close()
		b.wait(t, nil)
		wantStatus(t, 204)(x.mac1.activity(sid, map[string]any{
			"kind":      "wait_start",
			"agent":     "alice",
			"from":      "operator",
			"reply_to":  "1",
			"timeout_s": 60,
		}))
		v := parse[sessionView](t, jsonOf(x.vps2.view(sid, "bob")))
		if want := []peer{{Name: "alice@mac-1", State: "idle", Online: true, WaitingOn: "operator"}}; !slices.Equal(v.Peers, want) {
			t.Fatalf("peers %+v, want %+v", v.Peers, want)
		}
		rec, ok := x.h.presence(sid + ".mac-1.alice")
		if !ok || rec.Waiting == nil || rec.Waiting.On != "operator" || rec.Waiting.ReplyTo != "1" {
			t.Fatalf("presence %v %+v, want waiting on operator with reply_to 1", ok, rec)
		}
	})

	t.Run("closing a stream removes presence and records left", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		a.close()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, ok := x.h.presence(sid + ".mac-1.alice"); !ok {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("presence not removed")
			}
			time.Sleep(50 * time.Millisecond)
		}
		left := slices.ContainsFunc(x.h.events(sid), func(e wire.Event) bool {
			return e.Kind == wire.EventActivity && e.Activity != nil && e.Activity.Kind == "left"
		})
		if !left {
			t.Fatal("no left event")
		}
	})
}

func (x *hubSuite) autoCreate(t *testing.T) {
	t.Run("with the option on, the first join creates an open session; off, it is 404", func(t *testing.T) {
		auto := startHub(t, limits{}, options{autoCreate: true})
		op := operator{admin{auto.base, auto.operatorToken("op")}}
		mac := api{auto.base, auto.token("mac-1")}
		s := mac.stream("fresh", "alice")
		defer s.close()
		wantStatus(t, 200)(s.status(), s.body)
		got := data[joinedEvent](t, s.wait(t, nil))
		if want := (joinedEvent{Me: "alice@mac-1", Session: "fresh", Gate: "run"}); got != want {
			t.Fatalf("joined %+v, want %+v", got, want)
		}
		if rec, ok := op.getSession(t, "fresh"); !ok || rec.Status != "open" {
			t.Fatalf("session fresh %v %+v, want status open", ok, rec)
		}
		if ids := sessionIDs(op.listSessions(t)); !slices.Equal(ids, []string{"fresh"}) {
			t.Fatalf("sessions %v, want [fresh]", ids)
		}
		// A closed session stays closed: auto-create never reopens.
		s.close()
		if err := s.ended(0); err != nil {
			t.Fatal(err)
		}
		op.closeSession(t, "fresh")
		wantStatus(t, 403)(mac.stream("fresh", "alice").result())
		auto.stop()
		wantStatus(t, 404)(x.mac1.stream("fresh", "alice").result())
	})
}

func (x *hubSuite) adminAPI(t *testing.T) {
	adm := admin{x.h.base, x.h.operatorToken("matt")}

	t.Run("a machine token is refused; a bad token is 401", func(t *testing.T) {
		// mac1's own token: a new h.token("mac-1") would replace it and log mac1 out.
		body := wantStatus(t, 403)(admin{x.h.base, x.mac1.token}.sessions())
		if m := parse[errBody](t, body).Message; m != "a machine token cannot use the admin API" {
			t.Fatalf("message %q", m)
		}
		wantStatus(t, 401)(admin{x.h.base, "nope"}.sessions())
	})

	t.Run("session lifecycle: create, list, close, reopen, delete", func(t *testing.T) {
		x.n++
		sid := fmt.Sprintf("adm%d", x.n)
		created := parse[wire.SessionInfo](t, wantStatus(t, 200)(adm.create(sid, "Admin test")))
		if created.Session != sid || created.Status != "open" || created.Title != "Admin test" {
			t.Fatalf("created %+v, want session %s, status open, title Admin test", created, sid)
		}
		wantStatus(t, 409)(adm.create(sid, ""))
		list := parse[sessionList](t, jsonOf(adm.sessions()))
		if ids := sessionIDs(list.Sessions); !slices.Contains(ids, sid) {
			t.Fatalf("sessions %v have no %s", ids, sid)
		}
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		wantStatus(t, 409)(adm.delete(sid))
		wantStatus(t, 204)(adm.close(sid))
		if n := data[noticeEvent](t, a.wait(t, eventIs("notice"))); n.Kind != "closed" {
			t.Fatalf("notice %+v", n)
		}
		wantStatus(t, 204)(adm.reopen(sid))
		if n := data[noticeEvent](t, a.wait(t, eventIs("notice"))); n.Kind != "reopened" {
			t.Fatalf("notice %+v", n)
		}
		a.close()
		wantStatus(t, 204)(adm.close(sid))
		wantStatus(t, 204)(adm.delete(sid))
		after := parse[sessionList](t, jsonOf(adm.sessions()))
		if ids := sessionIDs(after.Sessions); slices.Contains(ids, sid) {
			t.Fatalf("sessions %v have %s", ids, sid)
		}
		wantStatus(t, 404)(adm.close("nosuch"))
		wantStatus(t, 404)(adm.delete("nosuch"))
	})

	t.Run("kick and unkick reach the agent and the kick record", func(t *testing.T) {
		sid := x.session(t)
		b := x.vps2.stream(sid, "bob")
		defer b.close()
		b.wait(t, nil)
		wantStatus(t, 204)(adm.kick(sid, "bob@vps-2"))
		if n := data[noticeEvent](t, b.wait(t, eventIs("notice"))); n.Kind != "kicked" {
			t.Fatalf("notice %+v", n)
		}
		if err := b.ended(0); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, 403)(x.vps2.stream(sid, "bob").result())
		wantStatus(t, 204)(adm.unkick(sid, "bob@vps-2"))
		again := x.vps2.stream(sid, "bob")
		defer again.close()
		wantStatus(t, 200)(again.status(), again.body)
		again.close()
		wantStatus(t, 404)(adm.kick("nosuch", "bob@vps-2"))
	})

	// Found in use: an agent the operator removed stayed a peer for the others, listed as away
	// and accepted as a recipient, although it could not come back.
	t.Run("a removed agent is no longer a peer; allow brings it back", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		b := x.vps2.stream(sid, "bob")
		b.wait(t, nil)
		b.close()
		a.wait(t, eventIs("notice"))
		away := []peer{{Name: "bob@vps-2", State: "idle", Online: false}}
		if v := parse[sessionView](t, jsonOf(x.mac1.view(sid, "alice"))); !slices.Equal(v.Peers, away) {
			t.Fatalf("peers before the kick %+v, want %+v", v.Peers, away)
		}
		wantStatus(t, 204)(adm.kick(sid, "bob@vps-2"))
		if v := parse[sessionView](t, jsonOf(x.mac1.view(sid, "alice"))); len(v.Peers) != 0 {
			t.Fatalf("peers after the kick %+v, want none", v.Peers)
		}
		wantStatus(t, 404)(x.mac1.send(sid, "alice", "bob", "hi", ""))
		wantStatus(t, 204)(adm.unkick(sid, "bob@vps-2"))
		if v := parse[sessionView](t, jsonOf(x.mac1.view(sid, "alice"))); !slices.Equal(v.Peers, away) {
			t.Fatalf("peers after the allow %+v, want %+v", v.Peers, away)
		}
	})

	// Found in use: three agents of a test run each joined for some seconds, and stayed peers
	// of the session for ever (smoke-researcher, smoke2, eng-t1). The only way to drop one was
	// a kick, and a kick also keeps the name out. Forget drops the agent and keeps the name free.
	t.Run("a forgotten agent is no longer a peer; it may join again as a new agent", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		b := x.vps2.stream(sid, "smoke2")
		b.wait(t, nil)
		// An agent that is in the session cannot be forgotten.
		wantStatus(t, 409)(adm.forget(sid, "smoke2@vps-2"))
		b.close()
		a.wait(t, eventIs("notice"))
		wantStatus(t, 200)(x.mac1.send(sid, "alice", "smoke2", "for later", ""))
		if v := parse[sessionView](t, jsonOf(x.mac1.view(sid, "alice"))); len(v.Peers) != 1 {
			t.Fatalf("peers before the forget %+v, want smoke2 away", v.Peers)
		}
		forgotten := func() int {
			n := 0
			for _, e := range x.h.events(sid) {
				if e.Kind == wire.EventActivity && e.From == "smoke2@vps-2" && e.Activity != nil && e.Activity.Kind == "forgotten" {
					n++
				}
			}
			return n
		}
		wantStatus(t, 204)(adm.forget(sid, "smoke2@vps-2"))
		if v := parse[sessionView](t, jsonOf(x.mac1.view(sid, "alice"))); len(v.Peers) != 0 {
			t.Fatalf("peers after the forget %+v, want none", v.Peers)
		}
		wantStatus(t, 404)(x.mac1.send(sid, "alice", "smoke2", "hi", ""))
		// A second forget, and a forget of an agent that never joined, change nothing.
		wantStatus(t, 204)(adm.forget(sid, "smoke2@vps-2"))
		wantStatus(t, 204)(adm.forget(sid, "nobody@vps-2"))
		if n := forgotten(); n != 1 {
			t.Fatalf("%d forgotten records, want 1", n)
		}
		// The name is free: the agent joins again, as a new agent with no replay.
		b2 := x.vps2.stream(sid, "smoke2")
		defer b2.close()
		b2.wait(t, nil)
		b2.none(t, isMsg, 300*time.Millisecond)
		if v := parse[sessionView](t, jsonOf(x.mac1.view(sid, "alice"))); len(v.Peers) != 1 || !v.Peers[0].Online {
			t.Fatalf("peers after the new join %+v, want smoke2 online", v.Peers)
		}
		wantStatus(t, 404)(adm.forget("no-such-session", "smoke2@vps-2"))
		wantStatus(t, 422)(adm.forget(sid, "smoke2"))
	})

	// Asked for by the operator: a removed agent that tries to join must show. Without a record
	// a forgotten :allow looks like an agent that never started.
	t.Run("a removed agent that tries to join leaves a refused record; a repeat soon after leaves none", func(t *testing.T) {
		sid := x.session(t)
		wantStatus(t, 204)(adm.kick(sid, "bob@vps-2"))
		refused := func() int {
			n := 0
			for _, e := range x.h.events(sid) {
				if e.Kind == wire.EventActivity && e.From == "bob@vps-2" && e.Activity != nil && e.Activity.Kind == "refused" && e.Activity.Reason == "removed" {
					n++
				}
			}
			return n
		}
		wantStatus(t, 403)(x.vps2.stream(sid, "bob").result())
		if n := refused(); n != 1 {
			t.Fatalf("%d refused records after the first try, want 1", n)
		}
		wantStatus(t, 403)(x.vps2.stream(sid, "bob").result())
		if n := refused(); n != 1 {
			t.Fatalf("%d refused records after a second try soon after, want 1", n)
		}
	})

	t.Run("the operator sends, in a thread, and redacts", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		b := x.vps2.stream(sid, "bob")
		defer b.close()
		a.wait(t, nil)
		b.wait(t, nil)
		q := parse[sendResponse](t, jsonOf(x.mac1.send(sid, "alice", "operator", "may I?", "")))
		body := wantStatus(t, 200)(adm.send(sid, "alice", "yes", q.ID))
		if to := parse[operatorSendResponse](t, body).To; to != "alice@mac-1" {
			t.Fatalf("to %q", to)
		}
		m := data[apiMessage](t, a.wait(t, isMsg))
		if m.From != "operator" || m.Text != "yes" || m.ReplyTo != q.ID {
			t.Fatalf("message %+v, want from operator, text yes, reply_to %s", m, q.ID)
		}
		b.none(t, isMsg, 300*time.Millisecond)
		wantStatus(t, 200)(adm.send(sid, "all", "everyone: pause", ""))
		if m := data[apiMessage](t, b.wait(t, isMsg)); m.Text != "everyone: pause" {
			t.Fatalf("text %q", m.Text)
		}
		wantStatus(t, 404)(adm.send(sid, "zed", "hi", ""))
		id := parse[sendResponse](t, jsonOf(x.vps2.send(sid, "bob", "alice", "oops", ""))).ID
		a.wait(t, isMsg)
		wantStatus(t, 204)(adm.redact(sid, id))
		if n := data[noticeEvent](t, a.wait(t, eventIs("notice"))); n.Kind != "redacted" || n.ID != id {
			t.Fatalf("notice %+v, want kind redacted and id %s", n, id)
		}
		wantStatus(t, 404)(adm.redact(sid, "999999"))
	})
}

func (x *hubSuite) adminFeed(t *testing.T) {
	adm := admin{x.h.base, x.h.operatorToken("viewer")}

	t.Run("a machine token is refused", func(t *testing.T) {
		wantStatus(t, 403)(admin{x.h.base, x.mac1.token}.stream().result())
	})

	t.Run("the current buckets come first, each closed by a snapshot marker", func(t *testing.T) {
		sid := x.session(t)
		s := adm.stream()
		defer s.close()
		wantStatus(t, 200)(s.status(), s.body)
		rec := data[adminEvent](t, s.wait(t, func(e sseEvent) bool {
			return e.event == "session" && strings.Contains(e.data, `"`+sid+`"`)
		}))
		if rec.Kind != "session" || rec.Session != sid || rec.SessionRecord == nil || rec.SessionRecord.Status != "open" {
			t.Fatalf("session event %+v, want session %s with status open", rec, sid)
		}
		markers := []string{
			data[adminEvent](t, s.wait(t, eventIs("snapshot"))).Bucket,
			data[adminEvent](t, s.wait(t, eventIs("snapshot"))).Bucket,
		}
		slices.Sort(markers)
		if !slices.Equal(markers, []string{"presence", "sessions"}) {
			t.Fatalf("snapshot markers %v, want [presence sessions]", markers)
		}
	})

	t.Run("a join and a message arrive as presence and as decodable events with ids", func(t *testing.T) {
		sid := x.session(t)
		s := adm.stream()
		defer s.close()
		s.wait(t, eventIs("snapshot"))
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		p := data[adminEvent](t, s.wait(t, func(e sseEvent) bool {
			return e.event == "presence" && strings.Contains(e.data, sid+".mac-1.alice")
		}))
		if p.Kind != "presence" || p.PresenceRecord == nil || p.PresenceRecord.State != "idle" || p.PresenceRecord.Host != "test-host" {
			t.Fatalf("presence event %+v, want state idle and host test-host", p)
		}
		joined := data[adminEvent](t, s.wait(t, func(e sseEvent) bool {
			return e.event == "event" && strings.Contains(e.data, "coop."+sid+".evt.mac-1.alice")
		}))
		if joined.Kind != "event" {
			t.Fatal("not an event")
		}
		decoded, ok := wire.DecodeEvent(joined.Subject, []byte(joined.Payload), joined.Seq)
		if !ok || decoded.Kind != wire.EventActivity || decoded.SID != sid || decoded.Activity == nil || decoded.Activity.Kind != "joined" {
			t.Fatalf("decoded %v %+v, want a joined activity of session %s", ok, decoded, sid)
		}
		id := parse[sendResponse](t, jsonOf(x.mac1.send(sid, "alice", "all", "to the log", ""))).ID
		m := s.wait(t, func(e sseEvent) bool { return e.event == "event" && strings.Contains(e.data, "to the log") })
		if m.id != id {
			t.Fatalf("event id %q, want %q", m.id, id)
		}
	})

	t.Run("a kick record appears, and goes away with unkick", func(t *testing.T) {
		sid := x.session(t)
		s := adm.stream()
		defer s.close()
		// Both snapshots must be done: a listing that runs during the kick would send it once more.
		s.wait(t, eventIs("snapshot"))
		s.wait(t, eventIs("snapshot"))
		key := sid + ".kick.mac-1.alice"
		myKick := func(e sseEvent) bool { return e.event == "kick" && strings.Contains(e.data, key) }
		adm.kick(sid, "alice@mac-1")
		if k := data[adminEvent](t, s.wait(t, myKick)); k.Kind != "kick" || k.Key != key || k.KickRecord == nil {
			t.Fatalf("kick event %+v, want key %s with a record", k, key)
		}
		adm.unkick(sid, "alice@mac-1")
		if k := data[adminEvent](t, s.wait(t, myKick)); k.Kind != "kick" || k.KickRecord != nil {
			t.Fatalf("kick event %+v, want a null record", k)
		}
	})

	t.Run("Last-Event-ID resumes after that event; the buckets come again", func(t *testing.T) {
		sid := x.session(t)
		id := x.op.send(t, sid, "all", "before")
		later := x.op.send(t, sid, "all", "after")
		s := adm.stream(id)
		defer s.close()
		// The first event is the one right after `id`: nothing older is sent again.
		first := s.wait(t, eventIs("event"))
		if first.id != later {
			t.Fatalf("first event id %q, want %q", first.id, later)
		}
		parsed := data[adminEvent](t, first)
		if parsed.Kind != "event" {
			t.Fatal("not an event")
		}
		if !strings.Contains(parsed.Payload, `"after"`) {
			t.Fatalf("payload %s has no \"after\"", parsed.Payload)
		}
		s.wait(t, eventIs("snapshot"))
	})

	t.Run("revoking the operator token ends the feed", func(t *testing.T) {
		temp := admin{x.h.base, x.h.operatorToken("temp-op")}
		s := temp.stream()
		defer s.close()
		s.wait(t, eventIs("snapshot"))
		x.h.revoke("temp-op")
		if err := s.ended(0); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, 401)(temp.stream().result())
	})
}

func (x *hubSuite) contractDocument(t *testing.T) {
	// The TS name starts with "/"; a "/" in a Go subtest name adds a level, so it is left out.
	t.Run("openapi.json lists every route", func(t *testing.T) {
		doc := parse[openAPIDoc](t, jsonOf(fetch(x.h.base+"/openapi.json")))
		want := []string{
			"/v1/admin/sessions",
			"/v1/admin/sessions/{sid}",
			"/v1/admin/sessions/{sid}/close",
			"/v1/admin/sessions/{sid}/forget",
			"/v1/admin/sessions/{sid}/gate",
			"/v1/admin/sessions/{sid}/hold",
			"/v1/admin/sessions/{sid}/kick",
			"/v1/admin/sessions/{sid}/messages",
			"/v1/admin/sessions/{sid}/redact",
			"/v1/admin/sessions/{sid}/reopen",
			"/v1/admin/sessions/{sid}/unkick",
			"/v1/admin/stream",
			"/v1/sessions/{sid}",
			"/v1/sessions/{sid}/activity",
			"/v1/sessions/{sid}/gate",
			"/v1/sessions/{sid}/messages",
			"/v1/sessions/{sid}/stream",
			"/v1/sessions/{sid}/trace",
			"/v1/whoami",
		}
		if got := slices.Sorted(maps.Keys(doc.Paths)); !slices.Equal(got, want) {
			t.Fatalf("paths %v, want %v", got, want)
		}
	})
}
