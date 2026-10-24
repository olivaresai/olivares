// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Orange count and glow scan, ported from the console concept QA check
// concepts-c2/qa/orange.js so the capture gate measures the same surface
// with the same thresholds. Evaluated in the page (at-run.ts and selftest.ts
// pass it to page.evaluate); the port is faithful to the concept check,
// typed and with the dead mask probe removed.
//
// Returns { hits, buttons: { filled, mock }, stops, animations, kfErrors, unparsed, n }:
//   hits       — glow-family styles: orange box/text-shadow, blur/drop-shadow
//                filters (any hue), backdrop-filter, radial gradients (any
//                hue), gradients with an orange stop
//   filled     — orange-filled action controls in the FIRST screen (top <
//                innerHeight); the console rule is AT MOST ONE. Controls inside
//                a `.mc` mock row are reported separately and do not count.
//                State toggles (role=switch/checkbox/radio) are excluded: they
//                report state, they are not competing calls to action.
//   animations — running animations whose keyframes touch shadow/filter; a
//                pulse/glow/halo name counts only when the keyframes were
//                unreadable. Opacity/transform-only keyframes do not move light.
//   kfErrors   — keyframes unreadable mid-transition (the animation channel
//                under-reports while they are)
//   unparsed   — colors the canvas probe could not resolve (counted, never
//                scored orange)
//   n          — computed styles read (measurement volume)

export interface OrangeHit {
  kind: string
  sel: string
  visible: boolean
  value: string
}

export interface OrangeButton {
  sel: string
  text: string
  top: number
}

export interface OrangeStop {
  in: string
  color: string
  offset: string | null
}

export interface OrangeAnimation {
  name: string
  target: string
  props: string[]
  state: string
}

export interface OrangeReport {
  hits: OrangeHit[]
  buttons: { filled: OrangeButton[]; mock: OrangeButton[] }
  stops: OrangeStop[]
  animations: OrangeAnimation[]
  kfErrors: number
  unparsed: number
  n: number
}

export function orangeScan(): OrangeReport {
  const cv = document.createElement('canvas')
  cv.width = cv.height = 1
  const cx = cv.getContext('2d', { willReadFrequently: true })
  if (!cx) throw new Error('2d context unavailable')
  const cache = new Map<string, [number, number, number, number]>()
  const toRGBA = (s: string): [number, number, number, number] => {
    const cached = cache.get(s)
    if (cached) return cached
    let m: RegExpMatchArray | null
    let r: [number, number, number, number]
    if (
      (m = s.match(
        /^rgba?\(\s*([\d.]+)[ ,]+([\d.]+)[ ,]+([\d.]+)(?:\s*[,/]\s*([\d.]+%?))?\s*\)$/,
      ))
    ) {
      const a =
        m[4] === undefined
          ? 1
          : m[4].endsWith('%')
            ? parseFloat(m[4]) / 100
            : parseFloat(m[4])
      r = [+m[1], +m[2], +m[3], a]
    } else if (
      (m = s.match(
        /^color\(srgb\s+([\d.e-]+)\s+([\d.e-]+)\s+([\d.e-]+)(?:\s*\/\s*([\d.]+%?))?\s*\)$/,
      ))
    ) {
      const a =
        m[4] === undefined
          ? 1
          : m[4].endsWith('%')
            ? parseFloat(m[4]) / 100
            : parseFloat(m[4])
      r = [+m[1] * 255, +m[2] * 255, +m[3] * 255, a]
    } else {
      cx.clearRect(0, 0, 1, 1)
      cx.fillStyle = '#010203'
      cx.fillStyle = s
      if (cx.fillStyle === '#010203') unparsed++
      cx.fillRect(0, 0, 1, 1)
      const d = cx.getImageData(0, 0, 1, 1).data
      r = [d[0], d[1], d[2], 1]
    }
    cache.set(s, r)
    return r
  }
  const isOrange = (s: string): boolean => {
    const [r0, g0, b0, a] = toRGBA(s)
    if (a < 0.02) return false
    const r = r0 / 255
    const g = g0 / 255
    const b = b0 / 255
    const mx = Math.max(r, g, b)
    const mn = Math.min(r, g, b)
    const d = mx - mn
    const l = (mx + mn) / 2
    if (d === 0) return false
    const sat = d / (1 - Math.abs(2 * l - 1))
    let h =
      mx === r
        ? ((g - b) / d) % 6
        : mx === g
          ? (b - r) / d + 2
          : (r - g) / d + 4
    h *= 60
    if (h < 0) h += 360
    return h >= 12 && h <= 50 && sat >= 0.45 && l >= 0.2 && l <= 0.8
  }
  const TOK =
    /(rgba?\([^)]*\)|color\([^)]*\)|oklch\([^)]*\)|oklab\([^)]*\)|lab\([^)]*\)|lch\([^)]*\)|hsla?\([^)]*\)|#[0-9a-fA-F]{3,8}\b)/g
  const orangeIn = (v: string): boolean => (v.match(TOK) || []).some(isOrange)
  const sel = (el: Element, ps?: string): string => {
    let p = el.tagName.toLowerCase()
    if (el.id) p += '#' + el.id
    else if (el.classList && el.classList.length)
      p += '.' + [...el.classList].slice(0, 3).join('.')
    const par =
      el.parentElement && el.parentElement !== document.body
        ? el.parentElement
        : null
    if (par) {
      let q = par.tagName.toLowerCase()
      if (par.id) q += '#' + par.id
      else if (par.classList.length)
        q += '.' + [...par.classList].slice(0, 2).join('.')
      p = q + ' > ' + p
    }
    return p + (ps || '')
  }
  const hits: OrangeHit[] = []
  const seen = new Set<string>()
  let n = 0
  let unparsed = 0
  const add = (
    kind: string,
    el: Element,
    ps: string | undefined,
    val: string,
    vis: boolean,
  ): void => {
    const s = sel(el, ps)
    // Visibility is part of the key: a hidden twin visited first must not
    // dedupe away the visible hit behind it, which is the one that blocks.
    const k = kind + '|' + s + (vis ? '|v' : '|h')
    if (seen.has(k)) return
    seen.add(k)
    hits.push({ kind, sel: s, visible: vis, value: val.slice(0, 120) })
  }
  const check = (el: Element, ps?: string): void => {
    const c = getComputedStyle(el, ps || null)
    n++
    if (ps) {
      const ct = c.content
      if (ct === 'none' || ct === 'normal') return
    }
    const vis =
      c.display !== 'none' &&
      c.visibility !== 'hidden' &&
      (ps ? true : el.getClientRects().length > 0)
    const bs = c.boxShadow
    if (bs && bs !== 'none' && orangeIn(bs))
      add('orange box-shadow', el, ps, bs, vis)
    const ts = c.textShadow
    if (ts && ts !== 'none' && orangeIn(ts))
      add('orange text-shadow', el, ps, ts, vis)
    const f = c.filter
    if (f && f !== 'none' && /blur\(|drop-shadow\(/.test(f))
      add(
        orangeIn(f)
          ? 'orange filter blur/drop-shadow'
          : 'filter blur/drop-shadow (any hue)',
        el,
        ps,
        f,
        vis,
      )
    const bf =
      c.backdropFilter ||
      (c as CSSStyleDeclaration & { webkitBackdropFilter?: string })
        .webkitBackdropFilter
    if (bf && bf !== 'none') add('backdrop-filter', el, ps, bf, vis)
    const bi = c.backgroundImage
    if (bi && bi !== 'none') {
      if (/radial-gradient/.test(bi))
        add(
          orangeIn(bi) ? 'orange radial-gradient' : 'radial-gradient (any hue)',
          el,
          ps,
          bi,
          vis,
        )
      if (
        /(linear|conic|repeating-linear|repeating-radial)-gradient/.test(bi) &&
        orangeIn(bi)
      )
        add('gradient with an orange stop', el, ps, bi, vis)
    }
  }
  // html and body carry the ambient glow of a page: read them with the rest
  for (const el of [
    document.documentElement,
    document.body,
    ...document.body.querySelectorAll('*'),
  ]) {
    if (el.closest('svg') && el.tagName.toLowerCase() !== 'svg') continue
    check(el)
    check(el, '::before')
    check(el, '::after')
  }
  const stops: OrangeStop[] = [...document.querySelectorAll('stop')]
    .filter((s) => isOrange(getComputedStyle(s).stopColor))
    .map((s) => ({
      in: s.parentElement ? s.parentElement.id || s.parentElement.tagName : '?',
      color: getComputedStyle(s).stopColor,
      offset: s.getAttribute('offset'),
    }))
  // animations that move light: keyframes touching shadow or filter. A
  // pulse/glow/halo name is the fallback when keyframes are unreadable.
  let kfErrors = 0
  const animations: OrangeAnimation[] = document
    .getAnimations()
    .map((a) => {
      let props: string[] = []
      try {
        const kf =
          a.effect instanceof KeyframeEffect ? a.effect.getKeyframes() : []
        props = [...new Set(kf.flatMap((k) => Object.keys(k)))]
      } catch {
        // keyframes unreadable mid-transition: counted without light-moving props
        kfErrors++
      }
      const anim = a as CSSAnimation & { transitionProperty?: string }
      return {
        name: anim.animationName || anim.transitionProperty || '?',
        target:
          a.effect instanceof KeyframeEffect && a.effect.target
            ? sel(a.effect.target)
            : '?',
        props,
        state: a.playState,
      }
    })
    .filter(
      (a) =>
        a.props.some((p) => /shadow|filter/i.test(p)) ||
        (/pulse|glow|halo/i.test(a.name) && a.props.length === 0),
    )
  // filled orange buttons in the first screen
  const vh = innerHeight
  const filled: OrangeButton[] = []
  const mock: OrangeButton[] = []
  for (const el of document.querySelectorAll(
    'a, button, input[type=submit], [role=button]',
  )) {
    const role = el.getAttribute('role') || ''
    if (role === 'switch' || role === 'checkbox' || role === 'radio') continue
    const c = getComputedStyle(el)
    if (c.display === 'none' || c.visibility === 'hidden') continue
    const r = el.getBoundingClientRect()
    if (r.width < 8 || r.height < 8 || r.bottom <= 0 || r.top >= vh) continue
    if (!isOrange(c.backgroundColor) || toRGBA(c.backgroundColor)[3] < 0.9)
      continue
    const o = {
      sel: sel(el),
      text: (el.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 40),
      top: Math.round(r.top),
    }
    ;(el.closest('#mc, .mc') ? mock : filled).push(o)
  }
  return {
    hits,
    buttons: { filled, mock },
    stops,
    animations,
    kfErrors,
    unparsed,
    n,
  }
}
