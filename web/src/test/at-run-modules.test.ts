// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// `pnpm at:gate` runs e2e-visual/at-run.ts with plain `node --experimental-strip-types`,
// whose ESM resolver wants the `.ts` extension on every relative runtime import. Vite,
// Vitest and Playwright's loader resolve extensionless specifiers, so only Node itself
// fails (ERR_MODULE_NOT_FOUND), five minutes into the a11y job. This parses at-run.ts
// under Node and loads every relative module it names, and what those import, so the
// PR fails instead. at-run.ts itself starts a browser on load, hence --check for it.
// Any relative specifier is loaded as written (type-only ones too: at-run.ts keeps `.ts`
// on them, as tsconfig.e2e.json documents), so a missing extension fails here.
import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, it } from 'vitest'

const webDir = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const atRun = resolve(webDir, 'e2e-visual/at-run.ts')
const nodeStripTypes = ['--experimental-strip-types', '--no-warnings']

function node(...args: string[]): void {
  execFileSync(process.execPath, [...nodeStripTypes, ...args], {
    cwd: webDir,
    encoding: 'utf8',
    stdio: 'pipe',
    timeout: 20_000,
  })
}

it('loads at-run.ts and every module it imports under node --experimental-strip-types', () => {
  const relative = [
    ...readFileSync(atRun, 'utf8').matchAll(
      /from\s+['"](\.{1,2}\/[^'"]+)['"]/g,
    ),
  ].map((m) => resolve(dirname(atRun), m[1]))
  expect(relative.length).toBeGreaterThan(0)

  node('--check', atRun)
  node(
    '--input-type=module',
    '-e',
    'await Promise.all(process.argv.slice(1).map((f) => import(f)))',
    ...relative,
  )
})
