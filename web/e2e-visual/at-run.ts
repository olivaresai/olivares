// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// formal assistive-technology (AT) verification, standalone runner.
//
// Run with:  node --experimental-strip-types e2e-visual/at-run.ts <theme> <routes> <suffix>
//   theme  = dark | light            (default dark)
//   routes = all | auth | public | contrast | /a,/b,...   (default all)
//   suffix = report shard name        (default = theme)
//
// Why standalone (not `playwright test`): in this container the Playwright test
// runner is killed the moment it forks a browser worker, and any listening dev
// server (vite preview / static) is reaped. A single Node process that drives the
// Playwright LIBRARY API directly — launching one Chromium as a direct child and
// serving the built dist/ + API fixtures via request interception (no listening
// socket) — works. We keep each invocation a small batch so the process is short.
//
// It verifies the contract NVDA/JAWS/VoiceOver consume (the platform accessibility
// tree) at the API level: axe-core (WCAG 2.0/2.1/2.2 A+AA + best-practice), the
// landmark/heading structure, the live-region inventory, and a contrast measurement
// — in both Warm Terminal themes.
//
// WHAT "CONTRAST MEASUREMENT" MEANS HERE, AND WHAT IT DOES NOT.
// Until this was a hand-written array of 41 token pairings, described in the
// VPAT as "exhaustive". It was not: a composition nobody had written down did not
// exist for the gate, and the gate's green did not say so — a selected trace row
// painted `bg-accent` with `text-muted-foreground` inside measured 1.17:1 and
// passed. There are now two sets, answering different questions:
//   - TOKEN_PAIRS  — the design-token contract, still hand-written on purpose.
//   - at-pairs.ts  — the compositions the console ACTUALLY paints, derived from
//                    its own source (1054 per theme against 43), including the ones
//                    that exist only in a state no walk renders.
// Neither is "every pairing that exists". The derived pass reports every run how
// many className expressions it could not resolve statically; those are covered
// only by axe-core, and only in the states the walk reached.
import {
  existsSync,
  mkdirSync,
  readFileSync,
  statSync,
  writeFileSync,
} from 'node:fs'
import { dirname, extname, join, normalize } from 'node:path'
import {
  AT_CONTENIDO_MS,
  esperaAnimaciones,
  esperaContenido,
} from './at-espera.ts'
import { fileURLToPath } from 'node:url'
import {
  chromium,
  type BrowserContext,
  type ConsoleMessage,
  type Page,
} from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtureFor } from './fixtures.ts'
import { AUTH_ROUTES, PUBLIC_ROUTES } from './routes.ts'
import { derivePairs, type DerivedPair } from './at-pairs.ts'

const HERE = dirname(fileURLToPath(import.meta.url))
const OUT_DIR = join(HERE, '__at__')
const DIST = join(HERE, '..', '..', 'core', 'internal', 'webui', 'dist')
const ORIGIN = 'http://localhost:5210'
const ORIGIN_HOST = 'localhost:5210'

const MIME: Record<string, string> = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.woff2': 'font/woff2',
  '.woff': 'font/woff',
  '.ico': 'image/x-icon',
  '.png': 'image/png',
  '.map': 'application/json; charset=utf-8',
}

type Theme = 'dark' | 'light'

// The DESIGN-TOKEN CONTRACT: pairings the palette promises to hold, whether or not
// any screen composes them yet. Hand-written ON PURPOSE, and NOT the gate's
// coverage — a token that ships broken should fail before the first component
// reaches for it. What the CONSOLE actually paints is derived from its own source
// by `at-pairs.ts`; that set is the coverage, this one is a floor. Before this
// list WAS the whole measurement, which is why a selected trace row at 1.17:1
// passed: nobody had written its pairing down.
const TOKEN_PAIRS: {
  name: string
  kind: 'text' | 'ui'
  fg: string
  bg: string
}[] = [
  {
    name: 'foreground/background',
    kind: 'text',
    fg: '--foreground',
    bg: '--background',
  },
  {
    name: 'foreground/surface',
    kind: 'text',
    fg: '--foreground',
    bg: '--surface',
  },
  {
    name: 'foreground/elevated',
    kind: 'text',
    fg: '--foreground',
    bg: '--elevated',
  },
  { name: 'foreground/muted', kind: 'text', fg: '--foreground', bg: '--muted' },
  {
    name: 'muted-foreground/background',
    kind: 'text',
    fg: '--muted-foreground',
    bg: '--background',
  },
  {
    name: 'muted-foreground/surface',
    kind: 'text',
    fg: '--muted-foreground',
    bg: '--surface',
  },
  {
    name: 'muted-foreground/elevated',
    kind: 'text',
    fg: '--muted-foreground',
    bg: '--elevated',
  },
  {
    name: 'muted-foreground/muted',
    kind: 'text',
    fg: '--muted-foreground',
    bg: '--muted',
  },
  {
    name: 'accent-foreground/accent',
    kind: 'text',
    fg: '--accent-foreground',
    bg: '--accent',
  },
  {
    name: 'accent-foreground/accent-hover',
    kind: 'text',
    fg: '--accent-foreground',
    bg: '--accent-hover',
  },
  {
    name: 'accent-foreground/accent-active',
    kind: 'text',
    fg: '--accent-foreground',
    bg: '--accent-active',
  },
  {
    name: 'accent-text/background',
    kind: 'text',
    fg: '--accent-text',
    bg: '--background',
  },
  {
    name: 'accent-text/surface',
    kind: 'text',
    fg: '--accent-text',
    bg: '--surface',
  },
  //added because the derived pass CANNOT see it: `--accent-text` reaches
  // `--elevated` only across a component boundary (`select.tsx` paints
  // `bg-elevated` in one function and sets `data-[state=checked]:text-accent-text`
  // in another; likewise dialogs and the Button `link` variant). It fails at
  // 4.33:1 in dark and goes on the ledger. The CONTRACT is where a pairing
  // belongs when no single JSX tree composes it.
  {
    name: 'accent-text/elevated',
    kind: 'text',
    fg: '--accent-text',
    bg: '--elevated',
  },
  {
    name: 'accent-soft-foreground/accent-soft',
    kind: 'text',
    fg: '--accent-soft-foreground',
    bg: '--accent-soft',
  },
  {
    name: 'foreground/accent-soft',
    kind: 'text',
    fg: '--foreground',
    bg: '--accent-soft',
  },
  {
    name: 'success/background',
    kind: 'text',
    fg: '--success',
    bg: '--background',
  },
  { name: 'success/surface', kind: 'text', fg: '--success', bg: '--surface' },
  {
    name: 'warning/background',
    kind: 'text',
    fg: '--warning',
    bg: '--background',
  },
  { name: 'warning/surface', kind: 'text', fg: '--warning', bg: '--surface' },
  {
    name: 'danger/background',
    kind: 'text',
    fg: '--danger',
    bg: '--background',
  },
  { name: 'danger/surface', kind: 'text', fg: '--danger', bg: '--surface' },
  { name: 'info/background', kind: 'text', fg: '--info', bg: '--background' },
  { name: 'info/surface', kind: 'text', fg: '--info', bg: '--surface' },
  {
    name: 'success/success-soft',
    kind: 'text',
    fg: '--success',
    bg: '--success-soft',
  },
  {
    name: 'warning/warning-soft',
    kind: 'text',
    fg: '--warning',
    bg: '--warning-soft',
  },
  {
    name: 'danger/danger-soft',
    kind: 'text',
    fg: '--danger',
    bg: '--danger-soft',
  },
  { name: 'info/info-soft', kind: 'text', fg: '--info', bg: '--info-soft' },
  {
    name: 'danger-solid-foreground/danger-solid',
    kind: 'text',
    fg: '--danger-solid-foreground',
    bg: '--danger-solid',
  },
  {
    name: 'border-strong/background',
    kind: 'ui',
    fg: '--border-strong',
    bg: '--background',
  },
  {
    name: 'border-strong/surface',
    kind: 'ui',
    fg: '--border-strong',
    bg: '--surface',
  },
  { name: 'ring/background', kind: 'ui', fg: '--ring', bg: '--background' },
  { name: 'ring/surface', kind: 'ui', fg: '--ring', bg: '--surface' },
  { name: 'accent/background', kind: 'ui', fg: '--accent', bg: '--background' },
  { name: 'accent/surface', kind: 'ui', fg: '--accent', bg: '--surface' },
  //a SELECTION ring drawn in `--accent` over an `--accent-soft` fill. The
  // derived pass covers text (1.4.3) only, so a `ring-*` is not a pairing it can
  // see; without this line the trace row's non-text state indicator would be a
  // number in a comment, in the very change whose thesis is that hand-computed
  // contrast goes stale. Named here so the gate measures it every run.
  // `--accent-strong` es el ÚNICO identificador de color del estado seleccionado/activo
  // (tokens.css:77, con test en tokens.test.ts:83), y los dos componentes que esta rama tocaba
  // dependen de él. Sin estos cuatro pares la derivación mediría todo MENOS el indicador de
  // selección: la clase exacta de verde que no mide lo que dice. Vienen de main, no de aquí.
  {
    name: 'accent-strong/surface',
    kind: 'ui',
    fg: '--accent-strong',
    bg: '--surface',
  },
  {
    name: 'accent-strong/background',
    kind: 'ui',
    fg: '--accent-strong',
    bg: '--background',
  },
  {
    name: 'accent-strong/elevated',
    kind: 'ui',
    fg: '--accent-strong',
    bg: '--elevated',
  },
  {
    name: 'accent-strong/accent-soft',
    kind: 'ui',
    fg: '--accent-strong',
    bg: '--accent-soft',
  },
  {
    name: 'accent/accent-soft',
    kind: 'ui',
    fg: '--accent',
    bg: '--accent-soft',
  },
  {
    name: 'confidence-attributed/background',
    kind: 'ui',
    fg: '--confidence-attributed',
    bg: '--background',
  },
  {
    name: 'confidence-approximate/background',
    kind: 'ui',
    fg: '--confidence-approximate',
    bg: '--background',
  },
  {
    name: 'success-line/surface',
    kind: 'ui',
    fg: '--success-line',
    bg: '--surface',
  },
  {
    name: 'warning-line/surface',
    kind: 'ui',
    fg: '--warning-line',
    bg: '--surface',
  },
  {
    name: 'danger-line/surface',
    kind: 'ui',
    fg: '--danger-line',
    bg: '--surface',
  },
  { name: 'info-line/surface', kind: 'ui', fg: '--info-line', bg: '--surface' },
  {
    name: 'accent-line/surface',
    kind: 'ui',
    fg: '--accent-line',
    bg: '--surface',
  },
]

/**
 * KNOWN, MEASURED, STILL-WATCHED contrast debt (2026-08-07).
 *
 * Deriving the pairings turned up real AA failures the hand-written list never
 * looked at. The ones with an unambiguous fix were fixed in the same change; the
 * ones below need a call this session is not the one to make — either a palette
 * decision (the API-playground method badges are raw Tailwind ramp colours, not
 * brand tokens) or a brand-token boundary that belongs to the other lane
 * (`--accent-text` over `--muted`, a pairing no token list had ever measured).
 *
 * THIS IS NOT A RECLASSIFICATION, AND IT CANNOT ROT. Each entry names ONE origin
 * and ONE pairing with the ratio measured the day it was written, and the gate
 * fails if:
 *   - a failing pairing is NOT in this list      (new debt blocks),
 *   - a listed pairing measures WORSE than recorded  (regression blocks),
 *   - a listed pairing now PASSES                (stale entry blocks — delete it).
 * Everything else stays under the full 4.5:1 rule. The three directions are
 * covered by mutants M4/M5, M7 and M8 respectively.
 */
const RAMP =
  'raw Tailwind ramp; needs a brand-token decision for the method badges'
const ACCENT_TEXT =
  '`--accent-text` over a tinted surface; the token value is coordinated in the orchestrator lane'
// ⛔ POR QUÉ `--accent` NO ALCANZA 3:1 CONTRA LOS FONDOS CLAROS Y AUN ASÍ NO ES UN DEFECTO.
//    Medido el 2026-08-18: `--accent` es `rgb(240,128,0)` —el naranja de marca— y contra
//    `background` da 2,58, contra `surface` 2,69 y contra `accent-soft` 2,37, los tres por debajo del
//    3:1 que WCAG 1.4.11 pide al contraste NO TEXTUAL.
//
//    El umbral aplica a «elementos de interfaz y partes de gráficos necesarias para entender el
//    contenido». Barrido el uso REAL del token en `web/src` antes de escribir esto, que es lo que
//    separa un candidato del contrato de un defecto vivo:
//      · `border-accent` → **0 usos**. El token nunca es un borde.
//      · `bg-accent`     → 7 usos, todos RELLENO con `--accent-foreground` encima: 6,88 ✓.
//      · `ring-accent`   → **1**, `editor.tsx:590`, y va al 30 % de opacidad ACOMPAÑANDO a
//                          `border-accent-strong`, que es el que lleva el contraste del indicador.
//      · `stroke-accent` → **1**, `brand.tsx:37`: es el trazo del LOGOTIPO, exento por 1.4.11
//                          («text that is part of a logo or brand name has no contrast requirement»).
//
//    ⇒ La alternativa a esta anotación sería oscurecer el naranja de marca, y eso no es una decisión
//      de accesibilidad: es una decisión de marca, y no es de esta sesión. Lo que sí es de esta
//      sesión es que la cifra quede escrita con su medida y se RE-MIDA en cada corrida — si algún día
//      alguien pone `border-accent`, el emparejamiento deja de ser teórico y esta entrada tendrá que
//      defenderse con otro argumento.
const ACCENT_FILL =
  '`--accent` es relleno de marca, no elemento de interfaz: 0 bordes, el único anillo va al 30 % junto a `border-accent-strong`, y el único trazo es el logotipo (WCAG 1.4.11 exime logotipos)'
const CONTRAST_DEBT: {
  origin: string
  pair: string
  theme: Theme
  ratio: number
  why: string
}[] = [
  {
    origin: 'tokens.css',
    pair: 'accent/background',
    theme: 'light',
    ratio: 2.58,
    why: ACCENT_FILL,
  },
  {
    origin: 'tokens.css',
    pair: 'accent/surface',
    theme: 'light',
    ratio: 2.69,
    why: ACCENT_FILL,
  },
  {
    origin: 'tokens.css',
    pair: 'accent/accent-soft',
    theme: 'light',
    ratio: 2.37,
    why: ACCENT_FILL,
  },
  // The api-playground badges: SIX HTTP-method badges plus the BETA marker on an
  // endpoint tag group (`endpoint-tree.tsx:105`) — same ramp, but "choose brand
  // tokens for the methods" does not reach the beta marker, so do not read this
  // as one fix. The `-600`/`-400` Tailwind ramp is off-brand to begin with.
  {
    origin: 'web/src/features/api-playground/request-panel.tsx:415',
    pair: 'red-400 on red-500/15',
    theme: 'dark',
    ratio: 4.48,
    why: RAMP,
  },
  {
    origin: 'web/src/features/api-playground/request-panel.tsx:415',
    pair: 'emerald-600 on emerald-500/15',
    theme: 'light',
    ratio: 3.05,
    why: RAMP,
  },
  {
    origin: 'web/src/features/api-playground/request-panel.tsx:415',
    pair: 'blue-600 on blue-500/15',
    theme: 'light',
    ratio: 4.22,
    why: RAMP,
  },
  {
    origin: 'web/src/features/api-playground/request-panel.tsx:415',
    pair: 'amber-600 on amber-500/15',
    theme: 'light',
    ratio: 2.74,
    why: RAMP,
  },
  {
    origin: 'web/src/features/api-playground/request-panel.tsx:415',
    pair: 'orange-600 on orange-500/15',
    theme: 'light',
    ratio: 2.94,
    why: RAMP,
  },
  {
    origin: 'web/src/features/api-playground/request-panel.tsx:415',
    pair: 'red-600 on red-500/15',
    theme: 'light',
    ratio: 3.7,
    why: RAMP,
  },
  {
    origin: 'web/src/features/api-playground/endpoint-tree.tsx:105',
    pair: 'amber-600 on amber-500/15',
    theme: 'light',
    ratio: 2.65,
    why: RAMP,
  },
  {
    origin: 'tokens.css',
    pair: 'accent-text/elevated',
    theme: 'dark',
    ratio: 4.33,
    why: ACCENT_TEXT,
  },
  // `--accent-text` is AA on `--background` and `--surface` — both in the token
  // contract — but not on `--muted`/`--accent-soft`/`--elevated`, which nobody
  // had ever paired.
]

async function serve(context: BrowserContext) {
  await context.route('**/*', async (route) => {
    const u = new URL(route.request().url())
    if (u.host !== ORIGIN_HOST) return route.fulfill({ status: 204, body: '' })
    const pathname = decodeURIComponent(u.pathname)
    const safe = normalize(pathname).replace(/^(\.\.[/\\])+/, '')
    let file = join(DIST, safe === '/' ? 'index.html' : safe)
    if (!existsSync(file) || statSync(file).isDirectory()) {
      if (safe.startsWith('/assets/'))
        return route.fulfill({ status: 404, body: 'not found' })
      file = join(DIST, 'index.html')
    }
    return route.fulfill({
      status: 200,
      headers: {
        'content-type': MIME[extname(file)] ?? 'application/octet-stream',
      },
      body: readFileSync(file),
    })
  })
  await context.route('**/v1/**', async (route) => {
    const p = new URL(route.request().url()).pathname
    if (p.endsWith('/stream')) {
      return route.fulfill({
        status: 200,
        contentType: 'text/event-stream',
        body: ': connected\n\n',
      })
    }
    const fx = fixtureFor(p)
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(fx ?? { items: [], has_more: false }),
    })
  })
}

async function structuralSummary(page: Page) {
  return page.evaluate(() => {
    const txt = (el: Element) =>
      (el.getAttribute('aria-label') || el.textContent || '')
        .trim()
        .slice(0, 80)
    const landmarkSel =
      'main,[role=main],header,[role=banner],nav,[role=navigation],aside,[role=complementary],[role=contentinfo],footer,[role=region],[role=search]'
    const landmarks = Array.from(document.querySelectorAll(landmarkSel)).map(
      (el) => {
        const tag = el.tagName.toLowerCase()
        const role =
          el.getAttribute('role') ||
          (
            {
              main: 'main',
              header: 'banner',
              nav: 'navigation',
              aside: 'complementary',
              footer: 'contentinfo',
            } as Record<string, string>
          )[tag] ||
          tag
        const lb = el.getAttribute('aria-labelledby')
        return {
          role,
          name:
            el.getAttribute('aria-label') ||
            (lb ? document.getElementById(lb)?.textContent?.trim() || '' : ''),
        }
      },
    )
    const headings = Array.from(
      document.querySelectorAll('h1,h2,h3,h4,h5,h6,[role=heading]'),
    ).map((el) => {
      const aria = el.getAttribute('aria-level')
      const m = el.tagName.match(/^H(\d)$/)
      return {
        level: aria ? Number(aria) : m ? Number(m[1]) : 0,
        text: txt(el),
      }
    })
    const liveRegions = {
      ariaLive: document.querySelectorAll('[aria-live]').length,
      status: document.querySelectorAll('[role=status]').length,
      alert: document.querySelectorAll('[role=alert]').length,
      log: document.querySelectorAll('[role=log]').length,
      output: document.querySelectorAll('output').length,
    }
    return { title: document.title, landmarks, headings, liveRegions }
  })
}

function analyzeHeadings(headings: { level: number; text: string }[]) {
  const levels = headings.map((h) => h.level).filter((l) => l > 0)
  const h1Count = levels.filter((l) => l === 1).length
  const skips: { from: number; to: number }[] = []
  let prev = 0
  for (const l of levels) {
    if (prev && l > prev + 1) skips.push({ from: prev, to: l })
    prev = l
  }
  return { h1Count, headingSkips: skips }
}

type ContrastRow = {
  name: string
  kind: 'text' | 'ui'
  source: 'token' | 'derived'
  fg: string
  bg: string
  rgbFg: string
  rgbBg: string
  ratio: number
  threshold: number
  pass: boolean
  origin: string
  /** set when the pairing could NOT be measured — never counted as a pass */
  unmeasured: string
}

/**
 * Resolve and measure both sets IN THE PAGE, against the shipped stylesheet.
 *
 * Token pairings are read straight off the custom properties. Derived pairings
 * arrive as UTILITY CLASSES and are resolved by finding each class's own rule in
 * the loaded CSS and replaying its declarations onto a probe — so `bg-muted/60`,
 * `hover:bg-accent-hover` and any `color-mix()` value measure exactly what the
 * browser would paint, including the variants no probe can trigger by itself.
 * A class whose rule is not in the bundle is reported UNMEASURED, never passed.
 */
async function measureContrast(page: Page, derived: DerivedPair[]) {
  return page.evaluate(
    ({ TOKENS, DERIVED }) => {
      // The probe hangs inside a parent pinned to a colour nothing in this
      // palette uses. A `var()` naming a property that does not exist is
      // invalid-at-computed-value-time: the declaration is dropped and `color`
      // INHERITS. With the probe on <body> it inherited `--foreground`, which
      // parses, measures ~14:1 and PASSES — so deleting a token from the palette
      // came back green (measured by the review panel, both arms). Under the
      // sentinel parent that fallback is detectable.
      const SENTINEL_INHERIT = 'rgb(1, 2, 3)'
      const probeParent = document.createElement('div')
      probeParent.style.position = 'fixed'
      probeParent.style.left = '-9999px'
      probeParent.style.color = SENTINEL_INHERIT
      const probe = document.createElement('div')
      probeParent.appendChild(probe)
      document.body.appendChild(probeParent)
      const canvas = document.createElement('canvas')
      canvas.width = canvas.height = 1
      const ctx = canvas.getContext('2d')!
      const SENTINEL = '#123456'
      type RGBA = [number, number, number, number]

      /** Rasterise any CSS colour value (rgb, oklab, color-mix, var()) to RGBA.
       *  Returns null when the value does not parse — an unparseable colour must
       *  not silently reuse the previous fillStyle and score as a measurement. */
      // ⛔ AQUI HUBO UN CANARIO CONTRA LA CONGELACION DE LA SONDA — cada 32
      // lecturas pedia un literal conocido y, si no casaba, recreaba la sonda.
      // LO RETIRO, y con una medida, no con una impresion: en la corrida de
      // control salto en las 80 comprobaciones de 2 572 lecturas —es decir,
      // SIEMPRE— y recrear la sonda rompio el bucle que iba bien: el gate paso
      // de 9 fallos a 1 239. El detector es la misma lectura poco fiable que
      // esta clase de defecto exhibe, asi que un canario construido sobre ella
      // no distingue una sonda congelada de una sana; y su cura, recrear, si
      // hace dano medible. Un control que no puede acertar y ademas estropea lo
      // que vigila es peor que no tenerlo.
      //
      // Queda pendiente de verdad: si el runner repite el `fg==bg` en las 1 247
      // filas, hara falta un detector que no dependa de leer un color de vuelta
      // de la sonda. No lo tengo hoy y no lo finjo.
      function toRGBA(
        cssValue: string,
      ): { rgba: RGBA; computed: string } | null {
        probe.style.color = ''
        probe.style.color = cssValue
        const computed = getComputedStyle(probe).color
        // Fell through to the sentinel => the value never applied at all.
        if (computed.replace(/\s/g, '') === 'rgb(1,2,3)') return null
        ctx.fillStyle = SENTINEL
        ctx.fillStyle = computed
        if (
          ctx.fillStyle === SENTINEL &&
          computed.replace(/\s/g, '') !== 'rgb(18,52,86)'
        )
          return null
        ctx.clearRect(0, 0, 1, 1)
        ctx.fillRect(0, 0, 1, 1)
        const d = ctx.getImageData(0, 0, 1, 1).data
        return { rgba: [d[0], d[1], d[2], d[3] / 255], computed }
      }

      function over(fore: RGBA, back: RGBA): RGBA {
        const a = fore[3]
        if (a >= 1) return fore
        return [
          fore[0] * a + back[0] * (1 - a),
          fore[1] * a + back[1] * (1 - a),
          fore[2] * a + back[2] * (1 - a),
          a + back[3] * (1 - a),
        ]
      }
      function lum([r, g, b]: number[]) {
        const f = (c: number) => {
          const s = c / 255
          return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4)
        }
        return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b)
      }
      function ratioOf(fg: number[], bg: number[]) {
        const L1 = lum(fg)
        const L2 = lum(bg)
        return (Math.max(L1, L2) + 0.05) / (Math.min(L1, L2) + 0.05)
      }

      // ---- class -> declarations, harvested from the SHIPPED stylesheet ----
      const wanted = new Set<string>()
      for (const p of DERIVED) {
        if (p.fgClass) wanted.add(p.fgClass)
        wanted.add(p.bgClass)
        if (p.underClass) wanted.add(p.underClass)
      }
      const escaped = new Map<string, string>()
      for (const c of wanted) escaped.set(c, CSS.escape(c))
      const decls = new Map<string, { color?: string; background?: string }>()

      // `.bg-accent` is a prefix of `.bg-accent-soft`, and `.text-muted-foreground`
      // of `.text-muted-foreground\/80`: the character after the class must END the
      // identifier, and a BACKSLASH CONTINUES it. Miss that and every token silently
      // measures at its neighbour's alpha — which is how a whole console's worth of
      // `muted-foreground` first read as 0.8 opaque and failed by ~0.2.
      const selectorHas = (sel: string, esc: string) => {
        const needle = '.' + esc
        let i = sel.indexOf(needle)
        while (i !== -1) {
          const after = sel[i + needle.length]
          if (after === undefined || !/[\w\\-]/.test(after)) return true
          i = sel.indexOf(needle, i + 1)
        }
        return false
      }
      const visitRules = (rules: CSSRuleList) => {
        for (let i = 0; i < rules.length; i++) {
          const rule = rules[i] as CSSRule & {
            cssRules?: CSSRuleList
            selectorText?: string
            style?: CSSStyleDeclaration
          }
          if (rule.cssRules) visitRules(rule.cssRules)
          if (typeof rule.selectorText !== 'string' || !rule.style) continue
          const color = rule.style.getPropertyValue('color')
          const background = rule.style.getPropertyValue('background-color')
          if (!color && !background) continue
          for (const [cls, esc] of escaped) {
            if (!selectorHas(rule.selectorText, esc)) continue
            const cur = decls.get(cls) ?? {}
            if (color) cur.color = color
            if (background) cur.background = background
            decls.set(cls, cur)
          }
        }
      }
      for (const sheet of Array.from(document.styleSheets)) {
        try {
          visitRules(sheet.cssRules)
        } catch {
          /* cross-origin sheet: nothing of ours */
        }
      }

      const rows: {
        name: string
        kind: 'text' | 'ui'
        source: 'token' | 'derived'
        fg: string
        bg: string
        rgbFg: string
        rgbBg: string
        ratio: number
        threshold: number
        pass: boolean
        origin: string
        unmeasured: string
      }[] = []

      // ⛔ LA ENTRADA SE TESTIFICA ANTES DE MEDIR, NO DESPUES, y esto no es
      // orden: es la unica posicion en la que la lectura vale. Medido en esta
      // caja, con la MISMA sonda y el mismo codigo: leido DESPUES de los bucles,
      // `var(--foreground)` y `var(--background)` devuelven los dos el mismo
      // valor —y segun como se lea sale `oklab(0.9848 …)`, `rgb(250,250,249)` o
      // el centinela `rgb(1,2,3)`—, mientras que las 47 filas de token que el
      // bucle produce ANTES salen exactas y sin arrastre. Una guarda que mide su
      // entrada cuando ya ha terminado de medir no comprueba la entrada: mide
      // los restos. Aqui, antes de la primera fila, es donde el propio bucle
      // demuestra que la lectura es fiel.
      const rootStyle = getComputedStyle(document.documentElement)
      const themeProbe = {
        rootFg: rootStyle.getPropertyValue('--foreground').trim(),
        rootBg: rootStyle.getPropertyValue('--background').trim(),
        sheets: document.styleSheets.length,
      }

      // ---- 1. the design-token contract ----
      for (const p of TOKENS) {
        const fg = toRGBA(`var(${p.fg})`)
        const bg = toRGBA(`var(${p.bg})`)
        const threshold = p.kind === 'text' ? 4.5 : 3
        if (!fg || !bg) {
          rows.push({
            name: p.name,
            kind: p.kind,
            source: 'token',
            fg: p.fg,
            bg: p.bg,
            rgbFg: fg?.computed ?? '',
            rgbBg: bg?.computed ?? '',
            ratio: 0,
            threshold,
            pass: false,
            origin: 'tokens.css',
            unmeasured: 'token did not resolve to a colour',
          })
          continue
        }
        const ratio = ratioOf(fg.rgba, bg.rgba)
        rows.push({
          name: p.name,
          kind: p.kind,
          source: 'token',
          fg: p.fg,
          bg: p.bg,
          rgbFg: fg.computed,
          rgbBg: bg.computed,
          ratio: Math.round(ratio * 100) / 100,
          threshold,
          pass: ratio >= threshold,
          origin: 'tokens.css',
          unmeasured: '',
        })
      }

      // ---- 2. the compositions the console actually paints ----
      const pageBg =
        toRGBA('var(--background)')?.rgba ?? ([255, 255, 255, 1] as RGBA)
      for (const p of DERIVED) {
        const miss: string[] = []
        const fgValue = p.fgInherited
          ? 'var(--foreground)'
          : decls.get(p.fgClass)?.color
        const bgValue = decls.get(p.bgClass)?.background
        if (!fgValue) miss.push(`no color rule for .${p.fgClass}`)
        if (!bgValue) miss.push(`no background rule for .${p.bgClass}`)
        const fg = fgValue ? toRGBA(fgValue) : null
        const bg = bgValue ? toRGBA(bgValue) : null
        if (fgValue && !fg) miss.push(`unparseable colour ${fgValue}`)
        if (bgValue && !bg) miss.push(`unparseable colour ${bgValue}`)
        if (!fg || !bg) {
          rows.push({
            name: p.name,
            kind: 'text',
            source: 'derived',
            fg: p.fgClass || 'inherited',
            bg: p.bgClass,
            rgbFg: '',
            rgbBg: '',
            ratio: 0,
            threshold: 4.5,
            pass: false,
            origin: p.origin,
            unmeasured: miss.join('; '),
          })
          continue
        }
        const underValue = p.underClass
          ? decls.get(p.underClass)?.background
          : undefined
        const under = underValue ? (toRGBA(underValue)?.rgba ?? pageBg) : pageBg
        const bgSolid = over(bg.rgba, over(under, pageBg))
        const fgSolid = over(fg.rgba, bgSolid)
        const ratio = ratioOf(fgSolid, bgSolid)
        rows.push({
          name: p.name,
          kind: 'text',
          source: 'derived',
          fg: p.fgClass || 'inherited',
          bg: p.bgClass,
          rgbFg: fg.computed,
          rgbBg: bg.computed,
          ratio: Math.round(ratio * 100) / 100,
          threshold: 4.5,
          pass: ratio >= 4.5,
          origin: p.origin,
          unmeasured: '',
        })
      }
      // ⛔ LA ENTRADA SE TESTIFICA ANTES DE EMITIR VEREDICTO. Un contraste medido
      // sobre una hoja que no se aplicó no es «malo»: es NO MEDIDO, y el gate no
      probeParent.remove()
      return {
        rows,
        classesResolved: decls.size,
        classesWanted: wanted.size,
        themeProbe,
      }
    },
    { TOKENS: TOKEN_PAIRS, DERIVED: derived },
  )
}

// The React error boundary's own title (`lib/i18n/locales/en/errors.json`). A
// crashed view renders it inside a valid sr-only <h1>, with no axe violation and
// no heading skip — so it scores h1=1 / skips=0 / axeBlock=0, IDENTICALLY to a
// view that rendered its content. Measured on an unmodified tree, 2026-08-07:
// 13 of the 54 walked routes are error boundaries, and every one of them was
// being counted as clean evidence, here and in the VPAT.
const CRASH_BOUNDARY_TITLE = 'This view crashed'

async function auditRoute(
  page: Page,
  route: string,
  theme: Theme,
  authed: boolean,
) {
  let rendered = true
  // ⛔ POR QUÉ SE CAPTURA EL ERROR Y NO SÓLO EL VEREDICTO. Este arnés sabía decir «CRASHED» y no
  // sabía decir POR QUÉ, así que cerrar una ruta consistía en ADIVINAR qué fixture le faltaba,
  // probarla, y volver a correr los seis minutos. Diez rutas se cerraron así. Las que quedaron no
  // cedían a la adivinanza, que es justo cuando se ve que el instrumento estaba incompleto: un
  // veredicto sin su causa obliga a buscar por fuerza bruta lo que el navegador ya sabe. React
  // imprime en `pageerror` el nombre del campo que se leyó de `undefined`.
  const lanzados: string[] = []
  const onError = (e: Error) =>
    lanzados.push(`throw: ${e.message.slice(0, 300)}`)
  // ⚠ Y NO BASTA CON `pageerror`, medido: con las tres rutas que quedaban NO IMPRIMIÓ NADA. Un
  // error boundary de React es exactamente lo que impide que el throw llegue a `window.onerror`,
  // así que el canal obvio está mudo justo en el único caso que hay que explicar. El error viaja
  // por `console.error`, que es donde React lo deja al capturarlo. Se escuchan los dos: si algún
  // día una ruta muere ANTES de montar el boundary, ése sí sale por `pageerror`.
  const onConsole = (m: ConsoleMessage) => {
    if (m.type() === 'error')
      lanzados.push(`console: ${m.text().slice(0, 300)}`)
  }
  page.on('pageerror', onError)
  page.on('console', onConsole)
  try {
    await page.goto(ORIGIN + route, {
      waitUntil: 'domcontentloaded',
      timeout: 30_000,
    })
  } catch {
    rendered = false
  }
  // ⛔⛔ AQUÍ HABÍA UNA ESPERA FIJA DE 1500 ms, y era la que decidía el veredicto bajo carga.
  //
  //    `goto` vuelve con `domcontentloaded`, así que a los 1500 ms la vista puede no haber
  //    renderizado todavía. Cuando eso pasa, `main` no está o está vacío, y esta ruta alimenta
  //    `sinMain` y `noH1` — que SÍ bloquean. Es decir: **el gate salía rojo por el reloj**, no por
  //    la accesibilidad. Probado por the orchestrator: entre dos corridas del mismo árbol, el rojo se
  //    MUDÓ de `/finops` a `/attestation`. Un veredicto que cambia de sujeto sin que cambie el
  //    sujeto no está midiendo el sujeto.
  //
  //    Se espera por CONTENIDO —un encabezado dentro de `main`— con tope. Y si el tope vence, NO se
  //    declara nada por reloj: se distinguen dos cosas que se parecen y no son lo mismo.
  //
  //      · `main` EXISTE y tiene contenido, pero sin encabezado ⇒ es un hallazgo REAL (`no-h1`), y
  //        se mide como siempre. Esperar por el encabezado sin esta rama ENMASCARARÍA el defecto
  //        que este arnés existe para encontrar, que sería cambiar un falso rojo por un falso verde.
  //      · `main` AUSENTE o VACÍO ⇒ la vista no llegó a renderizar: **NO PUDE MIRAR**. Ni rojo ni
  //        verde: fuera de las listas que bloquean, y NOMBRADA en el informe. Trece rutas sin medir
  //        no son trece limpias — la misma regla que este fichero ya aplica a `crashed`.
  //    La decisión vive en `./at-espera`, no aquí, y no es estilo: este fichero es un GUION
  //    —llama a `main()` al cargarse—, así que una spec que lo importara arrancaría la corrida
  //    entera. Con el módulo aparte, la regla se puede testificar; y hay UNA sola copia, que es
  //    lo que impide que las dos envejezcan por separado.
  const medible = await esperaContenido(page)
  // Cinturón sobre el `reducedMotion` del contexto: una animación que NO consulte
  // `prefers-reduced-motion` seguiría corriendo, y medirla a medias es el defecto de arriba.
  const animQuietas = await esperaAnimaciones(page)
  const darkApplied = await page.evaluate(() =>
    document.documentElement.classList.contains('dark'),
  )
  const summary = await structuralSummary(page)
  const { h1Count, headingSkips } = analyzeHeadings(summary.headings)
  // No `= []` seed: both the try and the catch below assign, so the initialiser
  // was dead and eslint (`no-useless-assignment`) has been erroring on it.
  let axe: {
    id: string
    impact: string | null
    help: string
    nodes: string[]
  }[]
  try {
    const results = await new AxeBuilder({ page })
      .withTags([
        'wcag2a',
        'wcag2aa',
        'wcag21a',
        'wcag21aa',
        'wcag22aa',
        'best-practice',
      ])
      .analyze()
    axe = results.violations.map((v) => ({
      id: v.id,
      impact: v.impact ?? null,
      help: v.help,
      nodes: v.nodes.map((n) => {
        const data = [...(n.any ?? []), ...(n.all ?? [])]
          .map((c) => c.data)
          .filter(Boolean)
        return `${n.target.join(' ')} :: ${n.html.slice(0, 100)}${data.length ? ' :: ' + JSON.stringify(data) : ''}`
      }),
    }))
  } catch (e) {
    axe = [{ id: 'axe-error', impact: 'serious', help: String(e), nodes: [] }]
  }
  const crashed = summary.headings.some((h) =>
    h.text.includes(CRASH_BOUNDARY_TITLE),
  )
  const blocking = axe.filter(
    (v) => v.impact === 'critical' || v.impact === 'serious',
  )
  // eslint-disable-next-line no-console
  console.log(
    `  ${route.padEnd(15)} [${theme}] dark=${darkApplied} h1=${h1Count} skips=${headingSkips.length} ` +
      `live(s/a/l/live)=${summary.liveRegions.status}/${summary.liveRegions.alert}/${summary.liveRegions.log}/${summary.liveRegions.ariaLive} ` +
      `axeBlock=${blocking.length}${blocking.length ? ' [' + blocking.map((b) => b.id).join(',') + ']' : ''}` +
      (crashed
        ? '  ** CRASHED — this route rendered the error boundary, not its content **'
        : ''),
  )
  page.off('pageerror', onError)
  page.off('console', onConsole)
  // Sólo con veredicto que explicar: un `pageerror` en una ruta que SÍ renderizó es ruido de otra
  // clase —el warning de una biblioteca— y mezclarlo aquí enseñaría a ignorar la línea.
  if (crashed && lanzados.length) {
    // eslint-disable-next-line no-console
    console.log(
      lanzados
        .slice(0, 3)
        .map((m) => `      \u21b3 ${m}`)
        .join('\n'),
    )
  }
  return {
    route,
    theme,
    authed,
    rendered,
    medible,
    // `false` = venció el tope de animaciones: lo que sigue PUEDE describir un fotograma y no la
    // superficie. No bloquea —no es un veredicto— pero viaja en el informe, como `crashed`.
    animQuietas,
    darkApplied,
    ...summary,
    h1Count,
    headingSkips,
    // Cuántas regiones `main` expone la ruta. Sale del inventario de landmarks que este arnés YA
    // recogía, no de axe — ver el bloque `sinMain` más abajo para por qué eso importa.
    mains: summary.landmarks.filter((l) => l.role === 'main').length,
    crashed,
    axe,
  }
}

async function main() {
  const theme = (process.argv[2] as Theme) || 'dark'
  const routesArg = process.argv[3] || 'all'
  const suffix = process.argv[4] || theme
  mkdirSync(OUT_DIR, { recursive: true })

  let routes: string[]
  let doContrast = false
  if (routesArg === 'all') {
    routes = [...AUTH_ROUTES, ...PUBLIC_ROUTES]
    doContrast = true
  } else if (routesArg === 'auth') routes = AUTH_ROUTES
  else if (routesArg === 'public') routes = PUBLIC_ROUTES
  else if (routesArg === 'contrast') {
    routes = []
    doContrast = true
  } else routes = routesArg.split(',')

  const browser = await chromium.launch({
    args: ['--no-sandbox', '--disable-dev-shm-usage'],
  })
  async function makeContext(withSession: boolean) {
    const c = await browser.newContext({
      viewport: { width: 1440, height: 900 },
      baseURL: ORIGIN,
      // ⛔ MIDE LA SUPERFICIE EN REPOSO, NO UN FOTOGRAMA DEL FUNDIDO. Sin esto el barrido es una
      // CARRERA contra `.animate-enter` (240 ms) y el escalonado de la vista Executive (200 ms):
      // `esperaContenido` devuelve en cuanto hay un encabezado dentro de `main` y axe lee el color
      // COMPUESTO. Medido el 2026-08-29 sobre dos corridas con árboles sin diferencia renderizable:
      // `33263227272` dio `axe /dashboards:color-contrast` (#ef867f sobre #44393c = 4,42 — el token
      // al 97,4 % de opacidad) y `33268462913` dio cero.
      //
      // El CSS ya está construido para esto: `.animate-enter` sólo aplica bajo
      // `prefers-reduced-motion: no-preference`, así que con `reduce` los elementos renderizan EN
      // REPOSO, que es el estado que WCAG juzga y el único que un veredicto bloqueante puede citar.
      //
      // ⚠ Lo que esto RETIRA, dicho en vez de omitido: el gate deja de poder ver un defecto que
      // sólo exista MIENTRAS la animación corre. Es lo correcto —un fotograma de 240 ms no es una
      // superficie— pero es cobertura que se quita, no que se gana.
      reducedMotion: 'reduce',
    })
    await c.addInitScript(
      ({ t, sess }: { t: string; sess: boolean }) => {
        localStorage.setItem('olivares.theme', t)
        localStorage.setItem('olivares.lang', 'en')
        if (sess) {
          localStorage.setItem(
            'olivares.session',
            JSON.stringify({
              state: {
                token: 'olvs_demo',
                sessionId: 's1',
                expiresAt: '2030-01-01T00:00:00Z',
              },
              version: 0,
            }),
          )
          localStorage.setItem(
            'olivares.tenant',
            JSON.stringify({ state: { activeTenant: 't-demo' }, version: 0 }),
          )
        }
      },
      { t: theme, sess: withSession },
    )
    await serve(c)
    return c
  }
  const authedContext = await makeContext(true)
  const page = await authedContext.newPage()

  // eslint-disable-next-line no-console
  console.log(
    `\n=== AT run: theme=${theme} routes=${routes.length} contrast=${doContrast} ===`,
  )
  const routeResults = []
  let anonPage: Page | null = null
  for (const route of routes) {
    const authed = !PUBLIC_ROUTES.includes(route)
    if (authed) {
      routeResults.push(await auditRoute(page, route, theme, authed))
    } else {
      if (!anonPage) anonPage = await (await makeContext(false)).newPage()
      routeResults.push(await auditRoute(anonPage, route, theme, authed))
    }
  }

  let contrast: ContrastRow[] = []
  let derivation = {
    pairs: 0,
    unresolved: 0,
    files: 0,
    elements: 0,
    nonTextForegrounds: 0,
    unmeasured: 0,
  }
  if (doContrast) {
    // Derive the compositions from the console's own source, FOR THIS THEME:
    // `dark:` utilities only paint in the dark one, and there they shadow their
    // own base, so each theme gets the set it actually renders.
    const src = derivePairs([join(HERE, '..', 'src')], theme)
    await page.goto(ORIGIN + '/', { waitUntil: 'domcontentloaded' })
    await page.waitForTimeout(600)
    const measured = await measureContrast(page, src.pairs)
    // ⛔ NO STYLESHEET APPLIED — la tercera respuesta, antes de cualquier veredicto.
    //
    // Sin esto, una hoja que no se aplica no da un gate rojo: da MIL DOSCIENTAS
    // filas cuyo fg y bg salen del mismo sitio, y el número grande se lee como
    // trabajo hecho. Las dos condiciones se comprueban por separado porque
    // fallan por separado:
    //
    //   1. los tokens no resuelven en `:root`  -> la hoja no está
    //   2. resuelven y dan EL MISMO color      -> están, pero no en la cascada
    //      del tema: ningún tema define `--foreground` y `--background` iguales
    //      (en claro son #28282b sobre #fafaf9, en oscuro al revés), así que esa
    //      igualdad no es un contraste malo, es una medida que no vale.
    //
    // Sale 3 y no 2: en este guion 2 significa «el gate SUSPENDE», que es un
    // veredicto, y aquí justamente no lo hay. Y sale por `process.exit`, no por
    // `return`: `main()` está cableada como `main().then(() => process.exit(0))`,
    // así que un retorno temprano saldría CERO y pintaría de verde la ausencia
    // de la entrada — que es el fallo exacto que esta guarda existe para cortar.
    const tp = measured.themeProbe
    const sinHoja = !tp.rootFg || !tp.rootBg
    // ⛔ LA GUARDA JUZGA LO QUE `:root` DECLARA, NO LO QUE LA SONDA RESUELVE, y
    // esto es una renuncia deliberada que dejo escrita porque me costo la tarde.
    // Intente basarla en `toRGBA('var(--foreground)')` frente a
    // `toRGBA('var(--background)')` y esa pareja devuelve el MISMO valor se lea
    // donde se lea —antes de los bucles, despues, con la sonda gastada, con una
    // recien creada, con reflujo forzado— mientras que la primera fila que el
    // bucle produce acto seguido sale exacta (`#fafaf9` sobre `#28282b`). No he
    // explicado esa asimetria y no la afirmo curada: la esquivo. La lectura de
    // `:root` por `getPropertyValue` ha dado el valor correcto en TODAS las
    // corridas de hoy, asi que la guarda se apoya en la unica medida que no ha
    // fallado nunca. Cubre las dos formas de fallo que se han visto de verdad:
    // la hoja ausente, y los dos tokens declarados con el MISMO color, que es
    // exactamente lo que reprodujo el mutante.
    const sinCascada = !!tp.rootFg && tp.rootFg === tp.rootBg
    // ⛔ TERCERA PUERTA, Y ES LA QUE CAZA EL CASO DEL RUNNER. Las dos de arriba
    // miran lo que `:root` DECLARA, y hay un fallo que no se ve ahi: el arbol
    // recien construido declara los dos tokens bien y aun asi la sonda devuelve
    // el `--foreground` para AMBAS lecturas, de modo que las 1 247 parejas salen
    // con fg == bg y ratio 1. Reproducido aqui el 2026-08-31 reconstruyendo el
    // bundle: contra el `dist` commiteado el gate da 9 fallos, y contra el
    // reconstruido —CSS byte a byte identico salvo un `12`->`7`— da 1 247 de
    // 1 247, en los DOS temas, mientras axe no ve ni una violacion. Es el mismo
    // sintoma que el runner reporta.
    //
    // Esta puerta no lee ningun color de vuelta de la sonda: mira el RESULTADO.
    // Que casi todas las parejas midan el mismo color en primer plano y en fondo
    // no es un contraste malo — es una medida que no vale, y decirlo asi es la
    // diferencia entre un rojo que se investiga y 1 247 rojos que se ignoran.
    const iguales = measured.rows.filter(
      (r) => !!r.rgbFg && r.rgbFg === r.rgbBg,
    ).length
    const sinMedida =
      measured.rows.length > 0 && iguales / measured.rows.length >= 0.9
    if (sinHoja || sinCascada || sinMedida) {
      // eslint-disable-next-line no-console
      console.error(
        `NO STYLESHEET APPLIED [${theme}] — no emito veredicto de contraste.\n` +
          `  motivo: ${sinHoja ? 'los tokens no resuelven en :root' : sinCascada ? ':root declara los dos con el MISMO color, que ningún tema produce' : `${iguales} de ${measured.rows.length} parejas miden fg == bg: la sonda no está leyendo el tema`}\n` +
          `  --foreground: ${JSON.stringify(tp.rootFg)}   --background: ${JSON.stringify(tp.rootBg)}\n` +

          `  hojas cargadas: ${tp.sheets}`,
      )
      process.exit(3)
    }
    contrast = measured.rows
    const fails = contrast.filter((c) => !c.pass && !c.unmeasured)
    const unmeasured = contrast.filter((c) => !!c.unmeasured)
    derivation = {
      pairs: src.pairs.length,
      unresolved: src.unresolved.length,
      files: src.filesScanned,
      elements: src.elementsScanned,
      nonTextForegrounds: src.nonTextForegrounds,
      unmeasured: unmeasured.length,
    }
    // eslint-disable-next-line no-console
    console.log(
      `  contrast [${theme}]: ${contrast.length} pairings — ${TOKEN_PAIRS.length} token contract + ` +
        `${src.pairs.length} derived from ${src.filesScanned} source files (${src.elementsScanned} JSX elements); ` +
        `${fails.length} fail, ${unmeasured.length} unmeasured` +
        // El canario se dice SIEMPRE, tambien cuando es 0: un control que solo
        // habla cuando salta no se distingue de un control que no corre.
        '',
    )
    for (const f of fails)
      // eslint-disable-next-line no-console
      console.log(
        `    FAIL [${f.source}] ${f.name}: ${f.ratio} < ${f.threshold} (${f.rgbFg} on ${f.rgbBg}) ${f.origin}`,
      )
    for (const u of unmeasured)
      console.log(
        `    UNMEASURED [${u.source}] ${u.name}: ${u.unmeasured} ${u.origin}`,
      )
    // What the derived pass could NOT look at, printed every run so "derived" is
    // never read as "exhaustive": className expressions no parser resolves, and
    // colour utilities on childless elements (icons/canvases — 1.4.11, not 1.4.3).
    console.log(
      `    residue: ${src.unresolved.length} className expression(s) not statically resolvable ` +
        `(a variable or a helper call) + ${src.nonTextForegrounds} colour utilities on childless ` +
        `elements — covered only by axe-core, in the states the walk reached`,
    )
  }

  writeFileSync(
    join(OUT_DIR, `at-report-${suffix}.json`),
    JSON.stringify(
      {
        theme,
        routes: routeResults,
        crashedRoutes: routeResults
          .filter((r) => r.crashed)
          .map((r) => r.route),
        contrast,
        derivation,
      },
      null,
      2,
    ),
  )
  await browser.close()

  // ---- gate: what blocks a clean "Supports" for AT users ----
  // EXCLUDED by design: the resting decorative borders (border-strong / *-line) are
  // < 3:1 but are not the SOLE identifier of a component/state (fill + label + the
  // >=3:1 focus ring identify controls); ratified as 1.4.11 Supports.
  // The waiver is scoped to the TOKEN contract by construction: it matches names
  // of the form `<token>/<token>`, which only that list produces. A derived
  // composition is named `<fg> on <bg>` and can never be swept into it silently.
  const DECORATIVE = /^(border-strong|.*-line)\//
  const axeBlocking = routeResults.flatMap((r) =>
    !r.medible
      ? []
      : r.axe
          .filter((v) => v.impact === 'critical' || v.impact === 'serious')
          .map((v) => `${r.route}:${v.id}`),
  )
  // A pairing nobody could measure is NOT a pass — the same fail-closed rule the
  // pre-push classifier runs on: "I could not look" is not "it is clean". This was
  // a waiver in the first draft of and the mutation matrix killed it: `bg-card`
  // is not a token of this system, Tailwind emits no rule for it, and the panel it
  // sat on had NO background at all — under the waiver that defect came back green
  // (mutant M6 survived). Being strict costs nothing: the clean tree resolves every
  // class in both themes, and a className that ever defeats the parser is named
  // instead of scored.
  const failed = (c: ContrastRow) => !c.pass

  // ---- the debt ledger, enforced in all three directions ----
  const debtForTheme = CONTRAST_DEBT.filter((d) => d.theme === theme)
  const debtOf = (c: ContrastRow) =>
    debtForTheme.find((d) => d.origin === c.origin && d.pair === c.name)
  const debtRegressed: string[] = []
  const debtStale: string[] = []
  if (doContrast) {
    for (const d of debtForTheme) {
      const row = contrast.find(
        (c) => c.origin === d.origin && c.name === d.pair,
      )
      if (!row) {
        debtStale.push(
          `${d.pair} at ${d.origin} is no longer a pairing at all — delete the ledger entry`,
        )
      } else if (row.pass) {
        debtStale.push(
          `${d.pair} at ${d.origin} now measures ${row.ratio} and PASSES — delete the ledger entry`,
        )
      } else if (row.ratio < d.ratio) {
        debtRegressed.push(
          `${d.pair} at ${d.origin}: ${row.ratio} is WORSE than the recorded ${d.ratio}`,
        )
      }
    }
  }

  const textContrast = contrast
    .filter((c) => failed(c) && c.kind === 'text' && !debtOf(c))
    .map((c) =>
      c.unmeasured
        ? `${c.name} NOT MEASURED — ${c.unmeasured} (${c.origin})`
        : c.source === 'derived'
          ? `${c.name} (${c.origin})`
          : c.name,
    )
  // `c.unmeasured` short-circuits the waiver: the decorative exemption is a
  // judgement about a MEASURED ratio, so it must never absolve a pairing whose
  // ratio nobody obtained. It used to be applied first, on the NAME alone.
  const uiContrast = contrast
    .filter(
      (c) =>
        failed(c) &&
        c.kind === 'ui' &&
        (!!c.unmeasured || (!DECORATIVE.test(c.name) && !debtOf(c))),
    )
    .map((c) =>
      c.unmeasured ? `${c.name} NOT MEASURED — ${c.unmeasured}` : c.name,
    )
  const headingSkips = routeResults
    .filter((r) => r.medible && r.headingSkips.length)
    .map((r) => r.route)
  // ⛔ AQUÍ DECÍA `r.authed && r.h1Count !== 1`, y esa exención era un agujero — retirada el
  //    2026-08-18. Las rutas públicas —`/login`, `/setup`, `/accept-invite`, `/status-page`— son las
  //    ÚNICAS que ve alguien que todavía no es cliente, y estaban excluidas del único chequeo de
  //    encabezado que tiene este arnés. Comprobado antes de quitarla, que es lo que la hace segura:
  //    las tres que el arnés ya visitaba dan `h1Count=1` en los dos temas, así que la exención no
  //    tapaba un incumplimiento — tapaba la POSIBILIDAD de detectarlo.
  const noH1 = routeResults
    .filter((r) => r.medible && r.h1Count !== 1)
    .map((r) => r.route)
  // ⛔⛔ POR QUÉ ESTA COMPROBACIÓN NO SALE DE AXE, que es el hallazgo que la trajo.
  //
  //    Axe **ya lo estaba diciendo**: `at-report-light.json` traía `landmark-one-main` y `region` en
  //    `/login`, `/setup` y `/status-page`, con `landmarks: []` en las dos primeras. No era un defecto
  //    invisible: era un hallazgo **que no costaba nada**. `axeBlocking` filtra por
  //    `critical | serious`, y las dos reglas de landmark son **`moderate`** — así que por
  //    construcción no podían bloquear nunca, por muchas veces que se corriera el arnés.
  //
  //    ⇒ La lección no es «faltaba una regla», es que **un informe que nadie puede suspender es
  //      documentación, no una guarda**. Esto se mide sobre el inventario estructural que el arnés ya
  //      recogía, así que es independiente de la escala de severidad de axe y de que ésta cambie.
  //
  //    Y lo que costó: `<main id="main-content">` vivía SÓLO en `app-layout.tsx`, el layout
  //    autenticado. Las páginas de ERROR sí traían la suya. La estructura de landmarks estaba en todo
  //    el producto menos en su puerta de entrada.
  const sinMain = routeResults
    .filter((r) => r.medible && !r.crashed && r.mains !== 1)
    .map((r) => `${r.route} (main=${r.mains})`)
  // ⛔⛔ LA GUARDA MÁS IMPORTANTE DE ESTE FICHERO, y la que faltaba — 2026-08-18.
  //
  //    Medido: **56 de las 58 rutas renderizaban la pantalla de acceso** y este arnés salía con
  //    `0 blocking issue(s)`. No es una laguna de cobertura: es que el informe entero describía la
  //    MISMA pantalla 56 veces. Un arnés que contesta lo mismo para cualquier entrada no ha medido
  //    nada, y el verde era la prueba de que nadie lo comprobaba.
  //
  //    La causa de aquel día fue un desborde de 32 bits en `setTimeout` que disparaba
  //    `/v1/auth/refresh` en el arranque, más un fixture ausente que respondía sin `token`. Los dos
  //    están arreglados. **Esta guarda no es de esa causa**: cualquier futuro camino que eche la
  //    sesión —un 401, un contrato que cambie, un fixture que caduque— vuelve a producir exactamente
  //    la misma foto, y sin esto volvería a salir verde.
  //
  //    Se juzga por el h1, que es el testigo que ya se recoge, y sólo sobre rutas marcadas `authed`:
  //    una ruta pública QUE ES la pantalla de acceso es correcta ahí.
  const desautenticadas = routeResults
    .filter(
      (r) =>
        r.medible &&
        r.authed &&
        r.headings.some((h) => /^sign in$/i.test(h.text.trim())),
    )
    .map((r) => r.route)
  const blocking = [
    ...desautenticadas.map(
      (x) => `sesion-perdida ${x} — renderizó la pantalla de acceso, no la vista`,
    ),
    ...sinMain.map((x) => `landmark-main ${x}`),
    ...axeBlocking.map((x) => `axe ${x}`),
    ...textContrast.map((x) => `text-contrast ${x}`),
    ...uiContrast.map((x) => `ui-contrast ${x}`),
    ...debtRegressed.map((x) => `contrast-debt-regressed ${x}`),
    ...debtStale.map((x) => `contrast-debt-stale ${x}`),
    ...headingSkips.map((x) => `heading-skip ${x}`),
    ...noH1.map((x) => `no-h1 ${x}`),
  ]
  if (doContrast && debtForTheme.length) {
    console.log(
      `\n  known contrast debt carried [${theme}]: ${debtForTheme.length} pairing(s), each re-measured every run`,
    )
    for (const d of debtForTheme)
      console.log(`    DEBT ${d.pair} @ ${d.ratio} — ${d.origin} — ${d.why}`)
  }
  // A crashed route is NOT clean evidence, and the structural checks cannot tell
  // the difference. Reported, counted and written to the report — deliberately
  // NOT blocking, because the 13 that fail today are a fixture/harness problem
  // this change does not own, and a gate nobody can run is worth nothing. But
  // never again silent: the count is the first thing a reader sees.
  //
  // ⭐ Y ESO YA NO ES UNA SUPOSICIÓN: está MEDIDO (2026-08-18). «Es problema de fixture» era una
  //    lectura razonable que nadie había comprobado — y si estuviera equivocada serían TRECE
  //    pantallas rotas en producción, no trece huecos de arnés. Se contrastó con el único
  //    instrumento que abre un navegador contra un MOTOR DE VERDAD: `task -x console:walk` sobre
  //    un `olivares quickstart` en 127.0.0.1:18080 paseó 55 pantallas con 0 hallazgos, y las TRECE
  //    que aquí revientan salen allí `nav=ok conErr=0 pageErr=0 badReq=0 stalled=0`, una por una:
  //
  //      /adoption /alerting /attestation /automations /compliance /eventing /finops /identity
  //      /killswitch /models /observability /orchestration /recordings
  //
  //    ⇒ El producto renderiza esas rutas; lo que no las satisface son las fixtures de ESTE arnés.
  //    Sigue costando lo que este bloque ya dice —su accesibilidad NO se ha medido, y trece rutas
  //    sin medir no son trece limpias—, pero ahora se sabe DÓNDE está el trabajo, en `fixtures.ts`
  //    y acotado, en vez de sospecharlo.
  //
  //    Y ACOTADO UN PASO MÁS, para que quien lo coja no empiece por el principio: **no es que
  //    falten envoltorios de lista**. El mock de rutas ya devuelve `{ items: [], has_more: false }`
  //    para toda ruta sin fixture, así que un listado sin cubrir renderiza VACÍO y no revienta. Lo
  //    que revienta son los endpoints que **NO devuelven una lista**: una vista que espera un
  //    OBJETO con campos recibe `{items: []}`, lee `undefined` donde hay un contrato, y cae al
  //    error boundary. ⇒ El trabajo es una fixture con la FORMA correcta por ruta, no un
  //    envoltorio genérico más — y por eso son trece piezas y no una.
  // NO PUDE MIRAR, por ruta. Estas no renderizaron a tiempo: `main` ausente o vacío cuando venció
  // la espera por contenido. Quedan FUERA de todo lo que bloquea —su h1, sus landmarks y su axe
  // describirían una pantalla a medio pintar— y se nombran aquí, porque una ruta sin medir no es
  // una ruta limpia y el informe no debe poder leerse como si lo fuera.
  const sinMedir = routeResults.filter((r) => !r.medible).map((r) => r.route)
  if (sinMedir.length) {
    // eslint-disable-next-line no-console
    console.log(
      `\n  ** NO PUDE MIRAR: ${sinMedir.length} of ${routeResults.length} route(s) had no rendered content ` +
        `inside <main> before the ${AT_CONTENIDO_MS} ms content wait expired — they are NOT counted as clean, ` +
        `and they are NOT counted as findings either **\n     ${sinMedir.join(' ')}`,
    )
  }
  const crashedRoutes = routeResults
    .filter((r) => r.crashed)
    .map((r) => r.route)
  if (crashedRoutes.length) {
    // eslint-disable-next-line no-console
    console.log(
      `\n  ** ${crashedRoutes.length} of ${routeResults.length} route(s) rendered the ERROR BOUNDARY, not their content — ` +
        `their h1/heading/axe results describe an error page **\n     ${crashedRoutes.join(' ')}`,
    )
  }
  // eslint-disable-next-line no-console
  console.log(
    `\nwrote __at__/at-report-${suffix}.json — ${blocking.length} blocking issue(s)`,
  )
  if (process.argv.includes('--gate') && blocking.length) {
    // eslint-disable-next-line no-console
    console.error('AT GATE FAILED:\n  ' + blocking.join('\n  '))
    process.exit(2)
  }
}

main().then(
  () => process.exit(0),
  (e) => {
    // eslint-disable-next-line no-console
    console.error('AT run failed:', e)
    process.exit(1)
  },
)
