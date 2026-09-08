package vterm

import (
	"strings"
	"testing"
)

func feed(t *testing.T, term *Terminal, s string) {
	t.Helper()
	if _, err := term.Write([]byte(s)); err != nil {
		t.Fatalf("Write: %v", err)
	}
}

func TestPrintAndWrap(t *testing.T) {
	term := New(5, 2)
	feed(t, term, "abcdefg")
	if got := term.Screen(); got != "abcde\nfg" {
		t.Fatalf("screen = %q", got)
	}
	if x, y := term.Cursor(); x != 2 || y != 1 {
		t.Fatalf("cursor = %d,%d want 2,1", x, y)
	}
	if !term.s.rows[1].wrapped {
		t.Fatal("second row should be marked wrapped")
	}
	// Exactly filling a row leaves the cursor in the pending-wrap state.
	term = New(3, 2)
	feed(t, term, "abc")
	if x, _ := term.Cursor(); x != 2 || !term.s.cur.pendingWrap {
		t.Fatalf("expected pending wrap at last column, cursor x=%d pending=%v", x, term.s.cur.pendingWrap)
	}
	feed(t, term, "\r\nX")
	if got := term.Screen(); got != "abc\nX" {
		t.Fatalf("CR LF after a full row must not produce an empty row: %q", got)
	}
}

func TestScrollbackAndScrolling(t *testing.T) {
	term := New(10, 3)
	feed(t, term, "1\r\n2\r\n3\r\n4\r\n5")
	if got := term.Screen(); got != "3\n4\n5" {
		t.Fatalf("screen = %q", got)
	}
	if term.ScrollbackLen() != 2 || term.ScrollbackLine(0) != "1" || term.ScrollbackLine(1) != "2" {
		t.Fatalf("scrollback = %d lines: %q %q", term.ScrollbackLen(), term.ScrollbackLine(0), term.ScrollbackLine(1))
	}
	// A region starting at the top row still feeds the scrollback on a
	// line feed (xterm.js behaviour); one starting lower does not.
	feed(t, term, "\x1b[1;2r\x1b[2;1Ha\r\nb\r\nc")
	if term.ScrollbackLen() != 4 {
		t.Fatalf("top-anchored region scroll should feed scrollback: %d", term.ScrollbackLen())
	}
	if got := term.Screen(); got != "b\nc\n5" {
		t.Fatalf("region scroll screen = %q", got)
	}
	feed(t, term, "\x1b[2;3r\x1b[3;1H\r\nd\r\ne")
	if term.ScrollbackLen() != 4 {
		t.Fatalf("lower region scroll leaked into scrollback: %d", term.ScrollbackLen())
	}
	// SU never feeds the scrollback.
	feed(t, term, "\x1b[r\x1b[2S")
	if term.ScrollbackLen() != 4 {
		t.Fatalf("SU leaked into scrollback: %d", term.ScrollbackLen())
	}
}

func TestCursorMovementAndErase(t *testing.T) {
	term := New(10, 4)
	feed(t, term, "hello world\x1b[2;3Hxy\x1b[1;1H\x1b[K")
	// EL from the home position clears row 0; row 1 has "d" from the wrap
	// overwritten at column 3.
	if got := term.Line(0); got != "" {
		t.Fatalf("row 0 = %q, want cleared", got)
	}
	if got := term.Line(1); got != "d xy" {
		t.Fatalf("row 1 = %q", got)
	}
	feed(t, term, "\x1b[2J")
	if got := term.Screen(); strings.TrimSpace(got) != "" {
		t.Fatalf("ED 2 left content: %q", got)
	}
	feed(t, term, "\x1b[3;4Hab\x1b[3D\x1b[2Ccd\x1b[A\x1b[Bz")
	if got := term.Line(2); got != "   acdz" {
		t.Fatalf("row 2 = %q", got)
	}
}

func TestInsertDeleteLinesAndChars(t *testing.T) {
	term := New(6, 4)
	feed(t, term, "aaa\r\nbbb\r\nccc\r\nddd")
	feed(t, term, "\x1b[2;1H\x1b[L")
	if got := term.Screen(); got != "aaa\n\nbbb\nccc" {
		t.Fatalf("IL screen = %q", got)
	}
	feed(t, term, "\x1b[2M")
	if got := term.Screen(); got != "aaa\nccc\n\n" {
		t.Fatalf("DL screen = %q", got)
	}
	feed(t, term, "\x1b[1;2H\x1b[@X\x1b[1;1H\x1b[P")
	if got := term.Line(0); got != "Xaa" {
		t.Fatalf("ICH/DCH row = %q", got)
	}
	feed(t, term, "\x1b[1;1H\x1b[2X")
	if got := term.Line(0); got != "  a" {
		t.Fatalf("ECH row = %q", got)
	}
}

func TestSGRAndColors(t *testing.T) {
	term := New(20, 2)
	feed(t, term, "\x1b[1;31mA\x1b[38;5;200mB\x1b[48;2;1;2;3mC\x1b[4:3mD\x1b[0mE")
	a := term.CellAt(0, 0)
	if a.Style.Attrs&AttrBold == 0 || a.Style.Fg != IndexedColor(1) {
		t.Fatalf("A style = %+v", a.Style)
	}
	if b := term.CellAt(1, 0); b.Style.Fg != IndexedColor(200) {
		t.Fatalf("B fg = %v", b.Style.Fg)
	}
	if c := term.CellAt(2, 0); c.Style.Bg != RGBColor(1, 2, 3) {
		t.Fatalf("C bg = %v", c.Style.Bg)
	}
	if d := term.CellAt(3, 0); d.Style.Underline != UnderlineCurly {
		t.Fatalf("D underline = %v", d.Style.Underline)
	}
	if e := term.CellAt(4, 0); !e.Style.IsZero() {
		t.Fatalf("E style = %+v, want default", e.Style)
	}
	// Colon-separated truecolor with a colorspace id.
	feed(t, term, "\x1b[38:2::9:8:7mF")
	if f := term.CellAt(5, 0); f.Style.Fg != RGBColor(9, 8, 7) {
		t.Fatalf("F fg = %v", f.Style.Fg)
	}
}

func TestWideAndCombining(t *testing.T) {
	term := New(4, 2)
	feed(t, term, "a漢b")
	if x, _ := term.Cursor(); x != 3 && !term.s.cur.pendingWrap {
		t.Fatalf("cursor x = %d", x)
	}
	if c := term.CellAt(1, 0); c.Ch != "漢" || c.Width != 2 {
		t.Fatalf("wide cell = %+v", c)
	}
	if c := term.CellAt(2, 0); c.Width != 0 {
		t.Fatalf("placeholder cell = %+v", c)
	}
	// A wide character that does not fit wraps whole.
	feed(t, term, "\x1b[1;4H字")
	if got := term.Line(1); got != "字" {
		t.Fatalf("row 1 = %q", got)
	}
	if c := term.CellAt(3, 0); c.Ch != "" {
		t.Fatalf("last cell should be blank after wide wrap, got %+v", c)
	}
	// Combining mark attaches to the previous cell.
	term = New(5, 1)
	feed(t, term, "éx")
	if c := term.CellAt(0, 0); c.Ch != "é" {
		t.Fatalf("combined cell = %q", c.Ch)
	}
	if c := term.CellAt(1, 0); c.Ch != "x" {
		t.Fatalf("next cell = %q", c.Ch)
	}
}

func TestSplitUTF8AcrossWrites(t *testing.T) {
	term := New(10, 1)
	s := []byte("漢字")
	term.Write(s[:1])
	term.Write(s[1:4])
	term.Write(s[4:])
	if got := term.Line(0); got != "漢字" {
		t.Fatalf("row = %q", got)
	}
	// A CSI split across writes.
	term = New(10, 1)
	term.Write([]byte("ab\x1b[1"))
	term.Write([]byte(";1Hz"))
	if got := term.Line(0); got != "zb" {
		t.Fatalf("row = %q", got)
	}
}

func TestAltScreenAndModes(t *testing.T) {
	term := New(10, 3)
	feed(t, term, "main\r\n\x1b[?1049h\x1b[H\x1b[2Jalt\x1b[?25l\x1b[?2004h\x1b[?1000h\x1b[?1006h")
	if !term.AltScreen() || term.Screen() != "alt\n\n" {
		t.Fatalf("alt screen = %v %q", term.AltScreen(), term.Screen())
	}
	if term.cursorVisible || !term.bracketedPaste || term.mouse != 1000 || term.mouseEnc != 1006 {
		t.Fatalf("modes not tracked: vis=%v paste=%v mouse=%v enc=%v", term.cursorVisible, term.bracketedPaste, term.mouse, term.mouseEnc)
	}
	feed(t, term, "\x1b[?1049l")
	if term.AltScreen() || term.Screen() != "main\n\n" {
		t.Fatalf("after leaving alt: %v %q", term.AltScreen(), term.Screen())
	}
	if x, y := term.Cursor(); x != 0 || y != 1 {
		t.Fatalf("main cursor restored to %d,%d want 0,1", x, y)
	}
}

func TestOSCTitleAndLink(t *testing.T) {
	term := New(20, 1)
	feed(t, term, "\x1b]0;my title\x07\x1b]8;;https://x.test\x1b\\link\x1b]8;;\x1b\\ plain")
	if term.Title() != "my title" {
		t.Fatalf("title = %q", term.Title())
	}
	if c := term.CellAt(0, 0); c.Link != "https://x.test" {
		t.Fatalf("link cell = %+v", c)
	}
	if c := term.CellAt(5, 0); c.Link != "" {
		t.Fatalf("plain cell has link %q", c.Link)
	}
}

func TestResize(t *testing.T) {
	term := New(10, 4)
	feed(t, term, "1\r\n2\r\n3\r\n4")
	term.Resize(10, 2)
	if got := term.Screen(); got != "3\n4" {
		t.Fatalf("after shrink: %q", got)
	}
	if term.ScrollbackLen() != 2 {
		t.Fatalf("shrink should push 2 rows to scrollback, got %d", term.ScrollbackLen())
	}
	if _, y := term.Cursor(); y != 1 {
		t.Fatalf("cursor y = %d", y)
	}
	term.Resize(10, 4)
	if got := term.Screen(); got != "1\n2\n3\n4" {
		t.Fatalf("after grow: %q", got)
	}
	if term.ScrollbackLen() != 0 {
		t.Fatalf("grow should pull rows back, scrollback %d", term.ScrollbackLen())
	}
	term.Resize(3, 4)
	feed(t, term, "\x1b[1;1Habcdef")
	if got := term.Screen(); got != "abc\ndef\n3\n4" {
		t.Fatalf("after narrow: %q", got)
	}
}

func TestDECGraphicsAndREP(t *testing.T) {
	term := New(10, 1)
	feed(t, term, "\x1b(0lqk\x1b(Bx\x1b[2b")
	if got := term.Line(0); got != "┌─┐xxx" {
		t.Fatalf("row = %q", got)
	}
}

func TestInlineRedrawLikeInk(t *testing.T) {
	// An Ink-style renderer redraws its frame in place with cursor-up and
	// erase-line. The emulator's screen must reflect only the final frame.
	term := New(20, 5)
	feed(t, term, "$ app\r\n")
	frame := func(n int) string {
		return "\x1b[2K\x1b[Gline A " + strings.Repeat("#", n) + "\r\n\x1b[2K\x1b[Gline B " + strings.Repeat("#", n) + "\r\n"
	}
	feed(t, term, frame(1))
	for i := 2; i <= 4; i++ {
		feed(t, term, "\x1b[2A"+frame(i))
	}
	if got := term.Screen(); got != "$ app\nline A ####\nline B ####\n\n" {
		t.Fatalf("screen = %q", got)
	}
}

// TestSnapshotRoundTrip feeds a snapshot into a fresh emulator and expects
// an identical state: screens, scrollback, cursor and modes.
func TestSnapshotRoundTrip(t *testing.T) {
	inputs := []string{
		"plain text\r\nsecond line",
		"\x1b[1;32mgreen bold\x1b[0m and \x1b[48;2;10;20;30mrgb bg\x1b[0m\r\n漢字 wide\r\n",
		"1\r\n2\r\n3\r\n4\r\n5\r\n6\r\n7\r\n8\r\n\x1b[2;4r\x1b[3;2Hregion",
		"a long line that wraps around the narrow terminal width several times over\r\nnext",
		"\x1b]0;title\x07\x1b[?1049h\x1b[H\x1b[2J\x1b[3;5Halt \x1b[7mtext\x1b[0m\x1b[?25l\x1b[?1000h\x1b[?1006h\x1b[?2004h\x1b[?1h",
		"before\x1b7\x1b[4;4Hsaved cursor test\x1b[?6h\x1b[2;5r\x1b[2;2Hx",
		"\x1b]8;;https://x.test\x1b\\link\x1b]8;;\x1b\\ tail \x1b[4:3mcurly",
		"fill\x1b[5;20Hend", // cursor at the last column, pending wrap
		"tab\there\r\n\x1b(0lqqk\x1b(B\r\n\x1b[?7lnowrap-long-line-past-the-edge",
	}
	for i, in := range inputs {
		src := New(20, 5)
		feed(t, src, in)
		snap := src.Snapshot()
		dst := New(20, 5)
		if _, err := dst.Write(snap); err != nil {
			t.Fatalf("case %d: write snapshot: %v", i, err)
		}
		if a, b := src.Screen(), dst.Screen(); a != b {
			t.Errorf("case %d: screen differs\nsrc: %q\ndst: %q\nsnapshot: %q", i, a, b, snap)
		}
		for y := 0; y < 5; y++ {
			for x := 0; x < 20; x++ {
				if a, b := src.CellAt(x, y), dst.CellAt(x, y); a != b {
					t.Errorf("case %d: cell %d,%d differs: %+v vs %+v", i, x, y, a, b)
				}
			}
		}
		if a, b := src.ScrollbackLen(), dst.ScrollbackLen(); a != b {
			t.Errorf("case %d: scrollback %d vs %d", i, a, b)
		} else {
			for j := 0; j < a; j++ {
				if p, q := src.ScrollbackLine(j), dst.ScrollbackLine(j); p != q {
					t.Errorf("case %d: scrollback line %d: %q vs %q", i, j, p, q)
				}
			}
		}
		ax, ay := src.Cursor()
		bx, by := dst.Cursor()
		if ax != bx || ay != by || src.s.cur.pendingWrap != dst.s.cur.pendingWrap {
			t.Errorf("case %d: cursor %d,%d(%v) vs %d,%d(%v)", i, ax, ay, src.s.cur.pendingWrap, bx, by, dst.s.cur.pendingWrap)
		}
		if src.AltScreen() != dst.AltScreen() || src.pen != dst.pen || src.link != dst.link ||
			src.cursorVisible != dst.cursorVisible || src.mouse != dst.mouse || src.mouseEnc != dst.mouseEnc ||
			src.bracketedPaste != dst.bracketedPaste || src.cursorKeys != dst.cursorKeys ||
			src.autowrap != dst.autowrap || src.origin != dst.origin || src.title != dst.title ||
			src.s.top != dst.s.top || src.s.bot != dst.s.bot || src.s.saved != dst.s.saved ||
			src.charsets != dst.charsets || src.glIndex != dst.glIndex {
			t.Errorf("case %d: state differs\nsrc: %+v\ndst: %+v", i, stateSummary(src), stateSummary(dst))
		}
		// Feeding more output after the snapshot keeps both in step.
		more := "\x1b[Bmore \x1b[31mred\x1b[0m\r\nz"
		feed(t, src, more)
		feed(t, dst, more)
		if a, b := src.Screen(), dst.Screen(); a != b {
			t.Errorf("case %d: post-snapshot divergence\nsrc: %q\ndst: %q", i, a, b)
		}
	}
}

func stateSummary(t *Terminal) map[string]interface{} {
	return map[string]interface{}{
		"alt": t.altActive, "pen": t.pen, "link": t.link, "vis": t.cursorVisible,
		"mouse": t.mouse, "enc": t.mouseEnc, "paste": t.bracketedPaste, "ckm": t.cursorKeys,
		"wrap": t.autowrap, "origin": t.origin, "title": t.title, "top": t.s.top, "bot": t.s.bot,
		"saved": t.s.saved, "charsets": t.charsets, "gl": t.glIndex,
	}
}

func BenchmarkWriteText(b *testing.B) {
	term := New(200, 50)
	chunk := []byte(strings.Repeat("\x1b[32msome \x1b[1mcolored\x1b[0m text on a line 漢字 that is fairly normal\r\n", 100))
	b.SetBytes(int64(len(chunk)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		term.Write(chunk)
	}
}

func BenchmarkWriteFrames(b *testing.B) {
	term := New(120, 40)
	var frame strings.Builder
	for r := 1; r <= 40; r++ {
		frame.WriteString("\x1b[" + itoa(r) + ";1H\x1b[2K\x1b[38;5;" + itoa(r+100) + "m" + strings.Repeat("▇", 119))
	}
	chunk := []byte(frame.String())
	b.SetBytes(int64(len(chunk)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		term.Write(chunk)
	}
}

func itoa(i int) string {
	return strings.TrimSpace(strings.Repeat(" ", 0) + string(rune('0'+i/100%10)) + string(rune('0'+i/10%10)) + string(rune('0'+i%10)))
}
