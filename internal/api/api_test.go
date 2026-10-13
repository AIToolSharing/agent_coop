package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// A known path with a method that no route lists is 405 with Allow, as JSON; an unknown path is
// 404 as JSON; a path is never cleaned or redirected.
func TestMethodNotAllowedAndNotFound(t *testing.T) {
	h := startHub(t, limits{}, options{})
	machine := h.token("mac-1")
	cases := []struct {
		token        string
		method, path string
		status       int
		allow        string
	}{
		{"", "PUT", "/v1/sessions/x/stream", 405, "GET"},
		{machine, "PATCH", "/v1/admin/sessions", 405, "GET"},
		{"", "PATCH", "/v1/admin/sessions", 405, "GET"},
		// The person who watches creates no session and sends no message.
		{machine, "POST", "/v1/admin/sessions", 405, "GET"},
		{machine, "POST", "/v1/admin/sessions/x/messages", 404, ""},
		{machine, "PUT", "/v1/admin/sessions/x", 405, "DELETE"},
		{"", "PUT", "/v1/admin/sessions/x", 405, "DELETE"},
		{"", "POST", "/v1/sessions/x", 405, "GET"},
		{"", "GET", "/nope", 404, ""},
		{"", "GET", "/v1/sessions", 404, ""},
		{"", "GET", "/v1/sessions//stream", 404, ""},
		{"", "GET", "/v1/admin/sessions/", 404, ""},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, h.base+c.path, nil)
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		res, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		var body struct{ Error, Message string }
		_ = json.NewDecoder(res.Body).Decode(&body)
		res.Body.Close()
		if res.StatusCode != c.status || res.Header.Get("Allow") != c.allow || body.Error == "" {
			t.Errorf("%s %s: %d Allow=%q body=%+v, want %d Allow=%q", c.method, c.path, res.StatusCode, res.Header.Get("Allow"), body, c.status, c.allow)
		}
	}
}

// A bad session name in the path is 422 for a holder of a token, 401 without one.
func TestBadSessionNameInThePath(t *testing.T) {
	h := startHub(t, limits{}, options{})
	tok := h.token("mac-1")
	for _, c := range []struct {
		token  string
		status int
	}{{tok, 422}, {"", 401}} {
		req, _ := http.NewRequest("GET", h.base+"/v1/sessions/Bad.Name?agent=alice", nil)
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != c.status {
			t.Errorf("token %q: %d, want %d", c.token, res.StatusCode, c.status)
		}
	}
}
