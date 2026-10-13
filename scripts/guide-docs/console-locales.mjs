// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Keep translated prose, but check the route identities and count against English.
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const root = process.argv[2] ?? fileURLToPath(new URL('../../', import.meta.url))
const begin = '<!-- BEGIN GENERATED olivares-console-routes'
const end = '<!-- END GENERATED olivares-console-routes -->'

function roster(rel) {
  const page = readFileSync(path.join(root, rel), 'utf8')
  if (page.split(begin).length !== 2 || page.split(end).length !== 2) {
    throw new Error(`${rel}: expected one console route marker pair`)
  }
  const start = page.indexOf(begin)
  const stop = page.indexOf(end)
  if (stop < start) throw new Error(`${rel}: reversed console route markers`)
  const region = page.slice(page.indexOf('\n', start), stop)
  // Route identity is the backtick-delimited path in column two, never a label
  // or a reference URL. All locales retain this machine-readable column.
  const routes = [...region.matchAll(/^\|(?:\\.|[^|\\\n])*\|\s*`(\/[^`]*)`[^|]*\|/gm)].map((m) => m[1])
  const claims = [...region.matchAll(/\*\*(\d+)[^*\n]*\*\*/g)].map((m) => Number(m[1]))
  if (routes.length === 0 || claims.length !== 1) {
    throw new Error(`${rel}: cannot read route rows or the single bold route count`)
  }
  return { routes, count: claims[0] }
}

try {
  const { PUBLISHED_LOCALES } = await import(pathToFileURL(path.join(root, 'docs-site/src/site-locales.mjs')))
  if (!Array.isArray(PUBLISHED_LOCALES) || PUBLISHED_LOCALES.length === 0) {
    throw new Error('the docs site declares no published locales')
  }
  const english = roster('docs-site/src/content/docs/reference/console.md')
  const expected = new Set(english.routes)
  const findings = []
  for (const locale of ['', ...PUBLISHED_LOCALES]) {
    const rel = `docs-site/src/content/docs/${locale ? `${locale}/` : ''}reference/console.md`
    const { routes, count } = roster(rel)
    const actual = new Set(routes)
    if (count !== routes.length || count !== expected.size) {
      findings.push(`${rel}: claims ${count} routes, lists ${routes.length}; English has ${expected.size}`)
    }
    if (actual.size !== routes.length) findings.push(`${rel}: duplicate route rows`)
    for (const route of expected) {
      if (!actual.has(route)) findings.push(`${rel}: missing ${route}`)
    }
    for (const route of actual) {
      if (!expected.has(route)) findings.push(`${rel}: stale ${route}`)
    }
  }
  if (findings.length) {
    console.error(`console-locales: ${findings.length} difference(s):\n${findings.join('\n')}`)
    process.exitCode = 1
  } else {
    console.log(`console-locales: OK — ${PUBLISHED_LOCALES.length} locales match English's ${expected.size} routes and count`)
  }
} catch (err) {
  console.error(`console-locales: CANNOT LOOK — ${err.message}`)
  process.exitCode = 2
}
