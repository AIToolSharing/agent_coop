package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/config"
)

// errBadToken: the hub answered, and refused the token.
var errBadToken = errors.New("the hub refused the token")

// probeToken asks the hub what the token is. The admin route answers 200 to an operator token,
// 403 to a machine token, and 401 to a token it does not know.
func probeToken(ctx context.Context, c *http.Client, base, token string) (role string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/admin/sessions", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot reach %s: %w", base, err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	switch res.StatusCode {
	case 200:
		return "operator", nil
	case 403:
		return "machine", nil
	case 401:
		return "", errBadToken
	}
	return "", fmt.Errorf("%s answered %d", base, res.StatusCode)
}

// cmdLogin stores the hub address and a token in the credential file. The token's role decides
// the key: COOP_OPERATOR_TOKEN for the TUI, COOP_TOKEN for the agents of this machine.
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
	role, err := probeToken(ctx, http.DefaultClient, base, args[1])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	key := "COOP_TOKEN"
	if role == "operator" {
		key = "COOP_OPERATOR_TOKEN"
	}
	file := config.DefaultEnvFile()
	if err := config.UpdateEnvFile(file, map[string]string{"COOP_URL": base, key: args[1]}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s (mode 0600): %s token for %s\n", file, role, base)
	if role == "operator" {
		fmt.Fprintln(stdout, "next:  coop tui")
	} else {
		fmt.Fprintln(stdout, "next:  cd <project> && coop session <name>   # put the project's agents in a session")
		fmt.Fprintln(stdout, "       coop claude                           # Claude Code with the coop channel")
	}
	return 0
}
