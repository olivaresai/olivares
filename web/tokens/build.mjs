// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Console design tokens: DTCG (2025.10) -> Style Dictionary -> src/styles/tokens.css.
// The .tokens.json files under tokens/console/ are the SINGLE SOURCE OF TRUTH for the
// console's v26.10 visual system. This pipeline writes the :root{} (light) and .dark{}
// CSS-variable blocks, the Tailwind 4 @theme inline{} block, and the density and motion
// blocks from those sources. Run `pnpm tokens` to regenerate; `pnpm tokens:check` fails CI
// if the committed output drifts from the sources.
//
// TWO LAYERS, ON PURPOSE. The files directly under tokens/ are the BRAND source that the
// transactional emails, the documentation site, the release diagrams and the website
// parity check read; this generator does not read them, so a console redesign cannot
// restyle an email or break the website's parity check by accident, and a brand change
// reaches the console only through a deliberate edit of tokens/console/.
//
// Style Dictionary PARSES and RESOLVES the DTCG sources; this file emits them with full
// control over ordering and format so the output stays byte-stable and readable. An
// authored value is emitted verbatim (hex, rgba(), color-mix(), multi-layer shadow, font
// stack); a DTCG alias (`{palette.canvas}`) is emitted as the value it resolves to, so a
// role name and the palette token it stands for can never hold two values.
// Node >=22, ESM (style-dictionary@5 is ESM-only).
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

/** A value authored as a DTCG alias, e.g. `{palette.canvas}`. */
const isAlias = (v) => typeof v === 'string' && /^\{[^{}]+\}$/.test(v.trim())

/** Build one source set into an ordered [{name, value, path}] list. */
async function load(sources) {
  let captured = []
  StyleDictionary.registerFormat({
    name: 'capture/namevalue',
    format: ({ dictionary }) => {
      captured = dictionary.allTokens.map((t) => ({
        name: t.name,
        // original.$value is the authored DTCG value, emitted verbatim (hex,
        // rgba(), color-mix(...), multi-layer shadow, font stack) with no
        // lossy color/size transform — exact identity preservation. An alias is
        // the one exception: it is emitted as the value Style Dictionary resolved.
        value: isAlias(t.original?.$value)
          ? t.$value
          : (t.original?.$value ?? t.$value ?? t.value),
        path: t.path,
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

// --- ordered emit plan (the reading order of the generated file) ---
// The v26.10 palette, under the names the design gives it.
const PALETTE_NEUTRALS = [
  'frame',
  'canvas',
  'surface',
  'raised',
  'hover',
  'active',
  'line',
  'line-strong',
  'ctl-border',
  'scrim',
]
const PALETTE_TEXT = ['text', 'text-2', 'text-3']
const PALETTE_ACCENT = [
  'accent',
  'on-accent',
  'accent-text',
  'accent-soft',
  'accent-line',
  'accent-border',
  'focus',
]
const PALETTE_STATUS = [
  'ok',
  'ok-soft',
  'warn',
  'warn-soft',
  'bad',
  'bad-soft',
  'info',
  'info-soft',
  'diff-add',
  'diff-del',
]
// Depth and atmosphere: plain variables, not Tailwind colors.
const PALETTE_DEPTH = ['shadow-pop', 'shadow-card', 'ember']
// The role names the components use, each an alias of a palette token or a measured value.
const SURFACES = [
  'background',
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
  'accent-foreground',
  'accent-hover',
  'accent-active',
  'accent-soft-foreground',
]
const SEMANTIC = [
  'success',
  'warning',
  'danger',
  'danger-solid',
  'danger-solid-foreground',
  'success-soft',
  'warning-soft',
  'danger-soft',
]
const CONFIDENCE = ['confidence-attributed', 'confidence-approximate']
const ELEV = ['elev-xs', 'elev-sm', 'elev-md', 'elev-lg', 'elev-xl']
// Hairlines mixed at runtime from the theme's status color; emitted in :root only.
const DERIVED = ['success-line', 'warning-line', 'danger-line', 'info-line']
// The SOLE identifier of the selected/active state on the session-viewer rows, so it is
// held to SC 1.4.11 (>=3:1): the palette's accent-border under the role name.
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
const RADII = [
  'radius-ctl',
  'radius-card',
  'radius-panel',
  'radius-sm',
  'radius-md',
  'radius-lg',
  'radius-xl',
]
// The type ladder. Emitted as Tailwind 4 `--text-<name>` entries WITH their
// `--line-height` / `--letter-spacing` / `--font-weight` sub-keys, so one utility
// (`text-title`) carries all four decisions. Ordered largest-first.
const TYPE_STEPS = [
  'hero',
  'display-lg',
  'display',
  'title',
  'heading',
  'body-l',
  'body',
  'caption',
  'overline',
  'mono',
  'mono-s',
]
const TYPE_SUBKEYS = ['line-height', 'letter-spacing', 'font-weight']
// The shell's geometry, named instead of bracketed.
const LAYOUT = [
  'container-page',
  'console-header-height',
  'console-rail-width',
  'console-inspector-width',
  'console-row-height',
  'console-list-row-height',
]
// The two keys Tailwind reads for EVERY `transition*` utility. There is no
// `--duration-*` namespace in Tailwind 4 (`duration-150` is a bare-number utility),
// so these two are the only place a console-wide motion decision can be made once.
const MOTION_DEFAULTS = [
  'default-transition-duration',
  'default-transition-timing-function',
]
// Density and pace: plain variables switched by an attribute on <html> (and, for pace,
// by the reduced-motion media query), emitted outside @theme.
const DENSITY = ['density-row-list', 'density-row-table', 'density-control']
const PACE = [
  'duration-fast',
  'duration-panel',
  'duration-sheet',
  'motion-shift',
]

const light = await load([
  'console/theme.light.tokens.json',
  'console/derived.tokens.json',
])
const dark = await load(['console/theme.dark.tokens.json'])
const primAll = await load(['console/primitives.tokens.json'])
// Density and pace carry one value per variant under the same leaf name; they are read
// by their group path and emitted in their own blocks, never through the @theme map.
const variantGroups = new Set(['density', 'pace'])
const prim = primAll.filter((e) => !variantGroups.has(e.path[0]))
function variant(group, name) {
  const entries = primAll.filter(
    (e) => e.path[0] === group && e.path[1] === name,
  )
  if (entries.length === 0) throw new Error(`no ${group}.${name} tokens`)
  return entries
}

const PALETTE = [
  { comment: 'Palette — neutrals', names: PALETTE_NEUTRALS },
  { comment: 'Palette — text', names: PALETTE_TEXT },
  { comment: 'Palette — the brand orange, by role', names: PALETTE_ACCENT },
  { comment: 'Palette — status and diff', names: PALETTE_STATUS },
  { comment: 'Palette — depth and atmosphere', names: PALETTE_DEPTH },
]
const ROLES = [
  { comment: 'Roles — surfaces and text', names: SURFACES },
  { comment: 'Roles — the orange', names: ACCENT },
  { comment: 'Roles — status', names: SEMANTIC },
  {
    comment: 'Roles — access-graph confidence (a cool axis apart from status)',
    names: CONFIDENCE,
  },
  { comment: 'Roles — elevation', names: ELEV },
  {
    comment: 'Roles — selection indicator, >=3:1 on its neighbours (SC 1.4.11)',
    names: SELECTION,
  },
]

const rootBlock = block(':root', light, [
  ...PALETTE.map((s) => ({ ...s, comment: `${s.comment} — LIGHT` })),
  ...ROLES,
  {
    comment: 'Status hairlines, mixed at runtime from the theme status color',
    names: DERIVED,
  },
])

const darkBlock = block('.dark', dark, [
  ...PALETTE.map((s) => ({ ...s, comment: `${s.comment} — DARK` })),
  ...ROLES,
])

const comfortableBlock = block(
  ":root, [data-density='comfortable']",
  variant('density', 'comfortable'),
  [{ comment: 'Density — comfortable (the default)', names: DENSITY }],
)
// Its own :root block, after the two themes: the first :root of the file stays the
// light theme, and this one sits before the two blocks that stop movement.
const motionBlock = block(':root', variant('pace', 'full'), [
  { comment: 'Motion — durations and entrance distance', names: PACE },
])
const compactBlock = block(
  "[data-density='compact']",
  variant('density', 'compact'),
  [
    {
      comment: 'Density — compact (spacing only; type does not change)',
      names: DENSITY,
    },
  ],
)
const reduced = variant('pace', 'reduced')
const reducedSections = [
  { comment: 'Reduced motion — movement stops', names: PACE },
]
const reducedMediaBlock = block(':root', reduced, reducedSections)
  .split('\n')
  .map((l) => `  ${l}`)
  .join('\n')
const reducedBlock = block("[data-motion='reduce']", reduced, reducedSections)

// @theme inline maps the theme-switched CSS vars to Tailwind 4 color utilities
// (re-resolved per theme at runtime), plus the theme-independent primitives.
const themeColorNames = [
  ...PALETTE_NEUTRALS,
  ...PALETTE_TEXT,
  ...PALETTE_ACCENT,
  ...PALETTE_STATUS,
  ...SURFACES,
  ...ACCENT,
  ...SEMANTIC,
  ...CONFIDENCE,
  ...DERIVED,
  ...SELECTION,
]
const primMap = new Map(prim.map((e) => [e.name, e.value]))
const themeLines = ['@theme inline {']
themeLines.push('  /* Palette and roles — mapped to the theme vars */')
for (const name of themeColorNames)
  themeLines.push(`  --color-${name}: var(--${name});`)
themeLines.push('')
themeLines.push(
  '  /* Neutral graphite ramp — surfaces/wells + access-graph neutrals beyond the semantic tokens */',
)
for (const name of GRAPHITE)
  themeLines.push(`  --color-${name}: ${primMap.get(name)};`)
themeLines.push('')
themeLines.push('  /* Typography — self-hosted in src/styles/fonts.css */')
for (const name of FONTS)
  themeLines.push(`  --${name}:\n    ${primMap.get(name)};`)
themeLines.push('')
themeLines.push('  /* Radii — controls, cards, panels */')
for (const name of RADII) themeLines.push(`  --${name}: ${primMap.get(name)};`)
themeLines.push('')
themeLines.push('  /* Elevation (theme-switched via --elev-*) */')
for (const name of ELEV)
  themeLines.push(`  --shadow-${name.replace('elev-', '')}: var(--${name});`)
themeLines.push('')
themeLines.push('  /* Motion — one easing curve, no bounce */')
themeLines.push(`  --ease-out: ${primMap.get('ease-out')};`)
themeLines.push(`  --ease-in: ${primMap.get('ease-in')};`)
themeLines.push(`  --animate-pulse-live: ${primMap.get('animate-pulse-live')};`)
// The defaults every `transition`/`transition-colors` utility inherits. They are
// emitted next to the curves they use so a reader sees the whole motion decision at
// once, and `block()`'s orphan check (above) still guarantees nothing is dropped.
for (const name of MOTION_DEFAULTS) {
  if (!primMap.has(name))
    throw new Error(`token missing for @theme inline: --${name}`)
  themeLines.push(`  --${name}: ${primMap.get(name)};`)
}
themeLines.push('')
themeLines.push(
  '  /* Type ladder — size + leading + tracking + weight per step */',
)
for (const step of TYPE_STEPS) {
  const base = `text-${step}`
  if (!primMap.has(base))
    throw new Error(`token missing for @theme inline: --${base}`)
  themeLines.push(`  --${base}: ${primMap.get(base)};`)
  for (const sub of TYPE_SUBKEYS) {
    const name = `${base}--${sub}`
    // Tracking and weight are optional per step on purpose: `text-body` inherits the
    // page's, and emitting an empty sub-key would override it with nothing.
    if (primMap.has(name)) themeLines.push(`  --${name}: ${primMap.get(name)};`)
  }
}
themeLines.push('')
themeLines.push(
  '  /* Layout — the shell geometry, named rather than bracketed */',
)
for (const name of LAYOUT) {
  if (!primMap.has(name))
    throw new Error(`token missing for @theme inline: --${name}`)
  themeLines.push(`  --${name}: ${primMap.get(name)};`)
}
themeLines.push('}')

const header = `/* SPDX-FileCopyrightText: 2026 Olivares.AI */
/* SPDX-License-Identifier: AGPL-3.0-only */

/*
 * Olivares AI console — design tokens (v26.10 visual system).
 *
 * GENERATED FILE — DO NOT EDIT. Source of truth: web/tokens/console/*.tokens.json
 * (DTCG 2025.10). Regenerate with \`pnpm tokens\`; \`pnpm tokens:check\` guards drift in CI.
 * Blocks, in order: the LIGHT theme (:root), the DARK theme (.dark), the Tailwind 4
 * @theme inline mapping, density (comfortable by default, compact by attribute) and
 * motion (stopped under prefers-reduced-motion and under the Reduce motion setting).
 * Each theme holds the palette — four layered neutrals, three text tones, the single
 * brand orange #f08000 by role, status colors with soft fills — and the role names the
 * components use, each resolved from the palette. Every text token is at least 4.5:1 and
 * every control boundary, focus ring and selection border at least 3:1 on every surface
 * of its theme (src/styles/tokens.test.ts measures the 372 pairs).
 */
`

writeFileSync(
  OUT,
  `${header}\n${rootBlock}\n\n${darkBlock}\n\n${themeLines.join('\n')}\n\n` +
    `${comfortableBlock}\n\n${compactBlock}\n\n${motionBlock}\n\n` +
    `@media (prefers-reduced-motion: reduce) {\n${reducedMediaBlock}\n}\n\n${reducedBlock}\n`,
)
console.log(
  `tokens.css written (${light.length} light, ${dark.length} dark, ${primAll.length} primitives)`,
)
