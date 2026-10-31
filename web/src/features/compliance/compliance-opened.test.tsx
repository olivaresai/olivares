// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// acceptance: Now shows the compliance score only after the person opened Compliance, per
// organization and user.
import { preferenceStorage } from '@/lib/preference-storage'
import { renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  useComplianceOpened,
  useMarkComplianceOpened,
} from './compliance-opened'

const auth = vi.hoisted(() => ({
  principal: { kind: 'user', user_id: 'u-1' } as Record<string, unknown> | null,
  activeTenant: 't-1' as string | null,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

afterEach(() => {
  preferenceStorage.clear()
  auth.principal = { kind: 'user', user_id: 'u-1' }
  auth.activeTenant = 't-1'
})

describe('whether Compliance was opened', () => {
  it('is false until the Compliance page marks it, then true', () => {
    expect(renderHook(() => useComplianceOpened()).result.current).toBe(false)
    renderHook(() => useMarkComplianceOpened())
    expect(renderHook(() => useComplianceOpened()).result.current).toBe(true)
  })

  it('is per organization and per person', () => {
    renderHook(() => useMarkComplianceOpened())
    auth.activeTenant = 't-2'
    expect(renderHook(() => useComplianceOpened()).result.current).toBe(false)
    auth.activeTenant = 't-1'
    auth.principal = { kind: 'user', user_id: 'u-2' }
    expect(renderHook(() => useComplianceOpened()).result.current).toBe(false)
  })

  it('records nothing for a token principal', () => {
    auth.principal = { kind: 'token', token_id: 'tk' }
    renderHook(() => useMarkComplianceOpened())
    expect(preferenceStorage.length).toBe(0)
    expect(renderHook(() => useComplianceOpened()).result.current).toBe(false)
  })
})
