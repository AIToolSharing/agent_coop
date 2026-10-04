// Package hub holds the hub's rules apart from HTTP: who a request comes from, what each agent
// may see, and what each agent and the operator receive. It ports packages/hub/src/hub.ts;
// the SQLite store stands in for the NATS stream and buckets, and connections fan events out
// in process.
//
// One mutex orders every write: an event goes into the store and to every live connection and
// feed in the same critical section, and a join reads the last sequence in that section too.
// So a connection gets every event after its catch-up point from its channel, and every event
// up to it from the store, with no gap and no repeat.
package hub

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/store"
	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// --- Errors ------------------------------------------------------------------------------------

// Error is an error that the API returns as {error, message}.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// Status is the HTTP status of the code.
func (e *Error) Status() int {
	switch e.Code {
	case "unauthorized":
		return 401
	case "forbidden":
		return 403
	case "not_found":
		return 404
	case "conflict", "ambiguous":
		return 409
	case "too_large":
		return 413
	case "invalid":
		return 422
	case "rate_limited":
		return 429
	}
	return 503
}

func errf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// --- Options -----------------------------------------------------------------------------------

// Limit is a token bucket: burst calls at once, then perSecond more.
type Limit struct {
	Burst     int
	PerSecond float64
}

// Limits are the rate limits per machine. A machine runs a few agents; these leave room for
// normal use.
type Limits struct {
	Join, Msg, Activity Limit
}

// DefaultLimits are the limits of the TypeScript hub.
var DefaultLimits = Limits{
	Join:     Limit{Burst: 10, PerSecond: 1},
	Msg:      Limit{Burst: 30, PerSecond: 5},
	Activity: Limit{Burst: 120, PerSecond: 20},
}

// Options configures a Hub.
type Options struct {
	// AutoCreate lets the first join of an unknown session create it, open. A closed session
	// stays closed.
	AutoCreate bool
	// HoldNew is the hold setting of a session that the hub makes: an agent that joins such a
	// session for the first time is held until the operator releases it.
	HoldNew bool
	// Ping is the interval of the SSE ping, and of the check of each live connection's token
	// and session. Zero means 15 s.
	Ping time.Duration
	// Limits are the rate limits; a zero limit means the default.
	Limits Limits
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
}

// ReplayMax is how many missed messages an agent gets when it joins again without a resume
// point.
const ReplayMax = 100

const defaultPing = 15 * time.Second

// --- API values --------------------------------------------------------------------------------

// StreamQuery is the query of a join.
type StreamQuery struct {
	Agent string
	// Instance is random per shim process. A join with the same instance replaces the old
	// stream (a resume).
	Instance                  string
	Host, Cwd                 string
	ClientName, ClientVersion string
	// Gated is true when the agent's tool calls go through the gate (started by `coop claude`).
	Gated bool
}

// Message is a message as an agent gets it.
type Message struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Text    string `json:"text"`
	ReplyTo string `json:"reply_to,omitempty"`
	SentAt  string `json:"sent_at"`
}

// Notice is something the agent must know that is not a message.
type Notice struct {
	Kind string `json:"kind"` // kicked closed reopened redacted peer_left held paused released
	ID   string `json:"id,omitempty"`
	Peer string `json:"peer,omitempty"`
	At   string `json:"at"`
}

type SendRequest struct {
	Agent, To, Text, ReplyTo string
}

type SendResponse struct {
	ID string `json:"id"`
	To string `json:"to"`
	// Online is false when the recipient left the session: it gets the message when it joins
	// again.
	Online bool   `json:"online"`
	SentAt string `json:"sent_at"`
	// State is the recipient's state when it is a peer: working, blocked, done or idle while
	// it is in the session, else away. Empty for all and operator.
	State string `json:"state,omitempty"`
}

// ActivityRequest is what an agent reports: state, wait_start or wait_end.
type ActivityRequest struct {
	Kind, Agent   string
	State, Note   string // state
	From, ReplyTo string // wait_start
	TimeoutS      int    // wait_start
	Result        string // wait_end
}

type Peer struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Note  string `json:"note,omitempty"`
	// Online is false for a peer that left; it is listed with its last state and can still be
	// written to.
	Online    bool   `json:"online"`
	WaitingOn string `json:"waiting_on,omitempty"`
}

type SessionView struct {
	Session string `json:"session"`
	Status  string `json:"status"`
	Me      string `json:"me"`
	Peers   []Peer `json:"peers"`
}

type HistoryQuery struct {
	Agent string
	With  string
	Limit int
}

type OperatorSendRequest struct {
	To, Text, ReplyTo string
}

type OperatorSendResponse struct {
	ID     string `json:"id"`
	To     string `json:"to"`
	SentAt string `json:"sent_at"`
}

// Owner is who a token belongs to.
type Owner struct {
	Name string
	Role string // machine or operator
}

// Sink is where a connection's server-sent events go. The hub calls it from one goroutine.
type Sink interface {
	// Write sends one event. An empty id means none.
	Write(event string, data any, id string) error
	Ping() error
}

// --- The hub -----------------------------------------------------------------------------------

// Hub applies the rules over one store.
type Hub struct {
	st             *store.Store
	opt            Options
	join, msg, act *limiter
	mu             sync.Mutex
	conns          map[string]*Conn // live connections, by presence key
	feeds          map[*feed]struct{}
	// refusedAt is when the hub last recorded a refused join, by presence key and reason.
	refusedAt map[string]time.Time
	closed    bool
	wg        sync.WaitGroup
}

// New makes a hub over st.
func New(st *store.Store, opt Options) *Hub {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Ping <= 0 {
		opt.Ping = defaultPing
	}
	pick := func(l, d Limit) Limit {
		if l.Burst <= 0 {
			return d
		}
		return l
	}
	opt.Limits = Limits{
		Join:     pick(opt.Limits.Join, DefaultLimits.Join),
		Msg:      pick(opt.Limits.Msg, DefaultLimits.Msg),
		Activity: pick(opt.Limits.Activity, DefaultLimits.Activity),
	}
	return &Hub{
		st:        st,
		opt:       opt,
		join:      newLimiter(opt.Limits.Join, opt.Now),
		msg:       newLimiter(opt.Limits.Msg, opt.Now),
		act:       newLimiter(opt.Limits.Activity, opt.Now),
		conns:     map[string]*Conn{},
		feeds:     map[*feed]struct{}{},
		refusedAt: map[string]time.Time{},
	}
}

func (h *Hub) now() string {
	return h.opt.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// Close ends every connection and feed and waits for them. The store stays open.
func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	for _, c := range h.conns {
		c.close("disconnected")
	}
	for f := range h.feeds {
		f.abort()
	}
	h.mu.Unlock()
	h.wg.Wait()
}

// Presence gives the live presence record of key `<sid>.<machine>.<agent>`, or false.
func (h *Hub) Presence(key string) (wire.PresenceRecord, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.conns[key]
	if !ok {
		return wire.PresenceRecord{}, false
	}
	return c.presence(), true
}

// Auth gives the owner of a bearer token.
func (h *Hub) Auth(header string) (Owner, error) {
	token, ok := strings.CutPrefix(header, "Bearer ")
	if ok {
		name, role, valid, err := h.st.VerifyToken(token)
		if err != nil {
			return Owner{}, errf("unavailable", "token check failed")
		}
		if valid {
			return Owner{Name: name, Role: role}, nil
		}
	}
	return Owner{}, errf("unauthorized", "missing or invalid token")
}

// --- Connections -------------------------------------------------------------------------------

// item is one thing for a connection's goroutine to send.
type item struct {
	ev     *wire.Event
	notice *Notice
}

// Conn is one live agent stream. The hub's mutex guards every field after the constants.
type Conn struct {
	sid      string
	me       wire.Address
	key      string
	instance string
	host     string
	cwd      string
	client   wire.Client
	joinedAt string
	resumed  bool
	out      chan item

	state   string
	note    string
	waiting *wire.Waiting
	// gate is whether the operator lets the agent work: run, held or paused.
	gate  string
	gated bool
	// quiet is the sequence of a gate record that the agent gets no notice of, or 0.
	quiet int64
	// sent holds the ids of the messages written to the stream, for redact notices.
	sent map[string]bool
	// status is the session status this connection was told last.
	status string
	// rev is the revision of the last presence record sent for this connection.
	rev int64

	closeMu sync.Mutex
	reason  string
	done    chan struct{}
}

func (c *Conn) push(it item) {
	select {
	case c.out <- it:
	default:
		// A client that does not read gets a fresh stream: it resumes by Last-Event-ID.
		c.close("disconnected")
	}
}

// close ends the connection with a reason; the first reason wins.
func (c *Conn) close(reason string) {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	select {
	case <-c.done:
		return
	default:
	}
	c.reason = reason
	close(c.done)
}

func (c *Conn) closeReason() string {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	return c.reason
}

// Joined is a reserved name; the caller then runs the stream with Run.
type Joined struct {
	Conn *Conn
	// StartSeq is the first sequence to deliver.
	StartSeq int64
	// CatchUpUntil is the last sequence that existed at the join; events up to it come from
	// the store, later ones from the connection.
	CatchUpUntil int64
	// hold: this is the agent's first join, and the session holds new agents.
	hold bool
}

// Join checks a join and reserves the name. A Joined must be followed by Run.
func (h *Hub) Join(machine, sid string, q StreamQuery, lastEventID string) (*Joined, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, errf("unavailable", "the hub is shutting down")
	}
	if !h.join.take(machine) {
		return nil, errf("rate_limited", "too many joins")
	}
	row, err := h.requireOpenLocked(sid, h.opt.AutoCreate)
	if err != nil {
		return nil, err
	}
	me := wire.Address{Agent: q.Agent, Machine: machine}
	kicked, err := h.st.Kicked(sid, me.String())
	if err != nil {
		return nil, storeErr(err)
	}
	if kicked {
		h.refusedLocked(sid, me, "removed")
		return nil, errf("forbidden", "removed from session")
	}
	end, err := h.st.LastSeq()
	if err != nil {
		return nil, storeErr(err)
	}
	known, isKnown, err := h.st.KnownAgent(sid, me.String())
	if err != nil {
		return nil, storeErr(err)
	}
	startSeq := end + 1
	if wire.IsID(lastEventID) {
		n, _ := strconv.ParseInt(lastEventID, 10, 64)
		startSeq = n + 1
	} else if isKnown {
		// A new process of an agent the hub knows gets what the agent missed.
		startSeq = known.SeenSeq + 1
	}
	// An agent that the hub does not know starts held when the session holds new agents.
	gate := gateOf(gateInput{Known: isKnown, Gate: known.Gate, Hold: row.Record.Hold})
	key := wire.BuildPresenceKey(wire.PresenceKey{SID: sid, Agent: me})
	old := h.conns[key]
	if old != nil && old.instance != q.Instance {
		h.refusedLocked(sid, me, "taken")
		return nil, errf("conflict", "name %s is taken", me)
	}
	if old != nil {
		old.close("replaced")
	}
	c := &Conn{
		sid: sid, me: me, key: key, instance: q.Instance,
		host: q.Host, cwd: q.Cwd, client: wire.Client{Name: q.ClientName, Version: q.ClientVersion},
		joinedAt: h.now(), resumed: old != nil,
		out:   make(chan item, 256),
		state: "idle", sent: map[string]bool{},
		gate: gate, gated: q.Gated,
		status: row.Record.Status, reason: "disconnected", done: make(chan struct{}),
	}
	h.conns[key] = c
	h.wg.Add(1)
	return &Joined{Conn: c, StartSeq: startSeq, CatchUpUntil: end, hold: !isKnown && gate == wire.GateHeld}, nil
}

// refusedEvery is the least time between two records of a refused join of one agent. It is a
// little under the five minutes that the TUI shows such a join, so that a session that keeps
// trying stays in view, and a retry loop does not fill the log.
const refusedEvery = 4 * time.Minute

// refusedLocked records a join that the hub did not let in, so that the operator sees it: the
// agent is removed (a forgotten :allow must not look like an agent that never started), or
// another session holds the name (reason taken).
func (h *Hub) refusedLocked(sid string, me wire.Address, reason string) {
	key := wire.BuildPresenceKey(wire.PresenceKey{SID: sid, Agent: me}) + "|" + reason
	now := h.opt.Now()
	if last, ok := h.refusedAt[key]; ok && now.Sub(last) < refusedEvery {
		return
	}
	h.refusedAt[key] = now
	_, _ = h.publishLocked(wire.Event{Kind: wire.EventActivity, SID: sid, From: me.String(), Activity: &wire.Activity{
		Kind: "refused", Reason: reason, At: h.now(),
	}}, false, nil)
}

// Run delivers events to one connection until it closes, the client goes (ctx), or a write
// fails. It then records the leave.
func (h *Hub) Run(ctx context.Context, j *Joined, sink Sink) {
	c := j.Conn
	defer h.wg.Done()
	defer h.leave(c)
	me := c.me.String()
	h.mu.Lock()
	if !c.resumed {
		_, err := h.publishLocked(wire.Event{Kind: wire.EventActivity, SID: c.sid, From: me, Activity: &wire.Activity{
			Kind: "joined", Host: c.host, Cwd: c.cwd, Client: c.client, At: c.joinedAt,
		}}, true, nil)
		if err == nil && j.hold {
			// The first join in a session that holds new agents: record the hold. The agent
			// gets no notice of it: the joined event tells it the gate.
			c.quiet, err = h.recordGateLocked(c.sid, me, wire.GateHeld)
		}
		if err != nil {
			h.mu.Unlock()
			return
		}
	}
	h.putPresenceLocked(c)
	gate := c.gate
	h.mu.Unlock()
	if sink.Write("joined", map[string]string{"me": me, "session": c.sid, "gate": gate}, "") != nil {
		return
	}
	if j.StartSeq <= j.CatchUpUntil {
		// Missed messages first, newest ReplayMax only. A notice from before the join is stale.
		missed, err := h.st.Events(c.sid, j.StartSeq, j.CatchUpUntil, wire.EventMsg)
		if err != nil {
			return
		}
		missed = slices.DeleteFunc(missed, func(e wire.Event) bool { return deliveryFor(&e, me, nil) != "message" })
		if len(missed) > ReplayMax {
			missed = missed[len(missed)-ReplayMax:]
		}
		for i := range missed {
			if !h.deliver(c, item{ev: &missed[i]}, sink) {
				return
			}
		}
	}
	ticker := time.NewTicker(h.opt.Ping)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case it := <-c.out:
			if !h.deliver(c, it, sink) {
				return
			}
		case <-ticker.C:
			if sink.Ping() != nil {
				c.close("disconnected")
				return
			}
			if !h.sweep(c) {
				return
			}
		}
	}
}

// deliver writes what one item means to this agent. It gives false when the stream ends.
func (h *Hub) deliver(c *Conn, it item, sink Sink) bool {
	write := func(event string, data any, id string) bool {
		if err := sink.Write(event, data, id); err != nil {
			c.close("disconnected")
			return false
		}
		return true
	}
	if it.notice != nil {
		return write("notice", it.notice, "")
	}
	e := it.ev
	me := c.me.String()
	h.mu.Lock()
	wasSent := c.sent[e.ID]
	quiet := c.quiet
	h.mu.Unlock()
	if e.Seq == quiet {
		return true
	}
	switch deliveryFor(e, me, func(string) bool { return wasSent }) {
	case "message":
		m := toMessage(e)
		h.mu.Lock()
		c.sent[m.ID] = true
		h.mu.Unlock()
		return write("message", m, m.ID)
	case "notice":
		id := strconv.FormatInt(e.Seq, 10)
		switch e.Kind {
		case wire.EventKick:
			write("notice", Notice{Kind: "kicked", At: e.At}, id)
			c.close("kicked")
			return false
		case wire.EventRedact:
			return write("notice", Notice{Kind: "redacted", ID: e.ID, At: e.At}, id)
		case wire.EventActivity:
			if e.Activity.Kind == "gate" {
				return write("notice", Notice{Kind: gateNotice(e.Activity.Gate), At: e.Activity.At}, id)
			}
			return write("notice", Notice{Kind: "peer_left", Peer: e.From, At: e.Activity.At}, id)
		}
	}
	return true
}

// sweep checks what another process may have changed: the machine's token and the session.
// It gives false when the stream must end.
func (h *Hub) sweep(c *Conn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	valid, err := h.st.TokenValid(c.me.Machine)
	if err != nil {
		return true
	}
	if !valid {
		c.close("revoked")
		return false
	}
	row, ok, err := h.st.Session(c.sid)
	if err != nil {
		return true
	}
	if !ok {
		c.close("closed")
		return false
	}
	if row.Record.Status != c.status {
		c.status = row.Record.Status
		c.push(item{notice: &Notice{Kind: statusNotice(c.status), At: h.now()}})
	}
	return true
}

func statusNotice(status string) string {
	if status == "closed" {
		return "closed"
	}
	return "reopened"
}

// leave takes the connection out of the session and records it.
func (h *Hub) leave(c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conns[c.key] != c {
		return
	}
	delete(h.conns, c.key)
	reason := c.closeReason()
	if reason == "replaced" {
		return
	}
	// A deleted session keeps no record of the leave.
	if _, ok, err := h.st.Session(c.sid); err == nil && ok {
		_, _ = h.publishLocked(wire.Event{Kind: wire.EventActivity, SID: c.sid, From: c.me.String(), Activity: &wire.Activity{
			Kind: "left", Reason: reason, At: h.now(),
		}}, true, &store.Known{State: c.state, Note: c.note})
	}
	if rev, err := h.st.NextRevision(); err == nil {
		h.feedAll("presence", presenceOut{Kind: "presence", Key: c.key, Revision: rev}, "")
	}
}

// publishLocked appends one event and gives it to every connection of its session and every
// feed. The caller holds the mutex.
func (h *Hub) publishLocked(e wire.Event, seen bool, k *store.Known) (int64, error) {
	seq, err := h.st.Append(e, seen, k)
	if err != nil {
		return 0, err
	}
	e.Seq = seq
	for _, c := range h.conns {
		if c.sid == e.SID {
			c.push(item{ev: &e})
		}
	}
	if len(h.feeds) > 0 {
		subject, payload, err := wire.EncodeEvent(e)
		if err == nil {
			h.feedAll("event", eventOut{Kind: "event", Seq: seq, Subject: subject, Payload: string(payload)}, strconv.FormatInt(seq, 10))
		}
	}
	return seq, nil
}

func (h *Hub) putPresenceLocked(c *Conn) {
	rev, err := h.st.NextRevision()
	if err != nil {
		return
	}
	c.rev = rev
	rec := c.presence()
	h.feedAll("presence", presenceOut{Kind: "presence", Key: c.key, Revision: rev, Record: &rec}, "")
}

func (c *Conn) presence() wire.PresenceRecord {
	r := wire.PresenceRecord{
		Host: c.host, Cwd: c.cwd, Client: c.client, State: c.state, Note: c.note,
		JoinedAt: c.joinedAt, Gated: c.gated,
	}
	if c.waiting != nil {
		w := *c.waiting
		r.Waiting = &w
	}
	return r
}

// --- Agent requests ----------------------------------------------------------------------------

func (h *Hub) Send(machine, sid string, req SendRequest) (SendResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.msg.take(machine) {
		return SendResponse{}, errf("rate_limited", "too many messages")
	}
	c, err := h.requireConnLocked(machine, sid, req.Agent)
	if err != nil {
		return SendResponse{}, err
	}
	if _, err := h.requireOpenLocked(sid, false); err != nil {
		return SendResponse{}, err
	}
	to, err := h.recipientLocked(sid, req.To, c.me)
	if err != nil {
		return SendResponse{}, err
	}
	sentAt := h.now()
	seq, err := h.publishLocked(wire.Event{Kind: wire.EventMsg, SID: sid, From: c.me.String(), To: to, Text: req.Text, ReplyTo: req.ReplyTo, SentAt: sentAt}, false, nil)
	if err != nil {
		return SendResponse{}, storeErr(err)
	}
	res := SendResponse{ID: strconv.FormatInt(seq, 10), To: to, Online: true, SentAt: sentAt}
	if a, ok := wire.ParseAddress(to); ok {
		if peer, live := h.conns[wire.BuildPresenceKey(wire.PresenceKey{SID: sid, Agent: a})]; live {
			res.State = peer.state
		} else {
			res.Online, res.State = false, "away"
		}
	}
	return res, nil
}

func (h *Hub) Activity(machine, sid string, req ActivityRequest) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.act.take(machine) {
		return errf("rate_limited", "too many updates")
	}
	c, err := h.requireConnLocked(machine, sid, req.Agent)
	if err != nil {
		return err
	}
	at := h.now()
	evt := func(a *wire.Activity, seen bool, k *store.Known) error {
		a.At = at
		_, err := h.publishLocked(wire.Event{Kind: wire.EventActivity, SID: sid, From: c.me.String(), Activity: a}, seen, k)
		return storeErr(err)
	}
	switch req.Kind {
	case "state":
		c.state, c.note = req.State, req.Note
		err = evt(&wire.Activity{Kind: "state", State: req.State, Note: req.Note}, false, &store.Known{State: req.State, Note: req.Note})
	case "wait_start":
		on := ""
		switch {
		case req.From == "":
		case req.From == wire.Operator:
			on = wire.Operator
		default:
			a, err := h.peerLocked(sid, req.From, &c.me, true)
			if err != nil {
				return err
			}
			on = a.String()
		}
		c.waiting = &wire.Waiting{On: on, ReplyTo: req.ReplyTo, Since: at}
		err = evt(&wire.Activity{Kind: "wait_start", From: on, ReplyTo: req.ReplyTo, TimeoutS: req.TimeoutS}, false, nil)
	case "wait_end":
		c.waiting = nil
		err = evt(&wire.Activity{Kind: "wait_end", Result: req.Result}, false, nil)
	default:
		return errf("invalid", "unknown activity kind %q", req.Kind)
	}
	if err != nil {
		return err
	}
	h.putPresenceLocked(c)
	return nil
}

func (h *Hub) View(machine, sid, agent string) (SessionView, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, err := h.requireConnLocked(machine, sid, agent)
	if err != nil {
		return SessionView{}, err
	}
	row, ok, err := h.st.Session(sid)
	if err != nil {
		return SessionView{}, storeErr(err)
	}
	if !ok {
		return SessionView{}, errf("not_found", "no session %s", sid)
	}
	peers := []Peer{}
	live := h.inSessionLocked(sid)
	for _, p := range live {
		if p == c {
			continue
		}
		peer := Peer{Name: p.me.String(), State: p.state, Note: p.note, Online: true}
		if p.waiting != nil {
			peer.WaitingOn = p.waiting.On
		}
		peers = append(peers, peer)
	}
	away, err := h.awayLocked(sid)
	if err != nil {
		return SessionView{}, storeErr(err)
	}
	for _, k := range away {
		if k.Agent == c.me.String() {
			continue
		}
		peers = append(peers, Peer{Name: k.Agent, State: k.State, Note: k.Note, Online: false})
	}
	return SessionView{Session: sid, Status: row.Record.Status, Me: c.me.String(), Peers: peers}, nil
}

func (h *Hub) History(machine, sid string, q HistoryQuery) ([]Message, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, err := h.requireConnLocked(machine, sid, q.Agent)
	if err != nil {
		return nil, err
	}
	var peer *wire.Address
	if q.With != "" {
		p, err := h.peerLocked(sid, q.With, &c.me, true)
		if err != nil {
			return nil, err
		}
		peer = &p
	}
	last, err := h.st.LastSeq()
	if err != nil {
		return nil, storeErr(err)
	}
	events, err := h.st.Events(sid, 1, last, wire.EventMsg)
	if err != nil {
		return nil, storeErr(err)
	}
	me := c.me.String()
	msgs := []Message{}
	for i := range events {
		e := &events[i]
		if !isVisible(e, me) {
			continue
		}
		if peer != nil && !between(e, me, peer.String()) {
			continue
		}
		msgs = append(msgs, toMessage(e))
	}
	if len(msgs) > q.Limit {
		msgs = msgs[len(msgs)-q.Limit:]
	}
	return msgs, nil
}

// --- The admin API: the operator token ---------------------------------------------------------

func (h *Hub) ListSessions() ([]wire.SessionInfo, error) {
	rows, err := h.st.Sessions()
	if err != nil {
		return nil, storeErr(err)
	}
	out := make([]wire.SessionInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, wire.SessionInfo{Session: r.SID, SessionRecord: r.Record})
	}
	return out, nil
}

func (h *Hub) CreateSession(sid, title string) (wire.SessionInfo, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	row, err := h.st.CreateSession(sid, title, h.now(), h.opt.HoldNew)
	if errors.Is(err, store.ErrExists) {
		return wire.SessionInfo{}, errf("conflict", "session %s exists", sid)
	}
	if err != nil {
		return wire.SessionInfo{}, storeErr(err)
	}
	h.feedSession(row)
	return wire.SessionInfo{Session: sid, SessionRecord: row.Record}, nil
}

func (h *Hub) CloseSession(sid string) error  { return h.setStatus(sid, "closed") }
func (h *Hub) ReopenSession(sid string) error { return h.setStatus(sid, "open") }

func (h *Hub) setStatus(sid, status string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	row, changed, err := h.st.SetStatus(sid, status, h.now())
	if errors.Is(err, store.ErrNotFound) {
		return errf("not_found", "no session %s", sid)
	}
	if err != nil {
		return storeErr(err)
	}
	h.feedSession(row)
	if changed {
		at := h.now()
		for _, c := range h.inSessionLocked(sid) {
			c.status = status
			c.push(item{notice: &Notice{Kind: statusNotice(status), At: at}})
		}
	}
	return nil
}

func (h *Hub) DeleteSession(sid string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	rev, kicks, err := h.st.DeleteSession(sid)
	if errors.Is(err, store.ErrNotFound) {
		return errf("not_found", "no session %s", sid)
	}
	if errors.Is(err, store.ErrOpen) {
		return errf("conflict", "session %s is open; close it first", sid)
	}
	if err != nil {
		return storeErr(err)
	}
	for _, k := range kicks {
		h.feedAll("kick", kickOut{Kind: "kick", Key: kickKey(k.SID, k.Target), Revision: k.Revision}, "")
	}
	h.feedAll("session", sessionOut{Kind: "session", Session: sid, Revision: rev}, "")
	for _, c := range h.inSessionLocked(sid) {
		c.close("closed")
	}
	return nil
}

func (h *Hub) Kick(sid string, target wire.Address) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.requireSessionLocked(sid); err != nil {
		return err
	}
	at := h.now()
	rev, err := h.st.Kick(sid, target.String(), at)
	if err != nil {
		return storeErr(err)
	}
	h.feedAll("kick", kickOut{Kind: "kick", Key: kickKey(sid, target.String()), Revision: rev, Record: &wire.KickRecord{At: at}}, "")
	_, err = h.publishLocked(wire.Event{Kind: wire.EventKick, SID: sid, Target: target.String(), At: at}, false, nil)
	return storeErr(err)
}

func (h *Hub) Unkick(sid string, target wire.Address) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.requireSessionLocked(sid); err != nil {
		return err
	}
	rev, err := h.st.Unkick(sid, target.String())
	if err != nil {
		return storeErr(err)
	}
	h.feedAll("kick", kickOut{Kind: "kick", Key: kickKey(sid, target.String()), Revision: rev}, "")
	return nil
}

// Forget drops an agent that left from the session: it is no peer of the other agents, and
// the operator's lists do not show it. Unlike Kick, the agent may join again; it then starts
// as a new agent, with no replay of what it missed. An agent that is in the session cannot be
// forgotten. An agent that the hub does not know is not an error.
func (h *Hub) Forget(sid string, target wire.Address) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.requireSessionLocked(sid); err != nil {
		return err
	}
	if _, live := h.conns[wire.BuildPresenceKey(wire.PresenceKey{SID: sid, Agent: target})]; live {
		return errf("conflict", "%s is in the session; only an agent that left can be forgotten", target)
	}
	known, err := h.st.Forget(sid, target.String())
	if err != nil || !known {
		return storeErr(err)
	}
	_, err = h.publishLocked(wire.Event{Kind: wire.EventActivity, SID: sid, From: target.String(), Activity: &wire.Activity{
		Kind: "forgotten", At: h.now(),
	}}, false, nil)
	return storeErr(err)
}

// --- The gate: whether the operator lets an agent work -------------------------------------------

// GateRemoved is the answer of Gate for an agent that the operator removed.
const GateRemoved = "removed"

// gateInput is what the gate of an agent depends on.
type gateInput struct {
	Kicked bool   // the operator removed the agent
	Known  bool   // the agent joined the session before
	Gate   string // the gate of a known agent
	Hold   bool   // the session holds an agent that joins for the first time
}

// gateOf gives the gate of an agent: removed, or run, held or paused. An agent that did not
// join yet is held when the session holds new agents. Thus a tool call that comes before the
// join does not get through.
func gateOf(in gateInput) string {
	switch {
	case in.Kicked:
		return GateRemoved
	case in.Known:
		return in.Gate
	case in.Hold:
		return wire.GateHeld
	}
	return wire.GateRun
}

// gateNotice is the kind of the notice that tells an agent its new gate.
func gateNotice(gate string) string {
	if gate == wire.GateRun {
		return "released"
	}
	return gate
}

// Gate answers an agent's question before a tool call: may I work? The answer is run, held,
// paused or removed. The agent does not have to be in the session: a session that does not
// exist yet holds new agents when the hub does.
func (h *Hub) Gate(machine, sid, agent string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.act.take(machine) {
		return "", errf("rate_limited", "too many updates")
	}
	me := wire.Address{Agent: agent, Machine: machine}.String()
	in := gateInput{Hold: h.opt.HoldNew}
	var err error
	if in.Kicked, err = h.st.Kicked(sid, me); err != nil {
		return "", storeErr(err)
	}
	if row, ok, err := h.st.Session(sid); err != nil {
		return "", storeErr(err)
	} else if ok {
		in.Hold = row.Record.Hold
	}
	k, known, err := h.st.KnownAgent(sid, me)
	if err != nil {
		return "", storeErr(err)
	}
	in.Known, in.Gate = known, k.Gate
	return gateOf(in), nil
}

// recordGateLocked stores the gate of a known agent and appends the record of the change. The
// agent, when it is in the session, gets the record as a notice.
func (h *Hub) recordGateLocked(sid, agent, gate string) (seq int64, err error) {
	if _, err := h.st.SetGate(sid, agent, gate); err != nil {
		return 0, storeErr(err)
	}
	seq, err = h.publishLocked(wire.Event{Kind: wire.EventActivity, SID: sid, From: agent, Activity: &wire.Activity{
		Kind: "gate", Gate: gate, At: h.now(),
	}}, false, nil)
	return seq, storeErr(err)
}

// SetGate sets the gate of one agent of a session, or of each agent that is not removed when
// target is nil: run lets it work, paused and held stop its tool calls. An agent that has that
// gate already gets no record.
func (h *Hub) SetGate(sid string, target *wire.Address, gate string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.requireSessionLocked(sid); err != nil {
		return err
	}
	var rows []store.KnownRow
	if target != nil {
		k, ok, err := h.st.KnownAgent(sid, target.String())
		if err != nil {
			return storeErr(err)
		}
		if !ok {
			return errf("not_found", "no agent %s in session %s", target, sid)
		}
		rows = []store.KnownRow{k}
	} else {
		known, err := h.st.Known(sid)
		if err != nil {
			return storeErr(err)
		}
		for _, k := range known {
			if kicked, err := h.st.Kicked(sid, k.Agent); err != nil {
				return storeErr(err)
			} else if !kicked {
				rows = append(rows, k)
			}
		}
	}
	for _, k := range rows {
		if k.Gate == gate {
			continue
		}
		if a, ok := wire.ParseAddress(k.Agent); ok {
			if c := h.conns[wire.BuildPresenceKey(wire.PresenceKey{SID: sid, Agent: a})]; c != nil {
				c.gate = gate
			}
		}
		if _, err := h.recordGateLocked(sid, k.Agent, gate); err != nil {
			return err
		}
	}
	return nil
}

// SetHold sets whether a session holds an agent that joins it for the first time.
func (h *Hub) SetHold(sid string, hold bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	row, err := h.st.SetHold(sid, hold)
	if errors.Is(err, store.ErrNotFound) {
		return errf("not_found", "no session %s", sid)
	}
	if err != nil {
		return storeErr(err)
	}
	h.feedSession(row)
	return nil
}

func (h *Hub) Redact(sid, id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.requireSessionLocked(sid); err != nil {
		return err
	}
	seq, _ := strconv.ParseInt(id, 10, 64)
	ok, err := h.st.Delete(sid, seq)
	if err != nil {
		return storeErr(err)
	}
	if !ok {
		return errf("not_found", "no message %s in session %s", id, sid)
	}
	_, err = h.publishLocked(wire.Event{Kind: wire.EventRedact, SID: sid, ID: id, At: h.now()}, false, nil)
	return storeErr(err)
}

// OperatorSend sends as the operator: to all, or to a peer that is or was in the session.
func (h *Hub) OperatorSend(sid string, req OperatorSendRequest) (OperatorSendResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.requireOpenLocked(sid, false); err != nil {
		return OperatorSendResponse{}, err
	}
	to := wire.Broadcast
	if req.To != wire.Broadcast {
		a, err := h.peerLocked(sid, req.To, nil, false)
		if err != nil {
			return OperatorSendResponse{}, err
		}
		to = a.String()
	}
	sentAt := h.now()
	seq, err := h.publishLocked(wire.Event{Kind: wire.EventMsg, SID: sid, From: wire.Operator, To: to, Text: req.Text, ReplyTo: req.ReplyTo, SentAt: sentAt}, false, nil)
	if err != nil {
		return OperatorSendResponse{}, storeErr(err)
	}
	return OperatorSendResponse{ID: strconv.FormatInt(seq, 10), To: to, SentAt: sentAt}, nil
}

// --- The operator's feed -----------------------------------------------------------------------

type feedItem struct {
	event string
	data  any
	id    string
}

type feed struct {
	name string
	out  chan feedItem
	once sync.Once
	done chan struct{}
}

func (f *feed) abort() { f.once.Do(func() { close(f.done) }) }

func (f *feed) push(it feedItem) {
	select {
	case f.out <- it:
	default:
		// A feed that does not keep up starts again: the client resumes by Last-Event-ID.
		f.abort()
	}
}

// The shapes of the feed's events, as packages/core/src/api.ts AdminEvent writes them.
type eventOut struct {
	Kind    string `json:"kind"`
	Seq     int64  `json:"seq"`
	Subject string `json:"subject"`
	Payload string `json:"payload"`
}

type sessionOut struct {
	Kind     string              `json:"kind"`
	Session  string              `json:"session"`
	Revision int64               `json:"revision"`
	Record   *wire.SessionRecord `json:"record"`
}

type kickOut struct {
	Kind     string           `json:"kind"`
	Key      string           `json:"key"`
	Revision int64            `json:"revision"`
	Record   *wire.KickRecord `json:"record"`
}

type presenceOut struct {
	Kind     string               `json:"kind"`
	Key      string               `json:"key"`
	Revision int64                `json:"revision"`
	Record   *wire.PresenceRecord `json:"record"`
}

type snapshotOut struct {
	Kind   string `json:"kind"`
	Bucket string `json:"bucket"`
}

func kickKey(sid, target string) string {
	a, _ := wire.ParseAddress(target)
	return wire.BuildSessionsKey(wire.SessionsKey{Kind: "kick", SID: sid, Target: a})
}

// feedAll gives one event to every open feed. The caller holds the mutex.
func (h *Hub) feedAll(event string, data any, id string) {
	for f := range h.feeds {
		f.push(feedItem{event: event, data: data, id: id})
	}
}

func (h *Hub) feedSession(row store.SessionRow) {
	rec := row.Record
	h.feedAll("session", sessionOut{Kind: "session", Session: row.SID, Revision: row.Revision, Record: &rec}, "")
}

// AdminFeed sends the operator's feed to sink until ctx ends or the token is revoked: the
// current sessions and kicks, then `snapshot sessions`; the live presence, then `snapshot
// presence`; every stored event from fromSeq; then every change as it happens.
func (h *Hub) AdminFeed(ctx context.Context, name string, sink Sink, fromSeq int64) {
	f := &feed{name: name, out: make(chan feedItem, 1024), done: make(chan struct{})}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.feeds[f] = struct{}{}
	h.wg.Add(1)
	// The dump and the catch-up point come from the same critical section as the
	// registration: a later change reaches the channel with a higher revision or sequence.
	sessions, err1 := h.st.Sessions()
	kicks, err2 := h.st.Kicks()
	end, err3 := h.st.LastSeq()
	var presence []feedItem
	for _, c := range h.conns {
		rec := c.presence()
		presence = append(presence, feedItem{event: "presence", data: presenceOut{Kind: "presence", Key: c.key, Revision: c.rev, Record: &rec}})
	}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.feeds, f)
		h.mu.Unlock()
		h.wg.Done()
	}()
	if err1 != nil || err2 != nil || err3 != nil {
		return
	}
	write := func(it feedItem) bool {
		return sink.Write(it.event, it.data, it.id) == nil
	}
	for _, s := range sessions {
		rec := s.Record
		if !write(feedItem{event: "session", data: sessionOut{Kind: "session", Session: s.SID, Revision: s.Revision, Record: &rec}}) {
			return
		}
	}
	for _, k := range kicks {
		if !write(feedItem{event: "kick", data: kickOut{Kind: "kick", Key: kickKey(k.SID, k.Target), Revision: k.Revision, Record: &wire.KickRecord{At: k.At}}}) {
			return
		}
	}
	if !write(feedItem{event: "snapshot", data: snapshotOut{Kind: "snapshot", Bucket: "sessions"}}) {
		return
	}
	sort.Slice(presence, func(i, j int) bool {
		return presence[i].data.(presenceOut).Key < presence[j].data.(presenceOut).Key
	})
	for _, p := range presence {
		if !write(p) {
			return
		}
	}
	if !write(feedItem{event: "snapshot", data: snapshotOut{Kind: "snapshot", Bucket: "presence"}}) {
		return
	}
	if fromSeq <= end {
		rows, err := h.st.Rows(max(fromSeq, 1), end)
		if err != nil {
			return
		}
		for _, r := range rows {
			if !write(feedItem{event: "event", data: eventOut{Kind: "event", Seq: r.Seq, Subject: r.Subject, Payload: string(r.Payload)}, id: strconv.FormatInt(r.Seq, 10)}) {
				return
			}
		}
	}
	ticker := time.NewTicker(h.opt.Ping)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-f.done:
			return
		case it := <-f.out:
			if !write(it) {
				return
			}
		case <-ticker.C:
			if sink.Ping() != nil {
				return
			}
			if valid, err := h.st.TokenValid(name); err == nil && !valid {
				return
			}
		}
	}
}

// --- Internals ---------------------------------------------------------------------------------

func storeErr(err error) error {
	if err == nil {
		return nil
	}
	return errf("unavailable", "storage error: %v", err)
}

func (h *Hub) requireConnLocked(machine, sid, agent string) (*Conn, error) {
	c := h.conns[wire.BuildPresenceKey(wire.PresenceKey{SID: sid, Agent: wire.Address{Agent: agent, Machine: machine}})]
	if c == nil {
		return nil, errf("forbidden", "not in a session")
	}
	return c, nil
}

func (h *Hub) requireSessionLocked(sid string) error {
	_, ok, err := h.st.Session(sid)
	if err != nil {
		return storeErr(err)
	}
	if !ok {
		return errf("not_found", "no session %s", sid)
	}
	return nil
}

// requireOpenLocked gives the session, open. With create, an unknown session is made.
func (h *Hub) requireOpenLocked(sid string, create bool) (store.SessionRow, error) {
	row, ok, err := h.st.Session(sid)
	if err != nil {
		return row, storeErr(err)
	}
	if !ok && create {
		row, err = h.st.CreateSession(sid, "", h.now(), h.opt.HoldNew)
		if err != nil && !errors.Is(err, store.ErrExists) {
			return row, storeErr(err)
		}
		if err == nil {
			h.feedSession(row)
		}
		ok = true
	}
	if !ok {
		return row, errf("not_found", "no session %s", sid)
	}
	if row.Record.Status != "open" {
		return row, errf("forbidden", "session closed")
	}
	return row, nil
}

func (h *Hub) inSessionLocked(sid string) []*Conn {
	var out []*Conn
	for _, c := range h.conns {
		if c.sid == sid {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// awayLocked gives the agents that were in sid and have no live connection now. An agent the
// operator removed is not one of them: it cannot come back, so it is no peer.
func (h *Hub) awayLocked(sid string) ([]store.KnownRow, error) {
	known, err := h.st.Known(sid)
	if err != nil {
		return nil, err
	}
	kicks, err := h.st.Kicks()
	if err != nil {
		return nil, err
	}
	removed := map[string]bool{}
	for _, k := range kicks {
		if k.SID == sid {
			removed[k.Target] = true
		}
	}
	var out []store.KnownRow
	for _, k := range known {
		a, ok := wire.ParseAddress(k.Agent)
		if !ok || removed[k.Agent] {
			continue
		}
		if _, live := h.conns[wire.BuildPresenceKey(wire.PresenceKey{SID: sid, Agent: a})]; !live {
			out = append(out, k)
		}
	}
	return out, nil
}

// recipientLocked resolves a `send` target: all, operator (the user), or a peer that is or
// was in the session.
func (h *Hub) recipientLocked(sid, input string, me wire.Address) (string, error) {
	if input == wire.Broadcast || input == wire.Operator {
		return input, nil
	}
	a, err := h.peerLocked(sid, input, &me, false)
	if err != nil {
		return "", err
	}
	return a.String(), nil
}

// peerLocked turns a peer as a client writes it (`name` or `name@machine`) into an address. A
// bare name must match exactly one agent that is or was in the session, other than me (nil
// for the operator). With allowUnknown, a full address passes even if the hub never saw it (a
// wait or history filter).
func (h *Hub) peerLocked(sid, input string, me *wire.Address, allowUnknown bool) (wire.Address, error) {
	var live []wire.Address
	for _, c := range h.inSessionLocked(sid) {
		live = append(live, c.me)
	}
	away, err := h.awayLocked(sid)
	if err != nil {
		return wire.Address{}, storeErr(err)
	}
	peers := slices.Clone(live)
	for _, k := range away {
		if a, ok := wire.ParseAddress(k.Agent); ok {
			peers = append(peers, a)
		}
	}
	notMe := func(p wire.Address) bool { return me == nil || p != *me }
	isLive := func(p wire.Address) bool { return slices.Contains(live, p) }
	list := func() string {
		var parts []string
		for _, p := range peers {
			if !notMe(p) {
				continue
			}
			if isLive(p) {
				parts = append(parts, p.String())
			} else {
				parts = append(parts, p.String()+" (away)")
			}
		}
		if len(parts) == 0 {
			return "none"
		}
		return strings.Join(parts, ", ")
	}
	var matches []wire.Address
	if strings.Contains(input, "@") {
		full, ok := wire.ParseAddress(input)
		if !ok {
			return wire.Address{}, errf("invalid", "bad peer %s", input)
		}
		matches = []wire.Address{full}
	} else {
		for _, p := range peers {
			if p.Agent == input && notMe(p) {
				matches = append(matches, p)
			}
		}
	}
	// Yourself and an ambiguous name conflict with the session state (409), not with the
	// format.
	if me != nil {
		self := input == me.Agent
		if len(matches) > 0 {
			self = matches[0] == *me
		}
		if self {
			return wire.Address{}, errf("conflict", "cannot address yourself")
		}
	}
	if len(matches) > 1 {
		var names []string
		for _, m := range matches {
			names = append(names, m.String())
		}
		return wire.Address{}, errf("ambiguous", "%q matches %s", input, strings.Join(names, ", "))
	}
	if len(matches) == 0 || (!slices.Contains(peers, matches[0]) && !allowUnknown) {
		return wire.Address{}, errf("not_found", "no peer %s in this session; peers: %s", input, list())
	}
	return matches[0], nil
}

// --- Delivery rules (packages/core/src/deliver.ts) ---------------------------------------------

// deliveryFor says what the hub pushes to me for one event: "message" for a message from
// someone else to all or to me; "notice" for my own kick, the redact of a message I got, a
// peer's leave, or a change of my own gate; "" for nothing.
func deliveryFor(e *wire.Event, me string, wasSent func(string) bool) string {
	switch e.Kind {
	case wire.EventMsg:
		if e.From != me && (e.To == wire.Broadcast || e.To == me) {
			return "message"
		}
	case wire.EventKick:
		if e.Target == me {
			return "notice"
		}
	case wire.EventRedact:
		if wasSent != nil && wasSent(e.ID) {
			return "notice"
		}
	case wire.EventActivity:
		if e.Activity == nil {
			return ""
		}
		if e.Activity.Kind == "left" && e.From != me || e.Activity.Kind == "gate" && e.From == me {
			return "notice"
		}
	}
	return ""
}

// isVisible reports whether me may see this message in history: sent to all, to me, or by me.
func isVisible(e *wire.Event, me string) bool {
	return e.Kind == wire.EventMsg && (e.To == wire.Broadcast || e.To == me || e.From == me)
}

// between reports a message between me and one peer: from the peer to me or to all, or from
// me to the peer.
func between(e *wire.Event, me, peer string) bool {
	return (e.From == peer && (e.To == wire.Broadcast || e.To == me)) || (e.From == me && e.To == peer)
}

func toMessage(e *wire.Event) Message {
	return Message{ID: strconv.FormatInt(e.Seq, 10), From: e.From, To: e.To, Text: e.Text, ReplyTo: e.ReplyTo, SentAt: e.SentAt}
}
