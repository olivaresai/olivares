// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Text-fit scan, ported from the console concept QA check concepts-c2/qa/textfit.js
// so the capture gate measures the same surface with the same thresholds. Evaluated in the page (at-run.ts and selftest.ts pass
// it to page.evaluate). The port is faithful to the concept check, typed, with
// three deliberate divergences:
//   · the always-true `a !== el || true` wrapper is dropped (behaviour-identical);
//   · the clipped-by-ancestor verdict is skipped when a scrollable ancestor can
//     bring the run into view (see the guard in the ancestor loop). The concept
//     check predates the console's scrollable shells: it flagged every run
//     scrolled out of view inside an overflow-hidden shell — 84 false hits on
//     /dashboards — which is the exit criterion "passes on main" fixing, not
//     a threshold edit. Scrolled content is reachable, not clipped.
//   · a designed truncation is not a clip defect: table cells and labels
//     truncate long values by design. A line-clamp (the select trigger's
//     value) paints its own ellipsis, so neither `clipped-text` nor
//     `clipped-by-ancestor` report it. A text-overflow ellipsis paints only
//     on a horizontal overflow: `clipped-text` skips an ellipsized element
//     with no vertical cut, and `clipped-by-ancestor` ignores a horizontal
//     overflow an ellipsized clip in the chain already handles (a vertical
//     cut of the line itself still reports — an ellipsis never excuses
//     cutting the glyphs). Outside-box and
//     outside-viewport measure the union intersected with every clip
//     ancestor's inner box: the part the user can actually see.
//
// Kinds (the gate blocks on every kind except `info`):
//   clipped-text       — the element's own box clips its text
//   clipped-by-ancestor— an ancestor's clipping box cuts the run
//   outside-box        — text outside its nearest bordered/backgrounded ancestor
//   doc-hscroll        — the document scrolls horizontally (fixed layouts)
//   outside-viewport   — the run sits off the horizontal viewport edge
//   info               — trailing counter row (text nodes measured), never blocks

export type TextFitKind =
  | 'clipped-text'
  | 'clipped-by-ancestor'
  | 'outside-box'
  | 'doc-hscroll'
  | 'outside-viewport'

export interface TextFitHit {
  kind: TextFitKind
  sel: string
  text: string
  note?: string
  scroll?: string
  client?: string
  overflow?: string
  textEl?: string
  textBox?: string
  clipBox?: string
  box?: string
  boxRect?: string
  innerWidth?: number
  scrollWidth?: number
}

export interface TextFitInfo {
  kind: 'info'
  textNodes: number
}

export type TextFitRow = TextFitHit | TextFitInfo

export function textfitScan(): TextFitRow[] {
  const hits: TextFitRow[] = []
  const seen = new Set<string>()
  const vw = window.innerWidth
  const root = document.documentElement
  const snip = (s: string): string => s.replace(/\s+/g, ' ').trim().slice(0, 70)
  const sel = (el: Element): string => {
    const parts: string[] = []
    for (
      let e: Element | null = el, i = 0;
      e && e.nodeType === 1 && e !== document.body && i < 4;
      e = e.parentElement, i++
    ) {
      let p = e.tagName.toLowerCase()
      if (e.id) p += '#' + e.id
      else if (e.classList.length)
        p += '.' + [...e.classList].slice(0, 2).join('.')
      parts.unshift(p)
    }
    return parts.join(' > ')
  }
  const add = (
    kind: TextFitKind,
    el: Element,
    text: string,
    extra?: Partial<TextFitHit>,
  ): void => {
    if (extra && el.closest('.mc-alt'))
      extra.note = 'off-window animation row (aria-hidden alternate)'
    const s = sel(el)
    const t = snip(text)
    const k = kind + '|' + s + '|' + t
    if (seen.has(k)) return
    seen.add(k)
    hits.push(Object.assign({ kind, sel: s, text: t }, extra || {}))
  }
  if (root.scrollWidth > vw)
    add('doc-hscroll', root, 'documentElement', {
      scrollWidth: root.scrollWidth,
      innerWidth: vw,
    })
  if (document.body.scrollWidth > vw)
    add('doc-hscroll', document.body, 'body', {
      scrollWidth: document.body.scrollWidth,
      innerWidth: vw,
    })

  const cache = new Map<Element, CSSStyleDeclaration>()
  const cs = (el: Element): CSSStyleDeclaration => {
    let c = cache.get(el)
    if (!c) {
      c = getComputedStyle(el)
      cache.set(el, c)
    }
    return c
  }
  const effOpacity = (el: Element): number => {
    let o = 1
    for (let e: Element | null = el; e && e.nodeType === 1; e = e.parentElement)
      o *= parseFloat(cs(e).opacity)
    return o
  }
  const isClip = (v: string): boolean => v === 'hidden' || v === 'clip'
  const isScroll = (v: string): boolean => v === 'auto' || v === 'scroll'
  // line-clamp paints its own ellipsis on the last visible line
  const isLineClamp = (c: CSSStyleDeclaration): boolean =>
    c.webkitLineClamp !== '' && c.webkitLineClamp !== 'none'
  const transparent = (c: string): boolean =>
    c === 'rgba(0, 0, 0, 0)' || c === 'transparent'
  const boxed = (el: Element): boolean => {
    const c = cs(el)
    if (!transparent(c.backgroundColor) || c.backgroundImage !== 'none')
      return true
    return ['Top', 'Right', 'Bottom', 'Left'].some(
      (s) =>
        parseFloat(c.getPropertyValue('border-' + s.toLowerCase() + '-width')) >
          0 &&
        c.getPropertyValue('border-' + s.toLowerCase() + '-style') !== 'none',
    )
  }
  let nodes = 0
  const checkedEl = new Set<Element>()
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const text = n.nodeValue
    if (!text || !text.trim()) continue
    const el = n.parentElement
    if (!el || el.closest('script,style,template,noscript')) continue
    const c = cs(el)
    if (c.display === 'none' || c.visibility === 'hidden') continue
    if (effOpacity(el) < 0.05) continue
    const range = document.createRange()
    range.selectNodeContents(n)
    const rects = [...range.getClientRects()].filter(
      (r) => r.width > 0.5 && r.height > 0.5,
    )
    if (!rects.length) continue
    const eb = el.getBoundingClientRect()
    if (eb.width <= 1 || eb.height <= 1) continue // visually hidden (sr-only)
    if (el.closest('.skip')) continue // skip link, off-screen until focused
    // text inside a closed <details> (not its summary) is not shown; Chromium still reports its rects
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
    nodes++
    const union = rects.reduce(
      (u, r) => ({
        l: Math.min(u.l, r.left),
        t: Math.min(u.t, r.top),
        r: Math.max(u.r, r.right),
        b: Math.max(u.b, r.bottom),
      }),
      { l: 1e9, t: 1e9, r: -1e9, b: -1e9 },
    )
    // (a) the element itself clips its text; a line-clamp, or an ellipsis
    // that has only a horizontal overflow to paint on, is a designed
    // truncation, not a clip defect
    if (!checkedEl.has(el)) {
      checkedEl.add(el)
      const cutVertically = el.scrollHeight > el.clientHeight + 1
      if (
        (isClip(c.overflowX) || isClip(c.overflowY)) &&
        !isLineClamp(c) &&
        !(c.textOverflow === 'ellipsis' && !cutVertically) &&
        (el.scrollWidth > el.clientWidth + 1 || cutVertically)
      ) {
        add('clipped-text', el, text, {
          scroll: el.scrollWidth + 'x' + el.scrollHeight,
          client: el.clientWidth + 'x' + el.clientHeight,
          overflow: c.overflowX + '/' + c.overflowY,
        })
      }
    }
    // ancestors: clipping boxes, scrollers, nearest boxed ancestor. The
    // scroller branch runs before `boxed`, so a boxed scroller never becomes
    // the nearest box. `cu` is the union intersected with every clip
    // ancestor's inner box: the part the user can actually see.
    let scrolled = false
    let ellipsized = false
    let clamped = false
    let boxEl: Element | null = null
    let boxScrolled = false
    const cu = { l: union.l, t: union.t, r: union.r, b: union.b }
    for (let a: Element | null = el; a && a !== root; a = a.parentElement) {
      if (a === document.body) break
      const ac = cs(a)
      const ox = ac.overflowX
      const oy = ac.overflowY
      const ab = a.getBoundingClientRect()
      // an ellipsized clip in the chain between the text and the candidate
      // is a designed truncation (divergence #3 in the header): it paints
      // the ellipsis where this branch would report a cut.
      if (ac.textOverflow === 'ellipsis' && (isClip(ox) || isClip(oy)))
        ellipsized = true
      if (isLineClamp(ac) && (isClip(ox) || isClip(oy))) clamped = true
      if (isScroll(ox) || isScroll(oy)) {
        if (
          union.l < ab.left - 1 ||
          union.r > ab.right + 1 ||
          union.t < ab.top - 1 ||
          union.b > ab.bottom + 1
        ) {
          // outside a scrollable ancestor: reachable by scrolling, and the
          // hidden part must not be measured against a box either
          scrolled = true
          boxScrolled = true
        }
        continue
      }
      if (isClip(ox) || isClip(oy)) {
        const bl = parseFloat(ac.borderLeftWidth)
        const br = parseFloat(ac.borderRightWidth)
        const bt = parseFloat(ac.borderTopWidth)
        const bb = parseFloat(ac.borderBottomWidth)
        const L = ab.left + bl
        const R = ab.right - br
        const T = ab.top + bt
        const B = ab.bottom - bb
        cu.l = Math.max(cu.l, L)
        cu.t = Math.max(cu.t, T)
        cu.r = Math.min(cu.r, R)
        cu.b = Math.min(cu.b, B)
        const outX = isClip(ox) && (union.l < L - 1 || union.r > R + 1)
        const outY = isClip(oy) && (union.t < T - 1 || union.b > B + 1)
        // Divergence from the concept check (see the header): a run that a
        // scrollable ancestor can bring into view is not clipped. `scrolled`
        // is set by the scroller branch only when the run really sits
        // outside that scroller's box, so inner clips that no scroller can
        // rescue (a card cutting a line) still report.
        if (
          (outX || outY) &&
          a !== el &&
          !scrolled &&
          !clamped &&
          !(ellipsized && outX && !outY)
        )
          add('clipped-by-ancestor', a, text, {
            textEl: sel(el),
            textBox: [union.l, union.t, union.r, union.b]
              .map(Math.round)
              .join(','),
            clipBox: [L, T, R, B].map(Math.round).join(','),
          })
      }
      if (!boxEl && boxed(a)) boxEl = a
    }
    const cuValid = cu.r - cu.l > 1 && cu.b - cu.t > 1
    // (b) text outside nearest bordered/backgrounded ancestor, measured on
    // the visible part
    if (boxEl && !boxScrolled && cuValid) {
      const b = boxEl.getBoundingClientRect()
      if (
        cu.l < b.left - 1 ||
        cu.r > b.right + 1 ||
        cu.t < b.top - 1 ||
        cu.b > b.bottom + 1
      ) {
        add('outside-box', el, text, {
          box: sel(boxEl),
          textBox: [cu.l, cu.t, cu.r, cu.b].map(Math.round).join(','),
          boxRect: [b.left, b.top, b.right, b.bottom].map(Math.round).join(','),
        })
      }
    }
    // (d) outside the viewport horizontally, measured on the visible part
    if (!scrolled && cuValid && (cu.l < -1 || cu.r > vw + 1))
      add('outside-viewport', el, text, {
        textBox: [cu.l, cu.t, cu.r, cu.b].map(Math.round).join(','),
        innerWidth: vw,
      })
  }
  hits.push({ kind: 'info', textNodes: nodes })
  return hits
}
