package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The hooks of coop in the user's Claude Code settings: the gate hook, and the hooks that
// report what the agent does. `coop claude` gives the hooks with --settings, which reaches
// only the sessions that it starts. A session that another tool starts (Herdr's `agent
// start`, a plain `claude`) gets the hooks only from the settings file, so `coop setup` writes
// them there.

// claudeSettingsPath is the user's Claude Code settings file.
func claudeSettingsPath() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "settings.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "settings.json")
}

// hookEvent is one hook event of Claude Code that runs `coop hook <arg>`.
type hookEvent struct {
	event   string
	arg     string
	timeout int // seconds that Claude Code gives the hook
}

// hookEvents are the hooks of coop. The first one is the gate hook.
var hookEvents = []hookEvent{
	{"PreToolUse", "pretool", hookTimeoutS},
	{"PostToolUse", "posttool", traceTimeoutS},
	{"PostToolUseFailure", "posttool", traceTimeoutS},
	{"UserPromptSubmit", "prompt", traceTimeoutS},
	{"Stop", "stop", traceTimeoutS},
}

// hookCommand is the command line of `coop hook <arg>` for the binary exe. It goes through a
// shell, so the path is quoted.
func hookCommand(exe, arg string) string {
	return "'" + strings.ReplaceAll(exe, "'", `'\''`) + "' hook " + arg
}

type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

type hookGroup struct {
	Matcher string      `json:"matcher"`
	Hooks   []hookEntry `json:"hooks"`
}

// group is the settings entry that runs the hook of the binary exe at each event.
func (e hookEvent) group(exe string) hookGroup {
	return hookGroup{Matcher: "", Hooks: []hookEntry{{Type: "command", Command: hookCommand(exe, e.arg), Timeout: e.timeout}}}
}

// gateGroup is the PreToolUse entry that runs the gate hook before each tool call.
func gateGroup(exe string) hookGroup { return hookEvents[0].group(exe) }

// member is one key of a JSON object with its value as written.
type member struct {
	key   string
	value json.RawMessage
}

// members reads a JSON object and keeps the order of its keys. Empty input is an empty
// object. The settings file is the user's: coop changes one entry and must not reorder or
// drop the rest.
func members(raw []byte) ([]member, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	var out []member
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("not a JSON object")
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, member{key, v})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("more than one JSON value")
	}
	return out, nil
}

// object writes the members as one JSON object, in their order.
func object(ms []member) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range ms {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(m.key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(m.value)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// set gives ms with key set to value: in place when the key is there, else at the end.
func set(ms []member, key string, value json.RawMessage) []member {
	for i := range ms {
		if ms[i].key == key {
			ms[i].value = value
			return ms
		}
	}
	return append(ms, member{key, value})
}

func get(ms []member, key string) json.RawMessage {
	for _, m := range ms {
		if m.key == key {
			return m.value
		}
	}
	return nil
}

// isCoopGroup reports whether a settings entry is a hook of coop, of any binary path.
func isCoopGroup(raw json.RawMessage) bool {
	var g hookGroup
	if json.Unmarshal(raw, &g) != nil {
		return false
	}
	for _, h := range g.Hooks {
		for _, e := range hookEvents {
			if strings.HasSuffix(h.Command, " hook "+e.arg) && strings.Contains(h.Command, "coop") {
				return true
			}
		}
	}
	return false
}

// hooksOf gives the top-level members of a settings text and the members of its hooks.
func hooksOf(settings []byte) (top, hooks []member, err error) {
	if top, err = members(settings); err != nil {
		return nil, nil, err
	}
	if raw := get(top, "hooks"); raw != nil {
		if hooks, err = members(raw); err != nil {
			return nil, nil, fmt.Errorf("hooks: %w", err)
		}
	}
	return top, hooks, nil
}

// groupsOf gives the entries of one hook event.
func groupsOf(hooks []member, event string) (groups []json.RawMessage, err error) {
	if raw := get(hooks, event); raw != nil {
		if err = json.Unmarshal(raw, &groups); err != nil {
			return nil, fmt.Errorf("hooks.%s: %w", event, err)
		}
	}
	return groups, nil
}

// hooksMissing gives the hooks of the binary exe that the settings text does not run. A text
// that is not a settings object runs none.
func hooksMissing(settings []byte, exe string) []hookEvent {
	_, hooks, err := hooksOf(settings)
	var missing []hookEvent
	for _, e := range hookEvents {
		found := false
		if groups, gerr := groupsOf(hooks, e.event); err == nil && gerr == nil {
			want, _ := json.Marshal(e.group(exe))
			for _, g := range groups {
				var b bytes.Buffer
				found = found || json.Compact(&b, g) == nil && bytes.Equal(b.Bytes(), want)
			}
		}
		if !found {
			missing = append(missing, e)
		}
	}
	return missing
}

// hookInstalled reports whether the settings text runs the gate hook of the binary exe.
func hookInstalled(settings []byte, exe string) bool {
	for _, e := range hooksMissing(settings, exe) {
		if e == hookEvents[0] {
			return false
		}
	}
	return true
}

// installHook gives the settings text with the hooks of exe: the gate hook in
// hooks.PreToolUse, and the hooks that report what the agent does. A hook of coop with another
// binary path is replaced. Every other key and entry stays, in its order. changed is false
// when each hook of exe is there already.
func installHook(settings []byte, exe string) (out []byte, changed bool, err error) {
	top, hooks, err := hooksOf(settings)
	if err != nil {
		return nil, false, err
	}
	for _, e := range hookEvents {
		// A list that coop cannot read is an error, also when the other hooks are there.
		if _, err := groupsOf(hooks, e.event); err != nil {
			return nil, false, err
		}
	}
	if len(hooksMissing(settings, exe)) == 0 {
		return settings, false, nil
	}
	for _, e := range hookEvents {
		groups, _ := groupsOf(hooks, e.event)
		kept := make([]json.RawMessage, 0, len(groups)+1)
		for _, g := range groups {
			if !isCoopGroup(g) {
				kept = append(kept, g)
			}
		}
		ours, _ := json.Marshal(e.group(exe))
		list, _ := json.Marshal(append(kept, ours))
		hooks = set(hooks, e.event, list)
	}
	top = set(top, "hooks", object(hooks))
	var b bytes.Buffer
	if err := json.Indent(&b, object(top), "", "  "); err != nil {
		return nil, false, err
	}
	b.WriteByte('\n')
	return b.Bytes(), true, nil
}

// installHookFile puts the hooks of exe into the settings file at path. Before the first
// change it copies the file to <path>.before-coop. A file that is not a JSON object is left
// as it is, and the error says so.
func installHookFile(path, exe string) (changed bool, err error) {
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	out, changed, err := installHook(old, exe)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	if !changed {
		return false, nil
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
		backup := path + ".before-coop"
		if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(backup, old, mode); err != nil {
				return false, err
			}
		}
	} else if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	// Write a new file and move it into place: a crash leaves the old file whole.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.json")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	return true, os.Rename(tmp.Name(), path)
}

// readUserSettings gives the text of the user's settings file, or nil.
func readUserSettings() []byte {
	path := claudeSettingsPath()
	if path == "" {
		return nil
	}
	raw, _ := os.ReadFile(path)
	return raw
}

// hookInSettings reports whether the user's settings file runs the gate hook of exe.
func hookInSettings(exe string) bool {
	raw := readUserSettings()
	return raw != nil && hookInstalled(raw, exe)
}
