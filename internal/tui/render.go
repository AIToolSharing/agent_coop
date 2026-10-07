package tui

import (
	"context"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/AIToolSharing/agent_coop/internal/model"
	"github.com/AIToolSharing/agent_coop/internal/tui/view"
	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// ANSI indexes of the color names the views use.
var colors = map[string]string{"red": "1", "green": "2", "yellow": "3", "blue": "4", "cyan": "6", "gray": "8"}

var styles = map[view.Style]lipgloss.Style{}

func styleOf(st view.Style) lipgloss.Style {
	if s, ok := styles[st]; ok {
		return s
	}
	s := lipgloss.NewStyle()
	if c, ok := colors[st.Color]; ok {
		s = s.Foreground(lipgloss.Color(c))
	}
	s = s.Bold(st.Bold).Faint(st.Dim).Reverse(st.Inverse)
	styles[st] = s
	return s
}

// paint turns a line into a styled string. A selected line shows its first segment inverse.
func paint(l view.Line, selected bool) string {
	var b strings.Builder
	for i, seg := range l {
		st := seg.Style
		if selected && i == 0 {
			st.Inverse = true
		}
		b.WriteString(styleOf(st).Render(seg.Text))
	}
	return b.String()
}

func (a *App) render() string {
	s := a.screen()
	a.last = s
	rows := make([]string, 0, a.height)
	rows = append(rows, paint(view.Fit(view.Line{view.Bold(a.title(s))}, a.width), false))
	sideOffset := listOffset(a.cursorOf(s), len(s.sidebar.Lines), s.bodyH)
	offset := a.viewOffset(s)
	border := styleOf(view.Style{Color: "gray"})
	if a.focus == "sidebar" {
		border = styleOf(view.Style{Color: "cyan"})
	}
	blank := strings.Repeat(" ", s.mainW)
	for i := 0; i < s.bodyH; i++ {
		var b strings.Builder
		if a.sidebar {
			if j := sideOffset + i; j < len(s.sidebar.Lines) {
				b.WriteString(paint(s.sidebar.Lines[j], false))
			} else {
				b.WriteString(strings.Repeat(" ", max(0, s.sidebarW-1)))
			}
			b.WriteString(border.Render("│"))
		}
		k := i
		if s.attn != nil {
			if i == 0 {
				b.WriteString(paint(view.Fit(s.attn, s.mainW), false))
				rows = append(rows, b.String())
				continue
			}
			k = i - 1
		}
		if j := offset + k; j < len(s.main.Lines) {
			b.WriteString(paint(s.main.Lines[j], s.main.IDs[j] != "" && s.main.IDs[j] == a.msgID))
		} else {
			b.WriteString(blank)
		}
		rows = append(rows, b.String())
	}
	promptStyle := view.Style{Color: "gray"}
	if a.mode.kind != "normal" {
		promptStyle = view.Style{Color: "cyan"}
	}
	for _, l := range a.promptLines(s) {
		rows = append(rows, paint(view.Fit(view.Line{view.Styled(l, promptStyle)}, a.width), false))
	}
	rows = append(rows, paint(view.Fit(view.Line{view.Dim(a.hint())}, a.width), false))
	return strings.Join(rows, "\n")
}

var help = [][2]string{
	{"MOVE", ""},
	{"tab", "focus the sidebar or the main pane"},
	{"↑ ↓  j k", "move: in the sidebar over sessions and agents, in the main pane over messages"},
	{"enter", "open what the cursor is on: a session, an agent, a message"},
	{"esc", "close the details or the help; clear the search and the agent filter"},
	{"pgup pgdn home end", "scroll the main pane; end follows the newest message again"},
	{"space", "follow the newest message on or off"},
	{"[", "hide or show the sidebar"},
	{"1 2 3", "views: transcript, threads, activity (what the agents do at their terminals)"},
	{"mouse", "click selects, a second click opens, the wheel scrolls"},
	{"WRITE", ""},
	{"m", "message the session; tab picks the target; \"@agent text\" also works"},
	{"r", "reply to the selected message, to its sender, in its thread"},
	{"alt+enter", "a new line in the composer"},
	{"a", "go to the next thing that needs you: a message for you, a held agent, a stale ask, a blocked agent"},
	{"/", "search in the message text, or in the activity; an empty search clears"},
	{"STEER  (an agent selected in the sidebar, or its details open)", ""},
	{"g", "release a held or paused agent, with a task or none"},
	{"p", "pause the agent at its next tool call, or resume it"},
	{"x", "stop the agent: remove it (:allow lets it back)"},
	{"P  R", "pause each working agent; resume each paused one"},
	{"H", "new agents are held, or start at once"},
	{"COMMANDS  (: then tab completes)", ""},
	{":new <name>", "create a session"},
	{":close  :reopen  :delete", "the shown session; delete needs a closed session"},
	{":kick <agent>  :allow <agent>", "remove an agent from the session; let it back in"},
	{":forget [agent]", "drop an agent that left from the lists; without a name: each one that left"},
	{":go [agent] [task]", "release a held or paused agent, with a task if you give one; no name: each held one"},
	{":pause [agent]  :resume [agent]", "stop an agent's work at its next tool call; let it go on; no name: each one"},
	{":hold on|off", "new agents of the session wait for your release, or start at once"},
	{":withdraw [#id]", "withdraw a message (default: the selected one)"},
	{":filter [agent]", "only messages, or activity, of one agent; without a name: off"},
	{":sys", "system lines on or off"},
	{":brief [every <N>m | off]", "a summary of the session by a local Claude Code run; repeat it, or stop the repeats"},
	{":help  :quit", "this help; quit"},
}

func helpLines(width int) view.Rendered {
	var r view.Rendered
	for _, h := range help {
		key, text := h[0], h[1]
		var l view.Line
		if text == "" {
			l = view.Line{view.Bold(key)}
		} else {
			l = view.Line{view.Color("  "+key+strings.Repeat(" ", max(0, 30-view.Width(key))), "cyan"), view.S(text)}
		}
		r.Lines = append(r.Lines, view.Fit(l, width))
		r.IDs = append(r.IDs, "")
	}
	return r
}

var commands = []string{"new", "close", "reopen", "delete", "kick", "allow", "forget", "go", "pause", "resume", "hold", "withdraw", "filter", "sys", "brief", "help", "quit"}

// Complete finishes a command line: the command word, or an agent name for the commands that
// take one.
func Complete(value string, agents []*model.Agent) string {
	word, arg, hasArg := strings.Cut(value, " ")
	if !hasArg {
		var hits []string
		for _, c := range commands {
			if strings.HasPrefix(c, word) {
				hits = append(hits, c)
			}
		}
		switch len(hits) {
		case 0:
			return value
		case 1:
			return hits[0] + " "
		}
		return commonPrefix(hits)
	}
	if !slices.Contains([]string{"kick", "allow", "forget", "filter", "go", "pause", "resume"}, word) {
		return value
	}
	arg = strings.TrimSpace(arg)
	var hits []string
	for _, a := range agents {
		name, _, _ := strings.Cut(a.Address, "@")
		if strings.HasPrefix(a.Address, arg) || strings.HasPrefix(name, arg) {
			hits = append(hits, a.Address)
		}
	}
	switch len(hits) {
	case 0:
		return value
	case 1:
		return word + " " + hits[0]
	}
	return word + " " + commonPrefix(hits)
}

func commonPrefix(xs []string) string {
	p := xs[0]
	for _, x := range xs {
		for !strings.HasPrefix(x, p) {
			p = p[:len(p)-1]
		}
	}
	return p
}

// command runs a `:` command.
func (a *App) command(line string, s screen) tea.Cmd {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil
	}
	cmd, arg := fields[0], strings.Join(fields[1:], " ")
	session := a.sid
	if session == model.AllSessions {
		session = ""
	}
	needSession := func() tea.Cmd { return a.setStatus("pick a session first (tab, then ↑↓)") }
	confirm := func(text string, run func() tea.Cmd) tea.Cmd {
		a.mode = mode{kind: "confirm", text: text, run: run}
		return nil
	}
	// agentOf finds an agent of the shown session by address or bare name; default the
	// sidebar's agent.
	agentOf := func(name string) *model.Agent {
		want := name
		if want == "" {
			want = a.agentAddr
		}
		var hits []*model.Agent
		for _, ag := range s.v.AgentList() {
			bare, _, _ := strings.Cut(ag.Address, "@")
			if ag.Address == want || bare == want {
				hits = append(hits, ag)
			}
		}
		if len(hits) == 1 {
			return hits[0]
		}
		return nil
	}
	switch cmd {
	case "new":
		if !wire.IsToken(arg) {
			return a.setStatus("usage: :new <name>  (letters, digits, - and _)")
		}
		return a.act(func(ctx context.Context) (string, error) {
			return "created " + arg, a.op.CreateSession(ctx, arg)
		})
	case "close":
		if session == "" {
			return needSession()
		}
		return confirm("close "+session+"? agents in it are told (y/n)", func() tea.Cmd {
			return a.act(func(ctx context.Context) (string, error) { return "closed " + session, a.op.CloseSession(ctx, session) })
		})
	case "reopen":
		if session == "" {
			return needSession()
		}
		return a.act(func(ctx context.Context) (string, error) {
			return "reopened " + session, a.op.ReopenSession(ctx, session)
		})
	case "delete":
		if session == "" {
			return needSession()
		}
		if rec := a.store.Sessions[session]; rec == nil || rec.Status != "closed" {
			return a.setStatus("close the session first (:close)")
		}
		return confirm("delete "+session+" and its messages? (y/n)", func() tea.Cmd {
			return a.act(func(ctx context.Context) (string, error) {
				return "deleted " + session, a.op.DeleteSession(ctx, session)
			})
		})
	case "kick":
		if session == "" {
			return needSession()
		}
		ag := agentOf(arg)
		if ag == nil {
			return a.setStatus("which agent? :kick <name>")
		}
		return confirm("remove "+ag.Address+" from "+session+"? it cannot come back until :allow (y/n)", func() tea.Cmd {
			return a.act(func(ctx context.Context) (string, error) {
				return "removed " + ag.Address, a.op.Kick(ctx, session, ag.Address)
			})
		})
	case "allow":
		if session == "" {
			return needSession()
		}
		ag := agentOf(arg)
		if ag == nil {
			return a.setStatus("which agent? :allow <name>")
		}
		return a.act(func(ctx context.Context) (string, error) {
			return ag.Address + " may join again", a.op.Unkick(ctx, session, ag.Address)
		})
	case "forget":
		if session == "" {
			return needSession()
		}
		// Without a name: each agent that joined and is not in the session now.
		var targets []*model.Agent
		if arg == "" {
			for _, ag := range s.v.AgentList() {
				if !ag.Online && !ag.Forgotten && ag.JoinedAt != "" {
					targets = append(targets, ag)
				}
			}
			if len(targets) == 0 {
				return a.setStatus("no agent that left in " + session)
			}
		} else {
			ag := agentOf(arg)
			if ag == nil {
				return a.setStatus("which agent? :forget <name>")
			}
			if ag.Online {
				return a.setStatus(ag.Address + " is in the session; :kick removes it")
			}
			targets = []*model.Agent{ag}
		}
		forget := func() tea.Cmd {
			return a.act(func(ctx context.Context) (string, error) {
				for _, ag := range targets {
					if err := a.op.Forget(ctx, session, ag.Address); err != nil {
						return "", err
					}
				}
				if len(targets) == 1 {
					return "forgot " + targets[0].Address, nil
				}
				return "forgot " + strconv.Itoa(len(targets)) + " agents", nil
			})
		}
		if arg != "" {
			return forget()
		}
		return confirm("forget "+strconv.Itoa(len(targets))+" agents that left "+session+"? they may join again (y/n)", forget)
	case "go", "pause", "resume":
		if session == "" {
			return needSession()
		}
		// go takes held and paused agents to run; pause takes running ones; resume takes
		// paused ones and leaves a held agent held.
		want, applies := wire.GateRun, func(ag *model.Agent) bool { return ag.Gate != wire.GateRun }
		done := map[string]string{"go": "released", "pause": "paused", "resume": "resumed"}[cmd]
		switch cmd {
		case "pause":
			want, applies = wire.GatePaused, func(ag *model.Agent) bool { return ag.Gate == wire.GateRun }
		case "resume":
			applies = func(ag *model.Agent) bool { return ag.Gate == wire.GatePaused }
		}
		name, task := "", ""
		if len(fields) > 1 {
			name, task = fields[1], strings.Join(fields[2:], " ")
		}
		if task != "" && cmd != "go" {
			return a.setStatus("usage: :" + cmd + " [agent]")
		}
		if name != "" {
			ag := agentOf(name)
			if ag == nil {
				return a.setStatus("no agent " + name)
			}
			if !applies(ag) {
				return a.setStatus(ag.Address + " is " + ag.Gate + " already")
			}
			if cmd == "go" {
				return a.release(session, ag.Address, task)
			}
			return a.act(func(ctx context.Context) (string, error) {
				return done + " " + ag.Address, a.op.SetGate(ctx, session, ag.Address, want)
			})
		}
		// No name: each agent that the command applies to. A removed agent has no gate.
		var targets []*model.Agent
		for _, ag := range s.v.AgentList() {
			if !ag.Kicked && !ag.Forgotten && ag.JoinedAt != "" && applies(ag) && (cmd != "go" || ag.Gate == wire.GateHeld) {
				targets = append(targets, ag)
			}
		}
		if len(targets) == 0 {
			return a.setStatus("no agent to " + map[string]string{"go": "release", "pause": "pause", "resume": "resume"}[cmd] + " in " + session)
		}
		return a.act(func(ctx context.Context) (string, error) {
			for _, ag := range targets {
				if err := a.op.SetGate(ctx, session, ag.Address, want); err != nil {
					return "", err
				}
			}
			if len(targets) == 1 {
				return done + " " + targets[0].Address, nil
			}
			return done + " " + strconv.Itoa(len(targets)) + " agents", nil
		})
	case "hold":
		if session == "" {
			return needSession()
		}
		if arg != "on" && arg != "off" {
			return a.setStatus("usage: :hold on|off")
		}
		hold := arg == "on"
		return a.act(func(ctx context.Context) (string, error) {
			if hold {
				return "new agents of " + session + " are held until you release them", a.op.SetHold(ctx, session, true)
			}
			return "new agents of " + session + " start at once", a.op.SetHold(ctx, session, false)
		})
	case "withdraw":
		if session == "" {
			return needSession()
		}
		id := strings.TrimPrefix(arg, "#")
		if id == "" {
			id = s.current
		}
		if s.v.Msgs[id] == nil {
			return a.setStatus("which message? :withdraw #<id>")
		}
		return confirm("withdraw #"+id+"? (y/n)", func() tea.Cmd {
			return a.act(func(ctx context.Context) (string, error) {
				ok, err := a.op.Redact(ctx, session, id)
				if err == nil && !ok {
					return "#" + id + " is not a message of this session", nil
				}
				return "withdrew #" + id, err
			})
		})
	case "filter":
		if arg == "" {
			a.agentFilter = ""
			return a.setStatus("filter off")
		}
		ag := agentOf(arg)
		if ag == nil {
			return a.setStatus("no agent " + arg)
		}
		a.agentFilter = ag.Address
		return a.setStatus("only " + ag.Address)
	case "sys":
		a.system = !a.system
		if a.system {
			return a.setStatus("system lines on")
		}
		return a.setStatus("system lines off")
	case "brief":
		if session == "" {
			return needSession()
		}
		every, off, err := parseBrief(arg)
		switch {
		case err != nil:
			return a.setStatus(err.Error())
		case off:
			a.briefEvery = 0
			return a.setStatus("brief: repeats off")
		}
		a.briefEvery = every
		if every > 0 {
			a.briefNext = a.now().Add(every)
		}
		return a.startBrief(session, s.v)
	case "help":
		a.overlay = &overlay{kind: "help"}
		a.scroll = 0
		return nil
	case "quit", "q":
		a.quitting = true
		return tea.Quit
	}
	return a.setStatus("unknown command :" + cmd + " (tab lists them)")
}
