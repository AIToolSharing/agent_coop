package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
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
	res, err := http.Get("http://" + addr + "/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("GET /openapi.json: %d", res.StatusCode)
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

func TestServeRefusesAnArgument(t *testing.T) {
	var errOut bytes.Buffer
	if code := serve(context.Background(), []string{"extra"}, &errOut); code != 2 {
		t.Fatalf("code %d", code)
	}
}

func TestAdminTokens(t *testing.T) {
	data := t.TempDir()
	call := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := run(append([]string{"admin"}, args...), &out, &errOut)
		return code, out.String(), errOut.String()
	}
	code, out, _ := call("token", "add", "--data", data, "mac-1")
	if code != 0 || !strings.HasPrefix(out, "mac-1.") || len(strings.TrimSpace(out)) < 40 {
		t.Fatalf("add: %d %q", code, out)
	}
	if code, out, _ := call("token", "add", "--operator", "--data", data, "me"); code != 0 || !strings.HasPrefix(out, "me.") {
		t.Fatalf("add operator: %d %q", code, out)
	}
	code, out, _ = call("token", "list", "--data", data)
	if code != 0 || !strings.Contains(out, "mac-1\tmachine\t") || !strings.Contains(out, "me\toperator\t") || !strings.Contains(out, "\tvalid\n") {
		t.Fatalf("list: %d %q", code, out)
	}
	if code, _, errOut := call("token", "revoke", "--data", data, "mac-1"); code != 0 || !strings.Contains(errOut, "revoked mac-1") {
		t.Fatalf("revoke: %d %q", code, errOut)
	}
	if code, _, errOut := call("token", "revoke", "--data", data, "mac-1"); code != 1 || !strings.Contains(errOut, "no valid token") {
		t.Fatalf("revoke twice: %d %q", code, errOut)
	}
	if _, out, _ := call("token", "list", "--data", data); !strings.Contains(out, "mac-1\tmachine\tcreated ") || !strings.Contains(out, "\trevoked ") {
		t.Fatalf("list after revoke: %q", out)
	}
	if code, _, _ := call("token", "add", "--data", data, "Bad Name"); code != 1 {
		t.Fatalf("bad name: %d", code)
	}
	for _, bad := range [][]string{{}, {"token"}, {"token", "add", "--data", data}, {"token", "list", "--data", data, "x"}, {"session", "list"}} {
		if code, _, errOut := call(bad...); code != 2 || !strings.Contains(errOut, "usage:") {
			t.Fatalf("%v: %d %q", bad, code, errOut)
		}
	}
}
