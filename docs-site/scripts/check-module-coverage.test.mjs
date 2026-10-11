// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { test } from 'node:test'

const script = await readFile(new URL('./check-module-coverage.mjs', import.meta.url), 'utf8')
const repo = fileURLToPath(new URL('../../', import.meta.url))
const names = ['identity', 'claude-policy', 'claude-agents', 'skills']
const extras = ['vi-governance', 'session-cockpit', 'xix-api-manage-as-code',
  'xx-multi-tenancy', 'xxi-executive-dashboards', 'xxiii-fine-tuning']

async function check(t, { missingPage, missingLink, count = names.length, extraSpec = [] } = {}) {
  // Keep scratch on the same filesystem as the checkout, rather than shared /tmp.
  const root = await mkdtemp(join(repo, '.module-coverage-test-'))
  t.after(() => rm(root, { recursive: true, force: true }))
  const put = async (path, content) => {
    const target = join(root, path)
    await mkdir(dirname(target), { recursive: true })
    await writeFile(target, content)
  }
  await put('docs-site/scripts/check-module-coverage.mjs', script)
  await put('core/modulespec/modules.json', JSON.stringify([
    ...names.map(namespace => ({ namespace, selectable: true,
      package: namespace === 'skills' ? 'skills' : 'governance' })),
    ...extraSpec,
  ]))
  // A package directory is not the selectable roster. This directory used to
  // hide the three separately selectable governance consoles from the guard.
  await mkdir(join(root, 'modules/governance'), { recursive: true })
  const slugs = [...names, ...extras]
  for (const slug of slugs.filter(slug => slug !== missingPage)) {
    await put(`docs-site/src/content/docs/reference/modules/${slug}.md`, `---\ntitle: ${slug}\n---\n`)
  }
  await put('docs-site/src/content/docs/reference/modules/overview.md',
    `# The ${count} modules\n\n` + slugs.filter(slug => slug !== missingLink)
      .map(slug => `| [${slug}](/reference/modules/${slug}/) | — | Purpose |`).join('\n'))
  return spawnSync(process.execPath, [join(root, 'docs-site/scripts/check-module-coverage.mjs')],
    { encoding: 'utf8', timeout: 10000 })
}

test('counts selectable namespaces, including modules sharing a package', async t => {
  const result = await check(t)
  assert.equal(result.status, 0, result.stderr)
  assert.match(result.stdout, /4 selectable modules/)
})

for (const name of names) {
  test(`rejects missing ${name} reference page`, async t => {
    const result = await check(t, { missingPage: name })
    assert.equal(result.status, 1, result.stdout)
    assert.match(result.stderr, new RegExp(`module "${name}".*missing`))
  })
  test(`rejects missing ${name} catalog link`, async t => {
    const result = await check(t, { missingLink: name })
    assert.equal(result.status, 1, result.stdout)
    assert.match(result.stderr, new RegExp(`${name}.*not linked`))
  })
}

test('rejects a newly selectable namespace without a page', async t => {
  const result = await check(t, { count: 5, extraSpec: [
    { namespace: 'new-module', package: 'governance', selectable: true },
  ] })
  assert.equal(result.status, 1, result.stdout)
  assert.match(result.stderr, /module "new-module".*missing/)
})

test('excludes non-selectable descriptors from the selectable count', async t => {
  const result = await check(t, { extraSpec: [
    { namespace: 'session-cockpit', package: 'sessioncockpit', selectable: false },
  ] })
  assert.equal(result.status, 0, result.stderr)
  assert.match(result.stdout, /4 selectable modules/)
})

test('preserves coverage of supplementary capability pages', async t => {
  const result = await check(t, { missingPage: 'xxiii-fine-tuning' })
  assert.equal(result.status, 1, result.stdout)
  assert.match(result.stderr, /catalog page.*xxiii-fine-tuning.*missing/)
})

test('rejects an outdated selectable count in the overview', async t => {
  const result = await check(t, { count: 3 })
  assert.equal(result.status, 1, result.stdout)
  assert.match(result.stderr, /catalog count.*4 selectable modules/)
})
