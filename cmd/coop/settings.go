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

// The gate hook in the user's Claude Code settings. `coop claude` gives the hook with
// --settings, which reaches only the sessions that it starts. A session that another tool
// starts (Herdr's `agent start`, a plain `claude`) gets the hook only from the settings file,
// so `coop setup` writes it there.

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

// hookCommand is the command line of the gate hook for the binary exe. It goes through a
// shell, so the path is quoted.
func hookCommand(exe string) string {
	return "'" + strings.ReplaceAll(exe, "'", `'\''`) + "' hook pretool"
}

// hookSuffix ends the command line of every gate hook, of this binary or of another copy.
const hookSuffix = " hook pretool"

type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

type hookGroup struct {
	Matcher string      `json:"matcher"`
	Hooks   []hookEntry `json:"hooks"`
}

// gateGroup is the PreToolUse entry that runs the gate hook before each tool call.
func gateGroup(exe string) hookGroup {
	return hookGroup{Matcher: "", Hooks: []hookEntry{{Type: "command", Command: hookCommand(exe), Timeout: hookTimeoutS}}}
}

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

// isGateGroup reports whether a PreToolUse entry is a gate hook of coop, of any binary path.
func isGateGroup(raw json.RawMessage) bool {
	var g hookGroup
	if json.Unmarshal(raw, &g) != nil {
		return false
	}
	for _, h := range g.Hooks {
		if strings.HasSuffix(h.Command, hookSuffix) && strings.Contains(h.Command, "coop") {
			return true
		}
	}
	return false
}

// preToolUse gives the PreToolUse entries of a settings text.
func preToolUse(settings []byte) (top, hooks []member, groups []json.RawMessage, err error) {
	if top, err = members(settings); err != nil {
		return nil, nil, nil, err
	}
	if raw := get(top, "hooks"); raw != nil {
		if hooks, err = members(raw); err != nil {
			return nil, nil, nil, fmt.Errorf("hooks: %w", err)
		}
	}
	if raw := get(hooks, "PreToolUse"); raw != nil {
		if err = json.Unmarshal(raw, &groups); err != nil {
			return nil, nil, nil, fmt.Errorf("hooks.PreToolUse: %w", err)
		}
	}
	return top, hooks, groups, nil
}

// hookInstalled reports whether the settings text runs the gate hook of the binary exe.
func hookInstalled(settings []byte, exe string) bool {
	_, _, groups, err := preToolUse(settings)
	if err != nil {
		return false
	}
	want, _ := json.Marshal(gateGroup(exe))
	for _, g := range groups {
		var b bytes.Buffer
		if json.Compact(&b, g) == nil && bytes.Equal(b.Bytes(), want) {
			return true
		}
	}
	return false
}

// installHook gives the settings text with the gate hook of exe in hooks.PreToolUse. A gate
// hook of another binary path is replaced. Every other key and entry stays, in its order.
// changed is false when the hook of exe is there already.
func installHook(settings []byte, exe string) (out []byte, changed bool, err error) {
	if hookInstalled(settings, exe) {
		return settings, false, nil
	}
	top, hooks, groups, err := preToolUse(settings)
	if err != nil {
		return nil, false, err
	}
	kept := make([]json.RawMessage, 0, len(groups)+1)
	for _, g := range groups {
		if !isGateGroup(g) {
			kept = append(kept, g)
		}
	}
	ours, _ := json.Marshal(gateGroup(exe))
	kept = append(kept, ours)
	list, _ := json.Marshal(kept)
	top = set(top, "hooks", object(set(hooks, "PreToolUse", list)))
	var b bytes.Buffer
	if err := json.Indent(&b, object(top), "", "  "); err != nil {
		return nil, false, err
	}
	b.WriteByte('\n')
	return b.Bytes(), true, nil
}

// installHookFile puts the gate hook of exe into the settings file at path. Before the first
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

// hookInSettings reports whether the user's settings file runs the gate hook of exe.
func hookInSettings(exe string) bool {
	path := claudeSettingsPath()
	if path == "" {
		return false
	}
	raw, err := os.ReadFile(path)
	return err == nil && hookInstalled(raw, exe)
}
