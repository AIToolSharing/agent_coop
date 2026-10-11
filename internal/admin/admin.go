// Package admin is the hub's watch API as the TUI uses it: the live feed with reconnect and
// resume, and the deletion of a session. Both need a watch token.
package admin

import (
	"bufio"
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

// ErrRefused means the service is reachable and refuses the token, or the token may not watch:
// a retry cannot help.
var ErrRefused = errors.New("the service refused the token, or the token may not show the sessions")

// APIError is a non-2xx answer of an action.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return e.Message }

// Client talks to one hub with one watch token.
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
	// To a token that may not watch, the hub has no such route: 404.
	if res.StatusCode == 401 || res.StatusCode == 403 || res.StatusCode == 404 {
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
	case "presence":
		seen["presence"][ev.Presence.Key] = true
		return emit(model.Update{Presence: ev.Presence})
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

// do runs an action that sends no body and whose answer body does not matter.
func (c *Client) do(ctx context.Context, method, path string) error {
	req, err := http.NewRequestWithContext(ctx, method, c.Base+"/v1/admin"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	res, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return &APIError{Status: res.StatusCode, Message: message(res)}
	}
	return nil
}

func (c *Client) DeleteSession(ctx context.Context, sid string) error {
	return c.do(ctx, http.MethodDelete, "/sessions/"+url.PathEscape(sid))
}
