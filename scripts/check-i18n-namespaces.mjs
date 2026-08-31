// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Fail when a screen can render a component whose i18n NAMESPACE its chunk never
// registers — the S53x raw-key bug class ("assurance.stepUpTitle" printed verbatim
// in the first-boot wizard).
//
// This is NOT what `check-i18n-usage.mjs` checks. That gate asks "does the key
// exist in the English bundle?" and `assurance.stepUpTitle` DOES exist, in
// features/identity/i18n/en.json — so it stayed green while the wizard shipped raw
// identifiers. The missing question is the one asked here: at the moment this
// route's code is LOADED, has anybody imported the module that calls
// `registerTranslations('identity', …)`? i18next resources are global but they are
// only present once some module in the loaded graph registers them, and every view
// is behind `lazy(() => import(…))`, so "another screen registers it" is worth
// nothing to an operator who deep-links (or, as here, boots for the first time).
//
// Model: one "chunk" per lazy boundary. When chunk C renders, the modules certain
// to have executed are C's own static closure plus the root closure (main.tsx),
// which every page pays. Anything else — a namespace registered by a sibling route
// the operator happened to visit first — is luck, not a guarantee, and the gate
// treats it as absent.
//
// Pure Node (no deps); run from the repository root.

import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const SRC = path.join(ROOT, 'web', 'src')
const ENTRY = path.join(SRC, 'main.tsx')

// Registered by `lib/i18n`'s own init() call, which main.tsx imports for its
// side effect — so these are present on every page by construction.
const FOUNDATION = ['common', 'nav', 'auth', 'errors', 'settings']

const SOURCE_EXTENSIONS = ['.ts', '.tsx', '.js', '.jsx']
const RESOLVE_SUFFIXES = [
  '',
  ...SOURCE_EXTENSIONS,
  ...SOURCE_EXTENSIONS.map((ext) => `${path.sep}index${ext}`),
]

/** Strip comments and template literals so a regex sweep cannot read code out of prose. */
function strip(source) {
  let out = ''
  let i = 0
  while (i < source.length) {
    const char = source[i]
    if (char === '/' && source[i + 1] === '/') {
      while (i < source.length && source[i] !== '\n') i++
      continue
    }
    if (char === '/' && source[i + 1] === '*') {
      i += 2
      while (i < source.length && !(source[i] === '*' && source[i + 1] === '/'))
        i++
      i += 2
      continue
    }
    if (char === '`') {
      // Template literals cannot carry a static namespace; drop them wholesale so
      // documentation prose inside one is never mistaken for a call.
      i++
      while (i < source.length) {
        if (source[i] === '\\') {
          i += 2
          continue
        }
        if (source[i] === '`') {
          i++
          break
        }
        i++
      }
      out += '``'
      continue
    }
    if (char === "'" || char === '"') {
      const quote = char
      out += char
      i++
      while (i < source.length) {
        if (source[i] === '\\') {
          out += source.slice(i, i + 2)
          i += 2
          continue
        }
        out += source[i]
        if (source[i] === quote) {
          i++
          break
        }
        i++
      }
      continue
    }
    out += char
    i++
  }
  return out
}

function resolveSpecifier(specifier, fromFile, src) {
  let base
  if (specifier.startsWith('.'))
    base = path.resolve(path.dirname(fromFile), specifier)
  else if (specifier.startsWith('@/')) base = path.join(src, specifier.slice(2))
  else return null // bare package — not our code
  for (const suffix of RESOLVE_SUFFIXES) {
    const candidate = base + suffix
    if (fs.existsSync(candidate) && fs.statSync(candidate).isFile())
      return candidate
  }
  return null
}

/** Static + dynamic import edges, plus the namespaces this module uses/registers. */
function analyze(file, src, cache) {
  const cached = cache.get(file)
  if (cached) return cached
  const source = strip(fs.readFileSync(file, 'utf8'))
  const statics = new Set()
  const dynamics = new Set()
  const uses = new Set()
  const registers = new Set()

  // `import … from 'x'`, `export … from 'x'`, bare side-effect `import 'x'`.
  for (const match of source.matchAll(
    /(?:^|[;}\n])\s*(?:import|export)\s*(?:[\w*{}\s,$]*?\sfrom\s*)?['"]([^'"]+)['"]/g,
  ))
    statics.add(match[1])
  for (const match of source.matchAll(/\bimport\s*\(\s*['"]([^'"]+)['"]/g))
    dynamics.add(match[1])

  // `useTranslation('ns')` and `useTranslation(['ns', …])`.
  for (const match of source.matchAll(/\buseTranslation\s*\(\s*([^)]*)\)/g)) {
    const args = match[1]
    if (!args.trim()) {
      uses.add('common') // defaultNS
      continue
    }
    for (const literal of args.matchAll(/['"]([^'"]+)['"]/g)) uses.add(literal[1])
  }
  // Explicit `t('ns:key')` / `i18n.t('ns:key')` namespace prefixes.
  for (const match of source.matchAll(/\bt\s*\(\s*['"]([^'"\s:]+):[^'"]*['"]/g))
    uses.add(match[1])
  for (const match of source.matchAll(
    /\bregisterTranslations\s*\(\s*['"]([^'"]+)['"]/g,
  ))
    registers.add(match[1])

  const resolvedStatic = [...statics]
    .map((spec) => resolveSpecifier(spec, file, src))
    .filter(Boolean)
  const resolvedDynamic = [...dynamics]
    .map((spec) => resolveSpecifier(spec, file, src))
    .filter(Boolean)
  const result = {
    dynamics: resolvedDynamic,
    registers,
    statics: resolvedStatic,
    uses,
  }
  cache.set(file, result)
  return result
}

/**
 * Scan one application: `entry` is the eagerly loaded root module, `src` the base of
 * the `@/` alias, `root` the directory paths are reported relative to. Returns the
 * problems plus what was covered — pure, so the fixture battery can call it too.
 */
function scan({ entry, foundation = FOUNDATION, root, src }) {
  const cache = new Map()
  const facts = (file) => analyze(file, src, cache)

  /** Modules reachable from `roots` WITHOUT crossing a lazy boundary. */
  const staticClosure = (roots) => {
    const seen = new Set()
    const stack = [...roots]
    while (stack.length) {
      const file = stack.pop()
      if (seen.has(file)) continue
      seen.add(file)
      if (!SOURCE_EXTENSIONS.includes(path.extname(file))) continue
      for (const next of facts(file).statics) stack.push(next)
    }
    return seen
  }

  /** Every lazy-loaded chunk root reachable from the entry, transitively. */
  const chunkRoots = () => {
    const roots = new Set()
    const queue = [entry]
    const visited = new Set()
    while (queue.length) {
      const chunk = queue.shift()
      if (visited.has(chunk)) continue
      visited.add(chunk)
      for (const file of staticClosure([chunk])) {
        if (!SOURCE_EXTENSIONS.includes(path.extname(file))) continue
        for (const dynamic of facts(file).dynamics) {
          if (roots.has(dynamic)) continue
          roots.add(dynamic)
          queue.push(dynamic)
        }
      }
    }
    return roots
  }

  const rootClosure = staticClosure([entry])
  const problems = []
  let chunksChecked = 0

  for (const chunk of [entry, ...chunkRoots()]) {
    chunksChecked++
    const loaded = new Set([...rootClosure, ...staticClosure([chunk])])
    const registered = new Set(foundation)
    for (const file of loaded) {
      if (!SOURCE_EXTENSIONS.includes(path.extname(file))) continue
      for (const namespace of facts(file).registers) registered.add(namespace)
    }
    for (const file of [...loaded].sort()) {
      if (!SOURCE_EXTENSIONS.includes(path.extname(file))) continue
      for (const namespace of facts(file).uses) {
        if (registered.has(namespace)) continue
        problems.push({
          chunk: path.relative(root, chunk),
          file: path.relative(root, file),
          namespace,
        })
      }
    }
  }
  return { chunksChecked, modules: cache.size, problems }
}

if (process.argv.includes('--self-test')) {
  const { selfTest } = await import('./check-i18n-namespaces.selftest.mjs')
  process.exit(selfTest(scan) ? 0 : 1)
}

const { chunksChecked, modules, problems } = scan({
  entry: ENTRY,
  root: ROOT,
  src: SRC,
})

if (problems.length) {
  // One line per (module, namespace); name a chunk that would render it raw.
  const grouped = new Map()
  for (const problem of problems) {
    const key = `${problem.file}\0${problem.namespace}`
    const entry = grouped.get(key) ?? { ...problem, chunks: [] }
    entry.chunks.push(problem.chunk)
    grouped.set(key, entry)
  }
  console.error(
    `✗ i18n namespaces FAILED — ${grouped.size} module/namespace pair(s) render raw keys:`,
  )
  const verbose = process.argv.includes('--verbose')
  for (const entry of [...grouped.values()].sort((a, b) =>
    `${a.file}${a.namespace}`.localeCompare(`${b.file}${b.namespace}`),
  )) {
    console.error(
      `  ${entry.file}: namespace "${entry.namespace}" is never registered in ` +
        `${entry.chunks.length} chunk(s), e.g. ${entry.chunks[0]}`,
    )
    if (verbose) for (const chunk of entry.chunks) console.error(`      ${chunk}`)
  }
  console.error(
    '\n  Fix: the module that TRANSLATES registers its own namespace ' +
      "(`import './i18n'`), so every screen that renders it has the strings.",
  )
  process.exit(1)
}

// ⛔ NO HABER MIRADO NO ES ESTAR LIMPIO. Sin esta guarda, un escaneo que deja de reconocer
//    la disposición del árbol examina CERO chunks, no encuentra ningún problema, y sale 0 con un
//    «✓ … OK — 0». El verde se lee como la mejor de las tres respuestas cuando es la peor: no se
//    miró. Medido el 2026-08-17 sobre un árbol vacío, este guion salía 0.
if (!(chunksChecked > 0)) {
  console.error(
    `⛔ NO HE PODIDO MIRAR: el escaneo examinó 0 chunks. Un árbol sin nada que examinar NO es un
árbol en orden — es un escáner que dejó de reconocer la disposición. Revisa las rutas antes de
leer esto como verde.`,
  )
  process.exit(2)
}

console.log(
  `✓ i18n namespaces OK — ${chunksChecked} chunk(s), ${modules} module(s); ` +
    'every namespace used is registered in the loading chunk',
)
