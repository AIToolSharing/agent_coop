package api_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// A send to `any` goes to one free peer: the hub picks the peer on the machine with the least
// load, equal machines take turns, and a peer that works, is blocked, held or paused, or that
// left, gets nothing.
func (x *hubSuite) sendToAny(t *testing.T) {
	t.Run("three free peers on three machines take turns; the result names each", func(t *testing.T) {
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
		var got []string
		for i := range 4 {
			sent := parse[operatorSendResponse](t, wantStatus(t, 200)(x.op.a.send(sid, "any", "task", "")))
			got = append(got, sent.To)
			if i < 3 {
				// The peer that was named got the message, addressed to it.
				var s *stream
				switch sent.To {
				case "alice@mac-1":
					s = a
				case "bob@vps-2":
					s = b
				case "carol@mac-3":
					s = c
				default:
					t.Fatalf("to %q", sent.To)
				}
				if m := data[apiMessage](t, s.wait(t, isMsg)); m.To != sent.To || m.From != "operator" || m.Text != "task" {
					t.Fatalf("message %+v", m)
				}
			}
		}
		// Equal machines take turns in the order of the presence keys: machine, then agent.
		if want := []string{"alice@mac-1", "carol@mac-3", "bob@vps-2", "alice@mac-1"}; !slices.Equal(got, want) {
			t.Fatalf("picks %v, want %v", got, want)
		}
		// The two others got nothing of the fourth send.
		b.none(t, isMsg, 200*time.Millisecond)
		c.none(t, isMsg, 200*time.Millisecond)
	})

	t.Run("an agent's send to any skips itself, a working and a blocked peer, and reports the state", func(t *testing.T) {
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
		wantStatus(t, 204)(x.vps2.activity(sid, map[string]any{"kind": "state", "agent": "bob", "state": "working"}))
		for range 2 {
			sent := parse[sendResponse](t, wantStatus(t, 200)(x.mac1.send(sid, "alice", "any", "please", "")))
			if sent.To != "carol@mac-3" || !sent.Online || sent.State != "idle" {
				t.Fatalf("sent %+v, want carol@mac-3 idle", sent)
			}
			if m := data[apiMessage](t, c.wait(t, isMsg)); m.From != "alice@mac-1" || m.To != "carol@mac-3" {
				t.Fatalf("message %+v", m)
			}
		}
		b.none(t, isMsg, 200*time.Millisecond)
		wantStatus(t, 204)(x.mac3.activity(sid, map[string]any{"kind": "state", "agent": "carol", "state": "blocked"}))
		body := wantStatus(t, 404)(x.mac1.send(sid, "alice", "any", "please", ""))
		if m := parse[errBody](t, body).Message; m != "no peer in this session can take work now; peers: carol@mac-3 (blocked), bob@vps-2 (working)" {
			t.Fatalf("message %q", m)
		}
	})

	t.Run("a machine that runs a working agent loses to a free machine", func(t *testing.T) {
		sid := x.session(t)
		other := x.session(t)
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		w := x.mac1.stream(other, "worker")
		defer w.close()
		b := x.vps2.stream(sid, "bob")
		defer b.close()
		for _, s := range []*stream{a, w, b} {
			s.wait(t, nil)
		}
		// mac-1 works in another session: bob on vps-2 gets each task, although alice is free
		// and comes first by name.
		wantStatus(t, 204)(x.mac1.activity(other, map[string]any{"kind": "state", "agent": "worker", "state": "working"}))
		for range 2 {
			if sent := parse[operatorSendResponse](t, wantStatus(t, 200)(x.op.a.send(sid, "any", "task", ""))); sent.To != "bob@vps-2" {
				t.Fatalf("to %q, want bob@vps-2", sent.To)
			}
		}
		// The worker is done: the machines are equal again, and alice's turn comes.
		wantStatus(t, 204)(x.mac1.activity(other, map[string]any{"kind": "state", "agent": "worker", "state": "done"}))
		if sent := parse[operatorSendResponse](t, wantStatus(t, 200)(x.op.a.send(sid, "any", "task", ""))); sent.To != "alice@mac-1" {
			t.Fatalf("to %q, want alice@mac-1", sent.To)
		}
	})

	t.Run("a held or paused peer and a peer that left get nothing; an empty session is 404", func(t *testing.T) {
		sid := x.session(t)
		wantStatus(t, 404)(x.op.a.send(sid, "any", "task", ""))
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		b := x.vps2.stream(sid, "bob")
		b.wait(t, nil)
		a.wait(t, nil)
		wantStatus(t, 204)(x.op.a.gate(sid, "alice@mac-1", "paused"))
		a.wait(t, eventIs("notice"))
		if sent := parse[operatorSendResponse](t, wantStatus(t, 200)(x.op.a.send(sid, "any", "task", ""))); sent.To != "bob@vps-2" {
			t.Fatalf("to %q, want bob@vps-2", sent.To)
		}
		b.close()
		body := wantStatus(t, 404)(x.op.a.send(sid, "any", "task", ""))
		if m := parse[errBody](t, body).Message; !strings.Contains(m, "alice@mac-1 (paused)") || strings.Contains(m, "bob") {
			t.Fatalf("message %q", m)
		}
	})

	t.Run("an orchestrator gives work to any and gets none", func(t *testing.T) {
		sid := x.session(t)
		boss := api{x.h.base, x.h.roleToken("boss", wire.RoleOrchestrator)}
		pm := boss.stream(sid, "pm")
		defer pm.close()
		a := x.mac1.stream(sid, "alice")
		defer a.close()
		pm.wait(t, nil)
		a.wait(t, nil)
		// The orchestrator is free and comes first by its key, but alice gets each task.
		for range 2 {
			if sent := parse[operatorSendResponse](t, wantStatus(t, 200)(x.op.a.send(sid, "any", "task", ""))); sent.To != "alice@mac-1" {
				t.Fatalf("to %q, want alice@mac-1", sent.To)
			}
		}
		if sent := parse[sendResponse](t, wantStatus(t, 200)(boss.send(sid, "pm", "any", "task", ""))); sent.To != "alice@mac-1" {
			t.Fatalf("the orchestrator's send went to %q, want alice@mac-1", sent.To)
		}
		pm.none(t, isMsg, 200*time.Millisecond)
		// When the only worker is busy, nobody takes the task: the orchestrator does not.
		wantStatus(t, 204)(x.mac1.activity(sid, map[string]any{"kind": "state", "agent": "alice", "state": "working"}))
		body := wantStatus(t, 404)(x.op.a.send(sid, "any", "task", ""))
		if m := parse[errBody](t, body).Message; m != "no peer in this session can take work now; peers: pm@boss (orchestrator), alice@mac-1 (working)" {
			t.Fatalf("message %q", m)
		}
	})

	t.Run("any is no agent name", func(t *testing.T) {
		sid := x.session(t)
		wantStatus(t, 422)(x.mac1.stream(sid, "any").result())
		wantStatus(t, 422)(x.mac1.send(sid, "any", "all", "hi", ""))
	})
}
