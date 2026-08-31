// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// console-dump.mjs — STAGE 1 of the console-guide gate: enumerate the console's routes
// from the console's own sources, as JSON, so the renderer never has to read TypeScript.
//
// WHY A SEPARATE STAGE, and why TypeScript and not a scan. The roster the guide must
// cover is `web/src/features/route-census.json` — 57 paths, append-only, and pinned
// against the BUILT ROUTER by registry.route-conservation.test.ts, which is why this
// file does not re-derive the roster. What the census does NOT carry is what an operator
// needs to be told about each screen: its stable id (the i18n key), the RBAC permission
// the route demands, and the docs page the console's own help link points at. Those live
// as object-literal properties in web/src/features/registry.tsx, and the only honest way
// to read a property out of TypeScript is the TypeScript compiler. A regex over that file
// would report a permission for a view whose `permission:` line sits inside a comment,
// and would miss one written across two lines by the formatter.
//
// It parses with the SAME compiler the console is built with (web/node_modules), the
// discipline scripts/check-console-perms.mjs established: a guard that parses the console
// with a different compiler is reading a different language than the one that ships.
//
// It DUMPS; it does not judge. Every comparison — census against registry, catalog
// against roster, page against render — belongs to the Go half, which owns the verdict
// and the exit codes. This stage has exactly two outcomes: JSON on stdout, or a
// diagnosed failure on stderr with exit 2 (CANNOT LOOK). It never prints a partial
// roster: an empty or suspiciously small FEATURE_VIEWS is a failure, not a dump.
//
// Usage:
//   node scripts/guide-docs/console-dump.mjs [--root DIR]
// Exit: 0 JSON written · 2 could not enumerate (cause named). There is no exit 1: this
// stage has no opinion about whether the docs are right.

import { existsSync, readdirSync, readFileSync } from 'node:fs'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath, pathToFileURL } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const argv = process.argv.slice(2)
let ROOT = path.resolve(HERE, '..', '..')
for (let i = 0; i < argv.length; i++) {
  if (argv[i] === '--root') {
    if (!argv[i + 1]) die('--root needs a directory')
    ROOT = path.resolve(argv[i + 1])
    i++
  } else {
    die(`unknown argument ${argv[i]}`)
  }
}
const WEB = path.join(ROOT, 'web')

// A tree that mounts fewer views than this is not a console — it is a fixture, a failed
// checkout or a parse that silently matched nothing. The floor is deliberately far below
// today's 52 registry entries so ordinary product work never trips it, and far above zero
// so "I parsed nothing" can never be dumped as "the console has no screens".
const MIN_VIEWS = 20

function die(msg) {
  process.stderr.write(`console-dump: CANNOT LOOK — ${msg}\n`)
  process.exit(2)
}

// --- the compiler, from the console's own node_modules -----------------------------
async function resolveTypeScript() {
  for (const base of [path.join(WEB, 'node_modules'), path.join(ROOT, 'node_modules')]) {
    const direct = path.join(base, 'typescript', 'lib', 'typescript.js')
    if (existsSync(direct)) return direct
    const store = path.join(base, '.pnpm')
    if (!existsSync(store)) continue
    let hit
    try {
      hit = readdirSync(store)
        .filter((d) => d.startsWith('typescript@'))
        .sort()
        .pop()
    } catch {
      continue
    }
    if (hit) {
      const p = path.join(store, hit, 'node_modules', 'typescript', 'lib', 'typescript.js')
      if (existsSync(p)) return p
    }
  }
  die(
    'no TypeScript compiler under web/node_modules or node_modules; run `pnpm --dir web ' +
      'install`. Refusing to guess: without the compiler the console was never read',
  )
}

const ts = (await import(pathToFileURL(await resolveTypeScript()).href)).default

function parseFile(rel) {
  const abs = path.join(ROOT, rel)
  if (!existsSync(abs)) die(`${rel} does not exist, so the console's routes were never read`)
  let text
  try {
    text = readFileSync(abs, 'utf8')
  } catch (e) {
    die(`${rel} is unreadable: ${e.message}`)
  }
  return ts.createSourceFile(abs, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
}

/** The literal value of an object-literal property, or undefined when it is not a literal. */
function literal(obj, name) {
  for (const p of obj.properties) {
    if (!ts.isPropertyAssignment(p)) continue
    const key = ts.isIdentifier(p.name) || ts.isStringLiteral(p.name) ? p.name.text : null
    if (key !== name) continue
    const v = p.initializer
    if (ts.isStringLiteral(v) || ts.isNoSubstitutionTemplateLiteral(v)) return v.text
    if (v.kind === ts.SyntaxKind.TrueKeyword) return true
    if (v.kind === ts.SyntaxKind.FalseKeyword) return false
    return undefined
  }
  return undefined
}

/** Whether the property is present at all — "absent" and "not a literal" are different. */
function hasProp(obj, name) {
  return obj.properties.some(
    (p) =>
      ts.isPropertyAssignment(p) &&
      (ts.isIdentifier(p.name) || ts.isStringLiteral(p.name)) &&
      p.name.text === name,
  )
}

function where(node, sf) {
  const { line } = sf.getLineAndCharacterOfPosition(node.getStart(sf))
  return `${path.relative(ROOT, sf.fileName)}:${line + 1}`
}

// --- FEATURE_VIEWS -----------------------------------------------------------------
const REGISTRY_REL = 'web/src/features/registry.tsx'
const registry = parseFile(REGISTRY_REL)

let viewsArray = null
for (const stmt of registry.statements) {
  if (!ts.isVariableStatement(stmt)) continue
  for (const d of stmt.declarationList.declarations) {
    if (!ts.isIdentifier(d.name) || d.name.text !== 'FEATURE_VIEWS') continue
    if (!d.initializer || !ts.isArrayLiteralExpression(d.initializer)) {
      die(`${REGISTRY_REL} declares FEATURE_VIEWS but not as an array literal, so it could not be walked`)
    }
    viewsArray = d.initializer
  }
}
if (!viewsArray) die(`${REGISTRY_REL} declares no FEATURE_VIEWS, so no console route was enumerated`)

// HUB_ORDER is the console's own section order ("run it → let it run → plug it in → rule
// it → show it"). It is carried through rather than restated in the renderer: a fixed
// order written twice is an order that will disagree with itself the first time a hub is
// added, and the guide's headings would stop matching the sidebar an operator is reading.
let hubOrder = null
for (const stmt of registry.statements) {
  if (!ts.isVariableStatement(stmt)) continue
  for (const d of stmt.declarationList.declarations) {
    if (!ts.isIdentifier(d.name) || d.name.text !== 'HUB_ORDER') continue
    if (!d.initializer || !ts.isArrayLiteralExpression(d.initializer)) {
      die(`${REGISTRY_REL} declares HUB_ORDER but not as an array literal`)
    }
    hubOrder = d.initializer.elements.map((e) => {
      if (!ts.isStringLiteral(e)) die(`${REGISTRY_REL} HUB_ORDER holds a non-literal entry at ${where(e, registry)}`)
      return e.text
    })
  }
}
if (!hubOrder || hubOrder.length === 0) {
  die(`${REGISTRY_REL} declares no HUB_ORDER, so the guide has no section order to follow`)
}

const views = []
for (const el of viewsArray.elements) {
  if (!ts.isObjectLiteralExpression(el)) {
    die(`a FEATURE_VIEWS element at ${where(el, registry)} is not an object literal; refusing to dump a partial roster`)
  }
  const id = literal(el, 'id')
  const routePath = literal(el, 'path')
  if (typeof id !== 'string' || id === '') die(`a FEATURE_VIEWS entry at ${where(el, registry)} has no literal id`)
  if (typeof routePath !== 'string' || routePath === '') {
    die(`FEATURE_VIEWS entry "${id}" at ${where(el, registry)} has no literal path`)
  }
  // A property that EXISTS but is not a literal is a finding, never a silent absence:
  // "this view needs no permission" and "this view's permission was computed and I
  // could not read it" are opposite facts and must not collapse into the same dump.
  for (const field of ['permission', 'helpHref', 'hub']) {
    if (hasProp(el, field) && literal(el, field) === undefined) {
      die(`FEATURE_VIEWS entry "${id}" at ${where(el, registry)} writes ${field} as a non-literal expression, which this dump cannot read`)
    }
  }
  views.push({
    id,
    path: routePath,
    hub: literal(el, 'hub') ?? '',
    permission: literal(el, 'permission') ?? '',
    helpHref: literal(el, 'helpHref') ?? '',
    hideInNav: literal(el, 'hideInNav') === true,
    where: where(el, registry),
  })
}
if (views.length < MIN_VIEWS) {
  die(`FEATURE_VIEWS yielded only ${views.length} entries (floor ${MIN_VIEWS}); that is a parse that matched almost nothing, not a console`)
}

// --- the routes mounted OUTSIDE the registry ---------------------------------------
// The public legs (/login, /setup, /accept-invite, /status-page) and /settings are
// createRoute() calls in app/routes.tsx. They are routes the operator can reach and the
// census records them, so a guide that covered only FEATURE_VIEWS would be short by five
// screens and would not know it. `getParentRoute: () => rootRoute` is what makes a leg
// public: rootRoute has no auth guard, appRoute is the guarded shell.
const ROUTES_REL = 'web/src/app/routes.tsx'
const routes = parseFile(ROUTES_REL)
const standalone = []
;(function walk(node) {
  if (
    ts.isCallExpression(node) &&
    ts.isIdentifier(node.expression) &&
    node.expression.text === 'createRoute' &&
    node.arguments.length === 1 &&
    ts.isObjectLiteralExpression(node.arguments[0])
  ) {
    const obj = node.arguments[0]
    const p = literal(obj, 'path')
    if (typeof p === 'string' && p !== '') {
      let parent = ''
      for (const prop of obj.properties) {
        if (!ts.isPropertyAssignment(prop)) continue
        const key = ts.isIdentifier(prop.name) ? prop.name.text : null
        if (key !== 'getParentRoute') continue
        const body = ts.isArrowFunction(prop.initializer) ? prop.initializer.body : null
        if (body && ts.isIdentifier(body)) parent = body.text
      }
      if (parent === '') {
        die(`a createRoute at ${where(node, routes)} mounts "${p}" with a parent this dump could not read`)
      }
      standalone.push({ path: p, parent, authenticated: parent !== 'rootRoute', where: where(node, routes) })
    }
  }
  ts.forEachChild(node, walk)
})(routes)

// --- the census, carried through so the renderer reads ONE input --------------------
const CENSUS_REL = 'web/src/features/route-census.json'
const censusAbs = path.join(ROOT, CENSUS_REL)
if (!existsSync(censusAbs)) die(`${CENSUS_REL} does not exist, so there is no route roster to cover`)
let census
try {
  census = JSON.parse(readFileSync(censusAbs, 'utf8'))
} catch (e) {
  die(`${CENSUS_REL} is not valid JSON: ${e.message}`)
}
if (!Array.isArray(census?.paths) || census.paths.length === 0) {
  die(`${CENSUS_REL} carries no "paths" array, so the console roster is unknown`)
}
for (const p of census.paths) {
  if (typeof p !== 'string' || !p.startsWith('/')) die(`${CENSUS_REL} lists a path that is not a route: ${JSON.stringify(p)}`)
}

process.stdout.write(
  JSON.stringify(
    {
      schema: 'olivares.console.routes/1',
      hubOrder,
      census: [...census.paths].sort(),
      views: views.sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0)),
      standalone: standalone.sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0)),
    },
    null,
    2,
  ) + '\n',
)
