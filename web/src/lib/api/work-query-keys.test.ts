// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The neutral builders must keep the arrays `features/work/api.ts` already
// produced, and must reach exactly the intended families through a real
// QueryClient. Serialized shape is pinned literally, not derived from the module
// under test.
import { QueryClient } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'
import { workItemQueryKeys } from './work-query-keys'
import { workKeys } from '@/features/work/api'

const T = 'tenant-A'
const OTHER = 'tenant-B'
const ITEM = 'work-1'
const PARAMS = { status: 'active', limit: 100 }

describe('neutral WorkItem query keys', () => {
  it('produces the exact literal arrays the Work feature has always used', () => {
    expect(workItemQueryKeys.collection(T)).toEqual(['work', T, 'items'])
    expect(workItemQueryKeys.detail(T, ITEM)).toEqual(['work', T, 'item', ITEM])
    expect(workItemQueryKeys.collection(null)).toEqual(['work', null, 'items'])
  })

  it('keeps workKeys byte-for-byte equivalent in serialized shape', () => {
    // The literals on the right are the pre-correction bytes, written out rather
    // than read from the module, so a change to either side fails this.
    expect(JSON.stringify(workKeys.items(T, PARAMS))).toBe(
      JSON.stringify(['work', T, 'items', PARAMS]),
    )
    expect(JSON.stringify(workKeys.item(T, ITEM))).toBe(
      JSON.stringify(['work', T, 'item', ITEM]),
    )
    // …and the collection prefix really is a prefix of the parameterised key.
    expect(workKeys.items(T, PARAMS).slice(0, 3)).toEqual(
      workItemQueryKeys.collection(T),
    )
  })

  it('invalidates only the intended tenant and family through a real QueryClient', async () => {
    const qc = new QueryClient()
    const seed = (key: readonly unknown[]) => qc.setQueryData(key, 'seeded')
    seed(workKeys.items(T, PARAMS))
    seed(workKeys.items(T, { limit: 1 }))
    seed(workKeys.item(T, ITEM))
    seed(workKeys.item(T, 'work-2'))
    seed(workKeys.lease(T, ITEM))
    seed(workKeys.events(T, ITEM))
    seed(workKeys.decisions(T, { view: 'history', limit: 10 }))
    seed(workKeys.items(OTHER, PARAMS))

    await qc.invalidateQueries({
      queryKey: workItemQueryKeys.collection(T),
    })
    await qc.invalidateQueries({ queryKey: workItemQueryKeys.detail(T, ITEM) })

    const invalidated = (key: readonly unknown[]) =>
      qc.getQueryState(key)?.isInvalidated === true

    expect(invalidated(workKeys.items(T, PARAMS))).toBe(true)
    expect(invalidated(workKeys.items(T, { limit: 1 }))).toBe(true)
    expect(invalidated(workKeys.item(T, ITEM))).toBe(true)

    // Untouched: another item, the sibling families and the other tenant.
    expect(invalidated(workKeys.item(T, 'work-2'))).toBe(false)
    expect(invalidated(workKeys.lease(T, ITEM))).toBe(false)
    expect(invalidated(workKeys.events(T, ITEM))).toBe(false)
    expect(
      invalidated(workKeys.decisions(T, { view: 'history', limit: 10 })),
    ).toBe(false)
    expect(invalidated(workKeys.items(OTHER, PARAMS))).toBe(false)
  })
})
