// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import assert from 'node:assert/strict'
import {
  mkdtempSync,
  readFileSync,
  mkdirSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { spawnSync } from 'node:child_process'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

const script = fileURLToPath(new URL('./console-dump.mjs', import.meta.url))
const repo = path.resolve(path.dirname(script), '../..')

function fixture(t) {
  const root = mkdtempSync(path.join(tmpdir(), 'console-spread-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  mkdirSync(path.join(root, 'web/src/features'), { recursive: true })
  mkdirSync(path.join(root, 'web/src/app'), { recursive: true })
  symlinkSync(
    path.join(repo, 'web/node_modules'),
    path.join(root, 'web/node_modules'),
  )
  const entries = Array.from(
    { length: 26 },
    (_, i) =>
      `{ id: 'view${i}', path: '/view${i}', navigation: { kind: 'feature', areaId: 'ai', sectionId: 'x' }, permission: 'view:read' }`,
  )
  const source = `import { FEATURE_EXTENSIONS } from './extensions'
export const NAV_AREAS = [AREA_LIST]
export const FEATURE_VIEWS = [
${entries.join(',\n')},
SPREAD
]
`
  const write = (rel, body) => writeFileSync(path.join(root, rel), body)
  write(
    'web/src/app/routes.tsx',
    "const login = createRoute({path: '/login', getParentRoute: () => rootRoute})\n",
  )
  write(
    'web/src/features/route-census.json',
    JSON.stringify({
      paths: ['/login', ...entries.map((_, i) => `/view${i}`)],
    }),
  )
  return {
    root,
    write,
    registry: (
      spread,
      areas = "{ id: 'security-identity', path: '/areas/security-identity' }, { id: 'ai', path: '/areas/ai' }",
    ) =>
      write(
        'web/src/features/registry.tsx',
        source.replace('SPREAD', spread).replace('AREA_LIST', areas),
      ),
    dump: () =>
      spawnSync(process.execPath, [script, '--root', root], {
        encoding: 'utf8',
      }),
  }
}

test('a frozen named-import array preserves the empty roster and includes literal entries', (t) => {
  const f = fixture(t)
  f.write(
    'web/src/features/extensions.ts',
    'export const FEATURE_EXTENSIONS = Object.freeze([])\n',
  )
  f.registry('')
  const baseline = f.dump()
  assert.equal(baseline.status, 0, baseline.stderr)
  f.registry('...FEATURE_EXTENSIONS,')
  const empty = f.dump()
  assert.equal(empty.status, 0, empty.stderr)
  assert.deepEqual(JSON.parse(empty.stdout), JSON.parse(baseline.stdout))

  f.write(
    'web/src/features/extensions.ts',
    `export const FEATURE_EXTENSIONS = Object.freeze([
    { id: 'extension', path: '/extension', hub: 'operate', navigation: Object.freeze({ kind: 'feature', areaId: 'security-identity', sectionId: 'access' }), permission: 'extension:read', helpHref: '/reference/extension' }
  ])\n`,
  )
  const populated = f.dump()
  assert.equal(populated.status, 0, populated.stderr)
  // The guide's section order is the registry's, not an alphabetical one.
  assert.deepEqual(JSON.parse(populated.stdout).areaOrder, ['security-identity', 'ai'])
  const view = JSON.parse(populated.stdout).views.find(
    (v) => v.id === 'extension',
  )
  assert.deepEqual(view, {
    id: 'extension',
    path: '/extension',
    // An edition entry may still declare the retired hub; the place is its navigation.
    area: 'security-identity',
    permission: 'extension:read',
    helpHref: '/reference/extension',
    hideInNav: false,
    where: 'web/src/features/extensions.ts:2',
  })
})

test('unknown spreads and unreadable imported arrays fail without a partial roster', (t) => {
  const f = fixture(t)
  for (const [spread, declaration] of [
    ['...unknown,', 'export const FEATURE_EXTENSIONS = Object.freeze([])'],
    ['...FEATURE_EXTENSIONS,', 'export const OTHER = Object.freeze([])'],
    [
      '...FEATURE_EXTENSIONS,',
      'export const FEATURE_EXTENSIONS = buildViews()',
    ],
    [
      '...FEATURE_EXTENSIONS,',
      'export const FEATURE_EXTENSIONS = Object.freeze(buildViews())',
    ],
    [
      '...FEATURE_EXTENSIONS,',
      'export const FEATURE_EXTENSIONS = Object.freeze([broken])',
    ],
    [
      '...FEATURE_EXTENSIONS,',
      "export const FEATURE_EXTENSIONS = Object.freeze([{ id: 'broken' }])",
    ],
    [
      '...FEATURE_EXTENSIONS,',
      "export const FEATURE_EXTENSIONS = Object.freeze([{ id: 'broken', path: '/broken', permission: computePermission() }])",
    ],
  ]) {
    f.registry(spread)
    f.write('web/src/features/extensions.ts', `${declaration}\n`)
    const result = f.dump()
    assert.equal(result.status, 2, `${declaration}: ${result.stderr}`)
    assert.match(result.stderr, /CANNOT LOOK/)
    assert.equal(
      result.stdout,
      '',
      'a refused input must not emit a partial roster',
    )
  }
})

test('views collected from each directory by import.meta.glob are read from those files', (t) => {
  const f = fixture(t)
  f.write(
    'web/src/features/extensions.ts',
    'export const FEATURE_EXTENSIONS = Object.freeze([])\n',
  )
  f.registry('')
  const baseline = JSON.parse(f.dump().stdout)
  const registry = (glob) =>
    f.write(
      'web/src/features/registry.tsx',
      `import { FEATURE_EXTENSIONS } from './extensions'
export const NAV_AREAS = [{ id: 'ai', path: '/areas/ai' }]
const BUILTIN = Object.values(${glob}).flat()
export const FEATURE_VIEWS = Object.freeze([...BUILTIN, ...FEATURE_EXTENSIONS].map((view) => Object.freeze(view)))
`,
    )
  const views = (dir, body) => {
    mkdirSync(path.join(f.root, 'web/src/features', dir), { recursive: true })
    f.write(`web/src/features/${dir}/views.tsx`, body)
  }
  const half = (from, to) =>
    Array.from(
      { length: to - from },
      (_, i) =>
        `{ id: 'view${from + i}', path: '/view${from + i}', navigation: { kind: 'feature', areaId: 'ai', sectionId: 'x' }, permission: 'view:read' }`,
    ).join(',\n')
  views('alpha', `export const VIEWS = [\n${half(0, 13)}\n] satisfies X[]\n`)
  views('beta', `export const VIEWS = [\n${half(13, 26)}\n]\n`)
  const glob = "import.meta.glob('./*/views.tsx', { eager: true, import: 'VIEWS' })"
  registry(glob)
  const collected = f.dump()
  assert.equal(collected.status, 0, collected.stderr)
  const out = JSON.parse(collected.stdout)
  assert.deepEqual(
    out.views.map(({ where, ...v }) => v),
    baseline.views.map(({ where, ...v }) => v),
  )
  assert.equal(
    out.views.find((v) => v.id === 'view20').where,
    'web/src/features/beta/views.tsx:9',
  )

  for (const [name, mutate] of [
    ['a directory file without the export', () => views('gamma', 'export const OTHER = []\n')],
    ['a computed export', () => views('gamma', 'export const VIEWS = build()\n')],
    ['a lazy glob', () => registry(glob.replace('eager: true', 'eager: false'))],
  ]) {
    mutate()
    const result = f.dump()
    assert.equal(result.status, 2, `${name}: ${result.stderr}`)
    assert.match(result.stderr, /CANNOT LOOK/)
    assert.equal(result.stdout, '', `${name}: no partial roster`)
    rmSync(path.join(f.root, 'web/src/features/gamma'), { recursive: true, force: true })
    registry(glob)
  }
})

test('area directories are read from NAV_AREAS as authenticated routes', (t) => {
  const f = fixture(t)
  f.registry('', "{ id: 'ai', path: '/areas/ai', sections: [] }")
  const dumped = f.dump()
  assert.equal(dumped.status, 0, dumped.stderr)
  const area = JSON.parse(dumped.stdout).standalone.find((m) => m.path === '/areas/ai')
  assert.deepEqual(
    { ...area, where: undefined },
    { path: '/areas/ai', parent: 'appRoute', authenticated: true, where: undefined },
  )

  f.registry('', "{ id: 'ai', path: AI_PATH, sections: [] }")
  const computed = f.dump()
  assert.equal(computed.status, 2, computed.stderr)
  assert.match(computed.stderr, /NAV_AREAS entry .* has no literal path/)
  assert.equal(computed.stdout, '', 'no partial roster')

  f.registry('', "{ path: '/areas/ai', sections: [] }")
  const unnamed = f.dump()
  assert.equal(unnamed.status, 2, unnamed.stderr)
  assert.match(unnamed.stderr, /NAV_AREAS holds an area without a literal id/)
  assert.equal(unnamed.stdout, '', 'no partial roster')

  f.write(
    'web/src/features/registry.tsx',
    readFileSync(path.join(f.root, 'web/src/features/registry.tsx'), 'utf8').replace(
      /export const NAV_AREAS = .*\n/,
      '',
    ),
  )
  const absent = f.dump()
  assert.equal(absent.status, 2, absent.stderr)
  assert.match(absent.stderr, /declares no NAV_AREAS/)
  assert.equal(absent.stdout, '', 'no partial roster')
})
