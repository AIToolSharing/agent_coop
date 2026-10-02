package config_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/AIToolSharing/agent_coop/internal/config"
)

// repo makes root/.git and root/pkg, as the TypeScript test of the session command does.
func repo(t *testing.T) (root, sub string) {
	t.Helper()
	root = t.TempDir()
	sub = filepath.Join(root, "pkg")
	mkdir(t, filepath.Join(root, ".git"))
	mkdir(t, sub)
	return root, sub
}

func TestWriteProjectFileWritesIgnoresOnceAndLoadReadsIt(t *testing.T) {
	root, sub := repo(t)
	ignore := filepath.Join(root, ".gitignore")
	writeFile(t, ignore, "node_modules/", 0o644)

	file, changed, err := config.WriteProjectFile(sub, "build-42", "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if file != filepath.Join(sub, ".coop") || changed != ignore {
		t.Fatalf("got %q %q", file, changed)
	}
	if got := readFile(t, file); got != "COOP_SESSION=build-42\nCOOP_AGENT=reviewer\n" {
		t.Fatalf(".coop = %q", got)
	}
	if got := readFile(t, ignore); got != "node_modules/\n.coop\n" {
		t.Fatalf(".gitignore = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, ".coop")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf(".coop at the git root: %v", err)
	}

	// A second run does not add a second line.
	_, changed, err = config.WriteProjectFile(sub, "other", "")
	if err != nil {
		t.Fatal(err)
	}
	if changed != "" || readFile(t, ignore) != "node_modules/\n.coop\n" {
		t.Fatalf("second run: changed %q, .gitignore %q", changed, readFile(t, ignore))
	}
	if got := readFile(t, file); got != "COOP_SESSION=other\n" {
		t.Fatalf(".coop = %q", got)
	}
	// The shim reads it back, also from a directory below that does not exist.
	c := config.Load(map[string]string{}, noCred, noWarn, filepath.Join(sub, "deeper-not-existing"))
	if c.Session != "other" {
		t.Fatalf("session %q", c.Session)
	}
}

func TestWriteProjectFileRefusesABadName(t *testing.T) {
	cases := []struct{ session, agent, msg string }{
		{"Bad Name", "", `"Bad Name" is not a valid session name: a-z, 0-9, _ and -, up to 64`},
		{"s", "Bad!", `"Bad!" is not a valid agent name: a-z, 0-9, _ and -, up to 64`},
		{"s", "all", `"all" is not a valid agent name: a-z, 0-9, _ and -, up to 64`},
		{"s", "operator", `"operator" is not a valid agent name: a-z, 0-9, _ and -, up to 64`},
	}
	for _, c := range cases {
		root, sub := repo(t)
		_, _, err := config.WriteProjectFile(sub, c.session, c.agent)
		if err == nil || err.Error() != c.msg {
			t.Errorf("%q %q: error %v, want %q", c.session, c.agent, err, c.msg)
		}
		for _, p := range []string{filepath.Join(sub, ".coop"), filepath.Join(root, ".gitignore")} {
			if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("%q %q: %s exists", c.session, c.agent, p)
			}
		}
	}
}

func TestWriteProjectFileAddsALineOnlyWhenNoLineIgnoresTheFile(t *testing.T) {
	cases := []struct {
		before, after string
	}{
		{"", ".coop\n"},
		{"a\n", "a\n.coop\n"},
		{"a", "a\n.coop\n"},
		{".coop\n", ".coop\n"},
		{"/.coop\n", "/.coop\n"},
		{"a\n  .coop \r\nb\n", "a\n  .coop \r\nb\n"},
		{".coop/\n", ".coop/\n.coop\n"},
		{"# .coop\n", "# .coop\n.coop\n"},
		{"src/.coop\n", "src/.coop\n.coop\n"},
	}
	for _, c := range cases {
		root, sub := repo(t)
		ignore := filepath.Join(root, ".gitignore")
		writeFile(t, ignore, c.before, 0o644)
		_, changed, err := config.WriteProjectFile(sub, "s", "")
		if err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, ignore); got != c.after {
			t.Errorf("%q: .gitignore = %q, want %q", c.before, got, c.after)
		}
		if want := c.before != c.after; (changed != "") != want {
			t.Errorf("%q: changed %q", c.before, changed)
		}
	}
}

func TestWriteProjectFileCreatesTheGitignore(t *testing.T) {
	root, _ := repo(t)
	_, changed, err := config.WriteProjectFile(root, "s", "")
	if err != nil {
		t.Fatal(err)
	}
	ignore := filepath.Join(root, ".gitignore")
	if changed != ignore || readFile(t, ignore) != ".coop\n" {
		t.Fatalf("changed %q", changed)
	}
}

func TestWriteProjectFileOutsideARepositoryTouchesNoGitignore(t *testing.T) {
	dir := t.TempDir()
	if root, ok := config.FindGitRoot(dir); ok {
		t.Skipf("the temporary directory is in a repository at %s", root)
	}
	file, changed, err := config.WriteProjectFile(dir, "s", "")
	if err != nil || file != filepath.Join(dir, ".coop") || changed != "" {
		t.Fatalf("got %q %q %v", file, changed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf(".gitignore: %v", err)
	}
}

func TestWriteProjectFileReportsAGitignoreItCannotRead(t *testing.T) {
	root, sub := repo(t)
	mkdir(t, filepath.Join(root, ".gitignore"))
	if _, _, err := config.WriteProjectFile(sub, "s", ""); err == nil {
		t.Fatal("no error")
	}
}
