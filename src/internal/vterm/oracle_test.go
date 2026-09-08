package vterm

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgainstXterm feeds a corpus of byte streams to this emulator and to
// the xterm.js bundle termulaa ships (in headless Chromium, via
// testdata/xterm_oracle.js) and compares what each shows: every line of
// scrollback and screen, the cursor, the active buffer, per-cell
// attributes and the input-affecting modes.
//
// It needs node with playwright-core resolvable (NODE_PATH) and a
// Playwright Chromium (PLAYWRIGHT_BROWSERS_PATH or PLAYWRIGHT_CHROMIUM);
// otherwise it is skipped. Set VTERM_ORACLE=1 to make a missing oracle a
// failure instead of a skip.
func TestAgainstXterm(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		skipOrFail(t, "node not found")
		return
	}
	if err := exec.Command(node, "-e", "require('playwright-core')").Run(); err != nil {
		skipOrFail(t, "playwright-core not resolvable from NODE_PATH")
		return
	}
	xterm := filepath.Join("..", "..", "cmd", "termulaa", "ui", "lib", "xterm.min.js")
	if _, err := os.Stat(xterm); err != nil {
		t.Fatalf("xterm bundle missing: %v", err)
	}

	const cols, rows = 24, 6
	cases := oracleCorpus()

	enc := make([]interface{}, len(cases))
	for i, c := range cases {
		if len(c.steps) == 0 {
			enc[i] = base64.StdEncoding.EncodeToString([]byte(c.input))
			continue
		}
		steps := make([]map[string]interface{}, len(c.steps))
		for j, st := range c.steps {
			if st.resize[0] > 0 {
				steps[j] = map[string]interface{}{"resize": []int{st.resize[0], st.resize[1]}}
			} else {
				steps[j] = map[string]interface{}{"write": base64.StdEncoding.EncodeToString([]byte(st.write))}
			}
		}
		enc[i] = steps
	}
	job := map[string]interface{}{"cols": cols, "rows": rows, "cases": enc}
	in, _ := json.Marshal(job)

	cmd := exec.Command(node, filepath.Join("testdata", "xterm_oracle.js"), xterm)
	cmd.Stdin = bytes.NewReader(in)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("oracle failed: %v\n%s", err, stderr.String())
	}

	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	i := 0
	for sc.Scan() {
		if i >= len(cases) {
			break
		}
		var want oracleResult
		if err := json.Unmarshal(sc.Bytes(), &want); err != nil {
			t.Fatalf("case %d: bad oracle output: %v", i, err)
		}
		c := cases[i]
		i++
		t.Run(c.name, func(t *testing.T) {
			term := New(cols, rows)
			term.SetMaxScrollback(5000)
			desc := c.input
			if len(c.steps) == 0 {
				feed(t, term, c.input)
			} else {
				desc = ""
				for _, st := range c.steps {
					if st.resize[0] > 0 {
						term.Resize(st.resize[0], st.resize[1])
						desc += fmt.Sprintf("<resize %dx%d>", st.resize[0], st.resize[1])
					} else {
						feed(t, term, st.write)
						desc += st.write
					}
				}
			}
			compareWithOracle(t, term, &want, desc, c.fuzz)
		})
	}
	if i != len(cases) {
		t.Fatalf("oracle returned %d results for %d cases\n%s", i, len(cases), stderr.String())
	}
}

func skipOrFail(t *testing.T, why string) {
	if os.Getenv("VTERM_ORACLE") != "" {
		t.Fatalf("oracle unavailable: %s", why)
	}
	t.Skip("oracle unavailable: " + why)
}

type oracleCell struct {
	X, Y   int
	Ch     string
	W      int
	B, D   int
	I, U   int
	V, S   int
	Fg, Bg string
}

type oracleResult struct {
	Lines  []string
	Base   int
	Cursor struct{ X, Y int }
	Alt    bool
	Cells  []oracleCell
	Modes  struct {
		AppCursor bool
		Paste     bool
		Mouse     string
		Wrap      bool
		Origin    bool
		Insert    bool
	}
}

func compareWithOracle(t *testing.T, term *Terminal, want *oracleResult, input string, structureOnly bool) {
	t.Helper()
	w, h := term.Size()

	// Lines: scrollback then screen. xterm.js may hold an alt buffer with
	// no scrollback; the emulator's scrollback is the main screen's.
	sb := 0
	if !want.Alt {
		sb = term.ScrollbackLen()
	}
	if len(want.Lines) != sb+h {
		t.Errorf("line count: xterm %d (base %d), vterm scrollback %d + %d rows\ninput %q",
			len(want.Lines), want.Base, sb, h, input)
	}
	for y, line := range want.Lines {
		var got string
		switch {
		case y < sb:
			got = term.ScrollbackLine(y)
		case y-sb < h:
			got = term.Line(y - sb)
		default:
			continue
		}
		// xterm.js keeps printed spaces at the end of a line; the emulator
		// stores them as blanks. Neither is visible.
		if strings.TrimRight(line, " ") != got {
			t.Errorf("line %d: xterm %q vterm %q\ninput %q", y, line, got, input)
		}
	}
	// xterm.js represents the pending-wrap state as x == cols.
	x, y := term.Cursor()
	if term.s.cur.pendingWrap {
		x = w
	}
	if x != want.Cursor.X || y != want.Cursor.Y {
		t.Errorf("cursor: xterm %d,%d vterm %d,%d (pending wrap %v)\ninput %q", want.Cursor.X, want.Cursor.Y, x, y, term.s.cur.pendingWrap, input)
	}
	if term.AltScreen() != want.Alt {
		t.Errorf("alt screen: xterm %v vterm %v", want.Alt, term.AltScreen())
	}
	if structureOnly {
		return
	}
	for _, c := range want.Cells {
		var cell Cell
		switch {
		case c.Y < sb:
			r := &term.main.scrollback[c.Y]
			if c.X < len(r.cells) {
				cell = r.cells[c.X]
			} else {
				cell = Cell{Width: 1}
			}
		case c.Y-sb < h && c.X < w:
			cell = term.CellAt(c.X, c.Y-sb)
		default:
			continue
		}
		got := oracleCell{X: c.X, Y: c.Y, Ch: cell.Ch, W: int(cell.Width),
			B: flag(cell.Style.Attrs&AttrBold != 0), D: flag(cell.Style.Attrs&AttrDim != 0),
			I: flag(cell.Style.Attrs&AttrItalic != 0), U: flag(cell.Style.Underline != UnderlineNone),
			V: flag(cell.Style.Attrs&AttrInverse != 0), S: flag(cell.Style.Attrs&AttrStrike != 0),
			Fg: colorKey(cell.Style.Fg), Bg: colorKey(cell.Style.Bg)}
		if got.Ch == "" {
			got.Ch = " "
		}
		if cell.Link != "" {
			got.U = c.U // xterm.js reports hyperlink cells as underlined
		}
		if c.Ch == "" {
			c.Ch = " "
		}
		if got != c {
			t.Errorf("cell %d,%d: xterm %+v vterm %+v\ninput %q", c.X, c.Y, c, got, input)
		}
	}
	mouse := "none"
	switch term.mouse {
	case 9:
		mouse = "x10"
	case 1000:
		mouse = "vt200"
	case 1002:
		mouse = "drag"
	case 1003:
		mouse = "any"
	}
	if want.Modes.AppCursor != term.cursorKeys || want.Modes.Paste != term.bracketedPaste ||
		want.Modes.Mouse != mouse || want.Modes.Wrap != term.autowrap ||
		want.Modes.Origin != term.origin || want.Modes.Insert != term.insert {
		t.Errorf("modes: xterm %+v vterm ckm=%v paste=%v mouse=%s wrap=%v origin=%v insert=%v",
			want.Modes, term.cursorKeys, term.bracketedPaste, mouse, term.autowrap, term.origin, term.insert)
	}
}

func flag(b bool) int {
	if b {
		return 1
	}
	return 0
}

func colorKey(c Color) string {
	switch c & colorKindMask {
	case colorKindIndexed:
		return fmt.Sprintf("p%d", c&0xff)
	case colorKindRGB:
		return fmt.Sprintf("r%d", c&0xffffff)
	}
	return ""
}

type oracleCase struct {
	name  string
	input string
	steps []oracleStep
	// fuzz marks a randomly generated stream: it pins the structure
	// (lines, cursor, alt screen, modes) but not every cell attribute.
	// The curated cases below pin attributes exactly; fuzz streams exist
	// to stress the state machine's structure, and chasing an exact
	// attribute match on a pathological synthetic interleaving (a printed
	// space vs an erased one at a scroll-region boundary) has no bearing
	// on real terminal output.
	fuzz bool
}

// oracleStep is one action of a multi-step case: bytes to write, or a
// resize (cols, rows) when resize[0] > 0.
type oracleStep struct {
	write  string
	resize [2]int
}

func write(s string) oracleStep           { return oracleStep{write: s} }
func resize(w, h int) oracleStep          { return oracleStep{resize: [2]int{w, h}} }
func steps(st ...oracleStep) []oracleStep { return st }

// oracleCorpus is the differential corpus: hand-written sequences for each
// operation family plus deterministic pseudo-random streams that mix them.
func oracleCorpus() []oracleCase {
	cases := []oracleCase{
		{name: "plain", input: "hello world\r\nsecond line"},
		{name: "wrap", input: "this line is longer than twenty-four columns and wraps twice over the edge"},
		{name: "exact fill then newline", input: strings.Repeat("x", 24) + "\r\nnext"},
		{name: "scroll into scrollback", input: "1\r\n2\r\n3\r\n4\r\n5\r\n6\r\n7\r\n8\r\n9"},
		{name: "cursor moves", input: "abc\x1b[2;5Hdef\x1b[A\x1b[3Dg\x1b[B\x1b[Ch\x1b[4;1Hi\x1b[2Fj\x1b[Ek"},
		{name: "erase line variants", input: "aaaaaaaaaa\x1b[5G\x1b[K\r\nbbbbbbbbbb\x1b[5G\x1b[1K\r\ncccccccccc\x1b[2K"},
		{name: "erase display 0/1", input: "1111\r\n2222\r\n3333\r\n4444\x1b[2;3H\x1b[J\x1b[1;1H\x1b[?25h"},
		{name: "erase display 1", input: "1111\r\n2222\r\n3333\x1b[2;3H\x1b[1J"},
		{name: "clear screen", input: "old\r\nstuff\x1b[H\x1b[2Jnew"},
		{name: "insert delete lines", input: "a\r\nb\r\nc\r\nd\r\ne\x1b[2;1H\x1b[2L\x1b[4;1H\x1b[M"},
		{name: "insert delete chars", input: "abcdefgh\x1b[3G\x1b[2@XY\x1b[1G\x1b[3P\x1b[2X"},
		{name: "scroll region", input: "1\r\n2\r\n3\r\n4\r\n5\r\n6\x1b[2;4r\x1b[4;1H\r\nA\r\nB\x1b[r\x1b[6;1HZ"},
		{name: "scroll region SU SD", input: "1\r\n2\r\n3\r\n4\r\n5\r\n6\x1b[2;5r\x1b[2S\x1b[T\x1b[r"},
		{name: "reverse index", input: "top\r\nmid\x1b[H\x1bM\x1bMri"},
		{name: "sgr basics", input: "\x1b[1mB\x1b[2mD\x1b[3mI\x1b[4mU\x1b[7mV\x1b[9mS\x1b[0mN\x1b[22;24;27;29m"},
		{name: "sgr colors", input: "\x1b[31mr\x1b[92mg\x1b[38;5;123mx\x1b[48;5;200my\x1b[38;2;1;2;3mz\x1b[48;2;250;251;252mw\x1b[39;49md"},
		{name: "sgr colon forms", input: "\x1b[38:5:99ma\x1b[38:2::10:20:30mb\x1b[4:3mc\x1b[0md"},
		{name: "erase keeps bg", input: "\x1b[44mblue\x1b[K\x1b[0m\r\n\x1b[42m\x1b[2K\x1b[0mx"},
		{name: "wide chars", input: "漢字テスト abc 中文"},
		{name: "wide wrap at edge", input: strings.Repeat("a", 23) + "漢b"},
		{name: "wide overwrite half", input: "漢字\x1b[2Gx\x1b[1;1H\x1b[4Gy"},
		{name: "combining", input: "é ä x\r\nb́"},
		{name: "emoji", input: "✅ ⚠ ✻ ⏺ 🚀 ok"},
		{name: "tabs", input: "a\tb\tc\x1b[Zd\r\n\x1b[3Ie"},
		{name: "alt screen", input: "main\r\nlines\x1b[?1049h\x1b[H\x1b[2Jalt screen\x1b[?1000h\x1b[?2004h\x1b[?1h"},
		{name: "alt screen return", input: "main\r\nlines\x1b[?1049h\x1b[H\x1b[2Jalt\x1b[?1049lback"},
		{name: "alt 47", input: "m\x1b[?47hx\x1b[?47l\x1b[?1047hy\x1b[?1047lz"},
		{name: "save restore cursor", input: "abc\x1b7\r\n\r\nxyz\x1b8Q\x1b[s\x1b[4;4H\x1b[uR"},
		{name: "origin mode", input: "\x1b[2;4r\x1b[?6h\x1b[Hx\x1b[3;1Hy\x1b[?6l\x1b[Hz"},
		{name: "autowrap off", input: "\x1b[?7l" + strings.Repeat("q", 30) + "\x1b[?7h"},
		{name: "insert mode", input: "abcdef\x1b[1G\x1b[4hXY\x1b[4l"},
		{name: "lnm", input: "\x1b[20ha\nb\x1b[20lc\nd"},
		{name: "dec graphics", input: "\x1b(0lqqk\r\nx  x\r\nmqqj\x1b(B done"},
		{name: "so si", input: "\x1b)0\x0elqk\x0fabc"},
		{name: "rep", input: "ab\x1b[3bc"},
		{name: "bs at wrap", input: strings.Repeat("z", 24) + "\x08y"},
		{name: "cr lf ff vt", input: "a\x0bb\x0cc\rd\n"},
		{name: "osc title link", input: "\x1b]0;t\x07\x1b]8;;http://x\x1b\\L\x1b]8;;\x1b\\p"},
		{name: "ignored queries", input: "a\x1b[c\x1b[6n\x1b[>c\x1b[?1;2$p\x1b[18t\x1b[>0qb\x1b[?u\x1b[>1uc\x1b[<ud"},
		{name: "dcs apc ignored", input: "a\x1bPqpayload\x1b\\b\x1b_apc\x1b\\c\x1b^pm\x1b\\d\x1bXsos\x1b\\e"},
		{name: "decstr", input: "\x1b[1;31m\x1b[2;4r\x1b[?6h\x1b[!pafter"},
		{name: "ris keeps nothing on screen", input: "old\r\nold\x1bcnew"},
		{name: "decaln", input: "\x1b#8\x1b[3;3Hx"},
		{name: "cursor style and vis", input: "\x1b[3 q\x1b[?25lhidden"},
		{name: "ink style redraw", input: "$ app\r\n\x1b[2K\x1b[Gline A #\r\n\x1b[2K\x1b[Gline B #\r\n\x1b[2A\x1b[2K\x1b[Gline A ##\r\n\x1b[2K\x1b[Gline B ##\r\n\x1b[2A\x1b[2K\x1b[Gline A ###\r\n\x1b[2K\x1b[Gline B ###\r\n"},
		{name: "vim like", input: "\x1b[?1049h\x1b[?1h\x1b=\x1b[?2004h\x1b[1;6r\x1b[?12h\x1b[?12l\x1b[27m\x1b[23m\x1b[29m\x1b[m\x1b[H\x1b[2J\x1b[2;1H~\r\n~\r\n~\r\n~\x1b[6;1H\x1b[1m-- INSERT --\x1b[m\x1b[1;1H"},
		{name: "htop like", input: "\x1b[?1049h\x1b[H\x1b[2J\x1b[0;36m  1 \x1b[0;37m[\x1b[0;32m||||\x1b[0;37m    50.0%]\r\n\x1b[0;30;46m  PID USER  CPU%\x1b[0m\r\n\x1b[0;37m 1234 root   1.0\x1b[K\r\n\x1b[7m  F1\x1b[0mHelp"},
		{name: "csi split by ctrl", input: "ab\x1b[2\rC\x1b[1;3Hz"},
		{name: "long csi params", input: "\x1b[" + strings.Repeat("1;", 40) + "Hx"},
		{name: "bad utf8", input: "a\xffb\xc3\x28c"},
	}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 24; i++ {
		cases = append(cases, oracleCase{name: fmt.Sprintf("random-%d", i), input: randomStream(rng, 40), fuzz: true})
	}
	return cases
}

// randomStream builds a deterministic mix of text and control sequences.
func randomStream(rng *rand.Rand, n int) string {
	words := []string{"lorem", "ipsum", "漢字", "ünïcödé", "x", "tab\t", "✻ ", "wide字", "á"}
	var b strings.Builder
	for i := 0; i < n; i++ {
		switch rng.Intn(20) {
		case 0, 1, 2, 3, 4, 5, 6:
			b.WriteString(words[rng.Intn(len(words))])
			b.WriteByte(' ')
		case 7:
			b.WriteString("\r\n")
		case 8:
			fmt.Fprintf(&b, "\x1b[%d;%dH", rng.Intn(8), rng.Intn(28))
		case 9:
			fmt.Fprintf(&b, "\x1b[%d%c", rng.Intn(4), "ABCD"[rng.Intn(4)])
		case 10:
			fmt.Fprintf(&b, "\x1b[%dJ", rng.Intn(3))
		case 11:
			fmt.Fprintf(&b, "\x1b[%dK", rng.Intn(3))
		case 12:
			fmt.Fprintf(&b, "\x1b[%d%c", rng.Intn(3), "LMP@X"[rng.Intn(5)])
		case 13:
			fmt.Fprintf(&b, "\x1b[%dm", []int{0, 1, 4, 7, 31, 32, 44, 92}[rng.Intn(8)])
		case 14:
			fmt.Fprintf(&b, "\x1b[38;5;%dm", rng.Intn(256))
		case 15:
			fmt.Fprintf(&b, "\x1b[%d;%dr", 1+rng.Intn(3), 4+rng.Intn(3))
		case 16:
			b.WriteString("\x1b[r")
		case 17:
			b.WriteString([]string{"\x1b7", "\x1b8", "\x1bM", "\x1bD", "\x1bE"}[rng.Intn(5)])
		case 18:
			b.WriteString(strings.Repeat("longword-", 4))
		case 19:
			fmt.Fprintf(&b, "\x1b[%dG", 1+rng.Intn(26))
		}
	}
	return b.String()
}
