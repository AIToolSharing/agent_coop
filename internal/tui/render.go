package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/AIToolSharing/agent_coop/internal/model"
	"github.com/AIToolSharing/agent_coop/internal/tui/view"
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
	border := styleOf(view.Style{Color: "gray"})
	if a.focus == "sidebar" {
		border = styleOf(view.Style{Color: "cyan"})
	}
	blank := strings.Repeat(" ", s.mainW)
	rule := paint(view.Line{view.Dim(strings.Repeat("─", s.mainW))}, false)
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
		switch {
		case i < s.mainH && i < len(s.main.Lines):
			b.WriteString(paint(s.main.Lines[i], s.main.IDs[i] != "" && s.main.IDs[i] == s.current))
		case i < s.mainH || s.previewH == 0:
			b.WriteString(blank)
		case i == s.mainH:
			b.WriteString(rule)
		case i-s.mainH-1 < len(s.preview.Lines):
			b.WriteString(paint(s.preview.Lines[i-s.mainH-1], false))
		default:
			b.WriteString(blank)
		}
		rows = append(rows, b.String())
	}
	promptStyle := view.Style{Color: "gray"}
	if a.mode.kind != "normal" {
		promptStyle = view.Style{Color: "cyan"}
	}
	rows = append(rows, paint(view.Fit(view.Line{view.Styled(a.prompt(), promptStyle)}, a.width), false))
	rows = append(rows, paint(view.Fit(view.Line{view.Dim(a.hint())}, a.width), false))
	return strings.Join(rows, "\n")
}

var help = [][2]string{
	{"MOVE", ""},
	{"j k  ↑ ↓", "the next, the previous message; in the sidebar the next row"},
	{"h l  ← →  tab", "focus the sidebar, the main pane"},
	{"G  end", "the newest message, and follow it"},
	{"gg  home", "the first message"},
	{"ctrl-d ctrl-u", "half a page down, up"},
	{"ctrl-f ctrl-b  pgdn pgup", "a page down, up"},
	{"ctrl-e ctrl-y", "one line down, up; the cursor stays"},
	{"enter", "open: a session; the preview of the message or the agent under the cursor"},
	{"esc", "close the preview or the help; clear the search and the agent filter"},
	{"space", "follow the newest message on or off"},
	{"[", "hide or show the sidebar"},
	{"1 2", "views: transcript, threads"},
	{"mouse", "click selects, a second click opens the preview, the wheel scrolls"},
	{"SEARCH", ""},
	{"/", "search: enter jumps to the next message whose text contains it"},
	{"n N", "the next, the previous match; the search goes around the end"},
	{"COMMANDS  (: then tab completes)", ""},
	{":delete", "delete the shown session and its messages; only with no agent in it"},
	{":filter [agent]", "only the messages of one agent; without a name: off"},
	{":sys", "system lines on or off"},
	{":help  :quit", "this help; quit"},
}

// helpBuffer is the help as one block.
func helpBuffer() view.Buffer {
	return view.Buffer{Blocks: []view.Block{{Render: func(o view.Options) []view.Line {
		var lines []view.Line
		for _, h := range help {
			key, text := h[0], h[1]
			var l view.Line
			if text == "" {
				l = view.Line{view.Bold(key)}
			} else {
				l = view.Line{view.Color("  "+key+strings.Repeat(" ", max(0, 30-view.Width(key))), "cyan"), view.S(text)}
			}
			lines = append(lines, view.Fit(l, o.Width))
		}
		return lines
	}}}}
}

var commands = []string{"delete", "filter", "sys", "help", "quit"}

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
	if word != "filter" {
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
	case "delete":
		if session == "" {
			return needSession()
		}
		return confirm("delete "+session+" and its messages? (y/n)", func() tea.Cmd {
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
				defer cancel()
				if err := a.op.DeleteSession(ctx, session); err != nil {
					return statusMsg("error: " + err.Error())
				}
				return statusMsg("deleted " + session)
			}
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
	case "help":
		a.help, a.helpTop = true, view.Anchor{}
		return nil
	case "quit", "q":
		a.quitting = true
		return tea.Quit
	}
	return a.setStatus("unknown command :" + cmd + " (tab lists them)")
}
