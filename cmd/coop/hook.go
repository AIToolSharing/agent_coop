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
	"strings"
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

// hookInput is what Claude Code gives a PreToolUse hook on stdin; only what the gate needs.
// It names no other field: a field of a type that this struct does not expect would make the
// whole input unreadable (found in use: mcp_server is an object, not a name).
type hookInput struct {
	ToolName string `json:"tool_name"`
}

// hookSettings is the settings text that makes Claude Code call this binary before each tool
// call. `coop claude` passes it with --settings, so no settings file changes.
func hookSettings(exe string) string {
	type hook struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	type group struct {
		Matcher string `json:"matcher"`
		Hooks   []hook `json:"hooks"`
	}
	// The command goes through a shell: quote the path.
	quoted := "'" + strings.ReplaceAll(exe, "'", `'\''`) + "'"
	b, _ := json.Marshal(map[string]any{"hooks": map[string][]group{
		"PreToolUse": {{Matcher: "", Hooks: []hook{{Type: "command", Command: quoted + " hook pretool", Timeout: hookTimeoutS}}}},
	}})
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
	defer res.Body.Close()
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

// cmdHook is what Claude Code runs before a tool call of an agent that `coop claude` started.
// It always exits 0 with its answer on stdout: any other end would let the tool call run.
func cmdHook(args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	if len(args) != 1 || args[0] != "pretool" {
		fmt.Fprintln(stderr, "usage: coop hook pretool   (Claude Code runs it; see coop claude)")
		return 2
	}
	defer func() {
		if r := recover(); r != nil {
			_, _ = stdout.Write(denyOutput(gate.Unknown))
			code = 0
		}
	}()
	env := environ()
	var in hookInput
	raw, err := io.ReadAll(io.LimitReader(stdin, 4<<20))
	if err == nil {
		err = json.Unmarshal(raw, &in)
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
	if reason := pretool(context.Background(), in, env, cfg, hubHTTP(cfg, io.Discard)); reason != "" {
		_, _ = stdout.Write(denyOutput(reason))
	}
	return 0
}
