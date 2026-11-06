// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE NOUN PER PAGE NAME. A page heading or a tab that pairs two nouns ("Health & SLA",
// "Approvals and permissions") names two ideas; the page is named for the one a person looks
// for, and the former name stays a palette search word (nav:aliases.<id>). The English
// catalog is the source: every feature's `title` and `tabs.*`.
import { describe, expect, it } from 'vitest'

const catalogs = import.meta.glob<Record<string, unknown>>(
  '@/features/*/i18n/en.json',
  { eager: true, import: 'default' },
)

/** Names still paired, each awaiting its own one-noun name. */
const PENDING = new Set([
  'compliance:tabs.erasure',
  'finops:title',
  'observability:title',
  'operations:title',
  'platforms:tabs.surfaces',
  'voice:title',
])

const PAIR = /&|\band\b/i

function pageNames(): [id: string, name: string][] {
  const out: [string, string][] = []
  for (const [path, catalog] of Object.entries(catalogs)) {
    const ns = path.split('/features/')[1].split('/')[0]
    if (typeof catalog.title === 'string')
      out.push([`${ns}:title`, catalog.title])
    const tabs = catalog.tabs
    if (tabs && typeof tabs === 'object')
      for (const [key, name] of Object.entries(tabs))
        if (typeof name === 'string') out.push([`${ns}:tabs.${key}`, name])
  }
  return out
}

describe('page names are one noun', () => {
  it('reads the feature catalogs', () => {
    expect(pageNames().length).toBeGreaterThan(50)
  })

  it('pairs no two nouns in a page heading or a tab', () => {
    const paired = pageNames()
      .filter(([id, name]) => PAIR.test(name) && !PENDING.has(id))
      .map(([id, name]) => `${id} = ${name}`)
    expect(paired).toEqual([])
  })

  it('lists as pending only names that are still paired', () => {
    const names = new Map(pageNames())
    const fixed = [...PENDING].filter((id) => !PAIR.test(names.get(id) ?? ''))
    expect(fixed).toEqual([])
  })
})
