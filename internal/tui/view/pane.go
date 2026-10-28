package view

// Block is one unit of a buffer: a message, a run of system lines, a day rule, a heading. It
// renders on demand, so a buffer of any length costs nothing until a window shows a part of
// it.
type Block struct {
	ID   string // the message id, or "" for a block that is not a message
	Text string // what a search looks in: the text of the message in lower case; "" otherwise
	// Render gives the lines of the block, each fitted to o.Width. Never empty.
	Render func(o Options) []Line
}

// Buffer is what a view shows: its blocks, in order.
type Buffer struct {
	Blocks []Block
}

// Find gives the index of the block of a message, or -1.
func (b Buffer) Find(id string) int {
	if id == "" {
		return -1
	}
	for i := range b.Blocks {
		if b.Blocks[i].ID == id {
			return i
		}
	}
	return -1
}

// Next gives the index of the first message block after (dir 1) or before (dir -1) `from`,
// or -1. A `from` of -1 with dir 1 starts at the first block; a `from` of len with dir -1
// starts at the last one.
func (b Buffer) Next(from, dir int) int {
	for i := from + dir; i >= 0 && i < len(b.Blocks); i += dir {
		if b.Blocks[i].ID != "" {
			return i
		}
	}
	return -1
}

// Anchor is the first visible line of a window: a block and a line in it.
type Anchor struct{ Block, Line int }

// Before reports whether a comes before b.
func Before(a, b Anchor) bool {
	return a.Block < b.Block || (a.Block == b.Block && a.Line < b.Line)
}

// Pane renders the blocks of a buffer for one frame. It keeps each block it rendered, so
// that the window, the end anchor and a move render a block once. Make a new Pane for each
// frame: the options (width, now) change between frames.
type Pane struct {
	Buf   Buffer
	O     Options
	lines map[int][]Line
}

// Lines renders block i.
func (p *Pane) Lines(i int) []Line {
	if l, ok := p.lines[i]; ok {
		return l
	}
	if p.lines == nil {
		p.lines = map[int][]Line{}
	}
	l := p.Buf.Blocks[i].Render(p.O)
	if len(l) == 0 {
		l = []Line{Fit(Line{}, p.O.Width)}
	}
	p.lines[i] = l
	return l
}

func (p *Pane) height(i int) int { return len(p.Lines(i)) }

// Window gives the lines from top, at most height of them, and the anchor it used: top,
// moved back when the buffer has fewer lines after it than the window shows. The first line
// of a message block carries its id.
func (p *Pane) Window(top Anchor, height int) (Rendered, Anchor) {
	var r Rendered
	n := len(p.Buf.Blocks)
	if n == 0 || height <= 0 {
		return r, Anchor{}
	}
	top = p.Advance(Anchor{Block: min(max(0, top.Block), n-1)}, max(0, top.Line))
	if end := p.End(height); Before(end, top) {
		top = end
	}
	for i := top.Block; i < n && len(r.Lines) < height; i++ {
		lines := p.Lines(i)
		skip := 0
		if i == top.Block {
			skip = min(top.Line, len(lines))
		}
		for j := skip; j < len(lines) && len(r.Lines) < height; j++ {
			id := ""
			if j == 0 {
				id = p.Buf.Blocks[i].ID
			}
			r.Lines = append(r.Lines, lines[j])
			r.IDs = append(r.IDs, id)
		}
	}
	return r, top
}

// End is the anchor of the window that ends with the last line of the buffer.
func (p *Pane) End(height int) Anchor {
	return p.EndAt(len(p.Buf.Blocks)-1, height)
}

// EndAt is the anchor of the window that ends with the last line of block i. A block taller
// than the window shows its last lines.
func (p *Pane) EndAt(i, height int) Anchor {
	if i < 0 || i >= len(p.Buf.Blocks) {
		return Anchor{}
	}
	need := height - p.height(i)
	if need <= 0 {
		return Anchor{Block: i, Line: -need}
	}
	for i > 0 && need > 0 {
		i--
		h := p.height(i)
		if h >= need {
			return Anchor{Block: i, Line: h - need}
		}
		need -= h
	}
	return Anchor{}
}

// Advance moves an anchor by n lines, forward or back. It stops at the first line and at the
// last line of the buffer.
func (p *Pane) Advance(a Anchor, n int) Anchor {
	last := len(p.Buf.Blocks) - 1
	if last < 0 {
		return Anchor{}
	}
	a.Block = min(max(0, a.Block), last)
	a.Line += n
	for a.Line < 0 && a.Block > 0 {
		a.Block--
		a.Line += p.height(a.Block)
	}
	if a.Line < 0 {
		a.Line = 0
	}
	for h := p.height(a.Block); a.Line >= h; h = p.height(a.Block) {
		if a.Block == last {
			a.Line = h - 1
			break
		}
		a.Line -= h
		a.Block++
	}
	return a
}

// Reveal moves the window the least so that block i is whole in it: not at all when it is,
// to the block when it is above, so that the block ends at the bottom when it is below. A
// block taller than the window shows from its first line.
func (p *Pane) Reveal(top Anchor, i, height int) Anchor {
	if i < 0 || i >= len(p.Buf.Blocks) {
		return top
	}
	if i < top.Block || (i == top.Block && top.Line > 0) || p.height(i) >= height {
		return Anchor{Block: i}
	}
	n := 0
	for j := top.Block; j <= i && n <= height; j++ {
		h := p.height(j)
		if j == top.Block {
			h -= top.Line
		}
		n += max(0, h)
	}
	if n <= height {
		return top
	}
	return p.EndAt(i, height)
}

// Flat is the number of lines before an anchor: the position in the buffer as one list of
// lines. It renders every block before the anchor: for tests and for small buffers.
func (p *Pane) Flat(a Anchor) int {
	n := 0
	for i := 0; i < a.Block && i < len(p.Buf.Blocks); i++ {
		n += p.height(i)
	}
	return n + a.Line
}

// Note is a buffer of one dim line.
func Note(text string) Buffer {
	return Buffer{Blocks: []Block{{Render: func(o Options) []Line { return []Line{Fit(Line{Dim(text)}, o.Width)} }}}}
}

// Heading is a block of one bold line.
func Heading(text string) Block {
	return Block{Render: func(o Options) []Line { return []Line{Fit(Line{Bold(text)}, o.Width)} }}
}

// Blank is a block of one empty line.
func Blank() Block {
	return Block{Render: func(o Options) []Line { return []Line{Fit(Line{}, o.Width)} }}
}
