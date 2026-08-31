// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// render-evidence.mjs — render the generated emails in a real browser and MEASURE
// them. Not a gate: it needs a browser, so it runs on demand and its output is
// evidence rather than a verdict.
//
// IT IS COMMITTED BECAUSE THE EVIDENCE CLAIMED IT. The first version of
// an internal design note (not shipped)*.txt recorded network counts, screenshot hashes and
// overflow measurements produced by a script that lived only in a scratch
// directory, so a reviewer running the two commands the file named got none of
// them and correctly marked the whole section UNVERIFIED. A measurement nobody
// else can repeat is an assertion with a number in it.
//
// What it measures, and why each is the thing that matters:
//   * network requests and image elements per render — the claim is not that the
//     message looks fine with images off, it is that it asks for nothing at all.
//   * light vs images-blocked screenshot hashes — if they are byte-identical then
//     nothing in the message depended on the network. A screenshot that merely
//     looks the same does not show that.
//   * horizontal overflow at phone width for EVERY template in EVERY locale — the
//     failure mode a space-less script has. It is glyph-independent, so it is
//     measurable even in a container with no CJK font.
//
// Usage:
//   pnpm --dir web install && pnpm --dir web exec playwright install chromium
//   node email/render-evidence.mjs [outDir]
//
// In a container whose fontconfig maps the generic sans-serif to a monospace
// family (ours does), export FONTCONFIG_FILE pointing at a config that restores
// the ordinary desktop alias first, or every proportional stack renders monospace.

import { createRequire } from 'node:module'
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const ROOT = join(HERE, '..')
const OUT = resolve(process.argv[2] ?? join(ROOT, '.render-evidence'))

// playwright is a devDependency of the console workspace, not of the root; resolve
// it from there rather than hard-coding a store path that changes on every bump.
const req = createRequire(join(ROOT, 'web', 'package.json'))
// @playwright/test is what the console workspace actually declares (playwright
// itself is only a transitive dependency, so requiring it by name fails under
// pnpm's layout); it re-exports the same browser launcher.
let chromium
try {
  ;({ chromium } = req('@playwright/test'))
} catch {
  console.error(
    'email/render-evidence: playwright is not installed.\n' +
      '  pnpm --dir web install && pnpm --dir web exec playwright install chromium',
  )
  process.exit(2)
}

const core = JSON.parse(
  readFileSync(join(ROOT, 'core/emailtemplate/templates.generated.json'), 'utf8'),
)
const WORKER_BUNDLE = join(
  ROOT,
  'commercial/license-worker/src/email/templates.generated.ts',
)
const hasWorker = existsSync(WORKER_BUNDLE)
const worker = hasWorker ? readFileSync(WORKER_BUNDLE, 'utf8') : ''

/**
 * The worker bundle's template ids, DERIVED from the bundle itself.
 *
 * ⛔ THIS WAS A HAND-WRITTEN LIST, and it went stale the first time the bundle grew: on
 * 2026-08-27 the licence mail went from two shapes to four and the sweep below went on measuring
 * three ids, reporting "no render scrolls horizontally" over a set that no longer was the set.
 * A list that expires silently is the shape of gate this repository has found broken most often.
 * The union type is emitted by email/build.mjs from the same TEMPLATES object the bodies come
 * from, so reading it here cannot disagree with what was rendered.
 */
function workerTemplateIds() {
  const m = /export type WorkerTemplateId = ([^;]+);/.exec(worker)
  if (!m) throw new Error('email/render-evidence: the worker bundle declares no WorkerTemplateId')
  const ids = Array.from(m[1].matchAll(/"([A-Za-z]+)"/g), (x) => x[1])
  if (ids.length === 0) throw new Error('email/render-evidence: WorkerTemplateId is empty')
  return ids
}

function workerTemplate(locale, id) {
  const re = new RegExp(
    `\\n  ${locale}: \\{[\\s\\S]*?\\n    ${id}: \\{[\\s\\S]*?\\n      html: ("(?:[^"\\\\]|\\\\.)*"),`,
  )
  const m = re.exec(worker)
  if (!m) throw new Error(`email/render-evidence: no html for ${locale}/${id}`)
  return JSON.parse(m[1])
}

// Values shaped like the real ones: a long signed blob, a long tokenised URL, an
// organisation name with a space. Short placeholders would hide the wrapping this
// exists to measure.
const VALUES = {
  VERIFY_URL: 'https://licenses.example.invalid/portal/auth/verify?token=eyJhbGciOiJIUzI1NiJ9.EXAMPLE',
  ACCEPT_URL: 'https://console.example.invalid/invites/accept?token=EXAMPLE',
  EXPIRES_AT: '2026-08-13 10:30 UTC',
  LICENSEE: 'Contoso Industrial GmbH',
  EXPIRY_STATUS: 'This licence is valid until 2027-08-06.',
  DOWNLOAD_URL: 'https://licenses.example.invalid/download?token=EXAMPLE-TOKEN-VALUE',
  PORTAL_URL: 'https://licenses.example.invalid/portal/',
  LICENSE_KEY:
    'olv1.eyJob2xkZXIiOiJjb250b3NvIiwidGllciI6ImVudGVycHJpc2UiLCJleHAiOjE4' +
    'OTM0NTYwMDB9.MEUCIQDexampleSignatureBytesThatWrapAcrossSeveralLinesInAn' +
    'EmailBodyAndMustNotForceHorizontalScrollingOnAPhone',
}
const esc = (s) =>
  s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
const fill = (h) => h.replace(/\{\{([A-Z_]+)\}\}/g, (w, k) => (k in VALUES ? esc(VALUES[k]) : w))

const LOCALES = core.locales
const SHOTS = [
  ...(hasWorker
    ? [
        { name: 'signin-en', html: () => workerTemplate('en', 'signin') },
        { name: 'license-en', html: () => workerTemplate('en', 'license') },
        { name: 'signin-ja', html: () => workerTemplate('ja', 'signin') },
        // The shape with no download link. It is the one a misconfigured deployment sends, so it
        // is the one nobody looks at — and since 2026-08-27 it also carries the portal button as
        // the PRIMARY action, which is a different render and not a subset of the one above.
        { name: 'licenseNoDownload-en', html: () => workerTemplate('en', 'licenseNoDownload') },
      ]
    : []),
  { name: 'invite-en', html: () => core.templates.en.invite.html },
  { name: 'invite-de', html: () => core.templates.de.invite.html },
]

mkdirSync(OUT, { recursive: true })
const lines = []
const browser = await chromium.launch({ args: ['--no-sandbox', '--disable-dev-shm-usage'] })

// --- screenshots + what the page asks the network for ------------------------
const report = []
for (const shot of SHOTS) {
  const file = join(OUT, `${shot.name}.html`)
  writeFileSync(file, fill(shot.html()))
  for (const mode of ['light', 'dark', 'blocked']) {
    const ctx = await browser.newContext({
      colorScheme: mode === 'dark' ? 'dark' : 'light',
      viewport: { width: 760, height: 900 },
      deviceScaleFactor: 2,
    })
    const page = await ctx.newPage()
    const requests = []
    page.on('request', (r) => {
      if (r.url() !== `file://${file}`) requests.push(r.url())
    })
    if (mode === 'blocked')
      await page.route('**/*', (route) =>
        route.request().url() === `file://${file}` ? route.continue() : route.abort(),
      )
    await page.goto(`file://${file}`, { waitUntil: 'networkidle' })
    const png = join(OUT, `${shot.name}.${mode}.png`)
    await page.screenshot({ path: png, fullPage: true })
    report.push({
      shot: shot.name,
      mode,
      requests: requests.length,
      imgElements: await page.evaluate(() => document.images.length),
      sha: createHash('sha256').update(readFileSync(png)).digest('hex').slice(0, 16),
    })
    await ctx.close()
  }
}

lines.push('shot                 mode      requests  images  sha256/16')
for (const r of report)
  lines.push(
    `${r.shot.padEnd(20)} ${r.mode.padEnd(9)} ${String(r.requests).padStart(8)} ${String(r.imgElements).padStart(6)}  ${r.sha}`,
  )
lines.push('')
for (const shot of SHOTS) {
  const l = report.find((r) => r.shot === shot.name && r.mode === 'light').sha
  const b = report.find((r) => r.shot === shot.name && r.mode === 'blocked').sha
  lines.push(
    `${shot.name.padEnd(20)} light == blocked : ${l === b ? 'IDENTICAL' : `DIFFERENT (${l} vs ${b})`}`,
  )
}
const totalReq = report.reduce((n, r) => n + r.requests, 0)
const totalImg = report.reduce((n, r) => n + r.imgElements, 0)
lines.push('')
lines.push(`total network requests across ${report.length} renders: ${totalReq}`)
lines.push(`total image elements across ${report.length} renders: ${totalImg}`)

// --- horizontal overflow, every template in every locale ---------------------
const WIDTH = 360
const ctx = await browser.newContext({ viewport: { width: WIDTH, height: 800 } })
const page = await ctx.newPage()
const rows = []
for (const locale of LOCALES) {
  const cases = [
    ...(hasWorker
      ? workerTemplateIds().map((id) => [id, workerTemplate(locale, id)])
      : []),
    ['invite', core.templates[locale].invite.html],
  ]
  for (const [id, html] of cases) {
    const f = join(OUT, `ov-${locale}-${id}.html`)
    writeFileSync(f, fill(html))
    await page.goto(`file://${f}`, { waitUntil: 'load' })
    const m = await page.evaluate(() => ({
      scrollW: document.documentElement.scrollWidth,
      clientW: document.documentElement.clientWidth,
    }))
    rows.push({ locale, id, ...m, overflow: m.scrollW > m.clientW })
  }
}
await browser.close()

lines.push('')
lines.push(`viewport ${WIDTH}px — horizontal overflow, ${rows.length} renders`)
for (const r of rows)
  lines.push(
    `${r.locale.padEnd(7)} ${r.id.padEnd(19)} scrollW ${String(r.scrollW).padStart(5)}  clientW ${String(r.clientW).padStart(5)}  ${r.overflow ? 'OVERFLOWS' : 'fits'}`,
  )
const bad = rows.filter((r) => r.overflow)
lines.push('')
lines.push(bad.length ? `${bad.length} render(s) scroll horizontally` : 'no render scrolls horizontally')
if (!hasWorker)
  lines.push('NOTE: commercial/license-worker is absent here, so its two bodies were not rendered.')

const text = lines.join('\n') + '\n'
writeFileSync(join(OUT, 'evidence.txt'), text)
console.log(text)
process.exit(totalReq === 0 && totalImg === 0 && bad.length === 0 ? 0 : 1)
