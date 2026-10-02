package view

import (
	"strings"

	"github.com/AIToolSharing/agent_coop/internal/model"
)

func kv(key, value string, st Style) Line {
	return Line{Dim(key + strings.Repeat(" ", max(0, 12-Width(key))) + " "), Styled(value, st)}
}

// RenderAgent shows one agent: its state, where it runs, and its own timeline.
func RenderAgent(v *model.Session, address string, o Options) Rendered {
	a := v.Agents[address]
	if a == nil {
		for _, x := range v.AgentList() {
			if x.Address == address {
				a = x
			}
		}
	}
	if a == nil {
		return Note("select an agent (tab to the sidebar, then enter)", o.Width)
	}
	var r Rendered
	add := func(l Line, id string) {
		r.Lines = append(r.Lines, Fit(l, o.Width))
		r.IDs = append(r.IDs, id)
	}
	presence := Color("  ○ offline", "gray")
	if a.Online {
		presence = Color("  ● online", "green")
	}
	add(Line{Bold(a.Address), presence}, "")
	if a.Kicked {
		add(kv("removed", "by the operator; it cannot join until you allow it back (:allow)", Style{Color: "red"}), "")
	}
	state := a.State
	if a.StateSince != "" {
		state += " for " + Age(a.StateSince, o.Now)
	}
	add(kv("state", state, Style{Color: StateColor(a.State)}), "")
	if a.Note != "" {
		add(kv("note", a.Note, Style{}), "")
	}
	if w := a.Waiting; w != nil {
		text := "for any message"
		if w.On != "" {
			text = "on " + w.On
		}
		if w.ReplyTo != "" {
			text += " (ask #" + w.ReplyTo + ")"
		}
		add(kv("waiting", text+" for "+Age(w.Since, o.Now), Style{Color: "yellow"}), "")
	}
	add(kv("host", or(a.Host, "?"), Style{}), "")
	add(kv("directory", or(a.Cwd, "?"), Style{}), "")
	add(kv("client", or(a.Client, "?"), Style{}), "")
	joined := "?"
	if a.JoinedAt != "" {
		joined = Clock(a.JoinedAt, o.loc()) + " (" + Age(a.JoinedAt, o.Now) + " ago)"
	}
	add(kv("joined", joined, Style{}), "")
	if a.Left != nil {
		add(kv("left", Clock(a.Left.At, o.loc())+" ("+a.Left.Reason+")", Style{}), "")
	}
	add(kv("messages", "sent "+itoa(int64(a.Sent)), Style{}), "")
	add(Line{S("")}, "")
	add(Line{Bold("TIMELINE")}, "")
	for _, it := range v.Timeline {
		if it.Sys != nil {
			if it.Sys.Who == a.Address {
				add(Line{Dim(Clock(it.At, o.loc()) + "  "), Dim(it.Sys.Text)}, "")
			}
			continue
		}
		m := it.Msg
		var lead Line
		switch {
		case m.From == a.Address:
			lead = Line{Dim(Clock(it.At, o.loc()) + "  "), Color("sent ", "cyan"), S("#" + m.ID + " → " + m.To + ": ")}
		case m.To == a.Address || m.To == "all":
			lead = Line{Dim(Clock(it.At, o.loc()) + "  "), Color("got ", "green"), S("#" + m.ID + " ← " + m.From + ": ")}
		default:
			continue
		}
		body := []string{"[withdrawn]"}
		if !m.Redacted {
			body = Wrap(m.Text, max(10, o.Width-Width(Plain(lead))))
		}
		add(append(lead, S(body[0])), m.ID)
		pad := strings.Repeat(" ", Width(Plain(lead)))
		for _, line := range body[1:] {
			add(Line{S(pad), S(line)}, "")
		}
	}
	return r
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// RenderMessage shows one message whole, what it answers, and its replies.
func RenderMessage(v *model.Session, id string, o Options) Rendered {
	m := v.Msgs[id]
	if m == nil {
		return Note("select a message (↑↓ in the transcript, then enter)", o.Width)
	}
	var r Rendered
	add := func(l Line, lid string) {
		r.Lines = append(r.Lines, Fit(l, o.Width))
		r.IDs = append(r.IDs, lid)
	}
	head := Line{Bold("#" + m.ID)}
	if m.Redacted {
		head = append(head, Dim("  withdrawn"))
	} else if m.To == "operator" {
		head = append(head, Styled("  for you", Style{Color: "yellow", Bold: true}))
	}
	add(head, "")
	add(kv("from", m.From, Style{Color: SenderColor(m.From), Bold: true}), "")
	add(kv("to", m.To, Style{}), "")
	add(kv("sent", Stamp(m.SentAt, o.loc())+" ("+Age(m.SentAt, o.Now)+" ago)", Style{}), "")
	if m.ReplyTo != "" {
		text := "#" + m.ReplyTo
		if p := v.Msgs[m.ReplyTo]; p != nil {
			text += " from " + p.From + ": " + p.Text
		}
		add(kv("answers", text, Style{}), m.ReplyTo)
	}
	add(Line{S("")}, "")
	if m.Redacted {
		add(Line{Dim("  [withdrawn by the operator]")}, "")
	} else {
		for _, line := range Wrap(m.Text, max(10, o.Width-2)) {
			add(Line{S("  " + line)}, "")
		}
	}
	if len(m.Replies) > 0 {
		add(Line{S("")}, "")
		add(Line{Bold("REPLIES (" + itoa(int64(len(m.Replies))) + ")")}, "")
		for _, c := range m.Replies {
			add(Line{Dim("  #" + c.ID + " "), Bold(c.From)}, c.ID)
			for _, line := range Wrap(c.Text, max(10, o.Width-4)) {
				add(Line{S("    " + line)}, "")
			}
		}
	}
	return r
}
