// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Control cases for the console capture gate.
// Standalone runner, same shape as at-run.ts (no `playwright test`):
//
//   node --experimental-strip-types e2e-visual/console-qa/selftest.ts
//
// Each check the gate blocks on — glow (orange.ts), filled-orange count
// (orange.ts), text overlap (overlap.ts), text-fit (textfit.ts) — is planted
// on a fixture page through the SAME payloads the gate's QA_PLANT mode
// injects (plants.ts), and the check must report it. A clipped line must be
// reported by textfit.ts, and a clean page must report nothing — including a
// non-orange control, an orange STATE TOGGLE (role=switch, excluded from the
// filled count), an orange control inside a `.mc` mock row, an intentional
// ellipsis — alone and inside a clipped ancestor, the console row shape — a
// pointer-events:none decoration, a React Flow panel and a hidden
// glow (flagged invisible by the scan and dropped by the verdict half).
// The scan exclusions themselves are pinned too: the animation channel must
// ignore opacity-only keyframes and report filter-touching ones. The pure
// verdict wiring (verdicts.ts) is fed planted-shaped results and must emit
// each blocking entry — including the fail-closed ones: a route the checks
// skipped, a route whose scans saw no text, unreadable keyframes, unresolved
// colors and an empty walk all block, while an INVISIBLE glow hit must NOT
// block (hidden styles paint nothing) — so a plant the checks DO see but the
// gate does NOT block on fails too. A dropped control case fails via the
// assertion counter. The capture CI job runs this before the walk, so a check
// that stops detecting its plant fails the job even while the walk is green.

import { chromium, type Page } from '@playwright/test'
import { orangeScan, type OrangeReport } from './orange.ts'
import { overlapScan, type OverlapHit } from './overlap.ts'
import { textfitScan, type TextFitRow } from './textfit.ts'
import { plantQA, type PlantKind } from './plants.ts'
import { qaBlocking } from './verdicts.ts'

// The clean page also carries the exclusions the scans are allowed to make,
// so each one is pinned by an element that WOULD report if the exclusion
// broke: the switch decoy has no visible label (a 40x24 orange box must not
// overflow into a text-fit hit); an ellipsis-clipped line is a designed
// truncation, not a clip defect; a hidden glow paints nothing and must come
// back flagged invisible (the verdict half drops it — pinned below); a
// pointer-events:none overlay is a decoration nobody reads over; a React
// Flow panel is a floating layer over the canvas by design.
const CLEAN =
  '<main><h1>Console</h1><p>Estate and Ledger, one screen.</p>' +
  '<button style="background:#334155;color:#fff">Neutral</button>' +
  '<button role="switch" aria-checked="true" aria-label="Decoy" ' +
  'style="background:#f08000;color:#fff;width:40px;height:24px"></button>' +
  '<div class="mc"><button style="background:#f08000;color:#fff">Mock</button></div>' +
  '<div style="width:80px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">' +
  'This line ends in an intentional ellipsis</div>' +
  '<div aria-hidden="true" ' +
  'style="display:none;box-shadow:0 0 28px rgba(240,128,0,0.95)"></div>' +
  '<div aria-hidden="true" style="position:absolute;left:8px;top:24px;' +
  'width:140px;height:30px;background:rgba(255,255,255,0.6);pointer-events:none">' +
  '</div>' +
  '<div class="react-flow__panel" style="position:absolute;left:8px;top:72px;' +
  'width:180px;height:60px;background:#334155"></div></main>'

let failures = 0
let runs = 0
// Bumped by hand when a control case is added or removed: a case someone
// comments out must fail this run instead of silently shrinking the battery.
const EXPECTED_ASSERTS = 42

function assert(cond: boolean, what: string): void {
  runs++
  if (cond) {
    console.log(`  ok ${what}`)
  } else {
    console.error(`  FAIL ${what}`)
    failures++
  }
}

async function plantCase(
  page: Page,
  plant: PlantKind,
  assertOn: (orange: OrangeReport, overlap: OverlapHit[]) => void,
): Promise<void> {
  await page.setContent(CLEAN)
  await page.evaluate(plantQA(plant))
  assertOn(await page.evaluate(orangeScan), await page.evaluate(overlapScan))
}

async function main(): Promise<void> {
  const browser = await chromium.launch({
    args: ['--no-sandbox', '--disable-dev-shm-usage'],
  })
  const context = await browser.newContext({
    viewport: { width: 1024, height: 768 },
    reducedMotion: 'reduce',
  })
  const page = await context.newPage()

  await plantCase(page, 'glow', (orange) =>
    assert(
      // CLEAN carries a hidden orange glow, so only a VISIBLE hit is the plant
      orange.hits.some((h) => h.kind === 'orange box-shadow' && h.visible),
      'glow plant reported as a visible orange box-shadow',
    ),
  )
  // dedupe control: a hidden twin sharing the visible element's selector is
  // visited first, so the key must keep both and the visible hit must survive
  // to the verdict half (a hidden-only report blocks nothing).
  await page.setContent(
    '<main><h1>Twin</h1><p>Estate and Ledger, one screen.</p>' +
      '<div class="twin" aria-hidden="true" ' +
      'style="display:none;box-shadow:0 0 28px rgba(240,128,0,0.95)"></div>' +
      '<div class="twin" style="width:20px;height:20px;' +
      'box-shadow:0 0 28px rgba(240,128,0,0.95)"></div></main>',
  )
  const twin = await page.evaluate(orangeScan)
  assert(
    twin.hits.some((h) => h.kind === 'orange box-shadow' && h.visible),
    'a hidden twin does not dedupe away the visible glow behind it',
  )

  await plantCase(page, 'second-orange', (orange) =>
    assert(
      orange.buttons.filled.length >= 2,
      `second-orange plant reported (${orange.buttons.filled.length} filled)`,
    ),
  )
  await plantCase(page, 'overlap', (_orange, overlap) =>
    assert(
      overlap.some((h) => (h.a + h.b).includes('planted overlapping text')),
      'overlap plant reported, naming the planted text',
    ),
  )

  // the ambient glow of a page sits on html/body, not on a descendant: the
  // scan must read those two as well
  await page.setContent(
    '<style>body{background-image:radial-gradient(circle at 30% 20%,' +
      'rgba(240,128,0,0.5),transparent 60%)}</style>' +
      CLEAN,
  )
  assert(
    (await page.evaluate(orangeScan)).hits.some(
      (h) =>
        h.visible &&
        h.sel.startsWith('html') &&
        h.kind === 'orange radial-gradient',
    ),
    'an orange radial-gradient on body is reported',
  )

  // text-fit control: a clipped line must be reported, and as its own kind.
  await page.setContent(
    '<main><div style="width:60px;overflow:hidden;white-space:nowrap">overflowing text for the clip check</div></main>',
  )
  const clipped = (await page.evaluate(textfitScan)).filter(
    (h: TextFitRow) => h.kind !== 'info',
  )
  assert(
    clipped.some((h) => h.kind === 'clipped-text'),
    'clipped text reported as clipped-text',
  )

  // ancestor-branch controls: the console rows put an intentional ellipsis
  // inside clipped row containers, which is a designed truncation, not a cut
  // — while the same clip WITHOUT an ellipsis is a cut, and an ellipsis never
  // excuses a clip that cuts the line itself vertically.
  await page.setContent(
    '<main>' +
      '<div style="width:120px;overflow:hidden">' +
      '<span style="display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">' +
      'a long row name that ends in an intentional ellipsis</span></div>' +
      '<div style="width:60px;overflow:hidden">' +
      '<span style="display:block;white-space:nowrap">overflowing text without an ellipsis</span></div>' +
      '<div style="width:200px;height:10px;overflow:hidden">' +
      '<span style="display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;' +
      'font-size:16px;line-height:16px">a line whose clip cuts the glyphs</span></div>' +
      '</main>',
  )
  const ancestorRows = (await page.evaluate(textfitScan)).filter(
    (h: TextFitRow) => h.kind !== 'info',
  )
  assert(
    !ancestorRows.some(
      (h) =>
        h.kind === 'clipped-by-ancestor' &&
        h.text.includes('intentional ellipsis'),
    ),
    'an ellipsized span inside a clipped ancestor is a designed truncation, not a cut',
  )
  assert(
    ancestorRows.some(
      (h) =>
        h.kind === 'clipped-by-ancestor' &&
        h.text.includes('without an ellipsis'),
    ),
    'a non-ellipsized span cut by an ancestor still reports clipped-by-ancestor',
  )
  assert(
    ancestorRows.some(
      (h) =>
        h.kind === 'clipped-by-ancestor' && h.text.includes('cuts the glyphs'),
    ),
    'an ellipsis never excuses a vertical cut of the line itself',
  )

  // line-clamp is a designed truncation like an ellipsis (the select trigger's
  // value span is `line-clamp-1`: the browser paints the ellipsis on the last
  // visible line). A wrapping box that sets text-overflow:ellipsis WITHOUT
  // nowrap paints none on a vertical cut, so that one stays a cut.
  await page.setContent(
    '<main>' +
      '<span style="display:-webkit-box;-webkit-box-orient:vertical;-webkit-line-clamp:1;' +
      'overflow:hidden;width:120px">a line-clamped value that wraps onto a second line</span>' +
      '<p style="display:-webkit-box;-webkit-box-orient:vertical;-webkit-line-clamp:1;' +
      'overflow:hidden;width:120px"><b>a nested value inside a line-clamped block that wraps</b></p>' +
      '<div style="width:120px;height:20px;overflow:hidden;text-overflow:ellipsis;' +
      'font-size:16px;line-height:20px">a wrapping box whose ellipsis never paints on the cut line</div>' +
      '</main>',
  )
  const clampRows = (await page.evaluate(textfitScan)).filter(
    (h: TextFitRow) => h.kind !== 'info',
  )
  assert(
    !clampRows.some((h) => h.text.includes('line-clamped value')),
    'a line-clamped element is a designed truncation, not a cut',
  )
  assert(
    !clampRows.some((h) => h.text.includes('nested value')),
    'text inside a line-clamped block is a designed truncation, not a cut',
  )
  assert(
    clampRows.some(
      (h) => h.kind === 'clipped-text' && h.text.includes('never paints'),
    ),
    'text-overflow:ellipsis does not excuse a wrapping box cut vertically',
  )

  // textfit plant: the same payload the gate injects on a walked route must
  // be reported by textfit.ts as its own kind.
  await page.setContent(CLEAN)
  await page.evaluate(plantQA('textfit'))
  const fitPlant = (await page.evaluate(textfitScan)).filter(
    (h: TextFitRow) => h.kind !== 'info',
  )
  assert(
    fitPlant.some((h) => h.kind === 'clipped-text'),
    'textfit plant reported as clipped-text',
  )

  // animation channel control: keyframes touching only opacity must not count
  // as light-moving; keyframes touching filter must.
  await page.setContent(
    '<style>@keyframes pulse-live{from{opacity:1}to{opacity:.4}}' +
      '@keyframes blur-kf{from{filter:blur(0)}to{filter:blur(2px)}}</style>' +
      '<main><div id="live" style="animation:pulse-live 1s infinite">live</div>' +
      '<div id="blurred" style="animation:blur-kf 1s infinite">blurred</div></main>',
  )
  const anim = await page.evaluate(orangeScan)
  const animNames = anim.animations.map((a) => a.name)
  assert(
    !animNames.includes('pulse-live'),
    `animation channel: opacity-only keyframes do not count (${animNames.join(',')})`,
  )
  assert(
    animNames.includes('blur-kf'),
    `animation channel: filter-touching keyframes count (${animNames.join(',')})`,
  )

  // a clean page reports nothing — including the decoys: a non-orange control
  // must not count as filled, and an orange control inside a `.mc` mock row
  // must count as mock, not filled. Its text must still be measured.
  await page.setContent(CLEAN)
  const orange = await page.evaluate(orangeScan)
  assert(
    orange.hits.length === 1 &&
      orange.hits[0]?.kind === 'orange box-shadow' &&
      !orange.hits[0]?.visible,
    `clean page: the hidden glow is the only hit and is flagged invisible (${orange.hits.length} hits)`,
  )
  assert(
    orange.buttons.filled.length === 0,
    `clean page: no filled orange controls (${orange.buttons.filled.length})`,
  )
  assert(
    orange.buttons.mock.length === 1,
    `clean page: the .mc mock control is counted separately, not as filled (${orange.buttons.mock.length} mock)`,
  )
  const overlap = await page.evaluate(overlapScan)
  const firstOverlap = overlap[0]
  assert(
    overlap.length === 0,
    `clean page: no overlaps (${overlap.length}${firstOverlap ? ' — ' + firstOverlap.a + ' × ' + firstOverlap.b : ''})`,
  )
  const cleanRows = await page.evaluate(textfitScan)
  const cleanInfo = cleanRows.find((h: TextFitRow) => h.kind === 'info')
  assert(
    cleanInfo !== undefined && cleanInfo.kind === 'info',
    'clean page: textfit reports its text-node count',
  )
  assert(
    cleanInfo !== undefined &&
      cleanInfo.kind === 'info' &&
      cleanInfo.textNodes > 0,
    `clean page: text nodes measured (${cleanInfo && cleanInfo.kind === 'info' ? cleanInfo.textNodes : 'none'})`,
  )
  const cleanFit = cleanRows.filter((h: TextFitRow) => h.kind !== 'info')
  assert(
    cleanFit.length === 0,
    `clean page: no text-fit hits (${cleanFit.length}${cleanFit[0] ? ' — ' + cleanFit[0].kind + ' ' + cleanFit[0].sel : ''})`,
  )

  // verdict wiring control: the pure half of the gate (verdicts.ts) turns
  // scan results into blocking entries. Feed it planted-shaped results and
  // assert each entry appears, so a plant the checks DO see but the gate does
  // NOT block on still fails this run.
  const emptyOrange: OrangeReport = {
    hits: [],
    buttons: { filled: [], mock: [] },
    stops: [],
    animations: [],
    kfErrors: 0,
    unparsed: 0,
    n: 5,
  }
  const wired = qaBlocking(
    [
      {
        route: '/wiring-glow',
        width: 1440,
        medible: true,
        rendered: true,
        qaRan: true,
        qaOrange: {
          ...emptyOrange,
          hits: [
            {
              kind: 'orange box-shadow',
              sel: 'div.ctl',
              visible: true,
              value: '0 0 28px rgba(240,128,0,0.95)',
            },
          ],
          stops: [{ in: 'g', color: '#f08000', offset: null }],
          animations: [
            {
              name: 'pulse',
              target: 'div.ctl',
              props: ['filter'],
              state: 'running',
            },
          ],
        },
        qaTextFit: [],
        qaOverlap: [],
        qaTextNodes: 5,
        measuredWidth: 1440,
        darkApplied: true,
        rootFg: '#fafaf9',
        rootBg: '#28282b',
      },
      {
        route: '/wiring-hidglow',
        width: 1440,
        medible: true,
        rendered: true,
        qaRan: true,
        qaOrange: {
          ...emptyOrange,
          hits: [
            {
              kind: 'orange box-shadow',
              sel: 'div.hidden',
              visible: false,
              value: '0 0 28px rgba(240,128,0,0.95)',
            },
          ],
        },
        qaTextFit: [],
        qaOverlap: [],
        qaTextNodes: 5,
        measuredWidth: 1440,
        darkApplied: true,
        rootFg: '#fafaf9',
        rootBg: '#28282b',
      },
      {
        route: '/wiring-settled',
        width: 1440,
        medible: true,
        rendered: true,
        qaRan: true,
        qaOrange: {
          ...emptyOrange,
          animations: [
            {
              name: 'fade-glow',
              target: 'div.ctl',
              props: ['filter'],
              state: 'finished',
            },
          ],
        },
        qaTextFit: [],
        qaOverlap: [],
        qaTextNodes: 5,
        measuredWidth: 1440,
        darkApplied: true,
        rootFg: '#fafaf9',
        rootBg: '#28282b',
      },
      {
        route: '/wiring-kf',
        width: 1440,
        medible: true,
        rendered: true,
        qaRan: true,
        qaOrange: { ...emptyOrange, kfErrors: 1 },
        qaTextFit: [],
        qaOverlap: [],
        qaTextNodes: 5,
        measuredWidth: 1440,
        darkApplied: true,
        rootFg: '#fafaf9',
        rootBg: '#28282b',
      },
      {
        route: '/wiring-unparsed',
        width: 1440,
        medible: true,
        rendered: true,
        qaRan: true,
        qaOrange: { ...emptyOrange, unparsed: 2 },
        qaTextFit: [],
        qaOverlap: [],
        qaTextNodes: 5,
        measuredWidth: 1440,
        darkApplied: true,
        rootFg: '#fafaf9',
        rootBg: '#28282b',
      },
      {
        route: '/wiring-count',
        width: 1024,
        medible: true,
        rendered: true,
        qaRan: true,
        qaOrange: {
          ...emptyOrange,
          buttons: {
            filled: [
              { sel: 'button.a', text: 'A', top: 8 },
              { sel: 'button.b', text: 'B', top: 48 },
            ],
            mock: [],
          },
        },
        qaTextFit: [],
        qaOverlap: [],
        qaTextNodes: 5,
        measuredWidth: 1024,
        darkApplied: true,
        rootFg: '#fafaf9',
        rootBg: '#28282b',
      },
      {
        route: '/wiring-fit',
        width: 1024,
        medible: true,
        rendered: true,
        qaRan: true,
        qaOrange: emptyOrange,
        qaTextFit: [{ kind: 'clipped-text', sel: 'div.ctl', text: 'cut' }],
        qaOverlap: [],
        qaTextNodes: 5,
        measuredWidth: 1024,
        darkApplied: true,
        rootFg: '#fafaf9',
        rootBg: '#28282b',
      },
      {
        route: '/wiring-overlap',
        width: 1440,
        medible: true,
        rendered: true,
        qaRan: true,
        qaOrange: emptyOrange,
        qaTextFit: [],
        qaOverlap: [{ a: 'div.a "x"', b: 'div.b "y"', area: 12 }],
        qaTextNodes: 5,
        measuredWidth: 1440,
        darkApplied: true,
        rootFg: '#fafaf9',
        rootBg: '#28282b',
      },
      {
        route: '/wiring-unmeasured',
        width: 1440,
        medible: false,
        rendered: true,
        qaRan: false,
        qaOrange: emptyOrange,
        qaTextFit: [],
        qaOverlap: [],
        qaTextNodes: 0,
        measuredWidth: 0,
        darkApplied: false,
        rootFg: '#fafaf9',
        rootBg: '#28282b',
      },
      {
        route: '/wiring-skipped',
        width: 1440,
        medible: true,
        rendered: true,
        qaRan: false,
        qaOrange: emptyOrange,
        qaTextFit: [],
        qaOverlap: [],
        qaTextNodes: 0,
        measuredWidth: 1440,
        darkApplied: true,
        rootFg: '#fafaf9',
        rootBg: '#28282b',
      },
      {
        route: '/wiring-empty',
        width: 1440,
        medible: true,
        rendered: true,
        qaRan: true,
        qaOrange: { ...emptyOrange, n: 0 },
        qaTextFit: [],
        qaOverlap: [],
        qaTextNodes: 5,
        measuredWidth: 1440,
        darkApplied: true,
        rootFg: '#fafaf9',
        rootBg: '#28282b',
      },
      {
        route: '/wiring-notext',
        width: 1440,
        medible: true,
        rendered: true,
        qaRan: true,
        qaOrange: emptyOrange,
        qaTextFit: [],
        qaOverlap: [],
        qaTextNodes: 0,
        measuredWidth: 1440,
        darkApplied: true,
        rootFg: '#fafaf9',
        rootBg: '#28282b',
      },
      {
        route: '/wiring-theme',
        width: 1440,
        medible: true,
        rendered: true,
        qaRan: true,
        qaOrange: emptyOrange,
        qaTextFit: [],
        qaOverlap: [],
        qaTextNodes: 5,
        measuredWidth: 1440,
        darkApplied: false,
        rootFg: '#fafaf9',
        rootBg: '#28282b',
      },
      {
        route: '/wiring-nosheet',
        width: 1440,
        medible: true,
        rendered: true,
        qaRan: true,
        qaOrange: emptyOrange,
        qaTextFit: [],
        qaOverlap: [],
        qaTextNodes: 5,
        measuredWidth: 1440,
        darkApplied: true,
        rootFg: '',
        rootBg: '',
      },
      {
        route: '/wiring-samesheet',
        width: 1440,
        medible: true,
        rendered: true,
        qaRan: true,
        qaOrange: emptyOrange,
        qaTextFit: [],
        qaOverlap: [],
        qaTextNodes: 5,
        measuredWidth: 1440,
        darkApplied: true,
        rootFg: '#28282b',
        rootBg: '#28282b',
      },
      {
        route: '/wiring-width',
        width: 1440,
        medible: true,
        rendered: true,
        qaRan: true,
        qaOrange: emptyOrange,
        qaTextFit: [],
        qaOverlap: [],
        qaTextNodes: 5,
        measuredWidth: 1024,
        darkApplied: true,
        rootFg: '#fafaf9',
        rootBg: '#28282b',
      },
    ],
    'dark',
  )
  const has = (prefix: string): boolean =>
    wired.some((l) => l.startsWith(prefix))
  assert(has('glow /wiring-glow@1440'), 'glow verdict wired')
  assert(has('glow-stop /wiring-glow@1440'), 'glow-stop verdict wired')
  assert(
    has('glow-animation /wiring-glow@1440'),
    'glow-animation verdict wired',
  )
  assert(
    !has('glow-animation /wiring-settled@1440'),
    'settled animations are not running light, not blocking',
  )
  assert(has('orange-count /wiring-count@1024'), 'orange-count verdict wired')
  assert(has('text-fit /wiring-fit@1024'), 'text-fit verdict wired')
  assert(has('overlap /wiring-overlap@1440'), 'overlap verdict wired')
  assert(
    has('capture-not-measured /wiring-unmeasured@1440'),
    'not-measured verdict wired',
  )
  assert(
    has('capture-not-measured /wiring-skipped@1440'),
    'skipped-qa verdict wired as not-measured',
  )
  assert(
    has('capture-no-elements /wiring-empty@1440'),
    'no-elements verdict wired',
  )
  assert(has('capture-no-text /wiring-notext@1440'), 'no-text verdict wired')
  assert(has('capture-theme /wiring-theme@1440'), 'theme verdict wired')
  assert(has('capture-width /wiring-width@1440'), 'width verdict wired')
  assert(
    has('capture-no-stylesheet /wiring-nosheet@1440'),
    'a route with no theme tokens blocks as capture-no-stylesheet',
  )
  assert(
    has('capture-no-stylesheet /wiring-samesheet@1440'),
    'a route whose foreground and background tokens are equal blocks',
  )
  assert(
    !has('glow /wiring-hidglow@1440'),
    'hidden glow paints nothing, not blocking',
  )
  assert(
    has('capture-kf-errors /wiring-kf@1440'),
    'unreadable-keyframes verdict wired',
  )
  assert(
    has('capture-unparsed /wiring-unparsed@1440'),
    'unparsed-colors verdict wired',
  )
  const noRoutes = qaBlocking([], 'dark')
  assert(
    noRoutes.some((l) => l.startsWith('capture-no-routes')),
    'empty walk blocks as capture-no-routes',
  )
  const cleanWired = qaBlocking(
    [
      {
        route: '/wiring-clean',
        width: 1440,
        medible: true,
        rendered: true,
        qaRan: true,
        qaOrange: emptyOrange,
        qaTextFit: [],
        qaOverlap: [],
        qaTextNodes: 5,
        measuredWidth: 1440,
        darkApplied: true,
        rootFg: '#fafaf9',
        rootBg: '#28282b',
      },
    ],
    'dark',
  )
  assert(cleanWired.length === 0, 'clean result wires no verdict')

  await browser.close()
  if (failures || runs !== EXPECTED_ASSERTS) {
    console.error(
      `SELFTEST FAILED — ${failures} control case(s) missed, ${runs}/${EXPECTED_ASSERTS} assertions ran`,
    )
    process.exit(1)
  }
  console.log(
    `SELFTEST PASS — ${runs}/${EXPECTED_ASSERTS} control assertions: every planted violation reported; clean page clean; verdicts wired`,
  )
}

main().then(
  () => process.exit(0),
  (e) => {
    console.error('selftest failed:', e)
    process.exit(1)
  },
)
