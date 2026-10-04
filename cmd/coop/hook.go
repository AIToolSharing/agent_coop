package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/config"
	"github.com/AIToolSharing/agent_coop/internal/gate"
	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// hookDeadline is how long the hook waits for the hub. Claude Code runs a tool call whose hook
// crashes or runs out of time, so the hook must give its answer itself, and soon.
const hookDeadline = 8 * time.Second

// hookTimeoutS is the time Claude Code gives the hook, in seconds. It is longer than
// hookDeadline, so that the hook's own answer always comes first.
const hookTimeoutS = 30

// hookInput is what Claude Code gives a hook on stdin; only what coop uses.
type hookInput struct {
	Event       string          // hook_event_name
	ToolName    string          // tool_name
	ToolUseID   string          // tool_use_id
	ToolInput   json.RawMessage // tool_input
	DurationMS  int64           // duration_ms
	Transcript  string          // transcript_path
	Prompt      string          // prompt
	LastMessage string          // last_assistant_message
}

// readHookInput reads the input of a hook. It reads each field on its own: a field of a type
// that coop does not expect must not make the whole input unreadable (found in use:
// mcp_server is an object, not a name).
func readHookInput(raw []byte) (hookInput, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return hookInput{}, err
	}
	str := func(key string) string {
		var s string
		_ = json.Unmarshal(fields[key], &s)
		return s
	}
	in := hookInput{
		Event: str("hook_event_name"), ToolName: str("tool_name"), ToolUseID: str("tool_use_id"), ToolInput: fields["tool_input"],
		Transcript: str("transcript_path"), Prompt: str("prompt"), LastMessage: str("last_assistant_message"),
	}
	var ms float64
	_ = json.Unmarshal(fields["duration_ms"], &ms)
	in.DurationMS = int64(ms)
	return in, nil
}

// hookSettings is the settings text that makes Claude Code call this binary for the given
// hook events. `coop claude` passes it with --settings, so no settings file changes.
func hookSettings(exe string, events []hookEvent) string {
	hooks := map[string][]hookGroup{}
	for _, e := range events {
		hooks[e.event] = []hookGroup{e.group(exe)}
	}
	b, _ := json.Marshal(map[string]any{"hooks": hooks})
	return string(b)
}

// denyOutput is the answer that makes Claude Code refuse the tool call and give the agent the
// reason.
func denyOutput(reason string) []byte {
	b, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName":            "PreToolUse",
		"permissionDecision":       "deny",
		"permissionDecisionReason": reason,
	}})
	return b
}

// errNoGate: the hub is of a version that has no gate. No operator can hold the agent there.
var errNoGate = errors.New("the hub has no gate")

// askGate asks the hub whether the operator lets the agent work.
func askGate(ctx context.Context, client *http.Client, cfg config.Config) (string, error) {
	body, _ := json.Marshal(map[string]string{"agent": cfg.Agent})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL+"/v1/sessions/"+url.PathEscape(cfg.Session)+"/gate", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	// Read the answer to its end: the trace report then uses the same connection.
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		res.Body.Close()
	}()
	if res.StatusCode == 404 {
		// The gate route answers 200 for each session, known or not. Only a hub that does not
		// have the route answers 404.
		return "", errNoGate
	}
	if res.StatusCode != 200 {
		return "", fmt.Errorf("the hub answered %d", res.StatusCode)
	}
	var out struct {
		Gate string `json:"gate"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&out); err != nil {
		return "", err
	}
	if out.Gate != gate.Removed && !wire.IsGate(out.Gate) {
		return "", fmt.Errorf("the hub gave the gate %q", out.Gate)
	}
	return out.Gate, nil
}

// pretool decides one tool call: "" lets it run, any other text is the reason to refuse it.
// A call passes with no question to the hub when the agent is in no session, when the person
// who started it set COOP_GATE=off, or when the tool is exempt. A hub of a version with no
// gate lets each call pass. When the hub gives no answer, the call is refused: the operator
// could not stop it.
func pretool(ctx context.Context, in hookInput, env map[string]string, cfg config.Config, client *http.Client) string {
	if env["COOP_GATE"] == "off" || cfg.Session == "" || cfg.URL == "" || cfg.Token == "" {
		return ""
	}
	if gate.Exempt(in.ToolName) {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, hookDeadline)
	defer cancel()
	g, err := askGate(ctx, client, cfg)
	if errors.Is(err, errNoGate) {
		return ""
	}
	if err != nil {
		return gate.Unknown
	}
	return gate.Text(g)
}

// cmdHook is what Claude Code runs for an agent in a coop session. `pretool` runs before a
// tool call: it asks the gate, and it always exits 0 with its answer on stdout, because any
// other end would let the tool call run. `posttool`, `prompt` and `stop` only report to the
// hub what the agent does; they print nothing.
func cmdHook(args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	known := false
	for _, e := range hookEvents {
		known = known || len(args) == 1 && args[0] == e.arg
	}
	if !known {
		fmt.Fprintln(stderr, "usage: coop hook pretool|posttool|prompt|stop   (Claude Code runs it; see coop claude)")
		return 2
	}
	hook := args[0]
	defer func() {
		if r := recover(); r != nil {
			if hook == "pretool" {
				_, _ = stdout.Write(denyOutput(gate.Unknown))
			}
			code = 0
		}
	}()
	env := environ()
	var in hookInput
	raw, err := io.ReadAll(io.LimitReader(stdin, 4<<20))
	if err == nil {
		in, err = readHookInput(raw)
	}
	// The shim starts in the project directory; the agent may be in another one by now.
	dir := env["CLAUDE_PROJECT_DIR"]
	if dir == "" {
		dir = cwd()
	}
	cfg := config.Load(env, config.DefaultEnvFile(), func(string) {}, dir)
	if err != nil {
		// Input that cannot be read names no tool: treat the call as one that needs the gate.
		in = hookInput{}
	}
	client := hubHTTP(cfg, io.Discard)
	if hook == "pretool" {
		if reason := pretool(context.Background(), in, env, cfg, client); reason != "" {
			_, _ = stdout.Write(denyOutput(reason))
			return 0
		}
	}
	items := traceItems(hook, in, dir, func() []byte { return readTail(in.Transcript, transcriptTail) })
	report(context.Background(), client, cfg, gitBranch(dir), items)
	return 0
}
