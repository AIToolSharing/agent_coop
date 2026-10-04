package api_test

// Small HTTP and SSE clients for the hub tests. They mirror packages/hub/test/harness.ts.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// defaultWait is the time that stream.next and stream.ended wait when the caller gives 0, as in
// harness.ts.
const defaultWait = 5 * time.Second

// doHTTP sends req on a new connection, so that no idle connection survives harness.restart.
// A transport error gives status 0 and the error text as the body: the status check of the
// test then fails and shows the cause.
func doHTTP(req *http.Request) (int, []byte) {
	req.Close = true
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, []byte(err.Error())
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return 0, []byte(err.Error())
	}
	return res.StatusCode, b
}

// httpCall sends one request with a bearer token. A nil body sends no body.
func httpCall(base, token, method, path string, body any) (int, []byte) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, []byte(err.Error())
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, rd)
	if err != nil {
		return 0, []byte(err.Error())
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return doHTTP(req)
}

// fetch gets u with no token, as a bare fetch in hub.int.test.ts does.
func fetch(u string) (int, []byte) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return 0, []byte(err.Error())
	}
	return doHTTP(req)
}

// newUUID gives a random version 4 UUID: the instance of a new process.
func newUUID() string {
	var b [16]byte
	rand.Read(b[:]) // crypto/rand.Read never returns an error.
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// --- The agent API ---------------------------------------------------------------------------

// api is the agent API with a machine token, as class Api in harness.ts.
type api struct{ base, token string }

func (a api) req(method, path string, body any) (int, []byte) {
	return httpCall(a.base, a.token, method, path, body)
}

// send posts a message. An empty replyTo sends no reply_to.
func (a api) send(sid, agent, to, text, replyTo string) (int, []byte) {
	body := map[string]any{"agent": agent, "to": to, "text": text}
	if replyTo != "" {
		body["reply_to"] = replyTo
	}
	return a.req(http.MethodPost, "/v1/sessions/"+sid+"/messages", body)
}

func (a api) activity(sid string, body map[string]any) (int, []byte) {
	return a.req(http.MethodPost, "/v1/sessions/"+sid+"/activity", body)
}

// gate asks whether the operator lets the agent work.
func (a api) gate(sid, agent string) (int, []byte) {
	return a.req(http.MethodPost, "/v1/sessions/"+sid+"/gate", map[string]any{"agent": agent})
}

func (a api) view(sid, agent string) (int, []byte) {
	return a.req(http.MethodGet, "/v1/sessions/"+sid+"?agent="+agent, nil)
}

func (a api) history(sid, agent, extra string) (int, []byte) {
	return a.req(http.MethodGet, "/v1/sessions/"+sid+"/messages?agent="+agent+extra, nil)
}

type streamConfig struct {
	instance    string
	lastEventID *string
	// extra holds more query parameters, after the six that every join has.
	extra [][2]string
}

type streamOpt func(*streamConfig)

// withQuery adds one query parameter to the join.
func withQuery(key, value string) streamOpt {
	return func(c *streamConfig) { c.extra = append(c.extra, [2]string{key, value}) }
}

// withInstance joins as an instance that joined before: the join takes over its old stream.
func withInstance(id string) streamOpt { return func(c *streamConfig) { c.instance = id } }

// withLastEventID sends the Last-Event-ID header, also when id is empty.
func withLastEventID(id string) streamOpt { return func(c *streamConfig) { c.lastEventID = &id } }

// stream opens the stream of an agent. The instance is a new random id (a new process) unless
// withInstance gives one.
func (a api) stream(sid, agent string, opts ...streamOpt) *stream {
	c := streamConfig{instance: newUUID()}
	for _, o := range opts {
		o(&c)
	}
	// The query has the order of the URLSearchParams in harness.ts.
	q := [][2]string{
		{"agent", agent},
		{"instance", c.instance},
		{"host", "test-host"},
		{"cwd", "/work"},
		{"client_name", "test"},
		{"client_version", "0"},
	}
	q = append(q, c.extra...)
	var qs strings.Builder
	for i, p := range q {
		if i > 0 {
			qs.WriteByte('&')
		}
		qs.WriteString(url.QueryEscape(p[0]) + "=" + url.QueryEscape(p[1]))
	}
	return openStream(a.base+"/v1/sessions/"+sid+"/stream?"+qs.String(), a.token, c.lastEventID, c.instance)
}

// --- SSE -------------------------------------------------------------------------------------

// sseEvent is one server-sent event. A field that the event does not have is "".
type sseEvent struct{ event, data, id string }

// stream is one SSE connection. Events are buffered; next waits for one that matches.
type stream struct {
	res      *http.Response // nil when the request did not reach the hub
	instance string
	body     []byte // the JSON of a refused stream, or the error text when there is no response
	cancel   context.CancelFunc
	done     chan struct{} // closed when the stream is closed

	mu      sync.Mutex
	events  []sseEvent
	closed  bool
	changed chan struct{} // closed and replaced when an event comes or the stream closes
}

func openStream(u, token string, lastEventID *string, instance string) *stream {
	ctx, cancel := context.WithCancel(context.Background())
	s := &stream{instance: instance, cancel: cancel, done: make(chan struct{}), changed: make(chan struct{})}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err == nil {
		req.Header.Set("Authorization", "Bearer "+token)
		if lastEventID != nil {
			req.Header.Set("Last-Event-ID", *lastEventID)
		}
		req.Close = true
		s.res, err = http.DefaultClient.Do(req)
	}
	if err != nil {
		s.body = []byte(err.Error())
		s.end()
		return s
	}
	if s.res.StatusCode < 200 || s.res.StatusCode > 299 {
		// A refused stream has a JSON body.
		s.body, _ = io.ReadAll(s.res.Body)
		s.res.Body.Close()
		s.end()
		return s
	}
	go s.pump(s.res.Body)
	return s
}

// status gives the HTTP status, or 0 when the request did not reach the hub.
func (s *stream) status() int {
	if s.res == nil {
		return 0
	}
	return s.res.StatusCode
}

// result closes the stream and gives its status and body, for a stream that the test only
// checks the status of.
func (s *stream) result() (int, []byte) {
	s.close()
	return s.status(), s.body
}

// wakeLocked wakes every waiter. The caller holds s.mu.
func (s *stream) wakeLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// end marks the stream closed and wakes the waiters.
func (s *stream) end() {
	s.mu.Lock()
	s.closed = true
	s.wakeLocked()
	s.mu.Unlock()
	s.cancel()
	close(s.done)
}

// pump reads the events until the test or the server closes the stream. A blank line sends
// the event; a line that starts with ':' is a comment (the hub sends ": ping").
func (s *stream) pump(body io.ReadCloser) {
	defer s.end()
	defer body.Close()
	rd := bufio.NewReader(body)
	var e sseEvent
	var buf strings.Builder // the data lines, each with "\n"
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return // The pending event is discarded.
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		switch {
		case line == "":
			if buf.Len() > 0 {
				e.data = strings.TrimSuffix(buf.String(), "\n")
				s.mu.Lock()
				s.events = append(s.events, e)
				s.wakeLocked()
				s.mu.Unlock()
			}
			e = sseEvent{}
			buf.Reset()
		case strings.HasPrefix(line, ":"):
		default:
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				e.event = value
			case "data":
				buf.WriteString(value + "\n")
			case "id":
				e.id = value
			}
		}
	}
}

// next waits for the first buffered or future event that matches pred, and removes it from
// the buffer. A nil pred matches every event; a 0 timeout is defaultWait.
func (s *stream) next(pred func(sseEvent) bool, timeout time.Duration) (sseEvent, error) {
	if pred == nil {
		pred = func(sseEvent) bool { return true }
	}
	if timeout == 0 {
		timeout = defaultWait
	}
	end := time.Now().Add(timeout)
	for {
		s.mu.Lock()
		for i, e := range s.events {
			if pred(e) {
				s.events = slices.Delete(s.events, i, i+1)
				s.mu.Unlock()
				return e, nil
			}
		}
		closed, changed := s.closed, s.changed
		s.mu.Unlock()
		if closed {
			return sseEvent{}, errors.New("stream closed before the event came")
		}
		left := time.Until(end)
		if left <= 0 {
			return sseEvent{}, errors.New("timed out waiting for event")
		}
		timer := time.NewTimer(left)
		select {
		case <-changed:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// wait is next with defaultWait. When no event matches, the test fails.
func (s *stream) wait(t *testing.T, pred func(sseEvent) bool) sseEvent {
	t.Helper()
	e, err := s.next(pred, 0)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// none fails the test when an event that matches pred comes within d (in TS: `rejects.toThrow()`).
func (s *stream) none(t *testing.T, pred func(sseEvent) bool, d time.Duration) {
	t.Helper()
	if e, err := s.next(pred, d); err == nil {
		t.Fatalf("unexpected event %+v", e)
	}
}

// ended waits until the server closes the stream. A 0 timeout is defaultWait.
func (s *stream) ended(timeout time.Duration) error {
	if timeout == 0 {
		timeout = defaultWait
	}
	select {
	case <-s.done:
		return nil
	case <-time.After(timeout):
		return errors.New("stream did not end")
	}
}

// close cancels the request and waits until the stream is closed. A second close does nothing.
func (s *stream) close() {
	s.cancel()
	<-s.done
}

// isMsg matches a message event.
func isMsg(e sseEvent) bool { return e.event == "message" }

// eventIs matches the events of one name.
func eventIs(name string) func(sseEvent) bool {
	return func(e sseEvent) bool { return e.event == name }
}

// --- The admin API ---------------------------------------------------------------------------

// admin is the admin API with an operator token, as class Admin in harness.ts.
type admin struct{ base, token string }

func (a admin) req(method, path string, body any) (int, []byte) {
	return httpCall(a.base, a.token, method, "/v1/admin"+path, body)
}

func (a admin) sessions() (int, []byte) { return a.req(http.MethodGet, "/sessions", nil) }

// create creates a session. An empty title sends no title.
func (a admin) create(session, title string) (int, []byte) {
	body := map[string]any{"session": session}
	if title != "" {
		body["title"] = title
	}
	return a.req(http.MethodPost, "/sessions", body)
}

func (a admin) close(sid string) (int, []byte) {
	return a.req(http.MethodPost, "/sessions/"+sid+"/close", nil)
}

func (a admin) reopen(sid string) (int, []byte) {
	return a.req(http.MethodPost, "/sessions/"+sid+"/reopen", nil)
}

func (a admin) delete(sid string) (int, []byte) {
	return a.req(http.MethodDelete, "/sessions/"+sid, nil)
}

func (a admin) kick(sid, target string) (int, []byte) {
	return a.req(http.MethodPost, "/sessions/"+sid+"/kick", map[string]any{"target": target})
}

func (a admin) unkick(sid, target string) (int, []byte) {
	return a.req(http.MethodPost, "/sessions/"+sid+"/unkick", map[string]any{"target": target})
}

// gate sets the gate of one agent, or of every agent of the session when target is "".
func (a admin) gate(sid, target, gate string) (int, []byte) {
	body := map[string]any{"gate": gate}
	if target != "" {
		body["target"] = target
	}
	return a.req(http.MethodPost, "/sessions/"+sid+"/gate", body)
}

func (a admin) hold(sid string, hold bool) (int, []byte) {
	return a.req(http.MethodPost, "/sessions/"+sid+"/hold", map[string]any{"hold": hold})
}

func (a admin) forget(sid, target string) (int, []byte) {
	return a.req(http.MethodPost, "/sessions/"+sid+"/forget", map[string]any{"target": target})
}

func (a admin) redact(sid, id string) (int, []byte) {
	return a.req(http.MethodPost, "/sessions/"+sid+"/redact", map[string]any{"id": id})
}

// send posts a message as the operator. An empty replyTo sends no reply_to.
func (a admin) send(sid, to, text, replyTo string) (int, []byte) {
	body := map[string]any{"to": to, "text": text}
	if replyTo != "" {
		body["reply_to"] = replyTo
	}
	return a.req(http.MethodPost, "/sessions/"+sid+"/messages", body)
}

// stream opens the operator's feed: every event from the start (or after lastEventID) and the
// buckets.
func (a admin) stream(lastEventID ...string) *stream {
	var id *string
	if len(lastEventID) > 0 {
		id = &lastEventID[0]
	}
	return openStream(a.base+"/v1/admin/stream", a.token, id, "")
}

// operator does over the admin API what hub.int.test.ts does with the broker Operator (h.op).
// The Operator throws when an action fails; here the test fails.
type operator struct{ a admin }

func (o operator) createSession(t *testing.T, sid string) {
	t.Helper()
	wantStatus(t, http.StatusOK)(o.a.create(sid, ""))
}

func (o operator) closeSession(t *testing.T, sid string) {
	t.Helper()
	wantStatus(t, http.StatusNoContent)(o.a.close(sid))
}

func (o operator) reopenSession(t *testing.T, sid string) {
	t.Helper()
	wantStatus(t, http.StatusNoContent)(o.a.reopen(sid))
}

func (o operator) kick(t *testing.T, sid, target string) {
	t.Helper()
	wantStatus(t, http.StatusNoContent)(o.a.kick(sid, target))
}

// redact gives false when id is not a message of the session, as Operator.redact does.
func (o operator) redact(t *testing.T, sid, id string) bool {
	t.Helper()
	status, body := o.a.redact(sid, id)
	if status != http.StatusNoContent && status != http.StatusNotFound {
		t.Fatalf("redact %s %s: status %d: %s", sid, id, status, body)
	}
	return status == http.StatusNoContent
}

// send sends a message as the operator and gives its id.
func (o operator) send(t *testing.T, sid, to, text string) string {
	t.Helper()
	id := parse[operatorSendResponse](t, wantStatus(t, http.StatusOK)(o.a.send(sid, to, text, ""))).ID
	if !wire.IsID(id) {
		t.Fatalf("send: bad id %q", id)
	}
	return id
}

func (o operator) listSessions(t *testing.T) []wire.SessionInfo {
	t.Helper()
	return parse[sessionList](t, wantStatus(t, http.StatusOK)(o.a.sessions())).Sessions
}

// getSession gives the record of a session, or false when the session does not exist.
func (o operator) getSession(t *testing.T, sid string) (wire.SessionRecord, bool) {
	t.Helper()
	for _, s := range o.listSessions(t) {
		if s.Session == sid {
			return s.SessionRecord, true
		}
	}
	return wire.SessionRecord{}, false
}

// --- Checks ----------------------------------------------------------------------------------

// wantStatus gives a check of a response: wantStatus(t, 200)(mac1.send(...)). The check fails
// the test, with the body, when the status is not want. Else it gives the body.
func wantStatus(t *testing.T, want int) func(int, []byte) []byte {
	t.Helper()
	return func(status int, body []byte) []byte {
		t.Helper()
		if status != want {
			t.Fatalf("status %d, want %d: %s", status, want, body)
		}
		return body
	}
}

// jsonOf gives the body of a response whose status the test does not check.
func jsonOf(_ int, body []byte) []byte { return body }

// parse decodes raw into a T. A type that has a zod schema in TS checks its shape in its
// UnmarshalJSON. A failed parse fails the test, as a failed zod parse throws.
func parse[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parse %T: %v: %s", v, err, raw)
	}
	return v
}

// data parses the JSON of an event, as data() in harness.ts.
func data[T any](t *testing.T, e sseEvent) T {
	t.Helper()
	return parse[T](t, []byte(e.data))
}

// strictDecode decodes b into v as a zod strictObject parse does: a field that v does not have
// fails, and each field in required must be present and not null.
func strictDecode(b []byte, v any, required ...string) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	for _, k := range required {
		if r, ok := m[k]; !ok || string(r) == "null" {
			return fmt.Errorf("field %q is missing", k)
		}
	}
	return nil
}

type rule struct {
	ok    bool
	field string
}

// checkRules gives an error that names the first field whose rule fails.
func checkRules(rs ...rule) error {
	for _, r := range rs {
		if !r.ok {
			return fmt.Errorf("field %q is not valid", r.field)
		}
	}
	return nil
}

func isAddress(s string) bool {
	_, ok := wire.ParseAddress(s)
	return ok
}

func isRecipient(s string) bool { return s == wire.Broadcast || s == wire.Operator || isAddress(s) }

func isWaitTarget(s string) bool { return s == wire.Operator || isAddress(s) }

func validText(s string) bool {
	n := len([]rune(s))
	return n >= 1 && n <= wire.MaxText
}

func validState(s string) bool {
	return slices.Contains([]string{"working", "blocked", "done", "idle"}, s)
}

// --- Response and event shapes (packages/core/src/api.ts) ------------------------------------

// apiMessage is ApiMessage.
type apiMessage struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Text    string `json:"text"`
	ReplyTo string `json:"reply_to"`
	SentAt  string `json:"sent_at"`
	// FromRole is "orchestrator" for a message of an orchestrator.
	FromRole string `json:"from_role"`
}

func (m *apiMessage) UnmarshalJSON(b []byte) error {
	type plain apiMessage
	if err := strictDecode(b, (*plain)(m), "id", "from", "to", "text", "sent_at"); err != nil {
		return err
	}
	return checkRules(
		rule{wire.IsID(m.ID), "id"},
		rule{m.From == wire.Operator || isAddress(m.From), "from"},
		rule{isRecipient(m.To), "to"},
		rule{validText(m.Text), "text"},
		rule{m.ReplyTo == "" || wire.IsID(m.ReplyTo), "reply_to"},
		rule{wire.IsTime(m.SentAt), "sent_at"},
		rule{m.FromRole == "" || m.FromRole == wire.RoleOrchestrator, "from_role"},
	)
}

// messageIDs gives the id of each message.
func messageIDs(ms []apiMessage) []string {
	ids := make([]string, len(ms))
	for i, m := range ms {
		ids[i] = m.ID
	}
	return ids
}

// sendResponse is SendResponse.
type sendResponse struct {
	ID     string `json:"id"`
	To     string `json:"to"`
	Online bool   `json:"online"`
	SentAt string `json:"sent_at"`
	// State is the recipient's state for a peer: its live state, or away.
	State string `json:"state"`
}

func (r *sendResponse) UnmarshalJSON(b []byte) error {
	type plain sendResponse
	if err := strictDecode(b, (*plain)(r), "id", "to", "online", "sent_at"); err != nil {
		return err
	}
	return checkRules(
		rule{wire.IsID(r.ID), "id"},
		rule{isRecipient(r.To), "to"},
		rule{r.State == "" || validState(r.State) || r.State == "away", "state"},
		rule{wire.IsTime(r.SentAt), "sent_at"},
	)
}

// peer is Peer. An absent optional field is "".
type peer struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	Note      string `json:"note"`
	Online    bool   `json:"online"`
	WaitingOn string `json:"waiting_on"`
}

func (p *peer) UnmarshalJSON(b []byte) error {
	type plain peer
	if err := strictDecode(b, (*plain)(p), "name", "state", "online"); err != nil {
		return err
	}
	return checkRules(
		rule{isAddress(p.Name), "name"},
		rule{validState(p.State), "state"},
		rule{len([]rune(p.Note)) <= 500, "note"},
		rule{p.WaitingOn == "" || isWaitTarget(p.WaitingOn), "waiting_on"},
	)
}

// sessionView is SessionView.
type sessionView struct {
	Session string `json:"session"`
	Status  string `json:"status"`
	Me      string `json:"me"`
	Peers   []peer `json:"peers"`
}

func (v *sessionView) UnmarshalJSON(b []byte) error {
	type plain sessionView
	if err := strictDecode(b, (*plain)(v), "session", "status", "me", "peers"); err != nil {
		return err
	}
	return checkRules(
		rule{wire.IsToken(v.Session), "session"},
		rule{v.Status == "open" || v.Status == "closed", "status"},
		rule{isAddress(v.Me), "me"},
	)
}

// historyResponse is HistoryResponse.
type historyResponse struct {
	Messages []apiMessage `json:"messages"`
}

func (h *historyResponse) UnmarshalJSON(b []byte) error {
	type plain historyResponse
	return strictDecode(b, (*plain)(h), "messages")
}

// joinedEvent is JoinedEvent, the data of SSE event `joined`.
type joinedEvent struct {
	Me      string `json:"me"`
	Session string `json:"session"`
	Gate    string `json:"gate"`
}

func (j *joinedEvent) UnmarshalJSON(b []byte) error {
	type plain joinedEvent
	if err := strictDecode(b, (*plain)(j), "me", "session", "gate"); err != nil {
		return err
	}
	return checkRules(rule{isAddress(j.Me), "me"}, rule{wire.IsToken(j.Session), "session"}, rule{wire.IsGate(j.Gate), "gate"})
}

// noticeEvent is NoticeEvent, the data of SSE event `notice`.
type noticeEvent struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Peer string `json:"peer"`
	At   string `json:"at"`
}

func (n *noticeEvent) UnmarshalJSON(b []byte) error {
	type plain noticeEvent
	if err := strictDecode(b, (*plain)(n), "kind", "at"); err != nil {
		return err
	}
	return checkRules(
		rule{slices.Contains([]string{"kicked", "closed", "reopened", "redacted", "peer_left", "held", "paused", "released"}, n.Kind), "kind"},
		rule{n.ID == "" || wire.IsID(n.ID), "id"},
		rule{n.Peer == "" || isAddress(n.Peer), "peer"},
		rule{wire.IsTime(n.At), "at"},
	)
}

// errBody is ErrorBody.
type errBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func (e *errBody) UnmarshalJSON(b []byte) error {
	type plain errBody
	if err := strictDecode(b, (*plain)(e), "error", "message"); err != nil {
		return err
	}
	codes := []string{"unauthorized", "forbidden", "not_found", "conflict", "ambiguous", "too_large", "invalid", "rate_limited", "unavailable"}
	return checkRules(rule{slices.Contains(codes, e.Error), "error"})
}

// operatorSendResponse is the body of an operator's send. The tests read it as plain JSON.
type operatorSendResponse struct {
	ID string `json:"id"`
	To string `json:"to"`
}

// sessionList is the body of GET /v1/admin/sessions. The tests read it as plain JSON.
type sessionList struct {
	Sessions []wire.SessionInfo `json:"sessions"`
}

// sessionIDs gives the id of each session.
func sessionIDs(ss []wire.SessionInfo) []string {
	ids := make([]string, len(ss))
	for i, s := range ss {
		ids[i] = s.Session
	}
	return ids
}

// adminEvent is AdminEvent: one event of the operator's feed. Kind selects the fields.
type adminEvent struct {
	Kind string
	// event
	Seq     int64
	Subject string
	Payload string
	// session
	Session string
	// kick and presence
	Key string
	// session, kick and presence
	Revision int64
	// The record of a bucket entry, by kind. It is nil when the key is gone.
	SessionRecord  *wire.SessionRecord
	KickRecord     *wire.KickRecord
	PresenceRecord *wire.PresenceRecord
	// snapshot
	Bucket string
}

func (e *adminEvent) UnmarshalJSON(b []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	var kind string
	if err := json.Unmarshal(m["kind"], &kind); err != nil {
		return fmt.Errorf("field \"kind\": %w", err)
	}
	// Each kind has exactly these fields, all required.
	var fields []string
	switch kind {
	case "event":
		fields = []string{"kind", "seq", "subject", "payload"}
	case "session":
		fields = []string{"kind", "session", "revision", "record"}
	case "kick", "presence":
		fields = []string{"kind", "key", "revision", "record"}
	case "snapshot":
		fields = []string{"kind", "bucket"}
	default:
		return fmt.Errorf("unknown kind %q", kind)
	}
	got := slices.Sorted(maps.Keys(m))
	if !slices.Equal(got, slices.Sorted(slices.Values(fields))) {
		return fmt.Errorf("kind %s: fields %v, want %v", kind, got, fields)
	}
	var raw struct {
		Seq      int64  `json:"seq"`
		Subject  string `json:"subject"`
		Payload  string `json:"payload"`
		Session  string `json:"session"`
		Key      string `json:"key"`
		Revision int64  `json:"revision"`
		Bucket   string `json:"bucket"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*e = adminEvent{
		Kind: kind, Seq: raw.Seq, Subject: raw.Subject, Payload: raw.Payload, Session: raw.Session,
		Key: raw.Key, Revision: raw.Revision, Bucket: raw.Bucket,
	}
	var err error
	switch kind {
	case "event":
		return checkRules(rule{raw.Seq >= 1, "seq"})
	case "session":
		if err = checkRules(rule{wire.IsToken(raw.Session), "session"}, rule{raw.Revision >= 0, "revision"}); err == nil {
			e.SessionRecord, err = bucketRecord[wire.SessionRecord](m["record"], "status", "created_at")
		}
	case "kick":
		if err = checkRules(rule{raw.Revision >= 0, "revision"}); err == nil {
			e.KickRecord, err = bucketRecord[wire.KickRecord](m["record"], "at")
		}
	case "presence":
		if err = checkRules(rule{raw.Revision >= 0, "revision"}); err == nil {
			e.PresenceRecord, err = bucketRecord[wire.PresenceRecord](m["record"],
				"host", "cwd", "client", "state", "joined_at")
		}
	case "snapshot":
		return checkRules(rule{raw.Bucket == "sessions" || raw.Bucket == "presence", "bucket"})
	}
	return err
}

// bucketRecord decodes the record of a bucket entry strictly. A null record gives nil: the key
// is gone.
func bucketRecord[T any](raw json.RawMessage, required ...string) (*T, error) {
	if string(raw) == "null" {
		return nil, nil
	}
	v := new(T)
	if err := strictDecode(raw, v, required...); err != nil {
		return nil, fmt.Errorf("record: %w", err)
	}
	return v, nil
}

// openAPIDoc is the part of /openapi.json that the tests read.
type openAPIDoc struct {
	Paths map[string]json.RawMessage `json:"paths"`
}
