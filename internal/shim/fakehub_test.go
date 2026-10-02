package shim

// A fake hub: the agent routes of packages/hub/src/app.ts, well enough for the shim's tests.
// The tests act as the operator through its methods.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/wire"
)

type sseOut struct {
	event, id string
	data      any
	// last ends the stream after this event.
	last bool
}

type fakeAgent struct {
	addr     string
	instance string
	online   bool
	state    string
	note     string
	out      chan sseOut // events for the open stream
}

type fakeSession struct {
	open   bool
	agents map[string]*fakeAgent // by address
	kicked map[string]bool
	msgs   []message
	acts   []activity // Agent holds the address
}

type fakeHub struct {
	srv      *httptest.Server
	machines map[string]string // token to machine
	ping     time.Duration

	mu       sync.Mutex
	seq      int
	sessions map[string]*fakeSession
	// query is the query of the last stream request.
	query string
}

func (h *fakeHub) lastQuery() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.query
}

func newFakeHub(t *testing.T) *fakeHub {
	t.Helper()
	h := &fakeHub{
		machines: map[string]string{"t-mac-1": "mac-1", "t-vps-2": "vps-2", "t-mac-3": "mac-3"},
		ping:     50 * time.Millisecond,
		sessions: map[string]*fakeSession{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions/{sid}/stream", h.stream)
	mux.HandleFunc("POST /v1/sessions/{sid}/messages", h.send)
	mux.HandleFunc("GET /v1/sessions/{sid}/messages", h.history)
	mux.HandleFunc("POST /v1/sessions/{sid}/activity", h.activity)
	mux.HandleFunc("GET /v1/sessions/{sid}", h.view)
	h.srv = httptest.NewServer(mux)
	t.Cleanup(h.srv.Close)
	return h
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (h *fakeHub) nextID() string {
	h.seq++
	return strconv.Itoa(h.seq)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// auth gives the machine of the request's token, or answers 401.
func (h *fakeHub) auth(w http.ResponseWriter, r *http.Request) (string, bool) {
	m, ok := h.machines[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if !ok {
		writeError(w, 401, "unauthorized", "missing or invalid token")
	}
	return m, ok
}

// session gives the session of the request with the lock held, or answers 404 and unlocks.
func (h *fakeHub) session(w http.ResponseWriter, r *http.Request) (*fakeSession, bool) {
	h.mu.Lock()
	s := h.sessions[r.PathValue("sid")]
	if s == nil {
		h.mu.Unlock()
		writeError(w, 404, "not_found", "no session "+r.PathValue("sid"))
	}
	return s, s != nil
}

// emit sends an event to every online agent of s that keep accepts. The caller holds the lock.
func (s *fakeSession) emit(ev sseOut, keep func(*fakeAgent) bool) {
	for _, a := range s.agents {
		if a.online && keep(a) {
			a.out <- ev
		}
	}
}

func (h *fakeHub) stream(w http.ResponseWriter, r *http.Request) {
	machine, ok := h.auth(w, r)
	if !ok {
		return
	}
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	h.query = r.URL.RawQuery
	q := r.URL.Query()
	addr := q.Get("agent") + "@" + machine
	a := s.agents[addr]
	switch {
	case !s.open:
		h.mu.Unlock()
		writeError(w, 403, "forbidden", "session closed")
		return
	case s.kicked[addr]:
		h.mu.Unlock()
		writeError(w, 403, "forbidden", "removed from session")
		return
	case a != nil && a.online && a.instance != q.Get("instance"):
		h.mu.Unlock()
		writeError(w, 409, "conflict", "name "+addr+" is taken")
		return
	}
	if a == nil {
		a = &fakeAgent{addr: addr, state: "idle"}
		s.agents[addr] = a
	}
	out := make(chan sseOut, 256)
	a.instance, a.online, a.out = q.Get("instance"), true, out
	h.mu.Unlock()

	openStream(w)
	writeEvent(w, "joined", "", joinedEvent{Me: addr, Session: r.PathValue("sid")})
	tick := time.NewTicker(h.ping)
	defer tick.Stop()
	defer h.left(s, a, out)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
			w.(http.Flusher).Flush()
		case ev := <-out:
			writeEvent(w, ev.event, ev.id, ev.data)
			if ev.last {
				return
			}
		}
	}
}

// left marks a as offline when out is still its stream, and tells the other agents.
func (h *fakeHub) left(s *fakeSession, a *fakeAgent, out chan sseOut) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if a.out != out {
		return
	}
	a.online, a.out = false, nil
	ev := sseOut{event: "notice", id: h.nextID(), data: notice{Kind: noticePeerLeft, Peer: a.addr, At: now()}}
	s.emit(ev, func(*fakeAgent) bool { return true })
}

func decodeBody[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var v T
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		writeError(w, 422, "invalid", err.Error())
		return v, false
	}
	return v, true
}

func (h *fakeHub) send(w http.ResponseWriter, r *http.Request) {
	machine, ok := h.auth(w, r)
	if !ok {
		return
	}
	req, ok := decodeBody[sendRequest](w, r)
	if !ok {
		return
	}
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	from := req.Agent + "@" + machine
	if !s.open {
		h.mu.Unlock()
		writeError(w, 403, "forbidden", "session closed")
		return
	}
	to, online := req.To, true
	if to != wire.Broadcast && to != wire.Operator {
		var found []*fakeAgent
		for _, a := range s.agents {
			if matchesPeer(req.To, a.addr) {
				found = append(found, a)
			}
		}
		switch {
		case len(found) == 0:
			h.mu.Unlock()
			writeError(w, 404, "not_found", "no peer "+req.To)
			return
		case len(found) > 1:
			h.mu.Unlock()
			writeError(w, 409, "ambiguous", "more than one peer")
			return
		case found[0].addr == from:
			h.mu.Unlock()
			writeError(w, 409, "conflict", "cannot address yourself")
			return
		}
		to, online = found[0].addr, found[0].online
	}
	m := message{ID: h.nextID(), From: from, To: to, Text: req.Text, ReplyTo: req.ReplyTo, SentAt: now()}
	s.msgs = append(s.msgs, m)
	s.emit(sseOut{event: "message", id: m.ID, data: m}, func(a *fakeAgent) bool {
		return a.addr != from && (to == wire.Broadcast || to == a.addr)
	})
	h.mu.Unlock()
	writeJSON(w, 200, sendResponse{ID: m.ID, To: to, Online: online, SentAt: m.SentAt})
}

func (h *fakeHub) activity(w http.ResponseWriter, r *http.Request) {
	machine, ok := h.auth(w, r)
	if !ok {
		return
	}
	act, ok := decodeBody[activity](w, r)
	if !ok {
		return
	}
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	act.Agent += "@" + machine
	s.acts = append(s.acts, act)
	if a := s.agents[act.Agent]; a != nil && act.Kind == "state" {
		a.state, a.note = act.State, act.Note
	}
	h.mu.Unlock()
	w.WriteHeader(204)
}

func (h *fakeHub) view(w http.ResponseWriter, r *http.Request) {
	machine, ok := h.auth(w, r)
	if !ok {
		return
	}
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	me := r.URL.Query().Get("agent") + "@" + machine
	v := sessionView{Session: r.PathValue("sid"), Status: "closed", Me: me, Peers: []peer{}}
	if s.open {
		v.Status = "open"
	}
	for _, a := range s.agents {
		if a.addr != me {
			v.Peers = append(v.Peers, peer{Name: a.addr, State: a.state, Note: a.note, Online: a.online})
		}
	}
	h.mu.Unlock()
	slices.SortFunc(v.Peers, func(a, b peer) int { return strings.Compare(a.Name, b.Name) })
	writeJSON(w, 200, v)
}

func (h *fakeHub) history(w http.ResponseWriter, r *http.Request) {
	machine, ok := h.auth(w, r)
	if !ok {
		return
	}
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	me, with := q.Get("agent")+"@"+machine, q.Get("with")
	limit, _ := strconv.Atoi(q.Get("limit"))
	out := []message{}
	for _, m := range s.msgs {
		visible := m.From == me || m.To == me || m.To == wire.Broadcast
		if visible && (with == "" || matchesPeer(with, m.From) || matchesPeer(with, m.To)) {
			out = append(out, m)
		}
	}
	h.mu.Unlock()
	writeJSON(w, 200, historyResponse{Messages: out[max(0, len(out)-limit):]})
}

// --- The operator's actions ------------------------------------------------------------------

func (h *fakeHub) createSession(sid string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessions[sid] = &fakeSession{open: true, agents: map[string]*fakeAgent{}, kicked: map[string]bool{}}
}

// operatorSend writes a message as the operator and gives its id.
func (h *fakeHub) operatorSend(sid, to, text, replyTo string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[sid]
	m := message{ID: h.nextID(), From: wire.Operator, To: to, Text: text, ReplyTo: replyTo, SentAt: now()}
	s.msgs = append(s.msgs, m)
	s.emit(sseOut{event: "message", id: m.ID, data: m}, func(a *fakeAgent) bool {
		return to == wire.Broadcast || to == a.addr
	})
	return m.ID
}

func (h *fakeHub) kick(sid, addr string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[sid]
	s.kicked[addr] = true
	ev := sseOut{event: "notice", id: h.nextID(), data: notice{Kind: noticeKicked, At: now()}, last: true}
	s.emit(ev, func(a *fakeAgent) bool { return a.addr == addr })
}

func (h *fakeHub) setOpen(sid string, open bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[sid]
	s.open = open
	kind := noticeClosed
	if open {
		kind = noticeReopened
	}
	s.emit(sseOut{event: "notice", data: notice{Kind: kind, At: now()}}, func(*fakeAgent) bool { return true })
}

func (h *fakeHub) redact(sid, id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ev := sseOut{event: "notice", id: h.nextID(), data: notice{Kind: noticeRedacted, ID: id, At: now()}}
	h.sessions[sid].emit(ev, func(*fakeAgent) bool { return true })
}

// activities gives what the agents reported, in order.
func (h *fakeHub) activities(sid string) []activity {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.sessions[sid].acts)
}

// messages gives every message of the session, in order.
func (h *fakeHub) messages(sid string) []message {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.sessions[sid].msgs)
}
