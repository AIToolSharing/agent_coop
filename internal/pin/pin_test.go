package pin_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AIToolSharing/agent_coop/internal/pin"
)

func get(t *testing.T, c *http.Client, url string) (int, error) {
	t.Helper()
	res, err := c.Get(url)
	if err != nil {
		return 0, err
	}
	res.Body.Close()
	return res.StatusCode, nil
}

func TestASelfSignedHubIsTrustedByItsFingerprintOnly(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	sum := sha256.Sum256(srv.Certificate().Raw)
	want := hex.EncodeToString(sum[:])

	plain, err := pin.Client("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := get(t, plain, srv.URL); err == nil || !pin.IsCertError(err) {
		t.Fatalf("without a pin the default verification must refuse a self-signed certificate; got %v", err)
	}
	fp, err := pin.Fingerprint(context.Background(), srv.URL)
	if err != nil || fp != want {
		t.Fatalf("fingerprint %q %v, want %q", fp, err, want)
	}
	pinned, err := pin.Client(fp)
	if err != nil {
		t.Fatal(err)
	}
	if code, err := get(t, pinned, srv.URL); err != nil || code != 200 {
		t.Fatalf("pinned: %d %v", code, err)
	}
	other, _ := pin.Client(strings.Repeat("ab", 32))
	if _, err := get(t, other, srv.URL); err == nil || !strings.Contains(err.Error(), "pinned") || !pin.IsCertError(err) {
		t.Fatalf("a different pin must refuse the certificate and name the pin; got %v", err)
	}
}

func TestAPinDoesNotTouchPlainHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer srv.Close()
	c, err := pin.Client(strings.Repeat("cd", 32))
	if err != nil {
		t.Fatal(err)
	}
	if code, err := get(t, c, srv.URL); err != nil || code != 204 {
		t.Fatalf("%d %v", code, err)
	}
	if _, err := pin.Fingerprint(context.Background(), srv.URL); err == nil {
		t.Fatal("a plain http address has no certificate")
	}
}

func TestBadPinsAndFormat(t *testing.T) {
	for _, bad := range []string{"zz", "abc", strings.Repeat("ab", 31)} {
		if _, err := pin.Client(bad); err == nil {
			t.Errorf("pin %q accepted", bad)
		}
	}
	if got := pin.Format("ab12cd"); got != "SHA256:AB:12:CD" {
		t.Errorf("format %q", got)
	}
	if !pin.IsCertError(nil) == false && pin.IsCertError(context.Canceled) {
		t.Error("a cancel is not a certificate error")
	}
}
