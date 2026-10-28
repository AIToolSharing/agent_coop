package view

import (
	"strconv"
	"strings"

	"github.com/AIToolSharing/agent_coop/internal/model"
)

const indent = "  "

// MsgVisible applies the agent filter to a message. A broadcast involves every agent of the
// session.
func MsgVisible(m *model.Msg, o Options) bool {
	return o.Agent == "" || m.From == o.Agent || m.To == o.Agent || m.To == "all"
}

func itemVisible(it model.Item, o Options) bool {
	if it.Msg != nil {
		return MsgVisible(it.Msg, o)
	}
	return o.System && (o.Agent == "" || it.Sys.Who == o.Agent)
}

type transcriptKey struct {
	sid    string
	agent  string
	system bool
}

// Transcript turns a session into blocks and keeps them between repaints. A new item adds a
// block; a change to the order of what was already shown (model.Session.Reshaped) starts
// over. The blocks render on demand, so a repaint costs the visible lines only, and a reply
// count or a relative time reads the model when the block is drawn.
type Transcript struct {
	key      transcriptKey
	version  int
	reshaped int
	all      bool
	n        int
	blocks   []Block
	day      string
	burst    []model.Item // the open run of system items
	burstAt  int          // the block of the open run, or -1
}

// Render gives the buffer of v. The blocks are shared with the cache: do not change them.
func (t *Transcript) Render(v *model.Session, o Options) Buffer {
	key := transcriptKey{v.SID, o.Agent, o.System}
	if t.key != key || t.reshaped != v.Reshaped || t.n > len(v.Timeline) {
		*t = Transcript{key: key, reshaped: v.Reshaped, all: v.SID == model.AllSessions, burstAt: -1}
	} else if t.version == v.Version {
		return t.buffer()
	}
	t.version = v.Version
	for ; t.n < len(v.Timeline); t.n++ {
		it := v.Timeline[t.n]
		if !itemVisible(it, o) {
			continue
		}
		day := Day(it.At, o.loc())
		if day != t.day {
			t.closeBurst()
			t.day = day
			t.blocks = append(t.blocks, dayBlock(day))
		}
		if it.Sys != nil {
			if t.burstAt < 0 {
				t.burstAt = len(t.blocks)
				t.blocks = append(t.blocks, Block{})
			}
			t.burst = append(t.burst, it)
			t.blocks[t.burstAt] = burstBlock(t.burst, t.all)
			continue
		}
		t.closeBurst()
		t.blocks = append(t.blocks, MessageBlock(v, it.Msg, t.all))
	}
	return t.buffer()
}

func (t *Transcript) buffer() Buffer {
	if len(t.blocks) == 0 {
		return Note("no messages yet")
	}
	return Buffer{Blocks: t.blocks}
}

// closeBurst ends the open run of system items. The block keeps the items it has.
func (t *Transcript) closeBurst() {
	t.burst, t.burstAt = nil, -1
}

func dayBlock(day string) Block {
	return Block{Render: func(o Options) []Line {
		rule := strings.Repeat("─", max(0, o.Width-Width(day)-4))
		return []Line{Fit(Line{Dim("── " + day + " " + rule)}, o.Width)}
	}}
}

// burstBlock folds a run of system items into one dim line.
func burstBlock(items []model.Item, all bool) Block {
	return Block{Render: func(o Options) []Line {
		parts := make([]string, 0, len(items))
		for _, it := range items {
			who := it.Sys.Who
			if all {
				who = it.SID + "/" + who
			}
			parts = append(parts, who+" "+it.Sys.Text)
		}
		return []Line{Fit(Line{Dim(Clock(items[0].At, o.loc()) + "  · "), Dim(strings.Join(parts, " · "))}, o.Width)}
	}}
}

// MessageBlock is a message: the header, the quote of the message it answers, and the whole
// body.
func MessageBlock(v *model.Session, m *model.Msg, all bool) Block {
	return Block{ID: m.ID, Text: strings.ToLower(m.Text), Render: func(o Options) []Line {
		lines := []Line{Fit(Header(m, o, all), o.Width)}
		if m.ReplyTo != "" {
			quote := "↩ #" + m.ReplyTo
			if p := v.Msgs[m.ReplyTo]; p != nil {
				quote += " " + p.From + ": " + p.Text
			}
			lines = append(lines, Fit(Line{S(indent), Styled(quote, Style{Color: "cyan", Dim: true})}, o.Width))
		}
		for _, line := range Wrap(m.Text, o.Width-len(indent)) {
			lines = append(lines, Fit(Line{S(indent), S(line)}, o.Width))
		}
		return lines
	}}
}

// Header is the first line of a message: time, sender and target; the id and the reply count
// on the right.
func Header(m *model.Msg, o Options, all bool) Line {
	right := Line{Dim("#" + m.ID)}
	if n := len(m.Replies); n > 0 {
		right = append(right, Color("  ↳"+strconv.Itoa(n), "cyan"))
	}
	left := Line{Dim(Clock(m.SentAt, o.loc())), S("  ")}
	if all {
		left = append(left, Dim("["+m.SID+"] "))
	}
	left = append(left, Bold(m.From), S(" → "), S(m.To))
	used := Width(Plain(left)) + Width(Plain(right))
	out := append(Line{}, left...)
	out = append(out, S(strings.Repeat(" ", max(1, o.Width-used))))
	return append(out, right...)
}
