// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// spec:openapi — validate the product's OpenAPI contract as OpenAPI 3.1.
// We validate the REAL file the site renders (web/openapi/openapi.json, authored
// by core/api), not a copy, so a broken contract fails the docs build too.
// (DOC-03)

import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { validate } from '@readme/openapi-parser'

// Both published contracts: the stable core (openapi.json) and the beta
// module-route document (openapi.beta.json). A broken EITHER fails the
// docs build.
const SPECS = ['../../web/openapi/openapi.json', '../../web/openapi/openapi.beta.json']

for (const rel of SPECS) {
  const spec = fileURLToPath(new URL(rel, import.meta.url))
  const raw = JSON.parse(await readFile(spec, 'utf8'))

  if (typeof raw.openapi !== 'string' || !raw.openapi.startsWith('3.1')) {
    console.error(`✗ ${rel}: expected OpenAPI 3.1.x, got openapi="${raw.openapi}"`)
    process.exit(1)
  }

  const pathCount = Object.keys(raw.paths ?? {}).length
  if (pathCount === 0) {
    console.error(`✗ ${rel}: OpenAPI document has no paths.`)
    process.exit(1)
  }

  try {
    // Throws (with a precise message) on any structural / schema violation.
    await validate(spec)
  } catch (err) {
    console.error(`✗ ${rel}: OpenAPI spec INVALID:\n${err.message}`)
    process.exit(1)
  }

  console.log(`✓ OpenAPI ${raw.openapi} valid: ${pathCount} paths (${rel}).`)
}
