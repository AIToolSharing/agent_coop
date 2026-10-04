// Package model keeps what the operator's feed said and answers what the views ask. Facts may
// arrive in any order and more than once; the derived state does not depend on that: every
// agent derives its state from its own events sorted by sequence, messages and timeline items
// are kept sorted by sequence, and every other index is a set keyed by id.
package model

import (
	"math"
	"sort"
	"strconv"

	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// Link is how the feed stands: before the first connection, live, or between connections.
type Link string

const (
	LinkConnecting   Link = "connecting"
	LinkLive         Link = "live"
	LinkReconnecting Link = "reconnecting"
)

// AllSessions is the id of the view over every session.
const AllSessions = "*"

// Update is one fact from the feed. Exactly one field is set.
type Update struct {
	Event    *wire.Event
	Session  *wire.SessionUpdate
	Kick     *wire.KickUpdate
	Presence *wire.PresenceUpdate
	Trace    *wire.TraceUpdate
	Snapshot *Snapshot
	Link     Link
}

// Snapshot says that the feed has sent every current entry of a bucket since it connected:
// entries of that bucket that are not in Seen are gone. The sessions bucket holds session ids
// and kick keys; the presence bucket holds presence keys.
type Snapshot struct {
	Bucket string // sessions or presence
	Seen   map[string]bool
}

// Msg is one message. Replies are the messages that answer it, in sequence order.
type Msg struct {
	ID       string
	Seq      int64
	SID      string
	From     string
	To       string
	Text     string
	ReplyTo  string
	SentAt   string
	Redacted bool
	Replies  []*Msg
}

// Waiting is what an agent waits for.
type Waiting struct{ On, ReplyTo, Since string }

// Left says why and when an agent left.
type Left struct{ Reason, At string }

// Agent is one agent of a session, as the views show it. The exported fields are the effective
// values: the live presence record when the agent is online, else what its events say.
type Agent struct {
	Address    string
	SID        string
	Host       string
	Cwd        string
	Client     string
	Online     bool
	Kicked     bool
	State      string // working blocked done idle left unknown
	StateSince string
	Note       string
	Waiting    *Waiting
	Sent       int
	JoinedAt   string
	// RefusedAt is the time of the last join that the hub refused because the operator removed
	// the agent; "" for none.
	RefusedAt string
	// DuplicateAt is the time of the last join that the hub refused because this agent holds
	// the name: a second session asked for it. "" for none, and after a later join.
	DuplicateAt string
	Left        *Left
	// Forgotten is true from the time the operator dropped the agent from the lists until the
	// agent joins again.
	Forgotten bool
	// Gate is whether the operator lets the agent work: run, held or paused.
	Gate string
	// Gated is true while the agent is online and its tool calls go through the gate. An
	// agent that is not gated only gets the gate as advice.
	Gated bool
	// HerdrPane is the Herdr pane that the agent runs in while it is online, or "".
	HerdrPane string
	// Orchestrator is true while the agent is online with an orchestrator token: it may also
	// act for the operator.
	Orchestrator bool
	// Trace is what the agent did at its terminal lately. Never nil.
	Trace *Trace
	// FirstSeq is the sequence of the agent's first event: the order agents are listed in.
	FirstSeq int64

	evts     []agentEvt
	live     *wire.PresenceRecord
	dState   string
	dSince   string
	dNote    string
	dWaiting *Waiting
}

type agentEvt struct {
	seq int64
	a   *wire.Activity
}

// Sys is a system line of the timeline: who did what.
type Sys struct{ Who, Text string }

// Item is one timeline entry: a message or a system line.
type Item struct {
	Seq int64
	At  string
	SID string
	Msg *Msg
	Sys *Sys
}

// Ask is a question one agent waits on another for.
type Ask struct {
	ID       string
	From     string
	To       string
	Since    string
	Question *Msg
	Answered bool
	seq      int64
}

// Session is the view of one session, or of every session (AllSessions).
type Session struct {
	SID      string
	Agents   map[string]*Agent
	Msgs     map[string]*Msg
	Timeline []Item
	Asks     map[string]*Ask
	// Version changes whenever the view changes, so renderers can cache.
	Version int
	// Reshaped changes when something already in the timeline changes: an item inserted
	// before the end, or a message withdrawn. A renderer that only appends must start over.
	Reshaped int

	store    *Store
	all      bool
	msgs     []*Msg
	children map[string][]*Msg
	redacted map[string]bool
	forYou   map[string]*Msg
}

// Store holds the raw facts and the views over them.
type Store struct {
	Sessions map[string]*wire.SessionRecord
	Kicks    map[string]*wire.KickRecord
	Presence map[string]*wire.PresenceRecord
	Link     Link
	// Version changes on every update.
	Version int

	revisions map[string]int64
	events    map[int64]*wire.Event
	views     map[string]*Session
	all       *Session
	// traces holds the trace of each agent, by presence key. boot names the run of the hub
	// that the traces are of.
	traces map[string]*Trace
	boot   int64
}

func New() *Store {
	s := &Store{
		Sessions:  map[string]*wire.SessionRecord{},
		Kicks:     map[string]*wire.KickRecord{},
		Presence:  map[string]*wire.PresenceRecord{},
		Link:      LinkConnecting,
		revisions: map[string]int64{},
		events:    map[int64]*wire.Event{},
		views:     map[string]*Session{},
		traces:    map[string]*Trace{},
	}
	s.all = newSession(s, AllSessions, true)
	return s
}

func newSession(st *Store, sid string, all bool) *Session {
	return &Session{
		SID:      sid,
		Agents:   map[string]*Agent{},
		Msgs:     map[string]*Msg{},
		Asks:     map[string]*Ask{},
		store:    st,
		all:      all,
		children: map[string][]*Msg{},
		redacted: map[string]bool{},
		forYou:   map[string]*Msg{},
	}
}

// View gives the view of one session, or of all sessions for AllSessions. Never nil.
func (s *Store) View(sid string) *Session {
	if sid == AllSessions {
		return s.all
	}
	v := s.views[sid]
	if v == nil {
		v = newSession(s, sid, false)
		s.views[sid] = v
	}
	return v
}

// SessionIDs lists the sessions that have a record, sorted.
func (s *Store) SessionIDs() []string {
	ids := make([]string, 0, len(s.Sessions))
	for id := range s.Sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Apply takes one fact in.
func (s *Store) Apply(u Update) {
	s.Version++
	switch {
	case u.Event != nil:
		e := u.Event
		if _, dup := s.events[e.Seq]; dup {
			return
		}
		s.events[e.Seq] = e
		s.View(e.SID).applyEvent(e)
		if s.Sessions[e.SID] != nil {
			s.all.applyEvent(e)
		}
	case u.Link != "":
		s.Link = u.Link
	case u.Trace != nil:
		s.applyTrace(u.Trace)
	case u.Snapshot != nil:
		s.applySnapshot(u.Snapshot)
	case u.Session != nil:
		if s.stale("session:"+u.Session.SID, u.Session.Revision) {
			return
		}
		if u.Session.Record == nil {
			delete(s.Sessions, u.Session.SID)
			s.deleteSession(u.Session.SID)
		} else {
			known := s.Sessions[u.Session.SID] != nil
			s.Sessions[u.Session.SID] = u.Session.Record
			if !known {
				s.admitToAll(u.Session.SID)
			}
		}
	case u.Kick != nil:
		if s.stale("kick:"+u.Kick.Key, u.Kick.Revision) {
			return
		}
		k, ok := wire.ParseSessionsKey(u.Kick.Key)
		if !ok || k.Kind != "kick" {
			return
		}
		if u.Kick.Record == nil {
			delete(s.Kicks, u.Kick.Key)
		} else {
			s.Kicks[u.Kick.Key] = u.Kick.Record
		}
		for _, v := range s.viewsOf(k.SID) {
			v.agent(k.SID, k.Target.String(), math.MaxInt64).Kicked = u.Kick.Record != nil
			v.Version++
		}
	case u.Presence != nil:
		if s.stale("presence:"+u.Presence.Key, u.Presence.Revision) {
			return
		}
		k, ok := wire.ParsePresenceKey(u.Presence.Key)
		if !ok {
			return
		}
		if u.Presence.Record == nil {
			delete(s.Presence, u.Presence.Key)
		} else {
			s.Presence[u.Presence.Key] = u.Presence.Record
		}
		for _, v := range s.viewsOf(k.SID) {
			a := v.agent(k.SID, k.Agent.String(), math.MaxInt64)
			a.live = u.Presence.Record
			a.refresh()
			v.Version++
		}
	}
}

func (s *Store) applySnapshot(snap *Snapshot) {
	switch snap.Bucket {
	case "sessions":
		for sid := range s.Sessions {
			if !snap.Seen[sid] {
				s.Apply(Update{Session: &wire.SessionUpdate{SID: sid}})
			}
		}
		for key := range s.Kicks {
			if !snap.Seen[key] {
				s.Apply(Update{Kick: &wire.KickUpdate{Key: key}})
			}
		}
	case "presence":
		for key := range s.Presence {
			if !snap.Seen[key] {
				s.Apply(Update{Presence: &wire.PresenceUpdate{Key: key}})
			}
		}
	}
}

// stale reports whether a newer revision of this entry was applied before, and records this
// one. A revision of zero means "none": the entry is applied and the record forgotten.
func (s *Store) stale(key string, rev int64) bool {
	if rev == 0 {
		delete(s.revisions, key)
		return false
	}
	if seen, ok := s.revisions[key]; ok && seen > rev {
		return true
	}
	s.revisions[key] = rev
	return false
}

// viewsOf gives the views a fact about sid goes to: its own, and the all view once the session
// has a record. The all view shows only sessions that exist.
func (s *Store) viewsOf(sid string) []*Session {
	if s.Sessions[sid] != nil {
		return []*Session{s.View(sid), s.all}
	}
	return []*Session{s.View(sid)}
}

// admitToAll replays what is known about a session into the all view, once the session has a
// record: its events in sequence order, then its presence and kick records.
func (s *Store) admitToAll(sid string) {
	var seqs []int64
	for seq, e := range s.events {
		if e.SID == sid {
			seqs = append(seqs, seq)
		}
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	for _, seq := range seqs {
		s.all.applyEvent(s.events[seq])
	}
	for key, rec := range s.Presence {
		if k, ok := wire.ParsePresenceKey(key); ok && k.SID == sid {
			a := s.all.agent(k.SID, k.Agent.String(), math.MaxInt64)
			a.live = rec
			a.refresh()
		}
	}
	for key := range s.Kicks {
		if k, ok := wire.ParseSessionsKey(key); ok && k.Kind == "kick" && k.SID == sid {
			s.all.agent(k.SID, k.Target.String(), math.MaxInt64).Kicked = true
		}
	}
	for key := range s.traces {
		if k, ok := wire.ParsePresenceKey(key); ok && k.SID == sid {
			s.all.agent(k.SID, k.Agent.String(), math.MaxInt64)
		}
	}
	s.all.Version++
}

// deleteSession drops a session's view and traffic, and rebuilds the all view without it.
func (s *Store) deleteSession(sid string) {
	delete(s.views, sid)
	for seq, e := range s.events {
		if e.SID == sid {
			delete(s.events, seq)
		}
	}
	for key := range s.traces {
		if k, ok := wire.ParsePresenceKey(key); ok && k.SID == sid {
			delete(s.traces, key)
		}
	}
	version := s.all.Version + 1
	s.all = newSession(s, AllSessions, true)
	for _, id := range s.SessionIDs() {
		s.admitToAll(id)
	}
	s.all.Version = version
}

func (v *Session) agentKey(sid, address string) string {
	if v.all {
		return sid + "/" + address
	}
	return address
}

// agent gives the agent row, creating it on first sight. seq lowers FirstSeq.
func (v *Session) agent(sid, address string, seq int64) *Agent {
	key := v.agentKey(sid, address)
	a := v.Agents[key]
	if a == nil {
		a = &Agent{Address: address, SID: sid, State: "unknown", dState: "unknown", Gate: wire.GateRun, FirstSeq: math.MaxInt64, Trace: &Trace{}}
		if addr, ok := wire.ParseAddress(address); ok {
			a.Trace = v.store.trace(wire.BuildPresenceKey(wire.PresenceKey{SID: sid, Agent: addr}))
			kick := wire.BuildSessionsKey(wire.SessionsKey{Kind: "kick", SID: sid, Target: addr})
			a.Kicked = v.store.Kicks[kick] != nil
			if rec := v.store.Presence[wire.BuildPresenceKey(wire.PresenceKey{SID: sid, Agent: addr})]; rec != nil {
				a.live = rec
				a.refresh()
			}
		}
		v.Agents[key] = a
	}
	if seq < a.FirstSeq {
		a.FirstSeq = seq
	}
	return a
}

func idOf(seq int64) string { return strconv.FormatInt(seq, 10) }

func (v *Session) applyEvent(e *wire.Event) {
	v.Version++
	switch e.Kind {
	case wire.EventMsg:
		m := &Msg{ID: idOf(e.Seq), Seq: e.Seq, SID: e.SID, From: e.From, To: e.To, Text: e.Text, ReplyTo: e.ReplyTo, SentAt: e.SentAt}
		m.Redacted = v.redacted[m.ID]
		m.Replies = v.children[m.ID]
		v.Msgs[m.ID] = m
		v.msgs = insertMsg(v.msgs, m)
		v.insertItem(Item{Seq: e.Seq, At: e.SentAt, SID: e.SID, Msg: m})
		if e.From != wire.Operator {
			v.agent(e.SID, e.From, e.Seq).Sent++
		}
		if e.ReplyTo != "" {
			v.children[e.ReplyTo] = insertMsg(v.children[e.ReplyTo], m)
			if p := v.Msgs[e.ReplyTo]; p != nil {
				p.Replies = v.children[e.ReplyTo]
				v.Reshaped++
			}
			if e.From == wire.Operator {
				delete(v.forYou, e.ReplyTo)
			}
			if q := v.Asks[e.ReplyTo]; q != nil && e.From == q.To {
				q.Answered = true
			}
		}
		if e.To == wire.Operator && !m.Redacted && !v.hasReplyFrom(m.ID, wire.Operator) {
			v.forYou[m.ID] = m
		}
		if q := v.Asks[m.ID]; q != nil {
			q.Question = m
		}
	case wire.EventKick:
		v.insertItem(Item{Seq: e.Seq, At: e.At, SID: e.SID, Sys: &Sys{Who: e.Target, Text: "removed by " + actor(e.By)}})
	case wire.EventRedact:
		v.redacted[e.ID] = true
		if m := v.Msgs[e.ID]; m != nil {
			m.Redacted = true
			v.Reshaped++
		}
		delete(v.forYou, e.ID)
		v.insertItem(Item{Seq: e.Seq, At: e.At, SID: e.SID, Sys: &Sys{Who: wire.Operator, Text: "withdrew message #" + e.ID}})
	case wire.EventActivity:
		x := e.Activity
		a := v.agent(e.SID, e.From, e.Seq)
		a.addEvent(e.Seq, x)
		sys := func(text string) {
			v.insertItem(Item{Seq: e.Seq, At: x.At, SID: e.SID, Sys: &Sys{Who: a.Address, Text: text}})
		}
		switch x.Kind {
		case "joined":
			sys("joined from " + x.Host + " (" + x.Client.Name + " " + x.Client.Version + ")")
		case "left":
			sys("left (" + x.Reason + ")")
		case "state":
			text := "is " + x.State
			if x.Note != "" {
				text += ": " + x.Note
			}
			sys(text)
		case "wait_start":
			text := "waits"
			if x.From != "" {
				text += " for " + x.From
			}
			if x.ReplyTo != "" {
				text += " (ask #" + x.ReplyTo + ")"
			}
			sys(text)
			if x.ReplyTo != "" && x.From != "" {
				// The first wait on a question, by sequence, is the ask.
				if old := v.Asks[x.ReplyTo]; old == nil || e.Seq < old.seq {
					q := &Ask{ID: x.ReplyTo, From: a.Address, To: x.From, Since: x.At, Question: v.Msgs[x.ReplyTo], seq: e.Seq}
					q.Answered = v.hasReplyFrom(q.ID, q.To)
					v.Asks[q.ID] = q
				}
			}
		case "wait_end":
			sys("wait ended: " + x.Result)
		case "refused":
			if x.Reason == "taken" {
				sys("is in use: a second session tried to join with this name and waits (coop --agent <name> claude gives it its own)")
			} else {
				sys("tried to join; it is removed (:allow lets it back)")
			}
		case "forgotten":
			sys("forgotten by the operator")
		case "gate":
			switch x.Gate {
			case wire.GateHeld:
				if x.By != "" {
					sys("held by " + actor(x.By))
				} else {
					sys("is held until the operator releases it")
				}
			case wire.GatePaused:
				sys("paused by " + actor(x.By))
			default:
				sys("released by " + actor(x.By))
			}
		}
	}
}

// actor names who made an admin change: the operator, or the orchestrator token by.
func actor(by string) string {
	if by == "" {
		return "the operator"
	}
	return "the orchestrator " + by
}

// hasReplyFrom reports whether a reply to id from `from` is known.
func (v *Session) hasReplyFrom(id, from string) bool {
	for _, m := range v.children[id] {
		if m.From == from {
			return true
		}
	}
	return false
}

func (v *Session) insertItem(it Item) {
	i := sort.Search(len(v.Timeline), func(i int) bool { return v.Timeline[i].Seq >= it.Seq })
	if i < len(v.Timeline) {
		v.Reshaped++
	}
	v.Timeline = append(v.Timeline, Item{})
	copy(v.Timeline[i+1:], v.Timeline[i:])
	v.Timeline[i] = it
}

func insertMsg(list []*Msg, m *Msg) []*Msg {
	i := sort.Search(len(list), func(i int) bool { return list[i].Seq >= m.Seq })
	list = append(list, nil)
	copy(list[i+1:], list[i:])
	list[i] = m
	return list
}

// AgentList lists the agents in order of first appearance.
func (v *Session) AgentList() []*Agent {
	out := make([]*Agent, 0, len(v.Agents))
	for _, a := range v.Agents {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.FirstSeq != b.FirstSeq {
			return a.FirstSeq < b.FirstSeq
		}
		if a.SID != b.SID {
			return a.SID < b.SID
		}
		return a.Address < b.Address
	})
	return out
}

// Messages lists every message in sequence order. The slice is shared: do not change it.
func (v *Session) Messages() []*Msg { return v.msgs }

// OpenAsks lists the questions that wait for an answer, oldest first.
func (v *Session) OpenAsks() []*Ask {
	var out []*Ask
	for _, q := range v.Asks {
		if q.Question != nil && !q.Answered {
			out = append(out, q)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Since != out[j].Since {
			return out[i].Since < out[j].Since
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ForYou lists the messages to the operator that no operator reply answered yet.
func (v *Session) ForYou() []*Msg {
	var out []*Msg
	for _, m := range v.forYou {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// addEvent records one activity and derives the agent's state again from all of them.
func (a *Agent) addEvent(seq int64, x *wire.Activity) {
	i := sort.Search(len(a.evts), func(i int) bool { return a.evts[i].seq >= seq })
	a.evts = append(a.evts, agentEvt{})
	copy(a.evts[i+1:], a.evts[i:])
	a.evts[i] = agentEvt{seq: seq, a: x}
	a.derive()
}

// derive replays the agent's events in sequence order, as the TS model did for all events.
func (a *Agent) derive() {
	a.Host, a.Cwd, a.Client, a.JoinedAt, a.RefusedAt, a.DuplicateAt = "", "", "", "", "", ""
	a.Left = nil
	a.Forgotten = false
	a.Gate = wire.GateRun
	a.dState, a.dSince, a.dNote, a.dWaiting = "unknown", "", "", nil
	for _, ev := range a.evts {
		x := ev.a
		switch x.Kind {
		case "joined":
			a.Host, a.Cwd, a.JoinedAt = x.Host, x.Cwd, x.At
			a.Client = x.Client.Name + " " + x.Client.Version
			a.Left = nil
			a.Forgotten = false
			a.DuplicateAt = ""
			if a.dState == "left" || a.dState == "unknown" {
				a.dState, a.dSince = "idle", x.At
			}
		case "left":
			a.Left = &Left{Reason: x.Reason, At: x.At}
			a.dState, a.dSince, a.dWaiting = "left", x.At, nil
		case "state":
			a.dState, a.dNote, a.dSince = x.State, x.Note, x.At
		case "wait_start":
			a.dWaiting = &Waiting{On: x.From, ReplyTo: x.ReplyTo, Since: x.At}
		case "wait_end":
			a.dWaiting = nil
		case "refused":
			if x.Reason == "taken" {
				a.DuplicateAt = x.At
			} else {
				a.RefusedAt = x.At
			}
		case "forgotten":
			// The hub knows nothing of the agent now: its next join starts with a new gate.
			a.Forgotten = true
			a.Gate = wire.GateRun
		case "gate":
			a.Gate = x.Gate
		}
	}
	a.refresh()
}

// refresh sets the effective fields: the live presence record wins while the agent is online.
func (a *Agent) refresh() {
	if a.live == nil {
		a.Online, a.Gated, a.HerdrPane, a.Orchestrator = false, false, "", false
		a.State, a.Note, a.StateSince, a.Waiting = a.dState, a.dNote, a.dSince, a.dWaiting
		return
	}
	a.Online, a.Gated, a.HerdrPane = true, a.live.Gated, a.live.HerdrPane
	a.Orchestrator = a.live.Role == wire.RoleOrchestrator
	a.State, a.Note, a.StateSince = a.live.State, a.live.Note, a.dSince
	a.Waiting = nil
	if w := a.live.Waiting; w != nil {
		a.Waiting = &Waiting{On: w.On, ReplyTo: w.ReplyTo, Since: w.Since}
	}
	if a.Host == "" {
		a.Host = a.live.Host
	}
	if a.Cwd == "" {
		a.Cwd = a.live.Cwd
	}
	if a.Client == "" {
		a.Client = a.live.Client.Name + " " + a.live.Client.Version
	}
}
