// Package wire holds the names, subjects, keys, records and events that the hub, the shim and
// the TUI exchange. It mirrors packages/core/src/names.ts, schema.ts and api.ts of the
// TypeScript hub (in the history before v0.2.0).
package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Token roles. A machine token acts as the agents of one machine. The operator is the human.
// An orchestrator is an agent that may also do what the operator does, but it sends as itself.
// A reporter only reads.
const (
	RoleMachine      = "machine"
	RoleOperator     = "operator"
	RoleOrchestrator = "orchestrator"
	RoleReporter     = "reporter"
)

// IsRole reports whether s is a token role.
func IsRole(s string) bool {
	return s == RoleMachine || s == RoleOperator || s == RoleOrchestrator || s == RoleReporter
}

// Reserved names in addressing. Any is a send target only: the hub picks the live peer that
// can take work, on the machine with the least load.
const (
	Operator  = "operator"
	Broadcast = "all"
	Any       = "any"
)

// MaxText is the longest message text.
const MaxText = 8000

var (
	tokenRE = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
	idRE    = regexp.MustCompile(`^[1-9][0-9]{0,15}$`)
)

// IsToken reports whether s is a name token: a session id or a machine name. No dots.
func IsToken(s string) bool { return tokenRE.MatchString(s) }

// IsAgentName reports whether s is a token that is not a reserved name.
func IsAgentName(s string) bool { return IsToken(s) && s != Operator && s != Broadcast && s != Any }

// IsID reports whether s is a message id: a stream sequence in decimal.
func IsID(s string) bool { return idRE.MatchString(s) }

// IsTime reports whether s is an ISO 8601 time with a zone, as the hub writes it.
func IsTime(s string) bool {
	_, err := time.Parse(time.RFC3339Nano, s)
	return err == nil
}

// Address is one agent: its name inside the session and the machine that runs it.
type Address struct {
	Agent   string
	Machine string
}

func (a Address) String() string { return a.Agent + "@" + a.Machine }

// ParseAddress reads `agent@machine`.
func ParseAddress(s string) (Address, bool) {
	agent, machine, ok := strings.Cut(s, "@")
	if !ok || !IsAgentName(agent) || !IsToken(machine) {
		return Address{}, false
	}
	return Address{Agent: agent, Machine: machine}, true
}

// Subject is a subject of stream COOP: `coop.<sid>.msg|evt.<machine>.<agent>` or `coop.<sid>.ops`.
type Subject struct {
	Kind string // msg, evt or ops
	SID  string
	From Address // empty for ops
}

func BuildSubject(s Subject) string {
	if s.Kind == "ops" {
		return "coop." + s.SID + ".ops"
	}
	return "coop." + s.SID + "." + s.Kind + "." + s.From.Machine + "." + s.From.Agent
}

func ParseSubject(subject string) (Subject, bool) {
	t := strings.Split(subject, ".")
	if len(t) < 3 || t[0] != "coop" || !IsToken(t[1]) {
		return Subject{}, false
	}
	if len(t) == 3 && t[2] == "ops" {
		return Subject{Kind: "ops", SID: t[1]}, true
	}
	if len(t) != 5 || (t[2] != "msg" && t[2] != "evt") || !IsToken(t[3]) || !IsAgentName(t[4]) {
		return Subject{}, false
	}
	return Subject{Kind: t[2], SID: t[1], From: Address{Agent: t[4], Machine: t[3]}}, true
}

// SessionsKey is a key of bucket coop_sessions: `<sid>` or `<sid>.kick.<machine>.<agent>`.
type SessionsKey struct {
	Kind   string // session or kick
	SID    string
	Target Address // kick only
}

func BuildSessionsKey(k SessionsKey) string {
	if k.Kind == "session" {
		return k.SID
	}
	return k.SID + ".kick." + k.Target.Machine + "." + k.Target.Agent
}

func ParseSessionsKey(key string) (SessionsKey, bool) {
	t := strings.Split(key, ".")
	if len(t) == 1 && IsToken(key) {
		return SessionsKey{Kind: "session", SID: key}, true
	}
	if len(t) != 4 || t[1] != "kick" || !IsToken(t[0]) || !IsToken(t[2]) || !IsAgentName(t[3]) {
		return SessionsKey{}, false
	}
	return SessionsKey{Kind: "kick", SID: t[0], Target: Address{Agent: t[3], Machine: t[2]}}, true
}

// PresenceKey is a key of bucket coop_presence: `<sid>.<machine>.<agent>`.
type PresenceKey struct {
	SID   string
	Agent Address
}

func BuildPresenceKey(k PresenceKey) string {
	return k.SID + "." + k.Agent.Machine + "." + k.Agent.Agent
}

func ParsePresenceKey(key string) (PresenceKey, bool) {
	t := strings.Split(key, ".")
	if len(t) != 3 || !IsToken(t[0]) || !IsToken(t[1]) || !IsAgentName(t[2]) {
		return PresenceKey{}, false
	}
	return PresenceKey{SID: t[0], Agent: Address{Agent: t[2], Machine: t[1]}}, true
}

// --- Records of the KV buckets ---------------------------------------------------------------

type Client struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// SessionRecord is the value of key <sid> in coop_sessions.
type SessionRecord struct {
	Status    string `json:"status"` // open or closed
	Title     string `json:"title,omitempty"`
	CreatedAt string `json:"created_at"`
	ClosedAt  string `json:"closed_at,omitempty"`
	// Hold: an agent that joins the session for the first time is held until the operator
	// releases it.
	Hold bool `json:"hold"`
}

// SessionInfo is one entry of GET /v1/admin/sessions: the id with its record.
type SessionInfo struct {
	Session string `json:"session"`
	SessionRecord
}

// KickRecord is the value of key <sid>.kick.<machine>.<agent> in coop_sessions.
type KickRecord struct {
	At string `json:"at"`
}

// Waiting is what an agent waits for, in its presence record.
type Waiting struct {
	On      string `json:"on,omitempty"` // a peer address or operator
	ReplyTo string `json:"reply_to,omitempty"`
	Since   string `json:"since"`
}

// PresenceRecord is the value of key <sid>.<machine>.<agent> in coop_presence.
type PresenceRecord struct {
	Host     string   `json:"host"`
	Cwd      string   `json:"cwd"`
	Client   Client   `json:"client"`
	State    string   `json:"state"`
	Note     string   `json:"note,omitempty"`
	JoinedAt string   `json:"joined_at"`
	Waiting  *Waiting `json:"waiting,omitempty"`
	// Gated is true when the agent's tool calls go through the operator's gate: the agent
	// was started with `coop claude`.
	Gated bool `json:"gated,omitempty"`
	// HerdrPane is the Herdr pane that the agent runs in, for example w1:p3; "" for none.
	HerdrPane string `json:"herdr_pane,omitempty"`
	// Role is RoleOrchestrator for an agent that may also act for the operator; "" for an
	// agent of a machine token.
	Role string `json:"role,omitempty"`
}

// Gates: whether the operator lets an agent work.
const (
	GateRun    = "run"
	GateHeld   = "held"   // not released yet after its first join
	GatePaused = "paused" // stopped by the operator for a time
)

// IsGate reports whether s is a gate.
func IsGate(s string) bool { return s == GateRun || s == GateHeld || s == GatePaused }

// --- Stream events ---------------------------------------------------------------------------

// Event kinds.
const (
	EventMsg      = "msg"
	EventKick     = "kick"
	EventRedact   = "redact"
	EventActivity = "evt"
)

// Event is one decoded event of stream COOP. Seq is the stream sequence and the message id.
type Event struct {
	Kind string
	Seq  int64
	SID  string
	// From is the sender: Operator or an address for a message; an address for an activity.
	From string
	// Message fields.
	To      string // Broadcast, Operator or an address
	Text    string
	ReplyTo string
	SentAt  string
	// FromRole is RoleOrchestrator for a message of an orchestrator; "" else.
	FromRole string
	// Kick field.
	Target string
	// Redact field.
	ID string
	// Kick and redact.
	At string
	// Kick: the orchestrator token that removed the agent; "" for the operator.
	By string
	// Activity fields.
	Activity *Activity
}

// Activity is what an agent did, on `coop.<sid>.evt.<machine>.<agent>`.
type Activity struct {
	Kind string `json:"kind"` // joined left state wait_start wait_end refused forgotten gate
	At   string `json:"at"`
	// joined
	Host   string `json:"host,omitempty"`
	Cwd    string `json:"cwd,omitempty"`
	Client Client `json:"client,omitzero"`
	// left: disconnected kicked revoked closed. refused (a join that the hub did not let in):
	// removed (the operator removed the agent) or taken (another session holds the name).
	Reason string `json:"reason,omitempty"`
	// state: working blocked done idle
	State string `json:"state,omitempty"`
	Note  string `json:"note,omitempty"`
	// wait_start
	From     string `json:"from,omitempty"` // a peer address or operator
	ReplyTo  string `json:"reply_to,omitempty"`
	TimeoutS int    `json:"timeout_s,omitempty"`
	// wait_end: message timeout cancelled
	Result string `json:"result,omitempty"`
	// gate: run held paused. The hub writes it when the gate of the agent changes.
	Gate string `json:"gate,omitempty"`
	// By is the orchestrator token that changed the gate; "" for the operator and the hub.
	By string `json:"by,omitempty"`
}

type msgPayload struct {
	To      string `json:"to"`
	Text    string `json:"text"`
	ReplyTo string `json:"reply_to,omitempty"`
	SentAt  string `json:"sent_at"`
	Role    string `json:"role,omitempty"`
}

type opsPayload struct {
	Kind    string `json:"kind"`
	To      string `json:"to,omitempty"`
	Text    string `json:"text,omitempty"`
	ReplyTo string `json:"reply_to,omitempty"`
	SentAt  string `json:"sent_at,omitempty"`
	Target  string `json:"target,omitempty"`
	ID      string `json:"id,omitempty"`
	At      string `json:"at,omitempty"`
	By      string `json:"by,omitempty"`
}

// EncodeEvent gives the subject and the payload that carry e. The inverse of DecodeEvent.
func EncodeEvent(e Event) (subject string, payload []byte, err error) {
	switch e.Kind {
	case EventMsg:
		if e.From == Operator {
			payload, err = json.Marshal(opsPayload{Kind: "msg", To: e.To, Text: e.Text, ReplyTo: e.ReplyTo, SentAt: e.SentAt})
			return BuildSubject(Subject{Kind: "ops", SID: e.SID}), payload, err
		}
		from, ok := ParseAddress(e.From)
		if !ok {
			return "", nil, fmt.Errorf("wire: bad sender %q", e.From)
		}
		payload, err = json.Marshal(msgPayload{To: e.To, Text: e.Text, ReplyTo: e.ReplyTo, SentAt: e.SentAt, Role: e.FromRole})
		return BuildSubject(Subject{Kind: "msg", SID: e.SID, From: from}), payload, err
	case EventKick:
		payload, err = json.Marshal(opsPayload{Kind: "kick", Target: e.Target, At: e.At, By: e.By})
		return BuildSubject(Subject{Kind: "ops", SID: e.SID}), payload, err
	case EventRedact:
		payload, err = json.Marshal(opsPayload{Kind: "redact", ID: e.ID, At: e.At})
		return BuildSubject(Subject{Kind: "ops", SID: e.SID}), payload, err
	case EventActivity:
		from, ok := ParseAddress(e.From)
		if !ok || e.Activity == nil {
			return "", nil, fmt.Errorf("wire: bad activity event %+v", e)
		}
		payload, err = json.Marshal(e.Activity)
		return BuildSubject(Subject{Kind: "evt", SID: e.SID, From: from}), payload, err
	}
	return "", nil, fmt.Errorf("wire: unknown event kind %q", e.Kind)
}

func strict(data []byte, v any) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return false
	}
	// One value only.
	return !dec.More()
}

func isRecipient(s string) bool {
	if s == Broadcast || s == Operator {
		return true
	}
	_, ok := ParseAddress(s)
	return ok
}

func isWaitTarget(s string) bool {
	if s == Operator {
		return true
	}
	_, ok := ParseAddress(s)
	return ok
}

func oneOf(s string, xs ...string) bool {
	for _, x := range xs {
		if s == x {
			return true
		}
	}
	return false
}

// DecodeEvent reads one stream message. An unknown subject or a bad payload gives false.
func DecodeEvent(subject string, payload []byte, seq int64) (Event, bool) {
	s, ok := ParseSubject(subject)
	if !ok {
		return Event{}, false
	}
	switch s.Kind {
	case "msg":
		var p msgPayload
		if !strict(payload, &p) || !isRecipient(p.To) || !validText(p.Text) || !optionalID(p.ReplyTo) || !IsTime(p.SentAt) || p.Role != "" && p.Role != RoleOrchestrator {
			return Event{}, false
		}
		return Event{Kind: EventMsg, Seq: seq, SID: s.SID, From: s.From.String(), To: p.To, Text: p.Text, ReplyTo: p.ReplyTo, SentAt: p.SentAt, FromRole: p.Role}, true
	case "ops":
		var p opsPayload
		if !strict(payload, &p) {
			return Event{}, false
		}
		switch p.Kind {
		case "msg":
			if !isRecipient(p.To) || !validText(p.Text) || !optionalID(p.ReplyTo) || !IsTime(p.SentAt) || p.Target != "" || p.ID != "" || p.At != "" || p.By != "" {
				return Event{}, false
			}
			return Event{Kind: EventMsg, Seq: seq, SID: s.SID, From: Operator, To: p.To, Text: p.Text, ReplyTo: p.ReplyTo, SentAt: p.SentAt}, true
		case "kick":
			if _, ok := ParseAddress(p.Target); !ok || !IsTime(p.At) || p.To != "" || p.Text != "" || p.ID != "" || p.By != "" && !IsToken(p.By) {
				return Event{}, false
			}
			return Event{Kind: EventKick, Seq: seq, SID: s.SID, Target: p.Target, At: p.At, By: p.By}, true
		case "redact":
			if !IsID(p.ID) || !IsTime(p.At) || p.To != "" || p.Text != "" || p.Target != "" || p.By != "" {
				return Event{}, false
			}
			return Event{Kind: EventRedact, Seq: seq, SID: s.SID, ID: p.ID, At: p.At}, true
		}
		return Event{}, false
	case "evt":
		var a Activity
		if !strict(payload, &a) || !validActivity(a) {
			return Event{}, false
		}
		return Event{Kind: EventActivity, Seq: seq, SID: s.SID, From: s.From.String(), Activity: &a}, true
	}
	return Event{}, false
}

func validText(s string) bool {
	n := len([]rune(s))
	return n >= 1 && n <= MaxText
}

func optionalID(s string) bool { return s == "" || IsID(s) }

func validActivity(a Activity) bool {
	if !IsTime(a.At) {
		return false
	}
	switch a.Kind {
	case "joined":
		return a.Host != "" && a.Cwd != "" && a.Client.Name != ""
	case "left":
		return oneOf(a.Reason, "disconnected", "kicked", "revoked", "closed")
	case "state":
		return oneOf(a.State, "working", "blocked", "done", "idle") && len([]rune(a.Note)) <= 500
	case "wait_start":
		return (a.From == "" || isWaitTarget(a.From)) && optionalID(a.ReplyTo) && a.TimeoutS >= 1 && a.TimeoutS <= 600
	case "wait_end":
		return oneOf(a.Result, "message", "timeout", "cancelled")
	case "refused":
		return oneOf(a.Reason, "removed", "taken")
	case "forgotten":
		// The operator dropped an agent that left from the lists. The hub writes it.
		return true
	case "gate":
		return IsGate(a.Gate) && (a.By == "" || IsToken(a.By))
	}
	return false
}

// --- The trace: what an agent does at its terminal -------------------------------------------

// Trace kinds.
const (
	TraceToolStart = "tool_start" // a tool call starts
	TraceToolEnd   = "tool_end"   // a tool call ends
	TraceSay       = "say"        // words of the agent
	TracePrompt    = "prompt"     // a prompt that a person typed at the agent's terminal
)

// Limits of the trace.
const (
	MaxTraceItems = 20   // items in one report
	MaxTraceText  = 2000 // characters of the text of one item
	MaxTraceName  = 128  // characters of an id or a tool name
	MaxTracePath  = 1024 // characters of a file path
)

// IsTraceKind reports whether s is a trace kind.
func IsTraceKind(s string) bool {
	return s == TraceToolStart || s == TraceToolEnd || s == TraceSay || s == TracePrompt
}

// TraceItem is one thing an agent did at its terminal, as a hook of Claude Code reports it.
// The hub keeps the newest items of each agent in memory and gives them only to the operator.
type TraceItem struct {
	// N and At come from the hub: the place of the item in the order of all items, and the
	// time the hub got it.
	N    int64  `json:"n,omitempty"`
	At   string `json:"at,omitempty"`
	Kind string `json:"kind"`
	// ID names the tool call: tool_start and tool_end of one call have the same ID. For say
	// it names the text: the hub drops a say item with an ID that it has for the agent.
	ID   string `json:"id,omitempty"`
	Tool string `json:"tool,omitempty"`
	// Text is one line that says what the tool call does, or the words.
	Text string `json:"text,omitempty"`
	// Failed: the tool call ended with an error.
	Failed bool `json:"failed,omitempty"`
	// MS is how long the tool call ran, in milliseconds.
	MS int64 `json:"ms,omitempty"`
	// File is the file that the tool call wrote, relative to the project directory when it
	// is in it.
	File string `json:"file,omitempty"`
	// Final: the words end a turn of the agent.
	Final bool `json:"final,omitempty"`
	// Before is the ID of the tool call that the words stand before. The hook can read the
	// words only after the call, so they get a later number than the start of the call.
	Before string `json:"before,omitempty"`
}

// TraceFile is one file that an agent changed.
type TraceFile struct {
	Path  string `json:"path"`
	Count int    `json:"count"` // how many tool calls wrote it
	At    string `json:"at"`    // the time of the last one
}

// --- The operator's feed ---------------------------------------------------------------------

// AdminEvent is one SSE event of GET /v1/admin/stream. Kind is the SSE event name.
type AdminEvent struct {
	Kind     string // event session kick presence snapshot trace
	Event    *Event
	Session  *SessionUpdate
	Kick     *KickUpdate
	Presence *PresenceUpdate
	Trace    *TraceUpdate
	Bucket   string // snapshot: sessions or presence
}

// SessionUpdate is a session record, or its removal when Record is nil.
type SessionUpdate struct {
	SID      string
	Revision int64
	Record   *SessionRecord
}

type KickUpdate struct {
	Key      string
	Revision int64
	Record   *KickRecord
}

type PresenceUpdate struct {
	Key      string
	Revision int64
	Record   *PresenceRecord
}

// TraceUpdate is new trace of one agent. Boot names the run of the hub that made it: the
// trace lives in the hub's memory, so a hub that starts again starts with none, and its item
// numbers start again.
type TraceUpdate struct {
	Boot   int64       `json:"boot"`
	Key    string      `json:"key"` // the presence key of the agent
	Branch string      `json:"branch,omitempty"`
	Items  []TraceItem `json:"items"`
	// Files are the changed files whose record is new with these items.
	Files []TraceFile `json:"files,omitempty"`
}

var errAdminEvent = errors.New("wire: bad admin event")

// ParseAdminEvent decodes the data of one feed event.
func ParseAdminEvent(data []byte) (AdminEvent, error) {
	var raw struct {
		Kind     string          `json:"kind"`
		Seq      int64           `json:"seq"`
		Subject  string          `json:"subject"`
		Payload  string          `json:"payload"`
		Session  string          `json:"session"`
		Key      string          `json:"key"`
		Revision int64           `json:"revision"`
		Record   json.RawMessage `json:"record"`
		Bucket   string          `json:"bucket"`
		TraceUpdate
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return AdminEvent{}, fmt.Errorf("%w: %v", errAdminEvent, err)
	}
	isNull := len(raw.Record) == 0 || bytes.Equal(raw.Record, []byte("null"))
	switch raw.Kind {
	case "event":
		e, ok := DecodeEvent(raw.Subject, []byte(raw.Payload), raw.Seq)
		if !ok {
			return AdminEvent{}, fmt.Errorf("%w: event %d on %s", errAdminEvent, raw.Seq, raw.Subject)
		}
		return AdminEvent{Kind: "event", Event: &e}, nil
	case "session":
		u := &SessionUpdate{SID: raw.Session, Revision: raw.Revision}
		if !isNull {
			u.Record = new(SessionRecord)
			if err := json.Unmarshal(raw.Record, u.Record); err != nil {
				return AdminEvent{}, fmt.Errorf("%w: %v", errAdminEvent, err)
			}
		}
		return AdminEvent{Kind: "session", Session: u}, nil
	case "kick":
		u := &KickUpdate{Key: raw.Key, Revision: raw.Revision}
		if !isNull {
			u.Record = new(KickRecord)
			if err := json.Unmarshal(raw.Record, u.Record); err != nil {
				return AdminEvent{}, fmt.Errorf("%w: %v", errAdminEvent, err)
			}
		}
		return AdminEvent{Kind: "kick", Kick: u}, nil
	case "presence":
		u := &PresenceUpdate{Key: raw.Key, Revision: raw.Revision}
		if !isNull {
			u.Record = new(PresenceRecord)
			if err := json.Unmarshal(raw.Record, u.Record); err != nil {
				return AdminEvent{}, fmt.Errorf("%w: %v", errAdminEvent, err)
			}
		}
		return AdminEvent{Kind: "presence", Presence: u}, nil
	case "snapshot":
		if raw.Bucket != "sessions" && raw.Bucket != "presence" {
			return AdminEvent{}, fmt.Errorf("%w: bucket %q", errAdminEvent, raw.Bucket)
		}
		return AdminEvent{Kind: "snapshot", Bucket: raw.Bucket}, nil
	case "trace":
		if _, ok := ParsePresenceKey(raw.Key); !ok {
			return AdminEvent{}, fmt.Errorf("%w: trace key %q", errAdminEvent, raw.Key)
		}
		u := raw.TraceUpdate
		u.Key = raw.Key
		return AdminEvent{Kind: "trace", Trace: &u}, nil
	}
	return AdminEvent{}, fmt.Errorf("%w: kind %q", errAdminEvent, raw.Kind)
}
