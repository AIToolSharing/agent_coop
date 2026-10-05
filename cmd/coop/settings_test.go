package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

const exeA, exeB = "/home/u/.local/bin/coop", "/opt/coop/coop"

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

// The settings of this user on 2026-10-03, in short: hooks of other tools, and other keys.
const userSettings = `{
  "model": "opus",
  "hooks": {
    "SessionStart": [{"hooks": [{"type": "command", "command": "bash '/Users/u/.claude/hooks/herdr-agent-state.sh' session"}]}],
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "jq -re '.tool_input.command'"}]},
      {"hooks": [{"type": "command", "command": "/Users/u/.cargo/bin/tokensave"}]}
    ],
    "Stop": [{"hooks": [{"type": "command", "command": "$HOME/.claude/hooks/verify-done.sh"}]}]
  },
  "permissions": {"allow": ["Bash(ls:*)"]},
  "env": {"A": "1"}
}`

func TestInstallHookAddsOneEntryAndKeepsTheRest(t *testing.T) {
	out, changed, err := installHook([]byte(userSettings), exeA)
	if err != nil || !changed {
		t.Fatalf("changed %v err %v", changed, err)
	}
	if !hookInstalled(out, exeA) || hookInstalled(out, exeB) || hookInstalled([]byte(userSettings), exeA) {
		t.Fatalf("hookInstalled is wrong for\n%s", out)
	}
	if got := keysOf(t, out); strings.Join(got, ",") != "model,hooks,permissions,env" {
		t.Fatalf("top-level keys %v", got)
	}
	top, _ := members(out)
	if got := keysOf(t, get(top, "hooks")); strings.Join(got, ",") != "SessionStart,PreToolUse,Stop,PostToolUse,PostToolUseFailure,UserPromptSubmit" {
		t.Fatalf("hook events %v", got)
	}
	var groups []json.RawMessage
	hooks, _ := members(get(top, "hooks"))
	if err := json.Unmarshal(get(hooks, "PreToolUse"), &groups); err != nil || len(groups) != 3 {
		t.Fatalf("PreToolUse: %v %s", err, get(hooks, "PreToolUse"))
	}
	want, _ := json.Marshal(gateGroup(exeA))
	if !sameJSON(groups[2], want) || !strings.Contains(string(groups[0]), "jq -re") || !strings.Contains(string(groups[1]), "tokensave") {
		t.Fatalf("PreToolUse entries %s", groups)
	}
	// A second run changes nothing; the bytes are the same.
	again, changed, err := installHook(out, exeA)
	if err != nil || changed || !bytes.Equal(again, out) {
		t.Fatalf("second run: changed %v err %v", changed, err)
	}
	// Another path of the binary replaces the hook: one gate hook, not two.
	moved, changed, err := installHook(out, exeB)
	if err != nil || !changed || !hookInstalled(moved, exeB) || hookInstalled(moved, exeA) || strings.Count(string(moved), "hook pretool") != 1 {
		t.Fatalf("moved binary: changed %v err %v\n%s", changed, err, moved)
	}
}

func TestInstallHookIntoNoFileAndRefusesWhatIsNotAnObject(t *testing.T) {
	for _, empty := range []string{"", "  \n", "{}"} {
		out, changed, err := installHook([]byte(empty), exeA)
		if err != nil || !changed || !hookInstalled(out, exeA) || !bytes.HasSuffix(out, []byte("}\n")) {
			t.Fatalf("%q: changed %v err %v out %s", empty, changed, err, out)
		}
	}
	for _, bad := range []string{"[]", `"text"`, "{", `{"hooks": []}`, `{"hooks": {"PreToolUse": {}}}`, `{} {}`} {
		if out, _, err := installHook([]byte(bad), exeA); err == nil {
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

// For any settings object: the hook is there after the install, a second install changes
// nothing, every other key keeps its place and its value, and the other PreToolUse entries
// keep their order before the new one.
func TestInstallHookKeepsAnySettings(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		var top []member
		seen := map[string]bool{}
		add := func(key string, v any) {
			if seen[key] {
				return
			}
			seen[key] = true
			raw, _ := json.Marshal(v)
			top = append(top, member{key, raw})
		}
		n := rapid.IntRange(0, 4).Draw(rt, "keys")
		hooksAt := rapid.IntRange(-1, n).Draw(rt, "hooksAt")
		var before []any
		for i := 0; i <= n; i++ {
			if i == hooksAt {
				hooks := map[string]any{}
				if rapid.Bool().Draw(rt, "pre") {
					k := rapid.IntRange(0, 3).Draw(rt, "groups")
					for j := 0; j < k; j++ {
						cmd := rapid.SampledFrom([]string{"/bin/tokensave", "jq -re .x", "'/old/coop' hook pretool", "/other/tool hook pretool"}).Draw(rt, "cmd")
						before = append(before, map[string]any{"matcher": "", "hooks": []any{map[string]any{"type": "command", "command": cmd}}})
					}
					hooks["PreToolUse"] = before
				}
				if rapid.Bool().Draw(rt, "stop") {
					hooks["Stop"] = []any{}
				}
				add("hooks", hooks)
				continue
			}
			add(rapid.StringMatching(`[a-z]{1,8}`).Draw(rt, "key"), jsonValue(2).Draw(rt, "value"))
		}
		in := object(top)
		out, changed, err := installHook(in, exeA)
		if err != nil || !changed || !hookInstalled(out, exeA) {
			rt.Fatalf("changed %v err %v\nin  %s\nout %s", changed, err, in, out)
		}
		if again, changed, err := installHook(out, exeA); err != nil || changed || !bytes.Equal(again, out) {
			rt.Fatalf("second install: changed %v err %v", changed, err)
		}
		got, _ := members(out)
		wantKeys := keysOf(rt, in)
		if !seen["hooks"] {
			wantKeys = append(wantKeys, "hooks")
		}
		if !reflect.DeepEqual(keysOf(rt, out), wantKeys) {
			rt.Fatalf("keys %v, want %v", keysOf(rt, out), wantKeys)
		}
		for _, m := range top {
			if m.key != "hooks" && !sameJSON(get(got, m.key), m.value) {
				rt.Fatalf("key %s changed: %s -> %s", m.key, m.value, get(got, m.key))
			}
		}
		// The entries of other tools stay, in order; an old gate hook of coop goes.
		var want []any
		for _, g := range before {
			if cmd := g.(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string); cmd != "'/old/coop' hook pretool" {
				want = append(want, g)
			}
		}
		var ours any
		raw, _ := json.Marshal(gateGroup(exeA))
		_ = json.Unmarshal(raw, &ours)
		want = append(want, ours)
		hooks, _ := members(get(got, "hooks"))
		var have []any
		if err := json.Unmarshal(get(hooks, "PreToolUse"), &have); err != nil || !reflect.DeepEqual(have, want) {
			rt.Fatalf("PreToolUse\n got %v\nwant %v", have, want)
		}
	})
}

func TestInstallHookFileKeepsACopyAndTheMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "settings.json")
	// No file yet: the file and its directory are made; there is nothing to copy.
	if changed, err := installHookFile(path, exeA); err != nil || !changed {
		t.Fatalf("changed %v err %v", changed, err)
	}
	if _, err := os.Stat(path + ".before-coop"); !os.IsNotExist(err) {
		t.Fatalf("a copy of no file: %v", err)
	}
	// A file of the user, mode 0600: the copy holds the old text, the mode stays.
	if err := os.WriteFile(path, []byte(userSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if changed, err := installHookFile(path, exeA); err != nil || !changed {
		t.Fatalf("changed %v err %v", changed, err)
	}
	info, _ := os.Stat(path)
	copyText, _ := os.ReadFile(path + ".before-coop")
	now, _ := os.ReadFile(path)
	// Windows has no Unix file modes: only the copy and the hook are checked there.
	if (info.Mode().Perm() != 0o600 && runtime.GOOS != "windows") || string(copyText) != userSettings || !hookInstalled(now, exeA) {
		t.Fatalf("mode %v, copy equal %v, installed %v", info.Mode().Perm(), string(copyText) == userSettings, hookInstalled(now, exeA))
	}
	// A second run changes nothing. A later change keeps the first copy.
	if changed, err := installHookFile(path, exeA); err != nil || changed {
		t.Fatalf("second run: changed %v err %v", changed, err)
	}
	if changed, err := installHookFile(path, exeB); err != nil || !changed {
		t.Fatalf("moved binary: changed %v err %v", changed, err)
	}
	if again, _ := os.ReadFile(path + ".before-coop"); string(again) != userSettings {
		t.Fatal("the copy of the first file was replaced")
	}
	// A file that is not an object stays as it is.
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte("not json"), 0o644)
	if _, err := installHookFile(bad, exeA); err == nil {
		t.Fatal("no error for a file that is not JSON")
	}
	if text, _ := os.ReadFile(bad); string(text) != "not json" {
		t.Fatalf("the bad file changed: %q", text)
	}
	// No temporary file is left.
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".settings-") {
			t.Fatalf("left over: %s", e.Name())
		}
	}
}

// A machine with coop v0.4.0 has only the gate hook in its settings. The next `coop setup`
// adds the hooks that report what the agent does, and leaves one gate hook. Until then,
// `coop claude` gives the hooks that the file does not have with --settings, and no others:
// a hook that is there two times would run two times.
func TestHooksOfAnOlderSetupAreCompleted(t *testing.T) {
	old := `{"hooks":{"PreToolUse":[{"matcher":"","hooks":[{"type":"command","command":"'/home/u/.local/bin/coop' hook pretool","timeout":30}]}]}}`
	missing := hooksMissing([]byte(old), exeA)
	var events []string
	for _, e := range missing {
		events = append(events, e.event)
	}
	if strings.Join(events, ",") != "PostToolUse,PostToolUseFailure,UserPromptSubmit,Stop" || !hookInstalled([]byte(old), exeA) {
		t.Fatalf("missing %v, gate hook installed %v", events, hookInstalled([]byte(old), exeA))
	}
	if s := hookSettings(exeA, missing); strings.Contains(s, "PreToolUse") || strings.Count(s, " hook ") != 4 {
		t.Fatalf("settings for coop claude: %s", s)
	}
	out, changed, err := installHook([]byte(old), exeA)
	if err != nil || !changed || len(hooksMissing(out, exeA)) != 0 || strings.Count(string(out), "hook pretool") != 1 {
		t.Fatalf("changed %v err %v\n%s", changed, err, out)
	}
	// No settings: each hook is missing. The binary at another path: each hook is replaced.
	if n := len(hooksMissing(nil, exeA)); n != len(hookEvents) {
		t.Fatalf("%d hooks missing with no settings, want %d", n, len(hookEvents))
	}
	moved, changed, err := installHook(out, exeB)
	if err != nil || !changed || len(hooksMissing(moved, exeB)) != 0 || strings.Contains(string(moved), exeA) {
		t.Fatalf("moved binary: changed %v err %v\n%s", changed, err, moved)
	}
	// The Stop hook of another tool stays, before the hook of coop.
	withStop, _, err := installHook([]byte(userSettings), exeA)
	if err != nil || !strings.Contains(string(withStop), "verify-done.sh") || strings.Index(string(withStop), "verify-done.sh") > strings.Index(string(withStop), "hook stop") {
		t.Fatalf("err %v\n%s", err, withStop)
	}
}
