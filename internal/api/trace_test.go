package api_test

import (
	"strings"
	"testing"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// trace reports what an agent did at its terminal.
func (a api) trace(sid string, body map[string]any) (int, []byte) {
	return a.req("POST", "/v1/sessions/"+sid+"/trace", body)
}

type traceEvent struct {
	Kind string `json:"kind"`
	wire.TraceUpdate
}

// The operator's complaint: the TUI shows messages and the state an agent sets for itself, not
// what the agent does. The hooks of Claude Code report tool calls, words and prompts; the hub
// gives them to the operator's feed, and to nobody else.
func TestTrace(t *testing.T) {
	high := limit{burst: 10_000, perSecond: 10_000}
	h := startHub(t, limits{join: high, msg: high, activity: high}, options{autoCreate: true})
	mac := api{base: h.base, token: h.token("mac-1")}
	adm := admin{h.base, h.operatorToken("op")}
	isTrace := func(key string) func(sseEvent) bool {
		return func(e sseEvent) bool { return e.event == "trace" && strings.Contains(e.data, `"`+key+`"`) }
	}
	items := func(xs ...map[string]any) []map[string]any { return xs }

	t.Run("the operator's feed gets the items with a number and a time; the agents get nothing", func(t *testing.T) {
		sid := "trace-1"
		a := mac.stream(sid, "alice")
		defer a.close()
		a.wait(t, nil)
		b := mac.stream(sid, "bob")
		defer b.close()
		b.wait(t, nil)
		feed := adm.stream()
		defer feed.close()
		feed.wait(t, eventIs("snapshot"))

		wantStatus(t, 204)(mac.trace(sid, map[string]any{"agent": "alice", "branch": "main", "items": items(
			map[string]any{"kind": "say", "id": "u1", "text": "I run the tests now.", "before": "t1"},
			map[string]any{"kind": "tool_start", "id": "t1", "tool": "Bash", "text": "go test ./..."},
		)}))
		u := data[traceEvent](t, feed.wait(t, isTrace(sid+".mac-1.alice")))
		if u.Boot == 0 || u.Branch != "main" || len(u.Items) != 2 {
			t.Fatalf("trace event %+v, want a boot, branch main and 2 items", u)
		}
		say, start := u.Items[0], u.Items[1]
		if say.Kind != "say" || say.Text != "I run the tests now." || say.Before != "t1" || start.Tool != "Bash" || start.Text != "go test ./..." {
			t.Fatalf("items %+v", u.Items)
		}
		if say.N < 1 || start.N != say.N+1 || !wire.IsTime(say.At) {
			t.Fatalf("numbers %d %d and time %q: want numbers that rise and a time", say.N, start.N, say.At)
		}
		a.none(t, func(e sseEvent) bool { return strings.Contains(e.data, "go test") }, 300*time.Millisecond)
		b.none(t, func(e sseEvent) bool { return strings.Contains(e.data, "go test") }, 10*time.Millisecond)
		if evs := h.events(sid); len(evs) != 2 {
			t.Fatalf("%d stored events, want only the 2 joins: the trace is not in the store", len(evs))
		}

		// Two tool calls of one turn report the same words: the hub keeps them one time.
		wantStatus(t, 204)(mac.trace(sid, map[string]any{"agent": "alice", "items": items(
			map[string]any{"kind": "say", "id": "u1", "text": "I run the tests now."},
			map[string]any{"kind": "tool_end", "id": "t1", "tool": "Edit", "text": "a.go", "ms": 1200, "file": "a.go"},
			map[string]any{"kind": "tool_end", "id": "t2", "tool": "Edit", "text": "a.go", "file": "a.go"},
			map[string]any{"kind": "tool_end", "id": "t3", "tool": "Write", "text": "b.go", "file": "b.go", "failed": true},
		)}))
		u = data[traceEvent](t, feed.wait(t, isTrace(sid+".mac-1.alice")))
		if len(u.Items) != 3 || u.Items[0].ID != "t1" || u.Items[0].MS != 1200 || !u.Items[2].Failed {
			t.Fatalf("items %+v, want the 3 tool ends and no second say", u.Items)
		}
		// A tool call that failed changed no file.
		if len(u.Files) != 2 || u.Files[1].Path != "a.go" || u.Files[1].Count != 2 || u.Branch != "main" {
			t.Fatalf("files %+v branch %q, want a.go with counts 1 and 2, and the branch kept", u.Files, u.Branch)
		}
		// A report with nothing new makes no feed event.
		wantStatus(t, 204)(mac.trace(sid, map[string]any{"agent": "alice", "items": items(
			map[string]any{"kind": "say", "id": "u1", "text": "I run the tests now."},
		)}))
		feed.none(t, eventIs("trace"), 300*time.Millisecond)

		// A feed that connects later gets the whole trace of the agent in one event.
		late := adm.stream()
		defer late.close()
		u = data[traceEvent](t, late.wait(t, isTrace(sid+".mac-1.alice")))
		if len(u.Items) != 5 || len(u.Files) != 1 || u.Files[0].Count != 2 {
			t.Fatalf("dump: %d items, files %+v; want 5 items and a.go with count 2", len(u.Items), u.Files)
		}
	})

	t.Run("only an agent that is in the session can report; the hub forgets the trace with the agent", func(t *testing.T) {
		sid := "trace-2"
		one := map[string]any{"agent": "carol", "items": items(map[string]any{"kind": "prompt", "text": "fix it"})}
		wantStatus(t, 403)(mac.trace(sid, one))
		c := mac.stream(sid, "carol")
		c.wait(t, nil)
		wantStatus(t, 204)(mac.trace(sid, one))
		c.close()
		if err := c.ended(2 * time.Second); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, 403)(mac.trace(sid, one))
		// The trace of an agent that left stays, until the operator forgets the agent.
		key := sid + ".mac-1.carol"
		feed := adm.stream()
		feed.wait(t, isTrace(key))
		feed.close()
		for range 50 {
			if _, live := h.presence(key); !live {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		wantStatus(t, 204)(adm.forget(sid, "carol@mac-1"))
		feed = adm.stream()
		defer feed.close()
		feed.wait(t, func(e sseEvent) bool { return e.event == "event" && strings.Contains(e.data, "forgotten") })
		// The feed gives the traces before the events: a trace of carol would be here by now.
		feed.none(t, isTrace(key), 50*time.Millisecond)
	})

	t.Run("a report that breaks the rules is refused", func(t *testing.T) {
		sid := "trace-3"
		d := mac.stream(sid, "dave")
		defer d.close()
		d.wait(t, nil)
		long := strings.Repeat("x", wire.MaxTraceText+1)
		var many []map[string]any
		for range wire.MaxTraceItems + 1 {
			many = append(many, map[string]any{"kind": "say", "text": "x"})
		}
		for name, body := range map[string]map[string]any{
			"no items":         {"agent": "dave", "items": items()},
			"too many items":   {"agent": "dave", "items": many},
			"unknown kind":     {"agent": "dave", "items": items(map[string]any{"kind": "thought"})},
			"text too long":    {"agent": "dave", "items": items(map[string]any{"kind": "say", "text": long})},
			"a number as item": {"agent": "dave", "items": []any{1}},
			"the hub's fields": {"agent": "dave", "items": items(map[string]any{"kind": "say", "n": 7})},
			"null for a field": {"agent": "dave", "items": items(map[string]any{"kind": "say", "failed": nil})},
			"negative time":    {"agent": "dave", "items": items(map[string]any{"kind": "tool_end", "ms": -1})},
			"reserved name":    {"agent": "operator", "items": items(map[string]any{"kind": "say"})},
			"unknown field":    {"agent": "dave", "dir": "/x", "items": items(map[string]any{"kind": "say"})},
		} {
			if status, raw := mac.trace(sid, body); status != 422 {
				t.Errorf("%s: status %d %s, want 422", name, status, raw)
			}
		}
		// The longest text that the rules allow passes.
		wantStatus(t, 204)(mac.trace(sid, map[string]any{"agent": "dave", "items": items(
			map[string]any{"kind": "say", "text": strings.Repeat("é", wire.MaxTraceText)},
		)}))
		wantStatus(t, 403)(api{base: h.base, token: adm.token}.trace(sid, map[string]any{"agent": "dave", "items": items(map[string]any{"kind": "say"})}))
	})
}
