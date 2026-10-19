// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { renderHook } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { usePermissionWords } from './permission-words'

function words(permission: string): string {
  return renderHook(() => usePermissionWords()).result.current(permission)
}

describe('usePermissionWords', () => {
  it.each([
    ['notify:route:write', 'write access'],
    ['gitpublish:merge:admin', 'admin access'],
    ['accessmap:drift:read', 'read access'],
    ['tenant:admin', 'admin access'],
  ])('names %s by its level and never by its id', (permission, expected) => {
    expect(words(permission)).toBe(expected)
  })

  it('does not guess a level for a verb it does not know', () => {
    expect(words('voice:policy:destroy')).toBe('more access')
    // A kind that merely ends in a level word is still not that level.
    expect(words('wallet:readwrite')).toBe('more access')
  })
})
