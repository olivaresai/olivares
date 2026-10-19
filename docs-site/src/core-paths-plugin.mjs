// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// The number of core REST paths, written in every locale as the token below and filled in
// at build time from the same file the REST reference is rendered from
// (web/openapi/openapi.json). No page states the count by hand, so an API change cannot
// leave a stale number behind; scripts/check-public-counts.sh refuses a literal count in
// the pages and scripts/check-spec-drift.mjs checks the built output.
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

export const CORE_PATHS_TOKEN = '{{core-paths}}'

const CONTRACT = fileURLToPath(new URL('../../web/openapi/openapi.json', import.meta.url))

export function countCorePaths(contract = CONTRACT) {
  let paths
  try {
    paths = JSON.parse(readFileSync(contract, 'utf8')).paths
  } catch (error) {
    throw new Error(`core-paths: cannot read ${contract}: ${error.message}`)
  }
  // An empty, missing or non-object `paths` is a vanished measurement, not a count of zero.
  if (paths === null || typeof paths !== 'object' || Array.isArray(paths) || Object.keys(paths).length === 0) {
    throw new Error(`core-paths: ${contract} declares no paths; refusing to publish a count of zero`)
  }
  return Object.keys(paths).length
}

export function corePathsPlugin(contract) {
  const count = String(countCorePaths(contract))
  return {
    // The count is in the name on purpose: Astro skips re-rendering a page whose file is unchanged
    // and clears its content cache only when the serialized config changes, which drops functions
    // but keeps strings. A count only in the closure would leave old numbers in cached pages.
    name: `core-paths-${count}`,
    text(node, ctx) {
      if (node.value.includes(CORE_PATHS_TOKEN)) {
        ctx.replaceNode(node, { type: 'text', value: node.value.replaceAll(CORE_PATHS_TOKEN, count) })
      }
    },
  }
}
