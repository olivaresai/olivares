// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const script =
  process.argv[2] ??
  fileURLToPath(new URL('./check-i18n-usage.mjs', import.meta.url))
const scratch = fs.mkdtempSync(
  path.join(process.env.TMPDIR ?? os.tmpdir(), 'i18n-usage-'),
)
function write(relative, body) {
  const file = path.join(scratch, relative)
  fs.mkdirSync(path.dirname(file), { recursive: true })
  fs.writeFileSync(file, body)
}
function run(status, message) {
  const result = spawnSync(
    process.execPath,
    [path.join(scratch, 'scripts/check-i18n-usage.mjs')],
    { encoding: 'utf8' },
  )
  assert.equal(result.error, undefined)
  assert.equal(result.signal, null)
  assert.equal(
    result.status,
    status,
    `${message}: ${result.stdout}${result.stderr}`,
  )
  return result
}
try {
  fs.mkdirSync(path.join(scratch, 'scripts'))
  fs.copyFileSync(script, path.join(scratch, 'scripts/check-i18n-usage.mjs'))
  write(
    'web/src/lib/i18n/locales/en/common.json',
    '{"actions":{"close":"Close"}}',
  )
  for (const [feature, namespace, bundle] of [
    ['console', 'console', { shared: { title: 'Shared' } }],
    [
      'extension-a',
      'console',
      { shared: { title: 'Replacement title', first: 'First' } },
    ],
    [
      'extension-b',
      'console',
      { shared: { second: 'Second', count_one: 'One', count_other: 'Many' } },
    ],
    ['foundation-extension', 'common', { actions: { extra: 'Extra' } }],
  ]) {
    write(
      `web/src/features/${feature}/i18n/index.ts`,
      `registerTranslations('${namespace}', { en })\n`,
    )
    write(`web/src/features/${feature}/i18n/en.json`, JSON.stringify(bundle))
  }
  for (const [feature, directory, key] of [
    ['console', 'business-i18n', 'identity'],
    ['extension-a', 'business-i18n', 'governance'],
    ['extension-b', 'business-i18n', 'agentops'],
    ['extension-b', 'executive-i18n', 'executive'],
  ]) {
    write(`web/src/features/${feature}/${directory}/index.ts`,
      `import en from './en.json'\nregisterTranslations('${directory === 'executive-i18n' ? 'executive' : 'console'}', { en })\n`)
    write(`web/src/features/${feature}/${directory}/en.json`,
      JSON.stringify({ paid: { [key]: key } }))
  }
  const calls =
    "const { t } = useTranslation('console')\nt('shared.title')\nt('shared.first')\nt('shared.second')\nt('shared.count')\nt('common:actions.close')\nt('common:actions.extra')\nt('paid.identity')\nt('paid.governance')\nt('paid.agentops')\nt('executive:paid.executive')\n"
  write('web/src/view.tsx', calls)
  const merged = run(
    0,
    'shared, multiple overlay and foundation extension keys resolve',
  )
  assert.match(
    merged.stdout,
    /10 literal t\(\) call\(s\), 3 English namespace\(s\)/,
  )
  write('web/src/features/console/unregistered/en.json', '{"ghost":"Not registered"}')
  for (const key of [
    'console:ghost',
    'console:paid.missing',
    'console:shared.missing',
    'common:actions.missing',
    'unknown:shared.title',
  ]) {
    write('web/src/view.tsx', calls + `t('${key}')\n`)
    assert.match(
      run(1, `${key} must still fail`).stderr,
      new RegExp(key.replaceAll('.', '\\.')),
    )
  }
  write('web/src/features/console/business-i18n/en.json', '{}')
  assert.match(run(1, 'removed registered additive key refuses').stderr, /paid\.identity/)
  write('web/src/features/console/business-i18n/en.json', '{"paid":{"identity":"Identity"}}')
  write('web/src/features/extension-a/i18n/en.json', '{"shared":"Replacement"}')
  write('web/src/view.tsx', "t('console:shared.title')\n")
  assert.match(
    run(1, 'a leaf replacing a shared subtree must fail').stderr,
    /conflicting English i18n key shape: shared/,
  )
  write('web/src/features/extension-a/i18n/en.json', '{}')
  write(
    'web/src/features/aaa-extension/i18n/index.ts',
    "registerTranslations('console', { en })\n",
  )
  write(
    'web/src/features/aaa-extension/i18n/en.json',
    '{"shared":"Replacement"}',
  )
  assert.match(
    run(1, 'a subtree replacing a shared leaf must fail').stderr,
    /conflicting English i18n key shape: shared/,
  )
  write('web/src/features/aaa-extension/i18n/en.json', '{}')
  write('web/src/view.tsx', calls)
  assert.match(
    run(1, 'a removed overlay key must fail').stderr,
    /shared\.first/,
  )
  write('web/src/view.tsx', '')
  assert.match(run(2, 'zero calls remain unverified').stderr, /COULD NOT LOOK/)
  console.log(
    'PASS: merged conventional and registered additive namespaces resolve all ten keys; unregistered JSON refuses; missing overlay/foundation/namespace and removed keys refuse; shape conflicts refuse in both orders; zero calls remain unverified',
  )
} finally {
  fs.rmSync(scratch, { recursive: true, force: true })
}
