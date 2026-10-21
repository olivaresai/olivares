// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// test:core-paths — the build-time core-path count (#2494): counted from the contract, refuses a
// contract that has no paths, and replaces only the token in text nodes.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { CORE_PATHS_TOKEN, corePathsPlugin, countCorePaths } from '../src/core-paths-plugin.mjs'

function withContract(body, run) {
  const dir = mkdtempSync(join(tmpdir(), 'core-paths-'))
  const file = join(dir, 'openapi.json')
  try {
    writeFileSync(file, JSON.stringify(body))
    return run(file)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
}

function replaced(contract, value) {
  const calls = []
  corePathsPlugin(contract).text({ type: 'text', value }, { replaceNode: (node, next) => calls.push(next) })
  return calls
}

test('counts the keys under paths', () => {
  withContract({ paths: { '/a': {}, '/b': {}, '/c': {} } }, (file) => assert.equal(countCorePaths(file), 3))
})

test('a contract without paths is a vanished measurement, never a count of zero', () => {
  for (const body of [{ paths: {} }, {}, { paths: null }, { paths: ['/a', '/b'] }]) {
    withContract(body, (file) => assert.throws(() => countCorePaths(file), /declares no paths/))
  }
})

test('the token becomes the count, in every position of a text node', () => {
  withContract({ paths: { '/a': {}, '/b': {} } }, (file) => {
    const calls = replaced(file, `${CORE_PATHS_TOKEN} core paths, not part of the ${CORE_PATHS_TOKEN}-path core`)
    assert.deepEqual(calls, [{ type: 'text', value: '2 core paths, not part of the 2-path core' }])
  })
})

test('an unreadable contract is named in the error', () => {
  assert.throws(() => countCorePaths('/nonexistent/openapi.json'), /cannot read \/nonexistent\/openapi\.json/)
})

test('the plugin name carries the count, so Astro drops its content cache when the count changes', () => {
  withContract({ paths: { '/a': {} } }, (one) => {
    withContract({ paths: { '/a': {}, '/b': {} } }, (two) => {
      assert.notEqual(corePathsPlugin(one).name, corePathsPlugin(two).name)
    })
  })
})

test('the default contract is the document the REST reference is rendered from', () => {
  const spec = new URL('../../web/openapi/openapi.json', import.meta.url)
  assert.equal(countCorePaths(), Object.keys(JSON.parse(readFileSync(spec, 'utf8')).paths).length)
})

test('text without the token is left alone', () => {
  withContract({ paths: { '/a': {} } }, (file) => assert.deepEqual(replaced(file, '132 core paths'), []))
})
