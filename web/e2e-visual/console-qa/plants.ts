// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Control-case payloads for the console capture gate.
// Each function is evaluated inside the page under test and plants ONE
// violation the gate must report:
//
//   glow          — orange box-shadow on an in-page element (orange.ts hit)
//   second-orange — two filled orange buttons in the first screen
//   overlap       — two overlapping text runs (overlap.ts hit)
//   textfit       — a clipped fixed-width line without an ellipsis (textfit.ts hit)
//
// The selftest plants through these to prove each check reports its plant, and
// the gate itself walks with QA_PLANT=<kind> to prove the JOB fails on each
// plant. One definition, so the plant the check is proven against can never
// drift from the plant the job is proven with.

export const PLANT_KINDS = [
  'glow',
  'second-orange',
  'overlap',
  'textfit',
] as const

export type PlantKind = (typeof PLANT_KINDS)[number]

export function plantQA(kind: PlantKind): () => void {
  switch (kind) {
    case 'glow':
      return () => {
        const d = document.createElement('div')
        d.style.cssText =
          'position:fixed;left:8px;top:8px;width:64px;height:64px;' +
          'box-shadow:0 0 28px rgba(240,128,0,0.95);z-index:2147483000'
        document.body.appendChild(d)
      }
    case 'second-orange':
      // Self-contained on purpose: page.evaluate serializes the function
      // SOURCE, so anything it references outside its own body dies on the
      // other side (a closure over a helper came back as ReferenceError).
      // The buttons carry NO text: a planted text run could pair with a real
      // element in the overlap scan and blur which verdict reddened the run.
      return () => {
        for (const top of [8, 48]) {
          const b = document.createElement('button')
          b.style.cssText =
            `position:fixed;left:8px;top:${top}px;width:120px;height:32px;` +
            'background:#f08000;color:#fff;z-index:2147483000'
          document.body.appendChild(b)
        }
      }
    case 'overlap':
      // The two runs overlay each other, but neither is under a sticky/fixed
      // element that does not hold it: overlap.ts skips a run whose centre is
      // covered by one (a real header, a fade), so a fixed-host plant would
      // silently not count. Absolute siblings inside a fixed host keep both
      // runs visible at their own centre points.
      return () => {
        const host = document.createElement('div')
        host.style.cssText = 'position:fixed;left:0;top:0;z-index:2147483000'
        for (const left of [0, 10]) {
          const d = document.createElement('div')
          d.textContent = 'planted overlapping text'
          d.style.cssText =
            `position:absolute;left:${left}px;top:0;font-size:16px;` +
            'color:#000;background:#fff;line-height:18px'
          host.appendChild(d)
        }
        document.body.appendChild(host)
      }
    case 'textfit':
      // A fixed, non-ellipsis, overflow-hidden line: the text sits outside
      // the 40px box with no scroll that could bring it into view, so
      // clipped-text must report. No orange, no shadow, one run only: just
      // textfit.ts can redden this plant.
      return () => {
        const d = document.createElement('div')
        d.style.cssText =
          'position:fixed;left:8px;top:8px;width:40px;height:16px;' +
          'overflow:hidden;white-space:nowrap;font-size:12px;' +
          'background:#334155;color:#fff;z-index:2147483000'
        d.textContent = 'planted clipped line for the text-fit check'
        document.body.appendChild(d)
      }
  }
}
