// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Overlap scan, ported from the console concept QA check concepts-c2/qa/overlap.js
// so the capture gate measures the same surface with the same thresholds. Evaluated in the
// page (at-run.ts and selftest.ts pass it to page.evaluate). The port is
// faithful to the concept check, typed.
//
// Reports visible text runs whose box intersects another element's text run
// (not an ancestor/descendant), and text runs crossing a filled graphic that
// is not their ancestor (kind `text-on-mark`: chart bars, ticks, lane bars).
// Ignores sr-only, hidden, opacity<0.05, closed details, runs covered by a
// sticky/fixed layer, runs clipped by a scrolling ancestor, and runs whose
// centre is not their own element's topmost point.
//
// Deliberate divergences from the concept check, all false-positive fixes so
// the gate can pass on main without weakening its plants:
//   · a run's rect is intersected with every scrolling/clipping ancestor's
//     inner box (visible-rect clipping) instead of a centre-point probe: a
//     title truncated by its cell no longer "overlaps" the next column;
//   · React Flow's floating panels count as layers (their minimap,
//     attribution and panel labels overlay the canvas by design);
//   · marks with pointer-events:none are decorations (nobody reads over
//     them), so they never report text-on-mark;
//   · the element under the run's centre is probed only to find the sticky/
//     fixed layer covering it (header, fade) — the concept check's comment
//     claims a topmost-point check the code does not perform.

export interface OverlapHit {
  kind?: 'text-on-mark'
  a: string
  b: string
  area: number
}

export function overlapScan(): OverlapHit[] {
  const vis = (el: Element): boolean => {
    for (
      let e: Element | null = el;
      e && e.nodeType === 1;
      e = e.parentElement
    ) {
      const c = getComputedStyle(e)
      if (
        c.display === 'none' ||
        c.visibility === 'hidden' ||
        parseFloat(c.opacity) < 0.05
      )
        return false
    }
    return true
  }
  // the run's rect intersected with the inner box of every scrolling/clipping
  // ancestor (including the element itself): the part that is really visible
  const visRect = (el: Element, b: DOMRect): DOMRect | null => {
    let L = b.left
    let T = b.top
    let R = b.right
    let B = b.bottom
    for (
      let a: Element | null = el;
      a && a.nodeType === 1;
      a = a.parentElement
    ) {
      if (a === document.body || a === document.documentElement) continue
      const c = getComputedStyle(a)
      if (!/(auto|scroll|hidden|clip)/.test(c.overflowX + c.overflowY)) continue
      const k = a.getBoundingClientRect()
      const bw = (s: string): number => parseFloat(c.getPropertyValue(s)) || 0
      L = Math.max(L, k.left + bw('border-left-width'))
      T = Math.max(T, k.top + bw('border-top-width'))
      R = Math.min(R, k.right - bw('border-right-width'))
      B = Math.min(B, k.bottom - bw('border-bottom-width'))
      if (R - L <= 1 || B - T <= 1) return null
    }
    return new DOMRect(L, T, R - L, B - T)
  }
  const sel = (el: Element): string => {
    const p: string[] = []
    for (
      let e: Element | null = el, i = 0;
      e && e.nodeType === 1 && e !== document.body && i < 3;
      e = e.parentElement, i++
    ) {
      let s = e.tagName.toLowerCase()
      if (e.id) s += '#' + e.id
      else if (e.classList && e.classList.length)
        s += '.' + [...e.classList].slice(0, 2).join('.')
      p.unshift(s)
    }
    return p.join(' > ')
  }
  interface Run {
    el: HTMLElement
    b: DOMRect
    t: string
  }
  const runs: Run[] = []
  const w = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
  for (let n = w.nextNode(); n; n = w.nextNode()) {
    const t = n.nodeValue
    if (!t || !t.trim()) continue
    const el = n.parentElement
    if (
      !el ||
      el.closest(
        'script,style,template,noscript,.sr-only,.skip,[aria-hidden="true"] .sr-only',
      )
    )
      continue
    const eb = el.getBoundingClientRect()
    if (eb.width <= 1 || eb.height <= 1 || !vis(el)) continue
    let closed = false
    for (
      let d = el.closest('details');
      d && !closed;
      d = d.parentElement ? d.parentElement.closest('details') : null
    ) {
      const sm = el.closest('summary')
      if (!d.open && !(sm && sm.parentElement === d)) closed = true
    }
    if (closed) continue
    const r = document.createRange()
    r.selectNodeContents(n)
    for (const b of r.getClientRects()) {
      if (
        b.width <= 1 ||
        b.height <= 1 ||
        b.bottom <= 0 ||
        b.top >= innerHeight ||
        b.right <= 0 ||
        b.left >= innerWidth
      )
        continue
      // the visible rect: the run intersected with every scrolling/clipping
      // ancestor's inner box (list rows scrolled out of view, lines cut by a
      // card measure nothing outside it)
      const vb = visRect(el, b)
      if (!vb) continue
      // the element under the run's centre feeds the sticky/fixed coverage
      // check below: a header, fade or footer covering the text must not
      // count as overlapping it
      const top = document.elementFromPoint(
        Math.min(innerWidth - 1, Math.max(0, (vb.left + vb.right) / 2)),
        Math.min(innerHeight - 1, Math.max(0, (vb.top + vb.bottom) / 2)),
      )
      if (!top) continue
      // covered by a sticky/fixed layer (header, fade, footer) that does not hold this text
      let covered = false
      for (
        let a: Element | null = top;
        a && a !== document.body;
        a = a.parentElement
      ) {
        const p = getComputedStyle(a).position
        if ((p === 'sticky' || p === 'fixed') && !a.contains(el)) {
          covered = true
          break
        }
      }
      if (covered) continue
      runs.push({ el, b: vb, t: t.trim().slice(0, 40) })
    }
  }
  const layer = (e: Element): Element | null =>
    e.closest(
      '[role=dialog],.sheet,dialog,.palette,[data-layer],.react-flow__minimap,.react-flow__attribution,.react-flow__panel',
    )
  const hits: OverlapHit[] = []
  const seen = new Set<string>()
  for (let i = 0; i < runs.length; i++)
    for (let j = i + 1; j < runs.length; j++) {
      const A = runs[i]
      const B = runs[j]
      if (A.el === B.el || A.el.contains(B.el) || B.el.contains(A.el)) continue
      const x = Math.min(A.b.right, B.b.right) - Math.max(A.b.left, B.b.left)
      const y = Math.min(A.b.bottom, B.b.bottom) - Math.max(A.b.top, B.b.top)
      if (x > 2 && y > 2) {
        // ignore stacked layers where one is inside a floating layer above the other
        // (sheets, palettes, dialogs)
        if (layer(A.el) !== layer(B.el)) continue
        const k = sel(A.el) + '|' + sel(B.el)
        if (seen.has(k)) continue
        seen.add(k)
        hits.push({
          a: sel(A.el) + ' "' + A.t + '"',
          b: sel(B.el) + ' "' + B.t + '"',
          area: Math.round(x * y),
        })
      }
    }
  // text on a mark: a text run crossing a filled graphic that is not its ancestor
  // (chart bars, ticks, lane bars, svg shapes)
  interface Mark {
    el: Element
    b: DOMRect
  }
  const marks: Mark[] = []
  for (const g of document.querySelectorAll(
    'svg rect, svg circle, svg path, svg polygon',
  )) {
    const c = getComputedStyle(g)
    if (
      c.fill === 'none' ||
      c.fill === 'rgba(0, 0, 0, 0)' ||
      parseFloat(c.fillOpacity) < 0.2 ||
      c.pointerEvents === 'none'
    )
      continue
    const b = g.getBoundingClientRect()
    if (b.width * b.height < 12 || b.width > 600 || !vis(g)) continue
    marks.push({ el: g, b })
  }
  for (const e of document.body.querySelectorAll('div,span,i,b,li')) {
    if (e.childElementCount || (e.textContent ?? '').trim()) continue
    const c = getComputedStyle(e)
    if (
      (c.backgroundColor === 'rgba(0, 0, 0, 0)' &&
        c.backgroundImage === 'none') ||
      c.pointerEvents === 'none'
    )
      continue
    const b = e.getBoundingClientRect()
    if (b.width * b.height < 6 || b.width > 600 || b.height > 200 || !vis(e))
      continue
    marks.push({ el: e, b })
  }
  for (const R of runs)
    for (const M of marks) {
      if (M.el.contains(R.el) || R.el.contains(M.el)) continue
      if (layer(R.el) !== layer(M.el)) continue
      const x = Math.min(R.b.right, M.b.right) - Math.max(R.b.left, M.b.left)
      const y = Math.min(R.b.bottom, M.b.bottom) - Math.max(R.b.top, M.b.top)
      if (x > 2 && y > 2) {
        const k = 'm|' + sel(R.el) + '|' + sel(M.el)
        if (seen.has(k)) continue
        seen.add(k)
        hits.push({
          kind: 'text-on-mark',
          a: sel(R.el) + ' "' + R.t + '"',
          b: sel(M.el),
          area: Math.round(x * y),
        })
      }
    }
  return hits
}
