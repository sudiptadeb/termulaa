package vterm

import (
	"strconv"
	"strings"
)

// Snapshot serializes the terminal state as an ANSI byte stream. Written
// into a freshly reset terminal of the same size, it reproduces the
// scrollback, both screens, the cursor, the pen and every mode that
// affects rendering or input, so bytes that follow it produce the same
// picture on the receiver as on this emulator.
func (t *Terminal) Snapshot() []byte {
	var b strings.Builder
	b.Grow(4096)

	// Start from a known state: reset attributes, main screen, full
	// scroll region, autowrap on, origin mode off.
	b.WriteString("\x1b[0m\x1b[?7h\x1b[?6l\x1b[?25l")

	t.writeScreen(&b, &t.main, true)

	if t.altActive {
		// Enter the alternate screen the way the application did, with the
		// main cursor saved, then paint it.
		b.WriteString("\x1b[?1049h")
		t.writeScreen(&b, &t.alt, false)
	}

	// Pen, link and modes that outlive the snapshot.
	b.WriteString(sgrSequence(t.pen))
	if t.link != "" {
		b.WriteString("\x1b]8;;")
		b.WriteString(t.link)
		b.WriteString("\x1b\\")
	}
	if t.charsets[0] == charsetDECGraphics {
		b.WriteString("\x1b(0")
	}
	if t.charsets[1] == charsetDECGraphics {
		b.WriteString("\x1b)0")
	}
	if t.glIndex == 1 {
		b.WriteByte(0x0e)
	}
	if t.insert {
		b.WriteString("\x1b[4h")
	}
	if t.lineFeedNewline {
		b.WriteString("\x1b[20h")
	}
	if t.cursorKeys {
		b.WriteString("\x1b[?1h")
	}
	if !t.autowrap {
		b.WriteString("\x1b[?7l")
	}
	if t.bracketedPaste {
		b.WriteString("\x1b[?2004h")
	}
	if t.focusEvents {
		b.WriteString("\x1b[?1004h")
	}
	if t.mouse != 0 {
		b.WriteString("\x1b[?" + strconv.Itoa(int(t.mouse)) + "h")
	}
	if t.mouseEnc != 0 {
		b.WriteString("\x1b[?" + strconv.Itoa(int(t.mouseEnc)) + "h")
	}
	if t.cursorStyle != 0 {
		b.WriteString("\x1b[" + strconv.Itoa(t.cursorStyle) + " q")
	}
	if t.title != "" {
		b.WriteString("\x1b]0;")
		b.WriteString(t.title)
		b.WriteString("\x1b\\")
	}
	if t.cursorVisible {
		b.WriteString("\x1b[?25h")
	}
	return []byte(b.String())
}

// writeScreen paints one screen: its scrollback (main only), its rows,
// then its saved cursor, scroll region and cursor position.
func (t *Terminal) writeScreen(b *strings.Builder, s *screen, withScrollback bool) {
	var pen Style
	var link string

	if withScrollback {
		for i := range s.scrollback {
			r := &s.scrollback[i]
			next := s.rows[0].wrapped
			if i+1 < len(s.scrollback) {
				next = s.scrollback[i+1].wrapped
			}
			writeRow(b, r, t.w, next, &pen, &link)
			if !next {
				b.WriteString("\r\n")
			}
		}
	}
	for y := range s.rows {
		r := &s.rows[y]
		next := y+1 < len(s.rows) && s.rows[y+1].wrapped
		writeRow(b, r, t.w, next, &pen, &link)
		if y+1 < len(s.rows) && !next {
			b.WriteString("\r\n")
		}
	}
	// Leave the row content pen behind.
	if !pen.IsZero() {
		b.WriteString("\x1b[0m")
	}
	if link != "" {
		b.WriteString("\x1b]8;;\x1b\\")
	}

	// Saved cursor (DECSC) for this screen.
	if s.saved.valid {
		b.WriteString(sgrSequence(s.saved.pen))
		b.WriteString(cup(s.saved.x, s.saved.y))
		b.WriteString("\x1b7\x1b[0m")
	}

	// Scroll region, origin mode, then the cursor within them.
	if s.top != 0 || s.bot != t.h-1 {
		b.WriteString("\x1b[" + strconv.Itoa(s.top+1) + ";" + strconv.Itoa(s.bot+1) + "r")
	}
	if t.origin && s == t.s {
		b.WriteString("\x1b[?6h")
		b.WriteString(cup(s.cur.x, s.cur.y-s.top))
	} else {
		b.WriteString(cup(s.cur.x, s.cur.y))
	}
	if s.cur.pendingWrap {
		// The cursor sits past the last column: reproduce that by
		// rewriting the last cell so the receiver enters the same state.
		last := &s.rows[s.cur.y].cells[t.w-1]
		if last.Width == 1 && last.Ch != "" {
			b.WriteString(sgrSequence(last.Style))
			b.WriteString(last.Ch)
			b.WriteString("\x1b[0m")
		}
	}
}

// writeRow emits a row's cells. full forces every cell out (the row is
// continued by a wrapped line, so the receiver must fill it to the edge);
// otherwise trailing blank cells are left to the cleared background.
func writeRow(b *strings.Builder, r *row, w int, full bool, pen *Style, link *string) {
	n := r.lastVisible()
	if full {
		n = len(r.cells)
	}
	for x := 0; x < n; x++ {
		c := &r.cells[x]
		if c.Width == 0 {
			continue
		}
		if c.Style != *pen {
			b.WriteString(sgrSequence(c.Style))
			*pen = c.Style
		}
		if c.Link != *link {
			b.WriteString("\x1b]8;;")
			b.WriteString(c.Link)
			b.WriteString("\x1b\\")
			*link = c.Link
		}
		if c.Ch == "" {
			b.WriteByte(' ')
		} else {
			b.WriteString(c.Ch)
		}
	}
}

func cup(x, y int) string {
	return "\x1b[" + strconv.Itoa(y+1) + ";" + strconv.Itoa(x+1) + "H"
}

// sgrSequence returns the SGR sequence that sets exactly st from the
// default rendition.
func sgrSequence(st Style) string {
	if st.IsZero() {
		return "\x1b[0m"
	}
	parts := make([]string, 0, 8)
	parts = append(parts, "0")
	if st.Attrs&AttrBold != 0 {
		parts = append(parts, "1")
	}
	if st.Attrs&AttrDim != 0 {
		parts = append(parts, "2")
	}
	if st.Attrs&AttrItalic != 0 {
		parts = append(parts, "3")
	}
	switch st.Underline {
	case UnderlineSingle:
		parts = append(parts, "4")
	case UnderlineDouble:
		parts = append(parts, "21")
	case UnderlineCurly:
		parts = append(parts, "4:3")
	case UnderlineDotted:
		parts = append(parts, "4:4")
	case UnderlineDashed:
		parts = append(parts, "4:5")
	}
	if st.Attrs&AttrBlink != 0 {
		parts = append(parts, "5")
	}
	if st.Attrs&AttrInverse != 0 {
		parts = append(parts, "7")
	}
	if st.Attrs&AttrHidden != 0 {
		parts = append(parts, "8")
	}
	if st.Attrs&AttrStrike != 0 {
		parts = append(parts, "9")
	}
	parts = appendColor(parts, st.Fg, 38, 30, 90)
	parts = appendColor(parts, st.Bg, 48, 40, 100)
	if !st.Ul.IsDefault() {
		parts = appendColor(parts, st.Ul, 58, 0, 0)
	}
	return "\x1b[" + strings.Join(parts, ";") + "m"
}

func appendColor(parts []string, c Color, ext, base, bright int) []string {
	switch c & colorKindMask {
	case colorKindIndexed:
		i := int(c & 0xff)
		if base != 0 && i < 8 {
			return append(parts, strconv.Itoa(base+i))
		}
		if bright != 0 && i >= 8 && i < 16 {
			return append(parts, strconv.Itoa(bright+i-8))
		}
		return append(parts, strconv.Itoa(ext)+";5;"+strconv.Itoa(i))
	case colorKindRGB:
		return append(parts, strconv.Itoa(ext)+";2;"+
			strconv.Itoa(int(c>>16&0xff))+";"+strconv.Itoa(int(c>>8&0xff))+";"+strconv.Itoa(int(c&0xff)))
	}
	return parts
}
