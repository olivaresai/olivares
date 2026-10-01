// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import assert from 'node:assert/strict'
import {
  mkdtempSync,
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
      `{ id: 'view${i}', path: '/view${i}', hub: 'operate', permission: 'view:read' }`,
  )
  const source = `import { FEATURE_EXTENSIONS } from './extensions'
export const HUB_ORDER = ['operate']
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
    write,
    registry: (spread) =>
      write('web/src/features/registry.tsx', source.replace('SPREAD', spread)),
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
    { id: 'extension', path: '/extension', hub: 'operate', permission: 'extension:read', helpHref: '/reference/extension' }
  ])\n`,
  )
  const populated = f.dump()
  assert.equal(populated.status, 0, populated.stderr)
  const view = JSON.parse(populated.stdout).views.find(
    (v) => v.id === 'extension',
  )
  assert.deepEqual(view, {
    id: 'extension',
    path: '/extension',
    hub: 'operate',
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
