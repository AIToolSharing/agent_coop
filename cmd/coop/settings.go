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

// coop v0.10.0 and before put hooks into the user's Claude Code settings: the gate hook and
// the hooks that reported what the agent did. coop has no hooks now. `coop setup` removes the
// old entries. The settings file is the user's: every other key and entry stays, in its order.

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

// oldHookArgs are the arguments of `coop hook` in the entries of an old setup.
var oldHookArgs = []string{"pretool", "posttool", "prompt", "stop"}

// member is one key of a JSON object with its value as written.
type member struct {
	key   string
	value json.RawMessage
}

// members reads a JSON object and keeps the order of its keys. Empty input is an empty
// object.
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

func get(ms []member, key string) json.RawMessage {
	for _, m := range ms {
		if m.key == key {
			return m.value
		}
	}
	return nil
}

// isCoopGroup reports whether a settings entry runs `coop hook <arg>` of an old setup, of any
// binary path.
func isCoopGroup(raw json.RawMessage) bool {
	var g struct {
		Hooks []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if json.Unmarshal(raw, &g) != nil {
		return false
	}
	for _, h := range g.Hooks {
		for _, arg := range oldHookArgs {
			if strings.HasSuffix(h.Command, " hook "+arg) && strings.Contains(h.Command, "coop") {
				return true
			}
		}
	}
	return false
}

// removeHooks gives the settings text without the entries of coop. An event list that is
// empty after it goes, and so does a hooks object that is empty after it. changed is false
// when the text has no entry of coop; the text then stays byte for byte.
func removeHooks(settings []byte) (out []byte, changed bool, err error) {
	top, err := members(settings)
	if err != nil {
		return nil, false, err
	}
	raw := get(top, "hooks")
	if raw == nil {
		return settings, false, nil
	}
	hooks, err := members(raw)
	if err != nil {
		return nil, false, fmt.Errorf("hooks: %w", err)
	}
	var kept []member
	for _, h := range hooks {
		var groups []json.RawMessage
		if err := json.Unmarshal(h.value, &groups); err != nil {
			return nil, false, fmt.Errorf("hooks.%s: %w", h.key, err)
		}
		var others []json.RawMessage
		for _, g := range groups {
			if isCoopGroup(g) {
				changed = true
			} else {
				others = append(others, g)
			}
		}
		switch {
		case len(others) == len(groups):
			kept = append(kept, h)
		case len(others) > 0:
			list, _ := json.Marshal(others)
			kept = append(kept, member{h.key, list})
		}
	}
	if !changed {
		return settings, false, nil
	}
	var next []member
	for _, m := range top {
		switch {
		case m.key != "hooks":
			next = append(next, m)
		case len(kept) > 0:
			next = append(next, member{"hooks", object(kept)})
		}
	}
	var b bytes.Buffer
	if err := json.Indent(&b, object(next), "", "  "); err != nil {
		return nil, false, err
	}
	b.WriteByte('\n')
	return b.Bytes(), true, nil
}

// removeHookFile removes the entries of coop from the settings file at path. A file that does
// not exist has none. A file that is not a JSON object is left as it is, and the error says
// so.
func removeHookFile(path string) (changed bool, err error) {
	old, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	out, changed, err := removeHooks(old)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	if !changed {
		return false, nil
	}
	info, err := os.Stat(path)
	if err != nil {
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
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	return true, os.Rename(tmp.Name(), path)
}
