// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// test:modules — anti-drift coverage guard for the per-module reference.
//
// Three assertions, so the reference cannot silently fall behind the code:
//   1. Every implemented module dir under /modules (except `example`) has a
//      reference page under docs-site/.../reference/modules/. A NEW module dir
//      with no mapping fails loudly — whoever adds the module must add its page here.
//   2. The catalog-only pages exist — the platform/core capability pages with no
//      /modules dir of their own (XIX/XX/XXI engine-or-web, plus the planned
//      XXIII fine-tuning page). The 30-module catalog itself is covered by 1+3.
//   3. Every reference page is linked from reference/modules/overview.md, so no
//      page is orphaned from the catalog the reader navigates.

import { readFile, readdir, access } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'

const here = (p) => fileURLToPath(new URL(p, import.meta.url))
const MODULES_DIR = here('../../modules')
const REF_DIR = here('../src/content/docs/reference/modules')
const OVERVIEW = here('../src/content/docs/reference/modules/overview.md')

const fail = (m) => { console.error(`✗ modules: ${m}`); process.exit(1) }
const exists = async (p) => { try { await access(p); return true } catch { return false } }

// modules/<dir> -> reference page slug. Keep in sync when a module is added.
const DIR_TO_SLUG = {
  claudeadoption: 'claudeadoption',
  consoleviews: 'consoleviews',
  reporting: 'reporting',
  'access-map': 'iii-access-map',
  'capabilities': 'v-capabilities',
  'catalog': 'xiv-catalog',
  'claudeadoption': 'claudeadoption',
  'compliance': 'xiii-compliance',
  'consoleviews': 'consoleviews',
  'deploy': 'vii-deploy',
  'evals': 'xii-evals',
  'eventing': 'eventing', // external subscriptions over the bus — its own page (Platform & integrations)
  'finops': 'xi-finops',
  'governance': 'vi-governance',
  'health': 'xxii-health',
  'inferenceproxy': 'inferenceproxy', // Inline inference PEP proxy — its own page (Claude & agent ecosystem)
  'inventory': 'i-inventory',
  'knowledge': 'viii-knowledge',
  'liveingest': 'live-ingest',
  'models': 'x-models',
  'notify': 'xv-notify',
  'observability': 'observability', // supporting read-model — its own page, like live-ingest
  'orchestration': 'iv-orchestration',
  'posture-export': 'posture-export', // read-only posture/inventory export — its own page (Compliance & evidence)
  'recording': 'recording', // privileged-session recording — its own page (Security & data protection)
  'redteam': 'xviii-redteam',
  'reporting': 'reporting',
  'sandbox': 'xvii-sandbox',
  'security': 'ix-security',
  'sessions': 'ii-sessions',
  'siemforward': 'siemforward', // SIEM/ITSM forwarder — its own page (Compliance & evidence)
  'sourcescope': 'sourcescope', // Source & credential scoping — its own page (Govern & enforce)
  'voice': 'xvi-voice',
}

// Catalog pages with no /modules dir (foundational engine/web, or post-v1).
const CATALOG_ONLY = [
  'xix-api-manage-as-code',
  'xx-multi-tenancy',
  'xxi-executive-dashboards',
  'xxiii-fine-tuning',
]

// 1. Every implemented module dir maps to an existing reference page.
const entries = await readdir(MODULES_DIR, { withFileTypes: true })
const moduleDirs = entries
  .filter((e) => e.isDirectory() && e.name !== 'example')
  .map((e) => e.name)

const wantSlugs = new Set()
for (const dir of moduleDirs) {
  const slug = DIR_TO_SLUG[dir]
  if (!slug) {
    fail(`module dir "modules/${dir}" has no reference page mapping — add it to DIR_TO_SLUG and write reference/modules/<slug>.md`)
  }
  const page = `${REF_DIR}/${slug}.md`
  if (!(await exists(page))) {
    fail(`module "modules/${dir}" maps to "${slug}" but reference/modules/${slug}.md is missing`)
  }
  wantSlugs.add(slug)
}

// 2. The catalog-only pages exist (platform/core capabilities and the planned
// XXIII page, alongside the 30-module catalog covered by 1+3).
for (const slug of CATALOG_ONLY) {
  if (!(await exists(`${REF_DIR}/${slug}.md`))) {
    fail(`catalog page reference/modules/${slug}.md is missing (catalog coverage incomplete)`)
  }
  wantSlugs.add(slug)
}

// 3. Every reference page is linked from overview.md (no orphans).
const overview = await readFile(OVERVIEW, 'utf8')
for (const slug of wantSlugs) {
  if (!overview.includes(`/reference/modules/${slug}/`)) {
    fail(`reference/modules/${slug}.md is not linked from overview.md — every module page must be reachable from the catalog`)
  }
}

console.log(`✓ modules: ${moduleDirs.length} module dirs + ${CATALOG_ONLY.length} catalog pages covered and linked (${wantSlugs.size} reference pages)`)
