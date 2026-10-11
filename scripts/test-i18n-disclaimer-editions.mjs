#!/usr/bin/env node
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const scanner = process.argv[2] ?? fileURLToPath(new URL('./check-i18n-disclaimers.mjs', import.meta.url))
const scratch = fs.mkdtempSync(path.join(os.tmpdir(), 'i18n-disclaimer-editions-'))
const canonical = 'This notice is not a certification and is not legal advice.'

function write(root, relative, text) {
  const file = path.join(root, relative)
  fs.mkdirSync(path.dirname(file), { recursive: true })
  fs.writeFileSync(file, text)
}

function run(root) {
  const result = spawnSync(process.execPath, [path.join(root, 'scripts/check-i18n-disclaimers.mjs')], {
    encoding: 'utf8',
    timeout: 10_000,
  })
  assert.equal(result.error, undefined)
  assert.equal(result.signal, null)
  return result
}

try {
  for (const [edition, baseline] of [['Community', 14], ['Business', 44]]) {
    const root = path.join(scratch, edition)
    write(root, 'scripts/check-i18n-disclaimers.mjs', fs.readFileSync(scanner, 'utf8'))
    write(root, 'web/src/features/_intel/disclaimers.ts',
      `export const KNOWN_DISCLAIMERS = {\n  '${canonical}': 'report',\n}\n`)
    for (const locale of ['en', 'es', 'de', 'fr', 'ja', 'ru', 'zh']) {
      write(root, `web/src/features/_intel/i18n/${locale}.json`, JSON.stringify({
        disclaimers: { report: locale === 'en' ? canonical : `[${locale}] ${canonical}` },
      }))
    }
    // The private compliance implementation is additive in the official assembly.
    if (edition === 'Business') write(root, 'modules/compliance/frameworks.go', 'package compliance\n')
    write(root, 'modules/probe/canonical.go', `package probe\nconst reportDisclaimer = "${canonical}"\n`)
    const residue = Array.from({ length: baseline }, (_, i) =>
      `const notice${i}Disclaimer = "Untranslated legal notice number ${i}, for testing the edition ratchet."`)
    const residueFile = 'modules/probe/residue.go'
    write(root, residueFile, `package probe\n${residue.join('\n')}\n`)
    const green = run(root)
    assert.equal(green.status, 0, `${edition} at its measured baseline: ${green.stdout}${green.stderr}`)
    assert.ok(green.stdout.includes(`${baseline} still English-only (baseline ${baseline})`))

    write(root, residueFile, `package probe\n${residue.join('\n')}\n` +
      'const newDisclaimer = "A new untranslated legal notice must fail in either edition."\n')
    const growth = run(root)
    assert.equal(growth.status, 1, `${edition} must reject a new untranslated disclaimer`)
    assert.ok(growth.stderr.includes(`${baseline + 1} engine disclaimers have no translation, baseline is ${baseline}.`))

    write(root, residueFile, `package probe\n${residue.slice(1).join('\n')}\n`)
    const shrink = run(root)
    assert.equal(shrink.status, 1, `${edition} must reject a stale baseline`)
    assert.ok(shrink.stderr.includes(`${baseline - 1} engine disclaimers are untranslated but the baseline still says ${baseline}.`))
    console.log(`PASS: ${edition} baseline ${baseline}; growth and stale baseline both refuse`)
  }
} finally {
  fs.rmSync(scratch, { recursive: true, force: true })
}
