// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// check-i18n-parity.mjs — fail if any console UI language drifts from English.
//
// The console ships seven languages (en/es/zh/ja/de/ru/fr). Every
// language MUST carry the same key set as English, or i18next falls back to
// English *silently* — an untranslated string ships looking translated. This
// check makes that drift a hard error in the push gate + mainline-ci.
//
// It verifies, for every namespace (the five foundation files + every feature's
// i18n bundle) and every language:
//   1. Key parity — the same non-plural keys as English (no missing, no extra).
//   2. Plural correctness — for each English plural base, the language carries
//      EXACTLY its CLDR cardinal categories (Intl.PluralRules): zh/ja → {other},
//      de → {one, other}, es/fr → {one, many, other},
//      ru → {one, few, many, other}.
//      (So this is NOT naive key-equality: a plural key set differs by language
//      *by design*, and getting it wrong is itself a bug we catch.)
//   3. Placeholder parity — the same {{interpolation}} variables and the same
//      <component> tags as English, so a translator can't drop or invent one.
//
// Usage:
//   node scripts/check-i18n-parity.mjs            # full scan, exits 1 on drift
//   node scripts/check-i18n-parity.mjs --file P   # check one locale file vs its en sibling
//   node scripts/check-i18n-parity.mjs --summary  # also print per-language counts
//
// Pure Node (no deps); run from the repo root.

import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'

const ROOT = path.resolve(path.dirname(new URL(import.meta.url).pathname), '..')
const WEB = path.join(ROOT, 'web')
const LOCALES = path.join(WEB, 'src/lib/i18n/locales')
const FEATURES = path.join(WEB, 'src/features')

// ⛔ Y EL PUNTO CIEGO DE ANTES DE EMPEZAR: si uno de los dos árboles no existe —repo a
//    medio clonar, ruta renombrada, ejecución desde otro sitio—, `readdirSync` LANZA y el guion
//    muere con una traza de Node y rc=1. Eso se lee como «el gate está roto», y lo que hay es que
//    no pudo mirar. Se comprueba antes y se sale 2, nombrando la ruta que falta.
for (const [nombre, dir] of [
  ['LOCALES', LOCALES],
  ['FEATURES', FEATURES],
]) {
  if (!fs.existsSync(dir)) {
    console.error(
      `⛔ i18n parity: NO HE PODIDO MIRAR — ${nombre} no existe (${dir}). No se comparó ningún ` +
        `idioma, así que esto no dice nada sobre la paridad.`,
    )
    process.exit(2)
  }
}
const FOUNDATION_NS = ['common', 'nav', 'auth', 'errors', 'settings']
const CANON = 'en'
const PLURAL_SUFFIX = /_(zero|one|two|few|many|other)$/

const args = new Set(process.argv.slice(2))
const SUMMARY = args.delete('--summary')
const fileFlagIdx = process.argv.indexOf('--file')
const SINGLE = fileFlagIdx !== -1 ? process.argv[fileFlagIdx + 1] : null

/** CLDR cardinal plural categories for a language (no hardcoded tables). */
const catsCache = new Map()
function pluralCats(lang) {
  if (!catsCache.has(lang))
    catsCache.set(
      lang,
      new Set(new Intl.PluralRules(lang, { type: 'cardinal' }).resolvedOptions().pluralCategories),
    )
  return catsCache.get(lang)
}

function readJSON(p) {
  // ⛔ Un fichero de locale ausente o ilegible es un DEFECTO del árbol (rc 1), no un punto ciego:
  //    si falta la referencia inglesa no hay nada contra lo que comparar, y eso es algo que
  //    arreglar. Lo que no vale es salir con una traza de Node, que se lee como «el gate se ha
  //    roto» en vez de como un veredicto sobre el árbol.
  try {
    return JSON.parse(fs.readFileSync(p, 'utf8'))
  } catch (e) {
    console.error(
      `✗ i18n parity: no se pudo leer ${p} — ${e instanceof Error ? e.message : String(e)}. ` +
        `Un locale ausente o con JSON inválido rompe la comparación: arréglalo o retíralo.`,
    )
    process.exit(1)
  }
}

/**
 * Flatten a locale object into:
 *   regular: Map<dottedPath, string>      — non-plural leaves
 *   plurals: Map<dottedBase, Map<cat,str>> — plural groups (base → {cat: value})
 * A plural group at a level = sibling keys `<base>_<cat>` where `<base>_other` exists.
 */
function flatten(obj, prefix = '', regular = new Map(), plurals = new Map()) {
  // Identify plural bases at this level (need an `_other` sibling).
  const otherBases = new Set()
  for (const k of Object.keys(obj)) {
    const m = k.match(PLURAL_SUFFIX)
    if (m && m[1] === 'other') otherBases.add(k.slice(0, m.index))
  }
  for (const k of Object.keys(obj)) {
    const v = obj[k]
    const m = k.match(PLURAL_SUFFIX)
    if (m && otherBases.has(k.slice(0, m.index))) {
      const base = prefix + k.slice(0, m.index)
      if (!plurals.has(base)) plurals.set(base, new Map())
      plurals.get(base).set(m[1], v)
      continue
    }
    if (v && typeof v === 'object' && !Array.isArray(v)) {
      flatten(v, prefix + k + '.', regular, plurals)
    } else {
      regular.set(prefix + k, v)
    }
  }
  return { regular, plurals }
}

/**
 * Interpolation variables ({{x}}, {{x, fmt}}) and React `<Trans>` component tag
 * names. Only GENUINE component tags are returned — a name that appears as a
 * matched pair (`<x>…</x>`) or self-closing (`<x/>`). A lone `<x>` with no close
 * is literal placeholder hint text (e.g. `<scheme>:<locator>` in deploy), which a
 * translator may legitimately localize, so it is NOT enforced.
 */
function tokensOf(value) {
  const vars = new Set()
  if (typeof value !== 'string') return { vars, tags: new Set() }
  for (const m of value.matchAll(/\{\{([^}]+)\}\}/g)) vars.add(m[1].split(/[,}]/)[0].trim())
  const opens = new Set()
  const closes = new Set()
  const selfClosing = new Set()
  for (const m of value.matchAll(/<(\/?)\s*([a-zA-Z][\w-]*)([^>]*)>/g)) {
    const [, slash, name, rest] = m
    if (slash) closes.add(name)
    else if (/\/\s*$/.test(rest)) selfClosing.add(name)
    else opens.add(name)
  }
  const tags = new Set(selfClosing)
  for (const n of opens) if (closes.has(n)) tags.add(n)
  return { vars, tags }
}

const diff = (a, b) => [...a].filter((x) => !b.has(x)) // members of a not in b

/** Compare one language namespace against the English canonical. Returns problems[]. */
function compareNs(nsId, lang, enObj, langObj) {
  const problems = []
  const en = flatten(enObj)
  const lg = flatten(langObj)
  const cats = pluralCats(lang)

  // 1. Regular keys: exact set parity.
  for (const k of diff(new Set(en.regular.keys()), new Set(lg.regular.keys())))
    problems.push(`missing key: ${k}`)
  for (const k of diff(new Set(lg.regular.keys()), new Set(en.regular.keys())))
    problems.push(`extra key: ${k}`)

  // 2. Plural bases + correct CLDR categories.
  const enBases = new Set(en.plurals.keys())
  const lgBases = new Set(lg.plurals.keys())
  for (const b of diff(enBases, lgBases)) problems.push(`missing plural base: ${b}`)
  for (const b of diff(lgBases, enBases)) problems.push(`extra plural base: ${b}`)
  for (const b of [...enBases].filter((x) => lgBases.has(x))) {
    const present = new Set(lg.plurals.get(b).keys())
    for (const c of diff(cats, present)) problems.push(`plural ${b}: missing _${c} (CLDR ${lang})`)
    for (const c of diff(present, cats)) problems.push(`plural ${b}: unexpected _${c} (not a ${lang} CLDR category)`)
  }

  // 3. Placeholder/tag parity (only for keys present in both; structural gaps are
  //    already reported above). Plural forms compare against EN's `_other`.
  for (const [k, enVal] of en.regular) {
    if (!lg.regular.has(k)) continue
    placeholderProblems(k, enVal, lg.regular.get(k), problems)
  }
  for (const b of [...enBases].filter((x) => lgBases.has(x))) {
    const enOther = en.plurals.get(b).get('other')
    for (const [cat, val] of lg.plurals.get(b)) placeholderProblems(`${b}_${cat}`, enOther, val, problems)
  }

  return problems.map((p) => `[${nsId}/${lang}] ${p}`)
}

function placeholderProblems(key, enVal, langVal, problems) {
  const a = tokensOf(enVal)
  const b = tokensOf(langVal)
  for (const v of diff(a.vars, b.vars)) problems.push(`${key}: missing placeholder {{${v}}}`)
  for (const v of diff(b.vars, a.vars)) problems.push(`${key}: extra placeholder {{${v}}}`)
  for (const t of diff(a.tags, b.tags)) problems.push(`${key}: missing tag <${t}>`)
  for (const t of diff(b.tags, a.tags)) problems.push(`${key}: extra tag <${t}>`)
}

/** Discover the languages present (the foundation locale dirs are the source of truth). */
function discoverLangs() {
  return fs
    .readdirSync(LOCALES, { withFileTypes: true })
    .filter((d) => d.isDirectory())
    .map((d) => d.name)
    .sort()
}

// Feature dirs that legitimately ship .tsx with NO i18n/en.json bundle. This
// allowlist is EXPLICIT: a new feature that renders UI without an i18n bundle is
// invisible to the parity scan below (discoverNamespaces only sees dirs that
// already have en.json), which is exactly how backups/ and logs/ shipped 100%
// hardcoded English. Add an entry ONLY with a reason, and remove it when the
// bundle lands.
const NO_I18N_ALLOWLIST = new Map([
  // (Empty on purpose — Closed the last gaps, backups/ and logs/. Add an
  // entry ONLY with a reason, and remove it when the bundle lands.)
])

/** Does this feature dir contain any .tsx (i.e. does it render UI)? */
function hasTsx(dir) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    if (e.isDirectory() && hasTsx(path.join(dir, e.name))) return true
    if (e.isFile() && e.name.endsWith('.tsx')) return true
  }
  return false
}

/** Fail-closed structural guard: every features/ dir that renders UI must
 *  carry an i18n/en.json, or be explicitly allowlisted above with a reason. */
function checkFeaturesWithoutI18n() {
  const problems = []
  for (const d of fs.readdirSync(FEATURES, { withFileTypes: true })) {
    if (!d.isDirectory()) continue
    const dir = path.join(FEATURES, d.name)
    if (fs.existsSync(path.join(dir, 'i18n', `${CANON}.json`))) continue
    if (!hasTsx(dir)) continue
    if (NO_I18N_ALLOWLIST.has(d.name)) continue
    problems.push(
      `[${d.name}] feature renders UI (.tsx) but has no i18n/${CANON}.json — ` +
        'every visible string must go through i18n (or allowlist the dir in ' +
        'scripts/check-i18n-parity.mjs with a reason)',
    )
  }
  return problems
}

/** All namespaces: {id, enPath, pathFor(lang)}. */
function discoverNamespaces() {
  const out = []
  for (const ns of FOUNDATION_NS)
    out.push({
      id: ns,
      enPath: path.join(LOCALES, CANON, `${ns}.json`),
      pathFor: (l) => path.join(LOCALES, l, `${ns}.json`),
    })
  for (const d of fs.readdirSync(FEATURES).sort()) {
    const enPath = path.join(FEATURES, d, 'i18n', `${CANON}.json`)
    if (fs.existsSync(enPath))
      out.push({
        id: d,
        enPath,
        pathFor: (l) => path.join(FEATURES, d, 'i18n', `${l}.json`),
      })
  }
  return out
}

function rel(p) {
  return path.relative(ROOT, p)
}

// ── Single-file mode (used by the translation workflow's verify stage) ────────
if (SINGLE) {
  const abs = path.resolve(SINGLE)
  const dir = path.dirname(abs)
  let enPath, id, theLang
  if (path.basename(dir) === 'i18n') {
    // feature: features/<feat>/i18n/<lang>.json
    theLang = path.basename(abs, '.json')
    id = path.basename(path.dirname(dir))
    enPath = path.join(dir, `${CANON}.json`)
  } else {
    // foundation: locales/<lang>/<ns>.json
    id = path.basename(abs, '.json')
    theLang = path.basename(dir)
    enPath = path.join(LOCALES, CANON, `${id}.json`)
  }
  const problems = compareNs(id, theLang, readJSON(enPath), readJSON(abs))
  if (problems.length) {
    console.error(`✗ i18n parity: ${problems.length} problem(s) in ${rel(abs)}`)
    for (const p of problems) console.error('  ' + p)
    process.exit(1)
  }
  console.log(`✓ i18n parity OK: ${rel(abs)} (${id}/${theLang})`)
  process.exit(0)
}

// ── Full scan ─────────────────────────────────────────────────────────────────
const langs = discoverLangs().filter((l) => l !== CANON)
const namespaces = discoverNamespaces()
const allProblems = checkFeaturesWithoutI18n()
const perLang = Object.fromEntries(langs.map((l) => [l, 0]))
let checked = 0

for (const ns of namespaces) {
  const enObj = readJSON(ns.enPath)
  for (const lang of langs) {
    const p = ns.pathFor(lang)
    if (!fs.existsSync(p)) {
      const msg = `[${ns.id}/${lang}] missing locale file: ${rel(p)}`
      allProblems.push(msg)
      perLang[lang]++
      continue
    }
    const problems = compareNs(ns.id, lang, enObj, readJSON(p))
    allProblems.push(...problems)
    perLang[lang] += problems.length
    checked++
  }
}

if (SUMMARY || allProblems.length) {
  console.log(
    `i18n parity: ${namespaces.length} namespaces × ${langs.length} languages (${langs.join(', ')}) — ${checked} files checked`,
  )
  for (const l of langs) console.log(`  ${l}: ${perLang[l] ? `${perLang[l]} problem(s)` : 'OK'}`)
}

// ⛔ NO HABER MIRADO NO ES PARIDAD. Sin esta guarda, un escaneo que deja de reconocer la
//    disposición del árbol —un directorio renombrado, un glob cambiado— encuentra CERO namespaces,
//    por tanto cero problemas, e imprime «✓ i18n parity OK — 0 namespaces» saliendo 0. El gate se
//    pone verde habiendo comparado NADA, que es la peor de las tres respuestas porque se lee como
//    la mejor. Es la misma guarda que `check-client-callers.mjs` lleva por la misma razón.
if (namespaces.length === 0 || langs.length === 0) {
  console.error(
    `⛔ i18n parity: NO HE PODIDO MIRAR — el escaneo encontró ${namespaces.length} namespace(s) y ` +
      `${langs.length} idioma(s). Un árbol sin namespaces ni idiomas NO es un árbol con la paridad ` +
      `en orden: es un escáner que dejó de reconocer la disposición. Revisa FEATURES/LOCALES antes ` +
      `de leer esto como verde.`,
  )
  process.exit(2)
}

if (allProblems.length) {
  console.error(`\n✗ i18n parity FAILED — ${allProblems.length} problem(s):\n`)
  for (const p of allProblems) console.error('  ' + p)
  console.error(
    '\nEvery language must carry the same keys as English (plurals follow CLDR).' +
      ' Fix the locale file(s) above; do not let a key fall back to English silently.',
  )
  process.exit(1)
}

console.log(`✓ i18n parity OK — ${namespaces.length} namespaces, languages: ${langs.join(', ')}`)
