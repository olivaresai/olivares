// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ADM-CORE-04 — brandv4 design tokens: DTCG (2025.10) -> Style Dictionary
// -> src/styles/tokens.css. The .tokens.json files under tokens/ are the SINGLE
// SOURCE OF TRUTH for the design tokens that used to hand-author directly in
// index.css. This pipeline regenerates the :root{} / .dark{} CSS-var blocks and
// the Tailwind 4 @theme inline{} block from those sources, byte-equivalently to
// the identity (copper #f08000, the confidence teal/slate axis, both AA+
// themes). Run `pnpm tokens` to regenerate; `pnpm tokens:check` fails CI if the
// committed output drifts from the sources (the openapi:check pattern).
//
// Style Dictionary is the standard, interoperable pipeline (the .tokens source can
// be shared with the public web + brand). We use it to PARSE + RESOLVE the DTCG
// sources, then emit with full control over ordering/format so the output stays
// byte-stable and readable. Node >=22, ESM (style-dictionary@5 is ESM-only).
import StyleDictionary from 'style-dictionary'
import { writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const OUT = join(here, '..', 'src', 'styles', 'tokens.css')

// name = the LAST path segment; leaf keys ARE the CSS custom-property names
// (background -> --background, accent-soft-foreground -> --accent-soft-foreground),
// so the group key (color/elevation/derived/...) only carries $type inheritance.
StyleDictionary.registerTransform({
  name: 'name/leaf',
  type: 'name',
  transform: (token) => token.path[token.path.length - 1],
})

/** Build one source set into an ordered [{name, value}] list (verbatim values). */
async function load(sources) {
  let captured = []
  StyleDictionary.registerFormat({
    name: 'capture/namevalue',
    format: ({ dictionary }) => {
      captured = dictionary.allTokens.map((t) => ({
        name: t.name,
        // original.$value is the authored DTCG value, emitted verbatim (hex,
        // rgba(), color-mix(...), multi-layer shadow, font stack) with no
        // lossy color/size transform — exact identity preservation.
        value: t.original?.$value ?? t.$value ?? t.value,
      }))
      return '' // we capture in-memory; no file written by this format
    },
  })
  const sd = new StyleDictionary({
    usesDtcg: true,
    source: sources.map((s) => join(here, s)),
    platforms: {
      capture: {
        transforms: ['name/leaf'],
        files: [{ destination: '__capture__', format: 'capture/namevalue' }],
      },
    },
    log: { verbosity: 'silent', warnings: 'disabled' },
  })
  await sd.buildAllPlatforms()
  return captured
}

/** Render `selector {\n  --name: value;\n}` with section comments interleaved.
 *
 * ⛔ COMPRUEBA LAS DOS DIRECCIONES, y la segunda faltaba. Un nombre del PLAN sin token en la fuente
 *    ya reventaba («token missing»); un token de la FUENTE sin hueco en el plan **se descartaba en
 *    silencio**. Medido el 2026-08-18 conmigo mismo: añadí `accent-strong` a
 *    `theme.dark.tokens.json`, el build dijo «32 dark» y no falló, y el token **no salió en el CSS**.
 *    Es el mismo defecto que este repositorio corrige en sus gates — un instrumento que calla donde
 *    debería dar un veredicto —, con el agravante de que aquí la consecuencia es un token que el
 *    autor cree definido y el navegador no tiene.
 */
function block(selector, entries, sections) {
  const byName = new Map(entries.map((e) => [e.name, e.value]))
  const lines = [`${selector} {`]
  const emitidos = new Set()
  for (const section of sections) {
    if (section.comment) lines.push(`  /* ${section.comment} */`)
    for (const name of section.names) {
      if (!byName.has(name))
        throw new Error(`token missing for ${selector}: --${name}`)
      lines.push(`  --${name}: ${byName.get(name)};`)
      emitidos.add(name)
    }
    lines.push('')
  }
  const huerfanos = [...byName.keys()].filter((n) => !emitidos.has(n))
  if (huerfanos.length > 0)
    throw new Error(
      `token(s) in the source with no slot in the emit plan for ${selector}, so they would be ` +
        `DROPPED SILENTLY: ${huerfanos.map((n) => `--${n}`).join(', ')}. ` +
        `Add each to a section's \`names\` (the plan is ordered on purpose) or remove it from the source.`,
    )
  if (lines[lines.length - 1] === '') lines.pop()
  lines.push('}')
  return lines.join('\n')
}

// --- ordered emit plan (mirrors the index.css structure for readability) ---
const SURFACES = [
  'background',
  'surface',
  'elevated',
  'overlay',
  'muted',
  'muted-foreground',
  'foreground',
  'border',
  'border-strong',
  'ring',
]
const ACCENT = [
  'accent',
  'accent-foreground',
  'accent-hover',
  'accent-active',
  'accent-text',
  'accent-soft',
  'accent-soft-foreground',
  'accent-line',
]
const SEMANTIC = [
  'success',
  'warning',
  'danger',
  'info',
  'danger-solid',
  'danger-solid-foreground',
]
const CONFIDENCE = ['confidence-attributed', 'confidence-approximate']
const ELEV = ['elev-xs', 'elev-sm', 'elev-md', 'elev-lg', 'elev-xl']
const DERIVED = [
  'success-soft',
  'success-line',
  'warning-soft',
  'warning-line',
  'danger-soft',
  'danger-line',
  'info-soft',
  'info-line',
]
// Kept OUT of DERIVED: the *-line tokens are resting container hairlines that only
// ever reinforce a label, and the AT gate waives them as advisory. This one is the
// SOLE identifier of the selected/active state on the session-viewer rows, so it is
// held to SC 1.4.11 (>=3:1) and gated as a blocking pair. Different duty, own name.
const SELECTION = ['accent-strong']
const GRAPHITE = [
  'graphite-50',
  'graphite-100',
  'graphite-200',
  'graphite-300',
  'graphite-400',
  'graphite-500',
  'graphite-600',
  'graphite-700',
  'graphite-800',
  'graphite-900',
  'graphite-950',
]
const FONTS = ['font-sans', 'font-mono', 'font-display']
const RADII = ['radius-sm', 'radius-md', 'radius-lg', 'radius-xl']
const MOTION = ['ease-out', 'ease-in', 'animate-pulse-live']

const light = await load(['theme.light.tokens.json', 'derived.tokens.json'])
const dark = await load(['theme.dark.tokens.json'])
const prim = await load(['primitives.tokens.json'])

const rootBlock = block(':root', light, [
  { comment: 'Surfaces & text — LIGHT', names: SURFACES },
  { comment: 'Brand accent (single orange) — LIGHT', names: ACCENT },
  {
    comment: 'Semantics — LIGHT (text/solid; soft fill + line derived below)',
    names: SEMANTIC,
  },
  {
    comment: 'Access-graph confidence (orthogonal cool axis) — LIGHT',
    names: CONFIDENCE,
  },
  {
    comment: 'Elevation — LIGHT (depth from layered surfaces + hairlines)',
    names: ELEV,
  },
  {
    comment:
      'Semantic soft fills + lines — derived from the (theme-switched) hue',
    names: DERIVED,
  },
  {
    comment:
      'Selection/active indicator — accent held at >=3:1 vs surface & background (SC 1.4.11)',
    names: SELECTION,
  },
])

const darkBlock = block('.dark', dark, [
  {
    comment: 'Surfaces & text — DARK (primary operator surface)',
    names: SURFACES,
  },
  { comment: 'Brand accent — DARK', names: ACCENT },
  // El indicador de selección se emite también en OSCURO desde el 2026-08-18: `derived` sólo se
  // carga en el bloque claro, así que fijar el claro a un hex explícito habría dejado al oscuro sin
  // definición. El valor de aquí es el `color-mix` que el oscuro YA resolvía por herencia, de modo
  // que el tema oscuro —hoy en cero bloqueantes— no cambia.
  { comment: 'Selection/active indicator — DARK', names: SELECTION },
  { comment: 'Semantics — DARK', names: SEMANTIC },
  { comment: 'Confidence — DARK', names: CONFIDENCE },
  {
    comment:
      'Elevation — DARK (borders + inset top highlight carry the lit edge)',
    names: ELEV,
  },
])

// @theme inline maps the theme-switched CSS vars to Tailwind 4 color utilities
// (re-resolved per theme at runtime), plus the theme-independent primitives.
const themeColorNames = [
  ...SURFACES,
  ...ACCENT,
  ...SEMANTIC,
  ...CONFIDENCE,
  ...DERIVED,
  ...SELECTION,
]
const primMap = new Map(prim.map((e) => [e.name, e.value]))
const themeLines = ['@theme inline {']
themeLines.push(
  '  /* Surfaces, accent, semantics & confidence — mapped to the theme vars */',
)
for (const name of themeColorNames)
  themeLines.push(`  --color-${name}: var(--${name});`)
themeLines.push('')
themeLines.push(
  '  /* Neutral graphite ramp — surfaces/wells + access-graph neutrals beyond the semantic tokens */',
)
for (const name of GRAPHITE)
  themeLines.push(`  --color-${name}: ${primMap.get(name)};`)
themeLines.push('')
themeLines.push(
  '  /* Typography (all self-hosted via @fontsource-variable in main.tsx) */',
)
for (const name of FONTS)
  themeLines.push(`  --${name}:\n    ${primMap.get(name)};`)
themeLines.push('')
themeLines.push('  /* Radii — tight/instrument-grade */')
for (const name of RADII) themeLines.push(`  --${name}: ${primMap.get(name)};`)
themeLines.push('')
themeLines.push('  /* Elevation (theme-switched via --elev-*) */')
for (const name of ELEV)
  themeLines.push(`  --shadow-${name.replace('elev-', '')}: var(--${name});`)
themeLines.push('')
themeLines.push('  /* Motion — fast, mechanical, no bounce */')
themeLines.push(`  --ease-out: ${primMap.get('ease-out')};`)
themeLines.push(`  --ease-in: ${primMap.get('ease-in')};`)
themeLines.push(`  --animate-pulse-live: ${primMap.get('animate-pulse-live')};`)
themeLines.push('}')

const header = `/* SPDX-FileCopyrightText: 2026 Olivares.AI */
/* SPDX-License-Identifier: AGPL-3.0-only */

/*
 * Olivares Operations Console — design tokens (brand-aligned, dark-first).
 *
 * GENERATED FILE — DO NOT EDIT. Source of truth: web/tokens/*.tokens.json (DTCG
 * 2025.10). Regenerate with \`pnpm tokens\`; \`pnpm tokens:check\` guards drift in CI.
 * The :root{} / .dark{} CSS-var blocks and the Tailwind 4 @theme inline{} block
 * below are emitted from the final Olivares brand identity (brandv4, 2026-06-10): brand neutrals #28282b / #fafaf9, the single brand orange #f08000 — the FILL
 * (--accent) in BOTH themes, deepened to #b45500 only where it is TEXT on the light
 * canvas (--accent-text) — and the attributed/approximate confidence
 * axis. Every TEXT pair is AA or better in both themes; the light accent FILL against
 * the canvas is deliberately below the 3:1 non-text threshold (2.58:1), because the
 * brand orange cannot reach it there — a declared trade-off, not an oversight. The tokens are not hand-authored (ADM-CORE-04): they are generated from a
 * standard, interoperable DTCG source via Style Dictionary.
 */
`

writeFileSync(
  OUT,
  `${header}\n${rootBlock}\n\n${darkBlock}\n\n${themeLines.join('\n')}\n`,
)
console.log(
  `tokens.css written (${light.length} light, ${dark.length} dark, ${prim.length} primitives)`,
)
