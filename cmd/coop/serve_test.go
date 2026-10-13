package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestServeListensAndStopsWithTheContext(t *testing.T) {
	data := t.TempDir()
	ready := make(chan string, 1)
	serveReady = func(addr string) { ready <- addr }
	t.Cleanup(func() { serveReady = nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var errOut bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- serve(ctx, []string{"--listen", "127.0.0.1:0", "--data", data}, &errOut) }()
	var addr string
	select {
	case addr = <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not listen")
	}
	res, err := http.Get("http://" + addr + "/v1/whoami")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("GET /v1/whoami with no token: %d", res.StatusCode)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("serve ended with %d: %s", code, errOut.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop")
	}
	if _, err := os.Stat(filepath.Join(data, dbName)); err != nil {
		t.Fatalf("no database: %v", err)
	}
	if !strings.Contains(errOut.String(), "listening on "+addr) {
		t.Fatalf("stderr: %q", errOut.String())
	}
}

// secretOf reads the secret that a hub made in its data directory.
func secretOf(t *testing.T, data string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(data, secretName))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// The hub makes its secret at the first start and keeps it: the file <data>/secret, for the
// owner only. Every machine presents it behind its name. A file that holds no secret stops
// the hub.
func TestServeMakesItsSecretOnceAndKeepsIt(t *testing.T) {
	data := t.TempDir()
	start := func() (secret string, stderr string) {
		t.Helper()
		ready := make(chan string, 1)
		serveReady = func(addr string) { ready <- addr }
		t.Cleanup(func() { serveReady = nil })
		ctx, cancel := context.WithCancel(context.Background())
		var errOut bytes.Buffer
		done := make(chan int, 1)
		go func() { done <- serve(ctx, []string{"--listen", "127.0.0.1:0", "--data", data}, &errOut) }()
		addr := <-ready
		secret = secretOf(t, data)
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/v1/whoami", nil)
		req.Header.Set("Authorization", "Bearer laptop."+secret)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || !strings.Contains(string(body), `"machine":"laptop"`) {
			t.Fatalf("whoami with the secret: %d %s", res.StatusCode, body)
		}
		cancel()
		<-done
		return secret, errOut.String()
	}
	first, stderr := start()
	if len(first) < 40 || strings.ContainsAny(first, ".+/=") || !strings.Contains(stderr, "made "+filepath.Join(data, secretName)) {
		t.Fatalf("first start: secret %q, stderr %q", first, stderr)
	}
	if info, err := os.Stat(filepath.Join(data, secretName)); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("secret file: %v %v", info, err)
	}
	if again, stderr := start(); again != first || strings.Contains(stderr, "made ") {
		t.Fatalf("second start: secret %q (first %q), stderr %q", again, first, stderr)
	}
	if err := os.WriteFile(filepath.Join(data, secretName), []byte("has.a.dot\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	if code := serve(context.Background(), []string{"--listen", "127.0.0.1:0", "--data", data}, &errOut); code != 1 || !strings.Contains(errOut.String(), "does not hold a secret") {
		t.Fatalf("a bad secret file: code %d, stderr %q", code, errOut.String())
	}
}

func TestServeRefusesAnArgument(t *testing.T) {
	var errOut bytes.Buffer
	if code := serve(context.Background(), []string{"extra"}, &errOut); code != 2 {
		t.Fatalf("code %d", code)
	}
}

// A device installs coop from its hub: `coop serve` reports its version and gives the release
// binaries of the directory dist in its data directory, or of --dist.
func TestServeGivesItsVersionAndItsBinaries(t *testing.T) {
	for name, flag := range map[string]bool{"dist in the data directory": false, "--dist": true} {
		data := t.TempDir()
		dist := filepath.Join(data, "dist")
		args := []string{"--listen", "127.0.0.1:0", "--data", data}
		if flag {
			dist = t.TempDir()
			args = append(args, "--dist", dist)
		}
		if err := os.MkdirAll(dist, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dist, "coop-linux-amd64"), []byte("the binary"), 0o755); err != nil {
			t.Fatal(err)
		}
		var errOut bytes.Buffer
		ready := make(chan string, 1)
		serveReady = func(addr string) { ready <- addr }
		t.Cleanup(func() { serveReady = nil })
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan int, 1)
		go func() { done <- serve(ctx, args, &errOut) }()
		base := "http://" + <-ready
		token := "mac-1." + secretOf(t, data)
		get := func(path string) (int, string) {
			req, _ := http.NewRequest(http.MethodGet, base+path, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, _ := io.ReadAll(res.Body)
			return res.StatusCode, string(body)
		}
		if status, body := get("/dl/coop-linux-amd64"); status != 200 || body != "the binary" {
			t.Errorf("%s: the binary: %d %q", name, status, body)
		}
		if status, body := get("/v1/whoami"); status != 200 || !strings.Contains(body, `"version":"`+version+`"`) {
			t.Errorf("%s: whoami: %d %s", name, status, body)
		}
		cancel()
		<-done
	}
}
