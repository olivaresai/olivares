// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// test:modules — the CLI/console selectable roster is the docs source of truth.
// Package directories can implement several modules or no selectable module.

import { readFile, access } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'

const here = (p) => fileURLToPath(new URL(p, import.meta.url))
const SPEC = here('../../core/modulespec/modules.json')
const REF_DIR = here('../src/content/docs/reference/modules')
const OVERVIEW = `${REF_DIR}/overview.md`
const fail = (m) => { console.error(`✗ modules: ${m}`); process.exit(1) }
const exists = async (p) => {
  try { await access(p); return true } catch (error) {
    if (error.code === 'ENOENT') return false
    throw error
  }
}

// Only historical URLs differ from the selectable namespace. New pages use the
// namespace directly; this is a URL compatibility map, not a second roster.
const LEGACY_SLUGS = {
  accessmap: 'iii-access-map',
  adoption: 'claudeadoption',
  capabilities: 'v-capabilities',
  catalog: 'xiv-catalog',
  compliance: 'xiii-compliance',
  deploy: 'vii-deploy',
  evals: 'xii-evals',
  finops: 'xi-finops',
  governance: 'vi-governance',
  health: 'xxii-health',
  inventory: 'i-inventory',
  knowledge: 'viii-knowledge',
  liveingest: 'live-ingest',
  models: 'x-models',
  notify: 'xv-notify',
  orchestration: 'iv-orchestration',
  posture: 'posture-export',
  redteam: 'xviii-redteam',
  sandbox: 'xvii-sandbox',
  security: 'ix-security',
  sessions: 'ii-sessions',
  voice: 'xvi-voice',
}

// Supplementary capability/availability pages remain covered, but do not add to
// the selectable count (session-cockpit is a non-selectable edition descriptor).
const CATALOG_ONLY = [
  'session-cockpit',
  'xix-api-manage-as-code',
  'xx-multi-tenancy',
  'xxi-executive-dashboards',
  'xxiii-fine-tuning',
]

const specs = JSON.parse(await readFile(SPEC, 'utf8'))
const selectable = specs.filter(spec => spec.selectable === true)
const overview = await readFile(OVERVIEW, 'utf8')
// Require a Markdown link rather than a bare URL.
const links = new Set([...overview.matchAll(/\[[^\]\n]+\]\(\/reference\/modules\/([^/)]+)\/\)/g)]
  .map(match => match[1]))

for (const { namespace } of selectable) {
  const slug = LEGACY_SLUGS[namespace] ?? namespace
  if (!(await exists(`${REF_DIR}/${slug}.md`))) {
    fail(`module "${namespace}" reference page reference/modules/${slug}.md is missing`)
  }
  if (!links.has(slug)) {
    fail(`module "${namespace}" reference/modules/${slug}.md is not linked from overview.md`)
  }
}

for (const slug of CATALOG_ONLY) {
  if (!(await exists(`${REF_DIR}/${slug}.md`))) {
    fail(`catalog page reference/modules/${slug}.md is missing (catalog coverage incomplete)`)
  }
  if (!links.has(slug)) {
    fail(`reference/modules/${slug}.md is not linked from overview.md`)
  }
}

const count = overview.match(/^##? The (\d+) (?:selectable )?modules\b/m)
if (!count || Number(count[1]) !== selectable.length) {
  fail(`catalog count must match ${selectable.length} selectable modules in core/modulespec/modules.json`)
}
console.log(`✓ modules: ${selectable.length} selectable modules + ${CATALOG_ONLY.length} supplementary pages covered and linked`)
