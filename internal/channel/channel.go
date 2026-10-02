// Package channel sends Claude Code channel notifications over an MCP connection.
//
// Claude Code reads `notifications/claude/channel` from an MCP server that advertised the
// experimental capability `claude/channel`. The notification can arrive at any time, outside a
// tool call. The MCP SDK gives a server no way to send a custom notification, so Transport keeps
// the connection it opened and writes the notification on it directly. Connection.Write may be
// called concurrently, so the server's own replies and a Push do not conflict.
package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Capability is the key Claude Code looks for under `capabilities.experimental`.
const Capability = "claude/channel"

// Method is the notification method Claude Code listens for.
const Method = "notifications/claude/channel"

// metaKey is what Claude Code accepts as a meta key. It drops other keys without a word.
var metaKey = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// Transport is an MCP transport that remembers the connection it opens.
type Transport struct {
	inner mcp.Transport

	once  sync.Once
	ready chan struct{}
	mu    sync.Mutex
	conn  mcp.Connection
}

// Wrap returns a Transport over inner.
func Wrap(inner mcp.Transport) *Transport {
	return &Transport{inner: inner, ready: make(chan struct{})}
}

// Connect opens the inner transport and keeps the connection for Push.
func (t *Transport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.conn = conn
	t.mu.Unlock()
	t.once.Do(func() { close(t.ready) })
	return conn, nil
}

type params struct {
	Content string            `json:"content"`
	Meta    map[string]string `json:"meta"`
}

// Push sends one channel notification. It waits until Connect has run, or until ctx ends.
// Every meta key must match [A-Za-z0-9_]+; any other key is an error here, because Claude Code
// would drop it in silence.
func (t *Transport) Push(ctx context.Context, content string, meta map[string]string) error {
	for k := range meta {
		if !metaKey.MatchString(k) {
			return fmt.Errorf("channel: meta key %q: only letters, digits and _ are allowed", k)
		}
	}
	if meta == nil {
		meta = map[string]string{}
	}
	raw, err := json.Marshal(params{Content: content, Meta: meta})
	if err != nil {
		return err
	}
	select {
	case <-t.ready:
	case <-ctx.Done():
		return fmt.Errorf("channel: no connection yet: %w", ctx.Err())
	}
	t.mu.Lock()
	conn := t.conn
	t.mu.Unlock()
	return conn.Write(ctx, &jsonrpc.Request{Method: Method, Params: raw})
}
