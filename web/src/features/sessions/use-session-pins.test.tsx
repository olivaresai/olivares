// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The pin is written once, also under StrictMode, which runs state updaters twice.
import { act, renderHook } from '@testing-library/react'
import { StrictMode, type ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { pinStorageKey } from './session-pins'
import { useSessionPins } from './use-session-pins'

const auth = vi.hoisted(() => ({
  principal: { kind: 'user', user_id: 'u-1' },
  activeTenant: 't-1',
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

const strict = ({ children }: { children: ReactNode }) => (
  <StrictMode>{children}</StrictMode>
)

afterEach(() => window.localStorage.clear())

describe('useSessionPins', () => {
  // WEB2 09b capture r4: on the development build the pin showed and storage kept [].
  it('stores the pin under StrictMode, and a second toggle removes it', () => {
    const key = pinStorageKey(
      window.location.origin,
      auth.principal as never,
      auth.activeTenant,
    )!
    const { result } = renderHook(() => useSessionPins(), { wrapper: strict })
    act(() => result.current.toggle!('live:lr-a'))
    expect(result.current.pinned.has('live:lr-a')).toBe(true)
    expect(JSON.parse(window.localStorage.getItem(key)!).pins).toEqual([
      'live:lr-a',
    ])
    act(() => result.current.toggle!('live:lr-a'))
    expect(result.current.pinned.has('live:lr-a')).toBe(false)
    expect(JSON.parse(window.localStorage.getItem(key)!).pins).toEqual([])
  })
})
