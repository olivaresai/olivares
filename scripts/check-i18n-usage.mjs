// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Fail when a string-literal t('key') call in web/src does not resolve in the
// English i18n resources. This targets literal typo/rename regressions such as
// the items.* raw-render bug class. Dynamic/interpolated keys such as
// t(`items.${kind}`) are intentionally out of scope because they cannot be
// resolved statically.
//
// Pure Node (no deps); run from the repository root.

import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const SRC = path.join(ROOT, 'web', 'src')
const FOUNDATION = path.join(SRC, 'lib', 'i18n', 'locales', 'en')
const FEATURES = path.join(SRC, 'features')
const PLURAL_SUFFIXES = ['zero', 'one', 'two', 'few', 'many', 'other']

// Ratchet baseline: pinned occurrences that are NOT raw-key bugs, so
// the gate fails on any NEW unresolved literal while tolerating these. The
// missing common:errors.generic key and the common:close→actions.close mis-key
// that the guard first surfaced were FIXED at introduction, so only the
// natural-language default key below remains — it renders its own English text
// as the fallback, not a raw dotted key.
const BASELINE_UNRESOLVED = new Map([
  [
    'web/src/features/finops/chargeback-components.tsx\0No cost centers configured',
    1,
  ],
])

function walk(dir, accept, out = []) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) walk(full, accept, out)
    else if (accept(full)) out.push(full)
  }
  return out.sort()
}

function keyPaths(value, prefix = '', out = new Set()) {
  if (prefix) out.add(prefix)
  if (value && typeof value === 'object' && !Array.isArray(value)) {
    for (const [key, child] of Object.entries(value))
      keyPaths(child, prefix ? `${prefix}.${key}` : key, out)
  }
  return out
}

function namespaceForFeature(enPath) {
  const featureDir = path.dirname(path.dirname(enPath))
  const candidates = [
    path.join(featureDir, 'i18n', 'index.ts'),
    path.join(featureDir, 'index.ts'),
  ]
  for (const candidate of candidates) {
    if (!fs.existsSync(candidate)) continue
    const source = fs.readFileSync(candidate, 'utf8')
    const match = source.match(/registerTranslations\(\s*(['"])([^'"]+)\1/)
    if (match) return match[2]
  }
  return path.basename(featureDir)
}

function loadResources() {
  const resources = new Map()
  for (const file of walk(FOUNDATION, (p) => p.endsWith('.json'))) {
    resources.set(
      path.basename(file, '.json'),
      keyPaths(JSON.parse(fs.readFileSync(file, 'utf8'))),
    )
  }
  for (const file of walk(FEATURES, (p) =>
    p.endsWith(`${path.sep}i18n${path.sep}en.json`),
  )) {
    const namespace = namespaceForFeature(file)
    if (resources.has(namespace))
      throw new Error(`duplicate English i18n namespace: ${namespace}`)
    resources.set(
      namespace,
      keyPaths(JSON.parse(fs.readFileSync(file, 'utf8'))),
    )
  }
  return resources
}

function readQuoted(source, start, quote, line) {
  let value = ''
  let i = start + 1
  for (; i < source.length; i++) {
    const char = source[i]
    if (char === quote) return { end: i + 1, line, value }
    if (char === '\n') line++
    if (char !== '\\') {
      value += char
      continue
    }
    const escaped = source[++i]
    if (escaped === undefined) break
    if (escaped === 'n') value += '\n'
    else if (escaped === 'r') value += '\r'
    else if (escaped === 't') value += '\t'
    else value += escaped
  }
  return { end: i, line, value }
}

/** Lightweight lexer: enough structure for calls/scopes, while excluding comments and literals. */
function tokenize(source) {
  const tokens = []
  let i = 0
  let line = 1
  while (i < source.length) {
    const char = source[i]
    if (/\s/.test(char)) {
      if (char === '\n') line++
      i++
      continue
    }
    if (char === '/' && source[i + 1] === '/') {
      i += 2
      while (i < source.length && source[i] !== '\n') i++
      continue
    }
    if (char === '/' && source[i + 1] === '*') {
      i += 2
      while (
        i < source.length &&
        !(source[i] === '*' && source[i + 1] === '/')
      ) {
        if (source[i] === '\n') line++
        i++
      }
      i += 2
      continue
    }
    if (char === "'" || char === '"') {
      const tokenLine = line
      const quoted = readQuoted(source, i, char, line)
      tokens.push({ type: 'string', value: quoted.value, line: tokenLine })
      i = quoted.end
      line = quoted.line
      continue
    }
    if (char === '`') {
      // Template keys are dynamic/out of scope. Skip the whole template rather
      // than accidentally treating its text as source tokens.
      i++
      while (i < source.length) {
        if (source[i] === '\n') line++
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
      continue
    }
    if (/[A-Za-z_$]/.test(char)) {
      const start = i++
      while (i < source.length && /[A-Za-z0-9_$]/.test(source[i])) i++
      tokens.push({ type: 'identifier', value: source.slice(start, i), line })
      continue
    }
    tokens.push({ type: 'punctuation', value: char, line })
    i++
  }
  return tokens
}

function scopedTokens(source) {
  const tokens = tokenize(source)
  const scopes = [{ parent: null }]
  let current = 0
  for (const token of tokens) {
    token.scope = current
    if (token.value === '{') {
      scopes.push({ parent: current })
      current = scopes.length - 1
    } else if (token.value === '}' && scopes[current].parent !== null) {
      current = scopes[current].parent
    }
  }
  return { scopes, tokens }
}

function translatorBinding(tokens, hookIndex) {
  if (
    tokens[hookIndex - 1]?.value !== '=' ||
    tokens[hookIndex - 2]?.value !== '}'
  )
    return null
  let depth = 0
  let open = -1
  for (let i = hookIndex - 2; i >= 0; i--) {
    if (tokens[i].value === '}') depth++
    else if (tokens[i].value === '{') {
      depth--
      if (depth === 0) {
        open = i
        break
      }
    }
  }
  if (open < 0) return null
  for (let i = open + 1; i < hookIndex - 2; i++) {
    if (tokens[i].value !== 't') continue
    if (tokens[i + 1]?.value === ':') return tokens[i + 2]?.value ?? null
    return 't'
  }
  return null
}

function translationDeclarations(tokens) {
  const byScope = new Map()
  const fileNamespaces = []
  for (let i = 0; i < tokens.length; i++) {
    if (tokens[i].value !== 'useTranslation' || tokens[i + 1]?.value !== '(')
      continue
    if (translatorBinding(tokens, i) !== 't') continue
    let cursor = i + 2
    if (tokens[cursor]?.value === '[') cursor++
    const namespace =
      tokens[cursor]?.type === 'string' ? tokens[cursor].value : 'common'
    const declaration = { index: i, namespace }
    const list = byScope.get(tokens[i].scope) ?? []
    list.push(declaration)
    byScope.set(tokens[i].scope, list)
    fileNamespaces.push(namespace)
  }
  return { byScope, fileNamespaces: [...new Set(fileNamespaces)] }
}

function nearestNamespaces(token, index, scopes, declarations) {
  let scope = token.scope
  while (scope !== null) {
    const nearest = (declarations.byScope.get(scope) ?? [])
      .filter((item) => item.index < index)
      .at(-1)
    if (nearest) return [nearest.namespace]
    scope = scopes[scope].parent
  }
  return declarations.fileNamespaces.length
    ? declarations.fileNamespaces
    : ['common']
}

function literalTranslationCalls(source) {
  const { scopes, tokens } = scopedTokens(source)
  const declarations = translationDeclarations(tokens)
  const calls = []
  for (let i = 0; i < tokens.length; i++) {
    const token = tokens[i]
    if (
      token.type !== 'identifier' ||
      token.value !== 't' ||
      tokens[i - 1]?.value === '.' ||
      tokens[i + 1]?.value !== '(' ||
      tokens[i + 2]?.type !== 'string' ||
      ![',', ')'].includes(tokens[i + 3]?.value)
    )
      continue

    const literal = tokens[i + 2].value
    const colon = literal.indexOf(':')
    if (colon > 0) {
      calls.push({
        key: literal.slice(colon + 1),
        line: token.line,
        literal,
        namespaces: [literal.slice(0, colon)],
      })
    } else {
      calls.push({
        key: literal,
        line: token.line,
        literal,
        namespaces: nearestNamespaces(token, i, scopes, declarations),
      })
    }
  }
  return calls
}

function resolves(resources, namespace, key) {
  const keys = resources.get(namespace)
  if (!keys) return false
  if (keys.has(key)) return true
  return PLURAL_SUFFIXES.some((suffix) => keys.has(`${key}_${suffix}`))
}

const resources = loadResources()
const problems = []
const baselineSeen = new Map()
let checked = 0

for (const file of walk(SRC, (p) => /\.tsx?$/.test(p))) {
  for (const call of literalTranslationCalls(fs.readFileSync(file, 'utf8'))) {
    checked++
    if (
      call.namespaces.some((namespace) =>
        resolves(resources, namespace, call.key),
      )
    )
      continue
    const relativeFile = path.relative(ROOT, file)
    const baselineKey = `${relativeFile}\0${call.literal}`
    const seen = baselineSeen.get(baselineKey) ?? 0
    const allowed = BASELINE_UNRESOLVED.get(baselineKey) ?? 0
    if (seen < allowed) {
      baselineSeen.set(baselineKey, seen + 1)
      continue
    }
    problems.push({ ...call, file: relativeFile })
  }
}

if (problems.length) {
  console.error(
    `✗ i18n usage FAILED — ${problems.length} unresolved literal key(s):`,
  )
  for (const problem of problems) {
    console.error(
      `  ${problem.file}:${problem.line}: t(${JSON.stringify(problem.literal)}) ` +
        `(namespace${problem.namespaces.length === 1 ? '' : 's'}: ${problem.namespaces.join(', ')})`,
    )
  }
  process.exit(1)
}

const baselineCount = [...baselineSeen.values()].reduce(
  (sum, count) => sum + count,
  0,
)
// ⛔ NO HABER MIRADO NO ES ESTAR LIMPIO. Sin esta guarda, un escaneo que deja de reconocer
//    la disposición del árbol examina CERO llamadas literales a t(), no encuentra ningún problema, y sale 0 con un
//    «✓ … OK — 0». El verde se lee como la mejor de las tres respuestas cuando es la peor: no se
//    miró. Medido el 2026-08-17 sobre un árbol vacío, este guion salía 0.
if (!(checked > 0)) {
  console.error(
    `⛔ NO HE PODIDO MIRAR: el escaneo examinó 0 llamadas literales a t(). Un árbol sin nada que examinar NO es un
árbol en orden — es un escáner que dejó de reconocer la disposición. Revisa las rutas antes de
leer esto como verde.`,
  )
  process.exit(2)
}

console.log(
  `✓ i18n usage OK — ${checked} literal t() call(s), ${resources.size} English namespace(s); ` +
    `${baselineCount} pre-existing unresolved occurrence(s) pinned`,
)
