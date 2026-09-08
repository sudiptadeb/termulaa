// xterm.js oracle for the vterm differential test.
//
// Reads a JSON job from stdin: {"cols": N, "rows": M, "cases": [<case>, ...]}
// where a case is either a base64 string (bytes to write) or a list of steps
// [{"write": <base64>} | {"resize": [cols, rows]}].
// For each case it feeds the bytes into a fresh xterm.js Terminal (the exact
// bundle termulaa ships, loaded from ui/lib) inside headless Chromium and
// prints one JSON line per case with the screen as the test compares it:
// the text of every row (trailing blanks trimmed), the cursor, and per-cell
// attributes for non-blank cells.
//
// Usage: node xterm_oracle.js <path to xterm.min.js> < job.json
// The unicode11 addon next to the bundle is loaded too, as termulaa does.
// Requires playwright-core on NODE_PATH and Chromium at
// $PLAYWRIGHT_CHROMIUM (or the default Playwright browsers directory).

'use strict';
const fs = require('fs');
const path = require('path');
const { chromium } = require('playwright-core');

async function main() {
  const xtermPath = path.resolve(process.argv[2]);
  const job = JSON.parse(fs.readFileSync(0, 'utf8'));
  const exe = process.env.PLAYWRIGHT_CHROMIUM || findChromium();
  const browser = await chromium.launch({ executablePath: exe, headless: true });
  const page = await browser.newPage();
  page.on('pageerror', (e) => { process.stderr.write('pageerror: ' + e + '\n'); });
  await page.setContent('<div id="t" style="width:2000px;height:2000px"></div>');
  await page.addScriptTag({ path: xtermPath });
  const unicode11 = path.join(path.dirname(xtermPath), 'xterm-addon-unicode11.min.js');
  if (fs.existsSync(unicode11)) await page.addScriptTag({ path: unicode11 });
  const results = await page.evaluate(async (job) => {
    const out = [];
    const decode = (b64) => {
      const bin = atob(b64);
      const bytes = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
      return bytes;
    };
    for (const c of job.cases) {
      const steps = typeof c === 'string' ? [{ write: c }] : c;
      const el = document.createElement('div');
      document.getElementById('t').appendChild(el);
      const term = new Terminal({ cols: job.cols, rows: job.rows, allowProposedApi: true, scrollback: 5000 });
      term.open(el);
      if (typeof Unicode11Addon !== 'undefined') {
        term.loadAddon(new Unicode11Addon.Unicode11Addon());
        term.unicode.activeVersion = '11';
      }
      for (const st of steps) {
        if (st.write !== undefined) await new Promise((res) => term.write(decode(st.write), res));
        if (st.resize) term.resize(st.resize[0], st.resize[1]);
      }
      const buf = term.buffer.active;
      const lines = [];
      const cells = [];
      for (let y = 0; y < buf.length; y++) {
        const line = buf.getLine(y);
        if (!line) { lines.push(''); continue; }
        lines.push(line.translateToString(true));
        for (let x = 0; x < term.cols; x++) {
          const c = line.getCell(x);
          if (!c || c.getWidth() === 0) continue;
          const ch = c.getChars();
          const attrs = {
            b: c.isBold() ? 1 : 0, d: c.isDim() ? 1 : 0, i: c.isItalic() ? 1 : 0,
            u: c.isUnderline() ? 1 : 0, v: c.isInverse() ? 1 : 0, s: c.isStrikethrough() ? 1 : 0,
            fg: c.isFgDefault() ? '' : (c.isFgPalette() ? 'p' + c.getFgColor() : 'r' + c.getFgColor()),
            bg: c.isBgDefault() ? '' : (c.isBgPalette() ? 'p' + c.getBgColor() : 'r' + c.getBgColor()),
          };
          const plain = (ch === '' || ch === ' ') && !attrs.b && !attrs.d && !attrs.i && !attrs.u && !attrs.v && !attrs.s && !attrs.fg && !attrs.bg;
          if (plain) continue;
          cells.push({ x, y, ch, w: c.getWidth(), ...attrs });
        }
      }
      out.push({
        lines,
        base: buf.baseY,
        cursor: { x: buf.cursorX, y: buf.cursorY },
        alt: term.buffer.active.type === 'alternate',
        cells,
        modes: {
          appCursor: term.modes.applicationCursorKeysMode,
          paste: term.modes.bracketedPasteMode,
          mouse: term.modes.mouseTrackingMode,
          wrap: term.modes.wraparoundMode,
          origin: term.modes.originMode,
          insert: term.modes.insertMode,
        },
      });
      term.dispose();
      el.remove();
    }
    return out;
  }, job);
  await browser.close();
  for (const r of results) process.stdout.write(JSON.stringify(r) + '\n');
}

function findChromium() {
  const roots = [process.env.PLAYWRIGHT_BROWSERS_PATH, path.join(require('os').homedir(), '.cache', 'ms-playwright')].filter(Boolean);
  for (const root of roots) {
    let entries = [];
    try { entries = fs.readdirSync(root); } catch (e) { continue; }
    for (const e of entries.filter((n) => /^chromium-\d+$/.test(n)).sort().reverse()) {
      for (const rel of ['chrome-linux64/chrome', 'chrome-linux/chrome', 'chrome-mac/Chromium.app/Contents/MacOS/Chromium']) {
        const p = path.join(root, e, rel);
        if (fs.existsSync(p)) return p;
      }
    }
  }
  throw new Error('Chromium not found; set PLAYWRIGHT_CHROMIUM');
}

main().catch((e) => { process.stderr.write(String(e && e.stack || e) + '\n'); process.exit(1); });
