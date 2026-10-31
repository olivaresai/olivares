// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { act, renderHook } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { expect, it, vi } from 'vitest'
import { AuthProvider, useAuth } from './context'
import { authApi } from '@/lib/api/endpoints'
import { savedViewsApi } from '@/features/saved-views/api'
import { useClientSettings } from '@/features/settings/preferences'
import { useSessionStore } from '@/stores/session'
import type { Whoami } from '@/lib/api/types'

it('loads the user start page before session adoption completes in a fresh browser', async () => {
  useSessionStore.getState().clear()
  useClientSettings.setState({ startPage: 'home' })
  vi.spyOn(authApi, 'whoami').mockResolvedValue({
    kind: 'user',
    user_id: 'person',
    grants: [{ tenant: 'tenant', role: 'viewer' }],
  } as Whoami)
  vi.spyOn(savedViewsApi, 'uiState').mockResolvedValue({
    stored: true,
    preferences: {
      'olivares.settings': JSON.stringify({ state: { startPage: 'sessions' } }),
    },
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const view = renderHook(() => useAuth(), {
    wrapper: ({ children }) => (
      <QueryClientProvider client={client}>
        <AuthProvider>{children}</AuthProvider>
      </QueryClientProvider>
    ),
  })
  try {
    await act(async () => {
      await view.result.current.adoptSession({
        csrf_token: 'synthetic-csrf',
        session_id: 'synthetic-session',
        expires_at: '',
      })
    })
    expect(useClientSettings.getState().startPage).toBe('sessions')
    expect(savedViewsApi.uiState).toHaveBeenCalledWith(
      expect.objectContaining({ tenant: 'tenant', sessionEffects: 'none' }),
    )
  } finally {
    view.unmount()
    client.clear()
    useSessionStore.getState().clear()
    vi.restoreAllMocks()
  }
})
