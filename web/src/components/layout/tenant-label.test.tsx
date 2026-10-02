// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { renderHook, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createTestQueryClient } from '@/test/intel'
import { ApiError } from '@/lib/api/errors'
import { systemApi } from '@/lib/api/endpoints'

const auth = vi.hoisted(() => ({
  activeTenant: '01a0f9a5-0000-7000-8000-000000000001',
  isSuperadmin: true,
  grants: [] as { tenant: string; tenant_name?: string }[],
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

import { useTenantLabel } from './tenant-label'

beforeEach(() => {
  vi.restoreAllMocks()
  auth.isSuperadmin = true
  auth.grants = []
})

describe('the organization label (N2 J8)', () => {
  it('a deployment that does not serve the organization list is asked once, and the label keeps the short id', async () => {
    // R1 08 with Postgres: GET /v1/system/orgs answered 501 on every screen.
    const list = vi
      .spyOn(systemApi, 'listOrgs')
      .mockRejectedValue(new ApiError(501, 'not_implemented', 'not served'))
    const client = createTestQueryClient()
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    )
    const first = renderHook(() => useTenantLabel(), { wrapper })
    await waitFor(() => expect(list).toHaveBeenCalledTimes(1))
    expect(first.result.current).toEqual({
      tenant: '01a0f9a5-0000-7000-8000-000000000001',
      name: '01a0f9a5…',
      named: false,
    })
    first.unmount()
    // The next screen mounts the label again: no second request.
    renderHook(() => useTenantLabel(), { wrapper })
    await new Promise((r) => setTimeout(r, 50))
    expect(list).toHaveBeenCalledTimes(1)
  })

  it('names the organization where the list is served', async () => {
    vi.spyOn(systemApi, 'listOrgs').mockResolvedValue({
      items: [
        {
          tenant_id: '01a0f9a5-0000-7000-8000-000000000001',
          name: 'Default Organization',
        },
      ],
      has_more: false,
    } as unknown as Awaited<ReturnType<typeof systemApi.listOrgs>>)
    const client = createTestQueryClient()
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    )
    const { result } = renderHook(() => useTenantLabel(), { wrapper })
    await waitFor(() =>
      expect(result.current.name).toBe('Default Organization'),
    )
    expect(result.current.named).toBe(true)
  })

  it('names the organization from the whoami grant when the list is not served (ARCH e69ad18a)', async () => {
    vi.spyOn(systemApi, 'listOrgs').mockRejectedValue(
      new ApiError(501, 'not_implemented', 'not served'),
    )
    auth.grants = [
      {
        tenant: '01a0f9a5-0000-7000-8000-000000000001',
        tenant_name: 'Default Organization',
      },
    ]
    const client = createTestQueryClient()
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    )
    const { result } = renderHook(() => useTenantLabel(), { wrapper })
    expect(result.current).toEqual({
      tenant: '01a0f9a5-0000-7000-8000-000000000001',
      name: 'Default Organization',
      named: true,
    })
  })
})
