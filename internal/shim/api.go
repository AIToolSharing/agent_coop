package shim

// The HTTP contract between the hub and the shim, as packages/core/src/api.ts defines it. The
// shim checks every value that it gets from the hub, like the TypeScript schemas do. A value
// that fails the check is dropped (an event) or makes the call fail (a response).

import (
	"bytes"
	"encoding/json"
	"slices"
	"unicode/utf8"

	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// message is one message as the hub gives it to an agent. The SSE id of a message is its id.
type message struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Text    string `json:"text"`
	ReplyTo string `json:"reply_to,omitempty"`
	SentAt  string `json:"sent_at"`
}

func (m message) valid() bool {
	return wire.IsID(m.ID) && (m.From == wire.Operator || isAddress(m.From)) && isTo(m.To) &&
		validText(m.Text) && optionalID(m.ReplyTo) && wire.IsTime(m.SentAt)
}

// Notice kinds.
const (
	noticeKicked   = "kicked"
	noticeClosed   = "closed"
	noticeReopened = "reopened"
	noticeRedacted = "redacted"
	noticePeerLeft = "peer_left"
	// The operator's gate: held and paused stop the agent's work, released lets it go on.
	noticeHeld     = "held"
	noticePaused   = "paused"
	noticeReleased = "released"
)

// notice is something the agent must know that is not a message.
type notice struct {
	Kind string `json:"kind"`
	// ID is the withdrawn message, for redacted.
	ID string `json:"id,omitempty"`
	// Peer is the peer that left, for peer_left.
	Peer string `json:"peer,omitempty"`
	At   string `json:"at"`
}

func (n notice) valid() bool {
	kinds := []string{noticeKicked, noticeClosed, noticeReopened, noticeRedacted, noticePeerLeft, noticeHeld, noticePaused, noticeReleased}
	return slices.Contains(kinds, n.Kind) && optionalID(n.ID) && (n.Peer == "" || isAddress(n.Peer)) &&
		wire.IsTime(n.At)
}

// joinedEvent is the first event of a stream.
type joinedEvent struct {
	Me      string `json:"me"`
	Session string `json:"session"`
	// Gate is whether the user lets the agent work: run, held or paused. A service of an
	// earlier version gives none; that counts as run.
	Gate string `json:"gate"`
}

func (j joinedEvent) valid() bool {
	return isAddress(j.Me) && wire.IsToken(j.Session) && (j.Gate == "" || wire.IsGate(j.Gate))
}

type sendRequest struct {
	Agent   string `json:"agent"`
	To      string `json:"to"`
	Text    string `json:"text"`
	ReplyTo string `json:"reply_to,omitempty"`
}

// sendResponse tells where a message went. Online is false when the recipient left the
// session: the hub keeps the message and gives it to the recipient when it joins again.
type sendResponse struct {
	ID     string `json:"id"`
	To     string `json:"to"`
	Online bool   `json:"online"`
	SentAt string `json:"sent_at"`
	// State is the peer's state (working, blocked, done, idle) or away; empty for all and
	// operator.
	State string `json:"state,omitempty"`
}

func (r sendResponse) valid() bool {
	return wire.IsID(r.ID) && isTo(r.To) && wire.IsTime(r.SentAt) &&
		(r.State == "" || r.State == "away" || slices.Contains(agentStates, r.State))
}

// activity is what an agent reports: state, wait_start or wait_end.
type activity struct {
	Kind     string `json:"kind"`
	Agent    string `json:"agent"`
	State    string `json:"state,omitempty"`
	Note     string `json:"note,omitempty"`
	From     string `json:"from,omitempty"`
	ReplyTo  string `json:"reply_to,omitempty"`
	TimeoutS int    `json:"timeout_s,omitempty"`
	Result   string `json:"result,omitempty"`
}

// peer is one other agent of the session. A peer that left has Online false; it keeps its
// last state and can still get messages.
type peer struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	Note      string `json:"note,omitempty"`
	Online    bool   `json:"online"`
	WaitingOn string `json:"waiting_on,omitempty"`
}

var agentStates = []string{"working", "blocked", "done", "idle"}

func (p peer) valid() bool {
	return isAddress(p.Name) && slices.Contains(agentStates, p.State) && utf8.RuneCountInString(p.Note) <= 500 &&
		(p.WaitingOn == "" || p.WaitingOn == wire.Operator || isAddress(p.WaitingOn))
}

type sessionView struct {
	Session string `json:"session"`
	Status  string `json:"status"`
	Me      string `json:"me"`
	Peers   []peer `json:"peers"`
}

func (v sessionView) valid() bool {
	if !wire.IsToken(v.Session) || (v.Status != "open" && v.Status != "closed") || !isAddress(v.Me) || v.Peers == nil {
		return false
	}
	for _, p := range v.Peers {
		if !p.valid() {
			return false
		}
	}
	return true
}

type historyResponse struct {
	Messages []message `json:"messages"`
}

func (h historyResponse) valid() bool {
	if h.Messages == nil {
		return false
	}
	for _, m := range h.Messages {
		if !m.valid() {
			return false
		}
	}
	return true
}

// errorCodes are the values of ErrorBody.error.
var errorCodes = []string{"unauthorized", "forbidden", "not_found", "conflict", "ambiguous", "too_large", "invalid", "rate_limited", "unavailable"}

// errorBody is the body of every error response of the hub.
type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func (e errorBody) valid() bool { return slices.Contains(errorCodes, e.Error) }

// decode reads exactly one JSON value with no unknown fields into v, and checks it.
func decode[T interface{ valid() bool }](data []byte) (T, bool) {
	var v T
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil || dec.More() {
		return v, false
	}
	return v, v.valid()
}

func isAddress(s string) bool {
	_, ok := wire.ParseAddress(s)
	return ok
}

// isTo reports whether s is the recipient of a message: all, operator, or an address.
func isTo(s string) bool { return s == wire.Broadcast || s == wire.Operator || isAddress(s) }

func validText(s string) bool {
	n := utf8.RuneCountInString(s)
	return n >= 1 && n <= wire.MaxText
}

func optionalID(s string) bool { return s == "" || wire.IsID(s) }
