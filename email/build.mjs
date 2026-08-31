// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// build.mjs — emit the finished email bundles for both runtimes.
//
//   web/tokens/*.tokens.json  ─┐
//   email/copy/<locale>.json  ─┼─> layout.mjs + templates.mjs ─┬─> commercial/license-worker/
//   email/templates.mjs       ─┘                               │     src/email/templates.generated.ts
//                                                              ├─> core/emailtemplate/
//                                                              │     templates.generated.json
//                                                              └─> email/email.manifest.json
//
// The generated files are ARTEFACTS, not sources: nobody edits them, and
// `task lint:email-brand` re-runs this build and fails the push if what is
// committed is not what the sources produce. That is the tokens.css pattern
// (ADM-CORE-04) applied to the one brand surface that was outside it.
//
// Usage:
//   node email/build.mjs            # write the artefacts
//   node email/build.mjs --check    # fail if the committed artefacts are stale

import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { LOCALES, contrast, contrastPairs, resolveBrand } from './brand.mjs'
import { esc, renderHtml, renderText } from './layout.mjs'
import {
  ALLOWED_PLACEHOLDERS,
  RUNTIME_FRAGMENT_MARKERS,
  RUNTIME_STRINGS,
  TEMPLATES,
} from './templates.mjs'

const HERE = dirname(fileURLToPath(import.meta.url))
const ROOT = join(HERE, '..')

const OUT_MANIFEST = join(HERE, 'email.manifest.json')
const OUT_WORKER = join(
  ROOT,
  'commercial/license-worker/src/email/templates.generated.ts',
)
const OUT_CORE = join(ROOT, 'core/emailtemplate/templates.generated.json')

// None of the seven product locales is right-to-left. The attribute is emitted
// anyway, from a table rather than a constant, so adding an RTL locale is a data
// change here and not a hunt through a rendered template.
const DIR = { en: 'ltr', es: 'ltr', zh: 'ltr', ja: 'ltr', de: 'ltr', ru: 'ltr', fr: 'ltr' }

// --- copy --------------------------------------------------------------------

/** Deep-walk a copy object and HTML-escape every leaf. Escaping happens ONCE, at
 *  build time, on text we author; the runtimes escape only the values they
 *  substitute. A string escaped twice would show `&amp;amp;` to a customer. */
function escapeCopy(node) {
  if (typeof node === 'string') return esc(node)
  const out = {}
  for (const [k, v] of Object.entries(node)) out[k] = escapeCopy(v)
  return out
}

function loadCopy(locale) {
  const raw = JSON.parse(
    readFileSync(join(HERE, 'copy', `${locale}.json`), 'utf8'),
  )
  if (raw.$locale !== locale)
    throw new Error(
      `email/build: copy/${locale}.json declares $locale ${JSON.stringify(raw.$locale)}`,
    )
  return raw
}

/**
 * Key parity across the seven locales, enforced here rather than left to
 * lint:i18n — that gate reads the console's own locale trees and has never
 * looked at this directory. A locale missing `invite.expiry` would render the
 * literal string `undefined` into an email and nothing downstream would notice.
 */
function assertKeyParity(all) {
  const paths = (o, p = '') =>
    Object.entries(o).flatMap(([k, v]) =>
      typeof v === 'string' ? [`${p}${k}`] : paths(v, `${p}${k}.`),
    )
  const ref = paths(all.en).sort()
  for (const locale of LOCALES) {
    if (locale === 'en') continue
    const got = paths(all[locale]).sort()
    const missing = ref.filter((k) => !got.includes(k))
    const extra = got.filter((k) => !ref.includes(k))
    if (missing.length || extra.length)
      throw new Error(
        `email/build: copy/${locale}.json key parity broken` +
          (missing.length ? `\n  missing: ${missing.join(', ')}` : '') +
          (extra.length ? `\n  unexpected: ${extra.join(', ')}` : ''),
      )
  }
}

// --- rendering ---------------------------------------------------------------

// A canonical marker, and NOT one wearing extra braces. The lookarounds are the
// whole point: `{{DATE}}}` and `{{DATE}` both walked past the first attempt at this
// check, because a plain /\{\{[^}]*\}\}/ happily matched the canonical PREFIX of
// the first and never saw the second at all.
const PLACEHOLDER_RE = /(?<!\{)\{\{([A-Z_]+)\}\}(?!\})/g

function placeholdersIn(s) {
  return new Set(Array.from(s.matchAll(PLACEHOLDER_RE), (m) => m[1]))
}

/**
 * Brace constructs that are not canonical markers.
 *
 * Canonical markers are removed first and whatever `{{` or `}}` survives is a
 * finding. Single braces are NOT a finding: the rendered body contains a stylesheet
 * and is full of them. Doubled braces, in these bodies, only ever mean a marker.
 */
function malformedMarkersIn(s) {
  const rest = s.replace(PLACEHOLDER_RE, '')
  const out = []
  for (const m of rest.matchAll(/\{\{[^\n]{0,40}|\}\}/g)) out.push(m[0])
  return out
}

/** Reject non-canonical brace constructs wherever they appear. */
function assertNoMalformedMarkers(where, text) {
  const bad = malformedMarkersIn(text)
  if (bad.length)
    throw new Error(
      `email/build: ${where} carries non-canonical marker(s) ${bad.join(', ')}; ` +
        'markers are {{UPPER_SNAKE}} and nothing else',
    )
}

/**
 * Render one (template, locale) pair and check its markers both ways. An unknown
 * marker would reach a customer as literal `{{FOO}}`; a marker the template has
 * stopped emitting means a runtime is still substituting into nothing, which is
 * the quieter half of the same defect.
 */
function render(id, locale, copy, brand) {
  const spec = TEMPLATES[id]
  // The block list is built TWICE, from escaped copy for the HTML body and from
  // raw copy for the plain-text one. Building both from the escaped copy shipped
  // `&#39;` where a French reader expects an apostrophe — five of them in one
  // body — because text/plain is not HTML and nothing there ever un-escapes.
  const c = escapeCopy(copy)
  const built = spec.build(c)
  const builtRaw = spec.build(copy)
  const subject = spec.subject(copy) // the SUBJECT header is not HTML
  const wordmark = c.common.wordmark
  const footer = c.common.footerNote

  const html = renderHtml(
    { ...built, wordmark, footer },
    brand,
  )
    .replace(/\{\{LANG\}\}/g, locale)
    .replace(/\{\{DIR\}\}/g, DIR[locale])
    .replace(/\{\{SUBJECT\}\}/g, esc(subject))

  const text = renderText({
    ...builtRaw,
    wordmark: copy.common.wordmark,
    footer: copy.common.footerNote,
    signoff: copy.common.signoff,
  })

  // The SUBJECT is checked alongside the bodies. It has no markers today, and the
  // cheap way to keep it that way is to look: a placeholder added to a subject
  // would be substituted by nobody and would arrive in an inbox as `{{LICENSEE}}`
  // — in the one line of an email a customer cannot avoid reading.
  // NO marker may appear in a subject. Both runtimes pass the subject through
  // untouched, so any marker there — even a declared one — arrives in the one line
  // a customer cannot avoid reading. Checking it against the allowed set was not
  // enough; the rule is none at all.
  assertNoMalformedMarkers(`${id}/${locale} subject`, subject)
  for (const p of placeholdersIn(subject))
    throw new Error(
      `email/build: ${id}/${locale} subject carries {{${p}}}; subjects are never substituted`,
    )
  for (const [what, body] of [['html', html], ['text', text]])
    assertNoMalformedMarkers(`${id}/${locale} ${what}`, body)

  const allowed = new Set(ALLOWED_PLACEHOLDERS[id])
  for (const [what, body] of [
    ['html', html],
    ['text', text],
  ]) {
    for (const p of placeholdersIn(body))
      if (!allowed.has(p))
        throw new Error(
          `email/build: ${id}/${locale} ${what} carries unknown placeholder {{${p}}}`,
        )
  }
  // ⛔ PER MEDIUM, AND NOT OVER THE UNION OF THE TWO. This used to build one `seen` set from the
  // HTML and the text together, so a marker present in only one of them satisfied the contract for
  // the whole template — and the Codex `sol max` contrast of 2026-08-27 demonstrated it with a
  // compiling mutant rather than a hypothesis: adding `htmlOnly: true` to the download button
  // removes the ONLY download destination from every plain-text licence email, and `--check`
  // still returned 0. A plain-text body is not a summary of the email, it IS the email (see
  // renderText in layout.mjs); a marker the customer needs is needed in both.
  for (const [what, body] of [
    ['html', html],
    ['text', text],
  ]) {
    const seen = placeholdersIn(body)
    for (const p of allowed)
      if (!seen.has(p))
        throw new Error(
          `email/build: ${id}/${locale} ${what} never emits declared placeholder {{${p}}}`,
        )
  }

  // The one deliverability rule that must hold no matter what a future template
  // author writes: no image tags, no external resources, no scripts. Asserted on
  // the RENDERED output, so it cannot be defeated by composing the tag at build
  // time out of pieces that individually look harmless.
  // The labels deliberately DESCRIBE the forbidden thing rather than spelling it:
  // `task lint:email-brand` reads the string literals of this file too, and a
  // label containing the literal syntax it forbids reports itself.
  for (const [what, re] of [
    ['an image tag', /<img\b/i],
    ['an external stylesheet or resource link', /<link\b/i],
    ['a script', /<script\b/i],
    ['a remote resource', /(?:src|background)\s*=\s*["']?https?:/i],
    ['a data URI', /["'(]\s*data:/i],
    ['a web-font rule', /@font-face/i],
    ['a stylesheet import', /@import/i],
  ])
    if (re.test(html))
      throw new Error(`email/build: ${id}/${locale} html contains ${what}`)

  return { subject, text, html }
}

// --- emitters ----------------------------------------------------------------

const GEN_BANNER = (sources) =>
  [
    'GENERATED FILE — DO NOT EDIT.',
    '',
    `Source of truth: ${sources}.`,
    'Regenerate with `node email/build.mjs`; `task lint:email-brand` fails the',
    'push when what is committed is not what those sources produce.',
  ].join('\n')

function tsLiteral(v) {
  return JSON.stringify(v)
}

function emitWorker(bundle, ids) {
  const head = [
    '// SPDX-FileCopyrightText: 2026 Olivares.AI',
    '// SPDX-License-Identifier: LicenseRef-Olivares-Commercial',
    '',
    '/**',
    ...GEN_BANNER('web/tokens/*.tokens.json + email/copy/*.json + email/templates.mjs')
      .split('\n')
      .map((l) => ` * ${l}`.trimEnd()),
    ' *',
    ' * Every colour, font stack and radius below was resolved from the DTCG design',
    ' * tokens that generate the console. Nothing here was typed by hand, and a hex',
    ' * typed by hand into this file is erased by the next regeneration.',
    ' */',
    '',
    `export type EmailLocale = ${LOCALES.map((l) => tsLiteral(l)).join(' | ')};`,
    '',
    `export const EMAIL_LOCALES: readonly EmailLocale[] = [${LOCALES.map((l) => tsLiteral(l)).join(', ')}];`,
    '',
    `export type WorkerTemplateId = ${ids.map((i) => tsLiteral(i)).join(' | ')};`,
    '',
    'export interface EmailTemplate {',
    '  readonly subject: string;',
    '  readonly text: string;',
    '  readonly html: string;',
    '}',
    '',
    'export interface EmailStrings {',
    '  /** Complete sentence: the licence has no end date. */',
    '  readonly statusPerpetual: string;',
    '  /** Complete sentence carrying a {{DATE}} marker the caller substitutes. */',
    '  readonly statusUntil: string;',
    '}',
    '',
    '/**',
    ' * The substitution contract, generated from the templates themselves. A template',
    ' * that grows or loses a {{MARKER}} changes this map, and every call site that no',
    ' * longer supplies exactly the right values stops compiling. That is the point:',
    ' * a missing value would otherwise ship the literal text `{{VERIFY_URL}}` to a',
    ' * customer, and `tsc` is the only reviewer guaranteed to look at every caller.',
    ' */',
    'export interface EmailValueMap {',
    ...ids.flatMap((id) => [
      `  readonly ${id}: {`,
      ...ALLOWED_PLACEHOLDERS[id].map((p) => `    readonly ${p}: string;`),
      '  };',
    ]),
    '}',
    '',
  ]

  const tpl = ['export const EMAIL_TEMPLATES: Readonly<']
  tpl.push('  Record<EmailLocale, Readonly<Record<WorkerTemplateId, EmailTemplate>>>')
  tpl.push('> = {')
  for (const locale of LOCALES) {
    tpl.push(`  ${locale}: {`)
    for (const id of ids) {
      const t = bundle.templates[locale][id]
      tpl.push(`    ${id}: {`)
      tpl.push(`      subject: ${tsLiteral(t.subject)},`)
      tpl.push(`      text: ${tsLiteral(t.text)},`)
      tpl.push(`      html: ${tsLiteral(t.html)},`)
      tpl.push('    },')
    }
    tpl.push('  },')
  }
  tpl.push('};')
  tpl.push('')

  const str = ['export const EMAIL_STRINGS: Readonly<Record<EmailLocale, EmailStrings>> = {']
  for (const locale of LOCALES) {
    const s = bundle.strings[locale]
    str.push(`  ${locale}: {`)
    str.push(`    statusPerpetual: ${tsLiteral(s.statusPerpetual)},`)
    str.push(`    statusUntil: ${tsLiteral(s.statusUntil)},`)
    str.push('  },')
  }
  str.push('};')
  str.push('')

  return [...head, ...tpl, ...str].join('\n')
}

function emitCore(bundle, ids) {
  const templates = {}
  for (const locale of LOCALES) {
    templates[locale] = {}
    for (const id of ids) templates[locale][id] = bundle.templates[locale][id]
  }
  // Go has no equivalent of the TypeScript value map — a map[string]string cannot
  // be checked at compile time — so the contract travels as data and the package's
  // own test asserts every template's markers against it. Same guarantee, one step
  // later: a template that grows a marker fails `go test`, not `tsc`.
  const placeholders = {}
  for (const id of ids) placeholders[id] = ALLOWED_PLACEHOLDERS[id]
  return `${JSON.stringify(
    {
      $description: GEN_BANNER(
        'web/tokens/*.tokens.json + email/copy/*.json + email/templates.mjs',
      ).replace(/\n/g, ' '),
      locales: LOCALES,
      placeholders,
      templates,
    },
    null,
    2,
  )}\n`
}

// --- main --------------------------------------------------------------------

function build() {
  const brand = resolveBrand()

  const copy = {}
  for (const locale of LOCALES) copy[locale] = loadCopy(locale)
  assertKeyParity(copy)

  const bundle = { templates: {}, strings: {} }
  for (const locale of LOCALES) {
    bundle.templates[locale] = {}
    for (const id of Object.keys(TEMPLATES))
      bundle.templates[locale][id] = render(id, locale, copy[locale], brand)
    bundle.strings[locale] = {
      ...RUNTIME_STRINGS.worker(copy[locale]),
      ...RUNTIME_STRINGS.core(copy[locale]),
    }
    // The runtime fragments are substituted too, and their markers were checked by
    // nobody: a typo turning {{DATE}} into {{DAT}} would have reached a licence
    // email as literal braces.
    for (const [name, value] of Object.entries(bundle.strings[locale])) {
      assertNoMalformedMarkers(`runtime string ${name}/${locale}`, value)
      const seen = [...placeholdersIn(value)]
      const want = RUNTIME_FRAGMENT_MARKERS[name] ?? []
      const bad = seen.filter((p) => !want.includes(p))
      const missing = want.filter((p) => !seen.includes(p))
      if (bad.length || missing.length)
        throw new Error(
          `email/build: runtime string ${name}/${locale} markers are ` +
            `${JSON.stringify(seen)}, expected ${JSON.stringify(want)}`,
        )
    }
  }

  const workerIds = Object.keys(TEMPLATES).filter(
    (id) => TEMPLATES[id].runtime === 'worker',
  )
  const coreIds = Object.keys(TEMPLATES).filter(
    (id) => TEMPLATES[id].runtime === 'core',
  )

  // AA is a measurement. The ratios are recorded in the manifest so a reviewer
  // reads the number rather than the claim, and the gate recomputes them.
  // Compare the RAW ratio and round only what is displayed. WCAG 2.2 is explicit
  // that computed values are not rounded before the comparison, and rounding first
  // lets a true 4.499 present itself as 4.50 and pass.
  const contrastRows = contrastPairs(brand).map((p) => {
    const raw = contrast(p.fg, p.bg)
    return { ...p, ratio: Math.round(raw * 100) / 100, raw }
  })
  const failed = contrastRows.filter((r) => r.raw < r.min)
  if (failed.length)
    throw new Error(
      `email/build: WCAG AA failure\n${failed
        .map((r) => `  ${r.theme}: ${r.what} ${r.fg} on ${r.bg} = ${r.ratio}:1 (needs ${r.min})`)
        .join('\n')}`,
    )

  const profile = treeProfile()
  const ctx = {
    manifest: { ...brand, contrast: contrastRows },
    bundle,
    workerIds,
    coreIds,
  }
  return {
    // Derived from outputSpecs(), never listed again: what is emitted and what the
    // gate exempts are now the same array by construction.
    files: outputSpecs().map((o) => [o.path, o.emit(ctx)]),
    contrastRows,
    profile,
  }
}

/**
 * The paths this build owns. Exported so the gate can assert that its GENERATED
 * exemption list is EXACTLY this set: an exemption list maintained independently
 * of the thing it exempts is a place to park a hand-written file where no rule
 * looks at it.
 */
/**
 * Which trees this checkout actually has, with THREE distinguishable answers —
 * the scripts/private-leg.sh doctrine, because this build hit exactly the defect
 * that script exists to prevent.
 *
 * The public export deliberately ships email/, core/emailtemplate/, the checker,
 * the Taskfile and the hook, and deliberately excludes commercial/ and cloud/. A
 * builder that always writes the worker bundle therefore produces a PUBLIC tree
 * whose own pre-push hook fails on its first push — a gate born red, which is
 * worse than no gate because everyone learns to bypass it.
 *
 *   directory present                  -> build it
 *   absent AND PUBLIC-EXPORT.md present -> not applicable here, say so, carry on
 *   absent with no marker               -> REFUSE. Absence is not a marker; a hub
 *                                          that lost commercial/ must not quietly
 *                                          emit less and report success.
 */
export function treeProfile() {
  const hasWorker = existsSync(join(ROOT, 'commercial/license-worker'))
  const isPublic = existsSync(join(ROOT, 'PUBLIC-EXPORT.md'))
  if (hasWorker) return { name: 'hub', worker: true }
  if (isPublic) return { name: 'public', worker: false }
  throw new Error(
    'email/build: commercial/license-worker is missing and there is no ' +
      'PUBLIC-EXPORT.md marker. Refusing to guess which tree this is — a hub that ' +
      'lost that directory would otherwise emit less and report success.',
  )
}

/**
 * THE output description. One array, and everything else is derived from it.
 *
 * There used to be two: `build()` listed the artefacts it wrote and `outputPaths()`
 * listed the same three constants again for the gate to compare against. Two
 * hand-maintained lists of the same thing is the coincidence-not-derivation defect
 * this whole directory exists to close, reintroduced inside its own gate — and it
 * was not theoretical: an artefact added to the emitter but not to the second list
 * was written, was exempt from the literal rule, and the gate still reported "3
 * generated bundles pinned" and exited 0.
 *
 * `emit` is called only when the artefacts are actually being produced, so reading
 * the paths costs nothing.
 */
function outputSpecs() {
  const worker = treeProfile().worker
  return [
    { path: OUT_MANIFEST, emit: (b) => `${JSON.stringify(b.manifest, null, 2)}\n` },
    ...(worker
      ? [{ path: OUT_WORKER, emit: (b) => emitWorker(b.bundle, b.workerIds) }]
      : []),
    { path: OUT_CORE, emit: (b) => emitCore(b.bundle, b.coreIds) },
  ]
}

/** The paths this build owns IN THIS TREE, derived from the one description above. */
export function outputPaths() {
  return outputSpecs().map((o) => relative(ROOT, o.path))
}

// Importing this module must not run the build. It is imported by the gate to
// read OUTPUT_PATHS, and a module that gates on import cannot be read by anything.
const isEntry =
  process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])

if (isEntry) main()

function main() {
const check = process.argv.includes('--check')
const { files, contrastRows, profile } = build()
if (profile.name !== 'hub')
  console.log(
    `email/build: ${profile.name} tree — the licence Worker is not part of it, so ` +
      'its bundle is NOT APPLICABLE here and was neither written nor checked.',
  )
let stale = 0

for (const [path, content] of files) {
  const rel = relative(ROOT, path)
  if (check) {
    let current = null
    try {
      current = readFileSync(path, 'utf8')
    } catch {
      /* absent counts as stale, reported below */
    }
    if (current !== content) {
      stale += 1
      console.error(
        current === null
          ? `email/build: MISSING ${rel} — run \`node email/build.mjs\``
          : `email/build: STALE ${rel} — run \`node email/build.mjs\``,
      )
    }
  } else {
    mkdirSync(dirname(path), { recursive: true })
    writeFileSync(path, content)
    console.log(`email/build: wrote ${rel} (${content.length} bytes)`)
  }
}

if (check) {
  if (stale) {
    console.error(
      `email/build: ${stale} artefact(s) no longer match web/tokens + email/copy.`,
    )
    process.exit(1)
  }
  console.log(
    `email/build: OK — ${files.length} artefacts match their sources; ` +
      `${contrastRows.length} contrast pairs at or above AA.`,
  )
}
}
