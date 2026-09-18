// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
// Record a real browser session against the live console via CDP screencast.
//
// Frames come from Chrome itself while the page animates in real time, so the
// timeline is the page's, not the screenshotter's. Nothing is composited: each
// frame is what the console painted.
//
// usage: node scripts/capture-console-rec.mjs <scene.json>
import { createRequire } from 'module';
import path from 'path';
import { fileURLToPath } from 'url';
const require_ = createRequire(import.meta.url);
// Resolved from the repo's own web/ install so this tool needs no node_modules
// of its own, and always uses the Playwright the repository pins.
// Resolved from THIS file's location, never from a hardcoded worktree: every contributor
// checks the repo out somewhere else, and a pinned absolute path only works in the
// tree that wrote it.
const PW_ROOT =
  process.env.OLIVARES_WEB_DIR ||
  path.join(path.dirname(path.dirname(fileURLToPath(import.meta.url))), 'web');
const { chromium } = require_(require_.resolve('@playwright/test', { paths: [PW_ROOT] }));
import fs from 'fs';

const scene = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
const outDir = scene.frames_dir;
fs.rmSync(outDir, { recursive: true, force: true });
fs.mkdirSync(outDir, { recursive: true });

const browser = await chromium.launch({
  executablePath: '/opt/google/chrome/chrome',
  args: ['--no-sandbox', '--force-device-scale-factor=1', '--hide-scrollbars'],
});
const ctx = await browser.newContext({
  viewport: scene.viewport ?? { width: 1600, height: 900 },
  deviceScaleFactor: 1,
  colorScheme: scene.color_scheme ?? 'light',
  reducedMotion: 'no-preference',
});
const page = await ctx.newPage();

// ---- log in through the real login form -------------------------------------
await page.goto(`${scene.base}/login`, { waitUntil: 'networkidle', timeout: 60000 });
await page.fill('input[type=email], input[name=email]', scene.email);
await page.fill('input[type=password], input[name=password]', scene.password);
await Promise.all([page.waitForLoadState('networkidle'), page.click('button[type=submit]')]);
await page.waitForTimeout(3000);
if (page.url().includes('/login')) throw new Error('login did not take');

await page.goto(`${scene.base}${scene.route}`, { waitUntil: 'networkidle', timeout: 60000 });
await page.waitForTimeout(scene.settle_ms ?? 4000);

// ---- optional pre-roll steps, done BEFORE recording starts ------------------
for (const step of scene.prepare ?? []) await runStep(page, step);
await page.waitForTimeout(scene.pre_record_ms ?? 1200);

// ---- screencast --------------------------------------------------------------
const cdp = await ctx.newCDPSession(page);
const frames = [];
const t0 = Date.now();
cdp.on('Page.screencastFrame', async (ev) => {
  const t = Date.now() - t0;
  const i = frames.length;
  const file = path.join(outDir, `f${String(i).padStart(5, '0')}.png`);
  fs.writeFileSync(file, Buffer.from(ev.data, 'base64'));
  frames.push({ t, file });
  try { await cdp.send('Page.screencastFrameAck', { sessionId: ev.sessionId }); } catch { /* stream closed */ }
});
await cdp.send('Page.startScreencast', {
  format: 'png',
  everyNthFrame: 1,
  maxWidth: scene.viewport?.width ?? 1600,
  maxHeight: scene.viewport?.height ?? 900,
});

for (const step of scene.steps ?? []) await runStep(page, step);

await cdp.send('Page.stopScreencast');
await page.waitForTimeout(400);

fs.writeFileSync(
  path.join(outDir, 'frames.json'),
  JSON.stringify({ started: t0, frames: frames.map((f) => ({ t: f.t, file: path.basename(f.file) })) }, null, 1),
);
console.log(`console-rec: ${frames.length} frames · ${((frames.at(-1)?.t ?? 0) / 1000).toFixed(1)}s · ${outDir}`);
await browser.close();

async function runStep(page, step) {
  switch (step.op) {
    case 'wait':
      await page.waitForTimeout(step.ms);
      break;
    case 'click_button_text': {
      const b = page.locator('button', { hasText: step.text }).first();
      await b.click({ timeout: 20000 });
      break;
    }
    case 'click_aria': {
      await page.locator(`[aria-label="${step.label}"]`).first().click({ timeout: 20000 });
      break;
    }
    case 'hover_text': {
      await page.getByText(step.text, { exact: false }).first().hover({ timeout: 20000 });
      break;
    }
    case 'assert_text': {
      const body = await page.locator('body').innerText();
      if (!body.includes(step.text)) throw new Error(`assert_text failed: ${step.text}`);
      break;
    }
    case 'screenshot':
      await page.screenshot({ path: step.path });
      break;
    default:
      throw new Error(`unknown op ${step.op}`);
  }
}
