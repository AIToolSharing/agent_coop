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

// roleKeys is the key of the credential file for the token of each role.
var roleKeys = map[string]string{
	wire.RoleMachine:      "COOP_TOKEN",
	wire.RoleOperator:     "COOP_OPERATOR_TOKEN",
	wire.RoleOrchestrator: "COOP_ORCHESTRATOR_TOKEN",
}

// hubIdentity is what the hub says of a token and of itself.
type hubIdentity struct {
	Role string `json:"role"`
	// Version is the version of the hub: "" from a hub of a version that reports none.
	Version string `json:"version"`
}

// whoami asks the hub what the token is: GET /v1/whoami gives its role and the version of the
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

// probeToken gives the role of the token (whoami).
func probeToken(ctx context.Context, c *http.Client, base, token string) (role string, err error) {
	me, err := whoami(ctx, c, base, token)
	return me.Role, err
}

// cmdLogin stores the hub address and a token in the credential file. The token's role decides
// the key (roleKeys): COOP_TOKEN for the agents of this machine, COOP_OPERATOR_TOKEN for the TUI.
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
	// is pinned by its fingerprint after the token was accepted through it.
	values := map[string]string{"COOP_URL": base, "COOP_CERT_SHA256": ""}
	role, err := probeToken(ctx, &http.Client{}, base, args[1])
	if err != nil && pin.IsCertError(err) && strings.HasPrefix(base, "https://") {
		fp, ferr := pin.Fingerprint(ctx, base)
		if ferr != nil {
			fmt.Fprintln(stderr, ferr)
			return 1
		}
		pinned, _ := pin.Client(fp)
		if role, err = probeToken(ctx, pinned, base, args[1]); err == nil {
			values["COOP_CERT_SHA256"] = fp
			fmt.Fprintf(stdout, "the hub's certificate is self-signed; its fingerprint is pinned for %s:\n  %s\n  compare it with the fingerprint the hub's host shows\n", base, pin.Format(fp))
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	values[roleKeys[role]] = args[1]
	file := config.DefaultEnvFile()
	if err := config.UpdateEnvFile(file, values); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s (mode 0600): %s token for %s\n", file, role, base)
	switch role {
	case wire.RoleOperator:
		fmt.Fprintln(stdout, "next:  coop tui")
	case wire.RoleOrchestrator:
		fmt.Fprintln(stdout, "next:  coop --orchestrator claude <session>   # an agent that may also act for the operator")
	default:
		fmt.Fprintln(stdout, "next:  cd <project> && coop session <name>   # put the project's agents in a session")
		fmt.Fprintln(stdout, "       coop claude                           # Claude Code with the coop channel")
	}
	return 0
}
