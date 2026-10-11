package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	tea "charm.land/bubbletea/v2"

	"github.com/AIToolSharing/agent_coop/internal/admin"
	"github.com/AIToolSharing/agent_coop/internal/config"
	"github.com/AIToolSharing/agent_coop/internal/model"
	"github.com/AIToolSharing/agent_coop/internal/tui"
)

// cmdTUI shows the sessions of the hub. It needs a token that may watch.
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
			fmt.Fprintln(stderr, "usage: coop tui [--url <url>] [--token <token>]")
			return 2
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
		fmt.Fprintln(stderr, "no hub address or no token yet; run: coop login <url> <token>")
		return 2
	}
	client := &admin.Client{Base: url, Token: token, HTTP: hubHTTP(cfg, stderr)}
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
		if errors.Is(err, admin.ErrRefused) {
			fmt.Fprintln(stderr, err.Error()+"; on the host of the hub, coop admin token add --watch <name> makes a token that may show them")
		} else {
			fmt.Fprintln(stderr, err)
		}
		return 1
	}
	return 0
}
