package vterm

import (
	"strings"
)

// DefaultScrollback is the number of scrollback lines kept when none is
// configured.
const DefaultScrollback = 2000

// MouseMode is the active mouse tracking mode (DECSET 9/1000/1002/1003).
type MouseMode uint16

// MouseEncoding is the mouse report encoding (DECSET 1005/1006/1015).
type MouseEncoding uint16

// cursor is a screen cursor plus the state DECSC saves with it.
type cursor struct {
	x, y        int
	pendingWrap bool
}

type savedCursor struct {
	x, y    int
	pen     Style
	link    string
	charset int
	g       [2]charset
	origin  bool
	valid   bool
}

type charset uint8

const (
	charsetASCII charset = iota
	charsetDECGraphics
)

// screen is one of the two screens (main, alternate).
type screen struct {
	rows       []row
	cur        cursor
	saved      savedCursor
	top, bot   int   // scroll region, inclusive
	scrollback []row // main screen only
}

// Terminal is the emulator. It is not safe for concurrent use; the owner
// serialises Write, Resize and Snapshot.
type Terminal struct {
	w, h      int
	main, alt screen
	s         *screen // active screen
	altActive bool

	maxScrollback int

	pen         Style
	link        string
	lastPrinted rune

	// Modes.
	cursorKeys      bool // DECCKM
	origin          bool // DECOM
	autowrap        bool // DECAWM
	cursorVisible   bool // DECTCEM
	insert          bool // IRM
	lineFeedNewline bool // LNM
	bracketedPaste  bool // 2004
	focusEvents     bool // 1004
	mouse           MouseMode
	mouseEnc        MouseEncoding
	cursorStyle     int // DECSCUSR 0..6
	title           string

	charsets [2]charset
	glIndex  int // which of G0/G1 is shifted in (SI/SO)
	tabStops []bool

	blankRow []Cell // cached blank cells for fast clears, at least w long

	p parser
}

// New returns an emulator of the given size.
func New(w, h int) *Terminal {
	t := &Terminal{maxScrollback: DefaultScrollback}
	t.init(w, h)
	return t
}

func (t *Terminal) init(w, h int) {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	t.w, t.h = w, h
	t.main = newScreen(w, h)
	t.alt = newScreen(w, h)
	t.s = &t.main
	t.altActive = false
	t.pen = Style{}
	t.link = ""
	t.cursorKeys = false
	t.origin = false
	t.autowrap = true
	t.cursorVisible = true
	t.insert = false
	t.lineFeedNewline = false
	t.bracketedPaste = false
	t.focusEvents = false
	t.mouse = 0
	t.mouseEnc = 0
	t.cursorStyle = 0
	t.charsets = [2]charset{charsetASCII, charsetASCII}
	t.glIndex = 0
	t.resetTabStops()
	t.blankRow = nil
	t.p = parser{}
}

func newScreen(w, h int) screen {
	s := screen{rows: make([]row, h), bot: h - 1}
	for i := range s.rows {
		s.rows[i] = newRow(w)
	}
	return s
}

// SetMaxScrollback bounds the scrollback line count.
func (t *Terminal) SetMaxScrollback(n int) {
	if n < 0 {
		n = 0
	}
	t.maxScrollback = n
	t.trimScrollback()
}

// Size returns the terminal size in columns and rows.
func (t *Terminal) Size() (w, h int) { return t.w, t.h }

// Cursor returns the cursor position on the active screen.
func (t *Terminal) Cursor() (x, y int) { return t.s.cur.x, t.s.cur.y }

// AltScreen reports whether the alternate screen is active.
func (t *Terminal) AltScreen() bool { return t.altActive }

// Title returns the window title set by OSC 0/2.
func (t *Terminal) Title() string { return t.title }

// ScrollbackLen returns the number of scrollback lines held.
func (t *Terminal) ScrollbackLen() int { return len(t.main.scrollback) }

// Line returns the text of screen row y (trailing blanks trimmed), for
// tests and diagnostics.
func (t *Terminal) Line(y int) string {
	if y < 0 || y >= t.h {
		return ""
	}
	return rowText(&t.s.rows[y])
}

// ScrollbackLine returns the text of scrollback line i (0 = oldest).
func (t *Terminal) ScrollbackLine(i int) string {
	if i < 0 || i >= len(t.main.scrollback) {
		return ""
	}
	return rowText(&t.main.scrollback[i])
}

// Screen returns the active screen's text, one line per row, trailing
// blanks trimmed.
func (t *Terminal) Screen() string {
	var b strings.Builder
	for y := 0; y < t.h; y++ {
		if y > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(rowText(&t.s.rows[y]))
	}
	return b.String()
}

// CellAt returns the cell at x, y on the active screen.
func (t *Terminal) CellAt(x, y int) Cell {
	if y < 0 || y >= t.h || x < 0 || x >= t.w {
		return Cell{}
	}
	return t.s.rows[y].cells[x]
}

func rowText(r *row) string {
	var b strings.Builder
	n := r.lastVisible()
	for i := 0; i < n; i++ {
		c := &r.cells[i]
		if c.Width == 0 {
			continue
		}
		if c.Ch == "" {
			b.WriteByte(' ')
		} else {
			b.WriteString(c.Ch)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// ---------------------------------------------------------------------------
// Resize
// ---------------------------------------------------------------------------

// Resize changes the terminal size. Columns are truncated or padded and
// rows are added or removed the way xterm.js does with reflow disabled:
// no line is re-wrapped. Content is not lost across the common reconnect
// path because a Snapshot serialises logical lines (wrapped rows joined),
// so the client re-wraps them to its own width; the emulator only clips
// on a narrower-than-before reconnect, which matches xterm.js's own
// no-reflow behaviour. The scroll region resets to the full screen.
func (t *Terminal) Resize(w, h int) {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	if w == t.w && h == t.h {
		return
	}
	t.resizeScreen(&t.main, w, h, true)
	t.resizeScreen(&t.alt, w, h, false)
	t.w, t.h = w, h
	t.blankRow = nil
	t.resetTabStops()
}

func (t *Terminal) resizeScreen(s *screen, w, h int, useScrollback bool) {
	if w != t.w {
		for i := range s.rows {
			s.rows[i] = s.rows[i].fitted(w)
		}
		if s.cur.x >= w {
			s.cur.x = w - 1
		}
		if s.saved.x >= w {
			s.saved.x = w - 1
		}
	}
	oldH := len(s.rows)
	if h < oldH {
		remove := oldH - h
		// Blank rows below the cursor disappear first.
		for remove > 0 && len(s.rows)-1 > s.cur.y && s.rows[len(s.rows)-1].lastVisible() == 0 {
			s.rows = s.rows[:len(s.rows)-1]
			remove--
		}
		// Then rows scroll off the top.
		if remove > 0 {
			if useScrollback {
				for i := 0; i < remove; i++ {
					t.pushScrollback(s, s.rows[i])
				}
			}
			s.rows = s.rows[remove:]
			s.cur.y -= remove
			if s.cur.y < 0 {
				s.cur.y = 0
			}
			s.saved.y -= remove
			if s.saved.y < 0 {
				s.saved.y = 0
			}
		}
	} else if h > oldH {
		add := h - oldH
		if useScrollback {
			// Lines that scrolled off come back, as in xterm.js, when the
			// cursor sits at the bottom.
			for add > 0 && len(s.scrollback) > 0 && s.cur.y == len(s.rows)-1 {
				last := s.scrollback[len(s.scrollback)-1]
				s.scrollback = s.scrollback[:len(s.scrollback)-1]
				s.rows = append([]row{last.fitted(w)}, s.rows...)
				s.cur.y++
				s.saved.y++
				add--
			}
		}
		for i := 0; i < add; i++ {
			s.rows = append(s.rows, newRow(w))
		}
	}
	if s.cur.y >= h {
		s.cur.y = h - 1
	}
	if s.saved.y >= h {
		s.saved.y = h - 1
	}
	s.cur.pendingWrap = false
	s.top, s.bot = 0, h-1
}

// ---------------------------------------------------------------------------
// Scrollback
// ---------------------------------------------------------------------------

func (t *Terminal) pushScrollback(s *screen, r row) {
	if t.maxScrollback == 0 || s != &t.main {
		return
	}
	s.scrollback = append(s.scrollback, r.trimmed())
	if len(s.scrollback) > t.maxScrollback {
		// Drop in batches so a busy stream does not shift the slice on
		// every line.
		drop := len(s.scrollback) - t.maxScrollback
		if drop < t.maxScrollback/8 {
			drop = t.maxScrollback / 8
		}
		if drop > len(s.scrollback) {
			drop = len(s.scrollback)
		}
		copy(s.scrollback, s.scrollback[drop:])
		for i := len(s.scrollback) - drop; i < len(s.scrollback); i++ {
			s.scrollback[i] = row{}
		}
		s.scrollback = s.scrollback[:len(s.scrollback)-drop]
	}
}

func (t *Terminal) trimScrollback() {
	if n := len(t.main.scrollback); n > t.maxScrollback {
		t.main.scrollback = append([]row(nil), t.main.scrollback[n-t.maxScrollback:]...)
	}
}

// ---------------------------------------------------------------------------
// Printing
// ---------------------------------------------------------------------------

func (t *Terminal) print(r rune) {
	s := t.s
	if t.charsets[t.glIndex] == charsetDECGraphics {
		if g, ok := decGraphics[r]; ok {
			r = g
		}
	}
	width := runeWidth(r)
	if width == 0 {
		t.combine(r)
		return
	}
	t.lastPrinted = r
	if s.cur.pendingWrap {
		if t.autowrap {
			t.wrapToNextLine()
		} else {
			s.cur.x = t.w - 1
			s.cur.pendingWrap = false
		}
	}
	if width == 2 && s.cur.x == t.w-1 {
		// A wide character that does not fit wraps whole, leaving the last
		// column blank.
		if t.autowrap {
			s.rows[s.cur.y].cells[s.cur.x] = Cell{Width: 1, Style: eraseStyle(t.pen)}
			t.wrapToNextLine()
		} else {
			s.cur.x = t.w - 2
			if s.cur.x < 0 {
				return
			}
		}
	}
	row := &s.rows[s.cur.y]
	if t.insert {
		t.insertCells(width)
	}
	x := s.cur.x
	// Overwriting half of a wide character blanks its other half.
	if row.cells[x].Width == 0 && x > 0 {
		row.cells[x-1] = Cell{Width: 1, Style: eraseStyle(row.cells[x-1].Style)}
	}
	if width == 1 && x+1 < t.w && row.cells[x].Width == 2 {
		row.cells[x+1] = Cell{Width: 1, Style: eraseStyle(row.cells[x+1].Style)}
	}
	ch := string(r)
	if r == ' ' {
		ch = "" // a printed space is a blank cell; keeps storage and comparisons uniform
	}
	row.cells[x] = Cell{Ch: ch, Width: uint8(width), Style: t.pen, Link: t.link}
	if width == 2 {
		if x+2 < t.w && row.cells[x+1].Width == 2 {
			row.cells[x+2] = Cell{Width: 1, Style: eraseStyle(row.cells[x+2].Style)}
		}
		row.cells[x+1] = Cell{Width: 0, Style: t.pen, Link: t.link}
	}
	s.cur.x += width
	if s.cur.x >= t.w {
		s.cur.x = t.w - 1
		s.cur.pendingWrap = true
	}
}

// combine attaches a zero-width rune to the last printed cell.
func (t *Terminal) combine(r rune) {
	s := t.s
	row := &s.rows[s.cur.y]
	x := s.cur.x
	if s.cur.pendingWrap {
		x = t.w - 1
	} else if x > 0 {
		x--
	} else {
		return
	}
	if row.cells[x].Width == 0 && x > 0 {
		x--
	}
	c := &row.cells[x]
	if c.Ch == "" {
		if c.Width != 1 {
			return
		}
		c.Ch = " "
	}
	c.Ch += string(r)
}

func (t *Terminal) wrapToNextLine() {
	s := t.s
	s.cur.pendingWrap = false
	s.cur.x = 0
	t.lineFeed()
	s.rows[s.cur.y].wrapped = true
}

// insertCells shifts the cells at the cursor right by n (IRM / ICH).
func (t *Terminal) insertCells(n int) {
	s := t.s
	row := &s.rows[s.cur.y]
	x := s.cur.x
	if n > t.w-x {
		n = t.w - x
	}
	copy(row.cells[x+n:], row.cells[x:t.w-n])
	for i := x; i < x+n; i++ {
		row.cells[i] = Cell{Width: 1, Style: eraseStyle(t.pen)}
	}
	if row.cells[t.w-1].Width == 2 {
		row.cells[t.w-1] = Cell{Width: 1, Style: eraseStyle(row.cells[t.w-1].Style)}
	}
}

// ---------------------------------------------------------------------------
// C0 controls
// ---------------------------------------------------------------------------

func (t *Terminal) execute(b byte) {
	s := t.s
	switch b {
	case 0x07: // BEL
	case 0x08: // BS
		if s.cur.pendingWrap {
			s.cur.pendingWrap = false
		}
		if s.cur.x > 0 {
			s.cur.x--
		}
	case 0x09: // HT
		t.tabForward(1)
	case 0x0a, 0x0b, 0x0c: // LF, VT, FF
		t.lineFeed()
		if t.lineFeedNewline {
			s.cur.x = 0
		}
	case 0x0d: // CR
		s.cur.x = 0
		s.cur.pendingWrap = false
	case 0x0e: // SO
		t.glIndex = 1
	case 0x0f: // SI
		t.glIndex = 0
	}
}

func (t *Terminal) lineFeed() {
	s := t.s
	s.cur.pendingWrap = false
	if s.cur.y == s.bot {
		t.scroll(1, true)
	} else if s.cur.y < t.h-1 {
		s.cur.y++
	}
}

func (t *Terminal) reverseIndex() {
	s := t.s
	s.cur.pendingWrap = false
	if s.cur.y == s.top {
		t.scrollDown(1)
	} else if s.cur.y > 0 {
		s.cur.y--
	}
}

// scrollUp implements SU: the region scrolls and the lines that leave it
// are lost.
func (t *Terminal) scrollUp(n int) { t.scroll(n, false) }

// scroll moves the scroll region up n lines. When the scroll is the result
// of a line feed at the bottom margin and the region starts at the top
// row, the lines leaving it enter the main screen's scrollback, as in
// xterm.js (a region that starts lower loses them).
func (t *Terminal) scroll(n int, keep bool) {
	s := t.s
	if n <= 0 {
		return
	}
	if n > s.bot-s.top+1 {
		n = s.bot - s.top + 1
	}
	// The rows leaving the region are recycled as the blank rows entering
	// it at the bottom; scrollback keeps a trimmed copy.
	recycled := make([]row, n)
	copy(recycled, s.rows[s.top:s.top+n])
	if keep && s.top == 0 {
		for i := 0; i < n; i++ {
			t.pushScrollback(s, recycled[i])
		}
	}
	copy(s.rows[s.top:], s.rows[s.top+n:s.bot+1])
	for i, y := 0, s.bot-n+1; y <= s.bot; i, y = i+1, y+1 {
		r := recycled[i]
		t.clearRow(&r, 0, t.w)
		r.wrapped = false
		s.rows[y] = r
	}
	if s.top < t.h && s.top <= s.bot {
		s.rows[s.top].wrapped = false
	}
}

// clearRow blanks cells [from, to) of r with the pen's background. A
// default-background clear copies from a cached blank row, which is a
// memmove rather than a per-cell store.
func (t *Terminal) clearRow(r *row, from, to int) {
	if t.pen.Bg != 0 {
		r.clear(from, to, t.pen)
		return
	}
	if len(t.blankRow) < t.w {
		t.blankRow = newRow(t.w).cells
	}
	if from < 0 {
		from = 0
	}
	if to > len(r.cells) {
		to = len(r.cells)
	}
	if from < to {
		copy(r.cells[from:to], t.blankRow[:to-from])
	}
}

func (t *Terminal) scrollDown(n int) {
	s := t.s
	if n <= 0 {
		return
	}
	if n > s.bot-s.top+1 {
		n = s.bot - s.top + 1
	}
	copy(s.rows[s.top+n:s.bot+1], s.rows[s.top:s.bot+1-n])
	for y := s.top; y < s.top+n; y++ {
		s.rows[y] = newRow(t.w)
		s.rows[y].clear(0, t.w, t.pen)
	}
}

// ---------------------------------------------------------------------------
// Tab stops
// ---------------------------------------------------------------------------

func (t *Terminal) resetTabStops() {
	t.tabStops = make([]bool, t.w)
	for i := 8; i < t.w; i += 8 {
		t.tabStops[i] = true
	}
}

func (t *Terminal) tabForward(n int) {
	s := t.s
	s.cur.pendingWrap = false
	for ; n > 0; n-- {
		x := s.cur.x + 1
		for x < t.w && !t.tabStops[x] {
			x++
		}
		if x >= t.w {
			x = t.w - 1
		}
		s.cur.x = x
	}
}

func (t *Terminal) tabBackward(n int) {
	s := t.s
	s.cur.pendingWrap = false
	for ; n > 0; n-- {
		x := s.cur.x - 1
		for x > 0 && !t.tabStops[x] {
			x--
		}
		if x < 0 {
			x = 0
		}
		s.cur.x = x
	}
}

// ---------------------------------------------------------------------------
// Cursor movement helpers
// ---------------------------------------------------------------------------

func (t *Terminal) moveCursor(x, y int) {
	s := t.s
	minY, maxY := 0, t.h-1
	if t.origin {
		minY, maxY = s.top, s.bot
	}
	if y < minY {
		y = minY
	}
	if y > maxY {
		y = maxY
	}
	if x < 0 {
		x = 0
	}
	if x > t.w-1 {
		x = t.w - 1
	}
	s.cur.x, s.cur.y = x, y
	s.cur.pendingWrap = false
}

func (t *Terminal) cursorUp(n int) {
	s := t.s
	limit := 0
	if s.cur.y >= s.top {
		limit = s.top
	}
	y := s.cur.y - n
	if y < limit {
		y = limit
	}
	s.cur.y = y
	s.cur.pendingWrap = false
}

func (t *Terminal) cursorDown(n int) {
	s := t.s
	limit := t.h - 1
	if s.cur.y <= s.bot {
		limit = s.bot
	}
	y := s.cur.y + n
	if y > limit {
		y = limit
	}
	s.cur.y = y
	s.cur.pendingWrap = false
}

func (t *Terminal) saveCursor() {
	s := t.s
	s.saved = savedCursor{
		x: s.cur.x, y: s.cur.y, pen: t.pen, link: t.link,
		charset: t.glIndex, g: t.charsets, origin: t.origin, valid: true,
	}
}

func (t *Terminal) restoreCursor() {
	s := t.s
	if !s.saved.valid {
		t.moveCursor(0, 0)
		t.pen = Style{}
		t.link = ""
		return
	}
	t.pen = s.saved.pen
	t.link = s.saved.link
	t.glIndex = s.saved.charset
	t.charsets = s.saved.g
	t.origin = s.saved.origin
	s.cur.x, s.cur.y = s.saved.x, s.saved.y
	if s.cur.x > t.w-1 {
		s.cur.x = t.w - 1
	}
	if s.cur.y > t.h-1 {
		s.cur.y = t.h - 1
	}
	s.cur.pendingWrap = false
}

// ---------------------------------------------------------------------------
// Erase / insert / delete
// ---------------------------------------------------------------------------

func (t *Terminal) eraseInDisplay(mode int) {
	s := t.s
	switch mode {
	case 0: // cursor to end
		t.eraseInLine(0)
		for y := s.cur.y + 1; y < t.h; y++ {
			s.rows[y].clear(0, t.w, t.pen)
			s.rows[y].wrapped = false
		}
	case 1: // start to cursor
		for y := 0; y < s.cur.y; y++ {
			s.rows[y].clear(0, t.w, t.pen)
			s.rows[y].wrapped = false
		}
		t.eraseInLine(1)
	case 2: // whole screen
		for y := 0; y < t.h; y++ {
			t.clearRow(&s.rows[y], 0, t.w)
			s.rows[y].wrapped = false
		}
	case 3: // scrollback
		s.scrollback = nil
	}
}

func (t *Terminal) eraseInLine(mode int) {
	s := t.s
	row := &s.rows[s.cur.y]
	switch mode {
	case 0:
		t.clearRow(row, s.cur.x, t.w)
	case 1:
		end := s.cur.x + 1
		// Erasing the first half of a wide character clears both halves.
		if end < t.w && row.cells[end].Width == 0 {
			end++
		}
		row.clear(0, end, t.pen)
	case 2:
		t.clearRow(row, 0, t.w)
	}
}

func (t *Terminal) eraseChars(n int) {
	s := t.s
	end := s.cur.x + n
	if end > t.w {
		end = t.w
	}
	s.rows[s.cur.y].clear(s.cur.x, end, t.pen)
	s.cur.pendingWrap = false
}

func (t *Terminal) deleteChars(n int) {
	s := t.s
	row := &s.rows[s.cur.y]
	x := s.cur.x
	if n > t.w-x {
		n = t.w - x
	}
	copy(row.cells[x:], row.cells[x+n:])
	row.clear(t.w-n, t.w, t.pen)
	s.cur.pendingWrap = false
}

func (t *Terminal) insertLines(n int) {
	s := t.s
	if s.cur.y < s.top || s.cur.y > s.bot {
		return
	}
	if n > s.bot-s.cur.y+1 {
		n = s.bot - s.cur.y + 1
	}
	copy(s.rows[s.cur.y+n:s.bot+1], s.rows[s.cur.y:s.bot+1-n])
	for y := s.cur.y; y < s.cur.y+n; y++ {
		s.rows[y] = newRow(t.w)
		s.rows[y].clear(0, t.w, t.pen)
	}
	s.cur.x = 0
	s.cur.pendingWrap = false
}

func (t *Terminal) deleteLines(n int) {
	s := t.s
	if s.cur.y < s.top || s.cur.y > s.bot {
		return
	}
	if n > s.bot-s.cur.y+1 {
		n = s.bot - s.cur.y + 1
	}
	copy(s.rows[s.cur.y:], s.rows[s.cur.y+n:s.bot+1])
	for y := s.bot - n + 1; y <= s.bot; y++ {
		s.rows[y] = newRow(t.w)
		s.rows[y].clear(0, t.w, t.pen)
	}
	s.cur.x = 0
	s.cur.pendingWrap = false
}

// ---------------------------------------------------------------------------
// Screens and resets
// ---------------------------------------------------------------------------

// enterAltScreen activates the alternate screen. As in xterm.js the
// alternate buffer is cleared on every activation (with the pen's
// background) and the cursor position carries over from the main screen;
// DECSET 1049 additionally saves the main cursor first.
func (t *Terminal) enterAltScreen(saveCursor bool) {
	if t.altActive {
		return
	}
	if saveCursor {
		t.saveCursor()
	}
	t.altActive = true
	t.s = &t.alt
	for y := range t.alt.rows {
		t.alt.rows[y].clear(0, t.w, t.pen)
		t.alt.rows[y].wrapped = false
	}
	t.alt.top, t.alt.bot = 0, t.h-1
	t.alt.cur = cursor{x: t.main.cur.x, y: t.main.cur.y}
}

// leaveAltScreen returns to the main screen. The cursor position carries
// over from the alternate screen; DECRST 1049 then restores the cursor
// saved on entry.
func (t *Terminal) leaveAltScreen(restoreCursor bool) {
	if !t.altActive {
		return
	}
	t.altActive = false
	t.s = &t.main
	t.main.cur = cursor{x: t.alt.cur.x, y: t.alt.cur.y}
	if restoreCursor {
		t.restoreCursor()
	}
}

// softReset implements DECSTR.
func (t *Terminal) softReset() {
	s := t.s
	t.cursorVisible = true
	t.origin = false
	t.autowrap = true
	t.insert = false
	t.cursorKeys = false
	s.top, s.bot = 0, t.h-1
	t.pen = Style{}
	t.link = ""
	t.charsets = [2]charset{charsetASCII, charsetASCII}
	t.glIndex = 0
	s.saved = savedCursor{}
	s.cur.pendingWrap = false
}

// fullReset implements RIS. Scrollback survives, as in xterm.
func (t *Terminal) fullReset() {
	sb := t.main.scrollback
	title := t.title
	t.init(t.w, t.h)
	t.main.scrollback = sb
	t.title = title
}

// decGraphics maps the DEC Special Graphics set onto Unicode.
var decGraphics = map[rune]rune{
	'`': '◆', 'a': '▒', 'b': '␉', 'c': '␌', 'd': '␍', 'e': '␊', 'f': '°', 'g': '±',
	'h': '␤', 'i': '␋', 'j': '┘', 'k': '┐', 'l': '┌', 'm': '└', 'n': '┼', 'o': '⎺',
	'p': '⎻', 'q': '─', 'r': '⎼', 's': '⎽', 't': '├', 'u': '┤', 'v': '┴', 'w': '┬',
	'x': '│', 'y': '≤', 'z': '≥', '{': 'π', '|': '≠', '}': '£', '~': '·',
}
