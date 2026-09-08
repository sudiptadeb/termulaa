package vterm

import "strconv"

// escDispatch handles ESC sequences (two-character escapes and those with
// one intermediate byte).
func (t *Terminal) escDispatch(inter, final byte) {
	switch inter {
	case 0:
		switch final {
		case '7':
			t.saveCursor()
		case '8':
			t.restoreCursor()
		case 'D': // IND
			t.lineFeed()
		case 'E': // NEL
			t.lineFeed()
			t.s.cur.x = 0
		case 'H': // HTS
			if t.s.cur.x < len(t.tabStops) {
				t.tabStops[t.s.cur.x] = true
			}
		case 'M': // RI
			t.reverseIndex()
		case 'c': // RIS
			t.fullReset()
		case '=', '>': // DECKPAM / DECKPNM: keypad modes, not rendered
		}
	case '(':
		t.charsets[0] = designate(final)
	case ')':
		t.charsets[1] = designate(final)
	case '#':
		if final == '8' { // DECALN
			for y := 0; y < t.h; y++ {
				for x := 0; x < t.w; x++ {
					t.s.rows[y].cells[x] = Cell{Ch: "E", Width: 1}
				}
				t.s.rows[y].wrapped = false
			}
			t.s.top, t.s.bot = 0, t.h-1
			t.moveCursor(0, 0)
		}
	}
}

func designate(final byte) charset {
	if final == '0' {
		return charsetDECGraphics
	}
	return charsetASCII
}

// csiDispatch handles a complete CSI sequence.
func (t *Terminal) csiDispatch(final byte) {
	p := &t.p
	s := t.s

	if p.intermediate != 0 {
		switch {
		case p.intermediate == ' ' && final == 'q': // DECSCUSR
			t.cursorStyle = p.rawParam(0, 0)
		case p.intermediate == '!' && final == 'p': // DECSTR
			t.softReset()
		}
		return
	}

	switch p.private {
	case '?':
		switch final {
		case 'h':
			t.setPrivateModes(true)
		case 'l':
			t.setPrivateModes(false)
		}
		return
	case '>', '<', '=':
		// DA2/DA3, XTVERSION, kitty keyboard, xterm modifier options: no
		// rendering effect.
		return
	}

	switch final {
	case '@': // ICH
		t.insertCells(p.param(0, 1))
		s.cur.pendingWrap = false
	case 'A': // CUU
		t.cursorUp(p.param(0, 1))
	case 'B', 'e': // CUD, VPR
		t.cursorDown(p.param(0, 1))
	case 'C', 'a': // CUF, HPR
		x := s.cur.x + p.param(0, 1)
		if x > t.w-1 {
			x = t.w - 1
		}
		s.cur.x = x
		s.cur.pendingWrap = false
	case 'D': // CUB
		x := s.cur.x - p.param(0, 1)
		if x < 0 {
			x = 0
		}
		s.cur.x = x
		s.cur.pendingWrap = false
	case 'E': // CNL
		t.cursorDown(p.param(0, 1))
		s.cur.x = 0
	case 'F': // CPL
		t.cursorUp(p.param(0, 1))
		s.cur.x = 0
	case 'G', '`': // CHA, HPA
		x := p.param(0, 1) - 1
		if x > t.w-1 {
			x = t.w - 1
		}
		if x < 0 {
			x = 0
		}
		s.cur.x = x
		s.cur.pendingWrap = false
	case 'H', 'f': // CUP, HVP
		y := p.param(0, 1) - 1
		x := p.param(1, 1) - 1
		if t.origin {
			y += s.top
		}
		t.moveCursor(x, y)
	case 'I': // CHT
		t.tabForward(p.param(0, 1))
	case 'J': // ED
		t.eraseInDisplay(p.rawParam(0, 0))
	case 'K': // EL
		t.eraseInLine(p.rawParam(0, 0))
	case 'L': // IL
		t.insertLines(p.param(0, 1))
	case 'M': // DL
		t.deleteLines(p.param(0, 1))
	case 'P': // DCH
		t.deleteChars(p.param(0, 1))
	case 'S': // SU
		t.scrollUp(p.param(0, 1))
	case 'T': // SD
		t.scrollDown(p.param(0, 1))
	case 'X': // ECH
		t.eraseChars(p.param(0, 1))
	case 'Z': // CBT
		t.tabBackward(p.param(0, 1))
	case 'b': // REP
		if t.lastPrinted != 0 {
			r := t.lastPrinted
			for i := p.param(0, 1); i > 0; i-- {
				t.print(r)
			}
		}
	case 'd': // VPA
		y := p.param(0, 1) - 1
		if t.origin {
			y += s.top
		}
		t.moveCursor(s.cur.x, y)
	case 'g': // TBC
		switch p.rawParam(0, 0) {
		case 0:
			if s.cur.x < len(t.tabStops) {
				t.tabStops[s.cur.x] = false
			}
		case 3:
			for i := range t.tabStops {
				t.tabStops[i] = false
			}
		}
	case 'h':
		t.setModes(true)
	case 'l':
		t.setModes(false)
	case 'm':
		t.sgr()
	case 'r': // DECSTBM
		top := p.param(0, 1) - 1
		bot := p.param(1, t.h) - 1
		if bot > t.h-1 {
			bot = t.h - 1
		}
		if top < 0 {
			top = 0
		}
		if top < bot {
			s.top, s.bot = top, bot
			t.moveCursor(0, 0)
		}
	case 's': // SCOSC
		t.saveCursor()
	case 'u': // SCORC
		t.restoreCursor()
	case 'c', 'n', 't', 'x', 'q':
		// DA, DSR, window ops, DECREQTPARM: queries and window management,
		// answered by the real terminal, never by this one.
	}
}

func (t *Terminal) setModes(on bool) {
	p := &t.p
	for i := 0; i < p.nParams; i++ {
		switch p.rawParam(i, -1) {
		case 4:
			t.insert = on
		case 20:
			t.lineFeedNewline = on
		}
	}
}

func (t *Terminal) setPrivateModes(on bool) {
	p := &t.p
	for i := 0; i < p.nParams; i++ {
		switch p.rawParam(i, -1) {
		case 1:
			t.cursorKeys = on
		case 6:
			t.origin = on
			t.moveCursor(0, 0)
		case 7:
			t.autowrap = on
		case 25:
			t.cursorVisible = on
		case 9, 1000, 1002, 1003:
			if on {
				t.mouse = MouseMode(p.rawParam(i, 0))
			} else if t.mouse == MouseMode(p.rawParam(i, 0)) {
				t.mouse = 0
			}
		case 1005, 1006, 1015:
			if on {
				t.mouseEnc = MouseEncoding(p.rawParam(i, 0))
			} else if t.mouseEnc == MouseEncoding(p.rawParam(i, 0)) {
				t.mouseEnc = 0
			}
		case 1004:
			t.focusEvents = on
		case 2004:
			t.bracketedPaste = on
		case 47, 1047:
			if on {
				t.enterAltScreen(false)
			} else {
				t.leaveAltScreen(false)
			}
		case 1049:
			if on {
				t.enterAltScreen(true)
			} else {
				t.leaveAltScreen(true)
			}
		}
	}
}

// sgr applies Select Graphic Rendition parameters, including the colon
// sub-parameter forms for extended colors and underline styles.
func (t *Terminal) sgr() {
	p := &t.p
	if p.nParams == 0 {
		t.pen = Style{}
		return
	}
	for i := 0; i < p.nParams; i++ {
		if p.isSub[i] {
			continue // consumed by the parameter it belongs to
		}
		v := p.rawParam(i, 0)
		switch {
		case v == 0:
			t.pen = Style{}
		case v == 1:
			t.pen.Attrs |= AttrBold
		case v == 2:
			t.pen.Attrs |= AttrDim
		case v == 3:
			t.pen.Attrs |= AttrItalic
		case v == 4:
			style := UnderlineSingle
			if i+1 < p.nParams && p.isSub[i+1] {
				switch p.rawParam(i+1, 1) {
				case 0:
					style = UnderlineNone
				case 1:
					style = UnderlineSingle
				case 2:
					style = UnderlineDouble
				case 3:
					style = UnderlineCurly
				case 4:
					style = UnderlineDotted
				case 5:
					style = UnderlineDashed
				}
			}
			t.pen.Underline = style
		case v == 5 || v == 6:
			t.pen.Attrs |= AttrBlink
		case v == 7:
			t.pen.Attrs |= AttrInverse
		case v == 8:
			t.pen.Attrs |= AttrHidden
		case v == 9:
			t.pen.Attrs |= AttrStrike
		case v == 21:
			t.pen.Underline = UnderlineDouble
		case v == 22:
			t.pen.Attrs &^= AttrBold | AttrDim
		case v == 23:
			t.pen.Attrs &^= AttrItalic
		case v == 24:
			t.pen.Underline = UnderlineNone
		case v == 25:
			t.pen.Attrs &^= AttrBlink
		case v == 27:
			t.pen.Attrs &^= AttrInverse
		case v == 28:
			t.pen.Attrs &^= AttrHidden
		case v == 29:
			t.pen.Attrs &^= AttrStrike
		case v >= 30 && v <= 37:
			t.pen.Fg = IndexedColor(uint8(v - 30))
		case v == 38:
			c, used := t.extendedColor(i)
			if used > 0 {
				t.pen.Fg = c
				i += used
			}
		case v == 39:
			t.pen.Fg = 0
		case v >= 40 && v <= 47:
			t.pen.Bg = IndexedColor(uint8(v - 40))
		case v == 48:
			c, used := t.extendedColor(i)
			if used > 0 {
				t.pen.Bg = c
				i += used
			}
		case v == 49:
			t.pen.Bg = 0
		case v == 58:
			c, used := t.extendedColor(i)
			if used > 0 {
				t.pen.Ul = c
				i += used
			}
		case v == 59:
			t.pen.Ul = 0
		case v >= 90 && v <= 97:
			t.pen.Fg = IndexedColor(uint8(v - 90 + 8))
		case v >= 100 && v <= 107:
			t.pen.Bg = IndexedColor(uint8(v - 100 + 8))
		}
	}
}

// extendedColor parses the arguments after SGR 38/48/58 at index i. It
// accepts "5;n", "2;r;g;b" with ';' separators and the ':' forms
// "5:n", "2:r:g:b" and "2::r:g:b" (with a colorspace id). It returns the
// color and how many parameters were consumed (0 when malformed).
func (t *Terminal) extendedColor(i int) (Color, int) {
	p := &t.p
	sub := i+1 < p.nParams && p.isSub[i+1]
	mode := p.rawParam(i+1, -1)
	switch mode {
	case 5:
		if i+2 >= p.nParams {
			return 0, 0
		}
		n := p.rawParam(i+2, 0)
		if n < 0 || n > 255 {
			return 0, 0
		}
		return IndexedColor(uint8(n)), 2
	case 2:
		start := i + 2
		used := 4
		if sub && i+5 < p.nParams && p.isSub[i+5] {
			// 2::r:g:b — an empty colorspace id precedes the components.
			// Detect it when the sequence is long enough and the first
			// component slot is empty.
			if !p.hasValue[i+2] {
				start = i + 3
				used = 5
			}
		}
		if start+2 >= p.nParams {
			return 0, 0
		}
		r, g, b := p.rawParam(start, 0), p.rawParam(start+1, 0), p.rawParam(start+2, 0)
		if r > 255 || g > 255 || b > 255 {
			return 0, 0
		}
		return RGBColor(uint8(r), uint8(g), uint8(b)), used
	}
	return 0, 0
}

// dispatchOSC handles operating system commands: title and hyperlinks.
func (t *Terminal) dispatchOSC() {
	data := t.p.osc
	sep := -1
	for i, b := range data {
		if b == ';' {
			sep = i
			break
		}
	}
	if sep < 0 {
		return
	}
	code, err := strconv.Atoi(string(data[:sep]))
	if err != nil {
		return
	}
	arg := data[sep+1:]
	switch code {
	case 0, 2:
		t.title = string(arg)
	case 8:
		// OSC 8 ; params ; URI
		psep := -1
		for i, b := range arg {
			if b == ';' {
				psep = i
				break
			}
		}
		if psep < 0 {
			return
		}
		t.link = string(arg[psep+1:])
	}
}
