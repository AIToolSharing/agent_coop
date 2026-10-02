package main

// The Stage 1a spike. It answers three questions about Claude Code and the Go MCP SDK:
//  1. does a channel notification written outside a tool call reach the Claude session,
//  2. what the working directory of a user-scope stdio server is,
//  3. whether the push reaches `claude -p`.
// It writes what it sees to ~/.config/coop/spike.log, because the server's stderr is hard to
// read from inside Claude Code.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/channel"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// loggedTransport writes every message of the session to the spike log, and calls onInit
// once, when the first request arrives.
type loggedTransport struct {
	inner  mcp.Transport
	log    *spikeLog
	onInit func()
	once   sync.Once
}

func (t *loggedTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	t.log.Printf("connect")
	c, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &loggedConn{Connection: c, log: t.log, onInit: func() { t.once.Do(t.onInit) }}, nil
}

type loggedConn struct {
	mcp.Connection
	log    *spikeLog
	onInit func()
}

// classic is set by COOP_SPIKE_CLASSIC=1: the server then answers server/discover with
// "method not found", so the client falls back to the initialize handshake of the older
// protocol versions (what the TS SDK 1.x does, and what Claude Code's channels were built on).
var classic = os.Getenv("COOP_SPIKE_CLASSIC") == "1"

func (c *loggedConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	for {
		m, err := c.Connection.Read(ctx)
		if err != nil {
			c.log.Printf("read err=%v", err)
			return m, err
		}
		switch r := m.(type) {
		case *jsonrpc.Request:
			c.log.Printf("read request method=%s id=%v params=%s", r.Method, r.ID, clip(r.Params, 300))
			c.onInit()
			if classic && r.Method == "server/discover" && r.ID.IsValid() {
				resp := &jsonrpc.Response{ID: r.ID, Error: &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "method not found"}}
				if err := c.Write(ctx, resp); err != nil {
					return nil, err
				}
				continue
			}
		case *jsonrpc.Response:
			c.log.Printf("read response id=%v", r.ID)
		}
		return m, nil
	}
}

// Write logs what the server sends: responses and notifications.
func (c *loggedConn) Write(ctx context.Context, m jsonrpc.Message) error {
	switch r := m.(type) {
	case *jsonrpc.Request:
		c.log.Printf("write request method=%s id=%v params=%s", r.Method, r.ID, clip(r.Params, 300))
	case *jsonrpc.Response:
		if r.Error != nil {
			c.log.Printf("write response id=%v error=%v", r.ID, r.Error)
		} else {
			c.log.Printf("write response id=%v result=%s", r.ID, clip(r.Result, 400))
		}
	}
	return c.Connection.Write(ctx, m)
}

func clip(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "…"
	}
	return string(b)
}

type spikeLog struct {
	mu sync.Mutex
	f  *os.File
}

func openSpikeLog() (*spikeLog, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".config", "coop")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "spike.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &spikeLog{f: f}, nil
}

func (l *spikeLog) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.f, time.Now().Format(time.RFC3339)+" "+format+"\n", args...)
}

type waitIn struct {
	Seconds int `json:"seconds" jsonschema:"how long to wait, in seconds (1 to 60)"`
}

func runSpike(args []string) error {
	log, err := openSpikeLog()
	if err != nil {
		return err
	}
	defer log.f.Close()
	cwd, _ := os.Getwd()
	log.Printf("start pid=%d ppid=%d cwd=%q args=%q", os.Getpid(), os.Getppid(), cwd, args)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "CLAUDE") {
			log.Printf("env %s", e)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var tr *channel.Transport
	var pushes atomic.Int64
	push := func(content string) {
		n := pushes.Add(1)
		meta := map[string]string{"kind": "message", "from": "spike", "id": strconv.FormatInt(n, 10)}
		err := tr.Push(ctx, content, meta)
		log.Printf("push %d %q err=%v", n, content, err)
	}
	// One push every five seconds, from two seconds after the first request: by then the
	// handshake is over. (Claude Code 2.1.287 sends server/discover, not initialize.)
	startPushes := func() {
		log.Printf("first request seen; pushes start in 2 s")
		go func() {
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			for i := 1; ; i++ {
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
				}
				push(fmt.Sprintf("spike ping %d at %s", i, time.Now().Format("15:04:05")))
				timer.Reset(5 * time.Second)
			}
		}()
	}
	tr = channel.Wrap(&loggedTransport{inner: &mcp.StdioTransport{}, log: log, onInit: startPushes})

	server := mcp.NewServer(&mcp.Implementation{Name: "coop", Version: "spike"}, &mcp.ServerOptions{
		Instructions: "The coop channel spike. Call spike_wait when asked; messages may arrive on the coop channel.",
		Capabilities: &mcp.ServerCapabilities{
			Experimental: map[string]any{channel.Capability: map[string]any{}},
		},
		InitializedHandler: func(_ context.Context, _ *mcp.InitializedRequest) {
			log.Printf("initialized handler fired")
		},
	})
	mcp.AddTool(server, &mcp.Tool{Name: "spike_wait", Description: "Wait for some seconds, then report."},
		func(_ context.Context, _ *mcp.CallToolRequest, in waitIn) (*mcp.CallToolResult, any, error) {
			log.Printf("tool spike_wait seconds=%d", in.Seconds)
			s := min(max(in.Seconds, 1), 60)
			time.Sleep(time.Duration(s) * time.Second)
			text := fmt.Sprintf("waited %d s; server cwd=%s; pushes so far=%d", s, cwd, pushes.Load())
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "spike_status", Description: "Report the server's working directory and push count."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			log.Printf("tool spike_status")
			text := fmt.Sprintf("server cwd=%s; pushes so far=%d", cwd, pushes.Load())
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
		})

	err = server.Run(ctx, tr)
	log.Printf("run ended err=%v", err)
	return nil
}
