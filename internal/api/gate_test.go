package api_test

import (
	"testing"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// The operator's complaint: an agent starts work the moment it is launched, and nothing but a
// message or a removal can stop it. The gate is the hub's answer to "may I work?", which the
// agent's hook asks before each tool call.
func TestGate(t *testing.T) {
	high := limit{burst: 10_000, perSecond: 10_000}
	h := startHub(t, limits{join: high, msg: high, activity: high}, options{autoCreate: true, holdNew: true})
	mac := api{base: h.base, token: h.token("mac-1")}
	adm := admin{h.base, h.operatorToken("op")}
	gateOf := func(t *testing.T, sid, agent string) string {
		t.Helper()
		return parse[struct {
			Gate string `json:"gate"`
		}](t, wantStatus(t, 200)(mac.gate(sid, agent))).Gate
	}
	// gates lists the gate records of an agent, oldest first.
	gates := func(sid, agent string) []string {
		var out []string
		for _, e := range h.events(sid) {
			if e.Kind == wire.EventActivity && e.From == agent && e.Activity.Kind == "gate" {
				out = append(out, e.Activity.Gate)
			}
		}
		return out
	}
	isNotice := eventIs("notice")

	t.Run("a new agent is held before and after its first join, until the operator releases it", func(t *testing.T) {
		sid := "hold-1"
		// Before the join, and before the session exists: a tool call must not get through.
		if g := gateOf(t, sid, "alice"); g != "held" {
			t.Fatalf("gate before the join %q, want held", g)
		}
		a := mac.stream(sid, "alice")
		defer a.close()
		if j := data[joinedEvent](t, a.wait(t, nil)); j.Gate != "held" {
			t.Fatalf("joined %+v, want gate held", j)
		}
		if g := gateOf(t, sid, "alice"); g != "held" {
			t.Fatalf("gate after the join %q, want held", g)
		}
		if got := gates(sid, "alice@mac-1"); len(got) != 1 || got[0] != "held" {
			t.Fatalf("gate records %v, want [held]", got)
		}
		// The joined event told the agent; it gets no notice for the hold at the join.
		a.none(t, isNotice, 300*time.Millisecond)
		// A held agent can still talk: it must be able to ask the operator.
		wantStatus(t, 200)(mac.send(sid, "alice", "operator", "what is my task?", ""))

		wantStatus(t, 204)(adm.gate(sid, "alice@mac-1", "run"))
		if n := data[noticeEvent](t, a.wait(t, isNotice)); n.Kind != "released" {
			t.Fatalf("notice %+v, want released", n)
		}
		if g := gateOf(t, sid, "alice"); g != "run" {
			t.Fatalf("gate after the release %q, want run", g)
		}
	})

	t.Run("pause and resume; a repeat leaves no record; a new process keeps the gate", func(t *testing.T) {
		sid := "pause-1"
		a := mac.stream(sid, "alice")
		a.wait(t, nil)
		wantStatus(t, 204)(adm.gate(sid, "alice@mac-1", "run"))
		a.wait(t, isNotice)
		wantStatus(t, 204)(adm.gate(sid, "alice@mac-1", "paused"))
		if n := data[noticeEvent](t, a.wait(t, isNotice)); n.Kind != "paused" {
			t.Fatalf("notice %+v, want paused", n)
		}
		wantStatus(t, 204)(adm.gate(sid, "alice@mac-1", "paused"))
		if got := gates(sid, "alice@mac-1"); len(got) != 3 {
			t.Fatalf("gate records %v, want [held run paused]", got)
		}
		// The agent's process ends and a new one starts: it is still paused, and the join
		// writes no new hold.
		a.close()
		if err := a.ended(0); err != nil {
			t.Fatal(err)
		}
		if g := gateOf(t, sid, "alice"); g != "paused" {
			t.Fatalf("gate with no process %q, want paused", g)
		}
		a2 := mac.stream(sid, "alice")
		defer a2.close()
		if j := data[joinedEvent](t, a2.wait(t, nil)); j.Gate != "paused" {
			t.Fatalf("joined again %+v, want gate paused", j)
		}
		if got := gates(sid, "alice@mac-1"); len(got) != 3 {
			t.Fatalf("gate records after the new join %v, want 3", got)
		}
	})

	t.Run("no target sets every agent of the session", func(t *testing.T) {
		sid := "all-1"
		a := mac.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		b := mac.stream(sid, "bob")
		defer b.close()
		b.wait(t, nil)
		wantStatus(t, 204)(adm.gate(sid, "", "run"))
		for _, agent := range []string{"alice", "bob"} {
			if g := gateOf(t, sid, agent); g != "run" {
				t.Fatalf("gate of %s after run for all: %q", agent, g)
			}
		}
		wantStatus(t, 204)(adm.gate(sid, "", "paused"))
		for _, agent := range []string{"alice", "bob"} {
			if g := gateOf(t, sid, agent); g != "paused" {
				t.Fatalf("gate of %s after paused for all: %q", agent, g)
			}
		}
	})

	t.Run("a removed agent never works: the removal is the stop", func(t *testing.T) {
		sid := "stop-1"
		a := mac.stream(sid, "alice")
		a.wait(t, nil)
		wantStatus(t, 204)(adm.gate(sid, "alice@mac-1", "run"))
		wantStatus(t, 204)(adm.kick(sid, "alice@mac-1"))
		if err := a.ended(0); err != nil {
			t.Fatal(err)
		}
		if g := gateOf(t, sid, "alice"); g != "removed" {
			t.Fatalf("gate after the removal %q, want removed", g)
		}
		wantStatus(t, 204)(adm.unkick(sid, "alice@mac-1"))
		if g := gateOf(t, sid, "alice"); g != "run" {
			t.Fatalf("gate after the allow %q, want run", g)
		}
	})

	t.Run("a session with hold off lets a new agent work; the session record shows the setting", func(t *testing.T) {
		sid := "auto-1"
		info := parse[wire.SessionInfo](t, wantStatus(t, 200)(adm.create(sid, "")))
		if !info.Hold {
			t.Fatalf("a new session of a hub that holds new agents: %+v", info)
		}
		wantStatus(t, 204)(adm.hold(sid, false))
		if g := gateOf(t, sid, "carol"); g != "run" {
			t.Fatalf("gate before the join, hold off: %q", g)
		}
		c := mac.stream(sid, "carol")
		defer c.close()
		if j := data[joinedEvent](t, c.wait(t, nil)); j.Gate != "run" {
			t.Fatalf("joined %+v, want gate run", j)
		}
		if got := gates(sid, "carol@mac-1"); len(got) != 0 {
			t.Fatalf("gate records %v, want none", got)
		}
		wantStatus(t, 404)(adm.hold("no-such-session", true))
	})

	t.Run("a forgotten agent is new again: held at its next join", func(t *testing.T) {
		sid := "forget-1"
		a := mac.stream(sid, "alice")
		a.wait(t, nil)
		wantStatus(t, 204)(adm.gate(sid, "alice@mac-1", "run"))
		a.close()
		if err := a.ended(0); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, 204)(adm.forget(sid, "alice@mac-1"))
		if g := gateOf(t, sid, "alice"); g != "held" {
			t.Fatalf("gate after the forget %q, want held", g)
		}
	})

	// What the agent says about itself at the join shows to the operator while it is in the
	// session: that its tool calls go through the gate, and its Herdr pane.
	t.Run("the presence record says gated and names the herdr pane", func(t *testing.T) {
		sid := "pane-1"
		a := mac.stream(sid, "alice", withQuery("gated", "1"), withQuery("herdr_pane", "w1:p3"))
		defer a.close()
		a.wait(t, nil)
		if rec, ok := h.presence(sid + ".mac-1.alice"); !ok || !rec.Gated || rec.HerdrPane != "w1:p3" {
			t.Fatalf("presence %+v %v", rec, ok)
		}
		b := mac.stream(sid, "bob")
		defer b.close()
		b.wait(t, nil)
		if rec, ok := h.presence(sid + ".mac-1.bob"); !ok || rec.Gated || rec.HerdrPane != "" {
			t.Fatalf("presence of a plain agent %+v %v", rec, ok)
		}
		wantStatus(t, 422)(mac.stream(sid, "carol", withQuery("herdr_pane", "--help")).result())
		wantStatus(t, 422)(mac.stream(sid, "carol", withQuery("gated", "yes")).result())
	})

	t.Run("bad input", func(t *testing.T) {
		wantStatus(t, 404)(adm.gate("gate-none", "alice@mac-1", "run"))
		wantStatus(t, 200)(adm.create("gate-bad", ""))
		wantStatus(t, 404)(adm.gate("gate-bad", "nobody@mac-1", "run"))
		wantStatus(t, 422)(adm.gate("gate-bad", "alice@mac-1", "stopped"))
		wantStatus(t, 422)(adm.gate("gate-bad", "alice", "run"))
		wantStatus(t, 422)(adm.req("POST", "/sessions/gate-bad/hold", map[string]any{}))
		wantStatus(t, 422)(mac.req("POST", "/v1/sessions/gate-bad/gate", map[string]any{"agent": "operator"}))
		// An operator token cannot ask as an agent, and a machine token cannot set a gate.
		wantStatus(t, 403)(api{base: h.base, token: adm.token}.gate("gate-bad", "alice"))
		wantStatus(t, 403)(admin{h.base, mac.token}.gate("gate-bad", "alice@mac-1", "run"))
	})
}
