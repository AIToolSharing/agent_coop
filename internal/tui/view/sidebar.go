package view

import (
	"strings"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/model"
)

// Summary is one session row of the sidebar.
type Summary struct {
	SID      string
	Status   string // open, closed, or all
	Online   int
	Agents   int
	OpenAsks int
	// PerMinute counts the messages of the last minute.
	PerMinute int
}

// RefusedFor is how long a removed agent that tried to join stays in the list.
const RefusedFor = 5 * time.Minute

// LeftFor is how long an agent that left stays in the list.
const LeftFor = 5 * time.Minute

// lately reports whether the time iso is within the last RefusedFor.
func lately(iso string, now time.Time) bool {
	t, ok := parseTime(iso)
	return ok && now.Sub(t) < RefusedFor
}

// gone reports whether the agent left LeftFor ago or earlier.
func gone(a *model.Agent, now time.Time) bool {
	if a.Left == nil {
		return false
	}
	t, ok := parseTime(a.Left.At)
	return ok && now.Sub(t) >= LeftFor
}

// Listed gives the agents that the sidebar shows and the session count takes: every agent but
// those the operator removed or forgot, and those that left LeftFor ago or earlier. A short
// run, such as a headless agent that did its task, must not fill the list for good. A removed
// agent that tried to join lately is listed, so that the operator sees that it waits for
// :allow. AgentList keeps every agent, for :allow, for :forget and for the history.
func Listed(v *model.Session, now time.Time) []*model.Agent {
	all := v.AgentList()
	out := all[:0]
	for _, a := range all {
		hidden := a.Kicked || a.Forgotten || gone(a, now)
		if !hidden || a.Online || lately(a.RefusedAt, now) {
			out = append(out, a)
		}
	}
	return out
}

// Summaries gives the all-traffic row, then every session in name order.
func Summaries(s *model.Store, now time.Time) []Summary {
	minuteAgo := now.Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	one := func(sid, status string) Summary {
		v := s.View(sid)
		sum := Summary{SID: sid, Status: status, OpenAsks: len(v.OpenAsks())}
		for _, a := range Listed(v, now) {
			sum.Agents++
			if a.Online {
				sum.Online++
			}
		}
		// Count from the newest message back to the first one older than a minute. The scan
		// stops after 1000 messages: a rate past that is noise, and a clock ahead of ours
		// must not make every repaint walk the whole history.
		msgs := v.Messages()
		floor := mustTime(minuteAgo)
		for i, n := len(msgs)-1, 0; i >= 0 && n < 1000; i, n = i-1, n+1 {
			t, ok := parseTime(msgs[i].SentAt)
			if !ok || t.Before(floor) {
				break
			}
			if !t.After(now) {
				sum.PerMinute++
			}
		}
		return sum
	}
	out := []Summary{one(model.AllSessions, "all")}
	for _, sid := range s.SessionIDs() {
		out = append(out, one(sid, s.Sessions[sid].Status))
	}
	return out
}

func mustTime(iso string) time.Time {
	t, _ := parseTime(iso)
	return t
}

// gateText is what the agent list shows in place of the state for an agent that the operator
// does not let work: held or paused. An agent with no gate hook gets the gate only as advice:
// the text then says "soft". "" for an agent that may work.
func gateText(a *model.Agent) string {
	if a.Gate != "held" && a.Gate != "paused" {
		return ""
	}
	if a.Online && !a.Gated {
		// Short, so that the name stays whole: the agent's details say the rest.
		return a.Gate + " (soft)"
	}
	return a.Gate
}

// Row says what a sidebar line stands for: a session or an agent. Headers have no row.
type Row struct {
	SID     string
	Address string
}

// Sidebar is the rendered sidebar with a row per line.
type Sidebar struct {
	Rendered
	Rows []*Row
}

// Selection is the shown session and, when the sidebar has the focus, the cursor row.
type Selection struct {
	SID    string
	Cursor int // -1 when the sidebar has no focus
	// Hold is the hold setting of the shown session: new agents wait for a release.
	Hold bool
}

// SidebarWidth is the narrowest width that shows every row whole, at most maxWidth.
func SidebarWidth(sums []Summary, agents []*model.Agent, maxWidth int, now time.Time) int {
	longest := 14
	for _, s := range sums {
		longest = max(longest, Width(s.SID)+14)
	}
	for _, a := range agents {
		longest = max(longest, Width(a.Address)+18)
		if g := gateText(a); g != "" {
			// icon, name, space, gate, and the wait mark when the agent waits.
			wait := 0
			if a.Waiting != nil {
				wait = 6
			}
			longest = max(longest, 2+Width(a.Address)+1+Width(g)+wait+1)
		}
		if lately(a.DuplicateAt, now) {
			longest = max(longest, 30) // "  ↳ duplicate refused 59m ago"
		}
	}
	return min(max(maxWidth, 26), longest)
}

// RenderSidebar draws the sessions, then the agents of the shown session.
func RenderSidebar(sums []Summary, agents []*model.Agent, sel Selection, width int, now time.Time) Sidebar {
	var sb Sidebar
	add := func(l Line, row *Row) {
		sb.Lines = append(sb.Lines, Fit(l, width))
		sb.IDs = append(sb.IDs, "")
		sb.Rows = append(sb.Rows, row)
	}
	add(Line{Styled("SESSIONS", Style{Bold: true, Dim: true})}, nil)
	for _, s := range sums {
		here := len(sb.Rows) == sel.Cursor
		current := s.SID == sel.SID
		if s.Status == "all" {
			rate := ""
			if s.PerMinute > 0 {
				rate = "  " + itoa(int64(s.PerMinute)) + "/min"
			}
			add(Line{Dim("* "), Styled("all traffic", Style{Bold: current, Inverse: here}), Dim(rate)}, &Row{SID: s.SID})
			continue
		}
		live := s.Status == "open" && s.Online > 0
		icon, color := "○ ", "gray"
		if s.Status == "closed" {
			icon = "✕ "
		} else if live {
			icon, color = "● ", "green"
		}
		l := Line{Color(icon, color), Styled(s.SID, Style{Bold: current, Inverse: here}), Dim(" " + itoa(int64(s.Online)) + "/" + itoa(int64(s.Agents)))}
		if s.OpenAsks > 0 {
			plural := ""
			if s.OpenAsks > 1 {
				plural = "s"
			}
			l = append(l, Color(" "+itoa(int64(s.OpenAsks))+" ask"+plural, "yellow"))
		}
		if s.Status == "closed" {
			l = append(l, Dim(" closed"))
		}
		add(l, &Row{SID: s.SID})
	}
	if sel.SID == model.AllSessions {
		return sb
	}
	add(Line{S("")}, nil)
	add(Line{Styled("AGENTS · "+sel.SID, Style{Bold: true, Dim: true})}, nil)
	if sel.Hold {
		add(Line{Dim("  new agents are held (H)")}, nil)
	} else {
		add(Line{Dim("  new agents start at once (H)")}, nil)
	}
	if len(agents) == 0 {
		add(Line{Dim("  none yet")}, nil)
	}
	for _, a := range agents {
		here := len(sb.Rows) == sel.Cursor
		state := Color(a.State, StateColor(a.State))
		if g := gateText(a); g != "" {
			// The gate matters more than the state the agent gave itself: it does no work.
			state = Color(g, "yellow")
		}
		if a.Kicked {
			// Short, so that the name stays whole: the agent's details say the rest.
			text := "removed"
			if lately(a.RefusedAt, now) {
				text = "refused " + Age(a.RefusedAt, now) + " ago"
			}
			state = Color(text, "red")
		}
		wait := 0
		if a.Waiting != nil {
			wait = 6
		}
		nameW := max(6, width-3-Width(state.Text)-wait)
		icon, color := "○ ", "gray"
		if a.Online {
			icon, color = "● ", "green"
		}
		mark := Seg{}
		if a.Orchestrator {
			// The agent that may act for the operator.
			mark = Color("★", "cyan")
			nameW = max(6, nameW-1)
		}
		l := Line{Color(icon, color), Styled(strings.TrimRight(Cut(a.Address, nameW), " "), Style{Bold: true, Inverse: here}), mark, S(" "), state}
		if a.Waiting != nil {
			l = append(l, Color(" ⏳"+Age(a.Waiting.Since, now), "yellow"))
		}
		add(l, &Row{Address: a.Address})
		if l := NowLine(a, now); l != nil {
			// What the agent does at this moment. The line has no row of its own.
			add(l, nil)
		}
		if lately(a.DuplicateAt, now) {
			// A second session asked for this name and the hub refused it. It has no row of
			// its own: its address is this agent's.
			add(Line{S("  "), Color("↳ duplicate refused "+Age(a.DuplicateAt, now)+" ago", "red")}, nil)
		}
	}
	return sb
}
