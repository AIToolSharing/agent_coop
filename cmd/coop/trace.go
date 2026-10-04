package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/AIToolSharing/agent_coop/internal/config"
	"github.com/AIToolSharing/agent_coop/internal/gate"
	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// The trace hooks tell the hub what the agent does at its terminal, so that the operator sees
// it: the tool calls, the agent's own words, and the prompts that a person types there. They
// never stop or change what the agent does: they print nothing, and an error is dropped.

// traceDeadline is how long a trace hook waits for the hub. The agent waits that long too.
const traceDeadline = 2 * time.Second

// traceTimeoutS is the time Claude Code gives a trace hook, in seconds.
const traceTimeoutS = 10

// summaryMax is the most characters of the line that says what a tool call does.
const summaryMax = 200

// traceBodyMax is the most bytes of one report. The hub takes 32 KiB.
const traceBodyMax = 28 * 1024

// transcriptTail is how much of the end of the transcript a hook reads to find the agent's
// words before its tool calls.
const transcriptTail = 512 * 1024

// wordsMax is how many of the newest texts before tool calls a hook reports each time.
const wordsMax = 5

// oneLine makes s one line of at most limit characters: each run of white space and control
// characters becomes one space, and a text that is too long ends with "…".
func oneLine(s string, limit int) string {
	fields := strings.FieldsFunc(strings.ToValidUTF8(s, "?"), func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
	r := []rune(strings.Join(fields, " "))
	if len(r) > limit {
		return string(r[:limit-1]) + "…"
	}
	return string(r)
}

// cutText gives s with at most limit characters. A text that is too long ends with a mark, so
// that the cut is not silent.
func cutText(s string, limit int) string {
	s = strings.ToValidUTF8(s, "?")
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	const mark = " […]"
	return string([]rune(s)[:limit-utf8.RuneCountInString(mark)]) + mark
}

// inProject gives path relative to the project directory when it is in it.
func inProject(path, project string) string {
	if project != "" {
		if rel, ok := strings.CutPrefix(path, strings.TrimSuffix(project, "/")+"/"); ok {
			return rel
		}
	}
	return path
}

// summaryField names the field of the tool input that says best what a call does.
var summaryField = map[string]string{
	"Bash": "command", "Read": "file_path", "Edit": "file_path", "Write": "file_path", "MultiEdit": "file_path",
	"NotebookEdit": "notebook_path", "Grep": "pattern", "Glob": "pattern", "Agent": "description", "Task": "description",
	"WebFetch": "url", "WebSearch": "query", "Skill": "skill",
}

// writeTools are the tools that change a file. Their summary field is the path.
var writeTools = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true}

// summarize says in one line what a tool call does: the command of Bash, the path of a file
// tool, the pattern of a search. For a tool that it does not know, it gives the input.
func summarize(tool string, input json.RawMessage, project string) string {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(input, &fields)
	if key, ok := summaryField[tool]; ok {
		var s string
		if json.Unmarshal(fields[key], &s) == nil && s != "" {
			if strings.HasSuffix(key, "_path") {
				s = inProject(s, project)
			}
			return oneLine(s, summaryMax)
		}
	}
	var compact bytes.Buffer
	if len(fields) == 0 || json.Compact(&compact, input) != nil {
		return ""
	}
	return oneLine(compact.String(), summaryMax)
}

// changedFile gives the file that a tool call writes, relative to the project directory when
// it is in it, or "" for a tool that writes no file.
func changedFile(tool string, input json.RawMessage, project string) string {
	if !writeTools[tool] {
		return ""
	}
	var fields map[string]json.RawMessage
	var s string
	if json.Unmarshal(input, &fields) != nil || json.Unmarshal(fields[summaryField[tool]], &s) != nil {
		return ""
	}
	return cutText(inProject(s, project), wire.MaxTracePath)
}

// wordsBeforeCalls gives the newest texts that the agent wrote before tool calls, as say
// items, oldest first, at most limit. transcript is the end of the session's transcript file
// of Claude Code: one JSON object per line, and one line per part of an answer. The parts of
// one answer have the same message id. Each text names the next tool call of its answer.
//
// A hook reports these texts at each tool call, not only the text before that one call:
// Claude Code writes the transcript late, so the text before a call can be missing when the
// hook of that call runs (found in a live run). The hub drops a text that it has.
func wordsBeforeCalls(transcript []byte, limit int) []wire.TraceItem {
	var out []wire.TraceItem
	pending := map[string][]wire.TraceItem{}
	for _, line := range bytes.Split(transcript, []byte("\n")) {
		var entry struct {
			Type    string `json:"type"`
			UUID    string `json:"uuid"`
			Message struct {
				ID      string          `json:"id"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &entry) != nil || entry.Type != "assistant" {
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Text string `json:"text"`
		}
		if json.Unmarshal(entry.Message.Content, &blocks) != nil {
			continue
		}
		msg := entry.Message.ID
		for i, b := range blocks {
			switch {
			case b.Type == "tool_use" && b.ID != "":
				for _, it := range pending[msg] {
					it.Before = cutText(b.ID, wire.MaxTraceName)
					out = append(out, it)
				}
				delete(pending, msg)
			case b.Type == "text" && strings.TrimSpace(b.Text) != "":
				pending[msg] = append(pending[msg], wire.TraceItem{
					Kind: wire.TraceSay, ID: cutText(entry.UUID+":"+strconv.Itoa(i), wire.MaxTraceName),
					Text: cutText(strings.TrimSpace(b.Text), wire.MaxTraceText),
				})
			}
		}
	}
	return out[max(0, len(out)-limit):]
}

// readTail gives the last limit bytes of a file, or nil.
func readTail(path string, limit int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > limit {
		if _, err := f.Seek(-limit, io.SeekEnd); err != nil {
			return nil
		}
	}
	b, _ := io.ReadAll(io.LimitReader(f, limit))
	return b
}

// gitBranch gives the branch of the work tree that dir is in: the name after refs/heads/, or
// the start of the commit id when no branch is checked out. "" when dir is in no work tree.
// It reads the files of git and starts no process: a hook runs at each tool call.
func gitBranch(dir string) string {
	for range 64 {
		dot := filepath.Join(dir, ".git")
		if info, err := os.Stat(dot); err == nil {
			if !info.IsDir() {
				// A linked work tree: the file names the directory that holds HEAD.
				text, _ := os.ReadFile(dot)
				rest, ok := strings.CutPrefix(strings.TrimSpace(string(text)), "gitdir:")
				if !ok {
					return ""
				}
				if dot = strings.TrimSpace(rest); !filepath.IsAbs(dot) {
					dot = filepath.Join(dir, dot)
				}
			}
			head, err := os.ReadFile(filepath.Join(dot, "HEAD"))
			if err != nil {
				return ""
			}
			text := strings.TrimSpace(string(head))
			if ref, ok := strings.CutPrefix(text, "ref:"); ok {
				return oneLine(strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/"), 256)
			}
			return oneLine(text, 12)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

type traceReport struct {
	Agent  string           `json:"agent"`
	Branch string           `json:"branch,omitempty"`
	Items  []wire.TraceItem `json:"items"`
}

// batches splits items into reports that the hub takes: each has at most wire.MaxTraceItems
// items and at most traceBodyMax bytes.
func batches(agent, branch string, items []wire.TraceItem) [][]byte {
	var out [][]byte
	for len(items) > 0 {
		n := min(len(items), wire.MaxTraceItems)
		for {
			body, _ := json.Marshal(traceReport{Agent: agent, Branch: branch, Items: items[:n]})
			if len(body) <= traceBodyMax || n == 1 {
				out = append(out, body)
				break
			}
			n = max(1, n/2)
		}
		items = items[n:]
	}
	return out
}

// report sends trace items to the hub. It gives up at the first error.
func report(ctx context.Context, client *http.Client, cfg config.Config, branch string, items []wire.TraceItem) {
	if len(items) == 0 || cfg.Session == "" || cfg.URL == "" || cfg.Token == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, traceDeadline)
	defer cancel()
	for _, body := range batches(cfg.Agent, branch, items) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL+"/v1/sessions/"+url.PathEscape(cfg.Session)+"/trace", bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		res.Body.Close()
		if res.StatusCode != 204 {
			return
		}
	}
}

// traceItems gives what one hook event tells the operator. hook is the argument of
// `coop hook`. project is the project directory of the agent.
func traceItems(hook string, in hookInput, project string, transcript func() []byte) []wire.TraceItem {
	tool := cutText(in.ToolName, wire.MaxTraceName)
	id := cutText(in.ToolUseID, wire.MaxTraceName)
	switch hook {
	case "pretool":
		if in.ToolName == "" || gate.Exempt(in.ToolName) {
			return nil
		}
		return []wire.TraceItem{{Kind: wire.TraceToolStart, ID: id, Tool: tool, Text: summarize(in.ToolName, in.ToolInput, project)}}
	case "posttool":
		// The words before a call are in the transcript only after the call.
		items := wordsBeforeCalls(transcript(), wordsMax)
		if in.ToolName == "" || gate.Exempt(in.ToolName) {
			return items
		}
		end := wire.TraceItem{
			Kind: wire.TraceToolEnd, ID: id, Tool: tool, Text: summarize(in.ToolName, in.ToolInput, project),
			Failed: in.Event == "PostToolUseFailure", MS: min(max(in.DurationMS, 0), 86_400_000),
		}
		if !end.Failed {
			end.File = changedFile(in.ToolName, in.ToolInput, project)
		}
		return append(items, end)
	case "prompt":
		if strings.TrimSpace(in.Prompt) == "" {
			return nil
		}
		return []wire.TraceItem{{Kind: wire.TracePrompt, Text: cutText(strings.TrimSpace(in.Prompt), wire.MaxTraceText)}}
	case "stop":
		// The words before the last calls of the turn, when no hook saw them yet.
		items := wordsBeforeCalls(transcript(), wordsMax)
		if strings.TrimSpace(in.LastMessage) == "" {
			return items
		}
		return append(items, wire.TraceItem{Kind: wire.TraceSay, Final: true, Text: cutText(strings.TrimSpace(in.LastMessage), wire.MaxTraceText)})
	}
	return nil
}
