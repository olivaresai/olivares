// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The tokens build (tokens/build.mjs) runs style-dictionary, whose deepmerge is the deepmerge 4.3.1
// source inlined in @bundled-es-modules/deepmerge (GHSA-gcqx-w48m-w2h4: an own "__proto__" key, as
// JSON.parse makes it, sets the prototype of the merged object). web/patches/ skips that key; this
// test imports the exact module the build runs and fails if the patch stops applying.
/// <reference types="node" />
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { expect, it } from 'vitest'

type Merge = (target: object, source: object) => Record<string, unknown>

it('style-dictionary deepmerge ignores an own __proto__ key in target and source', async () => {
  // vitest runs with cwd = web/; the file is outside the package's exports map.
  const url = pathToFileURL(
    resolve('node_modules/style-dictionary/lib/utils/deepmerge.js'),
  ).href
  const { deepmerge } = (await import(/* @vite-ignore */ url)) as {
    deepmerge: Merge
  }

  const proto = '{"__proto__":{"polluted":1},"a":1}'
  for (const result of [
    deepmerge(JSON.parse(proto), { b: 2 }),
    deepmerge(JSON.parse(proto), JSON.parse(proto)),
  ]) {
    expect(Object.getPrototypeOf(result)).toBe(Object.prototype)
    expect(result.polluted).toBeUndefined()
  }
  expect(deepmerge({ a: { x: 1 } }, { a: { y: 2 } })).toEqual({
    a: { x: 1, y: 2 },
  })
})
