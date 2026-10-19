#!/usr/bin/env node
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = fileURLToPath(new URL('../', import.meta.url))
const checker = process.argv[2] ?? path.join(root, 'scripts/check-i18n-parity.mjs')
const scratch = mkdtempSync(path.join(tmpdir(), 'i18n-plural-'))
const languages = ['en', 'de', 'es', 'fr', 'ru', 'ja', 'zh', 'it', 'ko']
const localePath = (language, namespace = 'sessions') => namespace === 'sessions'
  ? `web/src/features/sessions/i18n/${language}.json`
  : `web/src/lib/i18n/locales/${language}/${namespace}.json`
const resources = new Map(languages.flatMap((language) => ['sessions', 'settings'].map((namespace) => {
  const file = localePath(language, namespace)
  return [file, JSON.parse(readFileSync(path.join(root, file), 'utf8'))]
})))

function write(relative, value) {
  const file = path.join(scratch, relative)
  mkdirSync(path.dirname(file), { recursive: true })
  writeFileSync(file, JSON.stringify(value))
}

function run(args = []) {
  const result = spawnSync(process.execPath,
    [path.join(scratch, 'scripts/check-i18n-parity.mjs'), ...args],
    { encoding: 'utf8', cwd: scratch, timeout: 10_000 },
  )
  assert.equal(result.error, undefined)
  assert.equal(result.signal, null)
  return result
}

function rejects(language, change, diagnostic, namespace = 'sessions') {
  const file = localePath(language, namespace)
  const resource = structuredClone(resources.get(file))
  change(resource)
  write(file, resource)
  try {
    for (const args of [[], ['--file', file]]) {
      const result = run(args)
      assert.equal(result.status, 1, `${namespace}/${language} ${args.join(' ')}: ${result.stdout}${result.stderr}`)
      assert.ok(result.stderr.includes(`[${namespace}/${language}] ${diagnostic}`), result.stderr)
    }
  } finally {
    write(file, resources.get(file))
  }
}

try {
  mkdirSync(path.join(scratch, 'scripts'))
  copyFileSync(checker, path.join(scratch, 'scripts/check-i18n-parity.mjs'))
  for (const language of languages) {
    for (const namespace of ['common', 'nav', 'auth', 'errors', 'settings']) {
      const file = localePath(language, namespace)
      write(file, resources.get(file) ?? {})
    }
    write(localePath(language), resources.get(localePath(language)))
  }

  const full = run()
  assert.equal(full.status, 0, `unchanged session strings must pass: ${full.stdout}${full.stderr}`)
  for (const language of languages) {
    for (const namespace of ['sessions', 'settings']) {
      const single = run(['--file', localePath(language, namespace)])
      assert.equal(single.status, 0, `${namespace}/${language}: ${single.stdout}${single.stderr}`)
    }
  }
  console.log('PASS: unchanged session and settings plurals in all nine languages, full and single-file modes')

  for (const language of languages.slice(1)) {
    const categories = new Intl.PluralRules(language).resolvedOptions().pluralCategories
    for (const category of categories.filter((c) => c !== 'one' || language === 'ru')) {
      const key = `compactedTimes_${category}`
      rejects(language, (resource) => { resource.activity[key] = resource.activity[key].replace('{{n}}', '') },
        `activity.${key}: missing placeholder {{n}}`)
    }
    rejects(language, (resource) => { resource.activity.compactedTimes_other += ' {{invented}}' },
      'activity.compactedTimes_other: extra placeholder {{invented}}')
    // A singular that needs no variable must still be a real string, and it may not invent one.
    rejects(language, (resource) => { resource.activity.compactedTimes_one = '' },
      'activity.compactedTimes_one: empty translation')
    rejects(language, (resource) => { resource.activity.compactedTimes_one = null },
      'activity.compactedTimes_one: empty translation')
    rejects(language, (resource) => { resource.activity.compactedTimes_one += ' {{invented}}' },
      'activity.compactedTimes_one: extra placeholder {{invented}}')
  }
  console.log('PASS: required compaction counts, including Russian one, and invented variables fail in both modes')

  assert.equal(new Intl.PluralRules('ru').select(21), 'one')
  for (const base of ['apply', 'pending']) {
    for (const category of new Intl.PluralRules('ru').resolvedOptions().pluralCategories) {
      const key = `${base}_${category}`
      rejects('ru', (resource) => { resource.modules[key] = resource.modules[key].replace('{{count}}', '') },
        `modules.${key}: missing placeholder {{count}}`, 'settings')
    }
  }
  console.log('PASS: Russian Apply and pending-change counts remain required in every form, both modes')

  // Different English forms can require different variables and Trans components.
  // Additional CLDR categories must retain the English other-form contract.
  for (const language of languages) {
    const resource = resources.get(localePath(language))
    resource.probe = {}
    for (const category of new Intl.PluralRules(language).resolvedOptions().pluralCategories) {
      resource.probe[`items_${category}`] = category === 'one'
        ? language === 'ru' ? '<em>{{n}} for {{name}}</em>' : '<em>One for {{name}}</em>'
        : '<strong>{{n}} for {{name}}</strong>'
    }
    write(localePath(language), resource)
  }
  const distinct = run()
  assert.equal(distinct.status, 0, `${distinct.stdout}${distinct.stderr}`)
  for (const language of languages.slice(1)) {
    for (const category of new Intl.PluralRules(language).resolvedOptions().pluralCategories) {
      const key = `items_${category}`
      const tag = category === 'one' ? 'em' : 'strong'
      rejects(language, (resource) => { resource.probe[key] = resource.probe[key].replace('{{name}}', '') },
        `probe.${key}: missing placeholder {{name}}`)
      rejects(language, (resource) => { resource.probe[key] = resource.probe[key].replaceAll(tag, 'b') },
        `probe.${key}: missing tag <${tag}>`)
      rejects(language, (resource) => { resource.probe[key] += ' <extra/>' },
        `probe.${key}: extra tag <extra>`)
    }
  }
  console.log('PASS: required singular variables, category-specific tags and other-form fallbacks remain enforced')
} finally {
  rmSync(scratch, { recursive: true, force: true })
}
