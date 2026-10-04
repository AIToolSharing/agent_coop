package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// roleToken issues a token with any role.
func (h *harness) roleToken(name, role string) string {
	h.t.Helper()
	tok, err := h.st.IssueToken(name, role, h.now())
	if err != nil {
		h.t.Fatal(err)
	}
	return tok
}

// The rules of the four token roles. A machine token acts as agents. The operator is the
// human. An orchestrator is an agent that may also do what the operator does, but it sends
// as itself, never as the operator. A reporter only reads.
func TestEachRoleReachesOnlyItsRoutes(t *testing.T) {
	h := startHub(t, limits{}, options{autoCreate: true})
	tokens := map[string]string{
		wire.RoleMachine:      h.roleToken("mac-1", wire.RoleMachine),
		wire.RoleOperator:     h.roleToken("matt", wire.RoleOperator),
		wire.RoleOrchestrator: h.roleToken("orch", wire.RoleOrchestrator),
		wire.RoleReporter:     h.roleToken("rep", wire.RoleReporter),
	}
	agent := []string{wire.RoleMachine, wire.RoleOrchestrator}
	read := []string{wire.RoleOperator, wire.RoleOrchestrator, wire.RoleReporter}
	act := []string{wire.RoleOperator, wire.RoleOrchestrator}
	all := []string{wire.RoleMachine, wire.RoleOperator, wire.RoleOrchestrator, wire.RoleReporter}
	// Each route of the hub, with the roles that may use it.
	routes := map[string][]string{
		"GET /v1/whoami":                         all,
		"GET /v1/sessions/{sid}/stream":          agent,
		"POST /v1/sessions/{sid}/messages":       agent,
		"GET /v1/sessions/{sid}/messages":        agent,
		"POST /v1/sessions/{sid}/activity":       agent,
		"POST /v1/sessions/{sid}/gate":           agent,
		"POST /v1/sessions/{sid}/trace":          agent,
		"GET /v1/sessions/{sid}":                 agent,
		"GET /v1/admin/sessions":                 read,
		"GET /v1/admin/stream":                   read,
		"POST /v1/admin/sessions":                act,
		"POST /v1/admin/sessions/{sid}/close":    act,
		"POST /v1/admin/sessions/{sid}/reopen":   act,
		"DELETE /v1/admin/sessions/{sid}":        act,
		"POST /v1/admin/sessions/{sid}/kick":     act,
		"POST /v1/admin/sessions/{sid}/unkick":   act,
		"POST /v1/admin/sessions/{sid}/forget":   act,
		"POST /v1/admin/sessions/{sid}/gate":     act,
		"POST /v1/admin/sessions/{sid}/hold":     act,
		"POST /v1/admin/sessions/{sid}/redact":   act,
		"POST /v1/admin/sessions/{sid}/messages": {wire.RoleOperator},
	}
	var served []string
	for _, r := range coopServer(h).Routes() {
		served = append(served, r)
		if _, ok := routes[r]; !ok {
			t.Errorf("the route %s has no rule in this test", r)
		}
	}
	if len(served) != len(routes) {
		t.Errorf("the hub serves %d routes, the test has rules for %d", len(served), len(routes))
	}
	for route, may := range routes {
		method, path, _ := strings.Cut(route, " ")
		path = strings.ReplaceAll(path, "{sid}", "roles-1")
		for role, tok := range tokens {
			// The body does not matter: the role check comes before the body check.
			var status int
			var body []byte
			if path == "/v1/admin/stream" || strings.HasSuffix(path, "/stream") {
				s := openStream(h.base+path+"?agent=a&instance="+newUUID()+"&host=h&cwd=/c&client_name=c&client_version=1", tok, nil, "")
				status, body = s.result()
			} else {
				status, body = httpCall(h.base, tok, method, path, map[string]any{})
			}
			refused := status == http.StatusForbidden && strings.Contains(string(body), "a "+role+" token cannot")
			allowed := false
			for _, r := range may {
				allowed = allowed || r == role
			}
			if allowed == refused {
				t.Errorf("%s with a %s token: status %d %s; allowed %v", route, role, status, body, allowed)
			}
		}
	}
}

// coop login and coop doctor learn the role of a token from the hub.
func TestWhoamiGivesTheNameAndTheRole(t *testing.T) {
	h := startHub(t, limits{}, options{autoCreate: true})
	for _, role := range []string{wire.RoleMachine, wire.RoleOperator, wire.RoleOrchestrator, wire.RoleReporter} {
		got := parse[struct {
			Name string `json:"name"`
			Role string `json:"role"`
		}](t, wantStatus(t, 200)(httpCall(h.base, h.roleToken("t-"+role, role), "GET", "/v1/whoami", nil)))
		if got.Name != "t-"+role || got.Role != role {
			t.Errorf("whoami %+v, want t-%s %s", got, role, role)
		}
	}
	wantStatus(t, 401)(httpCall(h.base, "nope.x", "GET", "/v1/whoami", nil))
}

func coopServer(h *harness) interface{ Routes() []string } {
	return h.srv.Handler.(interface{ Routes() []string })
}
