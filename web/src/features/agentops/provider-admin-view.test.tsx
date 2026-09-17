// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The two doors of the provider-profile plane. Each opens on the tab its entrance
// names, each tab is offered on ITS read tier, and a principal who holds only one of
// the two tiers reads only that plane — no profile, run or live request leaves the
// browser for a binding-only reader, and no binding request for a profile-only one.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const auth = vi.hoisted(() => ({
  perms: new Set<string>(),
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (p: string) => auth.perms.has(p),
    isSuperadmin: false,
    principal: { user_id: 'u1', aal: 1 },
  }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))
const api = vi.hoisted(() => ({
  listProfiles: vi.fn(),
  listBindings: vi.fn(),
  listRuns: vi.fn(),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, agentOpsApi: { ...(real.agentOpsApi as object), ...api } }
})
vi.mock('@/features/console/api', () => ({
  consoleApi: { listSources: vi.fn() },
  consoleKeys: { sources: () => ['console', 'sources'] },
}))

import { ProviderAdminView } from './provider-admin-view'

const PR = 'sessions:profile:read'
const BR = 'sessions:profile-binding:read'

function wrap(entrance: 'profiles' | 'bindings') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <ProviderAdminView entrance={entrance} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  api.listProfiles.mockResolvedValue({ items: [], has_more: false })
  api.listBindings.mockResolvedValue({ items: [], has_more: false })
  api.listRuns.mockResolvedValue({ items: [], has_more: false })
})

describe('ProviderAdminView — two doors, one room', () => {
  it('the profiles door opens on the profiles tab under its own h1', async () => {
    auth.perms = new Set([PR, BR])
    wrap('profiles')
    expect(
      await screen.findByRole('heading', { name: 'Provider profiles' }),
    ).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Profiles' })).toHaveAttribute(
      'aria-selected',
      'true',
    )
    expect(screen.getByRole('tab', { name: 'Bindings' })).toHaveAttribute(
      'aria-selected',
      'false',
    )
    await waitFor(() => expect(api.listProfiles).toHaveBeenCalled())
  })

  it('the bindings door opens on the bindings tab under its own h1', async () => {
    auth.perms = new Set([PR, BR])
    wrap('bindings')
    expect(
      await screen.findByRole('heading', { name: 'Source bindings' }),
    ).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Bindings' })).toHaveAttribute(
      'aria-selected',
      'true',
    )
    await waitFor(() => expect(api.listBindings).toHaveBeenCalled())
  })

  it('a binding-only reader gets the bindings plane and asks for nothing else', async () => {
    auth.perms = new Set([BR])
    wrap('bindings')
    expect(
      await screen.findByRole('heading', { name: 'Source bindings' }),
    ).toBeInTheDocument()
    await waitFor(() => expect(api.listBindings).toHaveBeenCalledOnce())
    expect(
      screen.queryByRole('tab', { name: 'Profiles' }),
    ).not.toBeInTheDocument()
    expect(api.listProfiles).not.toHaveBeenCalled()
    expect(api.listRuns).not.toHaveBeenCalled()
    // No bind control either: that is the write tier, not the read one.
    expect(
      screen.queryByRole('button', { name: 'Bind source' }),
    ).not.toBeInTheDocument()
  })

  it('a profile-only reader gets the profiles plane and no binding request', async () => {
    auth.perms = new Set([PR])
    wrap('profiles')
    expect(
      await screen.findByRole('heading', { name: 'Provider profiles' }),
    ).toBeInTheDocument()
    await waitFor(() => expect(api.listProfiles).toHaveBeenCalledOnce())
    expect(
      screen.queryByRole('tab', { name: 'Bindings' }),
    ).not.toBeInTheDocument()
    expect(api.listBindings).not.toHaveBeenCalled()
  })

  it('an entrance whose tier is missing falls back to the tier the principal holds', async () => {
    auth.perms = new Set([BR])
    wrap('profiles')
    // The door is the profiles one (the route guard would normally refuse it); with
    // only the binding tier the room still shows what may be read, nothing else.
    await waitFor(() => expect(api.listBindings).toHaveBeenCalledOnce())
    expect(api.listProfiles).not.toHaveBeenCalled()
    expect(screen.getByRole('tab', { name: 'Bindings' })).toHaveAttribute(
      'aria-selected',
      'true',
    )
  })
})
