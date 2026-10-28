package view_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/AIToolSharing/agent_coop/internal/tui/view"
	"pgregory.net/rapid"
)

// buffer draws a buffer of blocks with 1 to 5 lines each; every other block is a message.
// The pane counts how many times it rendered a block.
func buffer(rt *rapid.T) (view.Buffer, *int) {
	renders := 0
	n := rapid.IntRange(0, 12).Draw(rt, "blocks")
	var b view.Buffer
	for i := 0; i < n; i++ {
		height := rapid.IntRange(1, 5).Draw(rt, fmt.Sprint("height", i))
		id := ""
		if i%2 == 0 {
			id = fmt.Sprint("m", i)
		}
		i := i
		b.Blocks = append(b.Blocks, view.Block{ID: id, Text: id, Render: func(o view.Options) []view.Line {
			renders++
			lines := make([]view.Line, height)
			for j := range lines {
				lines[j] = view.Fit(view.Line{view.S(fmt.Sprintf("b%d l%d", i, j))}, o.Width)
			}
			return lines
		}})
	}
	return b, &renders
}

// flat is the whole buffer as one list of lines, with the id on the first line of a block.
func flat(b view.Buffer, o view.Options) (lines []string, ids []string) {
	for _, bl := range b.Blocks {
		for j, l := range bl.Render(o) {
			lines = append(lines, view.Plain(l))
			id := ""
			if j == 0 {
				id = bl.ID
			}
			ids = append(ids, id)
		}
	}
	return lines, ids
}

func TestPaneWindowIsASliceOfTheWholeBuffer(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		b, renders := buffer(rt)
		o := view.Options{Width: 20}
		lines, ids := flat(b, o)
		total := len(lines)
		p := &view.Pane{Buf: b, O: o}
		height := rapid.IntRange(1, 10).Draw(rt, "height")
		top := view.Anchor{Block: rapid.IntRange(-1, len(b.Blocks)+1).Draw(rt, "block"), Line: rapid.IntRange(-1, 6).Draw(rt, "line")}

		// The window from an anchor is the slice at its flat position, clamped so that the
		// window is full when the buffer has that many lines.
		r, used := p.Window(top, height)
		off := p.Flat(used)
		want := lines[min(off, total):min(off+height, total)]
		if strings.Join(texts(r), "|") != strings.Join(want, "|") {
			rt.Fatalf("window at %+v (flat %d): got %v, want %v", used, off, texts(r), want)
		}
		if fmt.Sprint(r.IDs) != fmt.Sprint(ids[min(off, total):min(off+height, total)]) {
			rt.Fatalf("ids at %+v: %v", used, r.IDs)
		}
		if total > height && len(r.Lines) != height {
			rt.Fatalf("window at %+v has %d lines, want %d", used, len(r.Lines), height)
		}
		if total <= height && len(r.Lines) != total {
			rt.Fatalf("window at %+v has %d lines, want all %d", used, len(r.Lines), total)
		}

		// The end anchor ends the window with the last line.
		end := p.End(height)
		if r, _ := p.Window(end, height); total > 0 && texts(r)[len(r.Lines)-1] != lines[total-1] {
			rt.Fatalf("the window at the end anchor %+v ends with %q", end, texts(r)[len(r.Lines)-1])
		}

		// Advance by n is the flat position plus n, kept in the buffer.
		n := rapid.IntRange(-15, 15).Draw(rt, "n")
		if total > 0 {
			moved := p.Advance(used, n)
			if got, want := p.Flat(moved), min(total-1, max(0, off+n)); got != want {
				rt.Fatalf("advance %+v by %d gave %+v (flat %d), want flat %d", used, n, moved, got, want)
			}
		}

		// Reveal puts a block whole in the window (or its first line at the top), and moves
		// nothing when it is already there.
		if len(b.Blocks) > 0 {
			i := rapid.IntRange(0, len(b.Blocks)-1).Draw(rt, "reveal")
			bh := len(b.Blocks[i].Render(o))
			start := p.Flat(view.Anchor{Block: i})
			shown := p.Reveal(used, i, height)
			so := p.Flat(shown)
			switch {
			case bh > height:
				if so != start {
					rt.Fatalf("reveal of the tall block %d from %+v gave flat %d, want %d", i, used, so, start)
				}
			case so > start || so+height < start+bh:
				rt.Fatalf("reveal of block %d (lines %d..%d) from %+v gave the window %d..%d", i, start, start+bh-1, used, so, so+height-1)
			case off <= start && start+bh <= off+height && shown != used:
				rt.Fatalf("reveal of the visible block %d moved the window from %+v to %+v", i, used, shown)
			}
		}
		// A frame renders a block once at most, whatever the pane is asked.
		if *renders > len(b.Blocks)+len(b.Blocks)*2 {
			rt.Fatalf("%d renders of %d blocks", *renders, len(b.Blocks))
		}
	})
}

func TestBufferFindAndNext(t *testing.T) {
	var b view.Buffer
	for _, id := range []string{"", "a", "", "", "b", ""} {
		b.Blocks = append(b.Blocks, view.Block{ID: id})
	}
	if b.Find("b") != 4 || b.Find("x") != -1 || b.Find("") != -1 {
		t.Fatal("find")
	}
	if b.Next(-1, 1) != 1 || b.Next(1, 1) != 4 || b.Next(4, 1) != -1 || b.Next(len(b.Blocks), -1) != 4 || b.Next(4, -1) != 1 || b.Next(1, -1) != -1 {
		t.Fatal("next")
	}
}
