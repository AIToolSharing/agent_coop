package api_test

// The behaviour of the hub, ported one test to one test from packages/hub/test/hub.int.test.ts.
// Each describe block of that file is a subtest of TestHub; the tests run in the order of that
// file, one at a time, against one hub.

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// hubSuite is what the tests share: one hub, three machines and the session counter.
type hubSuite struct {
	h                *harness
	mac1, vps2, mac3 api
	n                int
}

// session gives a fresh session name per test, so tests do not see each other's traffic. The
// first join creates the session.
func (x *hubSuite) session(t *testing.T) string {
	t.Helper()
	x.n++
	return fmt.Sprintf("s%d", x.n)
}

// writer joins sid as w@mac-3, which creates the session. It gives a function that sends a
// message to all as w and gives the id of the message.
func (x *hubSuite) writer(t *testing.T, sid string) func(text string) string {
	t.Helper()
	w := x.mac3.stream(sid, "w")
	t.Cleanup(w.close)
	w.wait(t, nil)
	return func(text string) string {
		t.Helper()
		return parse[sendResponse](t, wantStatus(t, 200)(x.mac3.send(sid, "w", "all", text, ""))).ID
	}
}

func TestHub(t *testing.T) {
	// High limits here; the rate limit test below uses its own hub with low limits.
	high := limit{burst: 10_000, perSecond: 10_000}
	h := startHub(t, limits{join: high, msg: high, activity: high}, options{})
	x := &hubSuite{
		h:    h,
		mac1: api{h.base, h.token("mac-1")},
		vps2: api{h.base, h.token("vps-2")},
		mac3: api{h.base, h.token("mac-3")},
	}
	t.Run("authentication", x.authentication)
	t.Run("joining", x.joining)
	t.Run("messages", x.messages)
	t.Run("rate limits", rateLimits)
	t.Run("resume", x.resume)
	t.Run("operator is an agent name like any other", x.operatorIsAName)
	t.Run("peer presence", x.peerPresence)
	t.Run("peers that left", x.peersThatLeft)
	t.Run("activity and presence", x.activityAndPresence)
	t.Run("the first join creates the session", x.autoCreate)
	t.Run("admin API", x.adminAPI)
	t.Run("admin feed", x.adminFeed)
}

func (x *hubSuite) authentication(t *testing.T) {
	t.Run("a request without a valid token gets 401", func(t *testing.T) {
		sid := x.session(t)
		wantStatus(t, 401)(api{x.h.base, "nope"}.view(sid, "a"))
		wantStatus(t, 401)(api{x.h.base, "mac-1.wrong-secret"}.view(sid, "a"))
		wantStatus(t, 401)(fetch(x.h.base + "/v1/sessions/" + sid))
	})

	// The person who watches is one more machine to the agents: a token that may watch also
	// acts as an agent, and whoami calls each token a machine token.
	t.Run("a watch token also acts as an agent; whoami calls it a machine token", func(t *testing.T) {
		sid := x.session(t)
		w := api{x.h.base, x.h.watchToken("matt")}
		s := w.stream(sid, "a")
		defer s.close()
		wantStatus(t, 200)(s.status(), s.body)
		s.wait(t, nil)
		wantStatus(t, 200)(w.view(sid, "a"))
		for _, tok := range []string{w.token, x.mac1.token} {
			status, _, body := get(t, x.h.base+"/v1/whoami", tok)
			if status != 200 || !strings.Contains(string(body), `"role":"machine"`) {
				t.Fatalf("whoami: %d %s", status, body)
			}
		}
		roles := x.h.tokenRoles()
		if roles["matt"] != "watch" {
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
		if want := (joinedEvent{Me: "alice@mac-1", Session: sid}); got != want {
			t.Fatalf("joined %+v, want %+v", got, want)
		}
		if _, ok := x.h.presence(sid + ".mac-1.alice"); !ok {
			t.Fatal("no presence record")
		}
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
	// show.
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

	// A shim of v0.10.0 or before joins with gated=1. The hub of now has no gate; the join is
	// accepted, so that a device that did not upgrade yet keeps its messages.
	t.Run("a join with the gated parameter of an older shim is accepted", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice", withQuery("gated", "1"))
		defer a.close()
		wantStatus(t, 200)(a.status(), a.body)
		wantStatus(t, 422)(x.mac1.stream(sid, "bob", withQuery("hold", "1")).result())
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
		// Found by Schemathesis: a schema-valid peer that is the caller is a conflict, not bad input.
		wantStatus(t, 409)(x.mac1.history(sid, "alice", "&with=alice%40mac-1"))
		wantStatus(t, 200)(x.mac1.send(sid, "alice", "x@mac-3", "hi", ""))
		if m := data[apiMessage](t, x3.wait(t, isMsg)); m.From != "alice@mac-1" {
			t.Fatalf("from %q", m.From)
		}
	})

	// Added with v0.3.0: the sender learns the recipient's state and can decide to wait.
	t.Run("send tells the recipient's state: live state, away, or none for all", func(t *testing.T) {
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
		r = parse[sendResponse](t, wantStatus(t, 200)(x.mac1.send(sid, "alice", "all", "three", "")))
		if !r.Online || r.State != "" {
			t.Fatalf("send to all: %+v, want online true and no state", r)
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

func (x *hubSuite) operatorIsAName(t *testing.T) {
	// The person who watches has no name in a session: "operator" names no one special. With no
	// agent of that name, a send to it and a wait on it find no peer.
	t.Run("a send to operator and a wait on it find no peer: 404", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		wantStatus(t, 404)(x.mac1.send(sid, "alice", "operator", "hi", ""))
		wantStatus(t, 404)(x.mac1.activity(sid, map[string]any{
			"kind":      "wait_start",
			"agent":     "alice",
			"from":      "operator",
			"reply_to":  "1",
			"timeout_s": 60,
		}))
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
		// Found in use: agents that left stayed peers for ever ("ghosts"). An agent that left
		// is no peer; the hub still keeps the messages for it.
		if v := parse[sessionView](t, jsonOf(x.mac1.view(sid, "alice"))); len(v.Peers) != 0 {
			t.Fatalf("peers %+v, want none", v.Peers)
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

	// An agent that left must not make a name ambiguous, and must not show in the list of an
	// error.
	t.Run("a bare name goes to the agent in the session, not to one that left", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		old := x.vps2.stream(sid, "bob")
		old.wait(t, nil)
		old.close()
		a.wait(t, notice)
		// Only the bob that left: the message is kept for him.
		if r := parse[sendResponse](t, wantStatus(t, 200)(x.mac1.send(sid, "alice", "bob", "for later", ""))); r.To != "bob@vps-2" || r.Online {
			t.Fatalf("send %+v, want to bob@vps-2 and online false", r)
		}
		b := x.mac3.stream(sid, "bob")
		defer b.close()
		b.wait(t, nil)
		if r := parse[sendResponse](t, wantStatus(t, 200)(x.mac1.send(sid, "alice", "bob", "now", ""))); r.To != "bob@mac-3" || !r.Online {
			t.Fatalf("send %+v, want to bob@mac-3 and online true", r)
		}
		if m := parse[errBody](t, wantStatus(t, 404)(x.mac1.send(sid, "alice", "zed", "hi", ""))).Message; m != "no peer zed in this session; peers: bob@mac-3" {
			t.Fatalf("message %q", m)
		}
		// A full address still reaches the agent that left.
		wantStatus(t, 200)(x.mac1.send(sid, "alice", "bob@vps-2", "full", ""))
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

	t.Run("closing a stream removes presence and records left", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		a.close()
		waitGone(t, x.h, sid+".mac-1.alice")
		left := slices.ContainsFunc(x.h.events(sid), func(e wire.Event) bool {
			return e.Kind == wire.EventActivity && e.Activity != nil && e.Activity.Kind == "left"
		})
		if !left {
			t.Fatal("no left event")
		}
	})
}

func (x *hubSuite) autoCreate(t *testing.T) {
	t.Run("a join of an unknown session creates it; the operator sees it", func(t *testing.T) {
		auto := startHub(t, limits{}, options{})
		op := operator{admin{auto.base, auto.watchToken("op")}}
		mac := api{auto.base, auto.token("mac-1")}
		s := mac.stream("fresh", "alice")
		defer s.close()
		wantStatus(t, 200)(s.status(), s.body)
		got := data[joinedEvent](t, s.wait(t, nil))
		if want := (joinedEvent{Me: "alice@mac-1", Session: "fresh"}); got != want {
			t.Fatalf("joined %+v, want %+v", got, want)
		}
		if rec, ok := op.getSession(t, "fresh"); !ok || rec.CreatedAt == "" {
			t.Fatalf("session fresh %v %+v, want a created_at", ok, rec)
		}
		if ids := sessionIDs(op.listSessions(t)); !slices.Equal(ids, []string{"fresh"}) {
			t.Fatalf("sessions %v, want [fresh]", ids)
		}
	})
}

func (x *hubSuite) adminAPI(t *testing.T) {
	adm := admin{x.h.base, x.h.watchToken("matt")}

	// To each token that may not watch, the admin routes do not exist: no 403 or 401 tells
	// that they are there.
	t.Run("for a machine token, a bad token and no token, the admin routes are not there: 404", func(t *testing.T) {
		// mac1's own token: a new h.token("mac-1") would replace it and log mac1 out.
		for _, tok := range []string{x.mac1.token, "nope", ""} {
			body := wantStatus(t, 404)(admin{x.h.base, tok}.sessions())
			if m := parse[errBody](t, body).Message; m != "no such route" {
				t.Fatalf("token %q: message %q", tok, m)
			}
		}
	})

	t.Run("session lifecycle: a join creates, list, delete", func(t *testing.T) {
		sid := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		list := parse[sessionList](t, jsonOf(adm.sessions()))
		if ids := sessionIDs(list.Sessions); !slices.Contains(ids, sid) {
			t.Fatalf("sessions %v have no %s", ids, sid)
		}
		// A session with an agent in it is not deleted: the agent would create it again.
		if m := parse[errBody](t, wantStatus(t, 409)(adm.delete(sid))).Message; !strings.Contains(m, "alice@mac-1") {
			t.Fatalf("message %q", m)
		}
		a.close()
		waitGone(t, x.h, sid+".mac-1.alice")
		wantStatus(t, 204)(adm.delete(sid))
		after := parse[sessionList](t, jsonOf(adm.sessions()))
		if ids := sessionIDs(after.Sessions); slices.Contains(ids, sid) {
			t.Fatalf("sessions %v have %s", ids, sid)
		}
		wantStatus(t, 404)(adm.delete("nosuch"))
	})

}

func (x *hubSuite) adminFeed(t *testing.T) {
	adm := admin{x.h.base, x.h.watchToken("viewer")}

	t.Run("for a machine token, the feed is not there", func(t *testing.T) {
		wantStatus(t, 404)(admin{x.h.base, x.mac1.token}.stream().result())
	})

	t.Run("the current buckets come first, each closed by a snapshot marker", func(t *testing.T) {
		sid := x.session(t)
		x.writer(t, sid)
		s := adm.stream()
		defer s.close()
		wantStatus(t, 200)(s.status(), s.body)
		rec := data[adminEvent](t, s.wait(t, func(e sseEvent) bool {
			return e.event == "session" && strings.Contains(e.data, `"`+sid+`"`)
		}))
		if rec.Kind != "session" || rec.Session != sid || rec.SessionRecord == nil || rec.SessionRecord.CreatedAt == "" {
			t.Fatalf("session event %+v, want session %s with a created_at", rec, sid)
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

	// The database of a hub of v0.10.0 or before holds kinds that this version dropped. The
	// feed must not send them: the TUI cannot read them.
	t.Run("a row of a dropped kind in the log does not reach the feed", func(t *testing.T) {
		sid := x.session(t)
		say := x.writer(t, sid)
		before := say("before")
		x.h.appendRaw(sid, "coop."+sid+".ops", `{"kind":"kick","target":"bob@vps-2","at":"2026-09-30T12:01:00.000Z"}`)
		after := say("after")
		s := adm.stream(before)
		defer s.close()
		if e := s.wait(t, eventIs("event")); e.id != after {
			t.Fatalf("first event %q (%s), want %q", e.id, e.data, after)
		}
	})

	t.Run("Last-Event-ID resumes after that event; the buckets come again", func(t *testing.T) {
		sid := x.session(t)
		say := x.writer(t, sid)
		id := say("before")
		later := say("after")
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

	t.Run("revoking the watch token ends the feed; the feed is then not there", func(t *testing.T) {
		temp := admin{x.h.base, x.h.watchToken("temp-op")}
		s := temp.stream()
		defer s.close()
		s.wait(t, eventIs("snapshot"))
		x.h.revoke("temp-op")
		if err := s.ended(0); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, 404)(temp.stream().result())
	})
}

// waitGone waits until the hub has no live presence record of key.
func waitGone(t *testing.T, h *harness, key string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := h.presence(key); !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("presence %s not removed", key)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
