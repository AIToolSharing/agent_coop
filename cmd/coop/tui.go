package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/AIToolSharing/agent_coop/internal/admin"
	"github.com/AIToolSharing/agent_coop/internal/config"
	"github.com/AIToolSharing/agent_coop/internal/model"
	"github.com/AIToolSharing/agent_coop/internal/tui"
)

// cmdTUI shows the sessions of the hub.
func cmdTUI(args []string, stderr io.Writer) int {
	url, token := "", ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--url" && i+1 < len(args):
			i++
			url = args[i]
		case args[i] == "--token" && i+1 < len(args):
			i++
			token = args[i]
		default:
			return unknown("tui", stderr)
		}
	}
	warn := func(s string) { fmt.Fprintln(stderr, s) }
	cfg := config.Load(environ(), config.DefaultEnvFile(), warn, cwd())
	if url == "" {
		url = cfg.URL
	}
	if token == "" {
		token = cfg.Token
	}
	if url == "" || token == "" {
		fmt.Fprintln(stderr, "coop tui: no hub address or no secret: run the join line of the hub, or coop login <url> <secret>")
		return 1
	}
	client := &admin.Client{Base: url, Token: token, HTTP: hubHTTP(cfg, stderr)}
	check, stop := context.WithTimeout(context.Background(), 10*time.Second)
	err := client.Check(check)
	stop()
	var refused *admin.APIError
	switch {
	case errors.As(err, &refused):
		fmt.Fprintln(stderr, "coop tui: the hub refused the secret: run the join line of the hub again")
		return 1
	case err != nil:
		fmt.Fprintln(stderr, "coop: cannot reach "+url)
		return 1
	}
	store := model.New()
	updates := make(chan model.Update, 256)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := tui.Options{Store: store, Updates: updates, Op: client}
	app := tui.New(o)
	p := tea.NewProgram(app)
	go func() {
		err := client.Feed(ctx, updates)
		close(updates)
		if err != nil {
			p.Send(tui.FeedError{Err: err})
		}
	}()
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := app.Err(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
