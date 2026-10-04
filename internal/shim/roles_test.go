package shim

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/admin"
	"github.com/AIToolSharing/agent_coop/internal/api"
	"github.com/AIToolSharing/agent_coop/internal/hub"
	"github.com/AIToolSharing/agent_coop/internal/store"
	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// realHub is a hub with a fresh store, with a token of each role. The tools of the two roles
// go through the admin API, so their tests use the real one.
type realHub struct {
	url    string
	tokens map[string]string // by role
	op     *admin.Client
}

func startRealHub(t *testing.T) *realHub {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "coop.db"))
	if err != nil {
		t.Fatal(err)
	}
	h := hub.New(st, hub.Options{AutoCreate: true, HoldNew: true, Ping: 100 * time.Millisecond})
	srv := httptest.NewServer(api.New(h, t.Logf))
	t.Cleanup(func() {
		srv.Close()
		h.Close()
		_ = st.Close()
	})
	r := &realHub{url: srv.URL, tokens: map[string]string{}}
	for role, name := range map[string]string{wire.RoleMachine: "mac-1", wire.RoleOperator: "matt", wire.RoleOrchestrator: "orch", wire.RoleReporter: "rep"} {
		if r.tokens[role], err = st.IssueToken(name, role, "2026-10-04T12:00:00.000Z"); err != nil {
			t.Fatal(err)
		}
	}
	r.op = &admin.Client{Base: srv.URL, Token: r.tokens[wire.RoleOperator]}
	return r
}

func (r *realHub) options(role, session, agent string) Options {
	o := Options{URL: r.url, Token: r.tokens[role], Session: session, Agent: agent, Host: "host", Cwd: "/work", second: 10 * time.Millisecond}
	if role == wire.RoleOrchestrator || role == wire.RoleReporter {
		o.Role = role
		o.Admin = &admin.Client{Base: r.url, Token: r.tokens[role]}
	}
	return o
}

func toolNamesOf(t *testing.T, a *testAgent) []string {
	t.Helper()
	tools, err := a.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

// The workflow of github.com/map588/agents: the orchestrator starts in a session that holds
// new agents, releases a worker with its task, reads what the workers say to each other, and
// sets up a second session. It is never held itself.
func TestTheOrchestratorSteersTheSessionWithItsTools(t *testing.T) {
	r := startRealHub(t)
	o := start(t, r.options(wire.RoleOrchestrator, "pipe-1", "pm"))
	if got := strings.Join(toolNamesOf(t, o), " "); got != "ask history inbox read send sessions set_state status steer wait" {
		t.Fatalf("orchestrator tools: %s", got)
	}
	if !strings.Contains(o.cs.InitializeResult().Instructions, "You are the orchestrator") {
		t.Fatal("no orchestrator instructions")
	}
	if s := joined(t, o); s["gate"] != "run" {
		t.Fatalf("orchestrator status %v, want gate run", s)
	}
	w1 := start(t, r.options(wire.RoleMachine, "pipe-1", "w1"))
	if s := joined(t, w1); s["gate"] != "held" {
		t.Fatalf("worker status %v, want held", s)
	}
	w2 := start(t, r.options(wire.RoleMachine, "pipe-1", "w2"))
	joined(t, w2)

	// A bare name is enough; the task arrives as the orchestrator's message.
	got := o.json("steer", map[string]any{"action": "release", "agent": "w1", "task": "Write .pipeline/research.md"})
	if got["agent"] != "w1@mac-1" || got["done"] != "release" {
		t.Fatalf("steer %v", got)
	}
	// First the notice of the release, then the task.
	var m map[string]any
	for range 3 {
		m = w1.json("wait", map[string]any{"timeout_s": 300})
		if msgs, _ := m["messages"].([]any); len(msgs) > 0 {
			break
		}
	}
	if msgs, _ := m["messages"].([]any); len(msgs) == 0 || !strings.Contains(fmt.Sprint(msgs), "Write .pipeline/research.md") || !strings.Contains(fmt.Sprint(msgs), "pm@orch") {
		t.Fatalf("worker got %v", m)
	}
	if s := w1.json("status", nil); s["gate"] != "run" {
		t.Fatalf("worker after the release %v", s)
	}

	// The orchestrator reads what two workers say to each other. No push of it reached it.
	w1.json("send", map[string]any{"to": "w2", "text": "API changed: see T2"})
	read := o.json("read", map[string]any{})
	if !strings.Contains(fmt.Sprint(read["messages"]), "API changed: see T2") {
		t.Fatalf("read %v", read)
	}
	if inbox := o.json("inbox", nil); strings.Contains(fmt.Sprint(inbox), "API changed") {
		t.Fatalf("a message between two workers reached the orchestrator's inbox: %v", inbox)
	}

	// The second session: create it, hold off, list it.
	o.json("steer", map[string]any{"action": "create_session", "session": "pipe-2"})
	o.json("steer", map[string]any{"action": "hold_off", "session": "pipe-2"})
	sessions := o.json("sessions", map[string]any{"session": "pipe-2"})
	b, _ := json.Marshal(sessions)
	if !strings.Contains(string(b), `"session":"pipe-2"`) || !strings.Contains(string(b), `"hold":false`) {
		t.Fatalf("sessions %s", b)
	}
	all := o.json("sessions", nil)
	b, _ = json.Marshal(all)
	for _, want := range []string{`"name":"pm@orch"`, `"role":"orchestrator"`, `"name":"w2@mac-1"`, `"gate":"held"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("sessions lack %s: %s", want, b)
		}
	}

	// Stop needs an agent; a task goes only with release; a name that no agent has fails.
	for _, bad := range []map[string]any{
		{"action": "stop"},
		{"action": "pause", "agent": "w2", "task": "x"},
		{"action": "release", "agent": "nobody"},
		{"action": "hold_on", "agent": "w2"},
		{"action": "release", "agent": "w2", "session": "pipe-2", "task": "x"},
	} {
		if res := o.tool("steer", bad); !res.isError {
			t.Errorf("steer %v: %s, want an error", bad, res.text)
		}
	}
	o.json("steer", map[string]any{"action": "stop", "agent": "w2"})
	if res := w2.tool("send", map[string]any{"to": "all", "text": "hi"}); !res.isError {
		t.Fatalf("a stopped worker could still send: %s", res.text)
	}
	// The operator can still pause the orchestrator.
	if err := r.op.SetGate(context.Background(), "pipe-1", "pm@orch", wire.GatePaused); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for o.json("status", nil)["gate"] != "paused" {
		if time.Now().After(deadline) {
			t.Fatal("the orchestrator did not learn that the operator paused it")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A reporter joins no session and changes nothing. It reads each session and its agents.
func TestTheReporterOnlyReads(t *testing.T) {
	r := startRealHub(t)
	w1 := start(t, r.options(wire.RoleMachine, "pipe-1", "w1"))
	joined(t, w1)
	if err := r.op.SetGate(context.Background(), "pipe-1", "w1@mac-1", wire.GateRun); err != nil {
		t.Fatal(err)
	}
	w1.json("set_state", map[string]any{"state": "working", "note": "T1"})
	w1.json("send", map[string]any{"to": "operator", "text": "DONE .pipeline/research.md"})

	// A session in its options does not make it join.
	rep := start(t, r.options(wire.RoleReporter, "pipe-1", "rep"))
	if got := strings.Join(toolNamesOf(t, rep), " "); got != "read sessions" {
		t.Fatalf("reporter tools: %s", got)
	}
	if !strings.Contains(rep.cs.InitializeResult().Instructions, "You change nothing") {
		t.Fatal("no reporter instructions")
	}
	b, _ := json.Marshal(rep.json("sessions", nil))
	for _, want := range []string{`"session":"pipe-1"`, `"name":"w1@mac-1"`, `"state":"working"`, `"note":"T1"`, `"gate":"run"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("sessions lack %s: %s", want, b)
		}
	}
	read := rep.json("read", map[string]any{"session": "pipe-1"})
	msgs, _ := read["messages"].([]any)
	if len(msgs) != 1 || !strings.Contains(fmt.Sprint(msgs[0]), "DONE .pipeline/research.md") {
		t.Fatalf("read %v", read)
	}
	id := msgs[0].(map[string]any)["id"].(string)
	if after := rep.json("read", map[string]any{"session": "pipe-1", "after": id}); len(after["messages"].([]any)) != 0 {
		t.Fatalf("read after the last id: %v", after)
	}
	if res := rep.tool("read", map[string]any{}); !res.isError {
		t.Fatalf("read with no session: %s", res.text)
	}
	// The reporter is not in the session: the operator's list does not have it.
	v, err := r.op.Session(context.Background(), "pipe-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range v.Agents {
		if strings.HasPrefix(a.Name, "rep@") {
			t.Fatalf("the reporter joined: %+v", v.Agents)
		}
	}
}
