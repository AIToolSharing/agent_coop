package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/admin"
	"github.com/AIToolSharing/agent_coop/internal/model"
)

// A scripted SSE server: each connection gets the frames of its index, then the handler
// returns (which drops the connection) unless the frames end with "hold".
type sse struct {
	mu       sync.Mutex
	scripts  [][]string
	requests []*http.Request
	hold     chan struct{}
}

func (s *sse) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	n := len(s.requests)
	s.requests = append(s.requests, r.Clone(context.Background()))
	var frames []string
	if n < len(s.scripts) {
		frames = s.scripts[n]
	}
	s.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer op.secret" {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"unauthorized","message":"bad token"}`))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	f := w.(http.Flusher)
	f.Flush()
	for _, fr := range frames {
		if fr == "hold" {
			select {
			case <-s.hold:
			case <-r.Context().Done():
			}
			return
		}
		_, _ = io.WriteString(w, fr)
		f.Flush()
	}
}

func (s *sse) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func frame(kind, id, data string) string {
	out := "event: " + kind + "\n"
	if id != "" {
		out += "id: " + id + "\n"
	}
	return out + "data: " + data + "\n\n"
}

const (
	sessionOpen = `{"kind":"session","session":"build-42","revision":1,"record":{"status":"open","created_at":"2026-09-30T11:59:00.000Z"}}`
	sessionDocs = `{"kind":"session","session":"docs","revision":2,"record":{"status":"closed","created_at":"2026-09-30T11:50:00.000Z","closed_at":"2026-09-30T11:59:00.000Z"}}`
	presenceBob = `{"kind":"presence","key":"build-42.vps-2.bob","revision":3,"record":{"host":"vps-2","cwd":"/srv","client":{"name":"codex","version":"0.9"},"state":"idle","joined_at":"2026-09-30T12:00:00.000Z"}}`
	msg7        = `{"kind":"event","seq":7,"subject":"coop.build-42.msg.mac-1.alice","payload":"{\"to\":\"bob@vps-2\",\"text\":\"hi\",\"sent_at\":\"2026-09-30T12:00:20.000Z\"}"}`
	msg9        = `{"kind":"event","seq":9,"subject":"coop.build-42.ops","payload":"{\"kind\":\"msg\",\"to\":\"all\",\"text\":\"carry on\",\"sent_at\":\"2026-09-30T12:00:30.000Z\"}"}`
)

func run(t *testing.T, srv *httptest.Server, token string, idle time.Duration, until func(*model.Store) bool) (*model.Store, error) {
	t.Helper()
	c := &admin.Client{Base: srv.URL, Token: token, Idle: idle, Backoff: func(int) time.Duration { return 5 * time.Millisecond }}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := make(chan model.Update, 64)
	errc := make(chan error, 1)
	go func() { errc <- c.Feed(ctx, out) }()
	store := model.New()
	for {
		select {
		case u := <-out:
			store.Apply(u)
			if until(store) {
				cancel()
				return store, <-errc
			}
		case err := <-errc:
			return store, err
		case <-ctx.Done():
			t.Fatal("the condition never came true")
		}
	}
}

func TestFeedResumesAndReconcilesAfterADrop(t *testing.T) {
	s := &sse{hold: make(chan struct{}), scripts: [][]string{
		// Connection 1: two sessions, bob online, one message, then the connection drops.
		{frame("session", "", sessionOpen), frame("session", "", sessionDocs), frame("snapshot", "", `{"kind":"snapshot","bucket":"sessions"}`),
			frame("presence", "", presenceBob), frame("snapshot", "", `{"kind":"snapshot","bucket":"presence"}`), frame("event", "7", msg7)},
		// Connection 2: docs is gone, bob is gone, one more message; then hold.
		{frame("session", "", sessionOpen), frame("snapshot", "", `{"kind":"snapshot","bucket":"sessions"}`),
			frame("snapshot", "", `{"kind":"snapshot","bucket":"presence"}`), frame("event", "9", msg9), "hold"},
	}}
	srv := httptest.NewServer(s)
	defer srv.Close()
	defer close(s.hold)
	store, err := run(t, srv, "op.secret", time.Second, func(st *model.Store) bool {
		return st.View("build-42").Msgs["9"] != nil && st.Link == model.LinkLive
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.SessionIDs(); fmt.Sprint(got) != "[build-42]" {
		t.Fatalf("sessions after reconciliation: %v", got)
	}
	if _, ok := store.Presence["build-42.vps-2.bob"]; ok {
		t.Fatal("bob's presence was not reconciled away")
	}
	if store.View("build-42").Msgs["7"] == nil {
		t.Fatal("the message from the first connection is gone")
	}
	if s.count() != 2 {
		t.Fatalf("%d connections", s.count())
	}
	if got := s.requests[1].Header.Get("Last-Event-ID"); got != "7" {
		t.Fatalf("Last-Event-ID %q", got)
	}
	if s.requests[0].Header.Get("Last-Event-ID") != "" || s.requests[0].Header.Get("Accept") != "text/event-stream" {
		t.Fatalf("first request headers %v", s.requests[0].Header)
	}
}

// The trace has no event id, and each connection gets the whole trace again. The store must
// hold each item one time, and the trace must not move the point that the feed resumes from.
func TestFeedCarriesTheTraceAndDoesNotResumeFromIt(t *testing.T) {
	trace := `{"kind":"trace","boot":5,"key":"build-42.vps-2.bob","branch":"main","items":[{"n":1,"at":"2026-09-30T12:00:01.000Z","kind":"tool_start","id":"t1","tool":"Bash","text":"go test ./..."}]}`
	s := &sse{hold: make(chan struct{}), scripts: [][]string{
		{frame("session", "", sessionOpen), frame("event", "7", msg7), frame("trace", "", trace)},
		{frame("session", "", sessionOpen), frame("trace", "", trace), frame("event", "9", msg9), "hold"},
	}}
	srv := httptest.NewServer(s)
	defer srv.Close()
	defer close(s.hold)
	store, err := run(t, srv, "op.secret", time.Second, func(st *model.Store) bool {
		return st.View("build-42").Msgs["9"] != nil
	})
	if err != nil {
		t.Fatal(err)
	}
	bob := store.View("build-42").Agents["bob@vps-2"]
	if bob == nil || len(bob.Trace.Items) != 1 || bob.Trace.Items[0].Text != "go test ./..." || bob.Trace.Branch != "main" {
		t.Fatalf("bob %+v, want one trace item and the branch", bob)
	}
	if got := s.requests[1].Header.Get("Last-Event-ID"); got != "7" {
		t.Fatalf("Last-Event-ID %q, want 7", got)
	}
}

func TestFeedReportsReconnectingBetweenConnections(t *testing.T) {
	s := &sse{hold: make(chan struct{}), scripts: [][]string{{frame("session", "", sessionOpen)}, {"hold"}}}
	srv := httptest.NewServer(s)
	defer srv.Close()
	defer close(s.hold)
	c := &admin.Client{Base: srv.URL, Token: "op.secret", Idle: time.Second, Backoff: func(int) time.Duration { return 5 * time.Millisecond }}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := make(chan model.Update, 64)
	go func() { _ = c.Feed(ctx, out) }()
	var links []model.Link
	for len(links) < 3 {
		select {
		case u := <-out:
			if u.Link != "" {
				links = append(links, u.Link)
			}
		case <-ctx.Done():
			t.Fatalf("links so far %v", links)
		}
	}
	if fmt.Sprint(links) != "[live reconnecting live]" {
		t.Fatalf("links %v", links)
	}
}

func TestFeedEndsWhenTheTokenIsRefused(t *testing.T) {
	s := &sse{hold: make(chan struct{})}
	srv := httptest.NewServer(s)
	defer srv.Close()
	_, err := run(t, srv, "wrong", time.Second, func(*model.Store) bool { return false })
	if !errors.Is(err, admin.ErrRefused) || !strings.Contains(err.Error(), "bad token") {
		t.Fatalf("err %v", err)
	}
}

func TestFeedReconnectsAfterSilence(t *testing.T) {
	s := &sse{hold: make(chan struct{}), scripts: [][]string{{"hold"}, {frame("session", "", sessionOpen), "hold"}}}
	srv := httptest.NewServer(s)
	defer srv.Close()
	defer close(s.hold)
	store, err := run(t, srv, "op.secret", 50*time.Millisecond, func(st *model.Store) bool { return len(st.Sessions) == 1 })
	if err != nil {
		t.Fatal(err)
	}
	if s.count() < 2 {
		t.Fatalf("%d connections: the watchdog did not fire", s.count())
	}
	_ = store
}

// --- The operator's actions ---------------------------------------------------------------

type call struct {
	Method, Path, Body string
}

func actions(t *testing.T, status int, body string) (*admin.Client, *[]call) {
	t.Helper()
	var calls []call
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, call{r.Method, r.URL.Path, string(b)})
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer op.secret" {
			w.WriteHeader(401)
			return
		}
		if body != "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return &admin.Client{Base: srv.URL, Token: "op.secret"}, &calls
}

func TestActionsCallTheAdminRoutes(t *testing.T) {
	ctx := context.Background()
	c, calls := actions(t, 204, "")
	if err := c.CreateSession(ctx, "build-42"); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseSession(ctx, "build-42"); err != nil {
		t.Fatal(err)
	}
	if err := c.ReopenSession(ctx, "build-42"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteSession(ctx, "build-42"); err != nil {
		t.Fatal(err)
	}
	if err := c.Kick(ctx, "build-42", "bob@vps-2"); err != nil {
		t.Fatal(err)
	}
	if err := c.Unkick(ctx, "build-42", "bob@vps-2"); err != nil {
		t.Fatal(err)
	}
	if err := c.Forget(ctx, "build-42", "bob@vps-2"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetGate(ctx, "build-42", "bob@vps-2", "paused"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetGate(ctx, "build-42", "", "run"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetHold(ctx, "build-42", false); err != nil {
		t.Fatal(err)
	}
	if ok, err := c.Redact(ctx, "build-42", "14"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	want := []call{
		{"POST", "/v1/admin/sessions", `{"session":"build-42"}`},
		{"POST", "/v1/admin/sessions/build-42/close", ""},
		{"POST", "/v1/admin/sessions/build-42/reopen", ""},
		{"DELETE", "/v1/admin/sessions/build-42", ""},
		{"POST", "/v1/admin/sessions/build-42/kick", `{"target":"bob@vps-2"}`},
		{"POST", "/v1/admin/sessions/build-42/unkick", `{"target":"bob@vps-2"}`},
		{"POST", "/v1/admin/sessions/build-42/forget", `{"target":"bob@vps-2"}`},
		{"POST", "/v1/admin/sessions/build-42/gate", `{"gate":"paused","target":"bob@vps-2"}`},
		{"POST", "/v1/admin/sessions/build-42/gate", `{"gate":"run"}`},
		{"POST", "/v1/admin/sessions/build-42/hold", `{"hold":false}`},
		{"POST", "/v1/admin/sessions/build-42/redact", `{"id":"14"}`},
	}
	if fmt.Sprint(*calls) != fmt.Sprint(want) {
		t.Fatalf("\n got %v\nwant %v", *calls, want)
	}
}

func TestSendAndListAndErrors(t *testing.T) {
	ctx := context.Background()
	c, calls := actions(t, 200, `{"id":"21","to":"all","sent_at":"2026-09-30T12:05:00.000Z"}`)
	id, err := c.Send(ctx, "build-42", "all", "carry on", "")
	if err != nil || id != "21" {
		t.Fatal(id, err)
	}
	if _, err := c.Send(ctx, "build-42", "bob@vps-2", "re", "7"); err != nil {
		t.Fatal(err)
	}
	if (*calls)[1].Body != `{"to":"bob@vps-2","text":"re","reply_to":"7"}` || (*calls)[0].Body != `{"to":"all","text":"carry on"}` {
		t.Fatalf("bodies %v", *calls)
	}
	if _, err := c.Send(ctx, "build-42", "operator", "x", ""); err == nil {
		t.Fatal("the operator cannot write to the operator")
	}
	lc, _ := actions(t, 200, `{"sessions":[{"session":"docs","status":"closed","created_at":"2026-09-30T11:50:00.000Z","closed_at":"2026-09-30T11:59:00.000Z"}]}`)
	list, err := lc.ListSessions(ctx)
	if err != nil || len(list) != 1 || list[0].Session != "docs" || list[0].Status != "closed" {
		t.Fatal(list, err)
	}
	ec, _ := actions(t, 409, `{"error":"conflict","message":"the session is open"}`)
	err = ec.DeleteSession(ctx, "build-42")
	var apiErr *admin.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 409 || apiErr.Message != "the session is open" {
		t.Fatalf("err %v", err)
	}
	nc, _ := actions(t, 404, `{"error":"not_found","message":"no such message"}`)
	if ok, err := nc.Redact(ctx, "build-42", "99"); err != nil || ok {
		t.Fatal(ok, err)
	}
	bc := &admin.Client{Base: ec.Base, Token: "wrong"}
	if err := bc.CloseSession(ctx, "x"); !errors.As(err, &apiErr) || apiErr.Status != 401 {
		t.Fatalf("err %v", err)
	}
	_ = json.Valid
}
