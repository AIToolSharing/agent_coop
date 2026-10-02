package shim

import (
	"bufio"
	"context"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Claude Code ends an MCP server by closing its stdin. Serve must then return, with the
// stream to the hub and every report ended, so that no process stays behind.
func TestServeEndsWhenStdinCloses(t *testing.T) {
	h := newFakeHub(t)
	h.createSession("demo")
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	o := options(h, "mac-1", "demo", "alice", true)
	o.Transport = &mcp.IOTransport{Reader: inR, Writer: outW}
	served := make(chan error, 1)
	go func() { served <- Serve(context.Background(), o) }()
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	write := func(s string) {
		if _, err := io.WriteString(inW, s+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"pipe","version":"1"}}}`)
	select {
	case l := <-lines:
		if !strings.Contains(l, `"id":1`) {
			t.Fatalf("first answer %q", l)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no answer to initialize")
	}
	write(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	eventually(t, "the agent joins the session", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		s := h.sessions["demo"]
		if s == nil {
			return false
		}
		for _, a := range s.agents {
			if a.online {
				return true
			}
		}
		return false
	})
	_ = inW.Close()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(3 * time.Second):
		buf := make([]byte, 1<<18)
		n := runtime.Stack(buf, true)
		t.Fatalf("Serve did not end 3 s after stdin closed; goroutines:\n%s", buf[:n])
	}
	_ = outW.Close()
}
