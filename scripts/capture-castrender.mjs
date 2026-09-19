// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// capture-castrender.mjs — render an asciicast v2 recording to a PNG of its final
// frame, using the browser this repository already installs.
//
// WHY THIS EXISTS. The terminal captures of the first-hour walk are real PTY
// recordings, and the tool that turns them into images (pyte + Pillow) is not
// installed here, has no pip to install it with, and would mean pulling an
// unvetted compiled wheel into the workspace to make a picture. Chromium IS
// installed, with a Playwright binding the web package already depends on. A
// terminal frame is a grid of coloured monospace cells, and that is a thing a
// browser renders exactly.
//
// WHAT IT DOES NOT DO, said plainly so nobody reads more into the output than is
// there: it is not a terminal emulator. It replays the output stream keeping a
// cursor, and it understands the sequences these recordings actually contain —
// SGR colour, carriage return, backspace, erase-to-end-of-line and the
// cursor-column move. Anything else is dropped rather than drawn, and a cast that
// needs more than that would come out wrong QUIETLY, so --strict refuses instead
// when it meets a sequence it does not implement.
//
// Colour comes from web/tokens/theme.dark.tokens.json, so the images and the
// console are the same dark canvas and the same four semantic colours rather than
// two designs that look similar.
//
// Usage:
//   node scripts/capture-castrender.mjs --out <dir> <file.cast> [more.cast ...]
//   node scripts/capture-castrender.mjs --out <dir> --pair before.cast after.cast --name 01-detect
//
// The --pair form writes one composite with the two frames side by side under one
// caption, which is the form a before/after is read in.

import { readFileSync, writeFileSync, mkdirSync, existsSync } from "node:fs";
import { basename, join, resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const HERE = dirname(fileURLToPath(import.meta.url));
const ROOT = resolve(HERE, "..");

// playwright is a workspace dependency of web/, and pnpm's store layout means the
// package is not at web/node_modules/playwright. Resolve it from web/'s own
// resolution root rather than guessing a path into .pnpm.
function loadPlaywright() {
  const require = createRequire(join(ROOT, "web", "package.json"));
  for (const id of ["playwright", "playwright-core", "@playwright/test"]) {
    try {
      return require(id);
    } catch {
      /* try the next one */
    }
  }
  const store = join(ROOT, "web", "node_modules", ".pnpm");
  throw new Error(
    `playwright is not resolvable from web/. Run: pnpm --dir web install --frozen-lockfile --prefer-offline\n` +
      `(store: ${store})`,
  );
}

// --- the palette, from the tokens the console uses --------------------------------
const tokens = JSON.parse(readFileSync(join(ROOT, "web/tokens/theme.dark.tokens.json"), "utf8"));
const tok = (name) => {
  const v = tokens.color?.[name]?.$value;
  if (typeof v !== "string" || !v.startsWith("#")) {
    throw new Error(`theme.dark.tokens.json has no usable color.${name}`);
  }
  return v;
};
const PALETTE = {
  background: tok("background"),
  foreground: tok("foreground"),
  ok: tok("success"),
  warn: tok("warning"),
  fail: tok("danger"),
  muted: tok("muted-foreground"),
  info: tok("info"),
};

// hexToRGB and nearest let a 256-colour index land on one of the palette's own
// colours instead of introducing a seventh. The recordings' shell prompt uses
// 38;5;75 and 38;5;245, which no product code emits — the renderer's own output
// is four SGR codes and nothing else.
const hexToRGB = (h) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));
const CUBE = [0, 95, 135, 175, 215, 255];
function xterm256(n) {
  if (n < 16) {
    const base = [
      [0, 0, 0], [205, 0, 0], [0, 205, 0], [205, 205, 0],
      [0, 0, 238], [205, 0, 205], [0, 205, 205], [229, 229, 229],
      [127, 127, 127], [255, 0, 0], [0, 255, 0], [255, 255, 0],
      [92, 92, 255], [255, 0, 255], [0, 255, 255], [255, 255, 255],
    ];
    return base[n];
  }
  if (n < 232) {
    const i = n - 16;
    return [CUBE[Math.floor(i / 36) % 6], CUBE[Math.floor(i / 6) % 6], CUBE[i % 6]];
  }
  const g = 8 + (n - 232) * 10;
  return [g, g, g];
}
function nearestToken(rgb) {
  let best = "foreground";
  let bestD = Infinity;
  for (const [name, hex] of Object.entries(PALETTE)) {
    if (name === "background") continue;
    const [r, g, b] = hexToRGB(hex);
    const d = (r - rgb[0]) ** 2 + (g - rgb[1]) ** 2 + (b - rgb[2]) ** 2;
    if (d < bestD) {
      bestD = d;
      best = name;
    }
  }
  return best;
}

// --- the replay ------------------------------------------------------------------
const SGR_TO_TOKEN = { 30: "muted", 31: "fail", 32: "ok", 33: "warn", 34: "info", 37: "foreground" };

// replay walks the output stream and returns rows of {text, token} runs.
function replay(stream, cols, strict) {
  const rows = [[]]; // each row is an array of cells: {ch, token, dim}
  let row = 0;
  let col = 0;
  let token = "foreground";
  let dim = false;
  const unimplemented = new Set();

  const at = (r, c) => {
    while (rows.length <= r) rows.push([]);
    const line = rows[r];
    while (line.length <= c) line.push({ ch: " ", token: "foreground", dim: false });
    return line;
  };

  for (let i = 0; i < stream.length; i++) {
    const ch = stream[i];
    if (ch === "\x1b") {
      // CSI: ESC [ params letter
      if (stream[i + 1] !== "[") {
        // ESC ] … BEL is an OSC (window title). Skip to the terminator.
        if (stream[i + 1] === "]") {
          const end = stream.indexOf("\x07", i);
          i = end < 0 ? stream.length : end;
          continue;
        }
        unimplemented.add(`ESC ${stream[i + 1]}`);
        i += 1;
        continue;
      }
      let j = i + 2;
      while (j < stream.length && !/[A-Za-z]/.test(stream[j])) j++;
      const params = stream.slice(i + 2, j);
      const letter = stream[j];
      i = j;
      if (letter === "m") {
        for (const p of params.split(";")) {
          const n = Number(p === "" ? "0" : p);
          if (n === 0) {
            token = "foreground";
            dim = false;
          } else if (n === 2) {
            dim = true;
            token = "muted";
          } else if (n === 22) {
            dim = false;
          } else if (n === 39) {
            token = "foreground";
          } else if (SGR_TO_TOKEN[n]) {
            token = SGR_TO_TOKEN[n];
          }
        }
        // 38;5;N — the 256-colour form the recorded shell prompt uses.
        const m = params.match(/(?:^|;)38;5;(\d+)/);
        if (m) token = nearestToken(xterm256(Number(m[1])));
      } else if (letter === "K") {
        const line = at(row, col);
        line.length = col; // erase to end of line
      } else if (letter === "G") {
        col = Math.max(0, Number(params || "1") - 1);
      } else if (letter === "C") {
        col += Number(params || "1");
      } else if (letter === "D") {
        col = Math.max(0, col - Number(params || "1"));
      } else if (letter === "h" || letter === "l") {
        // mode set/reset: bracketed paste, cursor visibility. Nothing to draw.
      } else if (letter === "J") {
        // erase display. The recordings use it only to clear before a redraw.
        rows.length = 0;
        rows.push([]);
        row = 0;
        col = 0;
      } else {
        unimplemented.add(`CSI ${params}${letter}`);
      }
      continue;
    }
    if (ch === "\r") {
      col = 0;
      continue;
    }
    if (ch === "\n") {
      row += 1;
      col = 0;
      at(row, 0);
      continue;
    }
    if (ch === "\b") {
      col = Math.max(0, col - 1);
      continue;
    }
    if (ch === "\x07") continue; // bell
    const line = at(row, col);
    line[col] = { ch, token, dim };
    col += 1;
    if (cols > 0 && col >= cols) {
      row += 1;
      col = 0;
      at(row, 0);
    }
  }
  if (strict && unimplemented.size > 0) {
    throw new Error(`--strict: sequences this replay does not implement: ${[...unimplemented].join(", ")}`);
  }
  return { rows, unimplemented: [...unimplemented] };
}

// runs turns a row of cells into the fewest coloured spans that describe it.
function runs(line) {
  const out = [];
  for (const cell of line) {
    const last = out[out.length - 1];
    if (last && last.token === cell.token) last.text += cell.ch;
    else out.push({ token: cell.token, text: cell.ch });
  }
  // A trailing run of spaces draws nothing; dropping it keeps the PNG's right
  // edge honest about where the text ends.
  while (out.length && out[out.length - 1].text.trim() === "") out.pop();
  return out;
}

// parseSource reads either an asciicast recording or a plain capture of what a
// pipe received.
//
// The difference is not cosmetic and the output says which it is. A .cast is a
// RECORDING with a screen: its final frame is the last `height` rows, because
// that is what was on the terminal when the recording stopped. A .txt is a
// TRANSCRIPT with no screen at all — it is what the command wrote to a pipe — so
// there is no frame to take the last rows of, and cutting it would be inventing a
// terminal that was never there.
function parseSource(path) {
  if (!path.endsWith(".cast")) {
    return { cols: 100, rowsMax: 0, stream: readFileSync(path, "utf8"), kind: "transcript" };
  }
  const lines = readFileSync(path, "utf8").split("\n").filter((l) => l.trim() !== "");
  const header = JSON.parse(lines[0]);
  if (header.version !== 2) throw new Error(`${path}: asciicast version ${header.version}, expected 2`);
  let stream = "";
  for (const line of lines.slice(1)) {
    const ev = JSON.parse(line);
    if (ev[1] === "o") stream += ev[2];
  }
  return { cols: header.width || 100, rowsMax: header.height || 30, stream, kind: "recording" };
}

const ESC_HTML = (s) =>
  s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

function frameHTML(frames, caption) {
  const panes = frames
    .map(
      (f) => `
    <figure class="pane">
      <figcaption>${ESC_HTML(f.title)}</figcaption>
      <pre class="term">${f.rows
        .map((line) => runs(line).map((r) => `<span class="t-${r.token}">${ESC_HTML(r.text)}</span>`).join("") || "&nbsp;")
        .join("\n")}</pre>
    </figure>`,
    )
    .join("\n");
  return `<!doctype html><meta charset="utf-8"><title>${ESC_HTML(caption)}</title>
<style>
  :root {
    --bg: ${PALETTE.background}; --fg: ${PALETTE.foreground};
    --ok: ${PALETTE.ok}; --warn: ${PALETTE.warn}; --fail: ${PALETTE.fail};
    --muted: ${PALETTE.muted}; --info: ${PALETTE.info};
  }
  * { box-sizing: border-box; }
  body { margin: 0; background: var(--bg); color: var(--fg);
         font: 14px/1.45 ui-monospace, "DejaVu Sans Mono", "Liberation Mono", monospace; }
  /* Each pane sizes to its own content. A fixed-width pane with overflow:hidden
     would CUT a long line, and cutting a value is the one thing the renderer this
     evidence is about refuses to do — an image that did it would contradict its
     own subject. Wide transcripts make wide PNGs, which is the honest trade. */
  .sheet { display: inline-flex; gap: 24px; padding: 24px; align-items: flex-start; }
  .pane { margin: 0; flex: 0 0 auto; }
  figcaption { color: var(--muted); font-size: 12px; letter-spacing: .08em;
               text-transform: uppercase; margin: 0 0 8px 2px; }
  pre.term { margin: 0; padding: 14px 16px; white-space: pre; overflow: visible;
             background: var(--bg); border: 1px solid #3a3a40; border-radius: 6px; }
  .t-foreground { color: var(--fg); }
  .t-ok { color: var(--ok); } .t-warn { color: var(--warn); }
  .t-fail { color: var(--fail); } .t-muted { color: var(--muted); }
  .t-info { color: var(--info); }
</style>
<div class="sheet">${panes}</div>`;
}

// --- cli ---------------------------------------------------------------------------
const argv = process.argv.slice(2);
let out = null;
let pair = null;
let name = null;
let strict = false;
const casts = [];
for (let i = 0; i < argv.length; i++) {
  const a = argv[i];
  if (a === "--out") out = argv[++i];
  else if (a === "--name") name = argv[++i];
  else if (a === "--strict") strict = true;
  else if (a === "--pair") {
    pair = [argv[++i], argv[++i]];
  } else casts.push(a);
}
if (!out) {
  console.error("capture-castrender: --out <dir> is required");
  process.exit(2);
}
if (!pair && casts.length === 0) {
  console.error("capture-castrender: name at least one .cast, or use --pair before.cast after.cast");
  process.exit(2);
}
mkdirSync(out, { recursive: true });

const { chromium } = loadPlaywright();
const browser = await chromium.launch();
// A viewport wide enough that inline-flex does not wrap the two panes onto two
// rows before the element screenshot is taken. The screenshot is of .sheet, so the
// viewport only has to be big enough not to reflow it.
const page = await browser.newPage({ deviceScaleFactor: 2, viewport: { width: 4000, height: 2400 } });

async function shoot(frames, file, caption) {
  await page.setContent(frameHTML(frames, caption), { waitUntil: "load" });
  const sheet = await page.locator(".sheet");
  const png = join(out, file);
  await sheet.screenshot({ path: png });
  console.log(`${png}  (${frames.map((f) => `${f.rows.length} rows`).join(", ")})`);
}

// A cast's FINAL FRAME is the last `height` rows: that is what is on the screen
// when the recording stops. Shorter transcripts are the whole thing.
function framesOf(path, title) {
  const src = parseSource(path);
  const { rows, unimplemented } = replay(src.stream, src.cols, strict);
  if (unimplemented.length) {
    console.error(`  note: ${basename(path)} used ${unimplemented.length} sequence(s) this replay drops: ${unimplemented.join(", ")}`);
  }
  while (rows.length && runs(rows[rows.length - 1]).length === 0) rows.pop();
  const frame = src.rowsMax > 0 && rows.length > src.rowsMax ? rows.slice(rows.length - src.rowsMax) : rows;
  return { title, rows: frame };
}

let failed = 0;
if (pair) {
  for (const p of pair) {
    if (!existsSync(p)) {
      console.error(`capture-castrender: no such capture: ${p}`);
      failed++;
    }
  }
  if (!failed) {
    const stem = name || basename(pair[1]).replace(/\.(cast|txt)$/, "");
    await shoot(
      [framesOf(pair[0], "before"), framesOf(pair[1], "after")],
      `${stem}.png`,
      stem,
    );
  }
} else {
  for (const c of casts) {
    if (!existsSync(c)) {
      console.error(`capture-castrender: no such capture: ${c}`);
      failed++;
      continue;
    }
    const stem = basename(c).replace(/\.(cast|txt)$/, "");
    await shoot([framesOf(c, stem)], `${stem}.png`, stem);
  }
}

await browser.close();
process.exit(failed ? 1 : 0);
