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
	"slices"
	"strings"
	"time"
)

// Token roles. A machine token acts as the agents of one machine. A watch token does the same,
// and it may also read every session (coop tui). Only the hub knows the difference: to every
// client, a watch token is a machine token.
const (
	RoleMachine = "machine"
	RoleWatch   = "watch"
)

// IsRole reports whether s is a token role.
func IsRole(s string) bool { return s == RoleMachine || s == RoleWatch }

// Broadcast is the recipient "all": the reserved name in addressing. No agent can have it.
const Broadcast = "all"

// MaxText is the longest message text.
const MaxText = 8000

var (
	tokenRE = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
	idRE    = regexp.MustCompile(`^[1-9][0-9]{0,15}$`)
)

// IsToken reports whether s is a name token: a session id or a machine name. No dots.
func IsToken(s string) bool { return tokenRE.MatchString(s) }

// IsAgentName reports whether s is a token that is not the reserved name.
func IsAgentName(s string) bool { return IsToken(s) && s != Broadcast }

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

// Subject is a subject of stream COOP: `coop.<sid>.msg|evt.<machine>.<agent>`.
type Subject struct {
	Kind string // msg or evt
	SID  string
	From Address
}

func BuildSubject(s Subject) string {
	return "coop." + s.SID + "." + s.Kind + "." + s.From.Machine + "." + s.From.Agent
}

func ParseSubject(subject string) (Subject, bool) {
	t := strings.Split(subject, ".")
	if len(t) < 3 || t[0] != "coop" || !IsToken(t[1]) {
		return Subject{}, false
	}
	if len(t) != 5 || (t[2] != "msg" && t[2] != "evt") || !IsToken(t[3]) || !IsAgentName(t[4]) {
		return Subject{}, false
	}
	return Subject{Kind: t[2], SID: t[1], From: Address{Agent: t[4], Machine: t[3]}}, true
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

// SessionRecord is the record of one session.
type SessionRecord struct {
	CreatedAt string `json:"created_at"`
}

// SessionInfo is one entry of GET /v1/admin/sessions: the id with its record.
type SessionInfo struct {
	Session string `json:"session"`
	SessionRecord
}

// Waiting is what an agent waits for, in its presence record.
type Waiting struct {
	On      string `json:"on,omitempty"` // a peer address
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
}

// --- Stream events ---------------------------------------------------------------------------

// Event kinds.
const (
	EventMsg      = "msg"
	EventActivity = "evt"
)

// Event is one decoded event of stream COOP. Seq is the stream sequence and the message id.
type Event struct {
	Kind string
	Seq  int64
	SID  string
	// From is the address of the sender.
	From string
	// Message fields.
	To      string // Broadcast or an address
	Text    string
	ReplyTo string
	SentAt  string
	// Activity fields.
	Activity *Activity
}

// Activity is what an agent did, on `coop.<sid>.evt.<machine>.<agent>`.
type Activity struct {
	Kind string `json:"kind"` // joined left state wait_start wait_end refused
	At   string `json:"at"`
	// joined
	Host   string `json:"host,omitempty"`
	Cwd    string `json:"cwd,omitempty"`
	Client Client `json:"client,omitzero"`
	// left: disconnected, revoked or closed. refused (a join that the hub did not let in):
	// taken (another session holds the name).
	Reason string `json:"reason,omitempty"`
	// state: working blocked done idle
	State string `json:"state,omitempty"`
	Note  string `json:"note,omitempty"`
	// wait_start
	From     string `json:"from,omitempty"` // a peer address
	ReplyTo  string `json:"reply_to,omitempty"`
	TimeoutS int    `json:"timeout_s,omitempty"`
	// wait_end: message timeout cancelled
	Result string `json:"result,omitempty"`
}

type msgPayload struct {
	To      string `json:"to"`
	Text    string `json:"text"`
	ReplyTo string `json:"reply_to,omitempty"`
	SentAt  string `json:"sent_at"`
}

// EncodeEvent gives the subject and the payload that carry e. The inverse of DecodeEvent.
func EncodeEvent(e Event) (subject string, payload []byte, err error) {
	switch e.Kind {
	case EventMsg:
		from, ok := ParseAddress(e.From)
		if !ok {
			return "", nil, fmt.Errorf("wire: bad sender %q", e.From)
		}
		payload, err = json.Marshal(msgPayload{To: e.To, Text: e.Text, ReplyTo: e.ReplyTo, SentAt: e.SentAt})
		return BuildSubject(Subject{Kind: "msg", SID: e.SID, From: from}), payload, err
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

func isRecipient(s string) bool { return s == Broadcast || isAddress(s) }

func isAddress(s string) bool {
	_, ok := ParseAddress(s)
	return ok
}

// DecodeEvent reads one stored event. An unknown subject or a bad payload gives false. An
// event that this version dropped (a message or an action on ops, a gate or a
// forgotten activity) gives false too: a log of an older hub holds such events.
func DecodeEvent(subject string, payload []byte, seq int64) (Event, bool) {
	s, ok := ParseSubject(subject)
	if !ok {
		return Event{}, false
	}
	switch s.Kind {
	case "msg":
		var p msgPayload
		if !strict(payload, &p) || !isRecipient(p.To) || !validText(p.Text) || !optionalID(p.ReplyTo) || !IsTime(p.SentAt) {
			return Event{}, false
		}
		return Event{Kind: EventMsg, Seq: seq, SID: s.SID, From: s.From.String(), To: p.To, Text: p.Text, ReplyTo: p.ReplyTo, SentAt: p.SentAt}, true
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
		return slices.Contains([]string{"disconnected", "revoked", "closed"}, a.Reason)
	case "state":
		return slices.Contains([]string{"working", "blocked", "done", "idle"}, a.State) && len([]rune(a.Note)) <= 500
	case "wait_start":
		return (a.From == "" || isAddress(a.From)) && optionalID(a.ReplyTo) && a.TimeoutS >= 1 && a.TimeoutS <= 600
	case "wait_end":
		return slices.Contains([]string{"message", "timeout", "cancelled"}, a.Result)
	case "refused":
		return a.Reason == "taken"
	}
	return false
}

// --- The watch feed --------------------------------------------------------------------------

// AdminEvent is one SSE event of GET /v1/admin/stream. Kind is the SSE event name.
type AdminEvent struct {
	Kind     string // event session presence snapshot
	Event    *Event
	Session  *SessionUpdate
	Presence *PresenceUpdate
	Bucket   string // snapshot: sessions or presence
}

// SessionUpdate is a session record, or its removal when Record is nil.
type SessionUpdate struct {
	SID      string
	Revision int64
	Record   *SessionRecord
}

type PresenceUpdate struct {
	Key      string
	Revision int64
	Record   *PresenceRecord
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
	}
	return AdminEvent{}, fmt.Errorf("%w: kind %q", errAdminEvent, raw.Kind)
}
