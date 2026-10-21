// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import census from './route-census.json'
import { FEATURE_EXTENSIONS } from './extensions'
import { FEATURE_VIEWS, ROUTE_ALIASES } from './registry'

// docs/editions.md ("Console pages") lists the pages it places in Business.
const editions = readFileSync(
  resolve(__dirname, '../../../docs/editions.md'),
  'utf8',
)
const MARKER = 'console-business-pages'
const block = new RegExp(
  `<!-- ${MARKER}:start -->([\\s\\S]*?)<!-- ${MARKER}:end -->`,
).exec(editions)?.[1]
const BUSINESS_PAGES = [...(block ?? '').matchAll(/^- `([^`]+)`$/gm)].map(
  (match) => match[1]!,
)

describe('edition placement of the console routes', () => {
  it('keeps the census equal to the pages docs/editions.md places in Business', () => {
    expect(BUSINESS_PAGES.length).toBeGreaterThan(0)
    expect([...census.business_paths].sort()).toEqual(
      [...BUSINESS_PAGES].sort(),
    )
    for (const path of BUSINESS_PAGES)
      expect(census.paths, path).toContain(path)
  })

  it('serves a Business page only where the build carries the Business extension', () => {
    if (FEATURE_EXTENSIONS.length > 0) return
    expect(
      FEATURE_VIEWS.map((view) => view.path).filter((path) =>
        census.business_paths.includes(path),
      ),
    ).toEqual([])
  })

  it('answers a Business path with the Business notice, not a redirect', () => {
    for (const path of census.business_paths) {
      expect(
        ROUTE_ALIASES.some((alias) => alias.from === path),
        path,
      ).toBe(false)
    }
  })

  // docs/editions.md ("Records"): retention schedules and the hold-checked sweep are Community,
  // while the Compliance page that used to hold the Retention tab is Business.
  it('keeps the Community Retention schedules reachable without the Business Compliance page', () => {
    const retention = FEATURE_VIEWS.find((view) => view.path === '/retention')
    expect(retention?.permission).toBe('compliance:retention:read')
    expect(census.paths).toContain('/retention')
    expect(census.business_paths).not.toContain('/retention')
    expect(BUSINESS_PAGES).not.toContain('/retention')
  })
})
