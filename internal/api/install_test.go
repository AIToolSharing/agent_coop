package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AIToolSharing/agent_coop/internal/wire"
	"pgregory.net/rapid"
)

// get does one GET with the token, or with no Authorization header when token is "".
func get(t *testing.T, url, token string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, res.Header, body
}

// A device gets the install script before it has a token: the script holds no secret.
func TestTheHubServesTheInstallScriptWithoutAToken(t *testing.T) {
	h := startHub(t, limits{}, options{autoCreate: true})
	status, header, body := get(t, h.base+"/install.sh", "")
	if status != 200 || !strings.HasPrefix(string(body), "#!/bin/sh\n") {
		t.Fatalf("GET /install.sh: %d %.60q", status, body)
	}
	if ct := header.Get("Content-Type"); !strings.HasPrefix(ct, "text/") {
		t.Errorf("content type %q", ct)
	}
	// The script asks the hub for the binary of the device, with the token.
	for _, want := range []string{"file=coop-$os-$arch", "$hub/dl/$file", "Authorization: Bearer $token", "login", "setup", "doctor"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the script has no %q", want)
		}
	}
}

// coop upgrade and coop doctor compare the version of the device with the version of the hub.
func TestWhoamiGivesTheVersionOfTheHub(t *testing.T) {
	h := startHub(t, limits{}, options{autoCreate: true, version: "v9.8.7"})
	status, _, body := get(t, h.base+"/v1/whoami", h.token("mac"))
	var me map[string]string
	if err := json.Unmarshal(body, &me); err != nil || status != 200 {
		t.Fatalf("whoami: %d %s", status, body)
	}
	if got, ok := me["version"]; !ok || got != "v9.8.7" {
		t.Fatalf("whoami %v: want version v9.8.7", me)
	}
}

// distDir makes a directory of release binaries: one for Linux, one for Windows.
func distDir(t *testing.T) (dir string, files map[string]string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "dist")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files = map[string]string{"coop-linux-amd64": "the linux binary", "coop-windows-amd64.exe": "the windows binary"}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// What a device must never get: a file next to the directory, a file of another name in
	// it, and a directory with the name of a binary.
	for _, path := range []string{filepath.Join(dir, "..", "coop.db"), filepath.Join(dir, "notes.txt")} {
		if err := os.WriteFile(path, []byte("secret"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "coop-a-directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, files
}

// A device downloads the binary of its platform from the hub, with its token.
func TestTheHubGivesItsBinariesToEachValidToken(t *testing.T) {
	dir, files := distDir(t)
	h := startHub(t, limits{}, options{autoCreate: true, dist: dir})
	for _, role := range []string{wire.RoleMachine, wire.RoleOperator, wire.RoleOrchestrator, wire.RoleReporter} {
		status, header, body := get(t, h.base+"/dl/coop-linux-amd64", h.roleToken("t-"+role, role))
		if status != 200 || string(body) != files["coop-linux-amd64"] || header.Get("Content-Type") != "application/octet-stream" {
			t.Errorf("%s token: %d %q %q", role, status, body, header.Get("Content-Type"))
		}
	}
	tok := h.token("mac")
	if status, _, body := get(t, h.base+"/dl/coop-windows-amd64.exe", tok); status != 200 || string(body) != files["coop-windows-amd64.exe"] {
		t.Errorf("the windows binary: %d %q", status, body)
	}
	// No token and a token that the hub does not know: no binary.
	for _, bad := range []string{"", "nope.x"} {
		if status, _, body := get(t, h.base+"/dl/coop-linux-amd64", bad); status != 401 || strings.Contains(string(body), "binary") {
			t.Errorf("token %q: %d %s", bad, status, body)
		}
	}
	// The hub has no binary for this platform: the answer names the file.
	status, _, body := get(t, h.base+"/dl/coop-darwin-arm64", tok)
	if status != 404 || !strings.Contains(string(body), "coop-darwin-arm64") {
		t.Errorf("a binary that the hub does not have: %d %s", status, body)
	}
	if status, header, _ := httpDo(t, "POST", h.base+"/dl/coop-linux-amd64", tok); status != 405 || header.Get("Allow") != "GET, HEAD" {
		t.Errorf("POST: %d, Allow %q", status, header.Get("Allow"))
	}
	// A hub with no directory gives no binary.
	none := startHub(t, limits{}, options{autoCreate: true})
	if status, _, _ := get(t, none.base+"/dl/coop-linux-amd64", none.token("mac")); status != 404 {
		t.Errorf("a hub with no directory: %d", status)
	}
}

func httpDo(t *testing.T, method, url, token string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, body
}

// The invariant of /dl/: whatever the path, the hub gives the bytes of one of its release
// binaries, or nothing. No path reaches another file.
func TestDownloadPathsReachOnlyTheBinaries(t *testing.T) {
	dir, files := distDir(t)
	h := startHub(t, limits{}, options{autoCreate: true, dist: dir})
	tok := h.token("mac")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	part := rapid.SampledFrom([]string{
		"coop-linux-amd64", "coop-windows-amd64.exe", "coop-a-directory", "notes.txt", "coop.db",
		"..", ".", "/", "%2F", "%2f", "%2E%2E", "%5C", "\\", "%00", "?", "#", "coop-", "-", ".exe", "a", "9",
	})
	rapid.Check(t, func(rt *rapid.T) {
		tail := strings.Join(rapid.SliceOfN(part, 0, 5).Draw(rt, "parts"), "")
		req, err := http.NewRequest(http.MethodGet, h.base+"/dl/"+tail, nil)
		if err != nil {
			rt.Skip() // not a URL that a client can send
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := client.Do(req)
		if err != nil {
			rt.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		// The path as the hub reads it: without the query and the fragment.
		if want, ok := files[strings.TrimPrefix(req.URL.Path, "/dl/")]; ok {
			if res.StatusCode != 200 || string(body) != want {
				rt.Fatalf("/dl/%s: %d %q, want the binary", tail, res.StatusCode, body)
			}
			return
		}
		if res.StatusCode == 200 || strings.Contains(string(body), "secret") {
			rt.Fatalf("/dl/%s: %d %q, want no file", tail, res.StatusCode, body)
		}
	})
}
