package shim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// forbiddenRE holds words that must never reach an agent: they would tell how the service works.
var forbiddenRE = regexp.MustCompile(`(?i)\b(hub|nats|jetstream|streams?|subjects?|kv|buckets?|consumers?|tokens?|https?|urls?|sse)\b|coop_`)

// recorder collects what the stream loop reports.
type recorder struct {
	mu      sync.Mutex
	states  []string
	msgs    []message
	notices []notice
	sleeps  []time.Duration
}

func (r *recorder) handlers() streamHandlers {
	return streamHandlers{
		state: func(l link) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.states = append(r.states, l.kind)
		},
		message: func(m message) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.msgs = append(r.msgs, m)
		},
		notice: func(n notice) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.notices = append(r.notices, n)
		},
	}
}

// sleep records each wait and sleeps 10 ms at most.
func (r *recorder) sleep(ctx context.Context, d time.Duration) bool {
	r.mu.Lock()
	r.sleeps = append(r.sleeps, d)
	r.mu.Unlock()
	t := time.NewTimer(min(d, 10*time.Millisecond))
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (r *recorder) snapshot() ([]string, []message, []notice, []time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.states), slices.Clone(r.msgs), slices.Clone(r.notices), slices.Clone(r.sleeps)
}

func testJoin(agent string) joinInfo {
	return joinInfo{agent: agent, instance: "6f1c2c4e-8d43-4f0e-9b7a-0b5f0f1d2c3a", host: "h", cwd: "/", clientName: "c", clientVersion: "1"}
}

func testClient(t *testing.T, base string, rec *recorder) *hubClient {
	return testClientAs(t, base, rec, "a")
}

func testClientAs(t *testing.T, base string, rec *recorder, agent string) *hubClient {
	t.Helper()
	c := newHubClient(base, "m.secret", "s", testJoin(agent))
	c.idle = 2 * time.Second
	c.sleep = rec.sleep
	return c
}

// runUntil runs the stream loop until cond holds or two seconds pass, then stops it.
func runUntil(t *testing.T, c *hubClient, rec *recorder, cond func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.run(ctx, rec.handlers())
		close(done)
	}()
	end := time.Now().Add(2 * time.Second)
	for !cond() && time.Now().Before(end) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the stream loop did not stop")
	}
	if !cond() {
		states, msgs, notices, sleeps := rec.snapshot()
		t.Fatalf("condition never held: states %v msgs %v notices %v sleeps %v", states, msgs, notices, sleeps)
	}
}

// runToEnd runs the stream loop and fails the test when it does not end by itself.
func runToEnd(t *testing.T, c *hubClient, rec *recorder) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c.run(ctx, rec.handlers())
	if ctx.Err() != nil {
		states, _, _, _ := rec.snapshot()
		t.Fatalf("the stream loop did not end by itself: states %v", states)
	}
}

func writeEvent(w http.ResponseWriter, event, id string, data any) {
	b, _ := json.Marshal(data)
	if id != "" {
		fmt.Fprintf(w, "id: %s\n", id)
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	w.(http.Flusher).Flush()
}

func openStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": msg})
}

var joinedA = map[string]string{"me": "a@m", "session": "s"}

func TestSilentStreamIsDroppedAfterIdleAndOpenedAgain(t *testing.T) {
	var mu sync.Mutex
	opened := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		opened++
		mu.Unlock()
		openStream(w)
		writeEvent(w, "joined", "", joinedA)
		// Then nothing: no ping, no close.
		<-r.Context().Done()
	}))
	defer srv.Close()
	rec := &recorder{}
	c := testClient(t, srv.URL, rec)
	c.idle = 150 * time.Millisecond
	runUntil(t, c, rec, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return opened >= 3
	})
	states, _, _, _ := rec.snapshot()
	if !slices.Equal(states[:3], []string{linkJoined, linkUnreachable, linkJoined}) {
		t.Fatalf("states %v", states)
	}
}

// A hub of a version before the gate refuses a join with a query parameter that it does not
// know (422). The client then joins without the optional parameters, at once, so that a new
// agent still works with an old hub.
func TestAJoinThatAnOlderServiceRefusesGoesAgainWithoutTheOptionalParameters(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		mu.Unlock()
		q := r.URL.Query()
		if q.Has("gated") || q.Has("herdr_pane") {
			writeError(w, 422, "invalid", "unknown query parameter gated")
			return
		}
		openStream(w)
		writeEvent(w, "joined", "", joinedA)
		<-r.Context().Done()
	}))
	defer srv.Close()
	rec := &recorder{}
	c := testClient(t, srv.URL, rec)
	c.join.gated, c.join.herdrPane = true, "w1:p3"
	runUntil(t, c, rec, func() bool {
		states, _, _, _ := rec.snapshot()
		return slices.Contains(states, linkJoined)
	})
	mu.Lock()
	defer mu.Unlock()
	if len(queries) != 2 || !strings.Contains(queries[0], "gated=1") || !strings.Contains(queries[0], "herdr_pane=") ||
		strings.Contains(queries[1], "gated") || strings.Contains(queries[1], "herdr_pane") {
		t.Fatalf("queries %q", queries)
	}
	// No wait between the two tries, and the agent never looked unreachable.
	if states, _, _, _ := rec.snapshot(); slices.Contains(states, linkUnreachable) {
		t.Fatalf("states %v", states)
	}
}

func TestPingsKeepTheStreamOpen(t *testing.T) {
	var mu sync.Mutex
	opened := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		opened++
		mu.Unlock()
		openStream(w)
		writeEvent(w, "joined", "", joinedA)
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
				fmt.Fprint(w, ": ping\n\n")
				w.(http.Flusher).Flush()
			}
		}
	}))
	defer srv.Close()
	rec := &recorder{}
	c := testClient(t, srv.URL, rec)
	c.idle = 100 * time.Millisecond
	start := time.Now()
	runUntil(t, c, rec, func() bool { return time.Since(start) > 400*time.Millisecond })
	mu.Lock()
	defer mu.Unlock()
	if opened != 1 {
		t.Fatalf("a stream with pings was opened %d times", opened)
	}
}

func TestJoinSendsTheQueryAndTheCredential(t *testing.T) {
	got := make(chan *http.Request, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case got <- r.Clone(context.Background()):
		default:
		}
		openStream(w)
		writeEvent(w, "joined", "", joinedA)
		<-r.Context().Done()
	}))
	defer srv.Close()
	rec := &recorder{}
	c := testClient(t, srv.URL, rec)
	runUntil(t, c, rec, func() bool { s, _, _, _ := rec.snapshot(); return len(s) > 0 })
	r := <-got
	if r.URL.Path != "/v1/sessions/s/stream" {
		t.Fatalf("path %s", r.URL.Path)
	}
	want := map[string]string{"agent": "a", "instance": testJoin("a").instance, "host": "h", "cwd": "/", "client_name": "c", "client_version": "1"}
	for k, v := range want {
		if r.URL.Query().Get(k) != v {
			t.Errorf("query %s = %q, want %q", k, r.URL.Query().Get(k), v)
		}
	}
	if r.Header.Get("Authorization") != "Bearer m.secret" || r.Header.Get("Accept") != "text/event-stream" {
		t.Fatalf("headers %v", r.Header)
	}
	if r.Header.Get("Last-Event-ID") != "" {
		t.Fatal("a first join must not resume")
	}
	if states, _, _, _ := rec.snapshot(); states[0] != linkJoined {
		t.Fatalf("states %v", states)
	}
}

// A name that another session on the machine holds is not taken over, and the client takes no
// numbered name. It says why it is blocked and tries again until the name is free.
func TestATakenNameBlocksTheClientUntilTheNameIsFree(t *testing.T) {
	var mu sync.Mutex
	var names []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		names = append(names, r.URL.Query().Get("agent"))
		n := len(names)
		mu.Unlock()
		if n <= 2 {
			writeError(w, 409, "conflict", "name dev@m is taken")
			return
		}
		openStream(w)
		writeEvent(w, "joined", "", map[string]string{"me": "dev@m", "session": "s"})
		<-r.Context().Done()
	}))
	defer srv.Close()
	rec := &recorder{}
	c := testClientAs(t, srv.URL, rec, "dev")
	runUntil(t, c, rec, func() bool { s, _, _, _ := rec.snapshot(); return slices.Contains(s, linkJoined) })
	mu.Lock()
	defer mu.Unlock()
	states, _, _, sleeps := rec.snapshot()
	if !slices.Equal(names, []string{"dev", "dev", "dev"}) || !slices.Equal(states, []string{linkTaken, linkTaken, linkJoined}) {
		t.Fatalf("names %v, states %v", names, states)
	}
	if !slices.Equal(sleeps, []time.Duration{retryTaken, retryTaken}) {
		t.Fatalf("waits %v, want two of %v", sleeps, retryTaken)
	}
}

// A stream that drops is opened again from the last message or notice; after a resume a 409
// is not a taken name, so the name stays.
func TestResumeSendsTheLastEventID(t *testing.T) {
	var mu sync.Mutex
	var lastIDs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lastIDs = append(lastIDs, r.Header.Get("Last-Event-ID"))
		n := len(lastIDs)
		mu.Unlock()
		switch n {
		case 1:
			openStream(w)
			writeEvent(w, "joined", "", joinedA)
			writeEvent(w, "message", "42", message{ID: "42", From: "b@m", To: "a@m", Text: "hi", SentAt: "2026-09-30T00:00:00.000Z"})
		case 2:
			openStream(w)
			writeEvent(w, "joined", "", joinedA)
			writeEvent(w, "notice", "43", notice{Kind: noticeRedacted, ID: "42", At: "2026-09-30T00:00:00.000Z"})
		default:
			writeError(w, 409, "conflict", "taken")
		}
	}))
	defer srv.Close()
	rec := &recorder{}
	c := testClient(t, srv.URL, rec)
	runUntil(t, c, rec, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(lastIDs) >= 4
	})
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(lastIDs[:4], []string{"", "42", "43", "43"}) {
		t.Fatalf("Last-Event-ID %v", lastIDs)
	}
	if c.agent() != "a" {
		t.Fatalf("a 409 after a resume renamed the agent to %q", c.agent())
	}
	_, msgs, notices, _ := rec.snapshot()
	if len(msgs) != 1 || msgs[0].ID != "42" || len(notices) != 1 || notices[0].Kind != noticeRedacted {
		t.Fatalf("msgs %v notices %v", msgs, notices)
	}
}

func TestRefusedAndRemovedEndTheLoop(t *testing.T) {
	for _, tc := range []struct {
		status    int
		code, msg string
		want      string
	}{
		{401, "unauthorized", "missing or invalid token", linkRefused},
		{403, "forbidden", "removed from session", linkRemoved},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, tc.status, tc.code, tc.msg)
		}))
		rec := &recorder{}
		runToEnd(t, testClient(t, srv.URL, rec), rec)
		srv.Close()
		if states, _, _, _ := rec.snapshot(); !slices.Equal(states, []string{tc.want}) {
			t.Errorf("%d %s: states %v", tc.status, tc.msg, states)
		}
	}
}

func TestClosedAndNoSessionRetryEvery30Seconds(t *testing.T) {
	for _, tc := range []struct {
		status    int
		code, msg string
		want      string
	}{
		{403, "forbidden", "session closed", linkClosed},
		{404, "not_found", "no session s", linkNoSession},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, tc.status, tc.code, tc.msg)
		}))
		rec := &recorder{}
		runUntil(t, testClient(t, srv.URL, rec), rec, func() bool { _, _, _, s := rec.snapshot(); return len(s) >= 2 })
		srv.Close()
		states, _, _, sleeps := rec.snapshot()
		if states[0] != tc.want || states[1] != tc.want || sleeps[0] != 30*time.Second || sleeps[1] != 30*time.Second {
			t.Errorf("%d: states %v sleeps %v", tc.status, states, sleeps)
		}
	}
}

func TestOtherFailuresBackOffUpTo30Seconds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, 503, "unavailable", "internal error")
	}))
	defer srv.Close()
	rec := &recorder{}
	runUntil(t, testClient(t, srv.URL, rec), rec, func() bool { _, _, _, s := rec.snapshot(); return len(s) >= 7 })
	_, _, _, sleeps := rec.snapshot()
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30}
	for i := range want {
		want[i] *= time.Second
	}
	if !slices.Equal(sleeps[:7], want) {
		t.Fatalf("sleeps %v", sleeps)
	}
}

func TestUnreachableServiceBacksOff(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	rec := &recorder{}
	runUntil(t, testClient(t, base, rec), rec, func() bool { _, _, _, s := rec.snapshot(); return len(s) >= 2 })
	states, _, _, sleeps := rec.snapshot()
	if states[0] != linkUnreachable || sleeps[0] != time.Second || sleeps[1] != 2*time.Second {
		t.Fatalf("states %v sleeps %v", states, sleeps)
	}
}

func TestKickedNoticeRemovesTheAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openStream(w)
		writeEvent(w, "joined", "", joinedA)
		writeEvent(w, "notice", "9", notice{Kind: noticeKicked, At: "2026-09-30T00:00:00.000Z"})
	}))
	defer srv.Close()
	rec := &recorder{}
	runToEnd(t, testClient(t, srv.URL, rec), rec)
	states, _, notices, _ := rec.snapshot()
	if !slices.Equal(states, []string{linkJoined, linkRemoved}) || len(notices) != 1 || notices[0].Kind != noticeKicked {
		t.Fatalf("states %v notices %v", states, notices)
	}
}

func TestBadEventsAreSkipped(t *testing.T) {
	good := message{ID: "7", From: "operator", To: "all", Text: "ok", SentAt: "2026-09-30T00:00:00.000Z"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openStream(w)
		writeEvent(w, "joined", "", map[string]string{"me": "not an address", "session": "s"})
		fmt.Fprint(w, "event: message\ndata: {not json\n\n")
		writeEvent(w, "message", "", map[string]any{"id": "1", "from": "b@m", "to": "all", "text": "x", "sent_at": "2026-09-30T00:00:00.000Z", "extra": 1})
		writeEvent(w, "message", "", message{ID: "0", From: "b@m", To: "all", Text: "x", SentAt: "2026-09-30T00:00:00.000Z"})
		writeEvent(w, "message", "", message{ID: "2", From: "b@m", To: "all", Text: "", SentAt: "2026-09-30T00:00:00.000Z"})
		writeEvent(w, "message", "", message{ID: "3", From: "all@m", To: "all", Text: "x", SentAt: "2026-09-30T00:00:00.000Z"})
		writeEvent(w, "notice", "", notice{Kind: "unknown", At: "2026-09-30T00:00:00.000Z"})
		writeEvent(w, "notice", "", notice{Kind: noticeClosed, At: "yesterday"})
		writeEvent(w, "other", "", good)
		// Two data lines make one value.
		fmt.Fprint(w, "event: message\r\ndata: {\"id\":\"7\",\"from\":\"operator\",\"to\":\"all\",\r\ndata: \"text\":\"ok\",\"sent_at\":\"2026-09-30T00:00:00.000Z\"}\r\n\r\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	rec := &recorder{}
	runUntil(t, testClient(t, srv.URL, rec), rec, func() bool { _, m, _, _ := rec.snapshot(); return len(m) > 0 })
	states, msgs, notices, _ := rec.snapshot()
	if slices.Contains(states, linkJoined) || len(notices) != 0 || len(msgs) != 1 || msgs[0] != good {
		t.Fatalf("states %v msgs %v notices %v", states, msgs, notices)
	}
}

// --- REST calls ------------------------------------------------------------------------------

type hubCall struct {
	method, path, query, auth string
	body                      map[string]any
}

// restHub answers every call with status and body, and records the calls.
func restHub(t *testing.T, status int, body string) (*httptest.Server, func() []hubCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []hubCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := hubCall{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization")}
		b, _ := io.ReadAll(r.Body)
		if len(b) > 0 {
			_ = json.Unmarshal(b, &c.body)
		}
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []hubCall {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(calls)
	}
}

func TestCallsFollowTheContract(t *testing.T) {
	ctx := context.Background()
	srv, calls := restHub(t, 200, `{"id":"5","to":"b@m","online":true,"sent_at":"2026-09-30T00:00:00.000Z"}`)
	c := testClient(t, srv.URL, &recorder{})
	r, err := c.send(ctx, "b", "hello", "4")
	if err != nil || r != (sendResponse{ID: "5", To: "b@m", Online: true, SentAt: "2026-09-30T00:00:00.000Z"}) {
		t.Fatalf("send %+v %v", r, err)
	}
	got := calls()[0]
	if got.method != "POST" || got.path != "/v1/sessions/s/messages" || got.auth != "Bearer m.secret" {
		t.Fatalf("send call %+v", got)
	}
	if want := map[string]any{"agent": "a", "to": "b", "text": "hello", "reply_to": "4"}; fmt.Sprint(got.body) != fmt.Sprint(want) {
		t.Fatalf("send body %v", got.body)
	}

	srv, calls = restHub(t, 204, "")
	c = testClient(t, srv.URL, &recorder{})
	if err := c.activity(ctx, activity{Kind: "wait_end", Result: "timeout"}); err != nil {
		t.Fatal(err)
	}
	got = calls()[0]
	if want := map[string]any{"kind": "wait_end", "agent": "a", "result": "timeout"}; got.path != "/v1/sessions/s/activity" || fmt.Sprint(got.body) != fmt.Sprint(want) {
		t.Fatalf("activity call %+v", got)
	}

	view := `{"session":"s","status":"open","me":"a@m","peers":[{"name":"b@m","state":"blocked","note":"n","online":false,"waiting_on":"operator"}]}`
	srv, calls = restHub(t, 200, view)
	c = testClient(t, srv.URL, &recorder{})
	v, err := c.view(ctx)
	if err != nil || v.Me != "a@m" || len(v.Peers) != 1 || v.Peers[0].WaitingOn != "operator" {
		t.Fatalf("view %+v %v", v, err)
	}
	if got = calls()[0]; got.method != "GET" || got.path != "/v1/sessions/s" || got.query != "agent=a" {
		t.Fatalf("view call %+v", got)
	}

	srv, calls = restHub(t, 200, `{"messages":[{"id":"1","from":"b@m","to":"a@m","text":"x","sent_at":"2026-09-30T00:00:00.000Z"}]}`)
	c = testClient(t, srv.URL, &recorder{})
	h, err := c.history(ctx, "b", 50)
	if err != nil || len(h.Messages) != 1 {
		t.Fatalf("history %+v %v", h, err)
	}
	if got = calls()[0]; got.path != "/v1/sessions/s/messages" || got.query != "agent=a&limit=50&with=b" {
		t.Fatalf("history call %+v", got)
	}
	if _, err := c.history(ctx, "", 3); err != nil || calls()[1].query != "agent=a&limit=3" {
		t.Fatalf("history without a peer: %v %+v", err, calls()[1])
	}
}

func TestCallsGiveAgentTexts(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		status int
		body   string
		want   string
	}{
		{404, `{"error":"not_found","message":"no peer zed"}`, "no peer zed"},
		{403, `{"error":"forbidden","message":"session closed"}`, "session closed"},
		{401, `{"error":"unauthorized","message":"missing or invalid token"}`, "not in a session: this machine is not allowed to join"},
		{429, `{"error":"rate_limited","message":"too many messages"}`, "too many requests; wait a moment and try again"},
		{503, `{"error":"unavailable","message":"internal error"}`, unreachable},
		{404, `not json`, unreachable},
		{404, `{"error":"teapot","message":"x"}`, unreachable},
		{200, `{"id":"x"}`, unreachable},
		{200, `{"id":"5","to":"b@m","online":true,"sent_at":"2026-09-30T00:00:00.000Z","extra":1}`, unreachable},
	} {
		srv, _ := restHub(t, tc.status, tc.body)
		_, err := testClient(t, srv.URL, &recorder{}).send(ctx, "b", "x", "")
		if err == nil || err.Error() != tc.want {
			t.Errorf("%d %s: got %v, want %q", tc.status, tc.body, err, tc.want)
		}
		var ae agentError
		if !errors.As(err, &ae) {
			t.Errorf("%d %s: %T is not an agent text", tc.status, tc.body, err)
		}
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	if _, err := testClient(t, srv.URL, &recorder{}).send(ctx, "b", "x", ""); err == nil || err.Error() != unreachable {
		t.Fatalf("a closed service: %v", err)
	}
}

func TestErrorTextGivesFixedTexts(t *testing.T) {
	leaky := &errorBody{Error: "unauthorized", Message: "missing or invalid token"}
	if got := errorText(401, leaky); got != "not in a session: this machine is not allowed to join" {
		t.Fatal(got)
	}
	if got := errorText(429, &errorBody{Error: "rate_limited", Message: "too many joins"}); !strings.Contains(got, "too many") {
		t.Fatal(got)
	}
	if got := errorText(503, &errorBody{Error: "unavailable", Message: "internal error"}); got != unreachable {
		t.Fatal(got)
	}
	if got := errorText(404, nil); got != unreachable {
		t.Fatal(got)
	}
}

func TestFixedTextsNeverContainAForbiddenWord(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		status := rapid.SampledFrom([]int{401, 500, 502, 503}).Draw(t, "status")
		code := rapid.SampledFrom(errorCodes).Draw(t, "code")
		msg := rapid.String().Draw(t, "message")
		got := errorText(status, &errorBody{Error: code, Message: msg + " token stream"})
		if forbiddenRE.MatchString(got) {
			t.Fatalf("%q", got)
		}
	})
}
