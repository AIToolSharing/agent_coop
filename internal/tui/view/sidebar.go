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

// Summaries gives the all-traffic row, then every session in name order.
func Summaries(s *model.Store, now time.Time) []Summary {
	minuteAgo := now.Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	one := func(sid, status string) Summary {
		v := s.View(sid)
		sum := Summary{SID: sid, Status: status, OpenAsks: len(v.OpenAsks())}
		for _, a := range v.Members() {
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
}

// SidebarWidth is the narrowest width that shows every row whole, at most maxWidth.
func SidebarWidth(sums []Summary, agents []*model.Agent, maxWidth int) int {
	longest := 14
	for _, s := range sums {
		longest = max(longest, Width(s.SID)+14)
	}
	for _, a := range agents {
		longest = max(longest, Width(a.Address)+18)
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
	if len(agents) == 0 {
		add(Line{Dim("  none yet")}, nil)
	}
	for _, a := range agents {
		here := len(sb.Rows) == sel.Cursor
		state := Color(a.State, StateColor(a.State))
		if a.Kicked {
			state = Color("removed", "red")
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
		l := Line{Color(icon, color), Styled(strings.TrimRight(Cut(a.Address, nameW), " "), Style{Bold: true, Inverse: here}), S(" "), state}
		if a.Waiting != nil {
			l = append(l, Color(" ⏳"+Age(a.Waiting.Since, now), "yellow"))
		}
		add(l, &Row{Address: a.Address})
	}
	return sb
}
