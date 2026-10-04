// Package admin is the hub's admin API as the operator's TUI uses it: the live feed with
// reconnect and resume, and the operator's actions. Both need an operator token.
package admin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/model"
	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// ErrRefused means the service is reachable and refuses the token; a retry cannot help.
var ErrRefused = errors.New("the service refused the token")

// APIError is a non-2xx answer of an action.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return e.Message }

// Client talks to one hub with one operator token.
type Client struct {
	Base  string
	Token string
	// HTTP is the client to use; nil means http.DefaultClient.
	HTTP *http.Client
	// Idle is how long the feed may be silent before the connection is dropped and opened
	// again. The hub pings every 15 s. Zero means 45 s.
	Idle time.Duration
	// Backoff gives the wait before connection attempt n (n starts at 0 after a failure).
	// Nil means 1 s, doubled per attempt, at most 30 s.
	Backoff func(attempt int) time.Duration
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) idle() time.Duration {
	if c.Idle > 0 {
		return c.Idle
	}
	return 45 * time.Second
}

func (c *Client) backoff(attempt int) time.Duration {
	if c.Backoff != nil {
		return c.Backoff(attempt)
	}
	d := time.Second << min(attempt, 5)
	return min(d, 30*time.Second)
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// Feed sends the hub's facts to out until ctx ends: link changes, bucket entries, stream
// events, and snapshot marks that let the store drop what the hub no longer has. A dropped
// connection is opened again from the last event id. It returns nil when ctx ends and
// ErrRefused when the token is refused.
func (c *Client) Feed(ctx context.Context, out chan<- model.Update) error {
	emit := func(u model.Update) bool {
		select {
		case out <- u:
			return true
		case <-ctx.Done():
			return false
		}
	}
	lastID := ""
	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := c.stream(ctx, lastID, &lastID, emit)
		if errors.Is(err, ErrRefused) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errConnected) {
			// The connection was open and then dropped: say so, wait a second, try again.
			attempt = 0
			if !emit(model.Update{Link: model.LinkReconnecting}) {
				return nil
			}
		}
		if !sleep(ctx, c.backoff(attempt)) {
			return nil
		}
		attempt++
	}
}

// errConnected marks a stream that was open before it ended.
var errConnected = errors.New("connection dropped")

// stream runs one connection of the feed.
func (c *Client) stream(ctx context.Context, fromID string, lastID *string, emit func(model.Update) bool) error {
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.Base+"/v1/admin/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "text/event-stream")
	if fromID != "" {
		req.Header.Set("Last-Event-ID", fromID)
	}
	res, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return fmt.Errorf("%w: %s", ErrRefused, message(res))
	}
	if res.StatusCode != 200 {
		return fmt.Errorf("the service answered %d", res.StatusCode)
	}
	if !emit(model.Update{Link: model.LinkLive}) {
		return nil
	}
	// The watchdog: silence longer than Idle ends the request.
	watchdog := time.AfterFunc(c.idle(), cancel)
	defer watchdog.Stop()

	seen := map[string]map[string]bool{"sessions": {}, "presence": {}}
	rd := bufio.NewReader(res.Body)
	var data []string
	for {
		line, err := rd.ReadString('\n')
		watchdog.Reset(c.idle())
		if err != nil {
			return errConnected
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(data) > 0 {
				if !c.dispatch(strings.Join(data, "\n"), lastID, seen, emit) {
					return nil
				}
				data = data[:0]
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		if field == "data" {
			data = append(data, value)
		}
	}
}

// dispatch turns one feed event into updates. An event the client cannot decode is skipped.
func (c *Client) dispatch(data string, lastID *string, seen map[string]map[string]bool, emit func(model.Update) bool) bool {
	ev, err := wire.ParseAdminEvent([]byte(data))
	if err != nil {
		return true
	}
	switch ev.Kind {
	case "event":
		*lastID = strconv.FormatInt(ev.Event.Seq, 10)
		return emit(model.Update{Event: ev.Event})
	case "session":
		seen["sessions"][ev.Session.SID] = true
		return emit(model.Update{Session: ev.Session})
	case "kick":
		seen["sessions"][ev.Kick.Key] = true
		return emit(model.Update{Kick: ev.Kick})
	case "presence":
		seen["presence"][ev.Presence.Key] = true
		return emit(model.Update{Presence: ev.Presence})
	case "trace":
		return emit(model.Update{Trace: ev.Trace})
	case "snapshot":
		snap := &model.Snapshot{Bucket: ev.Bucket, Seen: map[string]bool{}}
		for k := range seen[ev.Bucket] {
			snap.Seen[k] = true
		}
		return emit(model.Update{Snapshot: snap})
	}
	return true
}

func message(res *http.Response) string {
	var body struct {
		Message string `json:"message"`
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	if json.Unmarshal(b, &body) == nil && body.Message != "" {
		return body.Message
	}
	return fmt.Sprintf("the service answered %d", res.StatusCode)
}

// --- Actions ---------------------------------------------------------------------------------

func (c *Client) call(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+"/v1/admin"+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		defer res.Body.Close()
		return nil, &APIError{Status: res.StatusCode, Message: message(res)}
	}
	return res, nil
}

// do runs an action whose answer body does not matter.
func (c *Client) do(ctx context.Context, method, path string, body any) error {
	res, err := c.call(ctx, method, path, body)
	if err != nil {
		return err
	}
	return res.Body.Close()
}

func sessionPath(sid string) string { return "/sessions/" + url.PathEscape(sid) }

func (c *Client) ListSessions(ctx context.Context) ([]wire.SessionInfo, error) {
	res, err := c.call(ctx, http.MethodGet, "/sessions", nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var list struct {
		Sessions []wire.SessionInfo `json:"sessions"`
	}
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		return nil, err
	}
	return list.Sessions, nil
}

func (c *Client) CreateSession(ctx context.Context, sid string) error {
	return c.do(ctx, http.MethodPost, "/sessions", map[string]string{"session": sid})
}

func (c *Client) CloseSession(ctx context.Context, sid string) error {
	return c.do(ctx, http.MethodPost, sessionPath(sid)+"/close", nil)
}

func (c *Client) ReopenSession(ctx context.Context, sid string) error {
	return c.do(ctx, http.MethodPost, sessionPath(sid)+"/reopen", nil)
}

func (c *Client) DeleteSession(ctx context.Context, sid string) error {
	return c.do(ctx, http.MethodDelete, sessionPath(sid), nil)
}

// Agent is one agent of a session as GET /v1/admin/sessions/{sid} gives it.
type Agent struct {
	Name      string `json:"name"`
	Online    bool   `json:"online"`
	State     string `json:"state"`
	Note      string `json:"note,omitempty"`
	Gate      string `json:"gate"`
	WaitingOn string `json:"waiting_on,omitempty"`
	Role      string `json:"role,omitempty"`
}

// Session is a session with its agents.
type Session struct {
	wire.SessionInfo
	Agents []Agent `json:"agents"`
}

// Session gives a session with its agents.
func (c *Client) Session(ctx context.Context, sid string) (Session, error) {
	var s Session
	res, err := c.call(ctx, http.MethodGet, sessionPath(sid), nil)
	if err != nil {
		return s, err
	}
	defer res.Body.Close()
	err = json.NewDecoder(res.Body).Decode(&s)
	return s, err
}

// Message is one message of a session.
type Message struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Text    string `json:"text"`
	ReplyTo string `json:"reply_to,omitempty"`
	SentAt  string `json:"sent_at"`
}

// Messages gives the messages of a session, also those between two agents, oldest first.
// With after (a message id), it gives the first limit messages after it; with "", the newest
// limit.
func (c *Client) Messages(ctx context.Context, sid, after string, limit int) ([]Message, error) {
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if after != "" {
		q.Set("after", after)
	}
	res, err := c.call(ctx, http.MethodGet, sessionPath(sid)+"/messages?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var list struct {
		Messages []Message `json:"messages"`
	}
	err = json.NewDecoder(res.Body).Decode(&list)
	return list.Messages, err
}

// Kick keeps an agent out of a session. target is an address.
func (c *Client) Kick(ctx context.Context, sid, target string) error {
	return c.do(ctx, http.MethodPost, sessionPath(sid)+"/kick", map[string]string{"target": target})
}

// Unkick lets a kicked agent back in.
func (c *Client) Unkick(ctx context.Context, sid, target string) error {
	return c.do(ctx, http.MethodPost, sessionPath(sid)+"/unkick", map[string]string{"target": target})
}

// SetGate sets whether an agent may work: run, held or paused. An empty target means every
// agent of the session.
func (c *Client) SetGate(ctx context.Context, sid, target, gate string) error {
	body := map[string]string{"gate": gate}
	if target != "" {
		body["target"] = target
	}
	return c.do(ctx, http.MethodPost, sessionPath(sid)+"/gate", body)
}

// SetHold sets whether a session holds an agent that joins it for the first time.
func (c *Client) SetHold(ctx context.Context, sid string, hold bool) error {
	return c.do(ctx, http.MethodPost, sessionPath(sid)+"/hold", map[string]bool{"hold": hold})
}

// Forget drops an agent that left from a session's lists. The agent may join again.
func (c *Client) Forget(ctx context.Context, sid, target string) error {
	return c.do(ctx, http.MethodPost, sessionPath(sid)+"/forget", map[string]string{"target": target})
}

// Redact withdraws a message. It gives false when id is not a message of this session.
func (c *Client) Redact(ctx context.Context, sid, id string) (bool, error) {
	err := c.do(ctx, http.MethodPost, sessionPath(sid)+"/redact", map[string]string{"id": id})
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == 404 {
		return false, nil
	}
	return err == nil, err
}

// Send writes a message as the operator. to is Broadcast or a peer; replyTo may be empty.
// It gives the new message's id.
func (c *Client) Send(ctx context.Context, sid, to, text, replyTo string) (string, error) {
	if to == wire.Operator {
		return "", errors.New("the operator cannot write to the operator")
	}
	body := struct {
		To      string `json:"to"`
		Text    string `json:"text"`
		ReplyTo string `json:"reply_to,omitempty"`
	}{to, text, replyTo}
	res, err := c.call(ctx, http.MethodPost, sessionPath(sid)+"/messages", body)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var sent struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&sent); err != nil {
		return "", err
	}
	return sent.ID, nil
}
