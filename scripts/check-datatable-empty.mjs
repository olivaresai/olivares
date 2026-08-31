// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Fail when a console table can render the GENERIC empty state — the bug class where
// `DataTable` fell back to `t('states.noResults')` ("No results") whenever the caller
// omitted `empty`. On a clean install every list is empty, so that string was the
// customer's FIRST screen on 69 surfaces across 36 files. It was never 69 design
// decisions; it was one absent decision inherited 69 times from a default that should
// not have existed.
//
// `empty` is a REQUIRED `ReactElement`, so tsc already rejects both omission and
// `empty={undefined}` — the shape six sites actually shipped, and the one a required
// `ReactNode` would have waved through, because `ReactNode` includes `undefined`.
// This gate exists because a type can be widened in the same commit that reintroduces
// the defect, and because these shapes are type-correct and invisible to tsc:
//
//   - an INERT element — `<></>`, `<Fragment />`, or a local `const x = <></>` passed
//     by name — "no decision" spelled as a value;
//   - a GENERIC element — the caller hand-rolling `t('states.noResults')` back in;
//   - an OPAQUE site — a `{...spread}` that can overwrite `empty` at runtime with
//     something this gate never saw.
//
// WHAT IT STILL CANNOT DECIDE, stated rather than papered over: `empty={<Blank />}`
// where `Blank` is a component that returns null is type-correct and renders nothing,
// and proving that needs whole-program analysis this gate does not do. The difference
// that matters is that writing such a component is a deliberate act a reviewer can
// see, not the silent omission this class was made of.
//
// Scanning notes, each of which is a mistake made while MEASURING the defect or found
// by the closing contrast, and therefore a case in the red battery:
//
//   - `<DataTable<Row> …>` carries a TYPE-ARGUMENT list whose `>` is not the end of
//     the tag. Reading it as such hides every prop after it — that alone moved the
//     count by two, in the direction that reports a defect where none exists.
//   - `empty={…}` is routinely split across lines, so a line-oriented grep does not
//     see it. Tags are walked with brace/paren/bracket depth instead.
//   - a component named in a COMMENT or inside a string is not a call site, so the
//     match position is chosen on a fully masked copy…
//   - …but the tag is then WALKED on a copy with only comments blanked. Walking the
//     raw source let a comment inside the tag inject a second `empty=` that overrode
//     the real one, and let a `>` inside a string attribute end the tag early. Masking
//     strings for the walk too would have blinded the generic check, which looks for a
//     string literal.
//   - the component can arrive under a LOCAL ALIAS (`import { DataTable as Grid }`) or
//     a namespace (`import * as T`). Matching the literal name only means an aliased
//     call site counts as zero sites — a gate that reports green because it looked at
//     nothing.
//
// Pure Node (no deps); run from the repository root.

import fs from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const SRC = path.join(ROOT, 'web', 'src')

/** Components whose empty state is a product decision, not a default. */
const GUARDED = ['DataTable', 'AccessibleChart']

/** Module specifiers that can hand out a guarded component (barrels included). */
const GUARDED_MODULE = /(^|\/)(data-table|accessible-chart|components\/data)$/

/** `empty` values that are type-correct but mean "no decision". */
const INERT =
  /^(undefined|null|false|''|""|``|<>\s*<\/>|<(React\.)?Fragment\s*\/>|<(React\.)?Fragment>\s*<\/(React\.)?Fragment>)$/

/** The generic itself, however it is spelled back in. */
const GENERIC = /states\.noResults|(['"`])No results\1/

const ID = /[A-Za-z0-9_$]/

/** Blank a span in a char array, keeping newlines so line numbers survive. */
function blank(out, from, to) {
  for (let i = from; i < to && i < out.length; i++) if (out[i] !== '\n') out[i] = ' '
}

/** Walk source once, reporting comment and string spans. Offsets are preserved. */
function spans(source) {
  const comments = []
  const strings = []
  let i = 0
  while (i < source.length) {
    const char = source[i]
    if (char === '/' && source[i + 1] === '/') {
      let j = i
      while (j < source.length && source[j] !== '\n') j++
      comments.push([i, j])
      i = j
      continue
    }
    if (char === '/' && source[i + 1] === '*') {
      let j = i + 2
      while (j < source.length && !(source[j] === '*' && source[j + 1] === '/')) j++
      comments.push([i, Math.min(j + 2, source.length)])
      i = j + 2
      continue
    }
    if (char === '"' || char === "'" || char === '`') {
      let j = i + 1
      while (j < source.length) {
        if (source[j] === '\\') {
          j += 2
          continue
        }
        if (source[j] === char) {
          j++
          break
        }
        j++
      }
      strings.push([i, j])
      i = j
      continue
    }
    i++
  }
  return { comments, strings }
}

/** Comments AND strings blanked — used to CHOOSE where a call site starts. */
export function maskCode(source) {
  const out = source.split('')
  const { comments, strings } = spans(source)
  for (const [a, b] of comments) blank(out, a, b)
  for (const [a, b] of strings) blank(out, a, b)
  return out.join('')
}

/** Only comments blanked — used to WALK a tag, so the generic string stays readable. */
export function maskComments(source) {
  const out = source.split('')
  for (const [a, b] of spans(source).comments) blank(out, a, b)
  return out.join('')
}

/** Skip a `<…>` type-argument list; returns the index just past its closing `>`. */
function skipTypeArguments(source, start) {
  if (source[start] !== '<') return start
  let depth = 0
  let i = start
  while (i < source.length) {
    const char = source[i]
    if (char === '"' || char === "'" || char === '`') {
      const quote = char
      i++
      while (i < source.length) {
        if (source[i] === '\\') {
          i += 2
          continue
        }
        if (source[i] === quote) {
          i++
          break
        }
        i++
      }
      continue
    }
    if (char === '<') {
      depth++
      i++
      continue
    }
    if (char === '>') {
      // `=>` inside a function type is not a closing angle bracket.
      if (source[i - 1] === '=') {
        i++
        continue
      }
      depth--
      i++
      if (depth === 0) return i
      continue
    }
    i++
  }
  return i
}

/**
 * Return the opening tag's attribute region, walking nesting rather than lines.
 * `source` here is the COMMENT-MASKED copy: string literals must still be readable
 * (the generic check reads one) but must not be walked as code.
 */
function tagBody(source, afterName) {
  let i = afterName
  while (i < source.length && /\s/.test(source[i])) i++
  if (source[i] === '<') i = skipTypeArguments(source, i)
  const start = i
  let depth = 0
  while (i < source.length) {
    const char = source[i]
    if (char === '"' || char === "'" || char === '`') {
      const quote = char
      i++
      while (i < source.length) {
        if (source[i] === '\\') {
          i += 2
          continue
        }
        if (source[i] === quote) {
          i++
          break
        }
        i++
      }
      continue
    }
    if (char === '{' || char === '(' || char === '[') {
      depth++
      i++
      continue
    }
    if (char === '}' || char === ')' || char === ']') {
      depth--
      i++
      continue
    }
    if (depth === 0) {
      if (char === '>') return { body: source.slice(start, i), closed: true }
      if (char === '/' && source[i + 1] === '>') return { body: source.slice(start, i), closed: true }
    }
    i++
  }
  return { body: source.slice(start), closed: false }
}

/** Split an attribute region into `{ name: expression | true }`, plus spread order. */
function parseProps(body) {
  const props = {}
  const order = []
  let i = 0
  while (i < body.length) {
    while (i < body.length && /[\s/]/.test(body[i])) i++
    if (i >= body.length) break
    if (body[i] === '{') {
      let depth = 1
      let j = i + 1
      while (j < body.length && depth > 0) {
        if (body[j] === '{') depth++
        else if (body[j] === '}') depth--
        j++
      }
      order.push('...spread')
      i = j
      continue
    }
    if (!ID.test(body[i])) {
      i++
      continue
    }
    let j = i
    while (j < body.length && (ID.test(body[j]) || body[j] === '-')) j++
    const name = body.slice(i, j)
    let k = j
    while (k < body.length && /\s/.test(body[k])) k++
    if (body[k] !== '=') {
      props[name] = true
      order.push(name)
      i = j
      continue
    }
    k++
    while (k < body.length && /\s/.test(body[k])) k++
    if (body[k] === '"' || body[k] === "'") {
      const quote = body[k]
      let n = k + 1
      while (n < body.length && body[n] !== quote) {
        if (body[n] === '\\') n++
        n++
      }
      props[name] = body.slice(k, n + 1)
      order.push(name)
      i = n + 1
      continue
    }
    if (body[k] === '{') {
      let depth = 1
      let n = k + 1
      while (n < body.length && depth > 0) {
        const char = body[n]
        if (char === '"' || char === "'" || char === '`') {
          const quote = char
          n++
          while (n < body.length) {
            if (body[n] === '\\') {
              n += 2
              continue
            }
            if (body[n] === quote) {
              n++
              break
            }
            n++
          }
          continue
        }
        if (char === '{') depth++
        else if (char === '}') depth--
        n++
      }
      props[name] = body.slice(k + 1, n - 1).trim()
      order.push(name)
      i = n
      continue
    }
    i = k + 1
  }
  return { props, order }
}

/**
 * Local names a guarded component answers to in this file. Matching the literal name
 * only lets `import { DataTable as Grid }` walk past the gate entirely.
 *
 * Takes the COMMENT-masked copy, not the fully masked one: the module specifier IS a
 * string literal, so the fully masked copy has blanked the very thing to resolve.
 */
function localNames(masked, guarded) {
  const names = new Map() // localName -> canonical component
  const importRe = /import\s+([^;]*?)\s+from\s+['"]([^'"]+)['"]/g
  let match
  while ((match = importRe.exec(masked)) !== null) {
    const [, clause, specifier] = match
    if (!GUARDED_MODULE.test(specifier.replace(/\.[jt]sx?$/, ''))) continue
    const namespace = clause.match(/\*\s+as\s+([A-Za-z0-9_$]+)/)
    if (namespace) {
      for (const component of guarded) names.set(`${namespace[1]}.${component}`, component)
      continue
    }
    const braces = clause.match(/\{([^}]*)\}/)
    if (!braces) continue
    for (const part of braces[1].split(',')) {
      const [imported, alias] = part.split(/\s+as\s+/).map((s) => s.trim())
      if (!guarded.includes(imported)) continue
      names.set(alias || imported, imported)
    }
  }
  // A file that reaches a guarded component through a barrel this resolver does not
  // model still gets checked under the literal name: never fewer sites than before.
  for (const component of guarded) if (!names.has(component)) names.set(component, component)
  return names
}

/** Resolve `empty={someName}` to a same-file `const someName = …` initialiser. */
function resolveLocal(masked, name) {
  if (!/^[A-Za-z_$][A-Za-z0-9_$]*$/.test(name)) return null
  const declRe = new RegExp(`(?:^|\\n)\\s*const\\s+${name}\\s*(?::[^=]+)?=\\s*`, 'g')
  const match = declRe.exec(masked)
  if (!match) return null
  const start = match.index + match[0].length
  let i = start
  let depth = 0
  while (i < masked.length) {
    const char = masked[i]
    if (char === '(' || char === '[' || char === '{') depth++
    else if (char === ')' || char === ']' || char === '}') {
      if (depth === 0) break
      depth--
    } else if (depth === 0 && (char === '\n' || char === ';')) {
      const ahead = masked.slice(i + 1).match(/^\s*[.?:]/)
      if (!ahead) break
    }
    i++
  }
  return masked.slice(start, i).replace(/\s+/g, ' ').trim()
}

const isTest = (file) =>
  /\.(test|spec)\.[jt]sx?$/.test(file) || /(^|\/)(__tests__|__mocks__|e2e)\//.test(file)

function sourceFiles(dir, root, acc = []) {
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) {
      if (entry.name === 'node_modules' || entry.name === 'dist') continue
      sourceFiles(full, root, acc)
      continue
    }
    if (!/\.(tsx|ts)$/.test(entry.name)) continue
    const rel = path.relative(root, full).split(path.sep).join('/')
    if (isTest(rel)) continue
    acc.push({ rel, full })
  }
  return acc
}

/**
 * Scan a source tree; returns every guarded call site and every problem found.
 * `disable` drops named clauses — the self-test uses it to prove each one does work.
 * EVERY clause that can push a problem must be listed there, or it is a live check
 * nobody has watched fail: `unterminated` was exactly that until a contrast turned it
 * into `if (!closed && false)` and the battery stayed green.
 */
export function scan({ src, root, guarded = GUARDED, disable = [] }) {
  const off = (clause) => disable.includes(clause)
  const sites = []
  const problems = []
  for (const { rel, full } of sourceFiles(src, root)) {
    const source = fs.readFileSync(full, 'utf8')
    const masked = maskCode(source)
    const walkable = maskComments(source)
    for (const [local, component] of localNames(walkable, guarded)) {
      const pattern = new RegExp(`<${local.replace('.', '\\.')}(?![A-Za-z0-9_$])`, 'g')
      let match
      while ((match = pattern.exec(masked)) !== null) {
        const from = match.index + match[0].length
        const { body, closed } = tagBody(walkable, from)
        const { props, order } = parseProps(body)
        const line = source.slice(0, match.index).split('\n').length
        const site = { file: rel, line, component, local, at: `${rel}:${line}` }
        sites.push(site)
        if (!closed) {
          if (!off('unterminated')) problems.push({ ...site, kind: 'unterminated', value: null })
          continue
        }
        if (!('empty' in props)) {
          if (!off('missing')) problems.push({ ...site, kind: 'missing', value: null })
          continue
        }
        // A spread anywhere after `empty` can overwrite it at runtime with something
        // never seen here. Deny-closed: unverifiable is not clean.
        if (!off('opaque') && order.indexOf('...spread') > order.indexOf('empty')) {
          problems.push({ ...site, kind: 'opaque', value: '{...spread} after empty' })
          continue
        }
        let value = String(props.empty).replace(/\s+/g, ' ').trim()
        const resolved = resolveLocal(walkable, value)
        if (resolved) value = resolved
        if (!off('inert') && INERT.test(value)) problems.push({ ...site, kind: 'inert', value })
        else if (!off('generic') && GENERIC.test(value))
          problems.push({ ...site, kind: 'generic', value })
      }
    }
  }
  return { sites, problems }
}

const EXPLAIN = {
  missing: 'has no `empty` prop — the table cannot say what it means when it is empty',
  inert: 'passes an `empty` that renders nothing — "no decision" spelled as a value',
  generic: 'hand-rolls the generic back in — that string IS the defect',
  opaque: 'is followed by a spread that can overwrite `empty` with something unseen',
  unterminated: 'has an opening tag this gate could not read to its end',
}

if (process.argv.includes('--self-test')) {
  const { selfTest } = await import('./check-datatable-empty.selftest.mjs')
  process.exit(selfTest(scan) ? 0 : 1)
}

const { sites, problems } = scan({ src: SRC, root: ROOT })

if (problems.length) {
  console.error(`✗ datatable empty-state FAILED — ${problems.length} site(s):`)
  for (const problem of problems.sort((a, b) => a.at.localeCompare(b.at))) {
    console.error(
      `  ${problem.at}: <${problem.local}> ${EXPLAIN[problem.kind]}` +
        (problem.value ? ` (empty={${problem.value.slice(0, 60)}})` : ''),
    )
  }
  console.error(
    '\n  Fix: pass an EmptyState whose title names what is absent and whose\n' +
      '  description says what makes rows appear — see the bar in\n' +
      '  web/src/features/sessions/i18n/en.json ("No sessions observed yet").\n' +
      '  "Empty" is not "I could not read it", and it is not "your filter matched\n' +
      '  nothing" — DataTable tells those apart on `data.length`.',
  )
  process.exit(1)
}

// ⛔ NO HABER MIRADO NO ES ESTAR LIMPIO. Sin esta guarda, un escaneo que deja de reconocer
//    la disposición del árbol examina CERO sitios de llamada, no encuentra ningún problema, y sale 0 con un
//    «✓ … OK — 0». El verde se lee como la mejor de las tres respuestas cuando es la peor: no se
//    miró. Medido el 2026-08-17 sobre un árbol vacío, este guion salía 0.
if (!(sites.length > 0)) {
  console.error(
    `⛔ NO HE PODIDO MIRAR: el escaneo examinó 0 sitios de llamada. Un árbol sin nada que examinar NO es un
árbol en orden — es un escáner que dejó de reconocer la disposición. Revisa las rutas antes de
leer esto como verde.`,
  )
  process.exit(2)
}

console.log(
  `✓ datatable empty-state OK — ${sites.length} guarded call site(s) across ` +
    `${new Set(sites.map((s) => s.file)).size} file(s); every one names its own empty state`,
)
