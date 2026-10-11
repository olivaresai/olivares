// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { PUBLISHED_LOCALES } from '../src/site-locales.mjs'

const read = (path) => readFileSync(new URL(path, import.meta.url), 'utf8')
const override = read('../../deploy/compose/docker-compose.postgres.yml')
const required = [...new Set([...override.replace(/^\s*#.*$/gm, '')
  .matchAll(/\$\{([A-Z_]+):\?/g)].map((match) => match[1]))]
assert.ok(required.length > 0, 'Postgres override must declare required variables')

// Current guides only: the 2026-06 snapshot documents its historical deployment.
const locales = ['', ...PUBLISHED_LOCALES.map((locale) => `${locale}/`)]
const pages = ['how-to/docker-deployment.md', 'tutorials/getting-started/docker-compose.mdx']
const failures = []
for (const locale of locales) {
  for (const page of pages) {
    const path = `${locale}${page}`
    const content = read(`../src/content/docs/${path}`)
    const blocks = [...content.matchAll(/```bash\n([\s\S]*?)```/g)]
      .map((match) => match[1])
      .filter((block) => block.includes('docker-compose.postgres.yml'))
    assert.equal(blocks.length, 1, `${path}: expected one Postgres setup block`)
    const setup = blocks[0].split('docker compose')[0]
    assert.ok(setup.includes('cp deploy/compose/.env.example deploy/compose/.env'),
      `${path}: missing .env setup`)
    for (const variable of required) {
      if (!new RegExp(`\\b${variable}\\b`).test(setup)) {
        failures.push(`${path}: setup does not name ${variable}`)
      }
    }
  }
}
if (failures.length) {
  console.error(failures.join('\n'))
  process.exit(1)
}
console.log(`Postgres passwords: ${locales.length * pages.length} guides name all ${required.length} required variables before startup`)
