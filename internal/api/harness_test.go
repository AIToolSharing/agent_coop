package api_test

// A real hub on a free port with a fresh store, as packages/hub/test/harness.ts starts one.

import (
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	coopapi "github.com/AIToolSharing/agent_coop/internal/api"
	"github.com/AIToolSharing/agent_coop/internal/hub"
	"github.com/AIToolSharing/agent_coop/internal/store"
	"github.com/AIToolSharing/agent_coop/internal/wire"
)

type limit struct {
	burst     int
	perSecond float64
}

// limits are the rate limits of a test hub; a zero limit means the hub's default.
type limits struct{ join, msg, activity limit }

// options of a test hub; autoCreate false means the operator creates sessions.
// holdNew true means a session that the hub makes holds new agents. version is the version
// that the hub reports, and dist is the directory of the binaries that it gives to devices.
type options struct {
	autoCreate, holdNew bool
	version, dist       string
}

// testPing is the hub's ping and sweep interval in tests, so that a revoked token or a
// changed session shows within a second.
const testPing = 200 * time.Millisecond

type harness struct {
	base string
	t    *testing.T
	st   *store.Store
	l    limits
	o    options

	mu      sync.Mutex
	h       *hub.Hub
	srv     *http.Server
	addr    string
	running bool
}

// startHub starts a hub on a free port with a fresh SQLite store in t.TempDir(), and stops it
// in t.Cleanup.
func startHub(t *testing.T, l limits, o options) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "coop.db"))
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, st: st, l: l, o: o, addr: "127.0.0.1:0"}
	h.start()
	t.Cleanup(func() {
		h.stop()
		_ = st.Close()
	})
	return h
}

func (h *harness) start() {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	ln, err := net.Listen("tcp", h.addr)
	if err != nil {
		h.t.Fatal(err)
	}
	h.addr = ln.Addr().String()
	h.base = "http://" + h.addr
	conv := func(l limit) hub.Limit { return hub.Limit{Burst: l.burst, PerSecond: l.perSecond} }
	h.h = hub.New(h.st, hub.Options{
		AutoCreate: h.o.autoCreate,
		HoldNew:    h.o.holdNew,
		Ping:       testPing,
		Limits:     hub.Limits{Join: conv(h.l.join), Msg: conv(h.l.msg), Activity: conv(h.l.activity)},
	})
	srv := coopapi.New(h.h, h.t.Logf)
	srv.Version, srv.Dist = h.o.version, h.o.dist
	h.srv = &http.Server{Handler: srv}
	go func() { _ = h.srv.Serve(ln) }()
	h.running = true
}

// stop stops the hub (t.Cleanup also does it).
func (h *harness) stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.running {
		return
	}
	h.running = false
	h.h.Close()
	_ = h.srv.Close()
}

// restart stops the hub and starts a new one on the same port and the same store, as a
// restart of the service would.
func (h *harness) restart() {
	h.stop()
	h.start()
}

func (h *harness) now() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00") }

// token issues a machine token; it replaces the old token of that name.
func (h *harness) token(machine string) string {
	h.t.Helper()
	tok, err := h.st.IssueToken(machine, "machine", h.now())
	if err != nil {
		h.t.Fatal(err)
	}
	return tok
}

// operatorToken issues an operator token.
func (h *harness) operatorToken(name string) string {
	h.t.Helper()
	tok, err := h.st.IssueToken(name, "operator", h.now())
	if err != nil {
		h.t.Fatal(err)
	}
	return tok
}

// revoke revokes a token (as the CLI on the server does). The hub notices a revoked token at
// its next ping tick.
func (h *harness) revoke(name string) {
	h.t.Helper()
	if _, err := h.st.RevokeToken(name, h.now()); err != nil {
		h.t.Fatal(err)
	}
}

// tokenRoles gives the role of every token by name.
func (h *harness) tokenRoles() map[string]string {
	h.t.Helper()
	rows, err := h.st.Tokens()
	if err != nil {
		h.t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range rows {
		out[r.Name] = r.Role
	}
	return out
}

// presence gives the hub's live presence record of key "<sid>.<machine>.<agent>", or false.
func (h *harness) presence(key string) (wire.PresenceRecord, bool) {
	h.mu.Lock()
	hb := h.h
	h.mu.Unlock()
	return hb.Presence(key)
}

// events gives every stored event of a session, oldest first.
func (h *harness) events(sid string) []wire.Event {
	h.t.Helper()
	last, err := h.st.LastSeq()
	if err != nil {
		h.t.Fatal(err)
	}
	evs, err := h.st.Events(sid, 1, last)
	if err != nil {
		h.t.Fatal(err)
	}
	return evs
}
