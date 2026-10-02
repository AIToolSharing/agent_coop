package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/api"
	coophub "github.com/AIToolSharing/agent_coop/internal/hub"
	"github.com/AIToolSharing/agent_coop/internal/store"
)

// dbName is the database file inside the data directory.
const dbName = "coop.db"

// defaultDataDir is /var/lib/coop for root, else $XDG_DATA_HOME/coop or ~/.local/share/coop.
func defaultDataDir() string {
	if os.Geteuid() == 0 {
		return "/var/lib/coop"
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
	auto := fs.Bool("auto-create", true, "let the first agent that joins an unknown session create it")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "coop serve: unexpected argument %q\n", fs.Arg(0))
		return 2
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
	h := coophub.New(st, coophub.Options{AutoCreate: *auto})
	srv := &http.Server{
		Handler:           api.New(h, logf),
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
