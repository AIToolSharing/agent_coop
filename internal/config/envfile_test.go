package config_test

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AIToolSharing/agent_coop/internal/config"
	"pgregory.net/rapid"
)

func TestParseEnvExamples(t *testing.T) {
	cases := []struct {
		name string
		text string
		want map[string]string
	}{
		{"blank lines and comments go", "\n  \n# COOP_URL=x\n  # c\nA=1\n", map[string]string{"A": "1"}},
		{"a line without = goes", "JUST_A_WORD\nA=1", map[string]string{"A": "1"}},
		{"a line that starts with = goes", "=v\n =w\nA=1", map[string]string{"A": "1"}},
		{"key and value are trimmed", "  A  =  b c \t\n", map[string]string{"A": "b c"}},
		{"the first = splits", "A=b=c", map[string]string{"A": "b=c"}},
		{"an empty value stays", "A=", map[string]string{"A": ""}},
		{"the last line for a key wins", "A=1\nA=2", map[string]string{"A": "2"}},
		{"CRLF lines", "A=1\r\nB=2\r\n", map[string]string{"A": "1", "B": "2"}},
		{"double quotes go", `A="x y"`, map[string]string{"A": "x y"}},
		{"single quotes go", `A='x'`, map[string]string{"A": "x"}},
		{"only one pair of quotes goes", `A='"x"'`, map[string]string{"A": `"x"`}},
		{"empty quotes give an empty value", `A=""`, map[string]string{"A": ""}},
		{"quotes that differ stay", `A="x'`, map[string]string{"A": `"x'`}},
		{"one quote stays", `A="`, map[string]string{"A": `"`}},
		{"an inner quote stays", `A=x"y"`, map[string]string{"A": `x"y"`}},
		{"quotes stay around a carriage return", "A=\"a\rb\"", map[string]string{"A": "\"a\rb\""}},
		{"quotes stay around a line separator", "A=\"a\u2028b\"", map[string]string{"A": "\"a\u2028b\""}},
		{"a byte order mark goes", "\uFEFFA=1\n", map[string]string{"A": "1"}},
		{"a byte order mark before a comment goes", "\uFEFF# c=1\nA=1", map[string]string{"A": "1"}},
		{"U+0085 is not white space in JavaScript", "\u0085A=x\u0085", map[string]string{"\u0085A": "x\u0085"}},
		{"a space inside the key stays", "A B=1", map[string]string{"A B": "1"}},
		{"no lines", "", map[string]string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := config.ParseEnv(c.text); !maps.Equal(got, c.want) {
				t.Fatalf("ParseEnv(%q) = %q, want %q", c.text, got, c.want)
			}
		})
	}
}

func TestFormatEnvWritesSortedLines(t *testing.T) {
	got := config.FormatEnv(map[string]string{"COOP_URL": "https://x", "COOP_TOKEN": "m.s", "A": ""})
	want := "A=\nCOOP_TOKEN=m.s\nCOOP_URL=https://x\n"
	if got != want {
		t.Fatalf("FormatEnv = %q, want %q", got, want)
	}
	if got := config.FormatEnv(nil); got != "" {
		t.Fatalf("FormatEnv(nil) = %q", got)
	}
}

// envKey is a key as a shell writes an environment variable name.
var envKey = rapid.StringMatching(`^[A-Z_][A-Z0-9_]{0,20}$`)

// envValue is a value that reads back unchanged: printable ASCII, no space at either end, and
// not in a pair of quotes (ParseEnv removes such a pair).
var envValue = rapid.StringMatching(`^[ -~]{0,40}$`).Filter(func(v string) bool {
	if strings.TrimSpace(v) != v {
		return false
	}
	return len(v) < 2 || (v[0] != '"' && v[0] != '\'') || v[len(v)-1] != v[0]
})

func TestParseFormatRoundTrip(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		m := rapid.MapOf(envKey, envValue).Draw(rt, "m")
		text := config.FormatEnv(m)
		if got := config.ParseEnv(text); !maps.Equal(got, m) {
			rt.Fatalf("ParseEnv(%q) = %q, want %q", text, got, m)
		}
	})
}

func TestParseEnvKeysAreNeverEmptyOrSplit(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		text := rapid.String().Draw(rt, "text")
		for k, v := range config.ParseEnv(text) {
			if k == "" || strings.ContainsAny(k, "=\n") || strings.Contains(v, "\n") {
				rt.Fatalf("ParseEnv(%q) gave key %q value %q", text, k, v)
			}
		}
	})
}

func TestDefaultEnvFileIsUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := config.DefaultEnvFile(), filepath.Join(home, ".config", "coop", "env"); got != want {
		t.Fatalf("DefaultEnvFile() = %q, want %q", got, want)
	}
	// Without HOME the path is empty, not relative to the working directory.
	t.Setenv("HOME", "")
	if got := config.DefaultEnvFile(); got != "" {
		t.Fatalf("DefaultEnvFile() without HOME = %q, want empty", got)
	}
}

func writeFile(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
	// Set the mode again: WriteFile applies the umask.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func perm(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestReadEnvFileRefusesAFileThatOthersCanUse(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o602, 0o601, 0o604} {
		path := filepath.Join(t.TempDir(), "env")
		writeFile(t, path, "COOP_TOKEN=m.s\n", mode)
		var warnings []string
		got := config.ReadEnvFile(path, func(m string) { warnings = append(warnings, m) })
		if len(got) != 0 {
			t.Errorf("mode %o: read %q", mode, got)
		}
		want := path + " is readable by other users; run: chmod 600 " + path
		if len(warnings) != 1 || warnings[0] != want {
			t.Errorf("mode %o: warnings %q, want [%q]", mode, warnings, want)
		}
	}
}

func TestReadEnvFileReadsAPrivateFile(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o400, 0o700} {
		path := filepath.Join(t.TempDir(), "env")
		writeFile(t, path, "COOP_URL=https://x\nCOOP_TOKEN=m.s\n", mode)
		var warnings []string
		got := config.ReadEnvFile(path, func(m string) { warnings = append(warnings, m) })
		want := map[string]string{"COOP_URL": "https://x", "COOP_TOKEN": "m.s"}
		if !maps.Equal(got, want) || len(warnings) != 0 {
			t.Errorf("mode %o: got %q, warnings %q", mode, got, warnings)
		}
	}
}

func TestReadEnvFileWithoutAFileIsEmptyAndQuiet(t *testing.T) {
	var warnings []string
	got := config.ReadEnvFile(filepath.Join(t.TempDir(), "none"), func(m string) { warnings = append(warnings, m) })
	if got == nil || len(got) != 0 || len(warnings) != 0 {
		t.Fatalf("got %#v, warnings %q", got, warnings)
	}
}

func TestUpdateEnvFileCreatesAPrivateDirectoryAndFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg")
	path := filepath.Join(dir, "env")
	if err := config.UpdateEnvFile(path, map[string]string{"COOP_URL": "https://x", "COOP_TOKEN": "laptop.s"}); err != nil {
		t.Fatal(err)
	}
	if got, want := readFile(t, path), "COOP_TOKEN=laptop.s\nCOOP_URL=https://x\n"; got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
	if p := perm(t, dir); p != 0o700 {
		t.Errorf("directory mode %o, want 700", p)
	}
	if p := perm(t, path); p != 0o600 {
		t.Errorf("file mode %o, want 600", p)
	}
	// The shim reads back what login wrote.
	got := config.ReadEnvFile(path, func(m string) { t.Errorf("warning: %s", m) })
	if want := map[string]string{"COOP_URL": "https://x", "COOP_TOKEN": "laptop.s"}; !maps.Equal(got, want) {
		t.Fatalf("read back %q", got)
	}
}

func TestUpdateEnvFileKeepsTheOtherKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env")
	writeFile(t, path, "# a comment\nCOOP_URL=https://old\nCOOP_TOKEN=m.s\n", 0o600)
	if err := config.UpdateEnvFile(path, map[string]string{"COOP_URL": "https://x", "COOP_OPERATOR_TOKEN": "o.s"}); err != nil {
		t.Fatal(err)
	}
	want := "COOP_OPERATOR_TOKEN=o.s\nCOOP_TOKEN=m.s\nCOOP_URL=https://x\n"
	if got := readFile(t, path); got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestUpdateEnvFileMakesAnOpenFilePrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env")
	writeFile(t, path, "COOP_URL=https://x\n", 0o644)
	if err := config.UpdateEnvFile(path, map[string]string{"COOP_TOKEN": "m.s"}); err != nil {
		t.Fatal(err)
	}
	if p := perm(t, path); p != 0o600 {
		t.Fatalf("file mode %o, want 600", p)
	}
	if got, want := readFile(t, path), "COOP_TOKEN=m.s\nCOOP_URL=https://x\n"; got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestUpdateEnvFileRefusesAPairThatDoesNotReadBack(t *testing.T) {
	bad := []map[string]string{
		{"COOP_TOKEN": "m.s\nCOOP_URL=https://evil"},
		{"COOP_TOKEN": " m.s"},
		{"COOP_TOKEN": `"m.s"`},
		{"": "x"},
		{"A=B": "x"},
		{"#A": "x"},
		{"A\nB": "x"},
	}
	for _, values := range bad {
		path := filepath.Join(t.TempDir(), "env")
		writeFile(t, path, "COOP_URL=https://x\n", 0o600)
		if err := config.UpdateEnvFile(path, values); err == nil {
			t.Errorf("%q: no error", values)
		}
		if got := readFile(t, path); got != "COOP_URL=https://x\n" {
			t.Errorf("%q: file changed to %q", values, got)
		}
	}
}

func TestUpdateEnvFileKeepsAFileItCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file of mode 200")
	}
	path := filepath.Join(t.TempDir(), "env")
	writeFile(t, path, "COOP_OPERATOR_TOKEN=o.s\n", 0o200)
	if err := config.UpdateEnvFile(path, map[string]string{"COOP_TOKEN": "m.s"}); err == nil {
		t.Fatal("no error")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != "COOP_OPERATOR_TOKEN=o.s\n" {
		t.Fatalf("file changed to %q", got)
	}
}
