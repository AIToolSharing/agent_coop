package channel_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/channel"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"pgregory.net/rapid"
)

// A transport whose output the test reads line by line. Its input never ends.
func pipeTransport(t *testing.T) (*channel.Transport, *bufio.Reader) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	t.Cleanup(func() {
		_ = inW.Close()
		_ = outR.Close()
	})
	tr := channel.Wrap(&mcp.IOTransport{Reader: inR, Writer: outW})
	return tr, bufio.NewReader(outR)
}

type wire struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  struct {
		Content string            `json:"content"`
		Meta    map[string]string `json:"meta"`
	} `json:"params"`
}

// readWire reads one line, or fails the test after two seconds of silence.
func readWire(t *testing.T, r *bufio.Reader) wire {
	t.Helper()
	type res struct {
		line string
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		line, err := r.ReadString('\n')
		ch <- res{line, err}
	}()
	var line string
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatalf("read: %v", got.err)
		}
		line = got.line
	case <-time.After(2 * time.Second):
		t.Fatal("nothing was written within two seconds")
	}
	var w wire
	if err := json.Unmarshal([]byte(line), &w); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	return w
}

func TestPushWritesOneNotification(t *testing.T) {
	tr, out := pipeTransport(t)
	ctx := context.Background()
	if _, err := tr.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := tr.Push(ctx, "hello", map[string]string{"kind": "message", "from": "alice@mac"}); err != nil {
			t.Error(err)
		}
	}()
	w := readWire(t, out)
	if w.JSONRPC != "2.0" || w.Method != channel.Method {
		t.Fatalf("got %+v", w)
	}
	if len(w.ID) != 0 {
		t.Fatalf("a notification has no id, got %s", w.ID)
	}
	if w.Params.Content != "hello" || w.Params.Meta["kind"] != "message" || w.Params.Meta["from"] != "alice@mac" {
		t.Fatalf("params %+v", w.Params)
	}
}

func TestPushWaitsForTheConnection(t *testing.T) {
	tr, out := pipeTransport(t)
	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- tr.Push(ctx, "early", map[string]string{"kind": "notice"}) }()
	select {
	case err := <-done:
		t.Fatalf("Push returned before Connect: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := tr.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if w := readWire(t, out); w.Params.Content != "early" {
		t.Fatalf("got %+v", w)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPushGivesUpWhenTheContextEnds(t *testing.T) {
	tr, _ := pipeTransport(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := tr.Push(ctx, "late", nil); err == nil {
		t.Fatal("expected an error")
	}
}

func TestPushRefusesMetaKeysClaudeWouldDrop(t *testing.T) {
	tr, _ := pipeTransport(t)
	ctx := context.Background()
	if _, err := tr.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"reply-to", "", "a b", "ümlaut"} {
		if err := tr.Push(ctx, "x", map[string]string{key: "1"}); err == nil {
			t.Errorf("key %q: expected an error", key)
		}
	}
}

func TestPushRoundTrip(t *testing.T) {
	tr, out := pipeTransport(t)
	ctx := context.Background()
	if _, err := tr.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	key := rapid.StringMatching(`^[A-Za-z0-9_]{1,12}$`)
	rapid.Check(t, func(rt *rapid.T) {
		content := rapid.String().Draw(rt, "content")
		meta := rapid.MapOfN(key, rapid.String(), 0, 5).Draw(rt, "meta")
		go func() { _ = tr.Push(ctx, content, meta) }()
		w := readWire(t, out)
		if w.Params.Content != content {
			rt.Fatalf("content %q != %q", w.Params.Content, content)
		}
		if len(w.Params.Meta) != len(meta) {
			rt.Fatalf("meta %v != %v", w.Params.Meta, meta)
		}
		for k, v := range meta {
			if w.Params.Meta[k] != v {
				rt.Fatalf("meta[%q] %q != %q", k, w.Params.Meta[k], v)
			}
		}
		if strings.Contains(w.Method, "\n") {
			rt.Fatal("method with a newline")
		}
	})
}

// With Classic set, a server/discover request is answered "method not found" on the spot, so
// the client falls back to the initialize handshake; nothing else is touched.
func TestClassicAnswersDiscoverWithMethodNotFound(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	t.Cleanup(func() { _ = inW.Close(); _ = outR.Close() })
	tr := channel.Wrap(&mcp.IOTransport{Reader: inR, Writer: outW})
	tr.Classic = true
	ctx := context.Background()
	conn, err := tr.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","id":"server-discover-probe-1","method":"server/discover","params":{}}`+"\n")
		_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{}}`+"\n")
	}()
	// The client side reads the answer as it comes (a pipe write blocks until it is read).
	answer := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(outR).ReadString('\n')
		answer <- line
	}()
	// The SDK side reads: it must see tools/list, not server/discover.
	m, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	req, ok := m.(*jsonrpc.Request)
	if !ok || req.Method != "tools/list" {
		t.Fatalf("the server read %#v", m)
	}
	var line string
	select {
	case line = <-answer:
	case <-time.After(2 * time.Second):
		t.Fatal("no answer to the probe")
	}
	var resp struct {
		ID    any `json:"id"`
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID != "server-discover-probe-1" || resp.Error.Code != -32601 {
		t.Fatalf("answer %s", line)
	}
}
