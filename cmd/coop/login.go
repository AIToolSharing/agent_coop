package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/config"
	"github.com/AIToolSharing/agent_coop/internal/pin"
	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// errBadToken: the hub answered, and refused the token.
var errBadToken = errors.New("the hub refused the token")

// hubIdentity is what the hub says of a token and of itself. Role is machine for each token.
type hubIdentity struct {
	Role string `json:"role"`
	// Version is the version of the hub: "" from a hub of a version that reports none.
	Version string `json:"version"`
}

// whoami asks the hub whether it accepts the token: GET /v1/whoami gives the version of the
// hub, and 401 for a token that the hub does not know.
func whoami(ctx context.Context, c *http.Client, base, token string) (hubIdentity, error) {
	var me hubIdentity
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/whoami", nil)
	if err != nil {
		return me, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := c.Do(req)
	if err != nil {
		return me, fmt.Errorf("cannot reach %s: %w", base, err)
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case 200:
		if err := json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&me); err != nil || !wire.IsRole(me.Role) {
			return me, fmt.Errorf("%s gave no role for the token", base)
		}
		return me, nil
	case 401:
		return me, errBadToken
	case 404:
		return me, fmt.Errorf("%s is a hub of an older version: upgrade it", base)
	}
	return me, fmt.Errorf("%s answered %d", base, res.StatusCode)
}

// probeToken checks that the hub accepts the token (whoami).
func probeToken(ctx context.Context, c *http.Client, base, token string) error {
	_, err := whoami(ctx, c, base, token)
	return err
}

// cmdLogin stores the hub address and the token of this machine in the credential file. It
// removes COOP_OPERATOR_TOKEN, the second token of coop v0.13.0 and before.
func cmdLogin(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "usage: coop login <url> <token>")
		return 2
	}
	base := strings.TrimRight(args[0], "/")
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		fmt.Fprintln(stderr, "the address must start with http:// or https://")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// A hub with a public certificate, or plain http, needs no pin. A self-signed certificate
	// is pinned by its fingerprint after the token was accepted through it. With the
	// fingerprint in COOP_CERT_SHA256 (as the hub's host shows it), login checks the
	// certificate before the token goes; without it, the first certificate is trusted.
	values := map[string]string{"COOP_URL": base, "COOP_TOKEN": args[1], "COOP_CERT_SHA256": "", "COOP_OPERATOR_TOKEN": ""}
	err := probeToken(ctx, &http.Client{}, base, args[1])
	if err != nil && pin.IsCertError(err) && strings.HasPrefix(base, "https://") {
		fp, ferr := pin.Fingerprint(ctx, base)
		if ferr != nil {
			fmt.Fprintln(stderr, ferr)
			return 1
		}
		want := environ()["COOP_CERT_SHA256"]
		if want != "" {
			w, perr := pin.Parse(want)
			if perr != nil {
				fmt.Fprintln(stderr, "COOP_CERT_SHA256:", perr)
				return 2
			}
			if w != fp {
				fmt.Fprintf(stderr, "the hub's certificate has the fingerprint\n  %s\nnot the one of COOP_CERT_SHA256\n  %s\ncoop sent no token. Another server can be between this machine and the hub.\n", pin.Format(fp), pin.Format(w))
				return 1
			}
		}
		pinned, _ := pin.Client(fp)
		if err = probeToken(ctx, pinned, base, args[1]); err == nil {
			values["COOP_CERT_SHA256"] = fp
			if want != "" {
				fmt.Fprintf(stdout, "the hub's certificate matches COOP_CERT_SHA256; its fingerprint is pinned for %s:\n  %s\n", base, pin.Format(fp))
			} else {
				fmt.Fprintf(stdout, "the hub's certificate is self-signed; its fingerprint is pinned for %s:\n  %s\n  compare it with the fingerprint the hub's host shows. If they differ, revoke the token.\n", base, pin.Format(fp))
			}
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	file := config.DefaultEnvFile()
	if err := config.UpdateEnvFile(file, values); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s (mode 0600): the token for %s\n", file, base)
	fmt.Fprintln(stdout, "next:  cd <project> && coop claude   # Claude Code in the session of the git repository")
	return 0
}
