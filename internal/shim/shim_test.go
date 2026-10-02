package shim

// The ports of the scenarios in packages/mcp/test/shim.int.test.ts. Real shims run against the
// fake hub. The test acts as the MCP client (Claude Code) through an in-memory transport and
// records every text that an agent could see: the tool list, the instructions, the results and
// the channel notifications.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/channel"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type push struct {
	Content string            `json:"content"`
	Meta    map[string]string `json:"meta"`
}

type testAgent struct {
	t  *testing.T
	cs *mcp.ClientSession
	// stop ends the MCP session and waits until Serve returns.
	stop func()

	mu     sync.Mutex
	pushed []push
	seen   []string
}

func (a *testAgent) see(text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seen = append(a.seen, text)
}

func (a *testAgent) pushes() []push {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.pushed)
}

// captureConn takes the channel notifications out of the client's input, like Claude Code.
type captureConn struct {
	mcp.Connection
	a *testAgent
}

func (c captureConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	for {
		m, err := c.Connection.Read(ctx)
		if err != nil {
			return m, err
		}
		if r, ok := m.(*jsonrpc.Request); ok && r.Method == channel.Method && !r.ID.IsValid() {
			var p push
			if err := json.Unmarshal(r.Params, &p); err != nil {
				c.a.t.Errorf("channel params %s: %v", r.Params, err)
			}
			c.a.mu.Lock()
			c.a.pushed = append(c.a.pushed, p)
			c.a.seen = append(c.a.seen, string(r.Params))
			c.a.mu.Unlock()
			continue
		}
		return m, nil
	}
}

type captureTransport struct {
	inner mcp.Transport
	a     *testAgent
}

func (c captureTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return captureConn{conn, c.a}, nil
}

// options gives the options of an agent on machine against hub h. One second of a timeout_s
// lasts 10 ms.
func options(h *fakeHub, machine, session, agent string, push bool) Options {
	return Options{
		URL: h.srv.URL, Token: "t-" + machine, Session: session, Agent: agent, Push: push,
		Host: "host-" + machine, Cwd: "/work", second: 10 * time.Millisecond,
	}
}

// start runs Serve with o and connects an MCP client to it. At the end of the test it checks
// that no text the agent saw names how the service works.
func start(t *testing.T, o Options) *testAgent {
	t.Helper()
	serverT, clientT := mcp.NewInMemoryTransports()
	o.Transport = serverT
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- Serve(ctx, o) }()
	a := &testAgent{t: t}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	cs, err := client.Connect(ctx, captureTransport{clientT, a}, nil)
	if err != nil {
		cancel()
		t.Fatalf("connect: %v", err)
	}
	a.cs = cs
	var once sync.Once
	a.stop = func() {
		once.Do(func() {
			_ = cs.Close()
			select {
			case err := <-served:
				if err != nil {
					t.Errorf("Serve gave %v when the client went away", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("Serve did not return after the client went away")
			}
			cancel()
		})
	}
	t.Cleanup(func() {
		a.stop()
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, s := range a.seen {
			if forbiddenRE.MatchString(s) {
				t.Errorf("an agent saw a forbidden word: %s", s)
			}
		}
	})
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(tools)
	a.see(string(b))
	a.see(cs.InitializeResult().Instructions)
	return a
}

type toolResult struct {
	text    string
	isError bool
	err     error
}

func (a *testAgent) callTool(name string, args map[string]any) toolResult {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if args == nil {
		args = map[string]any{}
	}
	res, err := a.cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return toolResult{err: err}
	}
	var texts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			texts = append(texts, tc.Text)
		}
	}
	text := strings.Join(texts, "\n")
	a.see(text)
	return toolResult{text: text, isError: res.IsError}
}

// tool calls a tool and fails the test on a protocol error.
func (a *testAgent) tool(name string, args map[string]any) toolResult {
	a.t.Helper()
	r := a.callTool(name, args)
	if r.err != nil {
		a.t.Fatalf("%s: %v", name, r.err)
	}
	return r
}

// json calls a tool that must succeed and decodes its JSON text.
func (a *testAgent) json(name string, args map[string]any) map[string]any {
	a.t.Helper()
	r := a.tool(name, args)
	if r.isError {
		a.t.Fatalf("%s failed: %s", name, r.text)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(r.text), &v); err != nil {
		a.t.Fatalf("%s: %q is not JSON: %v", name, r.text, err)
	}
	return v
}

// async calls a tool in the background.
func (a *testAgent) async(name string, args map[string]any) <-chan toolResult {
	c := make(chan toolResult, 1)
	go func() { c <- a.callTool(name, args) }()
	return c
}

func decodeResult(t *testing.T, c <-chan toolResult) map[string]any {
	t.Helper()
	var r toolResult
	select {
	case r = <-c:
	case <-time.After(10 * time.Second):
		t.Fatal("the tool call did not end")
	}
	if r.err != nil || r.isError {
		t.Fatalf("the tool call failed: %v %s", r.err, r.text)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(r.text), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(end) {
			t.Fatalf("never true: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func joined(t *testing.T, a *testAgent) map[string]any {
	t.Helper()
	var s map[string]any
	eventually(t, "the agent joins", func() bool {
		s = a.json("status", nil)
		return s["joined"] == true
	})
	return s
}

// match reports whether got holds every key of want with an equal value. Maps and slices in
// want match by the same rule, item by item.
func match(got, want any) bool {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range w {
			if !match(g[k], v) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !match(g[i], w[i]) {
				return false
			}
		}
		return true
	}
	return fmt.Sprint(got) == fmt.Sprint(want)
}

func expect(t *testing.T, got map[string]any, want map[string]any) {
	t.Helper()
	if !match(got, want) {
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		t.Fatalf("got %s\nwant at least %s", g, w)
	}
}

// --- outside a session -----------------------------------------------------------------------

func TestNoSessionOffersNoToolsButStatusAnswers(t *testing.T) {
	h := newFakeHub(t)
	o := options(h, "mac-1", "", "agent", true)
	a := start(t, o)
	tools, err := a.cs.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 0 {
		t.Fatalf("tools %v %v", tools, err)
	}
	ir := a.cs.InitializeResult()
	if ir.Capabilities.Experimental != nil || ir.Instructions != "" {
		t.Fatalf("capabilities %+v instructions %q", ir.Capabilities, ir.Instructions)
	}
	if got := a.json("status", nil); !match(got, map[string]any{"joined": false, "reason": notSet}) || len(got) != 2 {
		t.Fatalf("status %v", got)
	}
	if r := a.tool("send", map[string]any{"to": "all", "text": "x"}); !r.isError || r.text != notSet {
		t.Fatalf("send %+v", r)
	}
	if r := a.callTool("nothing", nil); r.err == nil {
		t.Fatal("an unknown tool must stay a protocol error")
	}
}

func TestSessionWithoutMachineSetUpGivesAClearReason(t *testing.T) {
	h := newFakeHub(t)
	o := options(h, "mac-1", "x", "agent", false)
	o.URL = ""
	a := start(t, o)
	if s := a.json("status", nil); s["joined"] != false || !strings.Contains(fmt.Sprint(s["reason"]), "not set up") {
		t.Fatalf("status %v", s)
	}
	if r := a.tool("send", map[string]any{"to": "all", "text": "x"}); !r.isError {
		t.Fatalf("send %+v", r)
	}
}

func TestUnreachableServiceIsReportedNotThrown(t *testing.T) {
	h := newFakeHub(t)
	gone := httptest.NewServer(nil)
	gone.Close()
	o := options(h, "mac-1", "x", "agent", false)
	o.URL = gone.URL
	a := start(t, o)
	eventually(t, "status says unreachable", func() bool { return a.json("status", nil)["reason"] == unreachable })
	if r := a.tool("send", map[string]any{"to": "all", "text": "x"}); !r.isError || r.text != unreachable {
		t.Fatalf("send %+v", r)
	}
}

func TestWrongCredentialGivesAPlainReason(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("s")
	o := options(h, "mac-1", "s", "alice", false)
	o.Token = "mac-1.not-the-secret"
	a := start(t, o)
	eventually(t, "status says refused", func() bool {
		return strings.Contains(fmt.Sprint(a.json("status", nil)["reason"]), "not allowed to join")
	})
	if r := a.tool("send", map[string]any{"to": "all", "text": "x"}); !r.isError || !strings.Contains(r.text, "not allowed to join") {
		t.Fatalf("send %+v", r)
	}
}

// --- in a session ----------------------------------------------------------------------------

func TestToolListInstructionsAndChannelCapability(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("s")
	a := start(t, options(h, "mac-1", "s", "alice", true))
	tools, err := a.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		if tool.Description == "" {
			t.Errorf("%s has no description", tool.Name)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"ask", "history", "inbox", "send", "set_state", "status", "wait"}) {
		t.Fatalf("tools %v", names)
	}
	ir := a.cs.InitializeResult()
	if ir.Instructions != instructions {
		t.Fatalf("instructions %q", ir.Instructions)
	}
	if fmt.Sprint(ir.Capabilities.Experimental) != fmt.Sprint(map[string]any{"claude/channel": map[string]any{}}) {
		t.Fatalf("experimental %v", ir.Capabilities.Experimental)
	}
	// Without push, no channel.
	b := start(t, options(h, "vps-2", "s", "bob", false))
	if b.cs.InitializeResult().Capabilities.Experimental != nil {
		t.Fatal("a pull agent advertises the channel")
	}
}

func TestJoinSendsTheClientAndTheMachine(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("s")
	a := start(t, options(h, "mac-1", "s", "alice", false))
	joined(t, a)
	got := h.lastQuery()
	for _, want := range []string{"agent=alice", "host=host-mac-1", "cwd=%2Fwork", "client_name=test-client", "client_version=1.0"} {
		if !strings.Contains(got, want) {
			t.Errorf("query %q has no %s", got, want)
		}
	}
	if !regexp.MustCompile(`instance=[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`).MatchString(got) {
		t.Errorf("query %q has no UUID v4 instance", got)
	}
	// An option overrides the handshake, field by field.
	o := options(h, "vps-2", "s", "bob", false)
	o.ClientName = "claude-code"
	joined(t, start(t, o))
	if got := h.lastQuery(); !strings.Contains(got, "client_name=claude-code") || !strings.Contains(got, "client_version=1.0") {
		t.Errorf("query %q", got)
	}
}

func TestPushMessageReachesTheIdlePeer(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("s")
	a := start(t, options(h, "mac-1", "s", "alice", true))
	b := start(t, options(h, "vps-2", "s", "bob", true))
	joined(t, a)
	joined(t, b)
	t0 := time.Now()
	sent := a.json("send", map[string]any{"to": "bob", "text": "hello bob"})
	id := fmt.Sprint(sent["id"])
	var p push
	eventually(t, "bob gets the push", func() bool {
		ps := b.pushes()
		i := slices.IndexFunc(ps, func(p push) bool { return p.Meta["id"] == id })
		if i >= 0 {
			p = ps[i]
		}
		return i >= 0
	})
	if d := time.Since(t0); d > 250*time.Millisecond {
		t.Errorf("the push took %v", d)
	}
	want := push{Content: "hello bob", Meta: map[string]string{"kind": "message", "from": "alice@mac-1", "to": "bob@vps-2", "id": id}}
	if fmt.Sprint(p) != fmt.Sprint(want) {
		t.Fatalf("push %+v", p)
	}
	if len(a.pushes()) != 0 {
		t.Fatal("the sender got its own message")
	}
	// A reply carries reply_to in the meta.
	reply := b.json("send", map[string]any{"to": "alice", "text": "hi alice", "reply_to": id})
	eventually(t, "alice gets the reply", func() bool {
		ps := a.pushes()
		return len(ps) == 1 && ps[0].Meta["reply_to"] == id && ps[0].Meta["id"] == fmt.Sprint(reply["id"])
	})
}

func TestPullWaitReturnsTheMessageThenInboxIsEmpty(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("s")
	a := start(t, options(h, "mac-1", "s", "alice", false))
	b := start(t, options(h, "vps-2", "s", "bob", false))
	joined(t, a)
	joined(t, b)
	waiting := b.async("wait", map[string]any{"timeout_s": 500})
	eventually(t, "bob waits", func() bool {
		return slices.ContainsFunc(h.activities("s"), func(x activity) bool { return x.Kind == "wait_start" })
	})
	sent := a.json("send", map[string]any{"to": "all", "text": "news"})
	expect(t, decodeResult(t, waiting), map[string]any{
		"messages": []any{map[string]any{"id": sent["id"], "from": "alice@mac-1", "text": "news"}},
		"notices":  []any{},
	})
	if got := b.tool("inbox", nil).text; got != "{\n \"messages\": [],\n \"notices\": []\n}" {
		t.Fatalf("inbox %q", got)
	}
	for _, want := range []activity{
		{Kind: "wait_start", Agent: "bob@vps-2", TimeoutS: 500},
		{Kind: "wait_end", Agent: "bob@vps-2", Result: "message"},
	} {
		eventually(t, fmt.Sprintf("the hub gets %+v", want), func() bool { return slices.Contains(h.activities("s"), want) })
	}
}

func TestWaitEndsWithATimeout(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("s")
	a := start(t, options(h, "mac-1", "s", "alice", false))
	joined(t, a)
	t0 := time.Now()
	if got := a.json("wait", map[string]any{"timeout_s": 3}); !match(got, map[string]any{"timeout": true}) || len(got) != 1 {
		t.Fatalf("wait %v", got)
	}
	if d := time.Since(t0); d < 30*time.Millisecond {
		t.Fatalf("the wait ended after %v, before its timeout", d)
	}
	eventually(t, "the hub gets wait_end timeout", func() bool {
		return slices.Contains(h.activities("s"), activity{Kind: "wait_end", Agent: "alice@mac-1", Result: "timeout"})
	})
}

func TestAskReturnsTheAnswerAndTheAnswerIsNotPushed(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("s")
	a := start(t, options(h, "mac-1", "s", "alice", true))
	b := start(t, options(h, "vps-2", "s", "bob", false))
	joined(t, a)
	joined(t, b)
	asking := a.async("ask", map[string]any{"to": "bob", "text": "shape of /users?", "timeout_s": 500})
	q := b.json("wait", map[string]any{"from": "alice", "timeout_s": 500})
	msgs, _ := q["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("bob's wait %v", q)
	}
	question := fmt.Sprint(msgs[0].(map[string]any)["id"])
	b.json("send", map[string]any{"to": "alice", "text": "{id, name}", "reply_to": question})
	expect(t, decodeResult(t, asking), map[string]any{
		"question": question,
		"answer":   map[string]any{"text": "{id, name}", "from": "bob@vps-2", "reply_to": question},
	})
	time.Sleep(100 * time.Millisecond)
	if len(a.pushes()) != 0 {
		t.Fatalf("the answer was also pushed: %v", a.pushes())
	}
	for _, want := range []activity{
		{Kind: "wait_start", Agent: "alice@mac-1", From: "bob@vps-2", ReplyTo: question, TimeoutS: 500},
		{Kind: "wait_end", Agent: "alice@mac-1", Result: "message"},
	} {
		eventually(t, fmt.Sprintf("the hub gets %+v", want), func() bool { return slices.Contains(h.activities("s"), want) })
	}
}

func TestAskEndsWithATimeout(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("s")
	a := start(t, options(h, "mac-1", "s", "alice", false))
	b := start(t, options(h, "vps-2", "s", "bob", false))
	joined(t, a)
	joined(t, b)
	got := a.json("ask", map[string]any{"to": "bob", "text": "quick?", "timeout_s": 2})
	q := fmt.Sprint(got["question"])
	expect(t, got, map[string]any{"timeout": true, "note": "No answer yet. A late answer arrives as a message with reply_to " + q + "."})
	eventually(t, "the hub gets wait_end timeout", func() bool {
		return slices.Contains(h.activities("s"), activity{Kind: "wait_end", Agent: "alice@mac-1", Result: "timeout"})
	})
}

func TestAskAndWaitFromEndWhenThatPeerLeaves(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("s")
	a := start(t, options(h, "mac-1", "s", "alice", false))
	b := start(t, options(h, "vps-2", "s", "bob", false))
	c := start(t, options(h, "mac-3", "s", "carol", false))
	for _, x := range []*testAgent{a, b, c} {
		joined(t, x)
	}
	asking := a.async("ask", map[string]any{"to": "bob", "text": "still there?", "timeout_s": 500})
	onBob := c.async("wait", map[string]any{"from": "bob", "timeout_s": 500})
	onAny := c.async("wait", map[string]any{"timeout_s": 50})
	eventually(t, "three waits start", func() bool {
		n := 0
		for _, x := range h.activities("s") {
			if x.Kind == "wait_start" {
				n++
			}
		}
		return n == 3
	})
	b.stop()
	expect(t, decodeResult(t, asking), map[string]any{"peer_left": true})
	expect(t, decodeResult(t, onBob), map[string]any{
		"messages": []any{},
		"notices":  []any{map[string]any{"kind": "peer_left", "peer": "bob@vps-2", "text": "bob@vps-2 left the session."}},
	})
	if got := decodeResult(t, onAny); !match(got, map[string]any{"timeout": true}) {
		t.Fatalf("a plain wait ended with %v", got)
	}
	for _, x := range []*testAgent{a, c} {
		if got := x.tool("inbox", nil).text; got != "{\n \"messages\": [],\n \"notices\": []\n}" {
			t.Fatalf("inbox %q", got)
		}
	}
	eventually(t, "the hub gets wait_end cancelled", func() bool {
		return slices.Contains(h.activities("s"), activity{Kind: "wait_end", Agent: "alice@mac-1", Result: "cancelled"})
	})
}

func TestPeerThatLeftSendSaysSoAskReturnsAtOnce(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("s")
	a := start(t, options(h, "mac-1", "s", "alice", false))
	b := start(t, options(h, "vps-2", "s", "bob", false))
	joined(t, a)
	joined(t, b)
	b.stop()
	eventually(t, "status lists bob offline", func() bool {
		return match(a.json("status", nil), map[string]any{"peers": []any{map[string]any{"name": "bob@vps-2", "state": "idle", "online": false}}})
	})
	expect(t, a.json("send", map[string]any{"to": "bob", "text": "read me later"}), map[string]any{"to": "bob@vps-2", "online": false})
	t0 := time.Now()
	r := a.json("ask", map[string]any{"to": "bob", "text": "quick one?", "timeout_s": 600})
	expect(t, r, map[string]any{"peer_offline": true})
	if !strings.Contains(fmt.Sprint(r["note"]), "bob@vps-2") || time.Since(t0) > time.Second {
		t.Fatalf("ask %v after %v", r, time.Since(t0))
	}
}

func TestAskTheUserTheOperatorAnswersInTheThread(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("s")
	a := start(t, options(h, "mac-1", "s", "alice", false))
	joined(t, a)
	asking := a.async("ask", map[string]any{"to": "operator", "text": "may I delete the branch?", "timeout_s": 500})
	eventually(t, "alice waits on the operator", func() bool {
		return slices.ContainsFunc(h.activities("s"), func(x activity) bool { return x.Kind == "wait_start" && x.From == "operator" })
	})
	i := slices.IndexFunc(h.messages("s"), func(m message) bool { return m.To == "operator" })
	if i < 0 {
		t.Fatal("no question to the operator")
	}
	q := h.messages("s")[i].ID
	h.operatorSend("s", "alice@mac-1", "yes, go ahead", q)
	expect(t, decodeResult(t, asking), map[string]any{
		"question": q,
		"answer":   map[string]any{"from": "operator", "text": "yes, go ahead", "reply_to": q},
	})
}

func TestWaitFromIgnoresOtherPeers(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("s")
	a := start(t, options(h, "mac-1", "s", "alice", false))
	b := start(t, options(h, "vps-2", "s", "bob", false))
	joined(t, a)
	joined(t, b)
	b.json("send", map[string]any{"to": "alice", "text": "from bob"})
	eventually(t, "the message is queued", func() bool { return a.json("status", nil)["unread"] == float64(1) })
	if got := a.json("wait", map[string]any{"from": "carol", "timeout_s": 3}); !match(got, map[string]any{"timeout": true}) {
		t.Fatalf("wait %v", got)
	}
	expect(t, a.json("inbox", nil), map[string]any{"messages": []any{map[string]any{"text": "from bob"}}})
}

func TestInboxDrainsTheQueueAndReportsPulls(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("s")
	a := start(t, options(h, "mac-1", "s", "alice", false))
	b := start(t, options(h, "vps-2", "s", "bob", false))
	joined(t, a)
	joined(t, b)
	m1 := b.json("send", map[string]any{"to": "alice", "text": "one"})
	m2 := b.json("send", map[string]any{"to": "all", "text": "two"})
	h.redact("s", fmt.Sprint(m1["id"]))
	eventually(t, "three items are queued", func() bool { return a.json("status", nil)["unread"] == float64(3) })
	expect(t, a.json("inbox", nil), map[string]any{
		"messages": []any{map[string]any{"id": m1["id"], "text": "one"}, map[string]any{"id": m2["id"], "text": "two"}},
		"notices": []any{map[string]any{"kind": "redacted", "id": m1["id"],
			"text": fmt.Sprintf("The user withdrew message %s. Disregard what it said.", m1["id"])}},
	})
	if got := a.tool("inbox", nil).text; got != "{\n \"messages\": [],\n \"notices\": []\n}" {
		t.Fatalf("a second inbox %q", got)
	}
}

func TestTakenNameGetsASuffixInTheSession(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("s")
	a := start(t, options(h, "mac-1", "s", "dev", false))
	b := start(t, options(h, "mac-1", "s", "dev", false))
	names := []string{fmt.Sprint(joined(t, a)["me"]), fmt.Sprint(joined(t, b)["me"])}
	slices.Sort(names)
	if !slices.Equal(names, []string{"dev-2@mac-1", "dev@mac-1"}) {
		t.Fatalf("names %v", names)
	}
}

func TestStatusListsPeersWithTheirState(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("s")
	a := start(t, options(h, "mac-1", "s", "alice", false))
	b := start(t, options(h, "vps-2", "s", "bob", false))
	joined(t, a)
	joined(t, b)
	if got := b.json("set_state", map[string]any{"state": "blocked", "note": "need the schema"}); !match(got, map[string]any{"ok": true}) {
		t.Fatalf("set_state %v", got)
	}
	expect(t, a.json("status", nil), map[string]any{
		"joined": true, "session": "s", "session_open": true, "me": "alice@mac-1",
		"peers":  []any{map[string]any{"name": "bob@vps-2", "state": "blocked", "note": "need the schema", "online": true}},
		"unread": 0,
	})
}

func TestStatusAnswersWhenTheViewFails(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("s")
	a := start(t, options(h, "mac-1", "s", "alice", false))
	joined(t, a)
	h.failView(503)
	got := a.json("status", nil)
	want := map[string]any{"joined": true, "session": "s", "me": "alice@mac-1", "unread": 0, "note": unreachable}
	if !match(got, want) || len(got) != len(want) {
		t.Fatalf("status %v", got)
	}
}

func TestHistoryShowsWhatICanSee(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("s")
	a := start(t, options(h, "mac-1", "s", "alice", false))
	b := start(t, options(h, "vps-2", "s", "bob", false))
	c := start(t, options(h, "mac-3", "s", "carol", false))
	for _, x := range []*testAgent{a, b, c} {
		joined(t, x)
	}
	a.json("send", map[string]any{"to": "bob", "text": "dm to bob"})
	a.json("send", map[string]any{"to": "all", "text": "to all"})
	expect(t, c.json("history", nil), map[string]any{"messages": []any{map[string]any{"text": "to all"}}})
	expect(t, b.json("history", map[string]any{"with": "alice"}), map[string]any{
		"messages": []any{map[string]any{"text": "dm to bob"}, map[string]any{"text": "to all"}},
	})
	expect(t, b.json("history", map[string]any{"with": "alice", "limit": 1}), map[string]any{
		"messages": []any{map[string]any{"text": "to all"}},
	})
}

func TestBadArgumentsAreReportedToTheAgent(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("s")
	a := start(t, options(h, "mac-1", "s", "alice", false))
	joined(t, a)
	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		// The input schema rejects these.
		{"send", map[string]any{"to": "all", "text": ""}, "arguments"},
		{"send", map[string]any{"to": "all", "text": strings.Repeat("x", 8001)}, "arguments"},
		{"send", map[string]any{"to": "all", "text": "x", "extra": 1}, "arguments"},
		{"wait", map[string]any{"timeout_s": 0}, "arguments"},
		{"ask", map[string]any{"to": "bob", "text": "x", "timeout_s": 601}, "arguments"},
		{"set_state", map[string]any{"state": "sleeping"}, "arguments"},
		{"history", map[string]any{"limit": 201}, "arguments"},
		// The name rules reject these.
		{"send", map[string]any{"to": "nobody here", "text": "x"}, "invalid arguments"},
		{"send", map[string]any{"to": "all@m", "text": "x"}, "invalid arguments"},
		{"send", map[string]any{"to": "bob", "text": "x", "reply_to": "07"}, "invalid arguments"},
		{"ask", map[string]any{"to": "all", "text": "x"}, "invalid arguments"},
		{"wait", map[string]any{"from": "all"}, "invalid arguments"},
		{"history", map[string]any{"with": "operator"}, "invalid arguments"},
		// The hub rejects this one.
		{"send", map[string]any{"to": "zed", "text": "hi"}, "no peer zed"},
	} {
		r := a.tool(tc.tool, tc.args)
		if !r.isError || !strings.Contains(r.text, tc.want) {
			t.Errorf("%s %v: %+v, want an error with %q", tc.tool, tc.args, r, tc.want)
		}
	}
}

// --- operator actions as the agent sees them -------------------------------------------------

func TestKickGivesANoticeThenEveryToolSaysRemoved(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("s")
	b := start(t, options(h, "vps-2", "s", "bob", true))
	joined(t, b)
	h.kick("s", "bob@vps-2")
	eventually(t, "bob gets the kicked notice", func() bool {
		return slices.ContainsFunc(b.pushes(), func(p push) bool { return p.Meta["notice"] == noticeKicked })
	})
	p := b.pushes()[0]
	want := push{Content: "The user removed you from the shared session. You can no longer send or receive messages.", Meta: map[string]string{"kind": "notice", "notice": "kicked"}}
	if fmt.Sprint(p) != fmt.Sprint(want) {
		t.Fatalf("push %+v", p)
	}
	eventually(t, "status says removed", func() bool { return b.json("status", nil)["reason"] == "removed from session" })
	if r := b.tool("send", map[string]any{"to": "all", "text": "x"}); !r.isError || r.text != "removed from session" {
		t.Fatalf("send %+v", r)
	}
}

func TestCloseAndReopenGiveNoticesAndSendingFailsWhileClosed(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("s")
	a := start(t, options(h, "mac-1", "s", "alice", true))
	joined(t, a)
	h.setOpen("s", false)
	eventually(t, "alice gets the closed notice", func() bool {
		return slices.ContainsFunc(a.pushes(), func(p push) bool { return p.Meta["notice"] == noticeClosed })
	})
	if r := a.tool("send", map[string]any{"to": "all", "text": "x"}); !r.isError || r.text != "session closed" {
		t.Fatalf("send %+v", r)
	}
	expect(t, a.json("status", nil), map[string]any{"session_open": false})
	h.setOpen("s", true)
	eventually(t, "alice gets the reopened notice", func() bool {
		return slices.ContainsFunc(a.pushes(), func(p push) bool { return p.Meta["notice"] == noticeReopened })
	})
	if r := a.tool("send", map[string]any{"to": "all", "text": "x"}); r.isError {
		t.Fatalf("send %+v", r)
	}
}

func TestRedactTellsTheAgentToDisregardTheMessage(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("s")
	a := start(t, options(h, "mac-1", "s", "alice", false))
	b := start(t, options(h, "vps-2", "s", "bob", true))
	joined(t, a)
	joined(t, b)
	id := fmt.Sprint(a.json("send", map[string]any{"to": "bob", "text": "wrong numbers"})["id"])
	eventually(t, "bob gets the message", func() bool {
		return slices.ContainsFunc(b.pushes(), func(p push) bool { return p.Meta["id"] == id })
	})
	h.redact("s", id)
	eventually(t, "bob gets the redacted notice", func() bool {
		return slices.ContainsFunc(b.pushes(), func(p push) bool {
			return p.Meta["notice"] == noticeRedacted && p.Meta["id"] == id && strings.Contains(p.Content, "withdrew message "+id)
		})
	})
}

func TestOperatorMessageArrivesFromOperator(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("s")
	a := start(t, options(h, "mac-1", "s", "alice", true))
	joined(t, a)
	h.operatorSend("s", "all", "stop and report", "")
	eventually(t, "alice gets the operator's message", func() bool {
		return slices.ContainsFunc(a.pushes(), func(p push) bool { return p.Meta["from"] == "operator" && p.Content == "stop and report" })
	})
}

// --- opacity ---------------------------------------------------------------------------------

// Every test checks its agents' texts at the end (see start). This test makes sure that the
// texts of every tool, every notice and the instructions pass through that check.
func TestNothingAnAgentSeesNamesHowTheServiceWorks(t *testing.T) {
	if forbiddenRE.MatchString(instructions) {
		t.Fatal("the instructions name the service")
	}
	for name, d := range descriptions {
		if forbiddenRE.MatchString(d) {
			t.Fatalf("the description of %s names the service", name)
		}
	}
	for _, kind := range []string{noticeKicked, noticeClosed, noticeReopened, noticeRedacted, noticePeerLeft} {
		if text := noticeText(notice{Kind: kind, ID: "1", Peer: "a@m"}); text == "" || forbiddenRE.MatchString(text) {
			t.Fatalf("notice %s: %q", kind, text)
		}
	}
	for _, l := range []link{{kind: linkJoining}, {kind: linkNoSession}, {kind: linkClosed}, {kind: linkRemoved}, {kind: linkRefused}, {kind: linkUnreachable}} {
		if text := reason(false, l); text == "" || forbiddenRE.MatchString(text) {
			t.Fatalf("reason %s: %q", l.kind, text)
		}
	}
	if forbiddenRE.MatchString(reason(true, link{})) {
		t.Fatal("the reason for a machine without set-up names the service")
	}
	h := newFakeHub(t)
	h.createSession("s")
	a := start(t, options(h, "mac-1", "s", "alice", true))
	b := start(t, options(h, "vps-2", "s", "bob", false))
	joined(t, a)
	joined(t, b)
	b.json("send", map[string]any{"to": "alice", "text": "hello"})
	b.json("set_state", map[string]any{"state": "working"})
	a.json("history", nil)
	b.json("inbox", nil)
	b.json("wait", map[string]any{"timeout_s": 1})
	b.json("ask", map[string]any{"to": "alice", "text": "x", "timeout_s": 1})
	b.tool("send", map[string]any{"to": "all", "text": ""})
	h.setOpen("s", false)
	h.setOpen("s", true)
	eventually(t, "alice gets the notices", func() bool { return len(a.pushes()) >= 4 })
	a.mu.Lock()
	n := len(a.seen)
	a.mu.Unlock()
	if n < 8 {
		t.Fatalf("alice saw only %d texts", n)
	}
}
