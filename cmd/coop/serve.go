package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/api"
	coophub "github.com/AIToolSharing/agent_coop/internal/hub"
	"github.com/AIToolSharing/agent_coop/internal/store"
)

// dbName is the database file inside the data directory.
const dbName = "coop.db"

// serviceData is the data directory of the service (deploy/coop.service).
const serviceData = "/var/lib/coop"

// defaultDataDir is the service's directory when it exists or the user is root, so that the
// admin commands on the hub's host find the hub's database without a flag; else
// $XDG_DATA_HOME/coop or ~/.local/share/coop.
func defaultDataDir() string {
	if _, err := os.Stat(serviceData); err == nil || os.Geteuid() == 0 {
		return serviceData
	}
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "coop")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "coop-data"
	}
	return filepath.Join(home, ".local", "share", "coop")
}

// serveReady, when set, gets the bound address once the server listens. Tests use it.
var serveReady func(addr string)

// secretName is the file inside the data directory that holds the secret of the hub. Every
// machine presents it: the bearer token is `<machine>.<secret>`.
const secretName = "secret"

// loadSecret reads the secret of the hub from dir, and makes one when there is none. The
// secret has no dot and no character outside a-z, A-Z, 0-9, - and _, so it stands alone in
// a shell line and after a machine name.
func loadSecret(dir string) (secret string, made bool, err error) {
	path := filepath.Join(dir, secretName)
	b, err := os.ReadFile(path)
	if err == nil {
		secret = strings.TrimSpace(string(b))
		if secret == "" || strings.ContainsAny(secret, ".\n") {
			return "", false, fmt.Errorf("%s does not hold a secret: remove the file, and the hub makes a new one", path)
		}
		return secret, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", false, err
	}
	secret = base64.RawURLEncoding.EncodeToString(raw[:])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		return "", false, err
	}
	return secret, true, nil
}

func cmdServe(args []string, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, args, stderr)
}

// serve runs the hub until ctx ends.
func serve(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("coop serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", "127.0.0.1:8090", "the address to listen on (plain HTTP; put TLS in front)")
	data := fs.String("data", defaultDataDir(), "the directory of the database")
	// --auto-create was a flag of v0.10.0 and before. The first join always creates the session
	// now. The flag stays, so that a unit that gives it still starts.
	fs.Bool("auto-create", true, "ignored: the first agent that joins an unknown session creates it")
	dist := fs.String("dist", "", "the directory of the release binaries that the hub gives to devices (default: dist in the data directory)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "coop serve: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	secret, made, err := loadSecret(*data)
	if err != nil {
		fmt.Fprintf(stderr, "coop serve: %v\n", err)
		return 1
	}
	st, err := store.Open(filepath.Join(*data, dbName))
	if err != nil {
		fmt.Fprintf(stderr, "coop serve: %v\n", err)
		return 1
	}
	defer st.Close()
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(stderr, "coop serve: %v\n", err)
		return 1
	}
	logf := func(format string, a ...any) { fmt.Fprintf(stderr, "coop serve: "+format+"\n", a...) }
	if made {
		logf("made %s: the secret that every machine presents", filepath.Join(*data, secretName))
	}
	h := coophub.New(st, coophub.Options{Secret: secret})
	// A device installs and upgrades coop from its hub: the hub reports its version and gives
	// the binaries of that version (deploy/install.sh puts them into the directory).
	handler := api.New(h, logf)
	handler.Version, handler.Dist = version, *dist
	if handler.Dist == "" {
		handler.Dist = filepath.Join(*data, "dist")
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: the streams live for hours. Each stream write has its own deadline.
	}
	logf("listening on %s, data in %s", ln.Addr(), *data)
	if serveReady != nil {
		serveReady(ln.Addr().String())
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			logf("%v", err)
			return 1
		}
	}
	h.Close()
	_ = srv.Close()
	return 0
}
