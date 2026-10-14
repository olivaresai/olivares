// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { PUBLISHED_LOCALES } from '../../docs-site/src/site-locales.mjs'

const script = fileURLToPath(new URL('./console-locales.mjs', import.meta.url))
const begin = '<!-- BEGIN GENERATED olivares-console-routes -->'
const end = '<!-- END GENERATED olivares-console-routes -->'
const page = `${begin}\n**2 routes**\n| Screen | Path | Description |\n|---|---|---|\n| Home | \`/\` | Home |\n| Budgets | \`/stored-budgets\` | Stored limits |\n${end}\n`

function fixture(t) {
  const root = mkdtempSync(path.join(tmpdir(), 'console-locales-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  function write(rel, content) {
    mkdirSync(path.dirname(path.join(root, rel)), { recursive: true })
    writeFileSync(path.join(root, rel), content)
  }
  write('docs-site/src/site-locales.mjs', `export const PUBLISHED_LOCALES = ${JSON.stringify(PUBLISHED_LOCALES)}`)
  const rel = (locale) => `docs-site/src/content/docs/${locale ? `${locale}/` : ''}reference/console.md`
  for (const locale of ['', ...PUBLISHED_LOCALES]) write(rel(locale), page)
  return {
    mutate: (locale, content) => write(rel(locale), content),
    remove: (locale) => rmSync(path.join(root, rel(locale))),
    run: () => spawnSync(process.execPath, [script, root], { encoding: 'utf8' }),
  }
}

test('matching rosters allow translated labels and deep-link annotations', (t) => {
  const f = fixture(t)
  f.mutate('ja', page.replace('Home', 'ホーム \\| Home').replace('`/stored-budgets`', '`/stored-budgets` (リンクのみ)'))
  const result = f.run()
  assert.equal(result.status, 0, result.stderr)
})

for (const locale of PUBLISHED_LOCALES) {
  for (const [name, change, needle] of [
    ['route replacement with unchanged count', (s) => s.replace('/stored-budgets', '/finops'), 'missing /stored-budgets'],
    ['description route cannot replace Path', (s) => s.replace('| Budgets | `/stored-budgets` | Stored limits |', '| Budgets | unavailable | `/stored-budgets` |'), 'missing /stored-budgets'],
    ['stale count', (s) => s.replace('**2 routes**', '**83 routes**'), 'claims 83 routes'],
    ['duplicate row', (s) => s.replace(end, '| Home | `/` | Home |\n' + end), 'duplicate route rows'],
    ['missing markers', (s) => s.replace(begin, ''), 'CANNOT LOOK'],
    ['reversed markers', (s) => s.replace(begin, 'SWAP').replace(end, begin).replace('SWAP', end), 'CANNOT LOOK'],
  ]) {
    test(`${locale}: rejects ${name}`, (t) => {
      const f = fixture(t)
      f.mutate(locale, change(page))
      const result = f.run()
      assert.equal(result.status, needle === 'CANNOT LOOK' ? 2 : 1, result.stderr)
      assert.ok(result.stderr.includes(needle), result.stderr)
    })
  }
  test(`${locale}: missing reference fails closed`, (t) => {
    const f = fixture(t)
    f.remove(locale)
    const result = f.run()
    assert.equal(result.status, 2, result.stderr)
  })
}
