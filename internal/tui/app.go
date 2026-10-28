// Package tui is the terminal program that shows the sessions: a sidebar (sessions, then the
// agents of the shown session), a main pane (transcript or threads) with a preview pane under
// it for the details of a message or an agent, and a `:` command line. It only shows: it sends
// nothing to the agents. It paints what the view package returns.
//
// The main pane is a window over a buffer of blocks (view.Pane): a frame renders the visible
// blocks only, so the keys answer at once however long the session is.
package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/AIToolSharing/agent_coop/internal/model"
	"github.com/AIToolSharing/agent_coop/internal/tui/view"
)

// Hub is what the shell asks the hub to do. *admin.Client is one.
type Hub interface {
	DeleteSession(ctx context.Context, sid string) error
}

// Options builds an App.
type Options struct {
	Store *model.Store
	// Updates carries the feed; nil in tests.
	Updates <-chan model.Update
	Op      Hub
	Now     func() time.Time
	Loc     *time.Location
}

const (
	viewTranscript = "transcript"
	viewThreads    = "threads"
	wheelLines     = 3
	actionTimeout  = 10 * time.Second
)

type mode struct {
	kind  string // normal command search confirm
	value string // command and search
	text  string // confirm
	run   func() tea.Cmd
}

// App is the tea.Model. All state lives here; Update reads and writes it in one goroutine.
type App struct {
	store   *model.Store
	updates <-chan model.Update
	op      Hub
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
	help          bool
	preview       bool
	follow        bool
	system        bool
	agentFilter   string
	search        string      // the last search
	pending       string      // a first g, for gg
	top           view.Anchor // the first visible line of the main pane when not following
	helpTop       view.Anchor
	mode          mode
	status        string

	transcripts map[string]*view.Transcript
	threads     view.Threads
	agentView   view.AgentView
	last        screen
	quitting    bool
	err         error
}

// Err is the error that ended the program, or nil.
func (a *App) Err() error { return a.err }

// Messages.
type updatesMsg []model.Update
type tickMsg time.Time
type statusMsg string

// FeedError says that the feed ended with an error the program cannot recover from, such as a
// refused token. The program quits; Err gives the error after Run.
type FeedError struct{ Err error }

func New(o Options) *App {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Loc == nil {
		o.Loc = time.Local
	}
	return &App{
		store: o.Store, updates: o.Updates, op: o.Op, now: o.Now, loc: o.Loc,

		width: 80, height: 24,
		sid: model.AllSessions, view: viewTranscript, focus: "main", sidebar: true,
		cursor: -1, follow: true, system: true,
		mode:        mode{kind: "normal"},
		transcripts: map[string]*view.Transcript{},
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
	v         *model.Session
	sums      []view.Summary
	sidebar   view.Sidebar
	sidebarW  int // columns of the sidebar, border included; 0 when hidden
	mainW     int
	pane      *view.Pane    // the main buffer, rendered on demand for this frame
	main      view.Rendered // the visible lines of the main pane
	top       view.Anchor   // the anchor the window used
	end       view.Anchor   // the anchor of the window at the bottom
	mainH     int           // rows of the main window
	preview   view.Rendered // the preview pane; empty when off
	previewH  int
	current   string // the message under the cursor: the newest one while following
	lastID    string // the last message of the buffer
	following bool
	bodyH     int
}

func (a *App) options(width int) view.Options {
	return view.Options{Width: width, Now: a.now(), Loc: a.loc, Agent: a.agentFilter, System: a.system, Selected: a.msgID}
}

func (a *App) screen() screen {
	s := screen{v: a.store.View(a.sid)}
	now := a.now()
	s.sums = view.Summaries(a.store, now)
	agents := view.Listed(s.v)
	if a.sidebar {
		s.sidebarW = view.SidebarWidth(s.sums, agents, max(22, a.width/3), now)
	}
	cursor := -1
	if a.focus == "sidebar" {
		cursor = a.cursor
	}
	s.sidebar = view.RenderSidebar(s.sums, agents, view.Selection{SID: a.sid, Cursor: cursor}, max(1, s.sidebarW-1), now)
	s.mainW = max(20, a.width-s.sidebarW)
	o := a.options(s.mainW)
	// The rows that are not the body: the title, the prompt line and the hint line.
	s.bodyH = max(4, a.height-3)
	s.mainH = s.bodyH
	if a.preview && !a.help {
		s.previewH = min(max(3, s.bodyH/3), s.bodyH-2)
		s.mainH = s.bodyH - s.previewH - 1
	}
	var buf view.Buffer
	top := a.top
	switch {
	case a.help:
		buf = helpBuffer()
		top = a.helpTop
	case a.view == viewThreads:
		buf = a.threads.Render(s.v, o)
	default:
		t := a.transcripts[a.sid]
		if t == nil {
			t = &view.Transcript{}
			a.transcripts[a.sid] = t
		}
		buf = t.Render(s.v, o)
	}
	s.pane = &view.Pane{Buf: buf, O: o}
	s.end = s.pane.End(s.mainH)
	s.following = a.follow && !a.help && a.view != viewThreads
	if s.following {
		top = s.end
	}
	s.main, s.top = s.pane.Window(top, s.mainH)
	if i := buf.Next(len(buf.Blocks), -1); i >= 0 {
		s.lastID = buf.Blocks[i].ID
	}
	s.current = a.msgID
	if s.following {
		s.current = s.lastID
	}
	if a.preview && !a.help {
		var content view.Buffer
		if row := a.sidebarRow(s); a.focus == "sidebar" && row != nil && row.Address != "" {
			content = a.agentView.Render(s.v, row.Address, o)
		} else {
			content = view.RenderMessage(s.v, s.current, o)
		}
		p := &view.Pane{Buf: content, O: o}
		s.preview, _ = p.Window(view.Anchor{}, s.previewH)
	}
	return s
}

// sidebarRow is the row under the sidebar cursor, or nil.
func (a *App) sidebarRow(s screen) *view.Row {
	i := a.cursorOf(s)
	if i < 0 || i >= len(s.sidebar.Rows) {
		return nil
	}
	return s.sidebar.Rows[i]
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

func listOffset(index, length, height int) int {
	if index < 0 || length <= height {
		return 0
	}
	return min(length-height, max(0, index-height+1))
}

// anchor is the anchor of the shown buffer: the help's own while the help is open.
func (a *App) anchor() *view.Anchor {
	if a.help {
		return &a.helpTop
	}
	return &a.top
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
		// The repaint moves the relative times ("2m ago").
		return a, tick()
	case statusMsg:
		a.status = string(m)
		return a, nil
	case FeedError:
		a.err = m.Err
		return a, tea.Quit
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
	if a.mode.kind == "command" || a.mode.kind == "search" {
		a.mode.value += strings.ReplaceAll(view.OneLine(text), " ⏎ ", " ")
	}
	return nil
}

func (a *App) setStatus(s string) tea.Cmd {
	a.status = s
	return nil
}

func isKey(k tea.KeyPressMsg, code rune) bool { return k.Code == code && k.Mod == 0 }

func isCtrl(k tea.KeyPressMsg, code rune) bool { return k.Code == code && k.Mod == tea.ModCtrl }

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
	if a.pending == "g" {
		a.pending = ""
		if k.Text == "g" {
			a.goTop(s)
			return nil
		}
	}
	inMain := a.focus == "main" || a.help
	switch {
	case k.Text == "q" || isCtrl(k, 'c'):
		a.quitting = true
		return tea.Quit
	case k.Text == "?":
		a.help, a.helpTop = !a.help, view.Anchor{}
		return nil
	case isKey(k, tea.KeyEscape):
		switch {
		case a.help:
			a.help = false
		case a.preview:
			a.preview = false
		default:
			a.search, a.agentFilter = "", ""
			return a.setStatus("filters cleared")
		}
		return nil
	case isKey(k, tea.KeyTab):
		if a.focus == "main" {
			a.focusSidebar(s)
		} else {
			a.focus = "main"
		}
		return nil
	case k.Text == "h" || isKey(k, tea.KeyLeft):
		a.focusSidebar(s)
		return nil
	case k.Text == "l" || isKey(k, tea.KeyRight):
		a.focus = "main"
		return nil
	case k.Text == "[":
		a.sidebar = !a.sidebar
		if !a.sidebar {
			a.focus = "main"
		}
		return nil
	case k.Text == "j" || isKey(k, tea.KeyDown) || k.Text == "k" || isKey(k, tea.KeyUp):
		dir := 1
		if k.Text == "k" || isKey(k, tea.KeyUp) {
			dir = -1
		}
		switch {
		case a.help:
			a.scrollLines(s, dir)
		case a.focus == "sidebar":
			return a.moveSidebar(s, dir)
		default:
			a.moveCursor(s, dir)
		}
		return nil
	case k.Text == "g":
		a.pending = "g"
		return nil
	case k.Text == "G" || isKey(k, tea.KeyEnd):
		a.goBottom(s)
		return nil
	case isKey(k, tea.KeyHome):
		a.goTop(s)
		return nil
	case isCtrl(k, 'd') || isCtrl(k, 'u'):
		n := max(1, s.mainH/2)
		if isCtrl(k, 'u') {
			n = -n
		}
		a.scrollLines(s, n)
		return nil
	case isCtrl(k, 'f') || isKey(k, tea.KeyPgDown) || isCtrl(k, 'b') || isKey(k, tea.KeyPgUp):
		n := max(1, s.mainH-1)
		if isCtrl(k, 'b') || isKey(k, tea.KeyPgUp) {
			n = -n
		}
		a.scrollLines(s, n)
		return nil
	case isCtrl(k, 'e') || isCtrl(k, 'y'):
		n := 1
		if isCtrl(k, 'y') {
			n = -1
		}
		a.scrollLine(s, n)
		return nil
	case isKey(k, tea.KeyEnter):
		return a.openCurrent(s)
	}
	if a.help {
		return nil
	}
	switch k.Text {
	case "1":
		a.view, a.top = viewTranscript, view.Anchor{}
	case "2":
		a.view, a.top = viewThreads, view.Anchor{}
	case " ":
		a.follow = !a.follow
		if a.follow {
			return a.setStatus("follow on")
		}
		a.top = s.top
		return a.setStatus("follow off")
	case "/":
		a.mode = mode{kind: "search"}
	case "n", "N":
		if !inMain {
			return nil
		}
		dir := 1
		if k.Text == "N" {
			dir = -1
		}
		return a.find(s, dir)
	case ":":
		a.mode = mode{kind: "command"}
	}
	return nil
}

// inputKey handles the command line and the search line.
func (a *App) inputKey(k tea.KeyPressMsg, s screen) tea.Cmd {
	m := &a.mode
	switch {
	case isKey(k, tea.KeyEscape):
		a.mode = mode{kind: "normal"}
		return nil
	case isKey(k, tea.KeyEnter):
		done := *m
		a.mode = mode{kind: "normal"}
		if done.kind == "command" {
			return a.command(strings.TrimSpace(done.value), s)
		}
		a.search = strings.TrimSpace(done.value)
		if a.search == "" {
			return a.setStatus("search cleared")
		}
		return a.find(s, 1)
	case isKey(k, tea.KeyTab):
		if m.kind == "command" {
			m.value = Complete(m.value, s.v.AgentList())
		}
		return nil
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

func (a *App) focusSidebar(s screen) {
	if !a.sidebar {
		return
	}
	a.cursor = a.cursorOf(s)
	a.focus = "sidebar"
}

// scrollLine moves the window by n lines; the cursor stays where it is.
func (a *App) scrollLine(s screen, n int) {
	a.follow = false
	*a.anchor() = s.pane.Advance(s.top, n)
}

// scrollLines moves the window by n lines and puts the cursor on the last message of the new
// window (n > 0) or its first one.
func (a *App) scrollLines(s screen, n int) {
	a.scrollLine(s, n)
	if a.help {
		return
	}
	r, _ := s.pane.Window(*a.anchor(), s.mainH)
	for i := range r.IDs {
		j := i
		if n > 0 {
			j = len(r.IDs) - 1 - i
		}
		if r.IDs[j] != "" {
			a.msgID = r.IDs[j]
			return
		}
	}
}

// goTop shows the first line and puts the cursor on the first message.
func (a *App) goTop(s screen) {
	a.follow = false
	*a.anchor() = view.Anchor{}
	if i := s.pane.Buf.Next(-1, 1); i >= 0 && !a.help {
		a.msgID = s.pane.Buf.Blocks[i].ID
	}
}

// goBottom follows the newest message in a live view; elsewhere it shows the last line.
func (a *App) goBottom(s screen) {
	if !a.help && a.view != viewThreads {
		a.follow = true
		return
	}
	*a.anchor() = s.end
	if !a.help {
		a.msgID = s.lastID
	}
}

// moveCursor puts the cursor on the next (dir 1) or previous (dir -1) message and reveals it.
func (a *App) moveCursor(s screen, dir int) {
	buf := s.pane.Buf
	from := buf.Find(s.current)
	if from < 0 {
		from = -1
		if dir < 0 {
			from = len(buf.Blocks)
		}
	}
	i := buf.Next(from, dir)
	if i < 0 {
		if s.current != "" {
			return
		}
		i = buf.Next(len(buf.Blocks), -1)
		if i < 0 {
			return
		}
	}
	a.selectBlock(s, i)
}

// selectBlock puts the cursor on block i and moves the window the least to show it.
func (a *App) selectBlock(s screen, i int) {
	a.msgID, a.follow = s.pane.Buf.Blocks[i].ID, false
	a.top = s.pane.Reveal(s.top, i, s.mainH)
}

// find moves the cursor to the next (dir 1) or previous (dir -1) message whose text contains
// the last search, around the end if it must.
func (a *App) find(s screen, dir int) tea.Cmd {
	if a.search == "" {
		return a.setStatus("no search: / first")
	}
	if a.help {
		return nil
	}
	buf := s.pane.Buf
	n := len(buf.Blocks)
	from := buf.Find(s.current)
	if from < 0 {
		from = -1
		if dir < 0 {
			from = n
		}
	}
	want := strings.ToLower(a.search)
	wrapped := false
	for step, i := 1, from+dir; step <= n; step, i = step+1, i+dir {
		if i < 0 || i >= n {
			i = (i + n) % n
			wrapped = true
		}
		b := buf.Blocks[i]
		if b.ID == "" || !strings.Contains(b.Text, want) {
			continue
		}
		a.selectBlock(s, i)
		switch {
		case wrapped && dir > 0:
			return a.setStatus("search hit BOTTOM, continuing at TOP")
		case wrapped:
			return a.setStatus("search hit TOP, continuing at BOTTOM")
		}
		return a.setStatus("/" + a.search)
	}
	return a.setStatus("no match: " + a.search)
}

// chooseSession shows sid; the cursor and the selection start over.
func (a *App) chooseSession(sid string, cursor int) {
	if sid == a.sid {
		a.cursor = cursor
		return
	}
	a.sid, a.cursor = sid, cursor
	a.msgID, a.agentAddr, a.agentFilter = "", "", ""
	a.follow, a.top = true, view.Anchor{}
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
	a.preview = !a.preview
	return nil
}

// openCurrent opens what the cursor is on: a session, or the preview of an agent or a message.
func (a *App) openCurrent(s screen) tea.Cmd {
	if a.help {
		return nil
	}
	if a.focus == "sidebar" {
		return a.openSidebarRow(s, a.cursorOf(s))
	}
	if s.current == "" {
		return nil
	}
	a.msgID = s.current
	a.preview = !a.preview
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
	wasMain := a.focus == "main"
	a.focus = "main"
	if a.help || y >= len(s.main.IDs) {
		return nil
	}
	id := s.main.IDs[y]
	if id == "" {
		return nil
	}
	if id == a.msgID && !s.following && wasMain {
		a.preview = true
		return nil
	}
	a.msgID, a.follow, a.top = id, false, s.top
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
	next := s.pane.Advance(s.top, delta)
	// At the bottom of a live view the wheel follows again.
	if delta > 0 && !a.help && a.view != viewThreads && !view.Before(next, s.end) {
		a.follow = true
		return nil
	}
	a.follow = false
	*a.anchor() = next
	return nil
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
	if a.help {
		return "coop · help · esc back" + link
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
	case "command":
		return "enter run · tab complete · esc cancel"
	case "search":
		return "enter search · esc cancel"
	case "confirm":
		return "y yes · n no"
	}
	if a.help {
		return "esc back · j k scroll · q quit"
	}
	if a.focus == "sidebar" {
		return "j k move · enter open · l main · ? help"
	}
	if a.preview {
		return "j k move · enter esc close the preview · / search · ? help"
	}
	return "j k move · G gg · enter preview · / search · n N · : command · h sidebar · ? help · q quit"
}

// prompt is the line under the body: the command line, the search line, a question, or the
// status.
func (a *App) prompt() string {
	switch a.mode.kind {
	case "command":
		return ":" + a.mode.value + "▏"
	case "search":
		return "/" + a.mode.value + "▏"
	case "confirm":
		return a.mode.text
	}
	return a.status
}
