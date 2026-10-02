// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The sidebar's Approvals count: nothing while the queue is unread or empty, the page's size
// otherwise, and "+" when the server holds more than the page.
import { describe, expect, it } from 'vitest'
import { pendingCount } from './use-pending-approvals'

describe('pendingCount', () => {
  it('draws no count while the queue is unread or empty', () => {
    expect(pendingCount(undefined)).toBeUndefined()
    expect(pendingCount({ items: [], has_more: false })).toBeUndefined()
    expect(pendingCount({ items: [] })).toBeUndefined()
  })

  it('counts the page, and says when there are more', () => {
    expect(pendingCount({ items: [1, 2, 3], has_more: false })).toBe('3')
    expect(pendingCount({ items: [1, 2], has_more: true })).toBe('2+')
  })
})
