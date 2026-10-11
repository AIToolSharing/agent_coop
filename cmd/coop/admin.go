package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/store"
	"github.com/AIToolSharing/agent_coop/internal/wire"
)

const adminUsage = `usage: coop admin token add [--watch] [--data <dir>] <name>
       coop admin token list [--data <dir>]
       coop admin token revoke [--data <dir>] <name>
`

// cmdAdmin manages tokens in the hub's database. It runs on the hub's host; the hub notices a
// new or revoked token on its own. Without a database that this user can open, coop has no
// admin command: an agent cannot read the database of the hub.
func cmdAdmin(args []string, stdout, stderr io.Writer) int {
	// The flags are read with no output: a caller that may not use the command sees none.
	fs := flag.NewFlagSet("coop admin token", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	data := fs.String("data", defaultDataDir(), "the directory of the database")
	watch := fs.Bool("watch", false, "the token may also read every session (coop tui)")
	var parseErr error
	if len(args) >= 2 {
		parseErr = fs.Parse(args[2:])
	}
	if !hubDatabase(filepath.Join(*data, dbName)) {
		return unknown("admin", stderr)
	}
	if len(args) < 2 || args[0] != "token" || parseErr != nil {
		fmt.Fprint(stderr, adminUsage)
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
		role := wire.RoleMachine
		if *watch {
			role = wire.RoleWatch
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

// hubDatabase reports whether path is a database that this user can open for writing. Only the
// host of the hub has one, and only its root and the user coop can open it.
func hubDatabase(path string) bool {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
