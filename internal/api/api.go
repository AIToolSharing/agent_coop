// Package api is the HTTP layer of the hub: the routes of packages/hub/src/app.ts over
// internal/hub. The contract is the embedded OpenAPI document, served at /openapi.json; the
// handlers check every request against the same rules by hand (validate.go).
package api

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/hub"
	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// Document is the OpenAPI document of the API, as the TypeScript hub generated it from its
// schemas. It is the frozen contract that the clients and the contract test use.
//
//go:embed openapi.json
var Document []byte

// MaxBody is the largest request body: 32 KiB.
const MaxBody = 32 * 1024

// writeTimeout bounds one write to a stream. A client that does not read for this long is
// gone; the failed write ends its connection.
const writeTimeout = 10 * time.Second

// Server serves the API.
type Server struct {
	hub *hub.Hub
	log func(format string, args ...any)
	// routes by path pattern, in document order; each path has its methods.
	routes []*route
}

type route struct {
	pattern  []string // path segments; "{sid}" is the session
	path     string
	handlers map[string]http.HandlerFunc // by method
	allow    string                      // the Allow header
}

// New makes the handler. log gets internal errors; nil discards them.
func New(h *hub.Hub, log func(format string, args ...any)) *Server {
	if log == nil {
		log = func(string, ...any) {}
	}
	s := &Server{hub: h, log: log}
	s.add("GET", "/v1/sessions/{sid}/stream", s.machine(s.stream))
	s.add("POST", "/v1/sessions/{sid}/messages", s.machine(s.send))
	s.add("GET", "/v1/sessions/{sid}/messages", s.machine(s.history))
	s.add("POST", "/v1/sessions/{sid}/activity", s.machine(s.activity))
	s.add("POST", "/v1/sessions/{sid}/gate", s.machine(s.gate))
	s.add("GET", "/v1/sessions/{sid}", s.machine(s.view))
	s.add("GET", "/v1/admin/sessions", s.operator(s.adminSessions))
	s.add("POST", "/v1/admin/sessions", s.operator(s.adminCreate))
	s.add("POST", "/v1/admin/sessions/{sid}/close", s.operator(s.adminClose))
	s.add("POST", "/v1/admin/sessions/{sid}/reopen", s.operator(s.adminReopen))
	s.add("DELETE", "/v1/admin/sessions/{sid}", s.operator(s.adminDelete))
	s.add("POST", "/v1/admin/sessions/{sid}/kick", s.operator(s.adminKick))
	s.add("POST", "/v1/admin/sessions/{sid}/unkick", s.operator(s.adminUnkick))
	s.add("POST", "/v1/admin/sessions/{sid}/forget", s.operator(s.adminForget))
	s.add("POST", "/v1/admin/sessions/{sid}/gate", s.operator(s.adminGate))
	s.add("POST", "/v1/admin/sessions/{sid}/hold", s.operator(s.adminHold))
	s.add("POST", "/v1/admin/sessions/{sid}/redact", s.operator(s.adminRedact))
	s.add("POST", "/v1/admin/sessions/{sid}/messages", s.operator(s.adminSend))
	s.add("GET", "/v1/admin/stream", s.operator(s.adminStream))
	return s
}

func (s *Server) add(method, path string, h http.HandlerFunc) {
	var r *route
	for _, x := range s.routes {
		if x.path == path {
			r = x
		}
	}
	if r == nil {
		r = &route{pattern: strings.Split(strings.TrimPrefix(path, "/"), "/"), path: path, handlers: map[string]http.HandlerFunc{}}
		s.routes = append(s.routes, r)
	}
	r.handlers[method] = h
	methods := make([]string, 0, len(r.handlers))
	for m := range r.handlers {
		methods = append(methods, m)
	}
	sort.Strings(methods)
	r.allow = strings.Join(methods, ", ")
}

// Routes gives every method and path pair the server handles, as "METHOD /path".
func (s *Server) Routes() []string {
	var out []string
	for _, r := range s.routes {
		for m := range r.handlers {
			out = append(out, m+" "+r.path)
		}
	}
	sort.Strings(out)
	return out
}

// ServeHTTP routes by hand: a known path with another method is 405 with Allow, and no path is
// ever cleaned or redirected.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/openapi.json" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(Document)
		return
	}
	segs := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
	for _, rt := range s.routes {
		sid, ok := match(rt.pattern, segs)
		if !ok {
			continue
		}
		h, ok := rt.handlers[r.Method]
		if !ok {
			w.Header().Set("Allow", rt.allow)
			writeError(w, 405, "invalid", "method not allowed")
			return
		}
		if sid != "" && !wire.IsToken(sid) {
			// The auth comes first, as in the TypeScript hub; a bad session name is 422 after it.
			if _, err := s.auth(r, strings.HasPrefix(rt.path, "/v1/admin/")); err != nil {
				writeHubError(w, err)
				return
			}
			writeError(w, 422, "invalid", "sid: not a valid session name")
			return
		}
		r.SetPathValue("sid", sid)
		h(w, r)
		return
	}
	writeError(w, 404, "not_found", "no such route")
}

// match gives the session segment of a path that fits the pattern.
func match(pattern, segs []string) (sid string, ok bool) {
	if len(pattern) != len(segs) {
		return "", false
	}
	for i, p := range pattern {
		if p == "{sid}" {
			v, err := url.PathUnescape(segs[i])
			if err != nil || v == "" {
				return "", false
			}
			sid = v
			continue
		}
		if p != segs[i] {
			return "", false
		}
	}
	return sid, true
}

// --- Auth and plumbing -----------------------------------------------------------------------

func (s *Server) auth(r *http.Request, admin bool) (hub.Owner, error) {
	owner, err := s.hub.Auth(r.Header.Get("Authorization"))
	if err != nil {
		return owner, err
	}
	if admin && owner.Role != "operator" {
		return owner, &hub.Error{Code: "forbidden", Message: "this needs an operator token"}
	}
	if !admin && owner.Role != "machine" {
		return owner, &hub.Error{Code: "forbidden", Message: "an operator token cannot act as an agent"}
	}
	return owner, nil
}

type handler func(w http.ResponseWriter, r *http.Request, owner hub.Owner, sid string)

// machine wraps an agent route: a body limit, then a machine token.
func (s *Server) machine(h handler) http.HandlerFunc { return s.guard(false, h) }

// operator wraps an admin route: a body limit, then an operator token.
func (s *Server) operator(h handler) http.HandlerFunc { return s.guard(true, h) }

func (s *Server) guard(admin bool, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > MaxBody {
			writeError(w, 413, "too_large", "body too large")
			return
		}
		owner, err := s.auth(r, admin)
		if err != nil {
			writeHubError(w, err)
			return
		}
		h(w, r, owner, r.PathValue("sid"))
	}
}

// body reads a request body of at most MaxBody bytes.
func body(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, 413, "too_large", "body too large")
		} else {
			writeError(w, 422, "invalid", "unreadable body")
		}
		return nil, false
	}
	return b, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		writeError(w, 503, "unavailable", "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}

// writeHubError answers with a hub error, a validation error, or 503.
func writeHubError(w http.ResponseWriter, err error) {
	var he *hub.Error
	var inv invalid
	switch {
	case errors.As(err, &he):
		writeError(w, he.Status(), he.Code, he.Message)
	case errors.As(err, &inv):
		writeError(w, 422, "invalid", inv.Error())
	default:
		writeError(w, 503, "unavailable", "internal error")
	}
}

// sink writes server-sent events to one response.
type sink struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func startStream(w http.ResponseWriter) *sink {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	s := &sink{w: w, rc: http.NewResponseController(w)}
	_ = s.rc.Flush()
	return s
}

func (s *sink) flush() error {
	// A deadline the writer cannot set (a recorder in a test) does not stop the stream.
	if err := s.rc.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return s.rc.Flush()
}

func (s *sink) Write(event string, data any, id string) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "event: %s\n", event)
	if id != "" {
		fmt.Fprintf(&sb, "id: %s\n", id)
	}
	fmt.Fprintf(&sb, "data: %s\n\n", b)
	if _, err := io.WriteString(s.w, sb.String()); err != nil {
		return err
	}
	return s.flush()
}

func (s *sink) Ping() error {
	if _, err := io.WriteString(s.w, ": ping\n\n"); err != nil {
		return err
	}
	return s.flush()
}

// --- Agent routes ----------------------------------------------------------------------------

func (s *Server) stream(w http.ResponseWriter, r *http.Request, owner hub.Owner, sid string) {
	q, err := parseStreamQuery(r.URL.Query())
	if err != nil {
		writeHubError(w, err)
		return
	}
	joined, err := s.hub.Join(owner.Name, sid, q, r.Header.Get("Last-Event-ID"))
	if err != nil {
		writeHubError(w, err)
		return
	}
	s.hub.Run(r.Context(), joined, startStream(w))
}

func (s *Server) send(w http.ResponseWriter, r *http.Request, owner hub.Owner, sid string) {
	b, ok := body(w, r)
	if !ok {
		return
	}
	req, err := parseSend(b)
	if err != nil {
		writeHubError(w, err)
		return
	}
	res, err := s.hub.Send(owner.Name, sid, req)
	if err != nil {
		writeHubError(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) activity(w http.ResponseWriter, r *http.Request, owner hub.Owner, sid string) {
	b, ok := body(w, r)
	if !ok {
		return
	}
	req, err := parseActivity(b)
	if err != nil {
		writeHubError(w, err)
		return
	}
	if err := s.hub.Activity(owner.Name, sid, req); err != nil {
		writeHubError(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) gate(w http.ResponseWriter, r *http.Request, owner hub.Owner, sid string) {
	b, ok := body(w, r)
	if !ok {
		return
	}
	agent, err := parseAgentBody(b)
	if err != nil {
		writeHubError(w, err)
		return
	}
	gate, err := s.hub.Gate(owner.Name, sid, agent)
	if err != nil {
		writeHubError(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"gate": gate})
}

func (s *Server) view(w http.ResponseWriter, r *http.Request, owner hub.Owner, sid string) {
	agent, err := parseAgentQuery(r.URL.Query())
	if err != nil {
		writeHubError(w, err)
		return
	}
	v, err := s.hub.View(owner.Name, sid, agent)
	if err != nil {
		writeHubError(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) history(w http.ResponseWriter, r *http.Request, owner hub.Owner, sid string) {
	q, err := parseHistoryQuery(r.URL.Query())
	if err != nil {
		writeHubError(w, err)
		return
	}
	msgs, err := s.hub.History(owner.Name, sid, q)
	if err != nil {
		writeHubError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"messages": msgs})
}

// --- Admin routes ----------------------------------------------------------------------------

func (s *Server) adminSessions(w http.ResponseWriter, r *http.Request, _ hub.Owner, _ string) {
	list, err := s.hub.ListSessions()
	if err != nil {
		writeHubError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"sessions": list})
}

func (s *Server) adminCreate(w http.ResponseWriter, r *http.Request, _ hub.Owner, _ string) {
	b, ok := body(w, r)
	if !ok {
		return
	}
	sid, title, err := parseCreateSession(b)
	if err != nil {
		writeHubError(w, err)
		return
	}
	info, err := s.hub.CreateSession(sid, title)
	if err != nil {
		writeHubError(w, err)
		return
	}
	writeJSON(w, 200, info)
}

func (s *Server) done(w http.ResponseWriter, err error) {
	if err != nil {
		writeHubError(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) adminClose(w http.ResponseWriter, _ *http.Request, _ hub.Owner, sid string) {
	s.done(w, s.hub.CloseSession(sid))
}

func (s *Server) adminReopen(w http.ResponseWriter, _ *http.Request, _ hub.Owner, sid string) {
	s.done(w, s.hub.ReopenSession(sid))
}

func (s *Server) adminDelete(w http.ResponseWriter, _ *http.Request, _ hub.Owner, sid string) {
	s.done(w, s.hub.DeleteSession(sid))
}

func (s *Server) adminKick(w http.ResponseWriter, r *http.Request, _ hub.Owner, sid string) {
	b, ok := body(w, r)
	if !ok {
		return
	}
	target, err := parseTarget(b)
	if err != nil {
		writeHubError(w, err)
		return
	}
	s.done(w, s.hub.Kick(sid, target))
}

func (s *Server) adminUnkick(w http.ResponseWriter, r *http.Request, _ hub.Owner, sid string) {
	b, ok := body(w, r)
	if !ok {
		return
	}
	target, err := parseTarget(b)
	if err != nil {
		writeHubError(w, err)
		return
	}
	s.done(w, s.hub.Unkick(sid, target))
}

func (s *Server) adminForget(w http.ResponseWriter, r *http.Request, _ hub.Owner, sid string) {
	b, ok := body(w, r)
	if !ok {
		return
	}
	target, err := parseTarget(b)
	if err != nil {
		writeHubError(w, err)
		return
	}
	s.done(w, s.hub.Forget(sid, target))
}

func (s *Server) adminGate(w http.ResponseWriter, r *http.Request, _ hub.Owner, sid string) {
	b, ok := body(w, r)
	if !ok {
		return
	}
	target, gate, err := parseGate(b)
	if err != nil {
		writeHubError(w, err)
		return
	}
	s.done(w, s.hub.SetGate(sid, target, gate))
}

func (s *Server) adminHold(w http.ResponseWriter, r *http.Request, _ hub.Owner, sid string) {
	b, ok := body(w, r)
	if !ok {
		return
	}
	hold, err := parseHold(b)
	if err != nil {
		writeHubError(w, err)
		return
	}
	s.done(w, s.hub.SetHold(sid, hold))
}

func (s *Server) adminRedact(w http.ResponseWriter, r *http.Request, _ hub.Owner, sid string) {
	b, ok := body(w, r)
	if !ok {
		return
	}
	id, err := parseRedact(b)
	if err != nil {
		writeHubError(w, err)
		return
	}
	s.done(w, s.hub.Redact(sid, id))
}

func (s *Server) adminSend(w http.ResponseWriter, r *http.Request, _ hub.Owner, sid string) {
	b, ok := body(w, r)
	if !ok {
		return
	}
	req, err := parseOperatorSend(b)
	if err != nil {
		writeHubError(w, err)
		return
	}
	res, err := s.hub.OperatorSend(sid, req)
	if err != nil {
		writeHubError(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) adminStream(w http.ResponseWriter, r *http.Request, owner hub.Owner, _ string) {
	from := int64(1)
	if last := r.Header.Get("Last-Event-ID"); wire.IsID(last) {
		n, _ := strconv.ParseInt(last, 10, 64)
		from = n + 1
	}
	s.hub.AdminFeed(r.Context(), owner.Name, startStream(w), from)
}
