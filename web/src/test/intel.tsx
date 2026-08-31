// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Test helpers for the intelligence views. i18n is initialized globally by
// src/test/setup.ts, so components render real English copy. renderIntel wraps a
// subject in the providers the shared kit needs (TanStack Query for container
// views, a TooltipProvider for HashChip / IntegrityBadge tooltips). For RBAC, mock
// the auth module inline — see DEFAULT_AUTH for a ready value.
import type { ReactElement, ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, type RenderOptions } from '@testing-library/react'
import { TooltipProvider } from '@/components/ui/tooltip'

/** A query client that never retries — tests resolve/throw once, deterministically. */
export function createTestQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0, staleTime: 0 },
      mutations: { retry: false },
    },
  })
}

function Wrapper({
  children,
  queryClient,
}: {
  children: ReactNode
  queryClient: QueryClient
}) {
  return (
    <QueryClientProvider client={queryClient}>
      <TooltipProvider delayDuration={0}>{children}</TooltipProvider>
    </QueryClientProvider>
  )
}

export function renderIntel(
  ui: ReactElement,
  opts: { queryClient?: QueryClient } & Omit<RenderOptions, 'wrapper'> = {},
) {
  const { queryClient = createTestQueryClient(), ...rest } = opts
  return render(ui, {
    wrapper: ({ children }) => (
      <Wrapper queryClient={queryClient}>{children}</Wrapper>
    ),
    ...rest,
  })
}

/**
 * A ready auth value for `vi.mock('@/lib/auth/context', () => ({ useAuth: () => ... }))`.
 * `can` allows everything by default; pass `{ can: () => false }` to assert that a
 * write action is gated. Keep this inline-spreadable (no external refs) so it can be
 * used inside a hoisted vi.mock factory.
 */
export const DEFAULT_AUTH = {
  status: 'authenticated' as const,
  principal: { superadmin: true } as unknown,
  grants: [] as unknown[],
  activeTenant: 'demo',
  activeRole: 'owner',
  isSuperadmin: true,
  isAuthenticated: true,
  can: () => true,
  login: async () => {},
  logout: async () => {},
  setActiveTenant: () => {},
}

export * from '@testing-library/react'
export { default as userEvent } from '@testing-library/user-event'
