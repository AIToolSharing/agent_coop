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
	h := hub.New(st, hub.Options{AutoCreate: true, Ping: 100 * time.Millisecond})
	srv := httptest.NewServer(api.New(h, t.Logf))
	t.Cleanup(func() {
		srv.Close()
		h.Close()
		_ = st.Close()
	})
	r := &realHub{url: srv.URL, tokens: map[string]string{}}
	for role, name := range map[string]string{wire.RoleMachine: "mac-1", wire.RoleOperator: "matt", wire.RoleOrchestrator: "orch"} {
		if r.tokens[role], err = st.IssueToken(name, role, "2026-10-04T12:00:00.000Z"); err != nil {
			t.Fatal(err)
		}
	}
	r.op = &admin.Client{Base: srv.URL, Token: r.tokens[wire.RoleOperator]}
	return r
}

func (r *realHub) options(role, session, agent string) Options {
	o := Options{URL: r.url, Token: r.tokens[role], Session: session, Agent: agent, Host: "host", Cwd: "/work", second: 10 * time.Millisecond}
	if role == wire.RoleOrchestrator {
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

// The workflow of github.com/map588/agents: a worker that joins while the orchestrator is in
// the session is held; the orchestrator releases it with its task, reads what the workers say
// to each other, and sets up a second session. It is never held itself.
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
	// The worker knows that the task is from the orchestrator, and who the orchestrator is.
	if !strings.Contains(fmt.Sprint(m["messages"]), "from_role:orchestrator") {
		t.Fatalf("the task is not marked as the orchestrator's: %v", m)
	}
	if peers := fmt.Sprint(w1.json("status", nil)["peers"]); !strings.Contains(peers, "name:pm@orch") || !strings.Contains(peers, "role:orchestrator") {
		t.Fatalf("the worker's peers do not mark the orchestrator: %s", peers)
	}
	if !strings.Contains(w1.cs.InitializeResult().Instructions, `from_role="orchestrator"`) {
		t.Fatal("the agent instructions do not say what a message of the orchestrator is")
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

	// The second session: create it, list it.
	o.json("steer", map[string]any{"action": "create_session", "session": "pipe-2"})
	sessions := o.json("sessions", map[string]any{"session": "pipe-2"})
	b, _ := json.Marshal(sessions)
	if !strings.Contains(string(b), `"session":"pipe-2"`) || strings.Contains(string(b), `"hold"`) {
		t.Fatalf("sessions %s", b)
	}
	all := o.json("sessions", nil)
	b, _ = json.Marshal(all)
	for _, want := range []string{`"name":"pm@orch"`, `"role":"orchestrator"`, `"name":"w2@mac-1"`, `"gate":"held"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("sessions lack %s: %s", want, b)
		}
	}

	// Stop needs an agent; a task goes only with release; the actions of the hold of a
	// session are gone; a name that no agent has fails.
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

// The user's complaint: messages from the user and from each agent stop the orchestrator's
// work again and again. It gets no push for each item: one notice says that items wait, no
// second one until it reads them with inbox or wait, and the next batch gets its own notice.
// Without a notice, the orchestrator learns of a message only when it reads by itself, and a
// worker's ask gives up unanswered.
func TestTheOrchestratorGetsOneNoticePerBatch(t *testing.T) {
	r := startRealHub(t)
	oo := r.options(wire.RoleOrchestrator, "pipe-1", "pm")
	oo.Push = true
	o := start(t, oo)
	joined(t, o)
	w1 := start(t, r.options(wire.RoleMachine, "pipe-1", "w1"))
	joined(t, w1)
	w2 := start(t, r.options(wire.RoleMachine, "pipe-1", "w2"))
	joined(t, w2)
	o.json("steer", map[string]any{"action": "release"})

	notices := func() (n int, last push) {
		for _, p := range o.pushes() {
			if p.Meta["kind"] == "message" {
				t.Fatalf("the orchestrator got a message as a push: %+v", p)
			}
			if p.Meta["notice"] == "waiting" {
				n, last = n+1, p
			}
		}
		return n, last
	}
	w1.json("send", map[string]any{"to": "pm", "text": "DONE .pipeline/research.md"})
	eventually(t, "one notice", func() bool { n, _ := notices(); return n == 1 })
	// More items: no second notice while the queue is not read.
	w2.json("send", map[string]any{"to": "pm", "text": "API changed: see T2"})
	if _, err := r.op.Send(context.Background(), "pipe-1", "pm@orch", "How far are you?", ""); err != nil {
		t.Fatal(err)
	}
	asked := w2.async("ask", map[string]any{"to": "pm", "text": "May I change the users table?", "timeout_s": 600})
	eventually(t, "w2 waits on the orchestrator", func() bool {
		for _, p := range o.json("sessions", map[string]any{"session": "pipe-1"})["sessions"].([]any)[0].(map[string]any)["agents"].([]any) {
			if a := p.(map[string]any); a["name"] == "w2@mac-1" && a["waiting_on"] == "pm@orch" {
				return true
			}
		}
		return false
	})
	time.Sleep(200 * time.Millisecond)
	if n, last := notices(); n != 1 || !strings.Contains(last.Content, "call inbox") || !strings.Contains(last.Content, "1 from agents") {
		t.Fatalf("%d notices, last %+v; want 1", n, last)
	}

	// inbox gives the whole batch. The answer reaches the agent that asked.
	in := o.json("inbox", nil)
	b, _ := json.Marshal(in)
	for _, want := range []string{"How far are you?", "May I change the users table?", "DONE .pipeline/research.md", "API changed: see T2"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("inbox lacks %s:\n%s", want, b)
		}
	}
	qid := ""
	for _, m := range in["messages"].([]any) {
		if m := m.(map[string]any); m["text"] == "May I change the users table?" {
			qid = m["id"].(string)
		}
	}
	o.json("send", map[string]any{"to": "w2", "text": "Yes.", "reply_to": qid})
	if res := decodeResult(t, asked); !strings.Contains(fmt.Sprint(res), "Yes.") {
		t.Fatalf("ask %v", res)
	}
	// After the read, the next item is a new batch: one more notice.
	w1.json("send", map[string]any{"to": "pm", "text": "DONE T1"})
	eventually(t, "a second notice", func() bool { n, _ := notices(); return n == 2 })
	// A wait reads the queue too: the item after it gets its own notice.
	if w := o.json("wait", map[string]any{"timeout_s": 5}); !strings.Contains(fmt.Sprint(w), "DONE T1") {
		t.Fatalf("wait %v", w)
	}
	w1.json("send", map[string]any{"to": "pm", "text": "DONE T2"})
	eventually(t, "a third notice", func() bool { n, _ := notices(); return n == 3 })
}

// Found in a live run: a held worker sat in wait, got only the release notice, never saw the
// orchestrator's task, and asked the user what to do. Now the worker learns at its join who
// the orchestrator is, and one wait gives the release and the task together.
func TestAHeldWorkerGetsTheReleaseAndTheTaskInOneWait(t *testing.T) {
	r := startRealHub(t)
	o := start(t, r.options(wire.RoleOrchestrator, "pipe-1", "pm"))
	joined(t, o)
	w := start(t, r.options(wire.RoleMachine, "pipe-1", "w1"))
	if s := joined(t, w); s["gate"] != "held" {
		t.Fatalf("worker %v, want held", s)
	}
	first := w.json("wait", map[string]any{"timeout_s": 300})
	if got := fmt.Sprint(first["notices"]); !strings.Contains(got, "kind:orchestrator") || !strings.Contains(got, "This session has an orchestrator, pm@orch") || !strings.Contains(got, "ask it, not the user") {
		t.Fatalf("first wait %v, want the notice about the orchestrator", first)
	}
	waiting := w.async("wait", map[string]any{"timeout_s": 600})
	eventually(t, "the worker waits", func() bool {
		for _, p := range o.json("sessions", map[string]any{"session": "pipe-1"})["sessions"].([]any)[0].(map[string]any)["agents"].([]any) {
			if a := p.(map[string]any); a["name"] == "w1@mac-1" && a["waiting_on"] == nil && a["gate"] == "held" {
				return true
			}
		}
		return false
	})
	o.json("steer", map[string]any{"action": "release", "agent": "w1", "task": "Create done.txt with the text ok."})
	// The task comes first, marked as the orchestrator's; the release follows at once.
	got := decodeResult(t, waiting)
	if b, _ := json.Marshal(got); !strings.Contains(string(b), "Create done.txt with the text ok.") || !strings.Contains(string(b), `"from_role":"orchestrator"`) {
		t.Fatalf("the wait gave %s, want the task of the orchestrator", b)
	}
	eventually(t, "the worker may work", func() bool { return w.json("status", nil)["gate"] == "run" })
	next := w.json("wait", map[string]any{"timeout_s": 300})
	if b, _ := json.Marshal(next); !strings.Contains(string(b), `"kind":"released"`) || !strings.Contains(string(b), "The orchestrator (orch), for the user, released you") {
		t.Fatalf("the release notice %s does not name the orchestrator", b)
	}
	// The orchestrator itself gets no such notice.
	if a := o.json("inbox", nil); strings.Contains(fmt.Sprint(a["notices"]), "orchestrator") {
		t.Fatalf("the orchestrator was told about itself: %v", a)
	}
}

// A worker that waits only for the user (as the hold text of an earlier version told it)
// does not get the orchestrator's task from that wait. The release notice ends the wait,
// and it brings the queued task with it.
func TestAReleaseNoticeBringsTheQueuedTask(t *testing.T) {
	r := startRealHub(t)
	o := start(t, r.options(wire.RoleOrchestrator, "pipe-1", "pm"))
	joined(t, o)
	w := start(t, r.options(wire.RoleMachine, "pipe-1", "w1"))
	joined(t, w)
	w.json("wait", map[string]any{"timeout_s": 300}) // the notice about the orchestrator
	waiting := w.async("wait", map[string]any{"from": "operator", "timeout_s": 600})
	time.Sleep(100 * time.Millisecond)
	o.json("steer", map[string]any{"action": "release", "agent": "w1", "task": "Run the tests."})
	b, _ := json.Marshal(decodeResult(t, waiting))
	if !strings.Contains(string(b), `"kind":"released"`) || !strings.Contains(string(b), "Run the tests.") {
		t.Fatalf("the wait gave %s, want the release and the task", b)
	}
}
