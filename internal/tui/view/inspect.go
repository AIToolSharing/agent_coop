package view

import (
	"strconv"
	"strings"

	"github.com/AIToolSharing/agent_coop/internal/model"
)

func kv(key, value string, st Style) Line {
	return Line{Dim(key + strings.Repeat(" ", max(0, 12-Width(key))) + " "), Styled(value, st)}
}

// AgentView shows one agent: its state, where it runs, and its own timeline. A new item of
// the timeline adds a block; the head reads the agent when it is drawn.
type AgentView struct {
	sid      string
	address  string
	version  int
	reshaped int
	n        int
	blocks   []Block
}

// Render gives the buffer of the agent `address` of v.
func (av *AgentView) Render(v *model.Session, address string, o Options) Buffer {
	if av.sid != v.SID || av.address != address || av.reshaped != v.Reshaped || av.n > len(v.Timeline) || av.blocks == nil {
		a := v.Agents[address]
		if a == nil {
			for _, x := range v.AgentList() {
				if x.Address == address {
					a = x
				}
			}
		}
		if a == nil {
			return Note("select an agent (h to the sidebar, then enter)")
		}
		*av = AgentView{sid: v.SID, address: address, reshaped: v.Reshaped, blocks: []Block{agentHead(a), Blank(), Heading("TIMELINE")}}
	} else if av.version == v.Version {
		return Buffer{Blocks: av.blocks}
	}
	av.version = v.Version
	a := v.Agents[address]
	for ; av.n < len(v.Timeline); av.n++ {
		it := v.Timeline[av.n]
		if it.Sys != nil {
			if it.Sys.Who == a.Address {
				av.blocks = append(av.blocks, sysBlock(it))
			}
			continue
		}
		m := it.Msg
		switch {
		case m.From == a.Address:
			av.blocks = append(av.blocks, agentMsgBlock(m, Color("sent ", "cyan"), "#"+m.ID+" → "+m.To+": "))
		case m.To == a.Address || m.To == "all":
			av.blocks = append(av.blocks, agentMsgBlock(m, Color("got ", "green"), "#"+m.ID+" ← "+m.From+": "))
		}
	}
	return Buffer{Blocks: av.blocks}
}

func agentHead(a *model.Agent) Block {
	return Block{Render: func(o Options) []Line {
		var lines []Line
		add := func(l Line) { lines = append(lines, Fit(l, o.Width)) }
		presence := Color("  ○ offline", "gray")
		if a.Online {
			presence = Color("  ● online", "green")
		}
		add(Line{Bold(a.Address), presence})
		state := a.State
		if a.StateSince != "" {
			state += " for " + Age(a.StateSince, o.Now)
		}
		add(kv("state", state, Style{Color: StateColor(a.State)}))
		if a.Note != "" {
			add(kv("note", a.Note, Style{}))
		}
		if w := a.Waiting; w != nil {
			text := "for any message"
			if w.On != "" {
				text = "on " + w.On
			}
			if w.ReplyTo != "" {
				text += " (ask #" + w.ReplyTo + ")"
			}
			add(kv("waiting", text+" for "+Age(w.Since, o.Now), Style{Color: "yellow"}))
		}
		add(kv("host", or(a.Host, "?"), Style{}))
		add(kv("directory", or(a.Cwd, "?"), Style{}))
		add(kv("client", or(a.Client, "?"), Style{}))
		joined := "?"
		if a.JoinedAt != "" {
			joined = Clock(a.JoinedAt, o.loc()) + " (" + Age(a.JoinedAt, o.Now) + " ago)"
		}
		add(kv("joined", joined, Style{}))
		if a.Left != nil {
			add(kv("left", Clock(a.Left.At, o.loc())+" ("+a.Left.Reason+")", Style{}))
		}
		add(kv("messages", "sent "+strconv.Itoa(a.Sent), Style{}))
		return lines
	}}
}

func sysBlock(it model.Item) Block {
	return Block{Render: func(o Options) []Line {
		return []Line{Fit(Line{Dim(Clock(it.At, o.loc()) + "  "), Dim(it.Sys.Text)}, o.Width)}
	}}
}

func agentMsgBlock(m *model.Msg, verb Seg, head string) Block {
	return Block{ID: m.ID, Text: strings.ToLower(m.Text), Render: func(o Options) []Line {
		lead := Line{Dim(Clock(m.SentAt, o.loc()) + "  "), verb, S(head)}
		body := Wrap(m.Text, max(10, o.Width-Width(Plain(lead))))
		lines := []Line{Fit(append(lead, S(body[0])), o.Width)}
		pad := strings.Repeat(" ", Width(Plain(lead)))
		for _, line := range body[1:] {
			lines = append(lines, Fit(Line{S(pad), S(line)}, o.Width))
		}
		return lines
	}}
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// RenderMessage shows one message whole, what it answers, and its replies.
func RenderMessage(v *model.Session, id string, o Options) Buffer {
	m := v.Msgs[id]
	if m == nil {
		return Note("select a message (j k in the transcript, then enter)")
	}
	var blocks []Block
	one := func(id string, render func(o Options) Line) {
		blocks = append(blocks, Block{ID: id, Render: func(o Options) []Line { return []Line{Fit(render(o), o.Width)} }})
	}
	one("", func(Options) Line { return Line{Bold("#" + m.ID)} })
	one("", func(Options) Line { return kv("from", m.From, Style{Bold: true}) })
	one("", func(Options) Line { return kv("to", m.To, Style{}) })
	one("", func(o Options) Line {
		return kv("sent", Stamp(m.SentAt, o.loc())+" ("+Age(m.SentAt, o.Now)+" ago)", Style{})
	})
	if m.ReplyTo != "" {
		text := "#" + m.ReplyTo
		if p := v.Msgs[m.ReplyTo]; p != nil {
			text += " from " + p.From + ": " + p.Text
		}
		one(m.ReplyTo, func(Options) Line { return kv("answers", text, Style{}) })
	}
	blocks = append(blocks, Blank(), Block{Text: strings.ToLower(m.Text), Render: func(o Options) []Line {
		var lines []Line
		for _, line := range Wrap(m.Text, max(10, o.Width-2)) {
			lines = append(lines, Fit(Line{S("  " + line)}, o.Width))
		}
		return lines
	}})
	if len(m.Replies) > 0 {
		blocks = append(blocks, Blank(), Heading("REPLIES ("+strconv.Itoa(len(m.Replies))+")"))
		for _, c := range m.Replies {
			blocks = append(blocks, Block{ID: c.ID, Text: strings.ToLower(c.Text), Render: func(o Options) []Line {
				lines := []Line{Fit(Line{Dim("  #" + c.ID + " "), Bold(c.From)}, o.Width)}
				for _, line := range Wrap(c.Text, max(10, o.Width-4)) {
					lines = append(lines, Fit(Line{S("    " + line)}, o.Width))
				}
				return lines
			}})
		}
	}
	return Buffer{Blocks: blocks}
}
