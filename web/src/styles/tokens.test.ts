// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ADM-CORE-04 — brand-identity drift guard. The console design tokens are
// GENERATED from DTCG sources (web/tokens/*.tokens.json) via Style Dictionary into
// ./tokens.css. This test pins the generated values to the FINAL Olivares brand
// identity (brandv4, 2026-06-10) for BOTH themes, so a token
// edit can never silently shift the brand (brand neutrals #28282b / #fafaf9, the
// single brand orange #f08000 — the FILL in BOTH themes since deepened to
// #b45500 only where it is TEXT on the light canvas — the attributed/approximate
// confidence axis, both AA+ themes). The reference maps below are the verbatim
// brand token set; the semantic axes are preserved, the values re-derived.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

import {
  FALLBACK as CHART_FALLBACK,
  type ChartTheme,
} from '@/components/charts/chart-theme'

// vitest runs with cwd = web/; read the committed generated stylesheet.
const tokensCss = readFileSync('src/styles/tokens.css', 'utf8')

/** Parse a `selector { --a: x; --b: y; }` block into a name->value map. */
function parseBlock(rawCss: string, selector: string): Record<string, string> {
  // Strip /* */ comments first so a selector mentioned in prose (the header
  // documents ".dark{}") can't be mistaken for the real block.
  const css = rawCss.replace(/\/\*[\s\S]*?\*\//g, '')
  // Match the block whose selector starts a line (the real :root/.dark/@theme).
  const head = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const re = new RegExp(`(?:^|\\n)${head}\\s*\\{([\\s\\S]*?)\\n\\}`, 'm')
  const body = css.match(re)?.[1]
  if (!body) throw new Error(`block not found: ${selector}`)
  const out: Record<string, string> = {}
  for (const m of body.matchAll(/--([\w-]+):\s*([^;]+);/g)) {
    out[m[1]] = m[2].replace(/\s+/g, ' ').trim()
  }
  return out
}

// Verbatim brand identity — LIGHT (:root). Canvas = brand light #fafaf9.
const BRAND_LIGHT: Record<string, string> = {
  background: '#fafaf9',
  surface: '#ffffff',
  elevated: '#ffffff',
  overlay: 'rgba(24, 24, 27, 0.45)',
  muted: '#f1f1f0',
  'muted-foreground': '#5c564e',
  foreground: '#28282b',
  border: '#e6e6e4',
  'border-strong': '#d6d6d3',
  ring: '#b45500',
  accent: '#f08000',
  'accent-foreground': '#1a1206',
  'accent-hover': '#d87000',
  'accent-active': '#cc6a00',
  'accent-text': '#a84f00',
  'accent-soft': '#fdeedf',
  'accent-soft-foreground': '#8a3f00',
  'accent-line': '#f2c49e',
  success: '#2f6b2c',
  warning: '#8a5300',
  danger: '#b0201b',
  info: '#1e5a86',
  'danger-solid': '#b0201b',
  'danger-solid-foreground': '#fff8f0',
  'confidence-attributed': '#0a7c77',
  'confidence-approximate': '#5b6470',
  'elev-xs': '0 1px 1px rgba(28, 28, 31, 0.04)',
  'elev-sm':
    '0 1px 2px rgba(28, 28, 31, 0.06), 0 1px 1px rgba(28, 28, 31, 0.04)',
  'elev-md':
    '0 2px 6px -1px rgba(28, 28, 31, 0.08), 0 1px 2px rgba(28, 28, 31, 0.06)',
  'elev-lg':
    '0 8px 24px -6px rgba(28, 28, 31, 0.12), 0 2px 6px -2px rgba(28, 28, 31, 0.08)',
  'elev-xl':
    '0 20px 48px -12px rgba(28, 28, 31, 0.18), 0 4px 12px -4px rgba(28, 28, 31, 0.1)',
  'success-soft': 'color-mix(in oklab, var(--success) 12%, var(--surface))',
  'success-line': 'color-mix(in oklab, var(--success) 32%, var(--surface))',
  'warning-soft': 'color-mix(in oklab, var(--warning) 12%, var(--surface))',
  'warning-line': 'color-mix(in oklab, var(--warning) 32%, var(--surface))',
  'danger-soft': 'color-mix(in oklab, var(--danger) 7%, var(--surface))',
  'danger-line': 'color-mix(in oklab, var(--danger) 32%, var(--surface))',
  'info-soft': 'color-mix(in oklab, var(--info) 12%, var(--surface))',
  'info-line': 'color-mix(in oklab, var(--info) 32%, var(--surface))',
  'accent-strong': '#c26000',
}

// Verbatim brand identity — DARK (.dark). Canvas = brand dark #28282b. No
// soft/line (theme-reactive, defined once in :root).
const BRAND_DARK: Record<string, string> = {
  background: '#28282b',
  surface: '#2f2f33',
  elevated: '#38383d',
  overlay: 'rgba(16, 16, 18, 0.88)',
  muted: '#323237',
  'muted-foreground': '#aaaab3',
  foreground: '#fafaf9',
  border: '#3a3a40',
  'border-strong': '#4c4c54',
  ring: '#f0974a',
  accent: '#f08000',
  'accent-foreground': '#1a1206',
  'accent-hover': '#f89020',
  'accent-active': '#d87000',
  'accent-text': '#f08000',
  'accent-soft': '#34250f',
  'accent-soft-foreground': '#f0974a',
  'accent-line': '#5a3d17',
  success: '#86c58a',
  warning: '#e7b65a',
  danger: '#f7928b',
  info: '#6fb6e6',
  'danger-solid': '#c5362f',
  'danger-solid-foreground': '#fff1ef',
  'confidence-attributed': '#5be0d8',
  'confidence-approximate': '#9aa3b0',
  'elev-xs': 'none',
  'elev-sm': '0 1px 2px rgba(0, 0, 0, 0.5)',
  'elev-md': '0 2px 8px -2px rgba(0, 0, 0, 0.6)',
  'elev-lg':
    '0 10px 30px -8px rgba(0, 0, 0, 0.7), inset 0 1px 0 0 rgba(250, 250, 249, 0.04)',
  'elev-xl':
    '0 28px 64px -16px rgba(0, 0, 0, 0.8), inset 0 1px 0 0 rgba(250, 250, 249, 0.05)',
  // .dark REDEFINE accent-strong desde que el claro dejó de usar la mezcla: la mezcla
  // profundiza sólo sobre una superficie oscura, así que vive donde funciona.
  'accent-strong': 'color-mix(in oklab, var(--accent) 85%, var(--surface))',
}

/** WCAG 2.x relative luminance of an #rrggbb literal. */
/**
 * sRGB <-> OKLab, y la composición alfa — lo mínimo para reproducir lo que axe MIDE, que no es lo
 * que el token DICE. `--danger-soft` es un `color-mix(in oklab, …)`, así que sin esto sólo se puede
 * comparar el token contra sí mismo, y ése fue exactamente el desacuerdo del 2026-08-29: la cuenta
 * token-contra-token daba 4,56 y PASABA mientras axe daba 4,42 y fallaba. Las dos eran correctas;
 * describían instantes distintos.
 */
function hexToRgb(hex: string): [number, number, number] {
  const m = /^#([0-9a-f]{6})$/i.exec(hex.trim())
  if (!m) throw new Error(`not a 6-digit hex colour: ${hex}`)
  return [0, 1, 2].map((i) => parseInt(m[1].slice(i * 2, i * 2 + 2), 16)) as [
    number,
    number,
    number,
  ]
}
const toHex = (rgb: number[]) =>
  '#' +
  rgb
    .map((c) =>
      Math.max(0, Math.min(255, Math.round(c)))
        .toString(16)
        .padStart(2, '0'),
    )
    .join('')
const toLinear = (c: number) => {
  const s = c / 255
  return s <= 0.04045 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4)
}
const fromLinear = (c: number) => {
  const v = c <= 0.0031308 ? c * 12.92 : 1.055 * Math.pow(c, 1 / 2.4) - 0.055
  return v * 255
}
function rgbToOklab(hex: string): [number, number, number] {
  const [r, g, b] = hexToRgb(hex).map(toLinear)
  const l = Math.cbrt(0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b)
  const m = Math.cbrt(0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b)
  const s = Math.cbrt(0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b)
  return [
    0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s,
    1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s,
    0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s,
  ]
}
function oklabToHex([L, A, B]: [number, number, number]): string {
  const l = Math.pow(L + 0.3963377774 * A + 0.2158037573 * B, 3)
  const m = Math.pow(L - 0.1055613458 * A - 0.0638541728 * B, 3)
  const s = Math.pow(L - 0.0894841775 * A - 1.291485548 * B, 3)
  return toHex([
    fromLinear(4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s),
    fromLinear(-1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s),
    fromLinear(-0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s),
  ])
}
/** `color-mix(in oklab, a <pct>%, b)` tal y como lo resuelve el navegador. */
function mixOklab(a: string, pct: number, b: string): string {
  const A = rgbToOklab(a)
  const Bc = rgbToOklab(b)
  const p = pct / 100
  return oklabToHex(
    [0, 1, 2].map((i) => A[i] * p + Bc[i] * (1 - p)) as [
      number,
      number,
      number,
    ],
  )
}
/** Lo que se PINTA cuando el elemento va a `alpha` sobre su fondo. */
function composite(fg: string, bg: string, alpha: number): string {
  const F = hexToRgb(fg)
  const B = hexToRgb(bg)
  return toHex([0, 1, 2].map((i) => F[i] * alpha + B[i] * (1 - alpha)))
}

function luminance(hex: string): number {
  const m = /^#([0-9a-f]{6})$/i.exec(hex.trim())
  if (!m) throw new Error(`not a 6-digit hex colour: ${hex}`)
  const channel = (i: number) => {
    const c = parseInt(m[1].slice(i * 2, i * 2 + 2), 16) / 255
    return c <= 0.04045 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4)
  }
  return 0.2126 * channel(0) + 0.7152 * channel(1) + 0.0722 * channel(2)
}

/**
 * WCAG contrast ratio, RAW.
 *
 * It used to round to 2dp before returning, and the comparisons below then decided pass/fail on
 * the rounded number — which invents a passing window of [4.495, 4.5). Measured: #1a1206 on
 * #d75200 is 4.497789980527061, fails AA, and rounded to 4.50 it passed. The AT runner does the
 * opposite and is right (e2e-visual/at-run.ts:202-207): decide on the raw ratio, round only what
 * is printed. `show()` exists for the message, and nothing else may use it.
 */
function contrast(fg: string, hex: string): number {
  const a = luminance(fg)
  const b = luminance(hex)
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
}

/** 2dp, for human-readable assertion messages only — never for a comparison. */
const show = (ratio: number) =>
  `${(Math.round(ratio * 100) / 100).toFixed(2)}:1`

describe('ADM-CORE-04 — DTCG-generated tokens ≡ brand identity', () => {
  const light = parseBlock(tokensCss, ':root')
  const dark = parseBlock(tokensCss, '.dark')

  //THE DEFECT THIS PINS, and it is not hypothetical. `accent-foreground` is
  // NOT ink on `accent` alone: components/ui/button.tsx renders
  // `bg-accent text-accent-foreground hover:bg-accent-hover active:bg-accent-active`,
  // so the SAME ink lands on all three fills. Moving the light fill to the brand
  // orange without moving its two states with it would have shipped a button whose
  // ink measured 2.91:1 on hover (#1a1206 on the old #9a4800) and 2.29:1 on active
  // (#823c00) — a regression invisible to every value-equality assert above, because
  // each token would still have been "the value we wrote". Only the PAIR is the
  // property. The AT runner (e2e-visual/at-run.ts) measures these same three pairs
  // in a browser; this pins them at the source, where a token edit happens.
  it.each(['accent', 'accent-hover', 'accent-active'])(
    'accent-foreground stays AA readable on %s, in BOTH themes',
    (fill) => {
      for (const [name, theme] of [
        ['light', light],
        ['dark', dark],
      ] as const) {
        const ratio = contrast(theme['accent-foreground'], theme[fill])
        expect(
          ratio,
          `${name}: --accent-foreground ${theme['accent-foreground']} on --${fill} ${theme[fill]} is ${show(ratio)}`,
        ).toBeGreaterThanOrEqual(4.5)
      }
    },
  )

  it('the orange as TEXT clears AA on the light canvas, where the fill does not', () => {
    // The whole reason the token is split: the deepened value is REQUIRED for text, and the
    // fill does not have to meet the text threshold because nothing reads the fill — it reads
    // the ink ON it, pinned above.
    expect(
      contrast(light['accent-text'], light.background),
    ).toBeGreaterThanOrEqual(4.5)
    expect(contrast(light.accent, light.background)).toBeLessThan(4.5)
    expect(
      contrast(dark['accent-text'], dark.background),
    ).toBeGreaterThanOrEqual(4.5)
  })

  it('pins the DECLARED residue: the light fill is below the 3:1 UI boundary', () => {
    // NOT a requirement — a residue, pinned so it cannot be lost. Saying the fill is "exempt"
    // would be false: e2e-visual/at-run.ts:86-87 measures accent/background and accent/surface
    // as kind:'ui' at 3:1, they are outside its DECORATIVE exclusion (:331), and they are the
    // only two pairs of the 41 that this change turns red. That is the arithmetic consequence
    // of order of 2026-08-06 — the brand orange on a near-white canvas is 2.58:1 however
    // it is measured — and it is reported rather than waived. If these numbers ever move, the
    // declaration in sessions and the at:gate expectation move with them.
    expect(contrast(light.accent, light.background)).toBeCloseTo(2.58, 2)
    expect(contrast(light.accent, light.surface)).toBeCloseTo(2.69, 2)
    // Dark is untouched and stays above the UI threshold, which is why only light is declared.
    expect(contrast(dark.accent, dark.background)).toBeGreaterThanOrEqual(3)
  })

  it('the light neutrals stay AA as secondary text on every light surface', () => {
    // Every light surface token, not a sample of them: `elevated` was missing while the title
    // claimed the set. It equals `surface` today, so it changed no number — a coverage claim
    // larger than the coverage run is still worth closing.
    for (const surface of [
      'background',
      'surface',
      'elevated',
      'muted',
    ] as const) {
      const ratio = contrast(light['muted-foreground'], light[surface])
      expect(
        ratio,
        `--muted-foreground ${light['muted-foreground']} on --${surface} ${light[surface]} is ${show(ratio)}`,
      ).toBeGreaterThanOrEqual(4.5)
    }
  })

  it('generated :root block is byte-equivalent to the brand LIGHT identity', () => {
    expect(light).toEqual(BRAND_LIGHT)
  })

  it('generated .dark block is byte-equivalent to the brand DARK identity', () => {
    expect(dark).toEqual(BRAND_DARK)
  })

  // ⛔ LA CUARTA COPIA DEL COLOR, y hasta 2026-08-29 no la miraba NINGÚN test.
  // `--danger` vive en cuatro sitios: la fuente DTCG, el `tokens.css` generado, el
  // mapa BRAND_DARK de arriba y —fuera de los tres— la paleta de respaldo de los
  // gráficos. Un cambio de token movió los dos primeros, el tercero salió rojo en
  // CI (que es su trabajo) y el CUARTO habría derivado EN SILENCIO: sólo se pinta
  // donde no hay motor de layout (jsdom), o sea justo donde ninguna captura ni
  // ningún barrido de contraste lo mira. Medido ese día: 12 de las 13 entradas
  // casaban y la única deriva era la recién introducida.
  //
  // Se compara el OBJETO importado, no el fichero como texto: un regex sobre la
  // fuente que dejara de casar haría pasar este test SIN COMPARAR NADA. Y por eso
  // la primera aserción es de COBERTURA — si alguien añade un campo a ChartTheme
  // sin mapearlo aquí, esto se pone rojo antes de comparar ningún color.
  // ⛔ EL CASO QUE EXPLICA POR QUÉ `a11y` FLIPÓ, y por qué la cuenta token-contra-token no bastaba.
  //
  // El 2026-08-29 el contexto REQUERIDO `a11y` dio veredictos OPUESTOS sobre árboles sin diferencia
  // renderizable: la corrida 33263227272 falló con `axe /dashboards:color-contrast` y la 33268462913
  // salió limpia. El artefacto nombró la insignia `danger` (components/ui/badge.tsx) y sus DOS
  // colores: `#ef867f` sobre `#44393c`, 4,42 contra un umbral de 4,5.
  //
  // Ese `#ef867f` NO es `--danger`: es `--danger` compuesto a ~97,4 %, o sea el elemento fotografiado
  // A MEDIO FUNDIDO (`.animate-enter`, 240 ms). Por eso dos cuentas correctas discrepaban: el token
  // EN REPOSO medía 4,56 y PASABA, y lo pintado medía 4,42 y no. **El valor viejo cumplía AA sólo
  // con la animación al 99,1 %**, así que cualquier muestra temprana salía roja: un umbral rozado,
  // no un gate averiado. La cura del arnés es `reducedMotion: 'reduce'` (e2e-visual/at-run.ts).
  //
  // Este caso fija las dos mitades para que nadie lo vuelva a discutir, y la segunda ES una guarda:
  // si `--danger` vuelve a acercarse al umbral, esto se pone rojo ANTES que el gate del navegador.
  it('the danger badge: 4.42 painted mid-fade, 4.56 at rest — and today it clears both', () => {
    // El porcentaje se LEE del CSS: si la mezcla cambia, este caso cambia con ella.
    const expr = light['danger-soft']
    const mix =
      /color-mix\(in oklab,\s*var\(--danger\)\s*([\d.]+)%,\s*var\(--surface\)\)/.exec(
        expr,
      )
    expect(
      mix,
      `--danger-soft is no longer an oklab mix of --danger over --surface: ${expr}`,
    ).not.toBeNull()
    const pct = Number(mix![1])

    // --- lo MEDIDO, con los colores que axe reportó, no con un modelo nuestro ---
    // ⛔ EL ANCLA HISTÓRICA LLEVA SU PROPIO PORCENTAJE, no el de hoy. `#44393c`
    //    es el fondo que axe midió cuando la mezcla era del 12 %; al bajarla al
    //    7 % (2026-08-31) este caso se reproducía con `pct` vivo y suspendía —
    //    estaría midiendo el pasado con el presente. La mezcla de HOY se juzga
    //    abajo, con `pct` leído del CSS, que es donde tiene que estar.
    const PCT_ENTONCES = 12
    const ANTES = '#f38881' // el valor del token en la corrida roja
    const AXE_FG = '#ef867f' // primer plano que axe midió: ANTES, compuesto
    const AXE_BG = '#44393c' // fondo que axe midió
    expect(
      mixOklab(ANTES, PCT_ENTONCES, dark.surface),
      'the mix must reproduce the background axe reported, or the model is not the subject',
    ).toBe(AXE_BG)
    expect(contrast(AXE_FG, AXE_BG)).toBeCloseTo(4.42, 2) // pintado: FALLA AA
    expect(contrast(ANTES, AXE_BG)).toBeCloseTo(4.56, 2) // en reposo: pasa, por 0,06

    // ⛔ Y EL MODELO DE «PINTADO» NO ES UNA PRUEBA, ES UN SUELO. Medido el
    //    2026-08-31 con la mezcla al 12 %: este modelo daba 4,74 y axe midio
    //    4,38 sobre el mismo badge (`fg #ec857e` sobre `bg #44383b`). 0,36 de
    //    diferencia, y hacia el lado malo: el test AFIRMABA que cruzaba AA
    //    mientras el navegador decia que no. Un modelo que discrepa del
    //    navegador y aun asi certifica es la clase «bateria que certifica el
    //    defecto», y por eso queda dicho aqui: **la autoridad es axe**, que mide
    //    dentro de la pagina; esto de abajo solo comprueba que el token no baja
    //    de un suelo, y su verde NO sustituye a la corrida de `at:gate`.
    //    (El 29-08 se subio `--danger` fiandose de este modelo y el ratio
    //    EMPEORO de 4,42 a 4,38, porque `--danger-soft` toma `--danger` como
    //    ingrediente: aclarar el texto aclara su fondo. La palanca era la
    //    mezcla, y por eso hoy esta al 7 %.)
    // --- la GUARDA: el valor de HOY cruza AA en los DOS instantes ---
    const soft = mixOklab(dark.danger, pct, dark.surface)
    const enReposo = contrast(dark.danger, soft)
    const pintado = contrast(composite(dark.danger, soft, 0.974), soft)
    expect(
      enReposo,
      `--danger ${dark.danger} on --danger-soft ${soft} at rest is ${show(enReposo)}`,
    ).toBeGreaterThanOrEqual(4.5)
    expect(
      pintado,
      `--danger ${dark.danger} painted at 97.4% over ${soft} is ${show(pintado)} — the fade frame that made a11y flip`,
    ).toBeGreaterThanOrEqual(4.5)
  })

  it('the chart fallback palette is the dark token set, field by field', () => {
    const theme = parseBlock(tokensCss, '@theme inline')
    const MAP: Record<Exclude<keyof ChartTheme, 'series'>, string> = {
      text: 'foreground',
      mutedText: 'muted-foreground',
      grid: 'border',
      accent: 'accent',
      success: 'success',
      warning: 'warning',
      danger: 'danger',
      info: 'info',
      teal: 'confidence-attributed',
      slate: 'confidence-approximate',
      surface: 'surface',
      elevated: 'elevated',
      border: 'border',
    }

    // Cobertura antes que valores: ningún campo del tipo se queda sin testigo.
    expect(new Set(Object.keys(CHART_FALLBACK))).toEqual(
      new Set([...Object.keys(MAP), 'series']),
    )

    for (const [field, token] of Object.entries(MAP)) {
      expect(
        CHART_FALLBACK[field as keyof ChartTheme],
        `chart fallback \`${field}\` must equal --${token} in .dark`,
      ).toBe(dark[token])
    }

    // La rampa categórica: cinco semánticos de .dark + el gris de la escala.
    expect(CHART_FALLBACK.series).toEqual([
      dark.accent,
      dark['confidence-attributed'],
      dark.info,
      dark.warning,
      dark.success,
      theme['color-graphite-400'],
    ])
  })

  it('preserves the brand anchors (single orange, ring, confidence axis)', () => {
    //the orange is ONE brand orange split BY ROLE, not two oranges by theme.
    // The FILL is #f08000 in both themes (order, 2026-08-06): the ink sits ON
    // it, so the deepening that AA needs for TEXT buys nothing here and cost 2.18
    // points of contrast (#1a1206 on #f08000 = 6.88:1 vs #fff8f0 on #b45500 = 4.70:1).
    expect(dark.accent).toBe('#f08000') // the brand orange, dark surface
    expect(light.accent).toBe('#f08000') // the SAME brand orange — this is the fix
    expect(light.accent).toBe(dark.accent)
    expect(light['accent-foreground']).toBe(dark['accent-foreground'])
    // The deepening survives exactly where it is load-bearing: the orange as TEXT on
    // the light canvas, where #f08000 is 2.58:1 and fails AA for real.
    expect(light['accent-text']).toBe('#a84f00')
    expect(dark['accent-text']).toBe('#f08000')
    expect(light.ring).toBe('#b45500')
    expect(dark.ring).toBe('#f0974a')
    // The orthogonal cool confidence axis: teal attributed vs slate approximate.
    expect(light['confidence-attributed']).toBe('#0a7c77')
    expect(light['confidence-approximate']).toBe('#5b6470')
    expect(dark['confidence-attributed']).toBe('#5be0d8')
    expect(dark['confidence-approximate']).toBe('#9aa3b0')
  })

  it('anchors both backgrounds to the brand neutrals', () => {
    expect(dark.background).toBe('#28282b')
    expect(light.background).toBe('#fafaf9')
    // The two brand neutrals are the fg/bg pair, inverted per theme.
    expect(dark.foreground).toBe('#fafaf9')
    expect(light.foreground).toBe('#28282b')
  })

  it('keeps derived soft/line as runtime color-mix() in :root only (theme-reactive)', () => {
    expect(light['success-soft']).toContain(
      'color-mix(in oklab, var(--success)',
    )
    // Must NOT be inlined into a static color, and must NOT be duplicated into .dark.
    expect(dark['success-soft']).toBeUndefined()
    expect(dark['info-line']).toBeUndefined()
  })

  //--accent-strong is the SOLE colour identifier of the selected/active
  // state on the session-viewer rows, so SC 1.4.11 (>=3:1) applies to it and the
  // AT gate measures it for real as a BLOCKING pair (`accent-strong/surface`).
  // ⛔ Y LA MEZCLA ES DEL TEMA OSCURO, NO DEL CLARO — corregido al medir el claro por primera
  // vez. `color-mix(… var(--accent) 85%, var(--surface))` mezcla hacia la SUPERFICIE, y las dos
  // superficies van en direcciones opuestas: en oscuro `--surface` es #2f2f33 y la mezcla
  // PROFUNDIZA el naranja; en claro es #ffffff y lo ACLARA, así que resolvía a ~#f29326 —más
  // débil que el propio `--accent`— y medía 2,06 sobre accent-soft, 2,24 sobre background y 2,34
  // sobre surface. Un token que se llama «strong» y cuya definición lo debilita es peor que no
  // tenerlo.
  //
  // ⭐ Lo que esto corrige no es un valor, es una ATRIBUCIÓN. Los números de abajo se tomaron
  // «en chromium» y se escribieron como si valieran para los dos temas, cuando el tema claro NO
  // SE HABÍA EJECUTADO NUNCA (427d34cc7). Siguen siendo ciertos DONDE se midieron, y por eso se
  // conservan enteros en vez de reescribirlos: el error estaba en el alcance, no en la medida.
  // El claro lleva ahora un #c26000 explícito y medido (3,72 sobre la peor superficie).
  //
  // What the gate cannot see, and this pins, are the two bounds of the mix:
  //   - >=81%, the MEASURED floor across all four gated adjacencies (surface,
  //     background, elevated, accent-soft): 81% -> 3.05:1 worst case (light on
  //     accent-soft) and 80% -> 2.99:1, where the gate goes red. Measured in
  //     chromium; we ship 85% (3.30:1) for margin. TWO earlier bounds here
  //     were wrong and both are worth remembering: 80% was derived before the
  //     elevated and accent-soft neighbours existed as pairs, and would have passed
  //     a red value; 82% came from a sweep that only tried 80/82/85 and never tried
  //     81, so it was asserted as "the floor" without having been measured.
  //   - <100%, because in LIGHT --ring == --accent == #b45500 exactly, so a flat
  //     accent would make selection pixel-identical to the focus outline
  //     (src/index.css:78 `:focus-visible { outline: 2px solid var(--ring) }`).
  it('keeps the selection indicator derived from the accent and off the focus ring', () => {
    const mix = dark['accent-strong']!
    expect(mix).toMatch(
      /^color-mix\(in oklab, var\(--accent\) \d+%, var\(--surface\)\)$/,
    )
    const pct = Number(/(\d+)%/.exec(mix)![1])
    expect(pct).toBeGreaterThanOrEqual(81)
    expect(pct).toBeLessThan(100)
    // El claro NO usa la mezcla: lleva el valor explícito que sí mide >=3:1 sobre blanco.
    expect(light['accent-strong']).toBe('#c26000')
    // And the @theme inline mapping, without which `bg-/border-/ring-accent-strong`
    // simply do not exist as Tailwind utilities: every rail would lose its fill and
    // every ring fall back to currentColor, while the AT gate — which probes the CSS
    // var, not the utility — kept printing "0 blocking". Dropping `...SELECTION`
    // from themeColorNames in tokens/build.mjs is exactly that silent break.
    expect(tokensCss).toContain('--color-accent-strong: var(--accent-strong);')
  })

  it('maps every theme color to a Tailwind @theme inline utility var', () => {
    expect(tokensCss).toContain('--color-accent: var(--accent);')
    expect(tokensCss).toContain(
      '--color-confidence-attributed: var(--confidence-attributed);',
    )
    expect(tokensCss).toContain('--shadow-md: var(--elev-md);')
    expect(tokensCss).toContain('--color-graphite-500: #71717a;')
  })
})
