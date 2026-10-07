package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// standIn is a stand-in for a coop binary of one version: a shell script that answers
// `version`, and that leaves the file <itself>.setup when it runs `setup`.
func standIn(version string) string {
	return "#!/bin/sh\ncase $1 in\nversion) echo 'coop " + version + "' ;;\nsetup) echo ran >\"$0.setup\" ;;\nesac\n"
}

// distHub stands in for a hub of one version that knows the machine token mac.2. It gives
// the files of binaries at /dl/, and counts the downloads.
type distHub struct {
	*httptest.Server
	version   string // "" for a hub of a version that reports none
	binaries  map[string]string
	downloads int
}

func newDistHub(t *testing.T, version string, binary string) *distHub {
	t.Helper()
	h := &distHub{version: version, binaries: map[string]string{}}
	if binary != "" {
		h.binaries[binaryName()] = binary
	}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mac.2" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"unauthorized","message":"bad token"}`))
			return
		}
		name, isBinary := strings.CutPrefix(r.URL.Path, "/dl/")
		switch {
		case r.URL.Path == "/v1/whoami" && h.version == "":
			_, _ = w.Write([]byte(`{"name":"mac","role":"machine"}`))
		case r.URL.Path == "/v1/whoami":
			_, _ = w.Write([]byte(`{"name":"mac","role":"machine","version":"` + h.version + `"}`))
		case isBinary && h.binaries[name] != "":
			h.downloads++
			_, _ = w.Write([]byte(h.binaries[name]))
		case isBinary:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"not_found","message":"the hub has no binary ` + name + `"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(h.Close)
	return h
}

// device makes a home directory that is logged in to the hub, and a coop binary of the
// version "old" at path. Claude Code is there (a stand-in).
func device(t *testing.T, h *distHub, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in binary is a shell script")
	}
	setHome(t, t.TempDir())
	for _, key := range []string{"XDG_CONFIG_HOME", "COOP_URL", "COOP_TOKEN", "COOP_OPERATOR_TOKEN", "COOP_ORCHESTRATOR_TOKEN", "COOP_CERT_SHA256", "COOP_SESSION", "COOP_AGENT", "CLAUDE_PROJECT_DIR", "CLAUDE_CONFIG_DIR"} {
		t.Setenv(key, "")
	}
	t.Chdir(t.TempDir())
	(&fakeClaude{}).install(t, path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(standIn("old")), 0o755); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"login", h.URL, "mac.2"}, &out, &errOut); code != 0 {
		t.Fatalf("login: %s", errOut.String())
	}
}

func upgrade(t *testing.T) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run([]string{"upgrade"}, &out, &errOut)
	return code, out.String(), errOut.String()
}

// onlyTheBinary fails the test when the directory of the binary holds another file: a
// download that did not become the binary must not stay.
func onlyTheBinary(t *testing.T, path string, also ...string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(path) && !strings.Contains(" "+strings.Join(also, " ")+" ", " "+e.Name()+" ") {
			t.Errorf("a file is left next to the binary: %s", e.Name())
		}
	}
}

func TestUpgradeTakesTheBinaryOfTheHub(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bin", "coop")
	h := newDistHub(t, "v2.0.0", standIn("v2.0.0"))
	device(t, h, path)
	code, out, errOut := upgrade(t)
	if code != 0 {
		t.Fatalf("code %d: %s%s", code, out, errOut)
	}
	now, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if string(now) != standIn("v2.0.0") || info.Mode().Perm() != 0o755 {
		t.Fatalf("the binary is %q with mode %v", now, info.Mode().Perm())
	}
	// The new binary ran its setup: the hooks and the skill are those of the new version.
	if _, err := os.Stat(path + ".setup"); err != nil {
		t.Errorf("the new binary did not run setup: %v", err)
	}
	onlyTheBinary(t, path, "coop.setup")
	if h.downloads != 1 || !strings.Contains(out, version) || !strings.Contains(out, "v2.0.0") {
		t.Errorf("%d downloads, stdout %q", h.downloads, out)
	}
}

func TestUpgradeDoesNothingWhenTheVersionsAreEqual(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bin", "coop")
	h := newDistHub(t, version, standIn(version))
	device(t, h, path)
	code, out, errOut := upgrade(t)
	now, _ := os.ReadFile(path)
	if code != 0 || h.downloads != 0 || string(now) != standIn("old") || !strings.Contains(out, "is the version of the hub") {
		t.Fatalf("code %d, %d downloads, stdout %q, stderr %q", code, h.downloads, out, errOut)
	}
}

// Each of these ends with an error that says what to do, and with the old binary in place.
func TestUpgradeKeepsTheOldBinaryWhenItCannotUpgrade(t *testing.T) {
	t.Run("the binary is a build in a clone", func(t *testing.T) {
		clone := t.TempDir()
		if err := os.WriteFile(filepath.Join(clone, "go.mod"), []byte("module x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(clone, "dist", "coop")
		h := newDistHub(t, "v2.0.0", standIn("v2.0.0"))
		device(t, h, path)
		code, _, errOut := upgrade(t)
		if code != 1 || h.downloads != 0 || !strings.Contains(errOut, "make install") {
			t.Errorf("code %d, %d downloads, stderr %q", code, h.downloads, errOut)
		}
		keeps(t, path)
	})
	t.Run("the hub has no binary for this platform", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bin", "coop")
		h := newDistHub(t, "v2.0.0", "")
		device(t, h, path)
		code, _, errOut := upgrade(t)
		if code != 1 || !strings.Contains(errOut, binaryName()) {
			t.Errorf("code %d, stderr %q", code, errOut)
		}
		keeps(t, path)
	})
	t.Run("the hub gives a binary of another version than its own", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bin", "coop")
		h := newDistHub(t, "v2.0.0", standIn("v1.9.0"))
		device(t, h, path)
		code, _, errOut := upgrade(t)
		if code != 1 || !strings.Contains(errOut, "v1.9.0") || !strings.Contains(errOut, "v2.0.0") {
			t.Errorf("code %d, stderr %q", code, errOut)
		}
		keeps(t, path)
	})
	t.Run("the hub gives a file that does not run", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bin", "coop")
		h := newDistHub(t, "v2.0.0", "this is no program")
		device(t, h, path)
		if code, _, errOut := upgrade(t); code != 1 || errOut == "" {
			t.Errorf("code %d, stderr %q", code, errOut)
		}
		keeps(t, path)
	})
	t.Run("the hub is of a version that gives no binaries", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bin", "coop")
		h := newDistHub(t, "", standIn("v2.0.0"))
		device(t, h, path)
		code, _, errOut := upgrade(t)
		if code != 1 || h.downloads != 0 || !strings.Contains(errOut, "upgrade the hub") {
			t.Errorf("code %d, %d downloads, stderr %q", code, h.downloads, errOut)
		}
		keeps(t, path)
	})
	t.Run("the user cannot write the directory of the binary", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root writes every directory")
		}
		path := filepath.Join(t.TempDir(), "bin", "coop")
		h := newDistHub(t, "v2.0.0", standIn("v2.0.0"))
		device(t, h, path)
		if err := os.Chmod(filepath.Dir(path), 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Dir(path), 0o755) })
		code, _, errOut := upgrade(t)
		if code != 1 || !strings.Contains(errOut, filepath.Dir(path)) {
			t.Errorf("code %d, stderr %q", code, errOut)
		}
		keeps(t, path)
	})
	t.Run("the device is not logged in", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("as the other cases")
		}
		setHome(t, t.TempDir())
		for _, key := range []string{"XDG_CONFIG_HOME", "COOP_URL", "COOP_TOKEN", "COOP_OPERATOR_TOKEN", "COOP_ORCHESTRATOR_TOKEN"} {
			t.Setenv(key, "")
		}
		if code, _, errOut := upgrade(t); code != 1 || !strings.Contains(errOut, "coop login") {
			t.Errorf("code %d, stderr %q", code, errOut)
		}
	})
}

// keeps fails the test when the binary at path is not the old one, or a file is next to it.
func keeps(t *testing.T, path string) {
	t.Helper()
	if now, _ := os.ReadFile(path); string(now) != standIn("old") {
		t.Errorf("the binary changed: %q", now)
	}
	onlyTheBinary(t, path)
}

func TestUpgradeTakesNoArgument(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"upgrade", "now"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "usage: coop upgrade") {
		t.Fatalf("code %d, stderr %q", code, errOut.String())
	}
}

// coop doctor says when the device and the hub have different versions, and what to run.
func TestDoctorComparesTheVersionOfTheDeviceWithTheHub(t *testing.T) {
	for hubVersion, want := range map[string]string{
		version:  "ok    coop " + version + ", the version of the hub",
		"v2.0.0": "--    this coop is " + version + ", the hub is v2.0.0: run coop upgrade",
		"":       "--    the hub is older than this coop (" + version + "): upgrade the hub",
	} {
		h := newDistHub(t, hubVersion, "")
		device(t, h, filepath.Join(t.TempDir(), "bin", "coop"))
		var out, errOut bytes.Buffer
		run([]string{"doctor"}, &out, &errOut)
		if !strings.Contains(out.String(), want+"\n") {
			t.Errorf("hub version %q: no line %q in\n%s", hubVersion, want, out.String())
		}
	}
}
