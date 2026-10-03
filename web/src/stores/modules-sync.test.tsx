// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ServerInfo } from '@/lib/api/types'

let info: Partial<ServerInfo> | undefined
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => ({ data: info }),
}))
const get = vi.fn()
vi.mock('@/lib/api/client', () => ({
  http: { get: (...args: unknown[]) => get(...args) },
}))

import { useModulesStore, useSyncModulesNotEnabled } from './modules'

beforeEach(() => get.mockReset())
afterEach(() => useModulesStore.getState().setOff([]))

describe('the communication plane readiness comes from server-info', () => {
  it('a staged plane hides its screens and the console asks no route about it', () => {
    info = { modules_not_enabled: ['finops'], communication_ready: false }
    renderHook(() => useSyncModulesNotEnabled())
    expect([...useModulesStore.getState().off].sort()).toEqual([
      'communication',
      'finops',
    ])
    expect(get).not.toHaveBeenCalled()
  })

  it('an effective plane shows its screens', () => {
    info = { communication_ready: true }
    renderHook(() => useSyncModulesNotEnabled())
    expect(useModulesStore.getState().off.has('communication')).toBe(false)
    expect(get).not.toHaveBeenCalled()
  })

  it('before server-info answers, nothing is hidden yet', () => {
    info = undefined
    renderHook(() => useSyncModulesNotEnabled())
    expect(useModulesStore.getState().off.size).toBe(0)
  })
})
