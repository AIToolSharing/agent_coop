package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/config"
	"github.com/AIToolSharing/agent_coop/internal/gate"
	"pgregory.net/rapid"
)

// gateHub answers the gate question with one gate, and counts the questions.
func gateHub(t *testing.T, answer func(w http.ResponseWriter, body map[string]string)) (cfg config.Config, asked *int) {
	t.Helper()
	asked = new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*asked++
		if r.Method != "POST" || r.URL.Path != "/v1/sessions/build-42/gate" || r.Header.Get("Authorization") != "Bearer mac.1" {
			t.Errorf("request %s %s auth %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		answer(w, body)
	}))
	t.Cleanup(srv.Close)
	return config.Config{URL: srv.URL, Token: "mac.1", Session: "build-42", Agent: "alice"}, asked
}

func gateAnswer(g string) func(http.ResponseWriter, map[string]string) {
	return func(w http.ResponseWriter, body map[string]string) {
		if body["agent"] != "alice" {
			w.WriteHeader(422)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"gate": g})
	}
}

func TestPretoolFollowsTheGate(t *testing.T) {
	ctx := context.Background()
	bash := hookInput{ToolName: "Bash"}
	for g, want := range map[string]string{
		"run":     "",
		"held":    gate.Text("held"),
		"paused":  gate.Text("paused"),
		"removed": gate.Text("removed"),
	} {
		cfg, asked := gateHub(t, gateAnswer(g))
		if got, err := pretool(ctx, bash, map[string]string{}, cfg, http.DefaultClient, t.TempDir()); got != want || err != nil || *asked != 1 {
			t.Errorf("gate %s: reason %q (%v), want %q; asked %d times", g, got, err, want, *asked)
		}
		if g != "run" && want == "" {
			t.Errorf("gate %s has no text", g)
		}
	}
}

// The calls that pass with no question: the agent's own coop tools and ToolSearch (a held
// agent must be able to load and call wait), an agent in no session, a machine that is not
// set up, and a start with COOP_GATE=off.
func TestPretoolAsksOnlyWhenTheGateApplies(t *testing.T) {
	ctx := context.Background()
	cfg, asked := gateHub(t, gateAnswer("held"))
	none := map[string]string{}
	for name, c := range map[string]struct {
		in  hookInput
		env map[string]string
		cfg config.Config
	}{
		"coop tool":      {hookInput{ToolName: "mcp__coop__wait"}, none, cfg},
		"coop tool name": {hookInput{ToolName: "mcp__coop__status"}, none, cfg},
		"tool search":    {hookInput{ToolName: "ToolSearch"}, none, cfg},
		"no session":     {hookInput{ToolName: "Bash"}, none, config.Config{URL: cfg.URL, Token: cfg.Token, Agent: "alice"}},
		"not set up":     {hookInput{ToolName: "Bash"}, none, config.Config{Session: "build-42", Agent: "alice"}},
		"gate off":       {hookInput{ToolName: "Bash"}, map[string]string{"COOP_GATE": "off"}, cfg},
	} {
		if got, err := pretool(ctx, c.in, c.env, c.cfg, http.DefaultClient, t.TempDir()); got != "" || err != nil {
			t.Errorf("%s: refused with %q (%v)", name, got, err)
		}
	}
	if *asked != 0 {
		t.Fatalf("the hub got %d questions, want 0", *asked)
	}
	// Another server's tool, and a call with no tool name, need the gate.
	for _, in := range []hookInput{{ToolName: "mcp__github__create_issue"}, {}} {
		if got, _ := pretool(ctx, in, none, cfg, http.DefaultClient, t.TempDir()); got != gate.Text("held") {
			t.Errorf("%+v: reason %q, want the hold text", in, got)
		}
	}
}

// A hub that is down is not a reason to stop an agent that the hub did not refuse before.
// Every failure of the question lets the call run, and the error says why.
func TestPretoolLetsTheCallRunWhenTheHubGivesNoAnswer(t *testing.T) {
	ctx := context.Background()
	bash := hookInput{ToolName: "Bash"}
	for name, answer := range map[string]func(http.ResponseWriter, map[string]string){
		"error status": func(w http.ResponseWriter, _ map[string]string) { w.WriteHeader(503) },
		"bad gateway":  func(w http.ResponseWriter, _ map[string]string) { w.WriteHeader(502) },
		"rate limit":   func(w http.ResponseWriter, _ map[string]string) { w.WriteHeader(429) },
		"not json":     func(w http.ResponseWriter, _ map[string]string) { _, _ = io.WriteString(w, "<html>") },
		"unknown gate": gateAnswer("maybe"),
		"empty gate":   gateAnswer(""),
	} {
		cfg, _ := gateHub(t, answer)
		if got, err := pretool(ctx, bash, map[string]string{}, cfg, http.DefaultClient, t.TempDir()); got != "" || err == nil || errors.Is(err, errAuth) {
			t.Errorf("%s: reason %q, error %v", name, got, err)
		}
	}
	// A refused token is an error of its own: the user must fix it.
	for _, status := range []int{401, 403} {
		cfg, _ := gateHub(t, func(w http.ResponseWriter, _ map[string]string) { w.WriteHeader(status) })
		if got, err := pretool(ctx, bash, map[string]string{}, cfg, http.DefaultClient, t.TempDir()); got != "" || !errors.Is(err, errAuth) {
			t.Errorf("%d: reason %q, error %v", status, got, err)
		}
	}
	// A hub of a version before the gate has no such route: nobody can hold the agent there,
	// so the call passes with no error.
	old, _ := gateHub(t, func(w http.ResponseWriter, _ map[string]string) {
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"error":"not_found","message":"no such route"}`)
	})
	if got, err := pretool(ctx, bash, map[string]string{}, old, http.DefaultClient, t.TempDir()); got != "" || err != nil {
		t.Errorf("a hub with no gate: refused with %q (%v)", got, err)
	}
	// A hub that is not there.
	cfg := config.Config{URL: "http://127.0.0.1:1", Token: "mac.1", Session: "build-42", Agent: "alice"}
	if got, err := pretool(ctx, bash, map[string]string{}, cfg, http.DefaultClient, t.TempDir()); got != "" || err == nil {
		t.Errorf("no hub: reason %q, error %v", got, err)
	}
	// A hub that does not answer in time: the caller's deadline ends the wait.
	slow, _ := gateHub(t, func(http.ResponseWriter, map[string]string) { time.Sleep(300 * time.Millisecond) })
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if got, err := pretool(short, bash, map[string]string{}, slow, http.DefaultClient, t.TempDir()); got != "" || err == nil {
		t.Errorf("slow hub: reason %q, error %v", got, err)
	}
}

// noAnswer are the ways in which a hub gives no answer to the gate question.
var noAnswer = map[string]func(http.ResponseWriter){
	"error status":  func(w http.ResponseWriter) { w.WriteHeader(503) },
	"refused token": func(w http.ResponseWriter) { w.WriteHeader(401) },
	"not json":      func(w http.ResponseWriter) { _, _ = io.WriteString(w, "<html>") },
}

// stepHub is a hub whose answer to each question the test sets: a gate, or a key of noAnswer.
type stepHub struct {
	srv  *httptest.Server
	step atomic.Pointer[string]
}

func (h *stepHub) set(step string) { h.step.Store(&step) }

func newStepHub(t *testing.T) *stepHub {
	t.Helper()
	h := &stepHub{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		step := *h.step.Load()
		if fail, ok := noAnswer[step]; ok {
			fail(w)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"gate": step})
	}))
	t.Cleanup(h.srv.Close)
	return h
}

// When the hub gives no answer, its last answer decides. An agent that the user holds, paused
// or removed stays refused: a hub that stops (each deploy starts it again) must release no
// agent. An agent that worked, or that the hub never answered, works on.
func TestPretoolFollowsTheLastAnswerWhenTheHubGivesNoAnswer(t *testing.T) {
	for name, c := range map[string]struct {
		steps []string // the answers of the hub in order; the last one is no answer
		want  string   // the gate that decides the last call
	}{
		"held, then the hub stops":           {[]string{"held", "error status"}, "held"},
		"paused, then the hub stops":         {[]string{"paused", "error status"}, "paused"},
		"removed, then the hub stops":        {[]string{"removed", "error status"}, "removed"},
		"run, then the hub stops":            {[]string{"run", "error status"}, "run"},
		"the hub never answered":             {[]string{"error status"}, "run"},
		"held, then run, then the hub stops": {[]string{"held", "run", "error status"}, "run"},
		"held, then a refused token":         {[]string{"held", "refused token"}, "held"},
		"paused, and the hub stays away":     {[]string{"paused", "error status", "not json", "error status"}, "paused"},
	} {
		hub := newStepHub(t)
		cfg := config.Config{URL: hub.srv.URL, Token: "mac.1", Session: "build-42", Agent: "alice"}
		memory := t.TempDir()
		var got string
		var err error
		for _, step := range c.steps {
			hub.set(step)
			got, err = pretool(context.Background(), hookInput{ToolName: "Bash"}, map[string]string{}, cfg, http.DefaultClient, memory)
		}
		want := ""
		if c.want != "run" {
			want = gate.Text(c.want) + gate.NoAnswer
		}
		if got != want || err == nil {
			t.Errorf("%s: reason %q (%v), want %q and an error", name, got, err, want)
		}
	}
}

// The model of the hook's memory: for each agent of each session of each hub, the last answer
// of the hub. An answer decides its own call. No answer leaves the decision to the last
// answer, and the error says that there was none.
func TestPretoolAgreesWithTheModelOfTheLastAnswer(t *testing.T) {
	hubs := []*stepHub{newStepHub(t), newStepHub(t)}
	steps := []string{"run", "held", "paused", "removed"}
	for k := range noAnswer {
		steps = append(steps, k)
	}
	slices.Sort(steps)
	rapid.Check(t, func(rt *rapid.T) {
		memory := t.TempDir()
		last := map[string]string{}
		for i := range rapid.IntRange(1, 12).Draw(rt, "calls") {
			hub := rapid.IntRange(0, 1).Draw(rt, "hub")
			cfg := config.Config{
				URL: hubs[hub].srv.URL, Token: "mac.1",
				Session: rapid.SampledFrom([]string{"s1", "s2"}).Draw(rt, "session"),
				Agent:   rapid.SampledFrom([]string{"alice", "bob"}).Draw(rt, "agent"),
			}
			step := rapid.SampledFrom(steps).Draw(rt, "step")
			hubs[hub].set(step)
			got, err := pretool(context.Background(), hookInput{ToolName: "Bash"}, map[string]string{}, cfg, http.DefaultClient, memory)

			key := cfg.URL + " " + cfg.Session + " " + cfg.Agent
			_, failed := noAnswer[step]
			want := gate.Text(step)
			if failed {
				want = ""
				if gate.Text(last[key]) != "" {
					want = gate.Text(last[key]) + gate.NoAnswer
				}
			} else {
				last[key] = step
			}
			if got != want || (err != nil) != failed || errors.Is(err, errAuth) != (step == "refused token") {
				rt.Fatalf("call %d, %s in %s on hub %d, step %q after %q: reason %q (%v), want %q", i, cfg.Agent, cfg.Session, hub, step, last[key], got, err, want)
			}
		}
	})
}

func TestHookOutputAndSettingsAreWhatClaudeCodeReads(t *testing.T) {
	var out struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(denyOutput("why"), &out); err != nil {
		t.Fatal(err)
	}
	if h := out.HookSpecificOutput; h.HookEventName != "PreToolUse" || h.PermissionDecision != "deny" || h.PermissionDecisionReason != "why" {
		t.Fatalf("%+v", h)
	}
	var s struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(hookSettings("/opt/my tools/it's/coop", hookEvents)), &s); err != nil {
		t.Fatal(err)
	}
	pre := s.Hooks["PreToolUse"]
	if len(pre) != 1 || pre[0].Matcher != "" || len(pre[0].Hooks) != 1 {
		t.Fatalf("%+v", s)
	}
	// The hooks that report what the agent does: one for each event, with the hook's name.
	for event, arg := range map[string]string{"PostToolUse": "posttool", "PostToolUseFailure": "posttool", "UserPromptSubmit": "prompt", "Stop": "stop"} {
		g := s.Hooks[event]
		if len(g) != 1 || len(g[0].Hooks) != 1 || !strings.HasSuffix(g[0].Hooks[0].Command, "coop' hook "+arg) || time.Duration(g[0].Hooks[0].Timeout)*time.Second <= traceDeadline {
			t.Fatalf("%s: %+v", event, g)
		}
	}
	if len(s.Hooks) != len(hookEvents) {
		t.Fatalf("%d events, want %d", len(s.Hooks), len(hookEvents))
	}
	h := pre[0].Hooks[0]
	// The path is quoted for the shell, and Claude Code waits longer than the hook's own limit.
	if h.Type != "command" || h.Command != `'/opt/my tools/it'\''s/coop' hook pretool` || time.Duration(h.Timeout)*time.Second <= hookDeadline {
		t.Fatalf("%+v", h)
	}
}

// setHookEnv gives `coop hook` the hub of cfg, as Claude Code gives it the environment.
func setHookEnv(t *testing.T, cfg config.Config) {
	t.Setenv("COOP_URL", cfg.URL)
	t.Setenv("COOP_TOKEN", cfg.Token)
	t.Setenv("COOP_SESSION", cfg.Session)
	t.Setenv("COOP_AGENT", cfg.Agent)
	t.Setenv("COOP_CERT_SHA256", "")
	t.Setenv("COOP_GATE", "")
	t.Setenv("CLAUDE_PROJECT_DIR", t.TempDir())
	setHome(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
}

// A hub that is down must not stop Claude Code. A hub that refuses connections, one that never
// answers, and nginx in front of a stopped hub: the gate hook lets the call run, says so in
// one line on stderr, exits 0, and does not wait long. A refused token is visible (exit 1,
// which Claude Code shows and does not block on).
func TestHookGoesOnWithoutTheHub(t *testing.T) {
	hang := make(chan struct{})
	silent := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-hang }))
	t.Cleanup(silent.Close)
	t.Cleanup(func() { close(hang) })
	badGateway, _ := gateHub(t, func(w http.ResponseWriter, _ map[string]string) { w.WriteHeader(502) })
	for name, url := range map[string]string{"no hub": "http://127.0.0.1:1", "silent hub": silent.URL, "bad gateway": badGateway.URL} {
		setHookEnv(t, config.Config{URL: url, Token: "mac.1", Session: "build-42", Agent: "alice"})
		var out, errOut bytes.Buffer
		start := time.Now()
		code := cmdHook([]string{"pretool"}, strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"ls"}}`), &out, &errOut)
		took := time.Since(start)
		line := errOut.String()
		if code != 0 || out.Len() != 0 || !strings.HasPrefix(line, "coop hub unreachable, continuing without coop") || strings.Count(line, "\n") != 1 || took > hookDeadline+time.Second {
			t.Errorf("%s: code %d stdout %q stderr %q after %v", name, code, out.String(), line, took)
		}
	}
	refused, _ := gateHub(t, func(w http.ResponseWriter, _ map[string]string) { w.WriteHeader(401) })
	setHookEnv(t, refused)
	var out, errOut bytes.Buffer
	code := cmdHook([]string{"pretool"}, strings.NewReader(`{"tool_name":"Bash"}`), &out, &errOut)
	if line := errOut.String(); code != 1 || out.Len() != 0 || !strings.Contains(line, "refused the token") || strings.Count(line, "\n") != 1 {
		t.Errorf("refused token: code %d stdout %q stderr %q", code, out.String(), line)
	}
}

// The whole hook, as Claude Code runs it. The user holds an agent. Then the hub gives no
// answer, then it refuses the token: the agent stays refused, with the reason on stdout and
// exit 0. After the release, a hub that gives no answer does not refuse the agent. A hub that
// is not there (no connection) has the same result as one that answers with an error.
func TestHookKeepsAnAgentWhereTheUserPutItWhenTheHubStops(t *testing.T) {
	hub := newStepHub(t)
	setHookEnv(t, config.Config{URL: hub.srv.URL, Token: "mac.1", Session: "build-42", Agent: "alice"})
	run := func() (int, string, string) {
		var out, errOut bytes.Buffer
		code := cmdHook([]string{"pretool"}, strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"ls"}}`), &out, &errOut)
		return code, out.String(), errOut.String()
	}
	held := gate.Text("held")
	for i, c := range []struct {
		step     string
		code     int
		out, err string // stdout, and the start of stderr
	}{
		{"held", 0, string(denyOutput(held)), ""},
		{"error status", 0, string(denyOutput(held + gate.NoAnswer)), ""},
		{"refused token", 0, string(denyOutput(held + gate.NoAnswer)), ""},
		{"run", 0, "", ""},
		{"error status", 0, "", "coop hub unreachable, continuing without coop"},
		{"refused token", 1, "", "coop hub: the hub refused the token"},
		{"paused", 0, string(denyOutput(gate.Text("paused"))), ""},
	} {
		hub.set(c.step)
		code, out, errOut := run()
		if code != c.code || out != c.out || !strings.HasPrefix(errOut, c.err) || (c.err == "") != (errOut == "") {
			t.Errorf("call %d (%s): code %d stdout %q stderr %q, want code %d stdout %q stderr %q...", i, c.step, code, out, errOut, c.code, c.out, c.err)
		}
	}
	// The hub stops while the agent is paused.
	hub.srv.Close()
	start := time.Now()
	code, out, errOut := run()
	if took := time.Since(start); code != 0 || out != string(denyOutput(gate.Text("paused")+gate.NoAnswer)) || errOut != "" || took > hookDeadline+time.Second {
		t.Errorf("no hub: code %d stdout %q stderr %q after %v", code, out, errOut, took)
	}
}

func TestHookCommandAnswersOnStdoutAndExitsZero(t *testing.T) {
	cfg, _ := gateHub(t, gateAnswer("paused"))
	setHookEnv(t, cfg)
	run := func(stdin string) (int, string) {
		var out, errOut bytes.Buffer
		code := cmdHook([]string{"pretool"}, strings.NewReader(stdin), &out, &errOut)
		return code, out.String()
	}
	if code, out := run(`{"tool_name":"Bash","tool_input":{"command":"ls"}}`); code != 0 || !strings.Contains(out, `"permissionDecision":"deny"`) || !strings.Contains(out, "paused you") {
		t.Fatalf("paused: code %d out %q", code, out)
	}
	// The input of Claude Code 2.1.289 for a coop tool. Found in a live run: mcp_server is an
	// object. The hook read it as a name, lost the whole input, and refused `wait` to a held
	// agent, which then had nowhere to wait.
	live := `{"session_id":"a75c9872","transcript_path":"/t.jsonl","cwd":"/w","permission_mode":"bypassPermissions","hook_event_name":"PreToolUse","tool_name":"mcp__coop__wait","tool_input":{"from":"operator"},"tool_use_id":"toolu_01","mcp_server":{"name":"coop","source":"dynamic"}}`
	if code, out := run(live); code != 0 || out != "" {
		t.Fatalf("a coop tool: code %d out %q", code, out)
	}
	// Input that is not JSON names no tool: the call needs the gate.
	if code, out := run(`not json`); code != 0 || !strings.Contains(out, `"deny"`) {
		t.Fatalf("bad input: code %d out %q", code, out)
	}
	if code := cmdHook([]string{"other"}, strings.NewReader(""), io.Discard, io.Discard); code != 2 {
		t.Fatalf("unknown hook: code %d", code)
	}
}

// The shim and the hook of one Claude Code process must use the same session and agent name,
// in whatever directory the agent works later. coop claude puts both in the environment.
func TestLaunchEnvNamesTheSessionAndTheAgent(t *testing.T) {
	load := func(env map[string]string) config.Config {
		// As config.Load does: the environment wins, then the project (here: "from-dir").
		c := config.Config{Session: env["COOP_SESSION"], Agent: env["COOP_AGENT"]}
		if c.Agent == "" {
			c.Agent = "from-dir"
		}
		return c
	}
	has := func(env []string, kv string) bool {
		for _, e := range env {
			if e == kv {
				return true
			}
		}
		return false
	}
	env := launchEnv(map[string]string{"PATH": "/bin", "COOP_SESSION": "old", "COOP_ROLE": "orchestrator"}, "build-42", "", load)
	for _, kv := range []string{"PATH=/bin", "COOP_PUSH=1", "COOP_GATED=1", "COOP_SESSION=build-42", "COOP_AGENT=from-dir"} {
		if !has(env, kv) {
			t.Errorf("missing %s in %v", kv, env)
		}
	}
	// Each name one time: a second COOP_SESSION could win in the child.
	seen := map[string]int{}
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		seen[k]++
	}
	for k, n := range seen {
		if n != 1 {
			t.Errorf("%s is %d times in the environment", k, n)
		}
	}
	// An agent that an orchestrator starts gets no role from the orchestrator's environment.
	for _, e := range env {
		if strings.HasPrefix(e, "COOP_ROLE=") {
			t.Errorf("a plain start keeps %s", e)
		}
	}
	// No session: nothing is named, and the agent gets no tools and no gate.
	env = launchEnv(map[string]string{"PATH": "/bin"}, "", "", load)
	if has(env, "COOP_SESSION=") || has(env, "COOP_AGENT=from-dir") {
		t.Errorf("no session, but %v", env)
	}
	// --orchestrator sets the role.
	for _, role := range []string{"orchestrator"} {
		if env := launchEnv(map[string]string{"PATH": "/bin"}, "build-42", role, load); !has(env, "COOP_ROLE="+role) {
			t.Errorf("%s: %v", role, env)
		}
	}
}
