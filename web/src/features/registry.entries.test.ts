// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// FEATURE_VIEWS is collected from `features/<dir>/views.tsx`. Two facts the collection
// relies on and nothing else checks: `order` places one view only, and each entry's page
// lives in the directory that declares it (module-gate-census reads a view's directory
// from where its entry is).
import { Terminal } from 'lucide-react'
import { describe, expect, it } from 'vitest'
import { FEATURE_VIEWS, type FeatureView, type ViewEntry } from './registry'

const SOURCES = import.meta.glob<string>('./*/views.tsx', {
  eager: true,
  query: '?raw',
  import: 'default',
})
const ENTRIES = import.meta.glob<readonly ViewEntry[]>('./*/views.tsx', {
  eager: true,
  import: 'VIEWS',
})

describe('view entries', () => {
  it('give every built-in view a unique order', () => {
    const seen = new Map<number, string>()
    const repeated: string[] = []
    for (const v of Object.values(ENTRIES).flat()) {
      const other = seen.get(v.order)
      if (other) repeated.push(`${v.id} and ${other} share order ${v.order}`)
      seen.set(v.order, v.id)
    }
    expect(repeated).toEqual([])
  })

  it('are all registered', () => {
    const ids = new Set(FEATURE_VIEWS.map((v) => v.id))
    const entries = Object.values(ENTRIES).flat()
    expect(entries.length).toBeGreaterThan(0)
    expect(entries.filter((v) => !ids.has(v.id)).map((v) => v.id)).toEqual([])
  })

  it('import their pages from their own directory', () => {
    const outside = Object.entries(SOURCES)
      .filter(([, src]) => /import\(\s*['"](?!\.\/)/.test(src))
      .map(([file]) => file)
    expect(Object.keys(SOURCES).length).toBeGreaterThan(0)
    expect(outside).toEqual([])
  })

  // The Business overlay's entries still declare the retired hub axis. The type keeps
  // accepting it (and nothing reads it), so the overlay builds unchanged. Type-checked by
  // `tsc -b`, which covers this file.
  it('still accept an edition view that declares the retired hub', () => {
    const edition = {
      id: 'edition-fixture',
      path: '/edition-fixture',
      hub: 'operate',
      icon: Terminal,
      helpHref: '/',
      navigation: { kind: 'feature', areaId: 'ai', sectionId: 'sessions' },
      element: () => null,
    } satisfies FeatureView
    expect(edition.navigation.areaId).toBe('ai')
  })
})
