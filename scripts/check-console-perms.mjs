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
import { existsSync } from 'node:fs'
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
  process.exit(2)
}

// --- typescript ------------------------------------------------------------
// Loaded from the workspace's own pnpm store: the guard must parse the console
// with the SAME compiler the console is built with, or it is reading a different
// language than the one that ships.
const tsEntry = await resolveTypeScript()
const ts = (await import(pathToFileURL(tsEntry).href)).default

async function resolveTypeScript() {
  const { readdirSync } = await import('node:fs')
  // web/node_modules first: that is the compiler the console is actually built
  // with (pnpm --dir web install), and CI installs there, not at the repo root.
  // The root tree only carries commit tooling, so preferring it would silently
  // parse the console with a different compiler than the one that ships it.
  for (const base of [path.join(WEB, 'node_modules'), path.join(REPO, 'node_modules')]) {
    const direct = path.join(base, 'typescript', 'lib', 'typescript.js')
    if (existsSync(direct)) return direct
    // pnpm keeps the real package under .pnpm/typescript@<version>/…
    const store = path.join(base, '.pnpm')
    if (!existsSync(store)) continue
    const hit = readdirSync(store)
      .filter((d) => d.startsWith('typescript@'))
      .sort()
      .pop()
    if (hit) {
      const p = path.join(store, hit, 'node_modules', 'typescript', 'lib', 'typescript.js')
      if (existsSync(p)) return p
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
  const name = ts.isIdentifier(decl.name) ? decl.name.text : null
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

/**
 * Resolve an expression to the set of permission strings it can carry.
 * Returns null when it cannot be resolved — which the caller turns into a finding,
 * never into silence.
 */
function resolve(expr, depth = 0, seen = new Set()) {
  if (!expr || depth > 6) return null
  if (ts.isStringLiteral(expr) || ts.isNoSubstitutionTemplateLiteral(expr)) return [expr.text]
  if (ts.isParenthesizedExpression(expr)) return resolve(expr.expression, depth + 1, seen)
  if (ts.isAsExpression(expr) || ts.isSatisfiesExpression?.(expr)) {
    return resolve(expr.expression, depth + 1, seen)
  }
  // `cond ? 'a' : 'b'` — both branches reach can().
  if (ts.isConditionalExpression(expr)) {
    const a = resolve(expr.whenTrue, depth + 1, seen)
    const b = resolve(expr.whenFalse, depth + 1, seen)
    return a && b ? [...a, ...b] : null
  }

  const nameNode = ts.isPropertyAccessExpression(expr)
    ? expr.name
    : ts.isIdentifier(expr)
      ? expr
      : null
  if (!nameNode) return null

  const decl = declOf(nameNode)
  if (!decl) return null
  if (seen.has(decl)) return []
  seen.add(decl)

  // A module constant, or one property of an object-literal constant.
  if (ts.isVariableDeclaration(decl) && decl.initializer) {
    return resolve(decl.initializer, depth + 1, seen)
  }
  if (ts.isPropertyAssignment(decl)) return resolve(decl.initializer, depth + 1, seen)
  if (ts.isShorthandPropertyAssignment(decl)) {
    const v = checker.getShorthandAssignmentValueSymbol(decl)?.declarations?.[0]
    return v && ts.isVariableDeclaration(v) ? resolve(v.initializer, depth + 1, seen) : null
  }
  if (ts.isEnumMember(decl)) return decl.initializer ? resolve(decl.initializer, depth + 1, seen) : null

  // An interface property or a component prop: resolve through everything that
  // WRITES it.
  const sig = sinkSignatureOf(decl)
  if (sig) {
    const { writes, opaque } = writesTo(sig)
    // A sink nothing writes is NOT an empty set of permissions — it is a sink this
    // guard failed to read. The distinction is the difference between a check and a
    // decoration: returning [] here contributes no permissions and no finding, so
    // an entire prop-fed surface would vanish in silence. It did: an early version
    // returned [] and a component whose permission arrives by JSX attribute
    // disappeared from the check without a word.
    if (writes.length === 0 && !opaque) return null
    // A spread that could be carrying this property is the same answer: not read.
    if (opaque) return null
    const out = []
    for (const w of writes) {
      const v = resolve(w, depth + 1, new Set(seen))
      if (v === null) return null // one unreadable write makes the whole sink unread
      out.push(...v)
    }
    return out
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
    const isCan = !!d && canDecls.has(d)
    if (!isCan && nameNode.text !== 'can') return
    if (!isCan) {
      finding(
        'unreadable',
        n,
        'a call to something named can() that does not resolve to the console RBAC mirror ' +
          `(web/src/lib/auth). Declared at ${d ? at(d) : 'nowhere this guard could follow'}. ` +
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
    const values = resolve(arg)
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

// --- 4. compare ------------------------------------------------------------
inv = engineInventory()
const sitesOf = new Map()
for (const c of callSites) {
  if (!sitesOf.has(c.permission)) sitesOf.set(c.permission, [])
  sitesOf.get(c.permission).push(c.node)
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
  console.log(JSON.stringify({ checked: sitesOf.size, callSites: callSites.length, findings }, null, 2))
  process.exit(findings.length ? 1 : 0)
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
const SIN_SUPERFICIE_TRINQUETE = 26
const enRutas = new Map()
for (const m of inv.modules ?? []) {
  for (const r of m.routes ?? []) {
    if (!r.permission) continue
    if (!enRutas.has(r.permission)) enRutas.set(r.permission, [])
    enRutas.get(r.permission).push(`${m.namespace}${r.pattern}`)
  }
}
if (enRutas.size === 0) {
  die('the inventory declared no route permissions at all; refusing to report the console clean')
}
const sinSuperficie = [...enRutas.keys()].filter((perm) => !sitesOf.has(perm)).sort()
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

const order = { unreadable: 0, undeclared: 1, divergent: 2 }
findings.sort((a, b) => (order[a.kind] - order[b.kind]) || a.where.localeCompare(b.where))
for (const f of findings) {
  console.error(`${f.where}: [${f.kind}] ${f.detail}`)
  if (f.sites && f.sites.length > 1) {
    for (const s of f.sites.slice(1)) console.error(`    also at ${s}`)
  }
}
const scope = `${sitesOf.size} distinct permissions across ${callSites.length} can() call sites, against ${Object.keys(inv.declared).length} declared by the engine`
if (findings.length) {
  console.error(
    `\ncheck-console-perms: ${findings.length} finding(s) over ${scope}.\n` +
      'The console must not decide with a rule the engine does not share.',
  )
  process.exit(1)
}
console.log(`check-console-perms OK: ${scope}.`)
