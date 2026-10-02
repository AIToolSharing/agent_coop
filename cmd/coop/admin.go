package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/store"
)

const adminUsage = `usage: coop admin token add [--operator] [--data <dir>] <name>
       coop admin token list [--data <dir>]
       coop admin token revoke [--data <dir>] <name>
`

// cmdAdmin manages tokens in the hub's database. It runs on the hub's host; the hub notices a
// new or revoked token on its own.
func cmdAdmin(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 || args[0] != "token" {
		fmt.Fprint(stderr, adminUsage)
		return 2
	}
	fs := flag.NewFlagSet("coop admin token "+args[1], flag.ContinueOnError)
	fs.SetOutput(stderr)
	data := fs.String("data", defaultDataDir(), "the directory of the database")
	operator := fs.Bool("operator", false, "an operator token (the TUI and the admin API)")
	if err := fs.Parse(args[2:]); err != nil {
		return 2
	}
	verb, name := args[1], fs.Arg(0)
	if (verb == "add" || verb == "revoke") && (fs.NArg() != 1) || verb == "list" && fs.NArg() != 0 {
		fmt.Fprint(stderr, adminUsage)
		return 2
	}
	st, err := store.Open(filepath.Join(*data, dbName))
	if err != nil {
		fmt.Fprintf(stderr, "coop admin: %v\n", err)
		return 1
	}
	defer st.Close()
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	switch verb {
	case "add":
		role := "machine"
		if *operator {
			role = "operator"
		}
		tok, err := st.IssueToken(name, role, now)
		if err != nil {
			fmt.Fprintf(stderr, "coop admin: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, tok)
		return 0
	case "list":
		rows, err := st.Tokens()
		if err != nil {
			fmt.Fprintf(stderr, "coop admin: %v\n", err)
			return 1
		}
		for _, t := range rows {
			state := "valid"
			if t.RevokedAt != "" {
				state = "revoked " + t.RevokedAt
			}
			fmt.Fprintf(stdout, "%s\t%s\tcreated %s\t%s\n", t.Name, t.Role, t.CreatedAt, state)
		}
		return 0
	case "revoke":
		ok, err := st.RevokeToken(name, now)
		if err != nil {
			fmt.Fprintf(stderr, "coop admin: %v\n", err)
			return 1
		}
		if !ok {
			fmt.Fprintf(stderr, "no valid token for %s\n", name)
			return 1
		}
		fmt.Fprintf(stderr, "revoked %s\n", name)
		return 0
	}
	fmt.Fprint(stderr, adminUsage)
	return 2
}
