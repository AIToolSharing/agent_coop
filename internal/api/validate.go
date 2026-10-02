package api

// The request checks of packages/core/src/api.ts, by hand. The served document carries the
// same rules; the contract test (Schemathesis, positive and negative data) checks that the
// two agree. A check here that the document does not state is a bug in one of the two.

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"unicode/utf8"

	"github.com/AIToolSharing/agent_coop/internal/hub"
	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// invalid is a request that fails the schema: 422.
type invalid string

func (e invalid) Error() string { return string(e) }

func runes(s string) int { return utf8.RuneCountInString(s) }

func isAddress(s string) bool {
	_, ok := wire.ParseAddress(s)
	return ok
}

// isPeerInput reports a peer as a client writes it: a bare agent name, or `agent@machine`.
func isPeerInput(s string) bool {
	if bytes.ContainsRune([]byte(s), '@') {
		return isAddress(s)
	}
	return wire.IsAgentName(s)
}

func isRecipientInput(s string) bool {
	return s == wire.Broadcast || s == wire.Operator || isPeerInput(s)
}

func isWaitTargetInput(s string) bool { return s == wire.Operator || isPeerInput(s) }

func isText(s string) bool { n := runes(s); return n >= 1 && n <= wire.MaxText }

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

func oneOf(s string, xs ...string) bool {
	for _, x := range xs {
		if s == x {
			return true
		}
	}
	return false
}

// optString is an optional string field. An absent field is fine; a JSON null is not, as with
// zod's `.optional()`. Found by Schemathesis: a null was taken as absent.
type optString struct {
	set bool
	val string
}

func (o *optString) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return errors.New("null is not allowed")
	}
	o.set = true
	return json.Unmarshal(b, &o.val)
}

// decodeStrict reads exactly one JSON object with no unknown fields into v.
func decodeStrict(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return invalid("invalid JSON body: " + err.Error())
	}
	if dec.More() {
		return invalid("invalid JSON body: more than one value")
	}
	return nil
}

// query checks that q holds only the given keys, and gives the first value of each.
func query(q url.Values, keys ...string) (map[string]string, error) {
	out := map[string]string{}
	for k, vs := range q {
		if !oneOf(k, keys...) {
			return nil, invalid("unknown query parameter " + k)
		}
		if len(vs) > 0 {
			out[k] = vs[0]
		}
	}
	return out, nil
}

// --- Agent routes ----------------------------------------------------------------------------

func parseStreamQuery(q url.Values) (hub.StreamQuery, error) {
	m, err := query(q, "agent", "instance", "host", "cwd", "client_name", "client_version")
	if err != nil {
		return hub.StreamQuery{}, err
	}
	for _, k := range []string{"agent", "instance", "host", "cwd", "client_name", "client_version"} {
		if _, ok := m[k]; !ok {
			return hub.StreamQuery{}, invalid("missing query parameter " + k)
		}
	}
	s := hub.StreamQuery{Agent: m["agent"], Instance: m["instance"], Host: m["host"], Cwd: m["cwd"], ClientName: m["client_name"], ClientVersion: m["client_version"]}
	switch {
	case !wire.IsAgentName(s.Agent):
		return s, invalid("agent: not a valid agent name")
	case !uuidRE.MatchString(s.Instance):
		return s, invalid("instance: not a UUID")
	case runes(s.Host) > 256, runes(s.Cwd) > 4096, runes(s.ClientName) > 64, runes(s.ClientVersion) > 64:
		return s, invalid("a query value is too long")
	}
	return s, nil
}

func parseAgentQuery(q url.Values) (string, error) {
	m, err := query(q, "agent")
	if err != nil {
		return "", err
	}
	if !wire.IsAgentName(m["agent"]) {
		return "", invalid("agent: not a valid agent name")
	}
	return m["agent"], nil
}

func parseHistoryQuery(q url.Values) (hub.HistoryQuery, error) {
	m, err := query(q, "agent", "with", "limit")
	if err != nil {
		return hub.HistoryQuery{}, err
	}
	h := hub.HistoryQuery{Agent: m["agent"], With: m["with"], Limit: 50}
	if !wire.IsAgentName(h.Agent) {
		return h, invalid("agent: not a valid agent name")
	}
	if with, ok := m["with"]; ok && !isPeerInput(with) {
		return h, invalid("with: not a peer")
	}
	if limit, ok := m["limit"]; ok {
		n, err := strconv.Atoi(limit)
		if err != nil || n < 1 || n > 200 {
			return h, invalid("limit: an integer from 1 to 200")
		}
		h.Limit = n
	}
	return h, nil
}

type sendBody struct {
	Agent   string    `json:"agent"`
	To      string    `json:"to"`
	Text    string    `json:"text"`
	ReplyTo optString `json:"reply_to"`
}

func parseSend(body []byte) (hub.SendRequest, error) {
	var b sendBody
	if err := decodeStrict(body, &b); err != nil {
		return hub.SendRequest{}, err
	}
	r := hub.SendRequest{Agent: b.Agent, To: b.To, Text: b.Text, ReplyTo: b.ReplyTo.val}
	switch {
	case !wire.IsAgentName(r.Agent):
		return r, invalid("agent: not a valid agent name")
	case !isRecipientInput(r.To):
		return r, invalid("to: all, operator, or a peer")
	case !isText(r.Text):
		return r, invalid("text: 1 to 8000 characters")
	case b.ReplyTo.set && !wire.IsID(r.ReplyTo):
		return r, invalid("reply_to: not a message id")
	}
	return r, nil
}

// activityBody holds the union of the four activity shapes; the kind says which fields may be
// set. A field of another shape is an unknown field.
type activityBody struct {
	Kind     optString    `json:"kind"`
	Agent    optString    `json:"agent"`
	State    optString    `json:"state"`
	Note     optString    `json:"note"`
	ID       optString    `json:"id"`
	Via      optString    `json:"via"`
	From     optString    `json:"from"`
	ReplyTo  optString    `json:"reply_to"`
	TimeoutS *json.Number `json:"timeout_s"`
	Result   optString    `json:"result"`
}

func parseActivity(body []byte) (hub.ActivityRequest, error) {
	var b activityBody
	if err := decodeStrict(body, &b); err != nil {
		return hub.ActivityRequest{}, err
	}
	if !b.Kind.set || !wire.IsAgentName(b.Agent.val) {
		return hub.ActivityRequest{}, invalid("kind and a valid agent are required")
	}
	r := hub.ActivityRequest{Kind: b.Kind.val, Agent: b.Agent.val}
	set := map[string]bool{
		"state": b.State.set, "note": b.Note.set, "id": b.ID.set, "via": b.Via.set,
		"from": b.From.set, "reply_to": b.ReplyTo.set, "timeout_s": b.TimeoutS != nil, "result": b.Result.set,
	}
	// allow names the fields of the kind; any other set field fails.
	allow := func(fields ...string) error {
		for f, isSet := range set {
			if isSet && !oneOf(f, fields...) {
				return invalid("unknown field " + f + " for kind " + r.Kind)
			}
		}
		return nil
	}
	switch r.Kind {
	case "state":
		if err := allow("state", "note"); err != nil {
			return r, err
		}
		if !oneOf(b.State.val, "working", "blocked", "done", "idle") {
			return r, invalid("state: working, blocked, done or idle")
		}
		if runes(b.Note.val) > 500 {
			return r, invalid("note: at most 500 characters")
		}
		r.State, r.Note = b.State.val, b.Note.val
	case "delivered":
		if err := allow("id", "via"); err != nil {
			return r, err
		}
		if !wire.IsID(b.ID.val) || !oneOf(b.Via.val, "push", "pull", "ask") {
			return r, invalid("delivered needs a message id and via push, pull or ask")
		}
		r.ID, r.Via = b.ID.val, b.Via.val
	case "wait_start":
		if err := allow("from", "reply_to", "timeout_s"); err != nil {
			return r, err
		}
		if b.From.set && !isWaitTargetInput(b.From.val) {
			return r, invalid("from: operator or a peer")
		}
		if b.ReplyTo.set && !wire.IsID(b.ReplyTo.val) {
			return r, invalid("reply_to: not a message id")
		}
		if b.TimeoutS == nil {
			return r, invalid("timeout_s is required")
		}
		n, err := b.TimeoutS.Int64()
		if err != nil || n < 1 || n > 600 {
			return r, invalid("timeout_s: an integer from 1 to 600")
		}
		r.From, r.ReplyTo, r.TimeoutS = b.From.val, b.ReplyTo.val, int(n)
	case "wait_end":
		if err := allow("result"); err != nil {
			return r, err
		}
		if !oneOf(b.Result.val, "message", "timeout", "cancelled") {
			return r, invalid("result: message, timeout or cancelled")
		}
		r.Result = b.Result.val
	default:
		return r, invalid("kind: state, delivered, wait_start or wait_end")
	}
	return r, nil
}

// --- Admin routes ----------------------------------------------------------------------------

type createSessionBody struct {
	Session string    `json:"session"`
	Title   optString `json:"title"`
}

func parseCreateSession(body []byte) (sid, title string, err error) {
	var b createSessionBody
	if err := decodeStrict(body, &b); err != nil {
		return "", "", err
	}
	if !wire.IsToken(b.Session) {
		return "", "", invalid("session: not a valid session name")
	}
	if runes(b.Title.val) > 200 {
		return "", "", invalid("title: at most 200 characters")
	}
	return b.Session, b.Title.val, nil
}

func parseTarget(body []byte) (wire.Address, error) {
	var b struct {
		Target string `json:"target"`
	}
	if err := decodeStrict(body, &b); err != nil {
		return wire.Address{}, err
	}
	a, ok := wire.ParseAddress(b.Target)
	if !ok {
		return a, invalid("target: not an address agent@machine")
	}
	return a, nil
}

func parseRedact(body []byte) (string, error) {
	var b struct {
		ID string `json:"id"`
	}
	if err := decodeStrict(body, &b); err != nil {
		return "", err
	}
	if !wire.IsID(b.ID) {
		return "", invalid("id: not a message id")
	}
	return b.ID, nil
}

type operatorSendBody struct {
	To      string    `json:"to"`
	Text    string    `json:"text"`
	ReplyTo optString `json:"reply_to"`
}

func parseOperatorSend(body []byte) (hub.OperatorSendRequest, error) {
	var b operatorSendBody
	if err := decodeStrict(body, &b); err != nil {
		return hub.OperatorSendRequest{}, err
	}
	r := hub.OperatorSendRequest{To: b.To, Text: b.Text, ReplyTo: b.ReplyTo.val}
	switch {
	case r.To != wire.Broadcast && !isPeerInput(r.To):
		return r, invalid("to: all or a peer")
	case !isText(r.Text):
		return r, invalid("text: 1 to 8000 characters")
	case b.ReplyTo.set && !wire.IsID(r.ReplyTo):
		return r, invalid("reply_to: not a message id")
	}
	return r, nil
}
