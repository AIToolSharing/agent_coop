package shim

// The client of the hub API: the REST calls, and the event stream with reconnect and resume.
// It ports packages/mcp/src/client.ts. An error that a call gives is an agentError: its text
// is fit for an agent and never names the service.

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// unreachable is the text for every failure that a retry can repair.
const unreachable = "message service unreachable (retrying)"

// agentError is an error whose text an agent may read.
type agentError string

func (e agentError) Error() string { return string(e) }

// Kinds of link: where the stream stands, for the status tool and for tool errors.
const (
	linkJoining   = "joining"
	linkJoined    = "joined"
	linkNoSession = "no_session"
	linkClosed    = "closed"
	linkRemoved   = "removed"
	linkRefused   = "refused"
	// linkTaken: another session on this machine holds the agent's name.
	linkTaken       = "taken"
	linkUnreachable = "unreachable"
)

type link struct {
	kind string
	me   string // the agent's address, for joined
	gate string // whether the user lets the agent work, for joined
}

// streamHandlers get what the stream gives. The stream loop calls them one at a time.
type streamHandlers struct {
	state   func(link)
	message func(message)
	notice  func(notice)
}

// joinInfo is the query of a join.
type joinInfo struct {
	agent         string
	instance      string // random per shim process; a join with the same instance resumes
	host          string
	cwd           string
	clientName    string
	clientVersion string
	// gated: the agent's tool calls go through the operator's gate.
	gated bool
	// herdrPane is the Herdr pane of this process, or "".
	herdrPane string
}

const (
	// retryClosed is the wait before the next join when the session is closed or unknown.
	retryClosed = 30 * time.Second
	// retryTaken is the wait before the next join when another session holds the name. The
	// hub drops a dead holder within about 25 s, so a restarted session gets its name soon.
	retryTaken = 10 * time.Second
	// maxBackoff limits the wait before the next join after a failure.
	maxBackoff = 30 * time.Second
	// defaultIdle is how long a stream may be silent. The hub pings every 15 s, so a stream
	// silent this long is dead.
	defaultIdle = 45 * time.Second
)

type hubClient struct {
	base, token, session string
	join                 joinInfo
	// httpc nil means http.DefaultClient.
	httpc *http.Client
	// idle is how long the stream may be silent before the client opens it again.
	idle time.Duration
	// sleep waits for d. It gives false when ctx ended first.
	sleep func(ctx context.Context, d time.Duration) bool
	// plain: the join goes without the optional parameters (gated, herdr_pane). A service of
	// an earlier version refuses a join that has them.
	plain bool
}

func newHubClient(base, token, session string, join joinInfo) *hubClient {
	return &hubClient{base: base, token: token, session: session, join: join, idle: defaultIdle, sleep: sleep}
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

// agent gives the agent name that the stream holds.
func (c *hubClient) agent() string { return c.join.agent }

func (c *hubClient) http() *http.Client {
	if c.httpc != nil {
		return c.httpc
	}
	return http.DefaultClient
}

func (c *hubClient) path(p string) string {
	return c.base + "/v1/sessions/" + url.PathEscape(c.session) + p
}

// run holds the stream open until ctx ends, the token is refused, or the agent is removed.
func (c *hubClient) run(ctx context.Context, h streamHandlers) {
	lastID := ""
	backoff := time.Second
	failed := func() bool {
		h.state(link{kind: linkUnreachable})
		ok := c.sleep(ctx, backoff)
		backoff = min(backoff*2, maxBackoff)
		return ok
	}
	for ctx.Err() == nil {
		// The watchdog: no bytes for idle (a lost connection shows no error) ends the request.
		reqCtx, cancel := context.WithCancel(ctx)
		watchdog := time.AfterFunc(c.idle, cancel)
		res, err := c.openStream(reqCtx, lastID)
		if err != nil {
			watchdog.Stop()
			cancel()
			if ctx.Err() != nil || !failed() {
				return
			}
			continue
		}
		if res.StatusCode < 200 || res.StatusCode > 299 {
			e := readError(res)
			watchdog.Stop()
			cancel()
			switch {
			case res.StatusCode == 409:
				// Another session on this machine holds the name. It keeps its place; this
				// one says why it is out and joins when the name is free.
				h.state(link{kind: linkTaken, me: c.join.agent})
				if !c.sleep(ctx, retryTaken) {
					return
				}
			case res.StatusCode == 401:
				h.state(link{kind: linkRefused})
				return
			case res.StatusCode == 422 && !c.plain && (c.join.gated || c.join.herdrPane != ""):
				// A service of an earlier version does not know the optional parameters.
				// Join without them: the agent then works as with that version.
				c.plain = true
			case res.StatusCode == 403 && e != nil && e.Message == "removed from session":
				h.state(link{kind: linkRemoved})
				return
			case res.StatusCode == 403 || res.StatusCode == 404:
				kind := linkClosed
				if res.StatusCode == 404 {
					kind = linkNoSession
				}
				h.state(link{kind: kind})
				if !c.sleep(ctx, retryClosed) {
					return
				}
			default:
				if !failed() {
					return
				}
			}
			continue
		}
		backoff = time.Second
		removed := c.readStream(res.Body, watchdog, h, &lastID)
		_ = res.Body.Close()
		watchdog.Stop()
		cancel()
		if removed {
			h.state(link{kind: linkRemoved})
			return
		}
		if ctx.Err() != nil {
			return
		}
		h.state(link{kind: linkUnreachable})
		if !c.sleep(ctx, time.Second) {
			return
		}
	}
}

func (c *hubClient) openStream(ctx context.Context, lastID string) (*http.Response, error) {
	q := url.Values{
		"agent":          {c.agent()},
		"instance":       {c.join.instance},
		"host":           {c.join.host},
		"cwd":            {c.join.cwd},
		"client_name":    {c.join.clientName},
		"client_version": {c.join.clientVersion},
	}
	if c.join.gated && !c.plain {
		q.Set("gated", "1")
	}
	if c.join.herdrPane != "" && !c.plain {
		q.Set("herdr_pane", c.join.herdrPane)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.path("/stream")+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "text/event-stream")
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	return c.http().Do(req)
}

// readStream gives the stream's events to h until the body ends. Each line resets the
// watchdog. It gives true when the hub removed the agent.
func (c *hubClient) readStream(body io.Reader, watchdog *time.Timer, h streamHandlers, lastID *string) (removed bool) {
	rd := bufio.NewReader(body)
	var event, id string
	var data []string
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return removed
		}
		watchdog.Reset(c.idle)
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(data) > 0 && dispatch(event, id, strings.Join(data, "\n"), h, lastID) {
				removed = true
			}
			event, id, data = "", "", nil
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // a comment, such as the hub's ping
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			data = append(data, value)
		case "id":
			id = value
		}
	}
}

// dispatch gives one event to h. An event that fails its check is dropped. It gives true for
// the kicked notice.
func dispatch(event, id, data string, h streamHandlers, lastID *string) bool {
	switch event {
	case "joined":
		if j, ok := decode[joinedEvent]([]byte(data)); ok {
			h.state(link{kind: linkJoined, me: j.Me, gate: cmp.Or(j.Gate, wire.GateRun)})
		}
	case "message":
		if m, ok := decode[message]([]byte(data)); ok {
			*lastID = m.ID
			h.message(m)
		}
	case "notice":
		if n, ok := decode[notice]([]byte(data)); ok {
			if id != "" {
				*lastID = id
			}
			h.notice(n)
			return n.Kind == noticeKicked
		}
	}
	return false
}

// readError reads the error body of res and closes it. It gives nil when the body is not an
// error body.
func readError(res *http.Response) *errorBody {
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return nil
	}
	e, ok := decode[errorBody](b)
	if !ok {
		return nil
	}
	return &e
}

// errorText is the agent's text for an error response. The hub's own messages are already
// written for agents, except for 401 and failures of the service.
func errorText(status int, e *errorBody) string {
	switch {
	case status == 401:
		return "not in a session: this machine is not allowed to join"
	case status == 429:
		return "too many requests; wait a moment and try again"
	case status >= 500 || e == nil:
		return unreachable
	}
	return e.Message
}

func (c *hubClient) send(ctx context.Context, to, text, replyTo string) (sendResponse, error) {
	body := sendRequest{Agent: c.agent(), To: to, Text: text, ReplyTo: replyTo}
	return call[sendResponse](ctx, c, http.MethodPost, "/messages", body)
}

// activity reports a.Kind for the agent that the stream holds.
func (c *hubClient) activity(ctx context.Context, a activity) error {
	a.Agent = c.agent()
	res, err := c.request(ctx, http.MethodPost, "/activity", a)
	if err != nil {
		return err
	}
	return res.Body.Close()
}

func (c *hubClient) view(ctx context.Context) (sessionView, error) {
	return call[sessionView](ctx, c, http.MethodGet, "?agent="+url.QueryEscape(c.agent()), nil)
}

// history gives the messages that the agent can see, oldest first; withPeer "" means all.
func (c *hubClient) history(ctx context.Context, withPeer string, limit int) (historyResponse, error) {
	q := url.Values{"agent": {c.agent()}, "limit": {strconv.Itoa(limit)}}
	if withPeer != "" {
		q.Set("with", withPeer)
	}
	return call[historyResponse](ctx, c, http.MethodGet, "/messages?"+q.Encode(), nil)
}

func call[T interface{ valid() bool }](ctx context.Context, c *hubClient, method, p string, body any) (T, error) {
	var zero T
	res, err := c.request(ctx, method, p, body)
	if err != nil {
		return zero, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return zero, agentError(unreachable)
	}
	v, ok := decode[T](b)
	if !ok {
		return zero, agentError(unreachable)
	}
	return v, nil
}

// request sends one call. A failure gives an agentError.
func (c *hubClient) request(ctx context.Context, method, p string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.path(p), rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http().Do(req)
	if err != nil {
		return nil, agentError(unreachable)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, agentError(errorText(res.StatusCode, readError(res)))
	}
	return res, nil
}
