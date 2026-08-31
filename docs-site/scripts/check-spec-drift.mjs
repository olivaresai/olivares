// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// test:drift — guarantee the rendered REST reference comes from the product's
// real OpenAPI spec, not a divergent copy that could silently rot. (DOC-03)
//
// Three assertions:
//   1. astro.config.mjs points starlight-openapi at ../web/openapi/openapi.json
//      (the single source of truth, served by core/api).
//   2. No second copy of an openapi.json lives inside docs-site/src or public
//      (a copy is exactly the drift we forbid).
//   3. If dist/ has been built, every HTTP operation in the CURRENT spec has a
//      generated page — so when the spec changes, the render changes.

import { readFile, readdir } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { createRequire } from 'node:module'
import { fileURLToPath, pathToFileURL } from 'node:url'

// starlight-openapi's operation renderer uses github-slugger for route names.
// Resolve that dependency from the renderer package itself so this checker cannot
// silently drift onto a separately versioned slug implementation.
const rendererRequire = createRequire(import.meta.resolve('starlight-openapi/package.json'))
const { slug: rendererSlug } = await import(pathToFileURL(rendererRequire.resolve('github-slugger')).href)

const here = (p) => fileURLToPath(new URL(p, import.meta.url))
const fail = (m) => { console.error(`✗ drift: ${m}`); process.exit(1) }

// 1. Config references the real spec paths — stable AND the beta document.
const config = await readFile(here('../astro.config.mjs'), 'utf8')
if (!config.includes('../web/openapi/openapi.json')) {
  fail("astro.config.mjs must point starlight-openapi at '../web/openapi/openapi.json' (the real spec).")
}
if (!config.includes('../web/openapi/openapi.beta.json')) {
  fail("astro.config.mjs must point starlight-openapi at '../web/openapi/openapi.beta.json' (the real beta module-route spec).")
}

// 2. No stale copy of the spec inside the site.
async function findCopies(dir, acc = []) {
  let entries
  try { entries = await readdir(dir, { withFileTypes: true }) } catch { return acc }
  for (const e of entries) {
    const full = `${dir}/${e.name}`
    if (e.isDirectory()) {
      if (e.name === 'node_modules' || e.name === 'dist' || e.name === '.astro') continue
      await findCopies(full, acc)
    } else if (e.name === 'openapi.json' || e.name === 'openapi.beta.json') {
      acc.push(full)
    }
  }
  return acc
}
const copies = await findCopies(here('../src')).then((a) => a.concat(findCopiesSyncPublic()))
function findCopiesSyncPublic() {
  // public/ may legitimately host downloadables, but NOT a copy of either spec.
  const pub = here('../public')
  return ['openapi.json', 'openapi.beta.json']
    .map((f) => `${pub}/${f}`)
    .filter((p) => existsSync(p))
}
if (copies.length > 0) {
  fail(`a copy of openapi.json exists inside the site (drift risk): ${copies.join(', ')}. Render from ../web/openapi/openapi.json instead.`)
}

// 3. If built, EACH spec's operations must all be rendered under its own base —
// the stable contract and the beta module-route document alike. This mirrors
// starlight-openapi's getOperationsByTag route contract: operationId is the route
// source, the path is its fallback, and duplicate fallback IDs on one path gain a
// method suffix. Slug normalization itself comes from the renderer dependency above.
const OPERATION_METHODS = ['get', 'put', 'post', 'delete', 'options', 'head', 'patch', 'trace']

function renderedOperationPages(document) {
  const pages = []
  for (const [path, pathItem] of Object.entries(document.paths ?? {})) {
    const operationIds = OPERATION_METHODS.map((method) =>
      method in pathItem ? (pathItem[method].operationId ?? path) : undefined,
    )
    for (const [index, method] of OPERATION_METHODS.entries()) {
      const operationId = operationIds[index]
      if (!operationId) continue
      const operationSlug = rendererSlug(operationId)
      const duplicateOnPath = operationIds.filter((id) => id === operationId).length > 1
      pages.push({
        operation: `${method.toUpperCase()} ${path}`,
        relativePath: duplicateOnPath ? `${operationSlug}/${rendererSlug(method)}` : operationSlug,
      })
    }
  }
  return pages
}

const RENDERED = [
  { spec: '../../web/openapi/openapi.json', dist: '../dist/reference/api/operations' },
  { spec: '../../web/openapi/openapi.beta.json', dist: '../dist/reference/api-beta/operations' },
]
let anyBuilt = false
for (const { spec: specRel, dist: distRel } of RENDERED) {
  const distDir = here(distRel)
  const document = JSON.parse(await readFile(here(specRel), 'utf8'))
  const expectedPages = renderedOperationPages(document)
  if (!existsSync(distDir)) continue
  anyBuilt = true

  const claimedPages = new Map()
  for (const { operation, relativePath } of expectedPages) {
    const previous = claimedPages.get(relativePath)
    if (previous) {
      fail(`renderer route collision in ${specRel}: ${previous} and ${operation} both map to ${distRel}/${relativePath}`)
    }
    claimedPages.set(relativePath, operation)
  }

  const missing = []
  for (const { operation, relativePath } of expectedPages) {
    if (!existsSync(`${distDir}/${relativePath}/index.html`)) {
      missing.push(`${operation} (expected page '${relativePath}/index.html' under ${distRel})`)
    }
  }
  if (missing.length > 0) {
    fail(`built reference is missing pages for current spec operations (render diverged from spec):\n  ${missing.join('\n  ')}`)
  }
  console.log(`✓ anti-drift: all ${expectedPages.length} operations of ${specRel} rendered under ${distRel}.`)
}
if (!anyBuilt) {
  console.log(`✓ anti-drift: config → real specs; no copy. (dist not built yet — page-coverage check runs post-build.)`)
} else {
  console.log(`✓ anti-drift: config → real specs; no copy.`)
}
