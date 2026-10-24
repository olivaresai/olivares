// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Pure verdict half of the console capture gate.
// Callers: at-run.ts fills QaRouteResult per route/width/theme from the
// orange/textfit/overlap scans and asks qaBlocking for the blocking entries;
// selftest.ts feeds planted-shaped results to prove every entry wires. No
// existing file served this purpose: the blocking construction lived inline
// in at-run.ts, unreachable from any test.
//
// Fail-closed by construction — a skipped check must never read as clean:
//   · qaRan false, or the route not measured/rendered ⇒ capture-not-measured
//     (at-run sets qaRan only when the page settled and the checks ran);
//   · the scans inspected nothing ⇒ capture-no-elements / capture-no-text;
//   · wrong width or theme ⇒ capture-width / capture-theme;
//   · the walk covered no routes ⇒ capture-no-routes.
// Then the findings: VISIBLE glow-family hits (box/text-shadow, blur filters,
// gradients — hidden styles paint nothing), orange SVG gradient stops, running
// light animations, a second filled orange control in the first screen, clipped
// text and overlaps. Fail-closed on the measurement channels too: unreadable
// keyframes (capture-kf-errors) and colors the canvas probe could not resolve
// (capture-unparsed) block — an under-reported scan must never read as clean.
//
// Entry prefixes are a contract: the CI planted step and the selftest assert
// on them (`glow /login@1440`, `orange-count …`, `overlap …`).

import type { OrangeReport } from './orange.ts'
import type { OverlapHit } from './overlap.ts'
import type { TextFitHit } from './textfit.ts'

export interface QaRouteResult {
  route: string
  width: number
  medible: boolean
  rendered: boolean
  qaRan: boolean
  qaOrange: OrangeReport
  qaTextFit: TextFitHit[]
  qaOverlap: OverlapHit[]
  qaTextNodes: number
  measuredWidth: number
  darkApplied: boolean
  // `:root`'s --foreground / --background as the page resolves them
  rootFg: string
  rootBg: string
}

export function qaBlocking(
  results: QaRouteResult[],
  theme: 'dark' | 'light',
): string[] {
  if (!results.length)
    return ['capture-no-routes: the walk covered no routes at all']
  const out: string[] = []
  for (const r of results) {
    const at = `${r.route}@${r.width}`
    if (!r.qaRan || !r.medible || !r.rendered) {
      out.push(
        `capture-not-measured ${at}: the qa checks never ran on this route — not clean`,
      )
      continue
    }
    if (r.qaOrange.n === 0)
      out.push(
        `capture-no-elements ${at}: orangeScan read zero computed styles`,
      )
    if (r.qaTextNodes === 0)
      out.push(`capture-no-text ${at}: textfitScan measured zero text nodes`)
    if (r.measuredWidth !== r.width)
      out.push(
        `capture-width ${at}: measured ${r.measuredWidth}px, expected ${r.width}px`,
      )
    if (r.darkApplied !== (theme === 'dark'))
      out.push(
        `capture-theme ${at}: dark class ${r.darkApplied ? 'present' : 'absent'}, theme is ${theme}`,
      )
    // no theme tokens, or the same color for both: no stylesheet reached the
    // page, so every scan below read an unstyled document. No theme defines
    // --foreground and --background equal.
    if (!r.rootFg || !r.rootBg || r.rootFg === r.rootBg)
      out.push(
        `capture-no-stylesheet ${at}: --foreground ${JSON.stringify(r.rootFg)}, --background ${JSON.stringify(r.rootBg)} — the console CSS did not apply`,
      )
    for (const h of r.qaOrange.hits)
      if (h.visible) out.push(`glow ${at}: ${h.kind} ${h.sel} ${h.value}`)
    if (r.qaOrange.stops.length)
      out.push(
        `glow-stop ${at}: ${r.qaOrange.stops.length} orange SVG gradient stop(s) — first ${r.qaOrange.stops[0].in}:${r.qaOrange.stops[0].color}`,
      )
    const running = r.qaOrange.animations.filter((a) => a.state === 'running')
    if (running.length)
      out.push(
        `glow-animation ${at}: ${running.length} running — ${running.map((a) => a.name).join(',')}`,
      )
    if (r.qaOrange.kfErrors > 0)
      out.push(
        `capture-kf-errors ${at}: ${r.qaOrange.kfErrors} unreadable keyframes — animation channel under-reported`,
      )
    if (r.qaOrange.unparsed > 0)
      out.push(
        `capture-unparsed ${at}: ${r.qaOrange.unparsed} colors the canvas probe could not resolve`,
      )
    if (r.qaOrange.buttons.filled.length > 1)
      out.push(
        `orange-count ${at}: ${r.qaOrange.buttons.filled.length} filled orange controls in the first screen — at most one`,
      )
    for (const h of r.qaTextFit)
      out.push(`text-fit ${at}: ${h.kind} ${h.sel} "${h.text}"`)
    for (const h of r.qaOverlap)
      out.push(`overlap ${at}: ${h.a} × ${h.b} (${h.area}px)`)
  }
  return out
}
