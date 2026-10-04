package api_test

import (
	"fmt"
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
		"GET /v1/admin/sessions/{sid}":           read,
		"GET /v1/admin/sessions/{sid}/messages":  read,
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
			article := "a "
			if role == wire.RoleOperator || role == wire.RoleOrchestrator {
				article = "an "
			}
			refused := status == http.StatusForbidden && strings.Contains(string(body), article+role+" token cannot")
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

// The workflow of github.com/map588/agents: an orchestrator starts workers in a session that
// holds new agents, releases them itself, and reads what they say to each other. The operator
// sees who released them.
func TestAnOrchestratorRunsASessionForTheOperator(t *testing.T) {
	high := limit{burst: 10_000, perSecond: 10_000}
	h := startHub(t, limits{join: high, msg: high, activity: high}, options{autoCreate: true, holdNew: true})
	mac := api{base: h.base, token: h.roleToken("mac-1", wire.RoleMachine)}
	orchTok := h.roleToken("orch", wire.RoleOrchestrator)
	orch := api{base: h.base, token: orchTok}
	oadm := admin{h.base, orchTok}
	rep := admin{h.base, h.roleToken("rep", wire.RoleReporter)}
	sid := "pipe-1"
	gateOf := func(a api, agent string) string {
		t.Helper()
		return parse[struct {
			Gate string `json:"gate"`
		}](t, wantStatus(t, 200)(a.gate(sid, agent))).Gate
	}

	// Before and after its join, the orchestrator may work: the session holds only others.
	if g := gateOf(orch, "pm"); g != "run" {
		t.Fatalf("gate of the orchestrator before its join %q, want run", g)
	}
	o := orch.stream(sid, "pm")
	defer o.close()
	if j := data[joinedEvent](t, o.wait(t, nil)); j.Gate != "run" || j.Me != "pm@orch" {
		t.Fatalf("joined %+v, want pm@orch with gate run", j)
	}
	if rec, ok := h.presence(sid + ".orch.pm"); !ok || rec.Role != wire.RoleOrchestrator {
		t.Fatalf("presence %+v, want the role orchestrator", rec)
	}
	// A worker is held. The orchestrator releases it; the record names the orchestrator.
	w1 := mac.stream(sid, "w1")
	defer w1.close()
	if j := data[joinedEvent](t, w1.wait(t, nil)); j.Gate != "held" {
		t.Fatalf("worker joined %+v, want held", j)
	}
	if rec, _ := h.presence(sid + ".mac-1.w1"); rec.Role != "" {
		t.Fatalf("a worker has the role %q", rec.Role)
	}
	wantStatus(t, 204)(oadm.gate(sid, "w1@mac-1", "run"))
	if g := gateOf(mac, "w1"); g != "run" {
		t.Fatalf("worker after the release %q, want run", g)
	}
	var by []string
	for _, e := range h.events(sid) {
		if e.Kind == wire.EventActivity && e.Activity.Kind == "gate" && e.From == "w1@mac-1" {
			by = append(by, e.Activity.Gate+":"+e.Activity.By)
		}
	}
	if strings.Join(by, " ") != "held: run:orch" {
		t.Fatalf("gate records of w1 %v, want [held: run:orch]", by)
	}
	// A stop by the orchestrator names it too.
	w2 := mac.stream(sid, "w2")
	defer w2.close()
	w2.wait(t, nil)
	wantStatus(t, 204)(oadm.kick(sid, "w2@mac-1"))
	kicks := 0
	for _, e := range h.events(sid) {
		if e.Kind == wire.EventKick && e.Target == "w2@mac-1" {
			kicks++
			if e.By != "orch" {
				t.Fatalf("kick %+v, want by orch", e)
			}
		}
	}
	if kicks != 1 {
		t.Fatalf("%d kick records", kicks)
	}

	// The orchestrator and the reporter read the messages between two other agents.
	w3 := mac.stream(sid, "w3")
	defer w3.close()
	w3.wait(t, nil)
	id := parse[sendResponse](t, wantStatus(t, 200)(mac.send(sid, "w1", "w3@mac-1", "API changed: see T2", ""))).ID
	for name, reader := range map[string]admin{"orchestrator": oadm, "reporter": rep} {
		msgs := parse[struct {
			Messages []apiMessage `json:"messages"`
		}](t, wantStatus(t, 200)(reader.req("GET", "/sessions/"+sid+"/messages", nil))).Messages
		if len(msgs) != 1 || msgs[0].ID != id || msgs[0].From != "w1@mac-1" || msgs[0].To != "w3@mac-1" {
			t.Fatalf("%s read %+v, want the message w1 to w3", name, msgs)
		}
		// Paging: after the newest id, nothing.
		after := parse[struct {
			Messages []apiMessage `json:"messages"`
		}](t, wantStatus(t, 200)(reader.req("GET", "/sessions/"+sid+"/messages?after="+id, nil))).Messages
		if len(after) != 0 {
			t.Fatalf("%s: %d messages after the newest", name, len(after))
		}
		// The agents of the session, with their states and gates.
		view := parse[struct {
			Session string `json:"session"`
			Hold    bool   `json:"hold"`
			Agents  []struct {
				Name   string `json:"name"`
				Online bool   `json:"online"`
				State  string `json:"state"`
				Gate   string `json:"gate"`
				Role   string `json:"role"`
			} `json:"agents"`
		}](t, wantStatus(t, 200)(reader.req("GET", "/sessions/"+sid, nil)))
		got := map[string]string{}
		for _, a := range view.Agents {
			got[a.Name] = fmt.Sprintf("%v %s %s %s", a.Online, a.State, a.Gate, a.Role)
		}
		want := map[string]string{
			"pm@orch":  "true idle run orchestrator",
			"w1@mac-1": "true idle run ",
			"w3@mac-1": "true idle held ",
		}
		if view.Session != sid || !view.Hold || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%s: session view %+v\n got %v\nwant %v (a removed agent is not listed)", name, view, got, want)
		}
	}
	wantStatus(t, 404)(rep.req("GET", "/sessions/nosuch/messages", nil))
	wantStatus(t, 422)(rep.req("GET", "/sessions/"+sid+"/messages?after=x", nil))
	wantStatus(t, 422)(rep.req("GET", "/sessions/"+sid+"/messages?limit=0", nil))

	// The orchestrator sends as itself. It cannot send as the operator.
	msg := parse[sendResponse](t, wantStatus(t, 200)(orch.send(sid, "pm", "operator", "Gate 1: approve the stories?", "")))
	if msg.ID == "" {
		t.Fatal("no id")
	}
	if e := h.events(sid); e[len(e)-1].From != "pm@orch" {
		t.Fatalf("the message is from %q, want pm@orch", e[len(e)-1].From)
	}
	wantStatus(t, 403)(oadm.send(sid, "all", "I am the user", ""))
	// The operator can still stop the orchestrator.
	opAdm := admin{h.base, h.roleToken("matt", wire.RoleOperator)}
	wantStatus(t, 204)(opAdm.gate(sid, "pm@orch", "paused"))
	if g := gateOf(orch, "pm"); g != "paused" {
		t.Fatalf("orchestrator after the operator's pause %q, want paused", g)
	}
}
