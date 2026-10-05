package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/config"
	"github.com/AIToolSharing/agent_coop/internal/gate"
	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// hookDeadline is how long the gate hook waits for the hub. The agent waits that long at each
// tool call when the hub does not answer, so it is short.
const hookDeadline = 2 * time.Second

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

// errAuth: the hub refused the token. Unlike a hub that is down, the user must fix this.
var errAuth = errors.New("the hub refused the token (run coop doctor)")

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
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return "", errAuth
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

// gateMemory is the directory where the gate hook keeps the last answer of the hub, next to
// the credential file. It is "" when there is no home directory: the hook then keeps nothing.
func gateMemory() string {
	env := config.DefaultEnvFile()
	if env == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(env), "gate")
}

// refusalFile is the file in dir that holds the gate of the agent of cfg while the hub
// refuses its tool calls. The name is a digest: a session or agent name from the environment
// can hold any character.
func refusalFile(dir string, cfg config.Config) string {
	if dir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(cfg.URL + "\x00" + cfg.Session + "\x00" + cfg.Agent))
	return filepath.Join(dir, hex.EncodeToString(sum[:16]))
}

// remember keeps the answer of the hub for the time when the hub gives none. Only a gate that
// refuses has a file: no file means run. A file that cannot be written is not an error of the
// tool call: the hook then knows no last answer.
func remember(file, g string) {
	switch {
	case file == "":
	case gate.Text(g) == "":
		_ = os.Remove(file)
	case recall(file) != g:
		if os.MkdirAll(filepath.Dir(file), 0o700) == nil {
			_ = os.WriteFile(file, []byte(g+"\n"), 0o600)
		}
	}
}

// recall gives the gate that remember kept, or "" when the last answer was run or there is
// none.
func recall(file string) string {
	b, err := os.ReadFile(file)
	if g := strings.TrimSpace(string(b)); err == nil && gate.Text(g) != "" {
		return g
	}
	return ""
}

// pretool decides one tool call: "" lets it run, any other text is the reason to refuse it.
// A call passes with no question to the hub when the agent is in no session, when the person
// who started it set COOP_GATE=off, or when the tool is exempt. A hub of a version with no
// gate lets each call pass.
//
// When the hub gives no answer, err says why, and the last answer of the hub decides (memory
// is the directory that holds it). An agent that the user holds, paused or removed stays
// refused: a hub that stops must not release it. Each other agent works on: a hub that is
// down must not stop it.
func pretool(ctx context.Context, in hookInput, env map[string]string, cfg config.Config, client *http.Client, memory string) (string, error) {
	if env["COOP_GATE"] == "off" || cfg.Session == "" || cfg.URL == "" || cfg.Token == "" {
		return "", nil
	}
	if gate.Exempt(in.ToolName) {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, hookDeadline)
	defer cancel()
	g, err := askGate(ctx, client, cfg)
	if errors.Is(err, errNoGate) {
		return "", nil
	}
	file := refusalFile(memory, cfg)
	if err != nil {
		if last := recall(file); last != "" {
			return gate.Text(last) + gate.NoAnswer, err
		}
		return "", err
	}
	remember(file, g)
	return gate.Text(g), nil
}

// cmdHook is what Claude Code runs for an agent in a coop session. `pretool` runs before a
// tool call: it asks the gate and prints a refusal on stdout when the operator holds the
// agent. When it cannot read the gate, the last answer decides (see pretool). If that lets the
// call run, the hook says why in one line on stderr: exit 0 for a hub that does not answer,
// exit 1 for a refused token, which Claude Code shows and does not block on. `posttool`,
// `prompt` and `stop` only report to the hub what the agent does; they print nothing.
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
		reason, err := pretool(context.Background(), in, env, cfg, client, gateMemory())
		if reason != "" {
			_, _ = stdout.Write(denyOutput(reason))
			return 0
		}
		if errors.Is(err, errAuth) {
			fmt.Fprintln(stderr, "coop hub:", err, "- continuing without coop")
			return 1
		}
		if err != nil {
			// No trace report either: it would wait for the same hub again.
			fmt.Fprintln(stderr, "coop hub unreachable, continuing without coop:", err)
			return 0
		}
	}
	items := traceItems(hook, in, dir, func() []byte { return readTail(in.Transcript, transcriptTail) })
	report(context.Background(), client, cfg, gitBranch(dir), items)
	return 0
}
