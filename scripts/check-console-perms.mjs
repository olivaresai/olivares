// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// check-console-perms — the console's permission checks must agree with the engine.
//
// HISTORY, because it is why both halves below exist. web/src/lib/auth/rbac.ts used to
// MIRROR the RBAC rule client-side, and two implementations of one rule drift: verbOf
// fell back to 'read' for anything it did not recognise, where the engine's Verb()
// returns "", and the verb tier was applied to core permissions too, where RoleGrants
// consults an explicit set. A permission the engine had never heard of therefore became,
// client side, a read that every viewer holds. Measured: thirteen such permissions, plus
// six the engine grants at a higher tier than the console did.
//
// The mirror is GONE. The engine hands each grant the principal's effective
// permission set in GET /v1/auth/whoami, and can() is set membership. That removes the
// second implementation but not the need for this guard, because it moves the failure
// mode rather than deleting it. Two things are enforced:
//
//   Every permission string that can reach can() must be one the ENGINE declares.
//     A string the engine declares in no form can appear in no effective set, so the
//     action is hidden for every role, permanently, and nothing 403s to reveal it. The
//     old defect granted silently; this one hides silently. Both need a gate.
//
//   can() must be HONEST SET MEMBERSHIP and nothing else.
//     The console half is executed with a principal whose set is the one the ENGINE
//     computes, and its answer must equal the engine's for every built-in role. That is
//     a tautology for a correct can() — which is the point: it is not checking data, it
//     is checking that no verb arithmetic, no tier and no special case has grown back on
//     top of the lookup. Every one of those is how this console shipped its defects.
//
// HOW IT AVOIDS THE TWO WAYS THIS CHECK GOES WRONG
//
// 1. It runs both sides; it models neither. The engine's half comes from
//    `go run ./tools/permsdump` — auth.RoleGrants itself, plus each module's
//    Permissions() and each mounted route's requirement. The console's half comes
//    from importing rbac.ts and CALLING can(). A guard that re-derives either rule
//    creates the third copy.
//
//    This matters most for the permissions nobody wrote down: the core set is
//    CONCATENATED at init, so "session:read" is "session" + ":" + VerbRead and
//    appears as a literal nowhere. Cross-checking the console against grep-able
//    declarations reports 48 candidates of which 45 are ordinary core permissions.
//    A guard that cries wolf 45 times out of 48 is switched off inside a week.
//
// 2. It refuses to be silent about what it cannot read. A can() argument this
//    script fails to resolve is reported and FAILS the run — never skipped. An
//    unresolved argument is the one place a real divergence could hide, so
//    "I could not read it" must cost the same as "it is wrong". Resolution
//    understands string literals, module constants, object-literal properties,
//    interface properties written by object literals (the nav registry) and
//    component props written by JSX attributes; anything else is a finding.
//
// Usage:
//   node scripts/check-console-perms.mjs            # check
//   node scripts/check-console-perms.mjs --json     # findings as JSON
// Exit: 0 clean · 1 findings · 2 could not run the check at all (fail-closed).

import { execFileSync } from 'node:child_process'
import { accessSync, constants as fsConstants, existsSync, readdirSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const REPO = process.env.OLIVARES_CONSOLE_PERMS_REPO ?? path.resolve(HERE, '..')
const WEB = path.join(REPO, 'web')
const ROLES = ['viewer', 'editor', 'admin', 'owner']
const TENANT = 'tenant-under-test'

const args = new Set(process.argv.slice(2))
const asJson = args.has('--json')

function die(msg) {
  console.error(`check-console-perms: ${msg}`)
  process.exitCode = 2
  process.exit(2)
}

function readableFile(p) {
  try {
    accessSync(p, fsConstants.R_OK)
    return true
  } catch {
    return false
  }
}

// --- typescript ------------------------------------------------------------
// Loaded from the workspace's own pnpm store: the guard must parse the console
// with the SAME compiler the console is built with, or it is reading a different
// language than the one that ships.
const tsEntry = await resolveTypeScript()
let ts
try {
  const mod = await import(pathToFileURL(tsEntry).href)
  ts = mod?.default
} catch (e) {
  die(
    `cannot load the typescript compiler from ${tsEntry}: ${e.message}. ` +
      'Refusing to guess: without the compiler nothing was checked',
  )
}
if (
  !ts ||
  typeof ts.createProgram !== 'function' ||
  typeof ts.readConfigFile !== 'function' ||
  typeof ts.parseJsonConfigFileContent !== 'function'
) {
  die(
    `the module at ${tsEntry} is not a usable TypeScript compiler ` +
      '(createProgram/readConfigFile/parseJsonConfigFileContent missing). ' +
      'Refusing to guess: without the compiler nothing was checked',
  )
}

function compilerCandidate(p) {
  if (!existsSync(p)) return null
  if (!readableFile(p)) {
    die(
      `typescript compiler at ${p} exists but is unreadable; ` +
        'Refusing to guess: without the compiler nothing was checked',
    )
  }
  return p
}

async function resolveTypeScript() {
  // web/node_modules first: that is the compiler the console is actually built
  // with (pnpm --dir web install), and CI installs there, not at the repo root.
  // The root tree only carries commit tooling, so preferring it would silently
  // parse the console with a different compiler than the one that ships it.
  for (const base of [path.join(WEB, 'node_modules'), path.join(REPO, 'node_modules')]) {
    const direct = compilerCandidate(path.join(base, 'typescript', 'lib', 'typescript.js'))
    if (direct) return direct
    // pnpm keeps the real package under .pnpm/typescript@<version>/…
    const store = path.join(base, '.pnpm')
    if (!existsSync(store)) continue
    let entries
    try {
      entries = readdirSync(store)
    } catch (e) {
      die(
        `cannot read ${store}: ${e.message}. ` +
          'Refusing to guess: without the compiler nothing was checked',
      )
    }
    const hit = entries
      .filter((d) => d.startsWith('typescript@'))
      .sort()
      .pop()
    if (hit) {
      const p = compilerCandidate(
        path.join(store, hit, 'node_modules', 'typescript', 'lib', 'typescript.js'),
      )
      if (p) return p
    }
  }
  die(
    'cannot find the typescript compiler under web/node_modules or node_modules; ' +
      'run `pnpm --dir web install`. Refusing to guess: without the compiler nothing was checked',
  )
}

// --- 1. the engine's answer, executed --------------------------------------
const GO_DIR = path.join(REPO, 'cmd', 'olivares')

function permsdump(args, what) {
  if (!existsSync(path.join(GO_DIR, 'go.mod'))) {
    die(`no Go module at ${GO_DIR}: cannot ask the engine what it declares`)
  }
  try {
    return execFileSync('go', ['run', './tools/permsdump', ...args], {
      cwd: GO_DIR,
      encoding: 'utf8',
      maxBuffer: 64 * 1024 * 1024,
      env: { ...process.env, TMPDIR: process.env.TMPDIR ?? '/tmp' },
    })
  } catch (e) {
    die(
      `could not run \`go run ./tools/permsdump ${args.join(' ')}\` — ${what} did not ` +
        `execute, so nothing was verified:\n${e.stderr || e.message}`,
    )
  }
}

function engineInventory() {
  const raw = permsdump([], 'the engine half of this check')
  let inv
  try {
    inv = JSON.parse(raw)
  } catch {
    die('permsdump did not emit JSON')
  }
  if (inv.schema !== 'olivares.permissions.inventory/1') {
    die(`permsdump emitted schema ${inv.schema}, which this guard does not know how to read`)
  }
  if (!inv.declared || Object.keys(inv.declared).length === 0) {
    die('permsdump declared no permissions at all; refusing to report the console clean')
  }
  for (const m of inv.modules ?? []) {
    for (const r of m.routes ?? []) {
      if (r.surface !== undefined && !['console', 'api-only', 'edition-refusal'].includes(r.surface)) {
        die(`unknown route surface ${JSON.stringify(r.surface)} at ${m.namespace} ${r.method} ${r.pattern}`)
      }
    }
  }
  return inv
}

// --- 2. the console's answer, executed -------------------------------------
// The console is written for a BUNDLER: extensionless relative imports and the `@/`
// alias. Node resolves neither, so the guard teaches its loader the same two rules
// rather than make the console import differently for the benefit of a check —
// source that is shaped by its test harness is source the harness stopped testing.
{
  const { registerHooks } = await import('node:module')
  const CANDIDATES = ['', '.ts', '.tsx', '/index.ts', '/index.tsx']
  registerHooks({
    resolve(specifier, context, next) {
      const isAlias = specifier.startsWith('@/')
      const isRelative = specifier.startsWith('./') || specifier.startsWith('../')
      if (!isAlias && !isRelative) return next(specifier, context)
      const base = isAlias
        ? path.join(WEB, 'src', specifier.slice(2))
        : path.resolve(path.dirname(fileURLToPath(context.parentURL)), specifier)
      for (const ext of CANDIDATES) {
        if (ext && existsSync(base + ext)) {
          return { url: pathToFileURL(base + ext).href, shortCircuit: true }
        }
      }
      return next(specifier, context)
    },
  })
}
let rbac
try {
  rbac = await import(pathToFileURL(path.join(WEB, 'src', 'lib', 'auth', 'rbac.ts')).href)
} catch (e) {
  // Fail-closed with the cause named: without the console's own RBAC module NOTHING
  // here was checked, and "I could not look" must never read as "it is clean".
  die(
    'could not import web/src/lib/auth/rbac.ts, so the console half of this check did ' +
      `not run:\n${e.message}`,
  )
}
if (typeof rbac.can !== 'function') die('web/src/lib/auth/rbac.ts does not export can()')

// The inventory, filled in section 4. consoleCan closes over it because the console half
// is now driven WITH the engine's own answer: the synthetic principal carries the
// effective set the engine computes for that role, exactly as whoami would emit it.
let inv = null
const setCache = new Map()

/**
 * The permissions the ENGINE grants `role`, as auth.RoleGrants answered them — the same
 * set core/api's whoami hands the console for a principal holding that role.
 *
 * Building it here rather than asking rbac.ts for it is the whole discipline of this
 * guard: it runs both sides and models neither. If this derived the set from the console
 * instead, the comparison below would compare the console with itself.
 */
function engineSetFor(role) {
  let hit = setCache.get(role)
  if (hit) return hit
  hit = Object.entries(inv.declared)
    .filter(([, d]) => d.grants[role])
    .map(([p]) => p)
  setCache.set(role, hit)
  return hit
}

const consoleCan = (role, permission) =>
  rbac.can(permission, {
    principal: {
      superadmin: false,
      grants: [{ tenant: TENANT, role, permissions: engineSetFor(role) }],
    },
    tenant: TENANT,
  })

// --- 3. every permission string that can reach can() -----------------------
function buildProgram() {
  const cfgPath = path.join(WEB, 'tsconfig.app.json')
  // Three distinct refusals, worded distinctly ON PURPOSE. They used to share the
  // filename and little else, and a mutation battery showed the cost: disabling the
  // first left the second answering with a message the first's test still matched, so
  // the case stayed red and the clause looked load-bearing while being dead.
  if (!existsSync(cfgPath)) die(`the console has no tsconfig at ${cfgPath}`)
  const cfg = ts.readConfigFile(cfgPath, ts.sys.readFile)
  if (cfg.error) die(`the console tsconfig will not parse: ${cfg.error.messageText}`)
  const parsed = ts.parseJsonConfigFileContent(cfg.config, ts.sys, WEB)
  if (parsed.fileNames.length === 0) {
    die('tsconfig.app.json matched no files; refusing to report the console clean')
  }
  const program = ts.createProgram({ rootNames: parsed.fileNames, options: parsed.options })
  return { program, checker: program.getTypeChecker() }
}

const { program, checker } = buildProgram()

const rel = (f) => path.relative(REPO, f)
const at = (node) => {
  const sf = node.getSourceFile()
  return `${rel(sf.fileName)}:${sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1}`
}
const isProduction = (f) =>
  f.includes(`${path.sep}web${path.sep}src${path.sep}`) &&
  !/\.test\.|\.spec\.|__tests__|[\\/]test[\\/]/.test(f)

const sourceFiles = program
  .getSourceFiles()
  .filter((sf) => !sf.isDeclarationFile && isProduction(sf.fileName))

function walk(node, fn) {
  fn(node)
  ts.forEachChild(node, (c) => walk(c, fn))
}

// The can() declarations, resolved by SYMBOL. Matching on the name alone would
// accept any unrelated helper called `can` and, worse, would silently stop matching
// if the real one were renamed — a rename must break this guard loudly, not quietly
// empty it.
const canDecls = new Set()
const rbacSf = program
  .getSourceFiles()
  .find((sf) => sf.fileName.endsWith(path.join('src', 'lib', 'auth', 'rbac.ts')))
const ctxSf = program
  .getSourceFiles()
  .find((sf) => sf.fileName.endsWith(path.join('src', 'lib', 'auth', 'context.tsx')))
{
  if (!rbacSf) die('web/src/lib/auth/rbac.ts is not in the program')
  for (const sf of [rbacSf, ctxSf].filter(Boolean)) {
    walk(sf, (n) => {
      // `export function can(...)` and the `can:` member of AuthContextValue.
      if (ts.isFunctionDeclaration(n) && n.name?.text === 'can') canDecls.add(n)
      if (ts.isPropertySignature(n) && ts.isIdentifier(n.name) && n.name.text === 'can') {
        canDecls.add(n)
      }
    })
  }
  if (canDecls.size === 0) {
    die('found no declaration of can() in lib/auth; the guard would pass vacuously')
  }
}

const findings = []
const finding = (kind, node, detail) =>
  findings.push({ kind, where: at(node), detail })

/** The declaration a name resolves to, following aliases (imports). */
function declOf(node) {
  let sym = checker.getSymbolAtLocation(node)
  if (!sym) return null
  if (sym.flags & ts.SymbolFlags.Alias) {
    try {
      sym = checker.getAliasedSymbol(sym)
    } catch {
      /* not an alias after all */
    }
  }
  return sym.declarations?.[0] ?? null
}

/**
 * The property SIGNATURE a sink ultimately names, or null.
 *
 * Everything interesting in this console arrives through a DESTRUCTURING, and the
 * symbol of a destructured name is the binding element, not the property it came
 * from. Two different sites therefore name the same thing under different symbols:
 * `const { can } = useAuth()` in a view and the `can` member of AuthContextValue;
 * `function X({ permission }: Props)` and `<X permission="…">`. Resolving both to
 * the property signature is what lets one comparison serve either side.
 *
 * Getting this wrong is not a near miss. The first version of this guard matched
 * the callee by symbol WITHOUT this step, so every `const { can } = useAuth()` call
 * failed to match: it checked 2 call sites out of 205 and printed OK. A guard whose
 * pattern is narrower than the thing it guards stops guarding and says nothing —
 * which is the same defect, in a guard, that the console has in production.
 */
function sinkSignatureOf(decl) {
  if (!decl) return null
  if (ts.isPropertySignature(decl) || ts.isPropertyDeclaration(decl)) return decl
  if (!ts.isBindingElement(decl)) return null
  const key = decl.propertyName ?? decl.name
  const name = ts.isIdentifier(key) || ts.isStringLiteral(key) ? key.text : null
  if (!name) return null
  // `decl.parent` is the ObjectBindingPattern; its parent is what is being
  // destructured — a parameter (`function X({ p }: Props)`) or a variable
  // declaration (`const { can } = useAuth()`).
  const host = decl.parent?.parent
  if (!host) return null
  let type = null
  if (ts.isParameter(host)) type = checker.getTypeAtLocation(host)
  else if (ts.isVariableDeclaration(host) && host.initializer) {
    type = checker.getTypeAtLocation(host.initializer)
  }
  const d = type?.getProperty?.(name)?.declarations?.[0]
  if (d && (ts.isPropertySignature(d) || ts.isPropertyDeclaration(d))) return d
  return null
}

/**
 * The declaration a name canonically refers to: the property signature when the name
 * came from a destructuring, the aliased target when it came from `const x = obj.y`,
 * otherwise the declaration itself.
 *
 * The alias hop matters because `const allowed = auth.can` is an ordinary refactor and
 * used to make the call invisible: the name no longer spelled `can`, and its
 * declaration was a variable rather than the member it points at.
 */
function canonicalDecl(node, depth = 0) {
  const d = declOf(node)
  if (!d || depth > 4) return d
  const sig = sinkSignatureOf(d)
  if (sig) return sig
  if (ts.isVariableDeclaration(d) && d.initializer) {
    const init = d.initializer
    const target = ts.isPropertyAccessExpression(init)
      ? init.name
      : ts.isIdentifier(init)
        ? init
        : null
    if (target) return canonicalDecl(target, depth + 1) ?? d
  }
  return d
}

/**
 * Whether this node sits inside the definition of something called `can` — the
 * `const can = useCallback((permission, …) => rbacCan(permission, …))` in the auth
 * provider, and any future re-spelling of the same plumbing. Walking out to the
 * nearest NAMING declaration is what makes it exact.
 */
function isInsideCanDefinition(node) {
  for (let p = node.parent; p; p = p.parent) {
    if (ts.isSourceFile(p)) return false
    const named =
      (ts.isVariableDeclaration(p) && ts.isIdentifier(p.name) && p.name.text) ||
      (ts.isPropertyAssignment(p) && ts.isIdentifier(p.name) && p.name.text) ||
      (ts.isFunctionDeclaration(p) && p.name?.text) ||
      (ts.isMethodDeclaration(p) && ts.isIdentifier(p.name) && p.name.text) ||
      null
    if (named) return named === 'can'
  }
  return false
}

/**
 * Every write to the given property signature: object-literal properties, JSX
 * attributes, and the two shapes that carry a value WITHOUT naming it — an object
 * spread and a JSX spread.
 *
 * Returns { writes, opaque }. `opaque` means a spread could be carrying this
 * property and this script cannot see through it. That is NOT the same as "no
 * write", and conflating them is the fail-open a closing review found: because the
 * real nav registry has dozens of explicit writes, one write converted to
 * `{ ...entry }` would have vanished from the check while the sink still looked
 * perfectly readable. An opaque sink is reported as `unreadable` and fails.
 */
const writesCache = new Map()
function writesTo(sig) {
  if (writesCache.has(sig)) return writesCache.get(sig)
  const name = ts.isIdentifier(sig.name) || ts.isStringLiteral(sig.name) ? sig.name.text : null
  const out = { writes: [], opaque: null }
  /** Does this contextual type's `name` property resolve to the signature we track? */
  const isOurs = (t) => t?.getProperty?.(name)?.declarations?.[0] === sig
  if (name) {
    for (const sf of sourceFiles) {
      walk(sf, (n) => {
        if (
          ts.isPropertyAssignment(n) &&
          (ts.isIdentifier(n.name) || ts.isStringLiteral(n.name)) &&
          n.name.text === name
        ) {
          // Contextual typing is what separates a nav-registry entry from a
          // policy-authoring form that happens to have a field called `permission`.
          // Both are `{ permission: string }`; only one is a FeatureView.
          const t = checker.getContextualType(n.parent) ?? checker.getTypeAtLocation(n.parent)
          if (isOurs(t)) out.writes.push(n.initializer)
          return
        }
        // `{ permission }` — the value is the binding of the same name in scope.
        if (
          ts.isShorthandPropertyAssignment(n) &&
          ts.isIdentifier(n.name) &&
          n.name.text === name
        ) {
          const t = checker.getContextualType(n.parent) ?? checker.getTypeAtLocation(n.parent)
          if (!isOurs(t)) return
          const v = checker.getShorthandAssignmentValueSymbol(n)?.declarations?.[0]
          if (v && ts.isVariableDeclaration(v) && v.initializer) out.writes.push(v.initializer)
          else out.opaque ??= n
          return
        }
        // `{ ...entry }` — the spread may carry this property under any name.
        if (ts.isSpreadAssignment(n) && ts.isObjectLiteralExpression(n.parent)) {
          const t = checker.getContextualType(n.parent) ?? checker.getTypeAtLocation(n.parent)
          if (isOurs(t)) out.opaque ??= n
          return
        }
        if (ts.isJsxAttribute(n) && ts.isIdentifier(n.name) && n.name.text === name) {
          // getSymbolAtLocation on a JSX attribute name returns the ATTRIBUTE's own
          // symbol, whose declaration is the attribute itself — never the prop it
          // fills. Comparing that against the signature is always false, which made
          // every prop-fed sink resolve to nothing. The link is the contextual type
          // of the attributes node, exactly as for an object literal.
          if (!isOurs(checker.getContextualType(n.parent))) return
          const init = n.initializer
          if (!init) return
          out.writes.push(ts.isJsxExpression(init) ? init.expression : init)
          return
        }
        // `<Gate {...props} />` — same blind spot as the object spread.
        if (ts.isJsxSpreadAttribute(n) && ts.isJsxAttributes(n.parent)) {
          if (isOurs(checker.getContextualType(n.parent))) out.opaque ??= n
        }
      })
    }
  }
  writesCache.set(sig, out)
  return out
}

// Index local call edges by symbol, including arguments that carry a callable and
// closures returned to another local consumer. This is independent of the later
// capability graph: its roots/credit rules remain unchanged.
const permissionCalls = []
const callableAssignments = new Map()
for (const sf of sourceFiles) walk(sf, (n) => {
  if (ts.isCallExpression(n)) permissionCalls.push(n)
  if (ts.isBinaryExpression(n) && n.operatorToken.kind >= ts.SyntaxKind.FirstAssignment &&
      n.operatorToken.kind <= ts.SyntaxKind.LastAssignment) {
    const left = unwrapExpr(n.left)
    const d = ts.isIdentifier(left) ? declOf(left) : null
    if (d) {
      let writes = callableAssignments.get(d)
      if (!writes) callableAssignments.set(d, writes = [])
      writes.push(n.right)
    }
  }
})
const permissionCallers = new Map()
function parameterInputs(parameter) {
  const fn = parameter.parent
  if (!isFunctionLike(fn)) return []
  const index = fn.parameters.indexOf(parameter)
  return [...(permissionCallers.get(fn) ?? [])].map((call) => call.arguments[index])
}
function returnedExpressions(fn) {
  if (!fn.body) return [null]
  if (!ts.isBlock(fn.body)) return [fn.body]
  const out = []
  function visit(n) {
    if (isFunctionLike(n)) return
    if (ts.isReturnStatement(n)) out.push(n.expression ?? null)
    else ts.forEachChild(n, visit)
  }
  visit(fn.body)
  // A returned predicate must exist on every path. Unsupported endings stay opaque.
  if (!fn.body.statements.length || !ts.isReturnStatement(fn.body.statements.at(-1))) out.push(null)
  return out
}
const UNKNOWN_CALLABLE = Symbol('unknown callable')
function callableOrigins(expr, seen = new Set()) {
  expr = unwrapExpr(expr)
  if (!expr || seen.has(expr) || seen.size > 24) return [UNKNOWN_CALLABLE]
  const next = new Set(seen).add(expr)
  if (isFunctionLike(expr)) return [expr]
  if (ts.isConditionalExpression(expr)) {
    return [...callableOrigins(expr.whenTrue, next), ...callableOrigins(expr.whenFalse, next)]
  }
  if (ts.isCallExpression(expr)) {
    return callableOrigins(expr.expression, next).flatMap((fn) =>
      typeof fn !== 'symbol' && isFunctionLike(fn)
        ? returnedExpressions(fn).flatMap((value) => callableOrigins(value, next))
        : [UNKNOWN_CALLABLE],
    )
  }
  const name = ts.isPropertyAccessExpression(expr) ? expr.name : expr
  const raw = ts.isIdentifier(name) ? declOf(name) : null
  const d = ts.isIdentifier(name) ? canonicalDecl(name) : null
  if (!d) return [UNKNOWN_CALLABLE]
  const writes = callableAssignments.get(raw)
  if (writes) {
    // No execution-order proof for mutable aliases. Preserve any RBAC origin so
    // a renamed call is inspected, but never credit an ambiguous assigned callable.
    const initial = ts.isVariableDeclaration(raw) ? callableOrigins(raw.initializer, next)
      : canDecls.has(d) ? [d] : [UNKNOWN_CALLABLE]
    return [UNKNOWN_CALLABLE, ...initial, ...writes.flatMap((v) => callableOrigins(v, next))]
  }
  // Follow each variable rather than canonically jumping past a reassigned alias.
  if (raw && ts.isVariableDeclaration(raw)) return callableOrigins(raw.initializer, next)
  if (canDecls.has(d)) return [d]
  const fn = callableOf(d)
  if (fn) return [fn]
  if (ts.isVariableDeclaration(d) && d.initializer) return callableOrigins(d.initializer, next)
  if (ts.isParameter(d)) {
    const inputs = parameterInputs(d)
    return inputs.length ? inputs.flatMap((v) => callableOrigins(v, next)) : [UNKNOWN_CALLABLE]
  }
  return [UNKNOWN_CALLABLE]
}
// Monotonic edges; the small bound is an inspection limit, never permission credit.
for (let round = 0; ; round++) {
  let changed = false
  for (const call of permissionCalls) {
    for (const fn of callableOrigins(call.expression)) {
      if (typeof fn === 'symbol' || !isFunctionLike(fn)) continue
      let callers = permissionCallers.get(fn)
      if (!callers) permissionCallers.set(fn, callers = new Set())
      if (!callers.has(call)) { callers.add(call); changed = true }
    }
  }
  if (!changed) break
  if (round === 12) die('local permission call graph did not converge; nothing was verified')
}

function combineValues(parts) {
  return parts.some((p) => p === null) ? null : [...new Set(parts.flat())]
}

// Only dispatch discriminators may use a finite declared type. A permission of
// type string (or an asserted permission union) still needs its actual producer.
function dispatchValues(expr, depth, bindings) {
  const values = resolve(expr, depth, new Set(), bindings)
  if (values !== null) return values
  expr = unwrapExpr(expr)
  if (!expr || depth > 32) return null
  if (ts.isIdentifier(expr)) {
    const d = declOf(expr)
    const bound = bindings.get(d)
    if (bound) return bound.values ?? dispatchValues(bound.expr, depth + 1, bound.bindings)
    if (d && ts.isVariableDeclaration(d) && d.initializer) {
      return dispatchValues(d.initializer, depth + 1, bindings)
    }
    if (d && ts.isParameter(d)) {
      const inputs = parameterInputs(d)
      if (inputs.length) return combineValues(inputs.map((v) => dispatchValues(v, depth + 1, bindings)))
    }
  }
  if (!ts.isIdentifier(expr) && !ts.isPropertyAccessExpression(expr)) return null
  const t = checker.getTypeAtLocation(expr)
  const members = t.isUnion() ? t.types : [t]
  return members.length <= 32 && members.every((v) => v.flags & ts.TypeFlags.StringLiteral)
    ? members.map((v) => v.value) : null
}

function resolveStatements(statements, projection, depth, bindings) {
  if (depth > 32) return null
  for (let i = 0; i < statements.length; i++) {
    const statement = statements[i]
    const tail = statements.slice(i + 1)
    if (ts.isReturnStatement(statement)) {
      return resolve(statement.expression, depth + 1, new Set(), bindings, projection)
    }
    if (ts.isSwitchStatement(statement)) {
      const choices = dispatchValues(statement.expression, depth + 1, bindings)
      if (!choices?.length) return null
      const clauses = statement.caseBlock.clauses
      // Only literal labels are in the supported dispatch subset. An opaque or
      // multi-valued expression is not evidence that its branch cannot match.
      const labels = clauses.map((c) => ts.isCaseClause(c) ? unwrapExpr(c.expression) : null)
      if (labels.some((label) => label &&
          !ts.isStringLiteral(label) && !ts.isNoSubstitutionTemplateLiteral(label))) return null
      return combineValues(choices.map((value) => {
        let index = labels.findIndex((label) => label?.text === value)
        if (index < 0) index = clauses.findIndex(ts.isDefaultClause)
        if (index < 0) return resolveStatements(tail, projection, depth + 1, bindings)
        const narrowed = new Map(bindings)
        if (ts.isIdentifier(statement.expression)) {
          narrowed.set(declOf(statement.expression), { values: [value] })
        }
        return resolveStatements([...clauses.slice(index).flatMap((c) => [...c.statements]), ...tail],
          projection, depth + 1, narrowed)
      }))
    }
    if (ts.isIfStatement(statement)) {
      const branch = (n) => n ? ts.isBlock(n) ? [...n.statements] : [n] : []
      return combineValues([statement.thenStatement, statement.elseStatement].map((n) =>
        resolveStatements([...branch(n), ...tail], projection, depth + 1, bindings)))
    }
    // Declarations can execute arbitrary initializers, including assignments via
    // aliases. Only return/if/switch are supported; never skip a statement's effects.
    return null
  }
  return null
}

// Only this runtime-sealed construction is readable: Object.freeze of a visible
// array literal mapped through entry => Object.freeze(entry). Initializers,
// readonly types, asserted signatures and aliases alone do not prove immutability.
function standardMethod(call, owner, name) {
  if (!call || !ts.isCallExpression(call) || !ts.isPropertyAccessExpression(call.expression) ||
      call.expression.name.text !== name) return false
  const method = checker.getResolvedSignature(call)?.declaration
  return !!method && program.isSourceFileDefaultLibrary(method.getSourceFile()) &&
    method.parent.name?.text === owner && method.name?.getText() === name
}
function standardFreeze(call) {
  if (!standardMethod(call, 'ObjectConstructor', 'freeze') || call.arguments.length !== 1) return false
  const receiver = call.expression.expression
  const declarations = ts.isIdentifier(receiver)
    ? checker.getSymbolAtLocation(receiver)?.declarations : null
  return !!declarations?.some((d) => ts.isVariableDeclaration(d) && d.name.getText() === 'Object') &&
    declarations.every((d) => program.isSourceFileDefaultLibrary(d.getSourceFile()))
}
// Declared signatures do not prove unchanged runtime methods. Within the source
// census, constructors must stay behind direct static calls: exposing one (or its
// prototype/member) could replace freeze/map/find. Reject that evidence rather
// than interpreting aliases, mutation order or arbitrary dependency code.
function standardConstructorReference(node) {
  if (!ts.isIdentifier(node) || !['Object', 'Array'].includes(node.text)) return false
  const declarations = checker.getSymbolAtLocation(node)?.declarations
  return !!declarations?.some((d) => ts.isVariableDeclaration(d) && d.name.getText() === node.text) &&
    declarations.every((d) => program.isSourceFileDefaultLibrary(d.getSourceFile()))
}
function outerExpression(node) {
  while (node.parent && unwrapExpr(node.parent) === unwrapExpr(node)) node = node.parent
  return node
}
const globalObjectNames = ['globalThis', 'window', 'self']
const globalObjectSymbols = new Set(globalObjectNames.map((name) =>
  checker.resolveName(name, undefined, ts.SymbolFlags.Value, false)).filter(Boolean))
const registryBuiltinsIntact = !sourceFiles.some((sf) => {
  let exposed = false
  walk(sf, (node) => {
    if (exposed) return
    const globalObject = ts.isIdentifier(node) && globalObjectSymbols.has(checker.getSymbolAtLocation(node))
    if (!globalObject && !standardConstructorReference(node)) return
    let typeName = node
    while (ts.isQualifiedName(typeName.parent)) typeName = typeName.parent
    if (ts.isTypeReferenceNode(typeName.parent) || ts.isTypeQueryNode(typeName.parent)) return
    if (ts.isPropertyAccessExpression(node.parent) && node.parent.name === node) node = node.parent
    node = outerExpression(node)
    if (globalObject) {
      if (ts.isTypeOfExpression(node.parent)) return
      const access = node.parent
      const key = ts.isPropertyAccessExpression(access) && access.expression === node ? access.name
        : ts.isElementAccessExpression(access) && access.expression === node ? unwrapExpr(access.argumentExpression) : null
      if (!key || !(ts.isIdentifier(key) && ts.isPropertyAccessExpression(access) ||
          ts.isStringLiteral(key) || ts.isNoSubstitutionTemplateLiteral(key)) || globalObjectNames.includes(key.text)) {
        exposed = true // dynamic key or global-object escape: no heap/alias proof
        return
      }
      if (!['Object', 'Array'].includes(key.text)) return
      node = outerExpression(access) // dotted and computed constructors use the same check
    }
    if (ts.isPropertyAccessExpression(node.parent) && node.parent.expression === node) {
      if (node.parent.name.text === 'prototype') { exposed = true; return }
      node = outerExpression(node.parent)
    }
    exposed = !((ts.isCallExpression(node.parent) || ts.isNewExpression(node.parent)) &&
      node.parent.expression === node)
  })
  return exposed
})
function sealedRegistryProperty(call, projection, depth, seen, bindings) {
  if (!registryBuiltinsIntact || projection.length !== 1 ||
      !(standardMethod(call, 'Array', 'find') || standardMethod(call, 'ReadonlyArray', 'find'))) return null
  let receiver = unwrapExpr(call.expression.expression)
  if (ts.isIdentifier(receiver)) {
    const d = declOf(receiver)
    if (!d || !ts.isVariableDeclaration(d) || !(d.parent.flags & ts.NodeFlags.Const)) return null
    receiver = unwrapExpr(d.initializer)
  }
  if (!standardFreeze(receiver)) return null
  const map = unwrapExpr(receiver.arguments[0])
  if (!standardMethod(map, 'Array', 'map') || map.arguments.length !== 1) return null
  const array = unwrapExpr(map.expression.expression)
  const callback = unwrapExpr(map.arguments[0])
  if (!ts.isArrayLiteralExpression(array) || !ts.isArrowFunction(callback) ||
      callback.parameters.length !== 1 || callback.modifiers?.some((m) => m.kind === ts.SyntaxKind.AsyncKeyword)) return null
  const parameter = callback.parameters[0]
  const frozen = unwrapExpr(callback.body)
  if (!ts.isIdentifier(parameter.name) || parameter.initializer || parameter.dotDotDotToken ||
      !standardFreeze(frozen)) return null
  const returned = unwrapExpr(frozen.arguments[0])
  if (!ts.isIdentifier(returned) || declOf(returned) !== parameter) return null
  const values = combineValues(array.elements.map((entry) => {
    if (!ts.isObjectLiteralExpression(entry) || entry.properties.some((p) =>
      !ts.isPropertyAssignment(p) || ts.isComputedPropertyName(p.name) || p.name.text === '__proto__')) return null
    const properties = entry.properties.filter((p) => p.name.text === projection[0])
    if (!properties.length) return [] // an explicitly absent optional permission
    if (properties.length !== 1) return null // a later write must not hide behind the first
    const value = unwrapExpr(properties[0].initializer)
    if (!ts.isStringLiteral(value) && !ts.isNoSubstitutionTemplateLiteral(value)) return null
    return resolve(value, depth + 1, new Set(seen), bindings)
  }))
  return values?.length ? values : null
}

// Panel filter callbacks are consumers of the installed composition, including a
// known-empty Community collection. Follow actual arguments, not a generic type's
// permission union or an unrelated object's writes. This is deliberately limited
// to PANEL_EXTENSIONS; arbitrary array methods/remote producers stay unreadable.
function purePanelPredicate(predicate) {
  let pure = true
  walk(predicate, (n) => {
    if (ts.isBinaryExpression(n) && n.operatorToken.kind >= ts.SyntaxKind.FirstAssignment &&
        n.operatorToken.kind <= ts.SyntaxKind.LastAssignment ||
        ts.isPostfixUnaryExpression(n) || ts.isPrefixUnaryExpression(n) &&
        [ts.SyntaxKind.PlusPlusToken, ts.SyntaxKind.MinusMinusToken].includes(n.operator) ||
        ts.isDeleteExpression(n) || ts.isNewExpression(n)) pure = false
    if (ts.isCallExpression(n) && !(standardMethod(n, 'Array', 'includes') ||
        standardMethod(n, 'ReadonlyArray', 'includes') ||
        callableOrigins(n.expression).every((origin) => canDecls.has(origin)))) pure = false
  })
  return pure
}
function immutableInitializer(expr, depth = 0) {
  expr = unwrapExpr(expr)
  if (!expr || depth > 32) return null
  if (!ts.isIdentifier(expr)) return expr
  const d = declOf(expr)
  if (!d || !ts.isVariableDeclaration(d) || !(d.parent.flags & ts.NodeFlags.Const) ||
      callableAssignments.has(d)) return null
  return immutableInitializer(d.initializer, depth + 1)
}
function sealedProperty(expr, key) {
  expr = immutableInitializer(expr)
  if (!registryBuiltinsIntact || !standardFreeze(expr)) return null
  const object = unwrapExpr(expr.arguments[0])
  if (!ts.isObjectLiteralExpression(object) || object.properties.some((p) =>
      !ts.isPropertyAssignment(p) || ts.isComputedPropertyName(p.name) ||
      p.name.text === '__proto__')) return null
  const properties = object.properties.filter((p) => p.name.text === key)
  return properties.length === 1 ? properties[0].initializer : null
}
function sealedPermissionValue(expr, depth = 0) {
  if (depth > 32) return null
  expr = immutableInitializer(expr)
  if (!expr) return null
  if (ts.isStringLiteral(expr) || ts.isNoSubstitutionTemplateLiteral(expr)) return [expr.text]
  return ts.isPropertyAccessExpression(expr)
    ? sealedPermissionValue(sealedProperty(expr.expression, expr.name.text), depth + 1) : null
}
function frozenLiteral(expr) {
  return standardFreeze(outerExpression(expr).parent)
}
function parameterOnlyFiltered(parameter) {
  if (!parameter.parent.body) return false
  let safe = true
  walk(parameter.parent.body, (n) => {
    if (!ts.isIdentifier(n)) return
    const shorthand = ts.isShorthandPropertyAssignment(n.parent)
      ? checker.getShorthandAssignmentValueSymbol(n.parent)?.declarations : []
    if (declOf(n) !== parameter && !shorthand?.includes(parameter)) return
    const receiver = outerExpression(n)
    const access = receiver.parent
    const call = access?.parent
    if (!ts.isPropertyAccessExpression(access) || access.expression !== receiver ||
        !(standardMethod(call, 'Array', 'filter') || standardMethod(call, 'ReadonlyArray', 'filter')) ||
        !call.arguments[0] || !isFunctionLike(call.arguments[0]) ||
        !purePanelPredicate(call.arguments[0])) safe = false
  })
  return safe
}
function panelProducer(expr, projection = [], depth = 0, composition = false) {
  expr = unwrapExpr(expr)
  if (!registryBuiltinsIntact || !expr || depth > 32) return null
  if (ts.isIdentifier(expr)) {
    const d = declOf(expr)
    if (!d) return null
    if (ts.isParameter(d)) {
      if (callableAssignments.has(d) || !parameterOnlyFiltered(d)) return null
      const inputs = parameterInputs(d)
      return inputs.length ? panelParts(inputs.map((v) =>
        panelProducer(v, projection, depth + 1, composition))) : null
    }
    if (!ts.isVariableDeclaration(d) || !(d.parent.flags & ts.NodeFlags.Const) || callableAssignments.has(d)) return null
    // filter creates a mutable result; only an inline filter or an explicitly
    // frozen result can retain the installed composition's immutable witnesses.
    const value = unwrapExpr(d.initializer)
    if (standardMethod(value, 'Array', 'filter') || standardMethod(value, 'ReadonlyArray', 'filter')) return null
    composition ||= d.name.getText() === 'PANEL_EXTENSIONS' &&
      /[\/]features[\/]extensions\.tsx?$/.test(d.getSourceFile().fileName)
    return panelProducer(d.initializer, projection, depth + 1, composition)
  }
  if (ts.isPropertyAccessExpression(expr)) {
    return panelProducer(expr.expression, [expr.name.text, ...projection], depth + 1, composition)
  }
  if (ts.isCallExpression(expr) &&
      (standardMethod(expr, 'Array', 'filter') || standardMethod(expr, 'ReadonlyArray', 'filter'))) {
    // A pure prefilter can remove witnesses but cannot introduce permissions.
    // Keep the entire finite collection as a conservative census; opaque or
    // mutating callbacks cannot earn this composition credit.
    const predicate = expr.arguments[0]
    if (!predicate || !isFunctionLike(predicate)) return null
    return purePanelPredicate(predicate)
      ? panelProducer(expr.expression.expression, projection, depth + 1, composition) : null
  }

  if (standardFreeze(expr)) {
    return panelProducer(expr.arguments[0], projection, depth + 1, composition)
  }
  if (ts.isBinaryExpression(expr) && expr.operatorToken.kind === ts.SyntaxKind.QuestionQuestionToken) {
    // The fallback must itself be visibly empty. Unknown left operands cannot
    // disappear behind `?? []`.
    const right = unwrapExpr(expr.right)
    if (!ts.isArrayLiteralExpression(right) || right.elements.length) return null
    return panelProducer(expr.left, projection, depth + 1, composition)
  }
  if (ts.isObjectLiteralExpression(expr) && projection.length) {
    if (!frozenLiteral(expr)) return null
    const [key, ...rest] = projection
    if (expr.properties.some((p) => ts.isSpreadAssignment(p) || p.name?.text === '__proto__' ||
      p.name && ts.isComputedPropertyName(p.name))) return null
    const matches = expr.properties.filter((p) => p.name?.text === key)
    if (!matches.length) return composition ? { nodes: [], composition } : null
    if (matches.length !== 1) return null
    const p = matches[0]
    // Explicit properties follow the checked binding path below. Unsupported
    // shorthand producers fail closed instead of bypassing that path.
    if (!ts.isPropertyAssignment(p)) return null
    const value = p.initializer
    return panelProducer(value, rest, depth + 1, composition)
  }
  if (ts.isArrayLiteralExpression(expr) && !projection.length) {
    if (!frozenLiteral(expr)) return null
    return panelParts([...expr.elements].map((n) => ts.isSpreadElement(n)
      ? panelProducer(n.expression, [], depth + 1, composition)
      : { nodes: [n], composition }), composition)
  }
  return null
}
function panelParts(parts, composition = false) {
  if (parts.some((p) => p === null)) return null
  return { nodes: parts.flatMap((p) => p.nodes), composition: composition || parts.length > 0 && parts.every((p) => p.composition) }
}
// Select the stricter composition resolver only for collections flowing from
// the installed panel registry. Ordinary typed navigation targets retain their
// existing contextual-property resolver. This trace does not grant permission
// credit: mutation, opacity and freezing are checked by panelProducer below.
function fromPanelComposition(expr, seen = new Set()) {
  expr = unwrapExpr(expr)
  if (!expr || seen.has(expr) || seen.size > 32) return false
  const next = new Set(seen).add(expr)
  if (ts.isIdentifier(expr)) {
    const d = declOf(expr)
    if (!d) return false
    if (ts.isVariableDeclaration(d) && d.name.getText() === 'PANEL_EXTENSIONS' &&
        /[\/]features[\/]extensions\.tsx?$/.test(d.getSourceFile().fileName)) return true
    if (ts.isVariableDeclaration(d)) return fromPanelComposition(d.initializer, next)
    if (ts.isParameter(d)) return parameterInputs(d).some(v => fromPanelComposition(v, next))
  }
  if (ts.isPropertyAccessExpression(expr)) return fromPanelComposition(expr.expression, next)
  if (ts.isCallExpression(expr)) {
    return fromPanelComposition(expr.expression, next) ||
      expr.arguments.some(v => fromPanelComposition(v, next))
  }
  if (ts.isBinaryExpression(expr)) return fromPanelComposition(expr.left, next) ||
    fromPanelComposition(expr.right, next)
  return false
}
function panelPermission(expr) {
  expr = unwrapExpr(expr)
  if (!ts.isPropertyAccessExpression(expr) || expr.name.text !== 'permission' ||
      !ts.isIdentifier(expr.expression)) return undefined
  const parameter = declOf(expr.expression)
  if (!parameter || !ts.isParameter(parameter)) return undefined
  const callback = parameter.parent
  const call = callback.parent
  if (!ts.isCallExpression(call) || call.arguments[0] !== callback || callback.parameters[0] !== parameter ||
      !(standardMethod(call, 'Array', 'filter') || standardMethod(call, 'ReadonlyArray', 'filter'))) return undefined
  if (!fromPanelComposition(call.expression.expression)) return undefined
  if (!purePanelPredicate(callback)) return null
  const producer = panelProducer(call.expression.expression)
  if (!producer?.composition) return null
  return combineValues(producer.nodes.map((node) => {
    node = unwrapExpr(node)
    if (ts.isIdentifier(node)) {
      node = immutableInitializer(node)
    }
    if (!standardFreeze(node)) return null
    node = unwrapExpr(node.arguments[0])
    if (!node || !ts.isObjectLiteralExpression(node) || node.properties.some((p) =>
        ts.isSpreadAssignment(p) || p.name?.text === '__proto__' || p.name && ts.isComputedPropertyName(p.name))) return null
    const properties = node.properties.filter((p) => p.name?.text === 'permission')
    if (!properties.length) return []
    if (properties.length !== 1) return null
    const p = properties[0]
    return ts.isPropertyAssignment(p) ? sealedPermissionValue(p.initializer) : null
  }))
}

/** Resolve permissions through finite local producers; null always fails closed. */
function resolve(expr, depth = 0, seen = new Set(), bindings = new Map(), projection = []) {
  expr = unwrapExpr(expr)
  if (!expr || depth > 32) return null
  if (!projection.length && (ts.isStringLiteral(expr) || ts.isNoSubstitutionTemplateLiteral(expr))) {
    return [expr.text]
  }
  if (ts.isConditionalExpression(expr)) {
    return combineValues([expr.whenTrue, expr.whenFalse].map((branch) =>
      resolve(branch, depth + 1, new Set(seen), bindings, projection)))
  }
  if (!projection.length && ts.isTemplateExpression(expr)) {
    let out = [expr.head.text]
    for (const span of expr.templateSpans) {
      const values = dispatchValues(span.expression, depth + 1, bindings)
      if (!values || out.length * values.length > 64) return null
      out = out.flatMap((prefix) => values.map((v) => prefix + v + span.literal.text))
    }
    return out
  }
  // Private views also publish flat, frozen permission dictionaries. Read only
  // an own property of that sealed object; nested mutable objects, getters and
  // opaque spreads keep the existing unreadable result.
  if (projection.length === 1 && standardFreeze(expr)) {
    if ([...seen].some((d) => ts.isVariableDeclaration(d) &&
        (!(d.parent.flags & ts.NodeFlags.Const) || callableAssignments.has(d)))) return null
    return sealedPermissionValue(sealedProperty(expr, projection[0]))
  }

  if (ts.isCallExpression(expr)) {
    const name = calleeNameNode(expr)
    const fn = name && callableOf(canonicalDecl(name))
    if (!fn?.body) return sealedRegistryProperty(expr, projection, depth, seen, bindings)
    const bound = new Map(bindings)
    fn.parameters.forEach((p, i) => bound.set(p, { expr: expr.arguments[i] ?? p.initializer, bindings }))
    return ts.isBlock(fn.body)
      ? resolveStatements([...fn.body.statements], projection, depth + 1, bound)
      : resolve(fn.body, depth + 1, new Set(seen), bound, projection)
  }
  if (ts.isElementAccessExpression(expr)) {
    const keys = dispatchValues(expr.argumentExpression, depth + 1, bindings)
    return keys?.length ? combineValues(keys.map((key) =>
      resolve(expr.expression, depth + 1, new Set(seen), bindings, [key, ...projection]))) : null
  }
  if (ts.isObjectLiteralExpression(expr)) {
    if (!projection.length) return null
    const [key, ...rest] = projection
    for (const p of [...expr.properties].reverse()) {
      if (ts.isSpreadAssignment(p)) return resolve(p.expression, depth + 1, seen, bindings, projection)
      if ((ts.isIdentifier(p.name) || ts.isStringLiteral(p.name)) && p.name.text === key) {
        const value = ts.isShorthandPropertyAssignment(p)
          ? checker.getShorthandAssignmentValueSymbol(p)?.declarations?.[0]?.initializer : p.initializer
        return resolve(value, depth + 1, seen, bindings, rest)
      }
      if (p.name && ts.isComputedPropertyName(p.name)) return null
    }
    return null
  }
  if (ts.isPropertyAccessExpression(expr)) {
    const base = unwrapExpr(expr.expression)
    const d = ts.isIdentifier(base) ? declOf(base) : null
    // Project actual objects/return values before falling back to interface writes.
    if (ts.isCallExpression(base) || ts.isObjectLiteralExpression(base) ||
        ts.isElementAccessExpression(base) ||
        (d && ((ts.isVariableDeclaration(d) && d.initializer) || bindings.has(d)))) {
      return resolve(base, depth + 1, seen, bindings, [expr.name.text, ...projection])
    }
  }
  const nameNode = ts.isPropertyAccessExpression(expr) ? expr.name : ts.isIdentifier(expr) ? expr : null
  if (!nameNode) return null
  const decl = declOf(nameNode)
  if (!decl || seen.has(decl)) return null
  const next = new Set(seen).add(decl)
  const bound = bindings.get(decl)
  if (bound) return bound.values
    ? projection.length ? null : bound.values
    : resolve(bound.expr, depth + 1, new Set(), bound.bindings, projection)
  if (ts.isParameter(decl)) {
    const inputs = parameterInputs(decl)
    return inputs.length ? combineValues(inputs.map((v) =>
      resolve(v, depth + 1, new Set(next), bindings, projection))) : null
  }
  if (ts.isVariableDeclaration(decl) || ts.isPropertyAssignment(decl) || ts.isEnumMember(decl)) {
    return resolve(decl.initializer, depth + 1, next, bindings, projection)
  }
  if (ts.isShorthandPropertyAssignment(decl)) {
    const v = checker.getShorthandAssignmentValueSymbol(decl)?.declarations?.[0]
    return v && ts.isVariableDeclaration(v) ? resolve(v.initializer, depth + 1, next, bindings, projection) : null
  }
  const sig = sinkSignatureOf(decl)
  if (sig && !projection.length) {
    const { writes, opaque } = writesTo(sig)
    if (!writes.length || opaque) return null
    return combineValues(writes.map((w) => resolve(w, depth + 1, new Set(next), bindings)))
  }
  return null
}

// Walk every can() call.
const callSites = [] // {permission, node}
for (const sf of sourceFiles) {
  walk(sf, (n) => {
    if (!ts.isCallExpression(n)) return
    const callee = n.expression
    const nameNode = ts.isPropertyAccessExpression(callee)
      ? callee.name
      : ts.isIdentifier(callee)
        ? callee
        : null
    if (!nameNode) return
    // The mirror's own FORWARDING is not a gate: the function that IMPLEMENTS can()
    // passes its own parameter through to rbac.can. There is no permission there to
    // be wrong with, because that call is how can() exists.
    //
    // Anchored to being INSIDE a definition of can — not to the two files, and not
    // to "the argument looks like a parameter". Both were tried and both were named
    // by a sign-off as too wide, for the same reason: a REAL gate written in either
    // file, an ordinary `can(permission)` in a component, matches those shapes and
    // would vanish silently. Being lexically inside the thing called `can` cannot be
    // satisfied by a gate, because a gate is a CALLER of can, never a part of it.
    if (isInsideCanDefinition(n)) return
    // SYMBOL FIRST, spelling second. Filtering on the name before resolving would
    // miss `const allowed = auth.can; allowed('ghost:read')` — an ordinary alias,
    // and one a closing review reached by refactor. So every call is resolved and
    // matched on identity; the spelling is used only to decide whether a call that
    // does NOT resolve deserves a finding.
    const d = canonicalDecl(nameNode)
    const origins = callableOrigins(callee)
    const hasCan = origins.some((origin) => canDecls.has(origin))
    const isCan = hasCan && origins.every((origin) => canDecls.has(origin) ||
      (typeof origin !== 'symbol' && isFunctionLike(origin)))
    if (!hasCan && nameNode.text !== 'can') return
    if (!isCan) {
      finding(
        'unreadable',
        n,
        `cannot establish the RBAC origin of ${nameNode.text}() without unknown callable inputs. ` +
          `Declared at ${d ? at(d) : 'nowhere this guard could follow'}. ` +
          'If this is an unrelated helper, it needs a different name or this guard needs to ' +
          'learn about it — it will not assume.',
      )
      return
    }

    const arg = n.arguments[0]
    if (!arg) {
      finding('unreadable', n, 'can() called with no permission argument')
      return
    }
    const panelValues = panelPermission(arg)
    const values = panelValues === undefined ? resolve(arg) : panelValues
    if (values === null) {
      finding(
        'unreadable',
        arg,
        `cannot resolve the permission passed to can(): \`${arg
          .getText(sf)
          .replace(/\s+/g, ' ')
          .slice(0, 90)}\`. This guard fails rather than skip it — an argument it ` +
          'cannot read is exactly where a divergence would hide.',
      )
      return
    }
    for (const v of values) callSites.push({ permission: v, node: arg })
  })
}

if (callSites.length === 0 && findings.length === 0) {
  die('found no can() call sites at all; refusing to report the console clean')
}

// --- 3-bis. the console's OTHER way of asking (G1-B) ------------------------
//
// A screen no longer has to mirror a permission to have a surface. Since G1-B, a view
// whose authority the whoami reflection cannot express asks the ENGINE about the exact
// registered operation instead, through `POST /v1/auth/capabilities`, and enables itself
// only on that answer. `sessions:channel:admin` is the first: it can be held through a
// workspace-scoped authored grant the permission set never names, and it can be reflected
// while an authored policy forbids the operation. A `can()` for it would be a lie in both
// directions, so the migrated screens do not write one.
//
// ⛔ WITHOUT THIS BLOCK THE SURFACE CENSUS BELOW WOULD BE MEASURING THE WRONG THING. It
//    asks "does any screen ask the engine about this route's permission?" and answered it
//    by looking for `can()` alone — so a route whose console surface is a REAL capability
//    question would be reported as having none, and the ratchet would push the next writer
//    to add a `can()` that decides nothing just to quiet it. That call is precisely what
//    must not exist: a permission mirror beside a screen that does not use it is how the
//    two drift apart again.
//
// ⛔ MATCHING THE SPELLING `capabilityQuestion` IS NOT A CONSUMER. A first version of
//    this block credited every CallExpression whose callee text was that name. That
//    treated an exported builder nobody calls, and an unrelated local namesake, as a
//    console surface — the uncovered count dropped from one unaccounted permission to
//    a fake zero while the process still exited 0 under the ratchet. The property is
//    the credit, not that exit.
//
// Recognition is therefore the same KIND of thing can() already does, pointed at the
// current five builders and their actual hook/guard path, without a generic compiler:
//
//   1. SYMBOL, not spelling. The real `capabilityQuestion` / `useCapability` /
//      `requestCapabilityPermit` / `CapabilityPreflight.request` are the declarations
//      in web/src/lib/auth/capabilities.ts. Import aliases, `const x = useCapability`
//      and re-exports follow the existing declOf/canonicalDecl hop. A live-file call
//      that keeps the name but does not resolve to those declarations is unreadable,
//      the same as a namesake `can()`. Unsupported callees (computed, comma, spread)
//      are refused, not guessed.
//   2. A REAL CONSUMER must receive the question. Credit flows from `useCapability(q)`,
//      `preflight.request(q)` and `requestCapabilityPermit({ question: q })` — the
//      hook and the dispatch-time guard — not from a builder that merely exists. The
//      five feature builders count when a consumer calls them (or a registry field
//      typed as FeatureCapability.surface/deepLink holds them and a consumer calls
//      that field). An unused intermediate that takes a question and is never called
//      contributes nothing; a dead hook in a file no live can() surface imports
//      contributes nothing. Spelling `useCapability` one level later is not a loophole.
//   3. LIVE EXECUTION starts at the callable containing each real can() site, then
//      follows direct calls, rendered JSX components and callbacks handed to a call.
//      Static/dynamic imports only bound the files this small graph may inspect; being
//      somewhere in an imported module is not execution. This is enough to follow the
//      current administration tree and shared projection without pretending to be a
//      whole-program reachability analysis from index.html.
//
// The operation string is still resolved with the existing resolver and matched
// against `${method} /v1/m/${namespace}${pattern}` from the same inventory. It is
// not an exemption list, it names no permission of its own, and an operation this
// guard cannot read is a FINDING. The generic `can(view.permission)` census is
// unchanged: it still resolves every registry permission literal, including
// `sessions:channel:admin`, and that overlap remains a documented limitation.
const capabilityQuestions = [] // {operation, node} — CONSUMED questions only

const capsSf = program
  .getSourceFiles()
  .find((sf) => sf.fileName.endsWith(path.join('src', 'lib', 'auth', 'capabilities.ts')))
const capQuestionDecls = new Set()
const consumerDecls = new Set()
const plumbingDecls = new Set()
function noteCapabilityExport(name, decl) {
  if (name === 'capabilityQuestion') {
    capQuestionDecls.add(decl)
    plumbingDecls.add(decl)
    const inner = callableOf(decl)
    if (inner) plumbingDecls.add(inner)
  }
  if (name === 'useCapability' || name === 'requestCapabilityPermit') {
    consumerDecls.add(decl)
    plumbingDecls.add(decl)
    const inner = callableOf(decl)
    if (inner) plumbingDecls.add(inner)
  }
  if (name === 'useCapabilityPreflight') {
    plumbingDecls.add(decl)
    const inner = callableOf(decl)
    if (inner) plumbingDecls.add(inner)
  }
}
if (capsSf) {
  walk(capsSf, (n) => {
    if (ts.isFunctionDeclaration(n) && n.name) noteCapabilityExport(n.name.text, n)
    if (ts.isVariableDeclaration(n) && ts.isIdentifier(n.name)) noteCapabilityExport(n.name.text, n)
    if (
      ts.isInterfaceDeclaration(n) &&
      n.name.text === 'CapabilityPreflight'
    ) {
      for (const m of n.members) {
        if (
          ts.isPropertySignature(m) &&
          ts.isIdentifier(m.name) &&
          m.name.text === 'request'
        ) {
          consumerDecls.add(m)
        }
      }
    }
  })
}

function unwrapExpr(expr) {
  while (expr) {
    if (ts.isParenthesizedExpression(expr)) {
      expr = expr.expression
      continue
    }
    if (ts.isAsExpression(expr) || ts.isSatisfiesExpression?.(expr) || ts.isNonNullExpression(expr)) {
      expr = expr.expression
      continue
    }
    break
  }
  return expr
}

function calleeNameNode(call) {
  const expr = unwrapExpr(call.expression)
  if (!expr) return null
  if (ts.isPropertyAccessExpression(expr)) return expr.name
  if (ts.isIdentifier(expr)) return expr
  return null
}

function isFunctionLike(n) {
  return (
    ts.isFunctionDeclaration(n) ||
    ts.isFunctionExpression(n) ||
    ts.isArrowFunction(n) ||
    ts.isMethodDeclaration(n)
  )
}

function callableOf(decl) {
  if (!decl) return null
  if (isFunctionLike(decl)) return decl
  if (ts.isVariableDeclaration(decl) && decl.initializer) {
    const init = unwrapExpr(decl.initializer)
    if (init && (ts.isFunctionExpression(init) || ts.isArrowFunction(init))) return init
  }
  if (ts.isPropertyAssignment(decl) && decl.initializer) {
    const init = unwrapExpr(decl.initializer)
    if (init && (ts.isFunctionExpression(init) || ts.isArrowFunction(init))) return init
  }
  return null
}

function isInsidePlumbing(node) {
  for (let p = node.parent; p; p = p.parent) {
    if (ts.isSourceFile(p)) return false
    if (plumbingDecls.has(p)) return true
    const inner = callableOf(p)
    if (inner && plumbingDecls.has(inner)) return true
  }
  return false
}

function recordConsumedCapabilityQuestionSite(operation, node) {
  capabilityQuestions.push({ operation, node })
}

// FeatureCapability.surface/deepLink are the registry's actual consumer path
// (authorization.ts calls them; the five builders are the values written there).
// Nested `{ surface, deepLink }` literals do not take FeatureCapability as a
// contextual type, so writesTo() on those members is empty; FeatureView.capability
// DOES, and the object literal it receives names the builders.
const featureCapFields = new Map()
let featureViewCapabilitySig = null
for (const sf of sourceFiles) {
  walk(sf, (n) => {
    if (ts.isInterfaceDeclaration(n) && n.name.text === 'FeatureCapability') {
      for (const m of n.members) {
        if (
          ts.isPropertySignature(m) &&
          ts.isIdentifier(m.name) &&
          (m.name.text === 'surface' || m.name.text === 'deepLink')
        ) {
          featureCapFields.set(m.name.text, m)
        }
      }
    }
    if (ts.isInterfaceDeclaration(n) && n.name.text === 'FeatureView') {
      for (const m of n.members) {
        if (
          ts.isPropertySignature(m) &&
          ts.isIdentifier(m.name) &&
          m.name.text === 'capability'
        ) {
          featureViewCapabilitySig = m
        }
      }
    }
  })
}

const productionByName = new Map()
for (const sf of sourceFiles) productionByName.set(sf.fileName, sf)

const compilerOptions = program.getCompilerOptions()
function resolveImportedProduction(fromSf, spec) {
  const r = ts.resolveModuleName(spec, fromSf.fileName, compilerOptions, ts.sys)
  const fileName = r.resolvedModule?.resolvedFileName
  if (!fileName) return null
  return productionByName.get(fileName) ?? null
}

function importedSpecsFrom(sf) {
  const specs = []
  walk(sf, (n) => {
    if (ts.isImportDeclaration(n)) {
      if (n.importClause?.isTypeOnly) return
      if (n.moduleSpecifier && ts.isStringLiteral(n.moduleSpecifier)) specs.push(n.moduleSpecifier.text)
      return
    }
    if (ts.isExportDeclaration(n)) {
      if (n.isTypeOnly) return
      if (n.moduleSpecifier && ts.isStringLiteral(n.moduleSpecifier)) specs.push(n.moduleSpecifier.text)
      return
    }
    if (
      ts.isCallExpression(n) &&
      n.expression.kind === ts.SyntaxKind.ImportKeyword &&
      n.arguments[0] &&
      ts.isStringLiteral(n.arguments[0])
    ) {
      specs.push(n.arguments[0].text)
    }
  })
  return specs
}

const liveFiles = new Set()
{
  const queue = []
  for (const c of callSites) {
    const sf = c.node.getSourceFile()
    if (!liveFiles.has(sf)) {
      liveFiles.add(sf)
      queue.push(sf)
    }
  }
  while (queue.length) {
    const sf = queue.pop()
    for (const spec of importedSpecsFrom(sf)) {
      const dep = resolveImportedProduction(sf, spec)
      if (!dep || liveFiles.has(dep)) continue
      liveFiles.add(dep)
      queue.push(dep)
    }
  }
}

/** The nearest function/method whose execution owns `node`. */
function enclosingCallable(node) {
  for (let p = node.parent; p; p = p.parent) {
    if (isFunctionLike(p)) return p
    if (ts.isSourceFile(p)) return null
  }
  return null
}

// React's memo hooks are syntax only after their exported SYMBOL says so. Local
// helpers called useMemo/useCallback may ignore the callback entirely and cannot be
// treated as React merely because they share the spelling. Resolving exports from the
// module symbol preserves named-import aliases, namespace access and local re-exports.
const reactMemoDecls = new Map()
for (const sf of sourceFiles) {
  walk(sf, (n) => {
    if (
      !(ts.isImportDeclaration(n) || ts.isExportDeclaration(n)) ||
      !n.moduleSpecifier ||
      !ts.isStringLiteral(n.moduleSpecifier) ||
      n.moduleSpecifier.text !== 'react'
    ) {
      return
    }
    let moduleSymbol = checker.getSymbolAtLocation(n.moduleSpecifier)
    if (!moduleSymbol) return
    if (moduleSymbol.flags & ts.SymbolFlags.Alias) {
      try {
        moduleSymbol = checker.getAliasedSymbol(moduleSymbol)
      } catch {
        return
      }
    }
    for (const exported of checker.getExportsOfModule(moduleSymbol)) {
      if (exported.name !== 'useMemo' && exported.name !== 'useCallback') continue
      let target = exported
      if (target.flags & ts.SymbolFlags.Alias) {
        try {
          target = checker.getAliasedSymbol(target)
        } catch {
          continue
        }
      }
      for (const decl of target.declarations ?? []) reactMemoDecls.set(decl, exported.name)
    }
  })
}

/**
 * A bounded execution graph for consumer credit.
 *
 * Every real can() call supplies an already-established console execution root. From
 * those roots we follow only syntax that demonstrates the next callable can execute:
 * an ordinary call, a rendered JSX component, or a callback handed to an external
 * typed API (including callbacks in an inline options object). A local helper has a
 * body the scanner would have to prove invokes its callback; this bounded graph does
 * not guess. Merely declaring a hook in one of the imported files creates no edge and
 * therefore earns no credit.
 */
const executionEdges = new Map()
function addExecutionEdge(owner, target) {
  if (!owner || !target || owner === target) return
  let targets = executionEdges.get(owner)
  if (!targets) {
    targets = new Set()
    executionEdges.set(owner, targets)
  }
  targets.add(target)
}

function addCallbacksInArgument(owner, arg) {
  const visit = (n) => {
    if (isFunctionLike(n)) {
      addExecutionEdge(owner, n)
      return
    }
    ts.forEachChild(n, visit)
  }
  visit(arg)
}

for (const sf of liveFiles) {
  walk(sf, (n) => {
    if (ts.isCallExpression(n) && !isInsidePlumbing(n)) {
      const owner = enclosingCallable(n)
      const nameNode = calleeNameNode(n)
      const calleeDecl = nameNode ? canonicalDecl(nameNode) : null
      if (owner && calleeDecl) {
        let target = callableOf(calleeDecl)
        // React.useCallback retains its argument. The callback becomes executable
        // only when the returned value is itself called (including through an alias).
        if (!target && ts.isVariableDeclaration(calleeDecl) && calleeDecl.initializer) {
          const wrapped = memoizedCallable(unwrapExpr(calleeDecl.initializer))
          if (wrapped?.kind === 'useCallback') target = wrapped.fn
        }
        addExecutionEdge(owner, target)
      }
      // Package/framework declarations describe callbacks that the invoked API may
      // execute. For a source-local helper, passing a closure alone proves nothing:
      // the IR2 namesake returned null without ever invoking the closure. React's real
      // useCallback is also known to retain, rather than execute, its callback.
      if (
        owner &&
        calleeDecl?.getSourceFile().isDeclarationFile &&
        reactMemoDecls.get(calleeDecl) !== 'useCallback'
      ) {
        for (const arg of n.arguments) addCallbacksInArgument(owner, arg)
      }
      return
    }
    if (ts.isJsxOpeningElement(n) || ts.isJsxSelfClosingElement(n)) {
      const owner = enclosingCallable(n)
      const tag = n.tagName
      const nameNode = ts.isPropertyAccessExpression(tag)
        ? tag.name
        : ts.isIdentifier(tag)
          ? tag
          : null
      if (owner && nameNode) addExecutionEdge(owner, callableOf(canonicalDecl(nameNode)))
    }
  })
}

const reachableCallables = new Set()
{
  const queue = []
  for (const c of callSites) {
    // A can() may sit in a callback created by the component (the shared
    // useViewAccess projection does exactly this inside React.useMemo). Creating that
    // callback demonstrates every containing callable on the lexical chain; it says
    // nothing about sibling declarations, so a never-called hook remains unreachable.
    for (let p = c.node.parent; p; p = p.parent) {
      if (ts.isSourceFile(p)) break
      if (!isFunctionLike(p) || reachableCallables.has(p)) continue
      reachableCallables.add(p)
      queue.push(p)
    }
  }
  while (queue.length) {
    const owner = queue.pop()
    for (const target of executionEdges.get(owner) ?? []) {
      if (reachableCallables.has(target)) continue
      reachableCallables.add(target)
      queue.push(target)
    }
  }
}

function isInReachableExecution(node) {
  const owner = enclosingCallable(node)
  return !!owner && reachableCallables.has(owner)
}

function extractOperationsFromCapabilityQuestionCall(call) {
  const arg = call.arguments[0]
  if (!arg || !ts.isObjectLiteralExpression(arg)) return null
  const prop = arg.properties.find(
    (p) =>
      ts.isPropertyAssignment(p) &&
      (ts.isIdentifier(p.name) || ts.isStringLiteral(p.name)) &&
      p.name.text === 'operation',
  )
  if (!prop || !ts.isPropertyAssignment(prop)) return null
  return resolve(prop.initializer)
}

function exportedNameOf(decl) {
  if (ts.isFunctionDeclaration(decl) && decl.name) return decl.name.text
  if (ts.isVariableDeclaration(decl) && ts.isIdentifier(decl.name)) return decl.name.text
  if (ts.isPropertySignature(decl) && ts.isIdentifier(decl.name)) return decl.name.text
  return null
}

function questionArgOfConsumer(call, decl) {
  if (exportedNameOf(decl) === 'requestCapabilityPermit') {
    const arg = call.arguments[0]
    if (!arg) return { kind: 'expr', expr: null, missing: true }
    if (!ts.isObjectLiteralExpression(arg)) return { kind: 'unreadable', node: arg }
    const prop = arg.properties.find(
      (p) =>
        ts.isPropertyAssignment(p) &&
        (ts.isIdentifier(p.name) || ts.isStringLiteral(p.name)) &&
        p.name.text === 'question',
    )
    if (!prop || !ts.isPropertyAssignment(prop)) return { kind: 'unreadable', node: arg }
    return { kind: 'expr', expr: prop.initializer, missing: false }
  }
  return { kind: 'expr', expr: call.arguments[0] ?? null, missing: call.arguments.length === 0 }
}

const callsByCallable = new Map()
for (const sf of liveFiles) {
  walk(sf, (n) => {
    if (!ts.isCallExpression(n) || isInsidePlumbing(n) || !isInReachableExecution(n)) return
    const nameNode = calleeNameNode(n)
    if (!nameNode) return
    const callable = callableOf(canonicalDecl(nameNode))
    if (!callable) return
    let list = callsByCallable.get(callable)
    if (!list) {
      list = []
      callsByCallable.set(callable, list)
    }
    list.push(n)
  })
}

function memoizedCallable(expr) {
  if (!ts.isCallExpression(expr)) return null
  const name = calleeNameNode(expr)
  if (!name) return null
  const kind = reactMemoDecls.get(canonicalDecl(name))
  if (!kind) return null
  const fn = unwrapExpr(expr.arguments[0])
  if (fn && (ts.isArrowFunction(fn) || ts.isFunctionExpression(fn))) return { fn, kind }
  return null
}

function resolveFeatureCapabilityField(fieldName, depth, seen) {
  if (!featureViewCapabilitySig) return null
  const { writes, opaque } = writesTo(featureViewCapabilitySig)
  if (opaque) return null
  if (writes.length === 0) return null
  const out = []
  for (const w of writes) {
    const obj = unwrapExpr(w)
    if (obj && ts.isObjectLiteralExpression(obj)) {
      const prop = obj.properties.find(
        (p) =>
          ts.isPropertyAssignment(p) &&
          (ts.isIdentifier(p.name) || ts.isStringLiteral(p.name)) &&
          p.name.text === fieldName,
      )
      if (!prop || !ts.isPropertyAssignment(prop)) continue
      const v = resolveQuestionExpr(prop.initializer, depth + 1, seen)
      if (v === null) return null
      out.push(...v)
      continue
    }
    const v = resolveQuestionExpr(w, depth + 1, seen)
    if (v === null) return null
    out.push(...v)
  }
  return out
}

function resolveQuestionExpr(expr, depth = 0, seen = new Set()) {
  expr = unwrapExpr(expr)
  if (!expr || depth > 16) return null
  if (expr.kind === ts.SyntaxKind.NullKeyword) return []
  if (ts.isIdentifier(expr) && (expr.text === 'undefined' || expr.text === 'null')) return []
  const memo = memoizedCallable(expr)
  if (memo) return extractOperationsFromFunction(memo.fn, depth + 1, seen)
  if (ts.isConditionalExpression(expr)) {
    const a = resolveQuestionExpr(expr.whenTrue, depth + 1, seen)
    const b = resolveQuestionExpr(expr.whenFalse, depth + 1, seen)
    return a && b ? [...a, ...b] : null
  }
  if (
    ts.isBinaryExpression(expr) &&
    (expr.operatorToken.kind === ts.SyntaxKind.QuestionQuestionToken ||
      expr.operatorToken.kind === ts.SyntaxKind.BarBarToken)
  ) {
    const a = resolveQuestionExpr(expr.left, depth + 1, seen)
    const b = resolveQuestionExpr(expr.right, depth + 1, seen)
    return a && b ? [...a, ...b] : null
  }

  if (ts.isCallExpression(expr)) {
    const nameNode = calleeNameNode(expr)
    if (!nameNode) return null
    let d = canonicalDecl(nameNode)
    if (d && capQuestionDecls.has(d)) return extractOperationsFromCapabilityQuestionCall(expr)
    const callable = callableOf(d)
    if (callable) return extractOperationsFromFunction(callable, depth + 1, seen)
    // `const ask = React.useCallback(() => question, []); ask()` — unlike useMemo,
    // useCallback returns the callback rather than its value, so the invocation is
    // the demonstrated point at which its returned question exists.
    if (d && ts.isVariableDeclaration(d) && d.initializer) {
      const wrapped = memoizedCallable(unwrapExpr(d.initializer))
      if (wrapped?.kind === 'useCallback') {
        return extractOperationsFromFunction(wrapped.fn, depth + 1, seen)
      }
    }
    const field = nameNode.text
    if (
      (d && featureCapFields.get(field) === d) ||
      (!d && featureCapFields.has(field))
    ) {
      return resolveFeatureCapabilityField(field, depth + 1, seen)
    }
    if (d && (ts.isPropertySignature(d) || ts.isPropertyDeclaration(d))) {
      const { writes, opaque } = writesTo(d)
      if (opaque) return null
      if (writes.length === 0) {
        if (featureCapFields.has(field)) return resolveFeatureCapabilityField(field, depth + 1, seen)
        return null
      }
      const out = []
      for (const w of writes) {
        const v = resolveQuestionExpr(w, depth + 1, new Set(seen))
        if (v === null) return null
        out.push(...v)
      }
      return out
    }
    return null
  }

  const nameNode = ts.isPropertyAccessExpression(expr)
    ? expr.name
    : ts.isIdentifier(expr)
      ? expr
      : null
  if (!nameNode) return null
  const d = declOf(nameNode)
  if (!d) return null
  if (seen.has(d)) return []
  const next = new Set(seen)
  next.add(d)
  if (ts.isParameter(d)) return resolveParameterWrites(d, depth + 1, next)
  if (ts.isVariableDeclaration(d) && d.initializer) {
    return resolveQuestionExpr(d.initializer, depth + 1, next)
  }
  if (ts.isPropertyAssignment(d)) return resolveQuestionExpr(d.initializer, depth + 1, next)
  if (ts.isShorthandPropertyAssignment(d)) {
    const v = checker.getShorthandAssignmentValueSymbol(d)?.declarations?.[0]
    return v && ts.isVariableDeclaration(v) ? resolveQuestionExpr(v.initializer, depth + 1, next) : null
  }
  const callable = callableOf(d) || (ts.isFunctionDeclaration(d) ? d : null)
  // extractOperationsFromFunction records `callable` itself. Adding the same
  // FunctionDeclaration to `seen` here made every builder identifier return [].
  if (callable) return extractOperationsFromFunction(callable, depth + 1, seen)
  const sig = sinkSignatureOf(d)
  if (sig) {
    const { writes, opaque } = writesTo(sig)
    if (opaque) return null
    if (writes.length === 0) return null
    const out = []
    for (const w of writes) {
      const v = resolveQuestionExpr(w, depth + 1, new Set(next))
      if (v === null) return null
      out.push(...v)
    }
    return out
  }
  return null
}

function extractOperationsFromFunction(callable, depth, seen) {
  if (!callable || depth > 16) return null
  if (seen.has(callable)) return []
  const next = new Set(seen)
  next.add(callable)
  const body = callable.body
  if (!body) return null
  if (!ts.isBlock(body)) return resolveQuestionExpr(body, depth + 1, next)
  const out = []
  let unreadable = false
  const visit = (n) => {
    if (isFunctionLike(n) || ts.isClassDeclaration(n)) return
    if (ts.isReturnStatement(n)) {
      if (!n.expression) return
      const v = resolveQuestionExpr(n.expression, depth + 1, next)
      if (v === null) unreadable = true
      else out.push(...v)
      return
    }
    ts.forEachChild(n, visit)
  }
  for (const stmt of body.statements) visit(stmt)
  return unreadable ? null : out
}

function resolveParameterWrites(param, depth, seen) {
  const fn = param.parent
  if (!fn || !fn.parameters) return []
  const idx = fn.parameters.indexOf(param)
  if (idx < 0) return null
  const callers = callsByCallable.get(fn) ?? []
  if (callers.length === 0) return []
  const out = []
  for (const call of callers) {
    const arg = call.arguments[idx]
    if (!arg) return null
    const v = resolveQuestionExpr(arg, depth + 1, seen)
    if (v === null) return null
    out.push(...v)
  }
  return out
}

for (const sf of sourceFiles) {
  walk(sf, (n) => {
    if (!ts.isCallExpression(n) || isInsidePlumbing(n)) return
    const nameNode = calleeNameNode(n)
    if (!nameNode) return
    const d = canonicalDecl(nameNode)
    const live = liveFiles.has(sf) && isInReachableExecution(n)

    if (nameNode.text === 'capabilityQuestion' && !capQuestionDecls.has(d)) {
      if (live) {
        finding(
          'unreadable',
          n,
          'a call to something named capabilityQuestion() that does not resolve to ' +
            `web/src/lib/auth/capabilities.ts. Declared at ${d ? at(d) : 'nowhere this guard could follow'}. ` +
            'If this is an unrelated helper, it needs a different name — matching the text is not a consumer.',
        )
      }
      return
    }

    const isConsumer = !!d && consumerDecls.has(d)
    if (!isConsumer) {
      if (
        live &&
        (nameNode.text === 'useCapability' || nameNode.text === 'requestCapabilityPermit')
      ) {
        finding(
          'unreadable',
          n,
          `a call to something named ${nameNode.text}() that does not resolve to ` +
            `web/src/lib/auth/capabilities.ts. Declared at ${d ? at(d) : 'nowhere this guard could follow'}. ` +
            'A namesake hook is not a capability consumer.',
        )
      }
      return
    }
    if (!live) return

    const arg = questionArgOfConsumer(n, d)
    if (arg.kind === 'unreadable') {
      finding(
        'unreadable',
        arg.node,
        `${nameNode.text}() received a question this guard cannot read. ` +
          'An argument it cannot follow is a route whose console surface it cannot account for.',
      )
      return
    }
    if (arg.missing || arg.expr == null) {
      if (arg.missing) {
        finding('unreadable', n, `${nameNode.text}() called with no question argument`)
      }
      return
    }
    const values = resolveQuestionExpr(arg.expr)
    if (values === null) {
      finding(
        'unreadable',
        arg.expr,
        `cannot resolve the question passed to ${nameNode.text}(): \`${arg.expr
          .getText(sf)
          .replace(/\s+/g, ' ')
          .slice(0, 90)}\`. A consumer whose question this guard cannot read is a ` +
          'route whose console surface it cannot account for.',
      )
      return
    }
    for (const v of values) recordConsumedCapabilityQuestionSite(v, arg.expr)
  })
}

// --- 4. compare ------------------------------------------------------------
inv = engineInventory()
const sitesOf = new Map()
for (const c of callSites) {
  if (!sitesOf.has(c.permission)) sitesOf.set(c.permission, [])
  sitesOf.get(c.permission).push(c.node)
}

// The permissions a REGISTERED CAPABILITY QUESTION covers: the console named a mounted
// route, and the engine declares that route under this permission. Both halves come from
// the same two sources the rest of this guard reads — the console's own source and
// permsdump — so neither side can be satisfied by a string nobody uses.
//
// ⛔ RESOLVED HERE, ABOVE THE `--json` EARLY RETURN, so a question the engine cannot
//    answer is a FINDING in every mode. The census that consumes `porCapacidad` sits
//    below that return and is only visible on stderr; hanging this check off it would
//    have made the defect invisible to every caller that asks for JSON.
const rutaDeOperacion = new Map()
for (const m of inv.modules ?? []) {
  for (const r of m.routes ?? []) {
    if (!r.permission || !r.method || !r.pattern) continue
    rutaDeOperacion.set(`${r.method} /v1/m/${m.namespace}${r.pattern}`, r.permission)
  }
}
const porCapacidad = new Map()
for (const q of capabilityQuestions) {
  const perm = rutaDeOperacion.get(q.operation)
  if (!perm) {
    // A question naming an operation the engine does not mount is the SAME class of defect
    // this guard exists for, in the other direction: the screen would be permanently
    // unavailable and nothing would say why. The engine answers `not_supported`, which is
    // an unknown, so it never grants — but it never works either.
    finding(
      'undeclared',
      q.node,
      `the console asks the capability endpoint about \`${q.operation}\`, which the engine ` +
        'mounts under no route. It can never answer anything but not_supported.',
    )
    continue
  }
  if (!porCapacidad.has(perm)) porCapacidad.set(perm, [])
  porCapacidad.get(perm).push(q.operation)
}

const FORMS =
  'core (auth.PermissionsForRole, built by concatenation) · privileged ' +
  '(auth.PrivilegedReadPerms) · module (Module.Permissions()) · route (the permission a ' +
  'mounted route requires)'

for (const [permission, nodes] of [...sitesOf].sort()) {
  const d = inv.declared[permission]
  if (!d) {
    // Class 1: nothing in the engine declares it. There is no route to 403 — the
    // action the console offers does not exist server-side at all. Since can() became
    // set membership the CONSEQUENCE inverted: the string can appear in no effective
    // set, so instead of being silently granted it is silently hidden, for every role,
    // forever, with no failed request anywhere to notice it by.
    const opensTo = ROLES.filter((r) => consoleCan(r, permission))
    findings.push({
      kind: 'undeclared',
      where: at(nodes[0]),
      detail:
        `the console gates on "${permission}", which the engine declares in NO form. ` +
        `Searched: ${FORMS}. No effective set can contain it, so this action is hidden ` +
        'for every role and no request ever fails to say so.' +
        (opensTo.length
          ? ` The console nevertheless opens it to: ${opensTo.join(', ')} — can() is not honest set membership.`
          : '') +
        (nodes.length > 1 ? ` (+${nodes.length - 1} more call sites)` : ''),
      permission,
      sites: nodes.map(at),
    })
    continue
  }
  // Class 2: declared, and the console's answer must be the engine's. The console is
  // driven with the engine's own effective set, so a difference means can() is doing
  // something OTHER than looking the permission up — a verb tier, a role rank or a
  // special case grown back on top of the lookup. Each of those is a defect this console
  // has actually shipped.
  for (const role of ROLES) {
    const mine = consoleCan(role, permission)
    const theirs = d.grants[role]
    if (mine !== theirs) {
      findings.push({
        kind: 'divergent',
        where: at(nodes[0]),
        detail:
          `"${permission}": the console says ${role} ${mine ? 'MAY' : 'may NOT'} do this, the ` +
          `engine says ${theirs ? 'MAY' : 'may NOT'}, given the engine's OWN set for that role. ` +
          `Declared as [${d.forms.join(', ')}].` +
          (mine
            ? ' The console offers an action the backend answers 403 to.'
            : ' The console hides an action the principal is entitled to.'),
        permission,
        sites: nodes.map(at),
      })
      break // report the lowest diverging role; the rest is the same defect
    }
  }
}

// ═══ LA DIRECCIÓN INVERSA, ACOTADA PARA QUE NO GRITE EN FALSO ═══════════════════════════
//
// Todo lo de arriba pregunta «¿existe en el motor lo que la consola pide?». Nada preguntaba lo
// contrario, y por ahí se coló un defecto real: el motor exigía `compliance:aims:read|admin` en
// las cinco rutas `/aims/pack*` y la consola servía esa familia bajo `compliance:depth:read`.
// Quien tenía `aims:read` y no `depth:read` no veía nada; quien tenía `depth:read` y no
// `aims:read` veía la pantalla y cada llamada le daba 403. Arreglado el 2026-08-19, y este
// trinquete existe para que el siguiente se cace solo.
//
// ⛔ LA VERSIÓN INGENUA DE ESTA COMPROBACIÓN LA RECHAZA LA CABECERA DE ESTE FICHERO, con razón:
//    cruzar la consola contra TODAS las declaraciones da 48 candidatos de los que 45 son
//    permisos core ordinarios, y «un guardián que grita en falso 45 de 48 se apaga en una
//    semana». Por eso el alcance se acota a los permisos declarados EN UNA RUTA MONTADA
//    (`inv.modules[].routes[].permission`): los core concatenados no aparecen ahí por
//    construcción, así que la clase de falso positivo que preocupaba queda fuera sin listas de
//    exclusión que envejezcan.
//
// Es un TRINQUETE, no un umbral. Exigir cero hoy sería un gate que nadie puede satisfacer, y un
// gate que nadie puede satisfacer es un gate que alguien apaga.
// MEDIDO el 2026-08-19 con el resolutor DE ESTE FICHERO: **28**. Una sonda mía por `grep` de
// literales daba 40 porque veía 177 permisos donde éste resuelve 188 — la diferencia son las
// constantes, props y propiedades de objeto que sólo un parseo real alcanza. 40 era un TECHO, no
// un censo, y fijar el trinquete ahí lo habría dejado flojo en doce.
// MEDIDO el 2026-09-06 sobre el árbol de K3 I1 (web/src/features/communications): **25**. Las
// cinco patas que bajan de 30 a 25 son las que ese incremento hace pedir a can() desde una
// pantalla real —sessions:channel:write, sessions:delivery:read, sessions:delivery:write,
// sessions:message-send:write y sessions:message:read—; quedan sessions:channel:admin y
// sessions:handoff-response:write (I2/I3 del plan) y sessions:decision:write, que no es K3.
// El trinquete baja al número verificado por este mismo guion, nunca a una cifra deducida.
// MEDIDO el 2026-09-07 sobre el árbol de K3 I2 (la puerta /communications/administration y su
// hoja): **24**. La pata que baja de 25 a 24 es sessions:channel:admin, que ahora pide can() desde
// una pantalla real (el registro, la pestaña Administración, el guard de cada acto administrativo);
// quedan sessions:handoff-response:write (I3) y sessions:decision:write, que no es K3.
// MEDIDO el 2026-09-10 sobre el árbol de K3 I3 (la puerta /communications/handoffs, su hoja y el
// host de respuesta): **22**. La pata que se cubre es sessions:handoff-response:write, que ahora
// pide can() desde una pantalla real — la vista de comunicaciones, el control de respuesta de la
// hoja y el guard del acto en handoff-response-host.tsx. Queda sessions:decision:write, que no
// es K3.
// La cifra no es la que el contrato preveía (esperaba 23 bajando de 24) y se fija la MEDIDA:
// sobre el árbol base 27054d311a, antes de tocar nada, este guion ya daba 23 contra un trinquete
// de 24, así que el trinquete venía uno flojo. 23 − 1 = 22.
const SIN_SUPERFICIE_TRINQUETE = 22
const enRutas = new Map()
for (const m of inv.modules ?? []) {
  for (const r of m.routes ?? []) {
    if (!r.permission || r.surface === 'api-only' || r.surface === 'edition-refusal') continue
    if (!enRutas.has(r.permission)) enRutas.set(r.permission, [])
    enRutas.get(r.permission).push(`${m.namespace}${r.pattern}`)
  }
}
if (!(inv.modules ?? []).some((m) => (m.routes ?? []).some((r) => r.permission))) {
  die('the inventory declared no route permissions at all; refusing to report the console clean')
}
if (porCapacidad.size > 0) {
  const filas = [...porCapacidad]
    .sort()
    .map(([p, ops]) => `${p} (${[...new Set(ops)].sort().join(', ')})`)
  console.error(
    `check-console-perms: ${porCapacidad.size} route permission(s) have a REGISTERED ` +
      `CAPABILITY QUESTION as their console surface: ${filas.join('; ')}`,
  )
}
const sinSuperficie = [...enRutas.keys()]
  .filter((perm) => !sitesOf.has(perm) && !porCapacidad.has(perm))
  .sort()
for (const perm of sinSuperficie) {
  const rutas = enRutas.get(perm)
  console.error(
    `    ${perm} — ${rutas.length} route(s) require it and no can() asks for it` +
      ` (e.g. ${rutas[0]})`,
  )
}
console.error(
  `check-console-perms: ${sinSuperficie.length} route permission(s) have no console surface` +
    ` (ratchet ${SIN_SUPERFICIE_TRINQUETE}).`,
)
if (sinSuperficie.length > SIN_SUPERFICIE_TRINQUETE) {
  console.error(
    'The list is above. A route landed with a permission no screen asks for, or a screen that' +
      ' asked for one stopped. This number is never raised to make it pass.',
  )
  findings.push({
    kind: 'divergent',
    where: 'engine routes',
    detail:
      `${sinSuperficie.length} route permission(s) have no console surface, ratchet ` +
      `${SIN_SUPERFICIE_TRINQUETE}`,
  })
}

// --- 5. report -------------------------------------------------------------
if (args.has('--list')) {
  // Every permission the console can ask about, with where it was found. Not part
  // of the check — it is how a human audits what the guard believes it is guarding.
  for (const [p, nodes] of [...sitesOf].sort()) {
    const d = inv.declared[p]
    console.log(`${p}\t${d ? d.forms.join('|') : 'UNDECLARED'}\t${nodes.length} site(s)\t${at(nodes[0])}`)
  }
  process.exit(findings.length ? 1 : 0)
}

if (asJson) {
  console.log(
    JSON.stringify(
      {
        checked: sitesOf.size,
        callSites: callSites.length,
        capabilityQuestions: capabilityQuestions.length,
        findings,
      },
      null,
      2,
    ),
  )
  process.exit(findings.length ? 1 : 0)
}


const order = { unreadable: 0, undeclared: 1, divergent: 2 }
findings.sort((a, b) => (order[a.kind] - order[b.kind]) || a.where.localeCompare(b.where))
for (const f of findings) {
  console.error(`${f.where}: [${f.kind}] ${f.detail}`)
  if (f.sites && f.sites.length > 1) {
    for (const s of f.sites.slice(1)) console.error(`    also at ${s}`)
  }
}
const scope = `${sitesOf.size} distinct permissions across ${callSites.length} can() call sites and ${capabilityQuestions.length} registered capability question(s), against ${Object.keys(inv.declared).length} declared by the engine`
if (findings.length) {
  console.error(
    `\ncheck-console-perms: ${findings.length} finding(s) over ${scope}.\n` +
      'The console must not decide with a rule the engine does not share.',
  )
  process.exit(1)
}
console.log(`check-console-perms OK: ${scope}.`)
