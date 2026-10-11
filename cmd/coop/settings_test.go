package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

func keysOf(t interface{ Fatalf(string, ...any) }, raw []byte) []string {
	ms, err := members(raw)
	if err != nil {
		t.Fatalf("not an object: %v\n%s", err, raw)
	}
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.key
	}
	return out
}

func sameJSON(a, b []byte) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

// oldGroup is an entry that coop v0.10.0 put into the settings for `coop hook <arg>`.
func oldGroup(exe, arg string) map[string]any {
	return map[string]any{"matcher": "", "hooks": []any{map[string]any{"type": "command", "command": "'" + exe + "' hook " + arg, "timeout": 30}}}
}

// The settings of this user on 2026-10-03, in short, with the hooks of coop v0.10.0: hooks of
// other tools, and other keys.
const userSettings = `{
  "model": "opus",
  "hooks": {
    "SessionStart": [{"hooks": [{"type": "command", "command": "bash '/Users/u/.claude/hooks/agent-state.sh' session"}]}],
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "jq -re '.tool_input.command'"}]},
      {"hooks": [{"type": "command", "command": "/Users/u/.cargo/bin/tokensave"}]},
      {"matcher": "", "hooks": [{"type": "command", "command": "'/Users/u/.local/bin/coop' hook pretool", "timeout": 30}]}
    ],
    "Stop": [
      {"hooks": [{"type": "command", "command": "$HOME/.claude/hooks/verify-done.sh"}]},
      {"matcher": "", "hooks": [{"type": "command", "command": "'/Users/u/.local/bin/coop' hook stop", "timeout": 10}]}
    ],
    "PostToolUse": [{"matcher": "", "hooks": [{"type": "command", "command": "'/Users/u/.local/bin/coop' hook posttool", "timeout": 10}]}],
    "PostToolUseFailure": [{"matcher": "", "hooks": [{"type": "command", "command": "'/Users/u/.local/bin/coop' hook posttool", "timeout": 10}]}],
    "UserPromptSubmit": [{"matcher": "", "hooks": [{"type": "command", "command": "'/Users/u/.local/bin/coop' hook prompt", "timeout": 10}]}]
  },
  "permissions": {"allow": ["Bash(ls:*)"]},
  "env": {"A": "1"}
}`

func TestRemoveHooksTakesOnlyTheEntriesOfCoop(t *testing.T) {
	out, changed, err := removeHooks([]byte(userSettings))
	if err != nil || !changed {
		t.Fatalf("changed %v err %v", changed, err)
	}
	if strings.Contains(string(out), " hook ") {
		t.Fatalf("an entry of coop is left:\n%s", out)
	}
	if got := keysOf(t, out); strings.Join(got, ",") != "model,hooks,permissions,env" {
		t.Fatalf("top-level keys %v", got)
	}
	top, _ := members(out)
	if got := keysOf(t, get(top, "hooks")); strings.Join(got, ",") != "SessionStart,PreToolUse,Stop" {
		t.Fatalf("hook events %v", got)
	}
	for _, other := range []string{"agent-state.sh", "jq -re", "tokensave", "verify-done.sh", "Bash(ls:*)"} {
		if !strings.Contains(string(out), other) {
			t.Fatalf("%s is gone:\n%s", other, out)
		}
	}
	// A second run changes nothing.
	if again, changed, err := removeHooks(out); err != nil || changed || string(again) != string(out) {
		t.Fatalf("second run: changed %v err %v", changed, err)
	}
	// Settings with only the hooks of coop lose the hooks key.
	only := `{"model":"opus","hooks":{"PreToolUse":[{"matcher":"","hooks":[{"type":"command","command":"'/opt/coop' hook pretool","timeout":30}]}]}}`
	if out, changed, err := removeHooks([]byte(only)); err != nil || !changed || strings.Join(keysOf(t, out), ",") != "model" {
		t.Fatalf("changed %v err %v\n%s", changed, err, out)
	}
	for _, none := range []string{"", "{}", `{"hooks":{}}`, `{"model":"opus"}`} {
		if out, changed, err := removeHooks([]byte(none)); err != nil || changed || string(out) != none {
			t.Fatalf("%q: changed %v err %v out %q", none, changed, err, out)
		}
	}
	for _, bad := range []string{"[]", `"text"`, "{", `{"hooks": []}`, `{"hooks": {"PreToolUse": {}}}`, `{} {}`} {
		if out, _, err := removeHooks([]byte(bad)); err == nil {
			t.Fatalf("%q: no error, out %s", bad, out)
		}
	}
}

// jsonValue draws a JSON value of a small depth.
func jsonValue(depth int) *rapid.Generator[any] {
	return rapid.Custom(func(t *rapid.T) any {
		kind := rapid.IntRange(0, 5).Draw(t, "kind")
		if depth <= 0 && kind > 3 {
			kind = 0
		}
		switch kind {
		case 0:
			return rapid.StringMatching(`[a-zA-Z0-9 _./$'-]{0,12}`).Draw(t, "s")
		case 1:
			return float64(rapid.IntRange(-5, 5).Draw(t, "n"))
		case 2:
			return rapid.Bool().Draw(t, "b")
		case 3:
			return nil
		case 4:
			return rapid.SliceOfN(jsonValue(depth-1), 0, 3).Draw(t, "list")
		}
		return rapid.MapOfN(rapid.StringMatching(`[a-z]{1,6}`), jsonValue(depth-1), 0, 3).Draw(t, "obj")
	})
}

// For any settings object: put the entries of coop into it as an old setup did, at any place
// of any event, then remove them. The result is the settings object as it was, with each key
// in its place. (An empty hooks object of the user is not kept: removeHooks cannot tell it
// from the one that an old setup made.)
func TestRemoveHooksUndoesAnOldSetup(t *testing.T) {
	events := []string{"PreToolUse", "PostToolUse", "PostToolUseFailure", "UserPromptSubmit", "Stop", "SessionStart"}
	others := []string{"/bin/tokensave", "jq -re .x", "/other/tool hook pretool", "coop-lookalike --hook"}
	rapid.Check(t, func(rt *rapid.T) {
		// The settings of the user: other keys, and maybe hooks of other tools.
		var orig []member
		var withCoop []member
		seen := map[string]bool{}
		n := rapid.IntRange(0, 4).Draw(rt, "keys")
		hooksAt := rapid.IntRange(-1, n).Draw(rt, "hooksAt")
		coopHooks := func(user map[string][]any, order []string) json.RawMessage {
			// Add the entries of coop: at a random place of an event of the user, or as a new
			// event at the end.
			var ms []member
			added := map[string]bool{}
			for _, ev := range order {
				list := append([]any{}, user[ev]...)
				if rapid.Bool().Draw(rt, "coop in "+ev) {
					at := rapid.IntRange(0, len(list)).Draw(rt, "at")
					list = append(list[:at], append([]any{oldGroup(fmt.Sprint("/p", at, "/coop"), "pretool")}, list[at:]...)...)
				}
				raw, _ := json.Marshal(list)
				ms = append(ms, member{ev, raw})
				added[ev] = true
			}
			for _, ev := range events {
				if !added[ev] && rapid.Bool().Draw(rt, "new "+ev) {
					raw, _ := json.Marshal([]any{oldGroup("/home/u/.local/bin/coop", rapid.SampledFrom(oldHookArgs).Draw(rt, "arg"))})
					ms = append(ms, member{ev, raw})
				}
			}
			return object(ms)
		}
		for i := 0; i <= n; i++ {
			if i == hooksAt {
				user := map[string][]any{}
				var order []string
				first := rapid.IntRange(0, len(events)-1).Draw(rt, "first")
				for i, ev := range events {
					if i == first || rapid.Bool().Draw(rt, "user "+ev) {
						for range rapid.IntRange(1, 3).Draw(rt, "groups") {
							cmd := rapid.SampledFrom(others).Draw(rt, "cmd")
							user[ev] = append(user[ev], map[string]any{"matcher": "", "hooks": []any{map[string]any{"type": "command", "command": cmd}}})
						}
						order = append(order, ev)
					}
				}
				var ms []member
				for _, ev := range order {
					raw, _ := json.Marshal(user[ev])
					ms = append(ms, member{ev, raw})
				}
				orig = append(orig, member{"hooks", object(ms)})
				withCoop = append(withCoop, member{"hooks", coopHooks(user, order)})
				seen["hooks"] = true
				continue
			}
			key := rapid.StringMatching(`[a-z]{1,8}`).Draw(rt, "key")
			if seen[key] || key == "hooks" {
				continue
			}
			seen[key] = true
			raw, _ := json.Marshal(jsonValue(2).Draw(rt, "value"))
			orig = append(orig, member{key, raw})
			withCoop = append(withCoop, member{key, raw})
		}
		if !seen["hooks"] {
			if h := coopHooks(nil, nil); string(h) != "{}" {
				withCoop = append(withCoop, member{"hooks", h})
			}
		}
		in := object(withCoop)
		out, _, err := removeHooks(in)
		if err != nil {
			rt.Fatalf("err %v\nin %s", err, in)
		}
		want := object(orig)
		if !sameJSON(out, want) || !reflect.DeepEqual(keysOf(rt, out), keysOf(rt, want)) {
			rt.Fatalf("\nin   %s\nout  %s\nwant %s", in, out, want)
		}
		if strings.Contains(string(out), "' hook ") {
			rt.Fatalf("an entry of coop is left: %s", out)
		}
	})
}

func TestRemoveHookFileKeepsTheModeAndTheRest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	// No file: nothing to do, and no file is made.
	if changed, err := removeHookFile(path); err != nil || changed {
		t.Fatalf("changed %v err %v", changed, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a file was made: %v", err)
	}
	if err := os.WriteFile(path, []byte(userSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if changed, err := removeHookFile(path); err != nil || !changed {
		t.Fatalf("changed %v err %v", changed, err)
	}
	info, _ := os.Stat(path)
	now, _ := os.ReadFile(path)
	// Windows has no Unix file modes: only the text is checked there.
	if (info.Mode().Perm() != 0o600 && runtime.GOOS != "windows") || strings.Contains(string(now), " hook ") || !strings.Contains(string(now), "verify-done.sh") {
		t.Fatalf("mode %v\n%s", info.Mode().Perm(), now)
	}
	if changed, err := removeHookFile(path); err != nil || changed {
		t.Fatalf("second run: changed %v err %v", changed, err)
	}
	// A file that is not an object stays as it is.
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte("not json"), 0o644)
	if _, err := removeHookFile(bad); err == nil {
		t.Fatal("no error for a file that is not JSON")
	}
	if text, _ := os.ReadFile(bad); string(text) != "not json" {
		t.Fatalf("the bad file changed: %q", text)
	}
	// No temporary file is left.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".settings-") {
			t.Fatalf("left over: %s", e.Name())
		}
	}
}
