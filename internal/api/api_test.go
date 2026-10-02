package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	coopapi "github.com/AIToolSharing/agent_coop/internal/api"
)

// The embedded document and the router agree on every method and path, and the server gives
// the document out unchanged.
func TestDocumentListsExactlyTheRoutes(t *testing.T) {
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(coopapi.Document, &doc); err != nil {
		t.Fatal(err)
	}
	var want []string
	for p, ops := range doc.Paths {
		for m := range ops {
			want = append(want, strings.ToUpper(m)+" "+p)
		}
	}
	sort.Strings(want)
	h := startHub(t, limits{}, options{})
	got := coopapi.New(nil, nil).Routes()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("routes\n got %q\nwant %q", got, want)
	}
	res, err := http.Get(h.base + "/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	served, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/json" || !bytes.Equal(served, coopapi.Document) {
		t.Fatalf("GET /openapi.json: %d %s, %d bytes (want %d)", res.StatusCode, res.Header.Get("Content-Type"), len(served), len(coopapi.Document))
	}
}

// A known path with a method that no route lists is 405 with Allow, as JSON; an unknown path is
// 404 as JSON; a path is never cleaned or redirected.
func TestMethodNotAllowedAndNotFound(t *testing.T) {
	h := startHub(t, limits{}, options{})
	cases := []struct {
		method, path string
		status       int
		allow        string
	}{
		{"PUT", "/v1/sessions/x/stream", 405, "GET"},
		{"PATCH", "/v1/admin/sessions", 405, "GET, POST"},
		{"GET", "/v1/admin/sessions/x", 405, "DELETE"},
		{"POST", "/v1/sessions/x", 405, "GET"},
		{"GET", "/nope", 404, ""},
		{"GET", "/v1/sessions", 404, ""},
		{"GET", "/v1/sessions//stream", 404, ""},
		{"GET", "/v1/admin/sessions/", 404, ""},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, h.base+c.path, nil)
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

// Property-based contract test of the hub against its own /openapi.json (Schemathesis, positive
// and negative data). Slow and needs uvx, so it runs only with COOP_CONTRACT=1.
func TestContract(t *testing.T) {
	if os.Getenv("COOP_CONTRACT") != "1" {
		t.Skip("set COOP_CONTRACT=1 to run Schemathesis (needs uvx)")
	}
	high := limit{burst: 100_000, perSecond: 100_000}
	h := startHub(t, limits{join: high, msg: high, activity: high}, options{})
	tok := h.token("mac-1")
	op := h.operatorToken("matt")
	ad := admin{base: h.base, token: op}
	if status, _ := ad.create("contract", ""); status != 200 {
		t.Fatalf("create: %d", status)
	}
	// Live agents that schemathesis.toml names, so requests reach the hub's rules.
	a := api{base: h.base, token: tok}
	for _, agent := range []string{"alice", "bob"} {
		s := a.stream("contract", agent)
		if _, err := s.next(func(sseEvent) bool { return true }, 5*time.Second); err != nil {
			t.Fatal(err)
		}
		defer s.close()
	}
	conf, _ := filepath.Abs(filepath.Join("testdata", "schemathesis.toml"))
	conforms := func(bearer, paths, label string) {
		t.Helper()
		args := []string{"schemathesis@4.28.0", "--config-file", conf, "run", h.base + "/openapi.json",
			"--url", h.base, "-H", "Authorization: Bearer " + bearer,
			"--include-path-regex", paths, "--exclude-path-regex", "/stream$",
			"--mode", "all", "--max-examples", envOr("COOP_CONTRACT_EXAMPLES", "100"), "--seed", "1", "--no-color"}
		cmd := exec.Command("uvx", args...)
		out, err := cmd.CombinedOutput()
		if dir := os.Getenv("COOP_CONTRACT_OUT"); dir != "" {
			_ = os.WriteFile(filepath.Join(dir, "schemathesis."+label+".txt"), out, 0o644)
		}
		if err != nil {
			t.Fatalf("schemathesis %s failed: %v\n%s", label, err, out)
		}
	}
	conforms(tok, "^/v1/sessions/", "agent")
	conforms(op, "^/v1/admin/", "admin")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
