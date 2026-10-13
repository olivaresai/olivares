// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { renderHook } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { createTestQueryClient } from '@/test/intel'
import { beforeEach, expect, it, vi } from 'vitest'
import { systemApi } from '@/lib/api/endpoints'
import { useTenantLabel } from './tenant-label'

const auth = vi.hoisted(() => ({
  activeTenant: '01a0f9a5-0000-7000-8000-000000000001' as string | null,
  isSuperadmin: true,
  grants: [] as { tenant: string; tenant_name?: string }[],
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))
vi.mock('@/features/extensions', () => ({ PANEL_EXTENSIONS: {} }))

const client = createTestQueryClient()
function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

beforeEach(() => {
  client.clear()
  vi.restoreAllMocks()
  auth.activeTenant = '01a0f9a5-0000-7000-8000-000000000001'
  auth.grants = []
})

it('uses a short id without requesting the estate, even for a superadmin', () => {
  const list = vi
    .spyOn(systemApi, 'listOrgs')
    .mockResolvedValue({ items: [], has_more: false })
  const { result } = renderHook(() => useTenantLabel(), { wrapper })
  expect(result.current.name).toBe('01a0f9a5…')
  expect(result.current.named).toBe(false)
  expect(list).not.toHaveBeenCalled()
})

it('names the default organization from the principal’s own grant', () => {
  auth.grants = [
    { tenant: auth.activeTenant!, tenant_name: 'Default Organization' },
  ]
  const { result } = renderHook(() => useTenantLabel(), { wrapper })
  expect(result.current.name).toBe('Default Organization')
  expect(result.current.named).toBe(true)
})

it('has no label without an active tenant', () => {
  auth.activeTenant = null
  const { result } = renderHook(() => useTenantLabel(), { wrapper })
  expect(result.current).toEqual({ tenant: null, name: null, named: false })
})
