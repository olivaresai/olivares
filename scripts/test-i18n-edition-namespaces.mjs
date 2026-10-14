#!/usr/bin/env node
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { copyFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const script = process.argv[2] ?? fileURLToPath(new URL('./check-i18n-parity.mjs', import.meta.url))
const scratch = mkdtempSync(path.join(process.env.TMPDIR ?? tmpdir(), 'i18n-edition-'))
const languages = ['en', 'es', 'zh', 'ja', 'de', 'ru', 'fr']
function write(relative, value) {
  const file = path.join(scratch, relative)
  mkdirSync(path.dirname(file), { recursive: true })
  writeFileSync(file, value)
}
function run(args = []) {
  const result = spawnSync(process.execPath, [path.join(scratch, 'scripts/check-i18n-parity.mjs'), ...args], { encoding: 'utf8', cwd: scratch })
  assert.equal(result.error, undefined)
  assert.equal(result.signal, null)
  return result
}
try {
  mkdirSync(path.join(scratch, 'scripts'))
  copyFileSync(script, path.join(scratch, 'scripts/check-i18n-parity.mjs'))
  for (const language of languages) {
    for (const namespace of ['common', 'nav', 'auth', 'errors', 'settings']) {
      write(`web/src/lib/i18n/locales/${language}/${namespace}.json`, '{}')
    }
  }
  for (const [directory, namespace] of [['compliance-packs', 'compliance'], ['on-demand-reports', 'reporting'], ['authorization', 'console']]) {
    write(`web/src/features/${directory}/view.tsx`, `useTranslation('${namespace}')\n`)
    const canonical = `web/src/features/${namespace}/i18n/en.json`
    write(canonical, '{"title":"English title"}')
    for (const language of languages.slice(1)) {
      write(`web/src/features/${namespace}/i18n/${language}.json`, '{"title":"Translated title"}')
    }
    const accepted = run()
    assert.equal(accepted.status, 0, `${directory} uses ${namespace}: ${accepted.stdout}${accepted.stderr}`)
    rmSync(path.join(scratch, canonical))
    const missingEnglish = run()
    assert.equal(missingEnglish.status, 1, `${directory} must retain its English namespace`)
    assert.ok(`${missingEnglish.stdout}${missingEnglish.stderr}`.includes(canonical), `${directory} reports its canonical English path`)
    write(canonical, '{"title":"English title"}')
    for (const language of languages.slice(1)) {
      const translated = `web/src/features/${namespace}/i18n/${language}.json`
      rmSync(path.join(scratch, translated))
      const missingLocale = run()
      assert.equal(missingLocale.status, 1, `${directory} must retain its ${language} namespace`)
      assert.ok(missingLocale.stderr.includes(`[${namespace}/${language}] missing locale file:`))
      write(translated, '{}')
      const missingKey = run()
      assert.equal(missingKey.status, 1, `${directory} must retain ${language} keys`)
      assert.ok(missingKey.stderr.includes(`[${namespace}/${language}] missing key: title`))
      write(translated, '{"title":"Translated title"}')
    }
  }
  write('web/src/features/access-map/view.tsx', 'export { QueryErrorState } from \"@/components/layout/query-error-state\"\n')
  const bundleless = run()
  assert.equal(bundleless.status, 1, 'access-map is a Community feature with its own namespace: UI without a bundle is refused')
  assert.ok(`${bundleless.stdout}${bundleless.stderr}`.includes('access-map'), 'the refusal names the access-map directory')
  for (const language of languages) {
    write(`web/src/features/access-map/i18n/${language}.json`, '{"title":"Access map"}')
  }
  assert.equal(run().status, 0, 'the access map retains its own namespace')
  rmSync(path.join(scratch, 'web/src/features/access-map/i18n/en.json'))
  const missingBusiness = run()
  assert.equal(missingBusiness.status, 1, 'a missing canonical access-map namespace is refused')
  assert.ok(`${missingBusiness.stdout}${missingBusiness.stderr}`.includes('web/src/features/access-map/i18n/en.json'))
  write('web/src/features/access-map/i18n/en.json', '{"title":"Access map"}')
  for (const directory of ['business-i18n', 'executive-i18n']) {
    const relative = `web/src/features/access-map/${directory}`
    write(`${relative}/index.ts`, "import en from './en.json'\nregisterTranslations('executive', { en })\n")
    for (const language of languages) write(`${relative}/${language}.json`, '{"paid":"Translated"}')
    assert.equal(run().status, 0, 'registered additive bundle has full locale parity')
    const single = run(['--file', `${relative}/es.json`])
    assert.equal(single.status, 0, `registered additive locale works in single-file mode: ${single.stdout}${single.stderr}`)
    rmSync(path.join(scratch, `${relative}/en.json`))
    const missing = run()
    assert.equal(missing.status, 1, 'registered additive bundle must retain English')
    assert.ok(`${missing.stdout}${missing.stderr}`.includes(`${relative}/en.json`))
    write(`${relative}/en.json`, '{"paid":"Translated"}')
    for (const language of languages.slice(1)) {
      rmSync(path.join(scratch, `${relative}/${language}.json`))
      assert.equal(run().status, 1, 'registered additive bundle must retain every language')
      write(`${relative}/${language}.json`, '{}')
      assert.equal(run().status, 1, 'registered additive bundle must retain translated keys')
      write(`${relative}/${language}.json`, '{"paid":"Translated"}')
    }
  }
  write('web/src/features/unclassified/view.tsx', 'export default null\n')
  assert.equal(run().status, 1, 'unclassified UI still requires a namespace')
  console.log('PASS: all three edition UI directories use existing namespaces; the access-map namespace is required and checked, with no bundle-less exception; missing English, all six translated locales/keys and unknown UI refuse')
} finally {
  rmSync(scratch, { recursive: true, force: true })
}
