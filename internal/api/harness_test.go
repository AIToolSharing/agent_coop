package api_test

// A real hub on a free port with a fresh store, as packages/hub/test/harness.ts starts one.

import (
	"database/sql"
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

// options of a test hub: version is the version that the hub reports, dist is the directory
// of the binaries that it gives to devices, and secret is the secret of the hub ("" means
// testSecret).
type options struct {
	version, dist, secret string
}

// testSecret is the secret of a test hub with no other.
const testSecret = "s3cret"

// testPing is the hub's ping and sweep interval in tests, so that a changed session shows
// within a second.
const testPing = 200 * time.Millisecond

type harness struct {
	base string
	t    *testing.T
	path string // the database file
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
	path := filepath.Join(t.TempDir(), "coop.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if o.secret == "" {
		o.secret = testSecret
	}
	h := &harness{t: t, path: path, st: st, l: l, o: o, addr: "127.0.0.1:0"}
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
		Secret: h.o.secret,
		Ping:   testPing,
		Limits: hub.Limits{Join: conv(h.l.join), Msg: conv(h.l.msg), Activity: conv(h.l.activity)},
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

// token gives the token of a machine: `<machine>.<secret>`.
func (h *harness) token(machine string) string {
	return machine + "." + h.o.secret
}

// presence gives the hub's live presence record of key "<sid>.<machine>.<agent>", or false.
func (h *harness) presence(key string) (wire.PresenceRecord, bool) {
	h.mu.Lock()
	hb := h.h
	h.mu.Unlock()
	return hb.Presence(key)
}

// appendRaw writes one row into the event log as an older hub wrote it, past the store's
// encoder.
func (h *harness) appendRaw(sid, subject, payload string) {
	h.t.Helper()
	db, err := sql.Open("sqlite", h.path)
	if err != nil {
		h.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO events (sid, kind, subject, payload) VALUES (?, 'kick', ?, ?)`, sid, subject, []byte(payload)); err != nil {
		h.t.Fatal(err)
	}
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
