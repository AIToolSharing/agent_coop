package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/AIToolSharing/agent_coop/internal/config"
	"github.com/AIToolSharing/agent_coop/internal/wire"
	"pgregory.net/rapid"
)

// The summary of a tool call goes into one line of the operator's screen. Whatever the tool
// and its input are: one line, no control character, a limited length, valid text.
func TestSummarizeGivesOneShortLineForAnyInput(t *testing.T) {
	tools := []string{"Bash", "Read", "Edit", "Write", "NotebookEdit", "Grep", "Agent", "WebFetch", "mcp__github__create_issue", "", "Új"}
	rapid.Check(t, func(rt *rapid.T) {
		tool := rapid.SampledFrom(tools).Draw(rt, "tool")
		var input []byte
		if rapid.Bool().Draw(rt, "object") {
			obj := map[string]any{}
			for _, k := range []string{"command", "file_path", "pattern", "description", "other"} {
				if rapid.Bool().Draw(rt, "has "+k) {
					obj[k] = jsonValue(1).Draw(rt, k)
					if rapid.Bool().Draw(rt, "text "+k) {
						obj[k] = rapid.String().Draw(rt, "string "+k)
					}
				}
			}
			input, _ = json.Marshal(obj)
		} else {
			input = rapid.SliceOf(rapid.Byte()).Draw(rt, "bytes")
		}
		got := summarize(tool, input, rapid.SampledFrom([]string{"", "/src/app"}).Draw(rt, "project"))
		if !utf8.ValidString(got) || utf8.RuneCountInString(got) > summaryMax {
			rt.Fatalf("summary %q: %d characters", got, utf8.RuneCountInString(got))
		}
		for _, r := range got {
			if unicode.IsControl(r) || unicode.IsSpace(r) && r != ' ' {
				rt.Fatalf("summary %q has the character %U", got, r)
			}
		}
	})
}

func TestSummarizeSaysWhatTheCallDoes(t *testing.T) {
	long := strings.Repeat("x", 300)
	for _, c := range []struct{ tool, input, want string }{
		{"Bash", `{"command":"go test ./...\n  && make build","description":"Run the tests"}`, "go test ./... && make build"},
		{"Edit", `{"file_path":"/src/app/internal/x.go","old_string":"a","new_string":"b"}`, "internal/x.go"},
		{"Read", `{"file_path":"/etc/hosts"}`, "/etc/hosts"},
		{"Write", `{"file_path":"/src/application/x.go","content":"..."}`, "/src/application/x.go"},
		{"NotebookEdit", `{"notebook_path":"/src/app/n.ipynb"}`, "n.ipynb"},
		{"Grep", `{"pattern":"func main","path":"cmd"}`, "func main"},
		{"Agent", `{"description":"Find the callers","prompt":"..."}`, "Find the callers"},
		{"mcp__github__create_issue", `{"title": "Bug",  "labels": ["a"]}`, `{"title":"Bug","labels":["a"]}`},
		{"Bash", `{"command":7}`, `{"command":7}`},
		{"Bash", `{}`, ""},
		{"Bash", `not json`, ""},
		{"Bash", `{"command":"` + long + `"}`, strings.Repeat("x", summaryMax-1) + "…"},
	} {
		if got := summarize(c.tool, json.RawMessage(c.input), "/src/app"); got != c.want {
			t.Errorf("%s %s: %q, want %q", c.tool, c.input, got, c.want)
		}
	}
	if got := changedFile("Edit", json.RawMessage(`{"file_path":"/src/app/a/b.go"}`), "/src/app/"); got != "a/b.go" {
		t.Errorf("changed file %q, want a/b.go", got)
	}
	if got := changedFile("Read", json.RawMessage(`{"file_path":"/src/app/a/b.go"}`), "/src/app"); got != "" {
		t.Errorf("Read changes no file, got %q", got)
	}
}

// A text that is cut must show it, and must fit the limit of the hub.
func TestCutTextMarksTheCut(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		s := rapid.String().Draw(rt, "s")
		limit := rapid.IntRange(8, 50).Draw(rt, "limit")
		got := cutText(s, limit)
		n := utf8.RuneCountInString(got)
		if n > limit || !utf8.ValidString(got) {
			rt.Fatalf("%q: %d characters, limit %d", got, n, limit)
		}
		if utf8.RuneCountInString(s) <= limit && utf8.ValidString(s) && got != s {
			rt.Fatalf("a short text changed: %q -> %q", s, got)
		}
		if utf8.RuneCountInString(s) > limit && !strings.HasSuffix(got, " […]") {
			rt.Fatalf("the cut of %q is silent: %q", s, got)
		}
	})
}

// The transcript of Claude Code 2.1.286, in short: one line per part of an answer; the parts
// of one answer have the same message id. The second answer has two tool calls at one time.
const transcriptText = `ut off at the start of the tail","uuid":"x"}
{"type":"user","message":{"role":"user","content":"fix the parser"},"uuid":"u0"}
{"type":"assistant","message":{"id":"msg_1","content":[{"type":"thinking","thinking":"hm"}]},"uuid":"a1"}
{"type":"assistant","message":{"id":"msg_1","content":[{"type":"text","text":"I read the parser first.\n"}]},"uuid":"a2"}
{"type":"assistant","message":{"id":"msg_1","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]},"uuid":"a3"}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"..."}]},"uuid":"u1"}
{"type":"attachment","uuid":"x1"}
{"type":"assistant","message":{"id":"msg_2","content":[{"type":"text","text":"Now I change it and run the tests."}]},"uuid":"a4"}
{"type":"assistant","message":{"id":"msg_2","content":[{"type":"tool_use","id":"toolu_2","name":"Edit","input":{}}]},"uuid":"a5"}
{"type":"assistant","message":{"id":"msg_2","content":[{"type":"tool_use","id":"toolu_3","name":"Bash","input":{}}]},"uuid":"a6"}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","content":"ok"}]},"uuid":"u2"}
{"type":"assistant","message":{"id":"msg_3","content":[{"type":"tool_use","id":"toolu_4","name":"Bash","input":{}}]},"uuid":"a7"}
`

func TestWordsBeforeToolCallsNameTheNextCallOfTheirAnswer(t *testing.T) {
	text := func(items []wire.TraceItem) string {
		var out []string
		for _, it := range items {
			if it.Kind != wire.TraceSay || it.Final {
				t.Errorf("item %+v is not words before a call", it)
			}
			out = append(out, it.ID+"|"+it.Before+"|"+it.Text)
		}
		return strings.Join(out, "\n")
	}
	tail := []byte(transcriptText)
	// The second answer has two calls: its words stand before the first of them.
	want := "a2:0|toolu_1|I read the parser first.\na4:0|toolu_2|Now I change it and run the tests."
	if got := text(wordsBeforeCalls(tail, 5)); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := text(wordsBeforeCalls(tail, 1)); got != "a4:0|toolu_2|Now I change it and run the tests." {
		t.Errorf("limit 1: %q", got)
	}
	// Found in a live run with Claude Code 2.1.289: when the hook of the first call of an
	// answer runs, the transcript does not have the call yet, and can lack the words. Words
	// with no call after them are not reported: they can be the end of the turn, which the
	// Stop hook reports.
	early := []byte(strings.Join(strings.Split(transcriptText, "\n")[:8], "\n"))
	if got := text(wordsBeforeCalls(early, 5)); got != "a2:0|toolu_1|I read the parser first." {
		t.Errorf("before the call is written: %q", got)
	}
	if got := wordsBeforeCalls(nil, 5); len(got) != 0 {
		t.Errorf("no transcript: %+v", got)
	}
	long := `{"type":"assistant","message":{"id":"m","content":[{"type":"text","text":"` + strings.Repeat("é", 3000) + `"},{"type":"tool_use","id":"t"}]},"uuid":"` + strings.Repeat("u", 200) + `"}`
	got := wordsBeforeCalls([]byte(long), 5)
	if len(got) != 1 || utf8.RuneCountInString(got[0].Text) != wire.MaxTraceText || !strings.HasSuffix(got[0].Text, " […]") || utf8.RuneCountInString(got[0].ID) > wire.MaxTraceName {
		t.Fatalf("long words: %+v", got)
	}
}

func TestReadTailGivesTheEndOfAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := string(readTail(path, 4)); got != "6789" {
		t.Errorf("tail %q, want 6789", got)
	}
	if got := string(readTail(path, 100)); got != "0123456789" {
		t.Errorf("whole file %q", got)
	}
	if got := readTail(filepath.Join(t.TempDir(), "none"), 4); got != nil {
		t.Errorf("no file: %q", got)
	}
}

func TestGitBranchReadsTheFilesOfGit(t *testing.T) {
	root := t.TempDir()
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repo := filepath.Join(root, "repo")
	write(filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/fix/parser\n")
	write(filepath.Join(repo, "internal", "x", "x.go"), "")
	if got := gitBranch(repo); got != "fix/parser" {
		t.Errorf("repository: %q", got)
	}
	if got := gitBranch(filepath.Join(repo, "internal", "x")); got != "fix/parser" {
		t.Errorf("a directory in the repository: %q", got)
	}
	// No branch is checked out: the start of the commit id.
	write(filepath.Join(repo, ".git", "HEAD"), "0123456789abcdef0123456789abcdef01234567\n")
	if got := gitBranch(repo); got != "0123456789a…" {
		t.Errorf("detached: %q", got)
	}
	// A linked work tree: .git is a file that names the directory with HEAD.
	tree := filepath.Join(root, "tree")
	write(filepath.Join(repo, ".git", "worktrees", "tree", "HEAD"), "ref: refs/heads/agent-1\n")
	write(filepath.Join(tree, ".git"), "gitdir: "+filepath.Join(repo, ".git", "worktrees", "tree")+"\n")
	if got := gitBranch(tree); got != "agent-1" {
		t.Errorf("work tree: %q", got)
	}
	if got := gitBranch(t.TempDir()); got != "" {
		t.Errorf("no repository: %q", got)
	}
}

// The hub takes at most 20 items and 32 KiB in one report. Whatever a hook has to say, each
// report fits, and no item is lost or moved.
func TestBatchesFitTheLimitsOfTheHub(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(0, 70).Draw(rt, "n")
		items := make([]wire.TraceItem, n)
		for i := range items {
			size := rapid.SampledFrom([]int{0, 10, wire.MaxTraceText}).Draw(rt, "size")
			char := rapid.SampledFrom([]string{"a", "é", "<", "\x01", "😀"}).Draw(rt, "char")
			items[i] = wire.TraceItem{Kind: wire.TraceSay, ID: strings.Repeat("i", wire.MaxTraceName), Text: strings.Repeat(char, size)}
		}
		var back []wire.TraceItem
		for _, body := range batches("alice", "main", items) {
			var r traceReport
			if err := json.Unmarshal(body, &r); err != nil {
				rt.Fatal(err)
			}
			if len(body) > traceBodyMax || len(r.Items) < 1 || len(r.Items) > wire.MaxTraceItems || r.Agent != "alice" || r.Branch != "main" {
				rt.Fatalf("a report of %d bytes with %d items", len(body), len(r.Items))
			}
			back = append(back, r.Items...)
		}
		if len(back) != n {
			rt.Fatalf("%d items in the reports, want %d", len(back), n)
		}
		for i := range back {
			if back[i].Text != items[i].Text {
				rt.Fatalf("item %d changed", i)
			}
		}
	})
}

func TestTraceItemsOfEachHook(t *testing.T) {
	none := func() []byte { return nil }
	tail := func() []byte { return []byte(transcriptText) }
	edit := json.RawMessage(`{"file_path":"/src/app/a.go"}`)
	one := func(items []wire.TraceItem) wire.TraceItem {
		t.Helper()
		if len(items) != 1 {
			t.Fatalf("%d items, want 1: %+v", len(items), items)
		}
		return items[0]
	}
	start := one(traceItems("pretool", hookInput{ToolName: "Edit", ToolUseID: "toolu_2", ToolInput: edit}, "/src/app", none))
	if start != (wire.TraceItem{Kind: wire.TraceToolStart, ID: "toolu_2", Tool: "Edit", Text: "a.go"}) {
		t.Errorf("start %+v", start)
	}
	// The end of a call comes with the newest words before calls: the hub drops a repeat.
	items := traceItems("posttool", hookInput{Event: "PostToolUse", ToolName: "Edit", ToolUseID: "toolu_2", ToolInput: edit, DurationMS: 40}, "/src/app", tail)
	if len(items) != 3 || items[0].Before != "toolu_1" || items[1].Kind != wire.TraceSay || items[1].Before != "toolu_2" ||
		items[2] != (wire.TraceItem{Kind: wire.TraceToolEnd, ID: "toolu_2", Tool: "Edit", Text: "a.go", MS: 40, File: "a.go"}) {
		t.Errorf("end of a call: %+v", items)
	}
	// A call that failed changed no file.
	failed := one(traceItems("posttool", hookInput{Event: "PostToolUseFailure", ToolName: "Edit", ToolUseID: "toolu_9", ToolInput: edit, DurationMS: -5}, "/src/app", none))
	if !failed.Failed || failed.File != "" || failed.MS != 0 {
		t.Errorf("failed call %+v", failed)
	}
	// The agent's own coop tools are no news for the operator; the words before them are.
	if got := traceItems("pretool", hookInput{ToolName: "mcp__coop__send", ToolUseID: "toolu_1"}, "", none); got != nil {
		t.Errorf("start of a coop tool: %+v", got)
	}
	if got := traceItems("posttool", hookInput{ToolName: "mcp__coop__send", ToolUseID: "toolu_1"}, "", tail); len(got) != 2 || got[1].Kind != wire.TraceSay {
		t.Errorf("end of a coop tool: %+v", got)
	}
	// The end of a turn: the words before its calls that no hook saw, then the last words.
	if got := traceItems("stop", hookInput{LastMessage: "Done."}, "", tail); len(got) != 3 || got[2] != (wire.TraceItem{Kind: wire.TraceSay, Text: "Done.", Final: true}) {
		t.Errorf("stop with a transcript: %+v", got)
	}
	if got := one(traceItems("prompt", hookInput{Prompt: "  fix it \n"}, "", none)); got != (wire.TraceItem{Kind: wire.TracePrompt, Text: "fix it"}) {
		t.Errorf("prompt %+v", got)
	}
	if got := one(traceItems("stop", hookInput{LastMessage: "Done."}, "", none)); got != (wire.TraceItem{Kind: wire.TraceSay, Text: "Done.", Final: true}) {
		t.Errorf("stop %+v", got)
	}
	for hook, in := range map[string]hookInput{"prompt": {}, "stop": {LastMessage: " "}, "pretool": {}, "other": {ToolName: "Bash"}} {
		if got := traceItems(hook, in, "", none); len(got) != 0 {
			t.Errorf("%s with nothing to say: %+v", hook, got)
		}
	}
}

// traceHub takes gate questions and trace reports, and keeps the reports.
type traceHub struct {
	mu      sync.Mutex
	reports []traceReport
	status  int
}

func (h *traceHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/sessions/build-42/gate":
		_, _ = io.WriteString(w, `{"gate":"run"}`)
	case "/v1/sessions/build-42/trace":
		var rep traceReport
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&rep); err != nil || r.Header.Get("Authorization") != "Bearer mac.1" {
			w.WriteHeader(422)
			return
		}
		h.mu.Lock()
		h.reports = append(h.reports, rep)
		h.mu.Unlock()
		w.WriteHeader(h.status)
	default:
		w.WriteHeader(404)
	}
}

// Claude Code runs the hooks with its input on stdin. The trace hooks tell the hub and print
// nothing: output of a hook can change what Claude Code does.
func TestHookCommandsReportToTheHubAndPrintNothing(t *testing.T) {
	hub := &traceHub{status: 204}
	srv := httptest.NewServer(hub)
	defer srv.Close()
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(project, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	_ = os.WriteFile(transcript, []byte(transcriptText), 0o600)
	t.Setenv("COOP_URL", srv.URL)
	t.Setenv("COOP_TOKEN", "mac.1")
	t.Setenv("COOP_SESSION", "build-42")
	t.Setenv("COOP_AGENT", "alice")
	t.Setenv("COOP_CERT_SHA256", "")
	t.Setenv("COOP_GATE", "")
	t.Setenv("CLAUDE_PROJECT_DIR", project)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	run := func(hook string, input map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(input)
		var out, errOut bytes.Buffer
		if code := cmdHook([]string{hook}, bytes.NewReader(raw), &out, &errOut); code != 0 || out.Len() != 0 || errOut.Len() != 0 {
			t.Fatalf("%s: code %d stdout %q stderr %q", hook, code, out.String(), errOut.String())
		}
	}
	file := filepath.Join(project, "a.go")
	call := map[string]any{"tool_name": "Edit", "tool_use_id": "toolu_2", "tool_input": map[string]any{"file_path": file},
		"transcript_path": transcript, "mcp_server": map[string]any{"name": "x"}}
	run("pretool", call)
	call["hook_event_name"], call["duration_ms"] = "PostToolUse", 40
	run("posttool", call)
	run("prompt", map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": "fix it"})
	run("stop", map[string]any{"hook_event_name": "Stop", "last_assistant_message": "Done."})
	var got []string
	for _, r := range hub.reports {
		if r.Agent != "alice" || r.Branch != "main" {
			t.Errorf("report of %q on %q, want alice on main", r.Agent, r.Branch)
		}
		for _, it := range r.Items {
			got = append(got, it.Kind+" "+it.Tool+" "+it.Text+" "+it.File)
		}
	}
	want := []string{"tool_start Edit a.go ", "say  I read the parser first. ", "say  Now I change it and run the tests. ", "tool_end Edit a.go a.go", "prompt  fix it ", "say  Done. "}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("reports:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// A hub that refuses the report, and input that is not JSON: the hook ends as before.
	hub.status = 503
	run("posttool", call)
	var out bytes.Buffer
	if code := cmdHook([]string{"stop"}, strings.NewReader("not json"), &out, io.Discard); code != 0 || out.Len() != 0 {
		t.Fatalf("bad input: code %d stdout %q", code, out.String())
	}
	// An agent in no session reports nothing.
	t.Setenv("COOP_SESSION", "")
	before := len(hub.reports)
	run("prompt", map[string]any{"prompt": "hello"})
	if len(hub.reports) != before {
		t.Fatal("a hook with no session sent a report")
	}
}

func TestReportGivesUpAtTheFirstError(t *testing.T) {
	hub := &traceHub{status: 429}
	srv := httptest.NewServer(hub)
	defer srv.Close()
	cfg := config.Config{URL: srv.URL, Token: "mac.1", Session: "build-42", Agent: "alice"}
	items := make([]wire.TraceItem, 3*wire.MaxTraceItems)
	for i := range items {
		items[i] = wire.TraceItem{Kind: wire.TraceSay, Text: "x"}
	}
	report(context.Background(), http.DefaultClient, cfg, "", items)
	if len(hub.reports) != 1 {
		t.Fatalf("%d reports after a refusal, want 1", len(hub.reports))
	}
	hub.status, hub.reports = 204, nil
	report(context.Background(), http.DefaultClient, cfg, "", items)
	if len(hub.reports) != 3 {
		t.Fatalf("%d reports, want 3", len(hub.reports))
	}
}
