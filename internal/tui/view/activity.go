package view

import (
	"strings"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/model"
	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// Took is a short length of time from milliseconds: 0.4s, 42s, 3m12s.
func Took(ms int64) string {
	switch {
	case ms < 10_000:
		return itoa(ms/1000) + "." + itoa(ms%1000/100) + "s"
	case ms < 60_000:
		return itoa(ms/1000) + "s"
	}
	return itoa(ms/60_000) + "m" + itoa(ms%60_000/1000) + "s"
}

// toolText is what a tool call does, in one line: the tool, then what the hook said of it.
func toolText(it wire.TraceItem) string {
	if it.Text == "" {
		return it.Tool
	}
	return it.Tool + ": " + it.Text
}

// NowLine is the newest tool call that the agent runs now, with how long it runs: the
// sidebar shows it under the agent. Nil when the agent runs none, or is not in the session.
func NowLine(a *model.Agent, now time.Time) Line {
	if !a.Online {
		return nil
	}
	run := a.Trace.Running()
	if len(run) == 0 {
		return nil
	}
	it := run[len(run)-1]
	return Line{S("  "), Color("▸ ", "yellow"), Dim(Age(it.At, now) + " " + toolText(it))}
}

type actKey struct {
	agent *model.Agent
	id    string
}

// activityLines draws trace items, oldest first. A tool call is one entry, at the place where
// it started, with its result when it has one. The words of an agent and the prompts are
// whole, wrapped to the width.
func activityLines(acts []model.Act, o Options, all bool) []Line {
	starts, ends := map[actKey]bool{}, map[actKey]wire.TraceItem{}
	running := map[actKey]bool{}
	nameW := 0
	for _, x := range acts {
		k := actKey{x.Agent, x.Item.ID}
		switch x.Item.Kind {
		case wire.TraceToolStart:
			starts[k] = true
		case wire.TraceToolEnd:
			ends[k] = x.Item
		}
		if _, seen := running[actKey{x.Agent, ""}]; !seen {
			running[actKey{x.Agent, ""}] = false
			if x.Agent.Online {
				for _, it := range x.Agent.Trace.Running() {
					running[actKey{x.Agent, it.ID}] = true
				}
			}
		}
		nameW = max(nameW, Width(who(x.Agent, all)))
	}
	nameW = min(nameW, 28)
	var out []Line
	for _, x := range acts {
		it, k := x.Item, actKey{x.Agent, x.Item.ID}
		if o.Agent != "" && x.Agent.Address != o.Agent {
			continue
		}
		if o.Search != "" && !strings.Contains(strings.ToLower(it.Tool+" "+it.Text), strings.ToLower(o.Search)) {
			continue
		}
		var mark Seg
		var body string
		style := Style{}
		tail := ""
		switch it.Kind {
		case wire.TraceToolStart:
			body = toolText(it)
			end, ended := ends[k]
			switch {
			case ended && end.Failed:
				mark, tail = Color("✗ ", "red"), " "+Took(end.MS)
			case ended:
				mark, tail = Color("✓ ", "green"), " "+Took(end.MS)
			case running[k]:
				mark, tail = Color("▸ ", "yellow"), " "+Age(it.At, o.Now)+" …"
			default:
				mark, tail = Dim("· "), " no result"
			}
			style.Dim = ended || !running[k]
		case wire.TraceToolEnd:
			if starts[k] {
				continue
			}
			// The start is not in the trace any more, or the hook did not report it.
			body, tail = toolText(it), " "+Took(it.MS)
			mark, style.Dim = Color("✓ ", "green"), true
			if it.Failed {
				mark = Color("✗ ", "red")
			}
		case wire.TracePrompt:
			mark, body, style.Color = Color("» ", "cyan"), it.Text, "cyan"
		default:
			mark, body = S("  "), it.Text
		}
		lead := Line{Dim(Clock(it.At, o.loc()) + "  "), Bold(Cut(who(x.Agent, all), nameW)), S("  "), mark}
		pad := strings.Repeat(" ", Width(Plain(lead)))
		lines := Wrap(body+tail, max(10, o.Width-len(pad)))
		for i, text := range lines {
			l := Line{S(pad)}
			if i == 0 {
				l = append(Line{}, lead...)
			}
			// The result of a tool call stands after its text, in the last line.
			if i == len(lines)-1 && tail != "" && strings.HasSuffix(text, tail) {
				l = append(l, Styled(strings.TrimSuffix(text, tail), style), Dim(tail))
			} else {
				l = append(l, Styled(text, style))
			}
			out = append(out, Fit(l, o.Width))
		}
	}
	return out
}

// who is the name of an agent in the activity list: its address, with the session in the
// view of all sessions.
func who(a *model.Agent, all bool) string {
	if all {
		return a.SID + "/" + a.Address
	}
	return a.Address
}

// RenderActivity shows what the agents of the view do at their terminals: tool calls, their
// own words, and the prompts that a person typed there. Oldest first.
func RenderActivity(v *model.Session, o Options) Rendered {
	lines := activityLines(v.Activity(), o, v.SID == model.AllSessions)
	if len(lines) == 0 {
		return Note("no activity yet: an agent that coop claude started reports its tool calls and its words here", o.Width)
	}
	return Rendered{Lines: lines, IDs: make([]string, len(lines))}
}

// AgentActivityMax is how many of its newest trace items the details of an agent show.
const AgentActivityMax = 40

// agentTrace adds what the trace says to the details of an agent: the files it changed, then
// its newest activity.
func agentTrace(v *model.Session, a *model.Agent, o Options, add func(Line, string)) {
	if files := a.Trace.FileList(); len(files) > 0 {
		add(Line{S("")}, "")
		add(Line{Bold("CHANGED FILES (" + itoa(int64(len(files))) + ")")}, "")
		for _, f := range files {
			l := Line{S("  " + f.Path), Dim("  ×" + itoa(int64(f.Count)) + "  " + Age(f.At, o.Now) + " ago")}
			// A file that a second agent of the session changed too: their changes can collide.
			for _, other := range v.AgentList() {
				if other != a && other.SID == a.SID {
					if _, both := other.Trace.Files[f.Path]; both {
						l = append(l, Color("  also "+other.Address, "red"))
					}
				}
			}
			add(l, "")
		}
	}
	var acts []model.Act
	for _, it := range a.Trace.Items {
		acts = append(acts, model.Act{Agent: a, Item: it})
	}
	if len(acts) == 0 {
		return
	}
	title := "ACTIVITY"
	if len(acts) > AgentActivityMax {
		title += " (the newest " + itoa(AgentActivityMax) + " of " + itoa(int64(len(acts))) + "; 3 shows all)"
		acts = acts[len(acts)-AgentActivityMax:]
	}
	add(Line{S("")}, "")
	add(Line{Bold(title)}, "")
	o.Agent, o.Search = "", ""
	for _, l := range activityLines(acts, o, false) {
		add(l, "")
	}
}
