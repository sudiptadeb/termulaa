package vterm

import "unicode/utf8"

// The parser follows the VT500-series state machine (Paul Williams'
// description): ground, escape, CSI, OSC and the string states DCS, SOS,
// PM and APC. Strings other than OSC are consumed and ignored. C0 controls
// execute in every state except inside strings, as on real hardware, so a
// CR/LF in the middle of a malformed sequence still moves the cursor.

type parserState uint8

const (
	stateGround parserState = iota
	stateEscape
	stateEscapeIntermediate
	stateCSIEntry
	stateCSIParam
	stateCSIIntermediate
	stateCSIIgnore
	stateOSC
	stateOSCEscape // saw ESC inside an OSC: ESC \ terminates it
	stateString    // DCS / SOS / PM / APC payload, ignored
	stateStringEscape
)

const (
	maxParams   = 32
	maxOSCBytes = 4096
)

type parser struct {
	state parserState

	// CSI accumulation.
	params       [maxParams]int
	hasValue     [maxParams]bool
	isSub        [maxParams]bool // parameter was introduced by ':' (a sub-parameter)
	nParams      int
	intermediate byte
	private      byte // '?', '>', '<', '=' or 0
	overflow     bool

	osc []byte

	// partial holds an incomplete UTF-8 sequence cut by the end of a Write.
	partial [utf8.UTFMax]byte
	nPart   int
}

func (p *parser) resetCSI() {
	for i := 0; i < p.nParams+1 && i < maxParams; i++ {
		p.params[i] = 0
		p.hasValue[i] = false
		p.isSub[i] = false
	}
	p.nParams = 0
	p.intermediate = 0
	p.private = 0
	p.overflow = false
}

// param returns parameter i or def when it is absent or zero.
func (p *parser) param(i, def int) int {
	if i >= p.nParams || !p.hasValue[i] || p.params[i] == 0 {
		return def
	}
	return p.params[i]
}

// rawParam returns parameter i, or def when absent (zero is a value).
func (p *parser) rawParam(i, def int) int {
	if i >= p.nParams || !p.hasValue[i] {
		return def
	}
	return p.params[i]
}

// Write feeds terminal output. It always consumes the whole slice.
func (t *Terminal) Write(data []byte) (int, error) {
	n := len(data)
	if t.p.nPart > 0 {
		// Finish the rune cut by the previous Write.
		for len(data) > 0 && t.p.nPart < utf8.UTFMax {
			t.p.partial[t.p.nPart] = data[0]
			t.p.nPart++
			data = data[1:]
			if utf8.FullRune(t.p.partial[:t.p.nPart]) {
				break
			}
		}
		if !utf8.FullRune(t.p.partial[:t.p.nPart]) {
			return n, nil // still incomplete; wait for more
		}
		r, size := utf8.DecodeRune(t.p.partial[:t.p.nPart])
		rest := t.p.partial[size:t.p.nPart]
		t.p.nPart = 0
		t.feedRune(r)
		for len(rest) > 0 {
			// Bytes pulled in beyond the rune belong to the next one.
			rr, sz := utf8.DecodeRune(rest)
			t.feedRune(rr)
			rest = rest[sz:]
		}
	}
	for i := 0; i < len(data); {
		b := data[i]
		if b < utf8.RuneSelf || t.p.state != stateGround && t.p.state != stateOSC && t.p.state != stateString {
			// ASCII, or inside a sequence where only ASCII is meaningful.
			t.feedByte(b)
			i++
			continue
		}
		r, size := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && size == 1 {
			if !utf8.FullRune(data[i:]) {
				// Cut mid-rune at the end of the chunk: carry it over.
				t.p.nPart = copy(t.p.partial[:], data[i:])
				return n, nil
			}
			// Genuinely invalid byte: dropped, as xterm.js's decoder does.
			i++
			continue
		}
		t.feedRune(r)
		i += size
	}
	return n, nil
}

// feedRune routes a decoded non-ASCII rune. In ground it is printed; inside
// an OSC it is part of the string; inside other strings it is ignored.
func (t *Terminal) feedRune(r rune) {
	switch t.p.state {
	case stateGround:
		t.print(r)
	case stateOSC:
		if len(t.p.osc) < maxOSCBytes {
			t.p.osc = utf8.AppendRune(t.p.osc, r)
		}
	}
}

func (t *Terminal) feedByte(b byte) {
	p := &t.p

	// C0 controls and DEL. Inside string payloads they are part of the
	// string (BEL and ESC terminate OSC); everywhere else they execute.
	if b < 0x20 || b == 0x7f {
		switch p.state {
		case stateOSC:
			switch b {
			case 0x07:
				t.dispatchOSC()
				p.state = stateGround
			case 0x1b:
				p.state = stateOSCEscape
			}
			return
		case stateString:
			if b == 0x1b {
				p.state = stateStringEscape
			}
			return
		case stateOSCEscape, stateStringEscape:
			// ESC followed by a control: abandon the string, run the control.
			p.state = stateGround
		}
		if b == 0x1b {
			p.resetCSI()
			p.state = stateEscape
			return
		}
		if b == 0x18 || b == 0x1a { // CAN, SUB abort any sequence
			p.state = stateGround
			return
		}
		t.execute(b)
		return
	}

	switch p.state {
	case stateGround:
		t.print(rune(b))

	case stateEscape:
		switch {
		case b >= 0x20 && b <= 0x2f:
			p.intermediate = b
			p.state = stateEscapeIntermediate
		case b == '[':
			p.state = stateCSIEntry
		case b == ']':
			p.osc = p.osc[:0]
			p.state = stateOSC
		case b == 'P' || b == 'X' || b == '^' || b == '_':
			p.state = stateString
		default:
			p.state = stateGround
			t.escDispatch(0, b)
		}

	case stateEscapeIntermediate:
		if b >= 0x20 && b <= 0x2f {
			p.intermediate = b // keep the last one; enough for what we handle
			return
		}
		p.state = stateGround
		t.escDispatch(p.intermediate, b)

	case stateCSIEntry:
		switch {
		case b >= 0x30 && b <= 0x3b:
			p.state = stateCSIParam
			t.feedByte(b)
		case b >= 0x3c && b <= 0x3f:
			p.private = b
			p.state = stateCSIParam
		case b >= 0x20 && b <= 0x2f:
			p.intermediate = b
			p.state = stateCSIIntermediate
		default:
			p.state = stateGround
			t.csiDispatch(b)
		}

	case stateCSIParam:
		switch {
		case b >= '0' && b <= '9':
			p.pushDigit(b)
		case b == ';':
			p.nextParam(false)
		case b == ':':
			p.nextParam(true)
		case b >= 0x3c && b <= 0x3f:
			p.state = stateCSIIgnore
		case b >= 0x20 && b <= 0x2f:
			p.intermediate = b
			p.state = stateCSIIntermediate
		default:
			p.state = stateGround
			t.csiDispatch(b)
		}

	case stateCSIIntermediate:
		switch {
		case b >= 0x20 && b <= 0x2f:
			p.intermediate = b
		case b >= 0x30 && b <= 0x3f:
			p.state = stateCSIIgnore
		default:
			p.state = stateGround
			t.csiDispatch(b)
		}

	case stateCSIIgnore:
		if b >= 0x40 && b <= 0x7e {
			p.state = stateGround
		}

	case stateOSC:
		if len(p.osc) < maxOSCBytes {
			p.osc = append(p.osc, b)
		}

	case stateOSCEscape:
		p.state = stateGround
		if b == '\\' {
			t.dispatchOSC()
		} else {
			// ESC followed by something else: the OSC is abandoned and the
			// ESC sequence begins.
			p.resetCSI()
			p.state = stateEscape
			t.feedByte(b)
		}

	case stateString:
		// payload ignored

	case stateStringEscape:
		p.state = stateGround
		if b != '\\' {
			p.resetCSI()
			p.state = stateEscape
			t.feedByte(b)
		}
	}
}

func (p *parser) pushDigit(b byte) {
	if p.overflow {
		return
	}
	if p.nParams == 0 {
		p.nParams = 1
	}
	i := p.nParams - 1
	v := p.params[i]*10 + int(b-'0')
	if v > 1<<24 {
		v = 1 << 24
	}
	p.params[i] = v
	p.hasValue[i] = true
}

func (p *parser) nextParam(sub bool) {
	if p.overflow {
		return
	}
	if p.nParams == 0 {
		p.nParams = 1 // an empty first parameter
	}
	if p.nParams >= maxParams {
		p.overflow = true
		return
	}
	p.params[p.nParams] = 0
	p.hasValue[p.nParams] = false
	p.isSub[p.nParams] = sub
	p.nParams++
}
