// Package vterm is a headless terminal emulator: it consumes the byte stream
// a PTY produces and maintains the screen, scrollback, cursor and mode state
// an xterm-compatible terminal would hold. Its purpose is Snapshot: an ANSI
// stream that reconstructs the current state inside a fresh terminal, so a
// client that attaches to a running session sees exactly what a terminal
// that had been attached all along would show — instead of replaying the
// raw history, which for full-screen and inline-redrawing programs is a
// stack of frames drawn at sizes that no longer apply.
//
// Fidelity target is xterm.js (the client this feeds), not a VT500. Query
// responses are never generated: the real client answers those.
package vterm

// Color is a packed terminal color: 0 is the default color; otherwise the
// top byte holds the kind and the low bytes the value.
type Color uint32

const (
	colorKindIndexed Color = 1 << 24
	colorKindRGB     Color = 2 << 24
	colorKindMask    Color = 0xff << 24
)

// IndexedColor returns one of the 256 palette colors.
func IndexedColor(i uint8) Color { return colorKindIndexed | Color(i) }

// RGBColor returns a 24-bit color.
func RGBColor(r, g, b uint8) Color {
	return colorKindRGB | Color(r)<<16 | Color(g)<<8 | Color(b)
}

// IsDefault reports whether the color is the terminal default.
func (c Color) IsDefault() bool { return c == 0 }

// Attr is a set of SGR attribute flags.
type Attr uint8

// Attribute flags.
const (
	AttrBold Attr = 1 << iota
	AttrDim
	AttrItalic
	AttrBlink
	AttrInverse
	AttrHidden
	AttrStrike
)

// Underline is the underline style of a cell (SGR 4:n).
type Underline uint8

// Underline styles.
const (
	UnderlineNone Underline = iota
	UnderlineSingle
	UnderlineDouble
	UnderlineCurly
	UnderlineDotted
	UnderlineDashed
)

// Style is the rendition of a cell.
type Style struct {
	Fg, Bg, Ul Color
	Attrs      Attr
	Underline  Underline
}

// IsZero reports whether the style is the default rendition.
func (s Style) IsZero() bool { return s == Style{} }

// Cell is one screen position.
type Cell struct {
	// Ch is the grapheme shown in the cell: a base character plus any
	// combining marks. Empty means a blank cell (rendered as a space).
	Ch string
	// Width is the number of columns the grapheme occupies: 1, 2 for wide
	// characters, or 0 for the placeholder cell that follows a wide one.
	Width uint8
	Style Style
	Link  string
}

// IsBlank reports whether the cell holds nothing visible with the default
// rendition, so a serializer may leave it to the terminal's clear state.
func (c *Cell) IsBlank() bool {
	return (c.Ch == "" || c.Ch == " ") && c.Width != 0 && c.Style.IsZero() && c.Link == ""
}

// row is one screen or scrollback line.
type row struct {
	cells []Cell
	// wrapped marks a line that continues the previous one: autowrap put
	// the cursor here rather than a newline. Serialization keeps such lines
	// joined so the receiving terminal can reflow them on resize.
	wrapped bool
}

func newRow(w int) row {
	r := row{cells: make([]Cell, w)}
	for i := range r.cells {
		r.cells[i].Width = 1
	}
	return r
}

// clear resets every cell to blank with the given style (the pen's
// background survives an erase, as in xterm).
func (r *row) clear(from, to int, st Style) {
	if from < 0 {
		from = 0
	}
	if to > len(r.cells) {
		to = len(r.cells)
	}
	for i := from; i < to; i++ {
		r.cells[i] = Cell{Width: 1, Style: eraseStyle(st)}
	}
}

// eraseStyle is the rendition an erased cell keeps. xterm.js's erase
// attribute (_eraseAttrData) copies only the current background color;
// the foreground and other SGR flags are dropped. Match that.
func eraseStyle(st Style) Style {
	return Style{Bg: st.Bg}
}

// lastVisible returns the index just past the last cell that is not blank,
// so trailing blanks can be trimmed.
func (r *row) lastVisible() int {
	for i := len(r.cells) - 1; i >= 0; i-- {
		if !r.cells[i].IsBlank() {
			return i + 1
		}
	}
	return 0
}

// trimmed returns a copy with trailing blanks dropped, for scrollback
// storage.
func (r *row) trimmed() row {
	n := r.lastVisible()
	out := row{cells: make([]Cell, n), wrapped: r.wrapped}
	copy(out.cells, r.cells[:n])
	return out
}

// fitted returns the row padded or truncated to w columns.
func (r *row) fitted(w int) row {
	out := row{cells: make([]Cell, w), wrapped: r.wrapped}
	n := copy(out.cells, r.cells)
	for i := n; i < w; i++ {
		out.cells[i] = Cell{Width: 1}
	}
	// Never cut a wide character in half at the new right edge.
	if w > 0 && out.cells[w-1].Width == 2 {
		out.cells[w-1] = Cell{Width: 1}
	}
	return out
}
