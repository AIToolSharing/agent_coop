// Package tui is the operator's terminal program: a sidebar (sessions, then the agents of the
// shown session), a main pane (transcript or threads, or the details of a message or an
// agent), an attention line, a composer, and a `:` command line. It paints what the view
// package returns and calls the operator's actions.
package tui

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/AIToolSharing/agent_coop/internal/model"
	"github.com/AIToolSharing/agent_coop/internal/tui/view"
	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// Operator is what the shell asks the hub to do. *admin.Client is one.
type Operator interface {
	CreateSession(ctx context.Context, sid string) error
	CloseSession(ctx context.Context, sid string) error
	ReopenSession(ctx context.Context, sid string) error
	DeleteSession(ctx context.Context, sid string) error
	Kick(ctx context.Context, sid, target string) error
	Unkick(ctx context.Context, sid, target string) error
	Redact(ctx context.Context, sid, id string) (bool, error)
	Send(ctx context.Context, sid, to, text, replyTo string) (string, error)
}

// Options builds an App.
type Options struct {
	Store *model.Store
	// Updates carries the feed; nil in tests.
	Updates <-chan model.Update
	Op      Operator
	Now     func() time.Time
	Loc     *time.Location
}

const (
	viewTranscript = "transcript"
	viewThreads    = "threads"
	wheelLines     = 3
	actionTimeout  = 10 * time.Second
)

type overlay struct {
	kind string // message, agent, help
	id   string
}

type reply struct{ id, to string }

type mode struct {
	kind  string // normal compose command search confirm
	to    string // compose: all or an address
	reply *reply
	value string // command and search
	text  string // confirm
	run   func() tea.Cmd
}

// App is the tea.Model. All state lives here; Update reads and writes it in one goroutine.
type App struct {
	store   *model.Store
	updates <-chan model.Update
	op      Operator
	now     func() time.Time
	loc     *time.Location

	width, height int
	sid           string
	view          string
	focus         string // sidebar or main
	sidebar       bool
	cursor        int // sidebar row under the cursor; -1 follows the shown session
	msgID         string
	agentAddr     string
	overlay       *overlay
	follow        bool
	system        bool
	agentFilter   string
	search        string
	scroll        int // first visible main line when scrolled; -1 follows the cursor
	mode          mode
	status        string
	attn          int

	transcripts map[string]*view.Transcript
	ta          textarea.Model
	last        screen
	quitting    bool
}

// Messages.
type updatesMsg []model.Update
type tickMsg time.Time
type statusMsg string

func New(o Options) *App {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Loc == nil {
		o.Loc = time.Local
	}
	ta := textarea.New()
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = wire.MaxText
	return &App{
		store: o.Store, updates: o.Updates, op: o.Op, now: o.Now, loc: o.Loc,
		width: 80, height: 24,
		sid: model.AllSessions, view: viewTranscript, focus: "main", sidebar: true,
		cursor: -1, follow: true, system: true, scroll: -1,
		mode:        mode{kind: "normal"},
		transcripts: map[string]*view.Transcript{},
		ta:          ta,
	}
}

func (a *App) Init() tea.Cmd {
	return tea.Batch(a.readFeed(), tick())
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// readFeed waits for the next update, then takes what else is ready, so that a burst becomes
// one repaint.
func (a *App) readFeed() tea.Cmd {
	if a.updates == nil {
		return nil
	}
	ch := a.updates
	return func() tea.Msg {
		first, ok := <-ch
		if !ok {
			return nil
		}
		batch := updatesMsg{first}
		for len(batch) < 256 {
			select {
			case u, ok := <-ch:
				if !ok {
					return batch
				}
				batch = append(batch, u)
			default:
				return batch
			}
		}
		return batch
	}
}

// --- The screen --------------------------------------------------------------------------------

type screen struct {
	v          *model.Session
	sums       []view.Summary
	sidebar    view.Sidebar
	sidebarW   int // columns of the sidebar, border included; 0 when hidden
	mainW      int
	main       view.Rendered
	attn       view.Line
	items      []view.Item
	current    string // the message under the cursor: the newest one while following
	lastID     string // the newest message with a line in the main pane
	following  bool
	bodyH      int
	mainBodyH  int
	promptH    int
}

func (a *App) options(width int) view.Options {
	return view.Options{Width: width, Now: a.now(), Loc: a.loc, Agent: a.agentFilter, Search: a.search, System: a.system, Selected: a.msgID}
}

func (a *App) screen() screen {
	s := screen{v: a.store.View(a.sid)}
	now := a.now()
	s.sums = view.Summaries(a.store, now)
	agents := s.v.AgentList()
	if a.sidebar {
		s.sidebarW = view.SidebarWidth(s.sums, agents, max(22, a.width/3))
	}
	cursor := -1
	if a.focus == "sidebar" {
		cursor = a.cursor
	}
	s.sidebar = view.RenderSidebar(s.sums, agents, view.Selection{SID: a.sid, Cursor: cursor}, max(1, s.sidebarW-1), now)
	s.mainW = max(20, a.width-s.sidebarW)
	s.items = view.Attention(s.v, now)
	s.attn = view.RenderAttention(s.items, now)
	o := a.options(s.mainW)
	switch {
	case a.overlay != nil && a.overlay.kind == "help":
		s.main = helpLines(s.mainW)
	case a.overlay != nil && a.overlay.kind == "message":
		s.main = view.RenderMessage(s.v, a.overlay.id, o)
	case a.overlay != nil && a.overlay.kind == "agent":
		s.main = view.RenderAgent(s.v, a.overlay.id, o)
	case a.view == viewThreads:
		s.main = view.RenderThreads(s.v, o)
	default:
		t := a.transcripts[a.sid]
		if t == nil {
			t = &view.Transcript{}
			a.transcripts[a.sid] = t
		}
		s.main = t.Render(s.v, o)
	}
	if a.overlay == nil {
		s.lastID = nextID(s.main.IDs, len(s.main.IDs), -1)
	}
	s.following = a.follow && a.view == viewTranscript && a.overlay == nil
	if s.following {
		s.current = s.lastID
	} else {
		s.current = a.msgID
	}
	s.promptH = 1
	if a.mode.kind == "compose" {
		s.promptH = min(4, max(1, a.ta.LineCount()))
	}
	s.bodyH = max(4, a.height-1-s.promptH-1)
	s.mainBodyH = s.bodyH
	if s.attn != nil {
		s.mainBodyH = max(1, s.bodyH-1)
	}
	return s
}

// cursorOf is the sidebar row the cursor is on: where it was put, else the shown session.
func (a *App) cursorOf(s screen) int {
	if a.cursor >= 0 {
		return a.cursor
	}
	for i, r := range s.sidebar.Rows {
		if r != nil && r.SID == a.sid {
			return i
		}
	}
	return -1
}

// viewOffset is the first visible line of the main pane.
func (a *App) viewOffset(s screen) int {
	maxOff := max(0, len(s.main.Lines)-s.mainBodyH)
	if s.following {
		return maxOff
	}
	if a.scroll >= 0 {
		return min(maxOff, a.scroll)
	}
	line := indexOf(s.main.IDs, s.current)
	if line < 0 {
		return 0
	}
	return min(maxOff, max(0, line-s.mainBodyH+1))
}

func listOffset(index, length, height int) int {
	if index < 0 || length <= height {
		return 0
	}
	return min(length-height, max(0, index-height+1))
}

func reveal(line, offset, bodyH int) int {
	if line < offset {
		return line
	}
	if line >= offset+bodyH {
		return line - bodyH + 1
	}
	return offset
}

// nextID is the first message id in ids after (dir 1) or before (dir -1) line `from`, or "".
func nextID(ids []string, from, dir int) string {
	for i := from + dir; i >= 0 && i < len(ids); i += dir {
		if ids[i] != "" {
			return ids[i]
		}
	}
	return ""
}

func indexOf(xs []string, x string) int {
	if x == "" {
		return -1
	}
	for i, y := range xs {
		if y == x {
			return i
		}
	}
	return -1
}

// --- Update ------------------------------------------------------------------------------------

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = m.Width, m.Height
		return a, nil
	case updatesMsg:
		for _, u := range m {
			a.store.Apply(u)
		}
		return a, a.readFeed()
	case tickMsg:
		return a, tick()
	case statusMsg:
		a.status = string(m)
		return a, nil
	case tea.PasteMsg:
		return a, a.paste(m.Content)
	case tea.KeyPressMsg:
		return a, a.key(m)
	case tea.MouseClickMsg:
		return a, a.click(m.Mouse())
	case tea.MouseWheelMsg:
		return a, a.wheel(m.Mouse())
	}
	return a, nil
}

func (a *App) paste(text string) tea.Cmd {
	switch a.mode.kind {
	case "compose":
		a.ta.InsertString(strings.ReplaceAll(text, "\r\n", "\n"))
	case "command", "search":
		a.mode.value += strings.ReplaceAll(view.OneLine(text), " ⏎ ", " ")
	}
	return nil
}

func (a *App) setStatus(s string) tea.Cmd {
	a.status = s
	return nil
}

func isKey(k tea.KeyPressMsg, code rune) bool { return k.Code == code && k.Mod == 0 }

func (a *App) key(k tea.KeyPressMsg) tea.Cmd {
	s := a.screen()
	a.last = s
	if a.mode.kind == "confirm" {
		run := a.mode.run
		a.mode = mode{kind: "normal"}
		if k.Text == "y" {
			a.status = "…"
			return run()
		}
		return a.setStatus("cancelled")
	}
	if a.mode.kind != "normal" {
		return a.inputKey(k, s)
	}
	switch {
	case k.Text == "q" || (k.Code == 'c' && k.Mod == tea.ModCtrl):
		a.quitting = true
		return tea.Quit
	case k.Text == "?":
		if a.overlay != nil && a.overlay.kind == "help" {
			a.overlay = nil
		} else {
			a.overlay = &overlay{kind: "help"}
		}
		a.scroll = 0
		return nil
	case isKey(k, tea.KeyEscape):
		if a.overlay != nil {
			a.overlay = nil
			a.scroll = -1
			return nil
		}
		a.search, a.agentFilter = "", ""
		return a.setStatus("filters cleared")
	case isKey(k, tea.KeyTab):
		if a.focus == "main" {
			a.cursor = a.cursorOf(s)
			a.focus = "sidebar"
		} else {
			a.focus = "main"
		}
		return nil
	case k.Text == "[":
		a.sidebar = !a.sidebar
		if !a.sidebar {
			a.focus = "main"
		}
		return nil
	case isKey(k, tea.KeyDown) || k.Text == "j" || isKey(k, tea.KeyUp) || k.Text == "k":
		delta := 1
		if isKey(k, tea.KeyUp) || k.Text == "k" {
			delta = -1
		}
		if a.overlay != nil {
			return a.scrollBy(s, delta, false)
		}
		if a.focus == "sidebar" {
			return a.moveSidebar(s, delta)
		}
		if s.lastID == "" {
			return a.scrollBy(s, delta, false)
		}
		from := len(s.main.IDs)
		if s.current != "" {
			from = indexOf(s.main.IDs, s.current)
		}
		if id := nextID(s.main.IDs, from, delta); id != "" {
			a.selectMessage(s, id)
		} else if s.current == "" {
			a.selectMessage(s, s.lastID)
		}
		return nil
	case isKey(k, tea.KeyPgDown) || isKey(k, tea.KeyPgUp):
		delta := s.mainBodyH - 1
		if isKey(k, tea.KeyPgUp) {
			delta = -delta
		}
		a.scrollBy(s, delta, false)
		off := a.viewOffset(s)
		var inWindow []string
		for _, id := range s.main.IDs[min(off, len(s.main.IDs)):min(off+s.mainBodyH, len(s.main.IDs))] {
			if id != "" {
				inWindow = append(inWindow, id)
			}
		}
		if len(inWindow) > 0 && a.overlay == nil {
			if isKey(k, tea.KeyPgDown) {
				a.msgID = inWindow[len(inWindow)-1]
			} else {
				a.msgID = inWindow[0]
			}
		}
		return nil
	case isKey(k, tea.KeyHome):
		a.scroll, a.follow = 0, false
		if first := nextID(s.main.IDs, -1, 1); first != "" && a.overlay == nil {
			a.msgID = first
		}
		return nil
	case isKey(k, tea.KeyEnd):
		if a.overlay == nil && a.view == viewTranscript {
			a.follow, a.scroll = true, -1
			return nil
		}
		return a.scrollBy(s, len(s.main.Lines), false)
	case isKey(k, tea.KeyEnter):
		return a.openCurrent(s)
	}
	if a.overlay != nil {
		return nil
	}
	switch k.Text {
	case "1":
		a.view, a.scroll = viewTranscript, -1
	case "2":
		a.view, a.scroll = viewThreads, -1
	case " ":
		a.follow, a.scroll = !a.follow, -1
		if a.follow {
			return a.setStatus("follow on")
		}
		return a.setStatus("follow off")
	case "/":
		a.mode = mode{kind: "search", value: a.search}
	case ":":
		a.mode = mode{kind: "command"}
	case "a":
		return a.nextAttention(s)
	case "m":
		if a.sid == model.AllSessions {
			return a.setStatus("pick a session first (tab, then ↑↓)")
		}
		a.compose(wire.Broadcast, nil)
	case "r":
		m := s.v.Msgs[s.current]
		if m == nil {
			return a.setStatus("select a message first")
		}
		if m.From == wire.Operator {
			return a.setStatus("that is your own message")
		}
		a.compose(m.From, &reply{id: m.ID, to: m.From})
	}
	return nil
}

func (a *App) compose(to string, r *reply) {
	a.mode = mode{kind: "compose", to: to, reply: r}
	a.ta.Reset()
	a.ta.SetWidth(max(10, a.width-20))
	a.ta.SetHeight(1)
	a.ta.Focus()
}

// inputKey handles the composer, the command line, and the search line.
func (a *App) inputKey(k tea.KeyPressMsg, s screen) tea.Cmd {
	m := &a.mode
	switch {
	case isKey(k, tea.KeyEscape):
		a.mode = mode{kind: "normal"}
		return nil
	case k.Code == tea.KeyEnter && k.Mod == tea.ModAlt && m.kind == "compose":
		a.ta.InsertString("\n")
		a.ta.SetHeight(min(4, a.ta.LineCount()))
		return nil
	case isKey(k, tea.KeyEnter):
		done := *m
		a.mode = mode{kind: "normal"}
		switch done.kind {
		case "compose":
			return a.submit(done, s)
		case "command":
			return a.command(strings.TrimSpace(done.value), s)
		}
		a.search = strings.TrimSpace(done.value)
		if a.search == "" {
			return a.setStatus("search cleared")
		}
		return a.setStatus("search: " + a.search)
	case isKey(k, tea.KeyTab):
		if m.kind == "command" {
			m.value = Complete(m.value, s.v.AgentList())
			return nil
		}
		if m.kind == "compose" && m.reply == nil {
			targets := []string{wire.Broadcast}
			for _, ag := range s.v.AgentList() {
				targets = append(targets, ag.Address)
			}
			i := indexOf(targets, m.to)
			m.to = targets[(i+1)%len(targets)]
		}
		return nil
	}
	if m.kind == "compose" {
		var cmd tea.Cmd
		a.ta, cmd = a.ta.Update(k)
		a.ta.SetHeight(min(4, max(1, a.ta.LineCount())))
		return cmd
	}
	if isKey(k, tea.KeyBackspace) || isKey(k, tea.KeyDelete) {
		r := []rune(m.value)
		if len(r) > 0 {
			m.value = string(r[:len(r)-1])
		}
		return nil
	}
	if k.Text != "" && k.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
		m.value += k.Text
	}
	return nil
}

func (a *App) scrollTo(line int) {
	a.scroll, a.follow = max(0, line), false
}

// scrollBy moves the main pane; at the bottom of a live view it follows again.
func (a *App) scrollBy(s screen, delta int, live bool) tea.Cmd {
	maxOff := max(0, len(s.main.Lines)-s.mainBodyH)
	next := min(maxOff, max(0, a.viewOffset(s)+delta))
	canFollow := a.overlay == nil && a.view == viewTranscript
	if live && canFollow && next >= maxOff && delta > 0 {
		a.follow, a.scroll = true, -1
		return nil
	}
	a.scrollTo(next)
	return nil
}

func (a *App) selectMessage(s screen, id string) {
	line := indexOf(s.main.IDs, id)
	off := a.viewOffset(s)
	a.msgID, a.follow = id, false
	if line >= 0 {
		a.scroll = reveal(line, off, s.mainBodyH)
	}
}

// chooseSession shows sid; the cursor and the selection start over.
func (a *App) chooseSession(sid string, cursor int) {
	if sid == a.sid {
		a.cursor = cursor
		return
	}
	a.sid, a.cursor = sid, cursor
	a.msgID, a.agentAddr, a.agentFilter = "", "", ""
	a.overlay = nil
	a.follow, a.scroll, a.attn = true, -1, 0
}

// moveSidebar moves the cursor to the next row with content. The cursor only points; enter
// shows a session or opens an agent.
func (a *App) moveSidebar(s screen, dir int) tea.Cmd {
	rows := s.sidebar.Rows
	i := a.cursorOf(s)
	for range rows {
		next := min(len(rows)-1, max(0, i+dir))
		if next == i {
			return nil
		}
		i = next
		if rows[i] == nil {
			continue
		}
		a.cursor = i
		if rows[i].Address != "" {
			a.agentAddr = rows[i].Address
		}
		return nil
	}
	return nil
}

func (a *App) openSidebarRow(s screen, index int) tea.Cmd {
	if index < 0 || index >= len(s.sidebar.Rows) || s.sidebar.Rows[index] == nil {
		a.focus, a.cursor = "sidebar", index
		return nil
	}
	row := s.sidebar.Rows[index]
	if row.SID != "" {
		a.chooseSession(row.SID, index)
		a.focus = "main"
		return nil
	}
	a.cursor, a.agentAddr = index, row.Address
	a.overlay = &overlay{kind: "agent", id: row.Address}
	a.scroll = -1
	return nil
}

func (a *App) openCurrent(s screen) tea.Cmd {
	if a.overlay != nil {
		return nil
	}
	if a.focus == "sidebar" {
		return a.openSidebarRow(s, a.cursorOf(s))
	}
	if s.current != "" {
		a.msgID, a.follow = s.current, false
		a.overlay = &overlay{kind: "message", id: s.current}
		a.scroll = -1
	}
	return nil
}

func (a *App) nextAttention(s screen) tea.Cmd {
	if len(s.items) == 0 {
		return a.setStatus("nothing needs you right now")
	}
	item := s.items[a.attn%len(s.items)]
	a.attn++
	a.status = view.Describe(item)
	if item.Kind == "blocked" {
		a.agentAddr = item.Address
		a.overlay = &overlay{kind: "agent", id: item.Address}
		a.scroll = -1
		return nil
	}
	a.view, a.overlay = viewTranscript, nil
	a.selectMessage(a.screen(), item.ID)
	return nil
}

// --- Mouse -------------------------------------------------------------------------------------

func (a *App) click(m tea.Mouse) tea.Cmd {
	if m.Button != tea.MouseLeft {
		return nil
	}
	s := a.screen()
	a.last = s
	y := m.Y - 1 // row 0 is the title
	if y < 0 || y >= s.bodyH {
		return nil
	}
	if a.sidebar && m.X < s.sidebarW {
		sideOffset := listOffset(a.cursorOf(s), len(s.sidebar.Lines), s.bodyH)
		index := y + sideOffset
		var row *view.Row
		if index < len(s.sidebar.Rows) {
			row = s.sidebar.Rows[index]
		}
		switch {
		case row != nil && row.Address != "" && a.agentAddr == row.Address && a.focus == "sidebar":
			return a.openSidebarRow(s, index)
		case row != nil && row.SID != "":
			a.chooseSession(row.SID, index)
			a.focus = "sidebar"
		case row != nil:
			a.focus, a.cursor, a.agentAddr = "sidebar", index, row.Address
		default:
			a.focus = "sidebar"
		}
		return nil
	}
	line := y + a.viewOffset(s)
	if s.attn != nil {
		line--
	}
	id := ""
	if line >= 0 && line < len(s.main.IDs) {
		id = s.main.IDs[line]
	}
	wasMain := a.focus == "main"
	a.focus = "main"
	if id == "" || a.overlay != nil {
		return nil
	}
	if id == a.msgID && !s.following && wasMain {
		a.overlay = &overlay{kind: "message", id: id}
		a.scroll = -1
		return nil
	}
	a.selectMessage(s, id)
	return nil
}

func (a *App) wheel(m tea.Mouse) tea.Cmd {
	s := a.screen()
	delta := wheelLines
	if m.Button == tea.MouseWheelUp {
		delta = -wheelLines
	} else if m.Button != tea.MouseWheelDown {
		return nil
	}
	if a.sidebar && m.X < s.sidebarW {
		if delta > 0 {
			return a.moveSidebar(s, 1)
		}
		return a.moveSidebar(s, -1)
	}
	return a.scrollBy(s, delta, true)
}

// --- Actions -----------------------------------------------------------------------------------

func (a *App) act(f func(ctx context.Context) (string, error)) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		status, err := f(ctx)
		if err != nil {
			return statusMsg("error: " + err.Error())
		}
		return statusMsg(status)
	}
}

// submit sends what the composer holds.
func (a *App) submit(m mode, s screen) tea.Cmd {
	session := a.sid
	value := strings.TrimSpace(a.ta.Value())
	if session == model.AllSessions || value == "" {
		return nil
	}
	to, text, away := wire.Broadcast, value, ""
	direct := func(address string) bool {
		var hit *model.Agent
		for _, ag := range s.v.AgentList() {
			if ag.Address == address {
				hit = ag
			}
		}
		if _, ok := wire.ParseAddress(address); !ok || hit == nil {
			return false
		}
		to = address
		if !hit.Online {
			away = hit.Address
		}
		return true
	}
	switch {
	case m.reply != nil:
		if !direct(m.reply.to) {
			return a.setStatus("no agent " + m.reply.to)
		}
	case m.to == wire.Broadcast && strings.HasPrefix(value, "@"):
		name, rest, _ := strings.Cut(value[1:], " ")
		rest = strings.TrimSpace(rest)
		if rest == "" {
			return a.setStatus("no text after @" + name)
		}
		var hits []string
		for _, ag := range s.v.AgentList() {
			if ag.Address == name || strings.HasPrefix(ag.Address, name+"@") {
				hits = append(hits, ag.Address)
			}
		}
		if len(hits) == 0 {
			return a.setStatus("no agent " + name)
		}
		if len(hits) > 1 {
			return a.setStatus(name + " is ambiguous")
		}
		if !direct(hits[0]) {
			return nil
		}
		text = rest
	case m.to != wire.Broadcast:
		if !direct(m.to) {
			return a.setStatus("no agent " + m.to)
		}
	}
	if len([]rune(text)) > wire.MaxText {
		return a.setStatus("too long (max 8000)")
	}
	note := ""
	if away != "" {
		note = " (" + away + " is away; it gets it when it returns)"
	}
	replyTo := ""
	if m.reply != nil {
		replyTo = m.reply.id
	}
	return a.act(func(ctx context.Context) (string, error) {
		id, err := a.op.Send(ctx, session, to, text, replyTo)
		return "sent #" + id + note, err
	})
}

// --- View --------------------------------------------------------------------------------------

func (a *App) View() tea.View {
	v := tea.NewView(a.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (a *App) title(s screen) string {
	where := a.sid
	if a.sid == model.AllSessions {
		where = "all sessions"
	}
	link := ""
	if a.store.Link != model.LinkLive {
		link = "  [" + string(a.store.Link) + "]"
	}
	if o := a.overlay; o != nil {
		switch o.kind {
		case "help":
			return "coop · help · esc back" + link
		case "message":
			return "coop · " + where + " · message #" + o.id + " · esc back" + link
		case "agent":
			return "coop · " + where + " · agent " + o.id + " · esc back" + link
		}
	}
	t := "coop · " + where + " · " + a.view
	if a.agentFilter != "" {
		t += "  [" + a.agentFilter + "]"
	}
	if a.search != "" {
		t += "  [/" + a.search + "]"
	}
	if s.following {
		t += "  follow ●"
	}
	return t + link
}

func (a *App) hint() string {
	switch a.mode.kind {
	case "compose":
		return "enter send · alt+enter new line · tab target · esc cancel"
	case "command":
		return "enter run · tab complete · esc cancel"
	case "search":
		return "enter search · esc cancel"
	case "confirm":
		return "y yes · n no"
	}
	if a.overlay != nil {
		return "esc back · ↑↓ scroll · ? help · q quit"
	}
	if a.focus == "sidebar" {
		return "↑↓ move · enter open · [ hide · tab main pane · : command · ? help · q quit"
	}
	return "↑↓ enter esc · m message · r reply · a attention · / search · : command · tab sidebar · ? help · q quit"
}

func (a *App) promptLines(s screen) []string {
	switch a.mode.kind {
	case "compose":
		label := "to " + a.mode.to + " (tab: next) › "
		if a.mode.reply != nil {
			label = "reply to #" + a.mode.reply.id + " from " + a.mode.reply.to + " › "
		}
		lines := strings.Split(a.ta.View(), "\n")
		if len(lines) > s.promptH {
			lines = lines[len(lines)-s.promptH:]
		}
		out := make([]string, len(lines))
		pad := strings.Repeat(" ", view.Width(label))
		for i, l := range lines {
			if i == 0 {
				out[i] = label + l
			} else {
				out[i] = pad + l
			}
		}
		return out
	case "command":
		return []string{":" + a.mode.value + "▏"}
	case "search":
		return []string{"search › " + a.mode.value + "▏"}
	case "confirm":
		return []string{a.mode.text}
	}
	return []string{a.status}
}
