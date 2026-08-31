// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Red-case battery for check-console-perms.mjs. A gate is only worth its runtime if
// it has been SEEN to fail, and seen to fail for the RIGHT case.
//
// Each case builds a throwaway console under TMPDIR — a real tsconfig, the REAL
// rbac.ts copied in (so can() behaves exactly as it does in production), fixture
// views, and a stub `cmd/olivares/tools/permsdump` that prints a fixed inventory.
// The guard is then run unmodified, through its real code path including the Go
// invocation. Nothing outside the temp dir is touched, and no test-only bypass is
// added to the guard to make this possible: a check with a back door for its own
// battery has a back door.
//
// TWO RULES THIS BATTERY FOLLOWS, both learned from guards in this repo that went
// quietly blind:
//
//   Assert IDENTITY, never cardinals. A battery that counts findings cannot see one
//   MOVE between fixtures: a clause that stops catching case A and starts
//   double-counting case B keeps the total. Every expectation below is the exact
//   set of (file:line, kind, permission) triples.
//
//   Every clause must be shown to be LOAD-BEARING. For each detection there is a
//   fixture that goes green when the defect is removed — so a clause that silently
//   stopped working would show up here as a case that no longer fails.
//
// Run: node scripts/check-console-perms.selftest.mjs   (task lint:console-perms-selftest)

import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const REPO = path.resolve(HERE, '..')
const GUARD = path.join(HERE, 'check-console-perms.mjs')
const REAL_RBAC = fs.readFileSync(path.join(REPO, 'web', 'src', 'lib', 'auth', 'rbac.ts'), 'utf8')

// The stub inventory. Three permissions, one per declaration form that matters:
//   session:read        core, granted from viewer up (the CONCATENATED form)
//   voice:policy:admin  module + route, admin and up
//   authz:read          privileged: editor and up, NEVER viewer
const INVENTORY = {
  schema: 'olivares.permissions.inventory/1',
  roles: ['viewer', 'editor', 'admin', 'owner'],
  declared: {
    'session:read': { forms: ['core'], grants: { viewer: true, editor: true, admin: true, owner: true } },
    'session:write': { forms: ['core'], grants: { viewer: false, editor: true, admin: true, owner: true } },
    'voice:policy:admin': {
      forms: ['module:voice', 'route:voice'],
      grants: { viewer: false, editor: false, admin: true, owner: true },
    },
    'authz:read': {
      forms: ['privileged'],
      grants: { viewer: false, editor: true, admin: true, owner: true },
    },
  },
  modules: [],
}

const TSCONFIG = {
  compilerOptions: {
    target: 'es2023',
    lib: ['ES2023', 'DOM', 'DOM.Iterable'],
    module: 'esnext',
    moduleResolution: 'bundler',
    allowImportingTsExtensions: true,
    verbatimModuleSyntax: true,
    moduleDetection: 'force',
    noEmit: true,
    jsx: 'react-jsx',
    // The throwaway console has no React. A JSX element whose runtime types cannot
    // be found gets NO contextual type, so `<Gate permission="…">` resolves to
    // nothing and the prop-fed fixture would pass for the wrong reason — it would
    // look clean because the checker went blind, not because the code is right.
    jsxImportSource: '@/jsx-shim',
    strict: true,
    skipLibCheck: true,
    paths: { '@/*': ['./src/*'] },
  },
  include: ['src'],
}

// The smallest thing that makes `jsx: react-jsx` type-check without React.
const JSX_SHIM = `
export namespace JSX {
  interface Element { readonly _brand: unique symbol }
  interface ElementChildrenAttribute { children: object }
  interface IntrinsicElements { [name: string]: Record<string, unknown> }
}
export declare function jsx(type: unknown, props: unknown, key?: unknown): JSX.Element
export declare function jsxs(type: unknown, props: unknown, key?: unknown): JSX.Element
export declare const Fragment: unique symbol
`

// A minimal auth context: the guard anchors can() on the AuthContextValue member
// and on rbac.ts's exported function, so both must exist for the fixtures to be
// realistic.
const CONTEXT_TSX = `
import { can as rbacCan } from './rbac'
export interface AuthContextValue {
  can: (permission: string, opts?: { tenant?: string | null }) => boolean
}
export function useAuth(): AuthContextValue {
  return { can: (p: string) => rbacCan(p, { principal: null, tenant: null }) }
}
`

// Two rbac.ts MUTANTS, for the divergence cases. Since can() became set membership and
// the guard drives it with the ENGINE's own set, divergence can no longer be staged from
// the inventory — it can only come from a can() that does something other than look the
// permission up. These are the two shapes that have actually shipped in this console:
// a verb tier regrown on top of the lookup, and a hand-written special case.
//
// Both keep the real signature and the real lookup, so the fixture that goes green is
// green because the defect is gone, not because the module stopped loading.
const RBAC_FAIL_OPEN_READ = `
export function can(permission: string, ctx: { principal: any; tenant: string | null }): boolean {
  const { principal, tenant } = ctx
  if (!principal) return false
  if (principal.superadmin) return true
  const grant = principal.grants.find((g: any) => g.tenant === tenant)
  if (!grant) return false
  // THE DEFECT: the old verb fallback, regrown on top of the set.
  if (permission.endsWith(':read')) return true
  return (grant.permissions ?? []).includes(permission)
}
export function roleInTenant(principal: any, tenant: string | null): string | null {
  return principal?.grants.find((g: any) => g.tenant === tenant)?.role ?? null
}
`

const RBAC_SYSTEM_SPECIAL_CASE = `
export function can(permission: string, ctx: { principal: any; tenant: string | null }): boolean {
  const { principal, tenant } = ctx
  if (!principal) return false
  if (principal.superadmin) return true
  // THE DEFECT: a hand-written special case that outranks the engine's answer.
  if (permission.startsWith('system:')) return false
  const grant = principal.grants.find((g: any) => g.tenant === tenant)
  if (!grant) return false
  return (grant.permissions ?? []).includes(permission)
}
export function roleInTenant(principal: any, tenant: string | null): string | null {
  return principal?.grants.find((g: any) => g.tenant === tenant)?.role ?? null
}
`

let failures = 0
const results = []

/** Build a throwaway repo and return its root. */
function makeRepo(
  files,
  {
    inventory = INVENTORY,
    brokenEngine = false,
    // Knobs that break the ENVIRONMENT rather than the console. Each exists because a
    // fail-closed guard with no case is a guard nobody has watched refuse.
    noTypescript = false,
    noGoModule = false,
    badInventoryJson = false,
    tsconfig = TSCONFIG, // null omits it; a string writes it verbatim (malformed JSON)
    rbac = REAL_RBAC, // null omits the file entirely
    context = CONTEXT_TSX,
  } = {},
) {
  const root = fs.mkdtempSync(path.join(process.env.TMPDIR || os.tmpdir(), 'console-perms-'))
  const write = (rel, body) => {
    const full = path.join(root, rel)
    fs.mkdirSync(path.dirname(full), { recursive: true })
    fs.writeFileSync(full, body)
  }
  // The guard resolves the TypeScript compiler from web/node_modules FIRST and the
  // repo root SECOND; point the throwaway repo at whichever the host actually has,
  // rather than installing a second copy.
  //
  // web/ FIRST, and not the root, because the root is the one this repo never fills:
  // the only thing `lint:console-perms` installs is `pnpm --dir web install`, and the
  // root package.json is commitlint-only and declares no typescript. Symlinking the
  // root made this battery pass on the ONE machine it was written on — a hub whose
  // root node_modules had been installed months earlier and whose pnpm store happened
  // to carry typescript@6.0.3 — and fail 33 of its 34 cases in every worktree
  // `scripts/new-session.sh` creates. Measured 2026-08-08: 1/34 here, 34/34 with the
  // root pointed at web's store. A battery that only goes green at home is a battery
  // that tests the home, and this one sits in the FAST lane, where it would have
  // reddened every feature push.
  if (!noTypescript) {
    const hostModules = [path.join(REPO, 'web', 'node_modules'), path.join(REPO, 'node_modules')]
    const src = hostModules.find((p) => fs.existsSync(path.join(p, '.pnpm')) || fs.existsSync(path.join(p, 'typescript')))
    if (!src) {
      throw new Error(
        'self-test cannot run: no typescript compiler under web/node_modules or node_modules. ' +
          'Run `pnpm --dir web install` (this is what task lint:console-perms does first). ' +
          'Refusing to run the battery against a host that cannot compile: every case would ' +
          'fail for the environment rather than for the property it asserts.',
      )
    }
    // Mirror it at the SAME depth the guard looks in, so the throwaway resolves the
    // way the real repo does instead of via a fallback the real repo never uses.
    const dest =
      src === hostModules[0] ? path.join(root, 'web', 'node_modules') : path.join(root, 'node_modules')
    fs.mkdirSync(path.dirname(dest), { recursive: true })
    fs.symlinkSync(src, dest, 'dir')
  }
  if (tsconfig !== null) {
    write(
      'web/tsconfig.app.json',
      typeof tsconfig === 'string' ? tsconfig : JSON.stringify(tsconfig, null, 2),
    )
  }
  write('web/src/jsx-shim/jsx-runtime.d.ts', JSX_SHIM)
  if (rbac !== null) write('web/src/lib/auth/rbac.ts', rbac)
  write('web/src/lib/auth/context.tsx', context)
  for (const [rel, body] of Object.entries(files)) write(rel, body)

  // The stub engine — a real Go program, invoked exactly as the production one is, so
  // the guard's Go half runs through its real code path.
  if (!noGoModule) write('cmd/olivares/go.mod', 'module olivares-selftest-stub\n\ngo 1.26.5\n')
  write(
    'cmd/olivares/tools/permsdump/main.go',
    brokenEngine
      ? `package main

import "os"

func main() { os.Stderr.WriteString("permsdump: simulated failure\\n"); os.Exit(3) }
`
      : `package main

import "fmt"

func main() {
	fmt.Print(${JSON.stringify(badInventoryJson ? 'this is not json' : JSON.stringify(inventory))})
}
`,
  )
  return root
}

/** Run the guard against a throwaway repo; return {code, findings, stderr}. */
function run(root) {
  try {
    const out = execFileSync('node', [GUARD, '--json'], {
      encoding: 'utf8',
      env: { ...process.env, OLIVARES_CONSOLE_PERMS_REPO: root },
      maxBuffer: 64 * 1024 * 1024,
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    return { code: 0, ...JSON.parse(out), stderr: '' }
  } catch (e) {
    const stdout = e.stdout?.toString() ?? ''
    let parsed = null
    try {
      parsed = JSON.parse(stdout)
    } catch {
      /* exit 2 paths print no JSON */
    }
    return { code: e.status, findings: parsed?.findings ?? [], checked: parsed?.checked, stderr: e.stderr?.toString() ?? '' }
  }
}

/** The identity of a finding set: sorted "kind permission @ where" triples. */
const identity = (findings) =>
  findings
    .map((f) => `${f.kind} ${f.permission ?? '-'} @ ${f.where}`)
    .sort()
    .join('\n')

function check(name, root, expected, expectedCode = expected.length ? 1 : 0) {
  const r = run(root)
  const got = identity(r.findings)
  const want = expected.slice().sort().join('\n')
  const ok = got === want && r.code === expectedCode
  if (!ok) {
    failures++
    console.error(`\n✗ ${name}`)
    console.error(`  exit: got ${r.code}, want ${expectedCode}`)
    console.error(`  want findings:\n${want.replace(/^/gm, '      ') || '      (none)'}`)
    console.error(`  got  findings:\n${got.replace(/^/gm, '      ') || '      (none)'}`)
    if (r.stderr) console.error(`  stderr: ${r.stderr.slice(0, 500)}`)
  } else {
    console.log(`✓ ${name}`)
  }
  results.push({ name, ok })
  // CONSOLE_PERMS_SELFTEST_KEEP=1 leaves the throwaway consoles on disk so a failing
  // case can be inspected and re-run by hand instead of guessed at.
  if (!process.env.CONSOLE_PERMS_SELFTEST_KEEP) fs.rmSync(root, { recursive: true, force: true })
  else console.error(`    kept: ${root}`)
  return r
}

function checkExit(name, root, expectedCode, stderrMustMatch) {
  const r = run(root)
  const ok = r.code === expectedCode && (!stderrMustMatch || stderrMustMatch.test(r.stderr))
  if (!ok) {
    failures++
    console.error(`\n✗ ${name}`)
    console.error(`  exit: got ${r.code}, want ${expectedCode}`)
    console.error(`  stderr: ${r.stderr.slice(0, 600)}`)
  } else {
    console.log(`✓ ${name}`)
  }
  results.push({ name, ok })
  // CONSOLE_PERMS_SELFTEST_KEEP=1 leaves the throwaway consoles on disk so a failing
  // case can be inspected and re-run by hand instead of guessed at.
  if (!process.env.CONSOLE_PERMS_SELFTEST_KEEP) fs.rmSync(root, { recursive: true, force: true })
  else console.error(`    kept: ${root}`)
}

// EVERY fixture — including the ones that do not go through view() — carries a CONTROL
// call: `voice:policy:admin`, declared by the stub
// engine and answered identically by both sides in every fixture, so it never becomes a
// finding of its own — on the same line as the hook, so the body's
// line numbers are unaffected. Without it, disabling a detection clause did not turn a
// red fixture green — it made the file contain no resolvable can() call at all and hit
// the independent zero-call guard, so the case stayed red for a reason that had nothing
// to do with the clause under test. A mutation battery whose fixtures fail for the wrong
// reason cannot tell a load-bearing clause from a dead one.
const view = (body) => `import { useAuth } from '@/lib/auth/context'
export function View() {
  const { can } = useAuth(); const control = can('voice:policy:admin')
${body}
}
`

// ---------------------------------------------------------------------------
// 1. The NEGATIVE control comes first. If the guard cries on correct code, nothing
//    below it matters — a gate with false positives is a gate someone turns off.
//    session:read exists ONLY by concatenation in the engine; a check that reads
//    declarations textually reports it as invented.
// ---------------------------------------------------------------------------
check(
  'clean console: a concatenated core read, a module admin and a privileged read at the right tier',
  makeRepo({
    'web/src/features/ok.tsx': view(`  const a = can('session:read')
  const b = can('voice:policy:admin')
  return a && b`),
  }),
  [],
)

// ---------------------------------------------------------------------------
// 2. UNDECLARED — the console gates on a permission no form declares.
// ---------------------------------------------------------------------------
check(
  'undeclared: a permission the engine declares in no form',
  makeRepo({
    'web/src/features/bad.tsx': view(`  return can('phantom:read')`),
  }),
  ["undeclared phantom:read @ web/src/features/bad.tsx:4"],
)

// The same fixture with the permission DECLARED goes green: the clause is
// load-bearing, not a constant failure.
check(
  'undeclared clause is load-bearing: declaring the permission clears it',
  makeRepo(
    { 'web/src/features/bad.tsx': view(`  return can('phantom:read')`) },
    {
      inventory: {
        ...INVENTORY,
        declared: {
          ...INVENTORY.declared,
          // Grants that MATCH what the console answers for an unknown core-shaped
          // string: nobody. Declaring it is what clears the finding; inventing a
          // grant the console does not make would trade one finding for another.
          'phantom:read': { forms: ['core'], grants: { viewer: false, editor: false, admin: false, owner: false } },
        },
      },
    },
  ),
  [],
)

// ---------------------------------------------------------------------------
// 3. DIVERGENT — can() answered something OTHER than membership of the set the engine
//    handed it. Since that is the only way the two halves can disagree: the guard
//    drives the console with the engine's own effective set, so the inventory alone can
//    no longer stage a divergence. Both cases below are shapes this console has shipped.
// ---------------------------------------------------------------------------
check(
  'divergent: a verb tier regrown on top of the lookup opens a privileged read to viewers',
  makeRepo(
    // authz:read is the privileged class: editor and up, NEVER viewer. It ends in
    // ':read', so the regrown fallback hands it to every role — the original defect,
    // exactly.
    { 'web/src/features/priv.tsx': view(`  return can('authz:read')`) },
    { rbac: RBAC_FAIL_OPEN_READ },
  ),
  ['divergent authz:read @ web/src/features/priv.tsx:4'],
)

// The same mutant against a permission the fallback does NOT reach goes green: the
// clause is load-bearing and the case is not failing for some incidental reason.
check(
  'divergence clause is load-bearing: the same mutant is clean where the fallback cannot reach',
  makeRepo(
    { 'web/src/features/ok.tsx': view(`  return can('voice:policy:admin')`) },
    { rbac: RBAC_FAIL_OPEN_READ },
  ),
  [],
)

// And the other direction — the console being STRICTER than the engine is also a
// divergence, because it hides an action the principal is entitled to.
check(
  'divergent in the other direction: a hand-written special case hides what the engine grants',
  makeRepo(
    { 'web/src/features/strict.tsx': view(`  return can('system:admin')`) },
    {
      rbac: RBAC_SYSTEM_SPECIAL_CASE,
      inventory: {
        ...INVENTORY,
        declared: {
          ...INVENTORY.declared,
          // An engine that granted system:admin to owner would put it in owner's set;
          // a console that special-cases `system:` to false would hide it anyway.
          'system:admin': { forms: ['core'], grants: { viewer: false, editor: false, admin: false, owner: true } },
        },
      },
    },
  ),
  ['divergent system:admin @ web/src/features/strict.tsx:4'],
)

// ---------------------------------------------------------------------------
// 4. THE SINKS — a permission that reaches can() only through a registry entry or
//    a component prop must be caught, and the same-named field of an unrelated
//    object must NOT be. This pair is the whole false-positive control: both
//    objects are `{ permission: string }` and only one feeds can().
// ---------------------------------------------------------------------------
check(
  'sink: an undeclared permission reaching can() only through a nav-registry entry',
  makeRepo({
    'web/src/features/registry.tsx': `export interface FeatureView { id: string; permission?: string }
export const FEATURE_VIEWS: FeatureView[] = [
  { id: 'a', permission: 'session:read' },
  { id: 'b', permission: 'ghost:read' },
]
`,
    'web/src/components/nav.tsx': `import { useAuth } from '@/lib/auth/context'
import { FEATURE_VIEWS } from '@/features/registry'
export function Nav() {
  const { can } = useAuth()
  return FEATURE_VIEWS.filter((v) => !v.permission || can(v.permission)).length
}
`,
  }),
  ['undeclared ghost:read @ web/src/components/nav.tsx:5'],
)

check(
  'NOT a sink: an unrelated object with a `permission` field is left alone',
  makeRepo({
    'web/src/features/registry.tsx': `export interface FeatureView { id: string; permission?: string }
export const FEATURE_VIEWS: FeatureView[] = [{ id: 'a', permission: 'session:read' }]
`,
    // A policy-authoring form: same field name, same type, never reaches can().
    'web/src/features/policy-form.tsx': `export interface PolicyRule { permission: string }
export const DRAFT: PolicyRule = { permission: 'totally:made:up' }
`,
    'web/src/components/nav.tsx': `import { useAuth } from '@/lib/auth/context'
import { FEATURE_VIEWS } from '@/features/registry'
export function Nav() {
  const { can } = useAuth()
  return FEATURE_VIEWS.filter((v) => !v.permission || can(v.permission)).length
}
`,
  }),
  [],
)

check(
  'sink: a component prop written by a JSX attribute',
  makeRepo({
    'web/src/components/gate.tsx': `import { useAuth } from '@/lib/auth/context'
export function Gate({ permission }: { permission: string }) {
  const { can } = useAuth()
  return can(permission)
}
`,
    'web/src/features/use-gate.tsx': `import { Gate } from '@/components/gate'
export function Uses() {
  return <><Gate permission="session:read" /><Gate permission="spectre:read" /></>
}
`,
  }),
  ['undeclared spectre:read @ web/src/components/gate.tsx:4'],
)

// A sink NOTHING writes must be unreadable, never an empty permission set. This is
// the regression case for a fail-open that shipped in the first draft of the guard:
// writesTo() returning [] contributed no permissions AND no finding, so both
// prop-fed surfaces in the real console (RequirePermission, RedactionToggle)
// dropped out of the check without a word. "I found no writes" and "there are no
// permissions here" must never be the same answer.
check(
  'fail-open control: a prop-fed sink nothing writes is unreadable, not an empty set',
  makeRepo({
    'web/src/components/orphan.tsx': `import { useAuth } from '@/lib/auth/context'
export function Orphan({ permission }: { permission: string }) {
  const { can } = useAuth(); const control = can('voice:policy:admin')
  return can(permission) && control
}
`,
  }),
  ['unreadable - @ web/src/components/orphan.tsx:4'],
)

// ---------------------------------------------------------------------------
// 4b. THE SHAPES THAT CARRY A VALUE WITHOUT NAMING IT. A closing review found four
//     that used to disappear in silence — no value AND no finding — because the sink
//     had other explicit writes, so it never looked unread. Each is now either
//     resolved or reported. These four cases exist so the gap cannot reopen: every
//     one puts a reachable `ghost:read` where the old extraction saw nothing.
// ---------------------------------------------------------------------------
const REGISTRY = `export interface FeatureView { id: string; permission?: string }
export const EXTRA = { id: 'x', permission: 'ghost:read' }
export const PERM = 'ghost:read'
export const FEATURE_VIEWS: FeatureView[] = [
  { id: 'a', permission: 'session:read' },
]
`
const NAV = `import { useAuth } from '@/lib/auth/context'
import { FEATURE_VIEWS } from '@/features/registry'
export function Nav() {
  const { can } = useAuth()
  return FEATURE_VIEWS.filter((v) => !v.permission || can(v.permission)).length
}
`

check(
  'shape: an object SPREAD into a registry entry is unreadable, not invisible',
  makeRepo({
    'web/src/features/registry.tsx': REGISTRY.replace(
      "  { id: 'a', permission: 'session:read' },",
      "  { id: 'a', permission: 'session:read' },\n  { ...EXTRA },",
    ),
    'web/src/components/nav.tsx': NAV,
  }),
  ['unreadable - @ web/src/components/nav.tsx:5'],
)

check(
  'shape: a SHORTHAND registry property resolves to the binding it names',
  makeRepo({
    // `{ id, permission }` — the property is written WITHOUT naming a value, so the
    // extractor has to follow the binding in scope. The first version of this case
    // wrote `permission: PERM`, which is an ordinary property assignment: it was
    // named SHORTHAND and exercised the branch it was named after not at all.
    'web/src/features/registry.tsx': REGISTRY.replace(
      "  { id: 'a', permission: 'session:read' },",
      "  { id: 'a', permission: 'session:read' },\n  { id, permission },",
    ).replace(
      "export const PERM = 'ghost:read'",
      "const id = 'b'\nconst permission = 'ghost:read'\nexport const PERM = permission",
    ),
    'web/src/components/nav.tsx': NAV,
  }),
  ['undeclared ghost:read @ web/src/components/nav.tsx:5'],
)

check(
  'shape: a JSX SPREAD onto a gate component is unreadable, not invisible',
  makeRepo({
    'web/src/components/gate.tsx': `import { useAuth } from '@/lib/auth/context'
export function Gate({ permission }: { permission: string }) {
  const { can } = useAuth()
  return can(permission)
}
`,
    'web/src/features/use-gate.tsx': `import { Gate } from '@/components/gate'
const props = { permission: 'ghost:read' }
export function Uses() {
  return <><Gate permission="session:read" /><Gate {...props} /></>
}
`,
  }),
  ['unreadable - @ web/src/components/gate.tsx:4'],
)

check(
  'shape: an ALIASED can is still the console\'s can',
  makeRepo({
    'web/src/features/alias.tsx': `import { useAuth } from '@/lib/auth/context'
export function View() {
  const auth = useAuth()
  const control = auth.can('voice:policy:admin')
  const allowed = auth.can
  return allowed('ghost:read') && control
}
`,
  }),
  ['undeclared ghost:read @ web/src/features/alias.tsx:6'],
)

// The mirror's own files are NOT a blanket exception. The forwarding inside can()'s
// own definition is excused; a real gate written in the same file is not. Two earlier
// versions of this exception — skip the whole file, then skip any parameter-shaped
// argument — both went silent on exactly this fixture.
check(
  'the auth files are not a blanket exception: a real gate there is still checked',
  makeRepo({
    'web/src/lib/auth/context.tsx':
      CONTEXT_TSX +
      `
export function InlineGate({ permission }: { permission: string }) {
  const { can } = useAuth()
  return can(permission)
}
export function InlineLiteralGate() {
  const { can } = useAuth()
  return can('ghost:read')
}
`,
    'web/src/features/uses.tsx': `import { InlineGate } from '@/lib/auth/context'
export function Uses() {
  return <InlineGate permission="session:read" />
}
`,
  }),
  ['undeclared ghost:read @ web/src/lib/auth/context.tsx:16'],
)

// ---------------------------------------------------------------------------
// 5. UNREADABLE — the argument the guard cannot resolve. It must FAIL, not skip.
//    This is the clause that decides whether "I could not read it" is treated as
//    "it is fine", which is the failure mode the whole guard exists to prevent.
// ---------------------------------------------------------------------------
check(
  'unreadable: a can() argument that cannot be resolved is a finding, not a skip',
  makeRepo({
    'web/src/features/dyn.tsx': `import { useAuth } from '@/lib/auth/context'
export function View({ kind }: { kind: string }) {
  const { can } = useAuth(); const control = can('voice:policy:admin')
  return can(\`\${kind}:read\`) && control
}
`,
  }),
  ['unreadable - @ web/src/features/dyn.tsx:4'],
)

check(
  'unreadable: a call to an unrelated helper named can() is reported, never assumed harmless',
  makeRepo({
    'web/src/features/other.tsx': `import { useAuth } from '@/lib/auth/context'
function can(x: string): boolean { return x.length > 0 }
export function View() {
  const auth = useAuth(); const control = auth.can('voice:policy:admin')
  return can('anything') && control
}
`,
  }),
  ['unreadable - @ web/src/features/other.tsx:5'],
)

// ---------------------------------------------------------------------------
// 6. FAIL-CLOSED — the two ways the check can fail to run at all. Neither may
//    report the console clean. "I could not look" is not "it is clean".
// ---------------------------------------------------------------------------
// ---------------------------------------------------------------------------
// 6b. THE ENVIRONMENT GUARDS. Each of these refuses to report a clean console when
//     the check could not actually look, and every one of them is a `die` that a
//     sign-off measured as having NO case: the fixture factory always supplied a
//     compiler, a tsconfig, the real rbac.ts and a Go module, so the guards could not
//     be reached. A fail-closed arm nobody has watched refuse is a claim, not a gate.
// ---------------------------------------------------------------------------
const OK_VIEW = { 'web/src/features/ok.tsx': view(`  return can('session:read')`) }

checkExit(
  'fail-closed: no TypeScript compiler is exit 2, not a pass',
  makeRepo(OK_VIEW, { noTypescript: true }),
  2,
  /cannot find the typescript compiler/,
)

checkExit(
  'fail-closed: no Go module is exit 2 — the engine half cannot run',
  makeRepo(OK_VIEW, { noGoModule: true }),
  2,
  /no Go module at/,
)

checkExit(
  'fail-closed: an inventory that is not JSON is exit 2',
  makeRepo(OK_VIEW, { badInventoryJson: true }),
  2,
  /did not emit JSON/,
)

checkExit(
  'fail-closed: an inventory whose schema this guard does not know is exit 2',
  makeRepo(OK_VIEW, {
    inventory: { ...INVENTORY, schema: 'something.else/9' },
  }),
  2,
  /schema/,
)

checkExit(
  'fail-closed: no tsconfig is exit 2',
  makeRepo(OK_VIEW, { tsconfig: null }),
  2,
  /has no tsconfig at/,
)

checkExit(
  'fail-closed: a tsconfig that will not parse is exit 2',
  makeRepo(OK_VIEW, { tsconfig: '{ this is not json' }),
  2,
  /will not parse/,
)

checkExit(
  'fail-closed: a tsconfig matching zero files is exit 2, not an empty clean run',
  makeRepo(OK_VIEW, { tsconfig: { ...TSCONFIG, include: ['does-not-exist'] } }),
  2,
  /matched no files/,
)

checkExit(
  'fail-closed: rbac.ts missing is exit 2 — the console half did not run at all',
  makeRepo(OK_VIEW, { rbac: null }),
  2,
  /could not import web\/src\/lib\/auth\/rbac\.ts/,
)

checkExit(
  'fail-closed: an rbac.ts that exports no can() is exit 2',
  makeRepo(OK_VIEW, { rbac: 'export const somethingElse = 1\n' }),
  2,
  /does not export can|could not import/,
)

checkExit(
  'fail-closed: no declaration of can() anywhere is exit 2, not a vacuous pass',
  makeRepo(
    { 'web/src/features/ok.tsx': view(`  return can('session:read')`) },
    {
      // can() exists at RUNTIME (an arrow const satisfies the import check) but is
      // declared as neither a function declaration nor a context member, so nothing
      // anchors the symbol match. Every call site would then fail to match and the
      // console would be reported clean having checked nothing.
      rbac: 'export const can = (_p: string, _c: unknown) => false\n',
      context: `import { can as rbacCan } from './rbac'
export function useAuth() {
  return { check: (p: string) => rbacCan(p, null) }
}
`,
    },
  ),
  2,
  /no declaration of can/,
)

// NOT COVERED, and the reason is worth more than a case would be: the guard's
// "rbac.ts is not in the program" refusal (check-console-perms.mjs) cannot be reached
// while the console uses can() at all. TypeScript pulls rbac.ts into the program
// TRANSITIVELY through the auth context, so narrowing tsconfig's include does not
// exclude it; and a console that imports the context nowhere has no can() calls, so
// the zero-call refusal fires first. It is a backstop against a future change in how
// the program is built, kept deliberately and declared uncovered rather than paired
// with a fixture that would pass for a different reason.

// `can()` with no argument at all: not an unresolvable permission, an absent one.
check(
  'a can() call with no argument is a finding, not a skip',
  makeRepo({
    'web/src/features/noarg.tsx': view(`  return can()`),
  }),
  ['unreadable - @ web/src/features/noarg.tsx:4'],
)

checkExit(
  'fail-closed: a console with no can() call sites is exit 2, not a pass',
  makeRepo({ 'web/src/features/empty.tsx': `export const X = 1\n` }),
  2,
  /no can\(\) call sites/,
)

checkExit(
  'fail-closed: an engine that will not run is exit 2, not a pass',
  makeRepo({ 'web/src/features/ok.tsx': view(`  return can('session:read')`) }, { brokenEngine: true }),
  2,
  /did not execute/,
)

checkExit(
  'fail-closed: an engine that declares nothing is exit 2, not a pass',
  makeRepo(
    { 'web/src/features/ok.tsx': view(`  return can('session:read')`) },
    { inventory: { ...INVENTORY, declared: {} } },
  ),
  2,
  /declared no permissions/,
)

// ---------------------------------------------------------------------------
// 7. IDENTITY, not cardinals: two findings of the same kind must be distinguished
//    by WHERE and WHICH. A clause that moved one detection to another fixture
//    while keeping the count is what this asserts against.
// ---------------------------------------------------------------------------
check(
  'two distinct findings are reported by identity, not counted',
  makeRepo(
    {
      // `ghost:write` and not `ghost:read`: under the fail-open mutant a ':read' string
      // is answered TRUE, which would make this fixture BOTH undeclared and (arguably)
      // divergent — and the identity assertion is worth nothing if a case can satisfy it
      // by accident from the other clause.
      'web/src/features/one.tsx': view(`  return can('ghost:write')`),
      'web/src/features/two.tsx': view(`  return can('authz:read')`),
    },
    { rbac: RBAC_FAIL_OPEN_READ },
  ),
  [
    'undeclared ghost:write @ web/src/features/one.tsx:4',
    'divergent authz:read @ web/src/features/two.tsx:4',
  ],
)

// ---------------------------------------------------------------------------
console.log(
  `\ncheck-console-perms self-test: ${results.length - failures}/${results.length} cases pass ` +
    `(${results.filter((r) => r.name.startsWith('fail-closed') || r.name.includes('undeclared') || r.name.includes('divergent') || r.name.includes('unreadable') || r.name.includes('sink')).length} red-by-construction).`,
)
if (failures) {
  console.error(`${failures} self-test case(s) FAILED — the guard does not behave as documented.`)
  process.exit(1)
}
