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
	h := startHub(t, limits{join: high, msg: high, activity: high}, options{autoCreate: true})
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

	t.Run("a new agent may work before and after its first join; a pause stops it, a release lets it go on", func(t *testing.T) {
		sid := "new-1"
		// Before the join, and before the session exists: a tool call gets through.
		if g := gateOf(t, sid, "alice"); g != "run" {
			t.Fatalf("gate before the join %q, want run", g)
		}
		a := mac.stream(sid, "alice")
		defer a.close()
		if j := data[joinedEvent](t, a.wait(t, nil)); j.Gate != "run" {
			t.Fatalf("joined %+v, want gate run", j)
		}
		if g := gateOf(t, sid, "alice"); g != "run" {
			t.Fatalf("gate after the join %q, want run", g)
		}
		if got := gates(sid, "alice@mac-1"); len(got) != 0 {
			t.Fatalf("gate records %v, want none", got)
		}
		// The join gives no notice.
		a.none(t, isNotice, 300*time.Millisecond)

		wantStatus(t, 204)(adm.gate(sid, "alice@mac-1", "paused"))
		if n := data[noticeEvent](t, a.wait(t, isNotice)); n.Kind != "paused" {
			t.Fatalf("notice %+v, want paused", n)
		}
		if g := gateOf(t, sid, "alice"); g != "paused" {
			t.Fatalf("gate after the pause %q, want paused", g)
		}
		// A paused agent can still talk: it must be able to ask the operator.
		wantStatus(t, 200)(mac.send(sid, "alice", "operator", "what is my task?", ""))

		wantStatus(t, 204)(adm.gate(sid, "alice@mac-1", "run"))
		if n := data[noticeEvent](t, a.wait(t, isNotice)); n.Kind != "released" {
			t.Fatalf("notice %+v, want released", n)
		}
		if g := gateOf(t, sid, "alice"); g != "run" {
			t.Fatalf("gate after the release %q, want run", g)
		}
		if got := gates(sid, "alice@mac-1"); len(got) != 2 || got[0] != "paused" || got[1] != "run" {
			t.Fatalf("gate records %v, want [paused run]", got)
		}
	})

	t.Run("pause and resume; a repeat leaves no record; a new process keeps the gate", func(t *testing.T) {
		sid := "pause-1"
		a := mac.stream(sid, "alice")
		a.wait(t, nil)
		wantStatus(t, 204)(adm.gate(sid, "alice@mac-1", "run"))
		a.none(t, isNotice, 300*time.Millisecond)
		wantStatus(t, 204)(adm.gate(sid, "alice@mac-1", "paused"))
		if n := data[noticeEvent](t, a.wait(t, isNotice)); n.Kind != "paused" {
			t.Fatalf("notice %+v, want paused", n)
		}
		wantStatus(t, 204)(adm.gate(sid, "alice@mac-1", "paused"))
		if got := gates(sid, "alice@mac-1"); len(got) != 1 {
			t.Fatalf("gate records %v, want [paused]", got)
		}
		// The agent's process ends and a new one starts: it is still paused, and the join
		// writes no record.
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
		if got := gates(sid, "alice@mac-1"); len(got) != 1 {
			t.Fatalf("gate records after the new join %v, want 1", got)
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

	t.Run("a session that the operator made lets a new agent work", func(t *testing.T) {
		sid := "made-1"
		if info := parse[wire.SessionInfo](t, wantStatus(t, 200)(adm.create(sid, ""))); info.Status != "open" {
			t.Fatalf("a new session: %+v", info)
		}
		if g := gateOf(t, sid, "carol"); g != "run" {
			t.Fatalf("gate before the join: %q", g)
		}
		c := mac.stream(sid, "carol")
		defer c.close()
		if j := data[joinedEvent](t, c.wait(t, nil)); j.Gate != "run" {
			t.Fatalf("joined %+v, want gate run", j)
		}
		if got := gates(sid, "carol@mac-1"); len(got) != 0 {
			t.Fatalf("gate records %v, want none", got)
		}
	})

	// The hold: only a session with an orchestrator in it holds a new agent, and only while
	// the orchestrator is there. The operator never has to release an agent.
	t.Run("an orchestrator in the session holds each new agent until it releases it", func(t *testing.T) {
		sid := "orch-1"
		orch := api{base: h.base, token: h.roleToken("orch", wire.RoleOrchestrator)}
		// No orchestrator yet: a new agent may work before and after its join.
		if g := gateOf(t, sid, "early"); g != "run" {
			t.Fatalf("gate with no orchestrator %q, want run", g)
		}
		early := mac.stream(sid, "early")
		defer early.close()
		if j := data[joinedEvent](t, early.wait(t, nil)); j.Gate != "run" {
			t.Fatalf("early joined %+v, want gate run", j)
		}
		pm := orch.stream(sid, "pm")
		if j := data[joinedEvent](t, pm.wait(t, nil)); j.Gate != "run" {
			t.Fatalf("the orchestrator joined %+v, want gate run", j)
		}
		// The agent that was in the session already works on; a new one is held, before and
		// after its join, and its hold is on record.
		if g := gateOf(t, sid, "early"); g != "run" {
			t.Fatalf("early with an orchestrator %q, want run", g)
		}
		if g := gateOf(t, sid, "alice"); g != "held" {
			t.Fatalf("gate before the join %q, want held", g)
		}
		a := mac.stream(sid, "alice")
		defer a.close()
		if j := data[joinedEvent](t, a.wait(t, nil)); j.Gate != "held" {
			t.Fatalf("joined %+v, want gate held", j)
		}
		if got := gates(sid, "alice@mac-1"); len(got) != 1 || got[0] != "held" {
			t.Fatalf("gate records %v, want [held]", got)
		}
		a.none(t, isNotice, 300*time.Millisecond)
		// A held agent can still talk.
		wantStatus(t, 200)(mac.send(sid, "alice", "pm@orch", "what is my task?", ""))
		// The orchestrator releases it; the record names the orchestrator.
		wantStatus(t, 204)(admin{h.base, orch.token}.gate(sid, "alice@mac-1", "run"))
		if n := data[noticeEvent](t, a.wait(t, isNotice)); n.Kind != "released" || n.By != "orch" {
			t.Fatalf("notice %+v, want released by orch", n)
		}
		if g := gateOf(t, sid, "alice"); g != "run" {
			t.Fatalf("gate after the release %q, want run", g)
		}
		if got := gates(sid, "alice@mac-1"); len(got) != 2 || got[1] != "run" {
			t.Fatalf("gate records %v, want [held run]", got)
		}
		for _, e := range h.events(sid) {
			if e.Kind == wire.EventActivity && e.From == "alice@mac-1" && e.Activity.Kind == "gate" && e.Activity.Gate == "run" && e.Activity.By != "orch" {
				t.Fatalf("the release record %+v does not name the orchestrator", e.Activity)
			}
		}
		// A second worker is held. The orchestrator's process ends and starts again under
		// the same name: nobody is released. Then the orchestrator leaves for good: the hub
		// releases the held worker, says why, and names the orchestrator. The next new agent
		// is not held.
		carl := mac.stream(sid, "carl")
		defer carl.close()
		if j := data[joinedEvent](t, carl.wait(t, nil)); j.Gate != "held" {
			t.Fatalf("carl joined %+v, want gate held", j)
		}
		pm2 := orch.stream(sid, "pm", withInstance(pm.instance))
		pm2.wait(t, nil)
		carl.none(t, isNotice, 300*time.Millisecond)
		if g := gateOf(t, sid, "carl"); g != "held" {
			t.Fatalf("carl after the orchestrator came back %q, want held", g)
		}
		pm2.close()
		if err := pm2.ended(0); err != nil {
			t.Fatal(err)
		}
		// First the leave of the peer, then the release that it caused.
		if n := data[noticeEvent](t, carl.wait(t, isNotice)); n.Kind != "peer_left" || n.Peer != "pm@orch" {
			t.Fatalf("notice %+v, want peer_left pm@orch", n)
		}
		if n := data[noticeEvent](t, carl.wait(t, isNotice)); n.Kind != "released" || n.Peer != "pm@orch" || n.By != "" {
			t.Fatalf("notice %+v, want released with the peer pm@orch", n)
		}
		if g := gateOf(t, sid, "carl"); g != "run" {
			t.Fatalf("carl after the orchestrator left %q, want run", g)
		}
		released := 0
		for _, e := range h.events(sid) {
			if e.Kind == wire.EventActivity && e.From == "carl@mac-1" && e.Activity.Kind == "gate" && e.Activity.Gate == "run" {
				released++
				if e.Activity.Reason != "orchestrator_left" || e.Activity.From != "pm@orch" {
					t.Fatalf("release record %+v, want the reason orchestrator_left from pm@orch", e.Activity)
				}
			}
		}
		if released != 1 {
			t.Fatalf("%d release records of carl, want 1", released)
		}
		if g := gateOf(t, sid, "late"); g != "run" {
			t.Fatalf("gate after the orchestrator left %q, want run", g)
		}
	})

	t.Run("a forgotten agent is new again: its pause is gone at its next join", func(t *testing.T) {
		sid := "forget-1"
		a := mac.stream(sid, "alice")
		a.wait(t, nil)
		wantStatus(t, 204)(adm.gate(sid, "alice@mac-1", "paused"))
		a.close()
		if err := a.ended(0); err != nil {
			t.Fatal(err)
		}
		if g := gateOf(t, sid, "alice"); g != "paused" {
			t.Fatalf("gate before the forget %q, want paused", g)
		}
		wantStatus(t, 204)(adm.forget(sid, "alice@mac-1"))
		if g := gateOf(t, sid, "alice"); g != "run" {
			t.Fatalf("gate after the forget %q, want run", g)
		}
	})

	// What the agent says about itself at the join shows to the operator while it is in the
	// session: that its tool calls go through the gate.
	t.Run("the presence record says gated", func(t *testing.T) {
		sid := "gated-1"
		a := mac.stream(sid, "alice", withQuery("gated", "1"))
		defer a.close()
		a.wait(t, nil)
		if rec, ok := h.presence(sid + ".mac-1.alice"); !ok || !rec.Gated {
			t.Fatalf("presence %+v %v", rec, ok)
		}
		b := mac.stream(sid, "bob")
		defer b.close()
		b.wait(t, nil)
		if rec, ok := h.presence(sid + ".mac-1.bob"); !ok || rec.Gated {
			t.Fatalf("presence of a plain agent %+v %v", rec, ok)
		}
		wantStatus(t, 422)(mac.stream(sid, "carol", withQuery("gated", "yes")).result())
	})

	t.Run("bad input", func(t *testing.T) {
		wantStatus(t, 404)(adm.gate("gate-none", "alice@mac-1", "run"))
		wantStatus(t, 200)(adm.create("gate-bad", ""))
		wantStatus(t, 404)(adm.gate("gate-bad", "nobody@mac-1", "run"))
		wantStatus(t, 422)(adm.gate("gate-bad", "alice@mac-1", "stopped"))
		wantStatus(t, 422)(adm.gate("gate-bad", "alice", "run"))
		wantStatus(t, 404)(adm.req("POST", "/sessions/gate-bad/hold", map[string]any{"hold": false}))
		wantStatus(t, 422)(mac.req("POST", "/v1/sessions/gate-bad/gate", map[string]any{"agent": "operator"}))
		// An operator token cannot ask as an agent, and a machine token cannot set a gate.
		wantStatus(t, 403)(api{base: h.base, token: adm.token}.gate("gate-bad", "alice"))
		wantStatus(t, 403)(admin{h.base, mac.token}.gate("gate-bad", "alice@mac-1", "run"))
	})
}
