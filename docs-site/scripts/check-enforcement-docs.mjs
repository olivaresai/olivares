// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Observation is out of band; engine-backed enforcement denies closed.
import { readFileSync, existsSync } from 'node:fs'

const pages = [
  'start/what-is-olivares-ai.md',
  'explanation/security/security-model.md',
  'explanation/security/threat-model.mdx',
  'explanation/positioning/vs-llm-observability.md',
  'explanation/positioning/where-olivares-fits-vs-your-gateway.md',
]
const forbidden = [
  /does not sit in the agent's data path/i,
  /the core observes; it does not interpose/i,
  /never in the data path/i,
  /olivares ai is never in the data path/i,
  /read-first keeps the product out of the blast radius/i,
  /does not route, cache, load-balance, or sit on the hot path of your model traffic/i,
]
let failures = 0
let checked = 0
for (const prefix of ['', '2026-06/']) {
  for (const page of pages) {
    const name = prefix + page
    const url = new URL('../src/content/docs/' + name, import.meta.url)
    // The June snapshot predates the positioning pages.
    if (prefix && page.startsWith('explanation/positioning/')) continue
    if (!existsSync(url)) throw new Error(`Missing documentation: ${name}`)
    const text = readFileSync(url, 'utf8').replace(/\*/g, '').replace(/\s+/g, ' ')
    for (const pattern of forbidden) {
      if (pattern.test(text)) {
        console.error(`✗ enforcement: ${name}: false out-of-path claim: ${pattern}`)
        failures++
      }
    }
    if (!/Claude Code/.test(text) || !/deny.closed/i.test(text) || !/unreachable/i.test(text)) {
      console.error(`✗ enforcement: ${name}: explain the Claude Code hook's deny-closed engine dependency`)
      failures++
    }
    checked++
  }
}
if (failures) process.exit(1)
console.log(`✓ enforcement: ${checked} pages distinguish observation from deny-closed enforcement.`)
