// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SHELL READS THE ACCOUNTS ONCE, so every surface that names a session can wear the
// account's dot and label. Every surface test hands the badge its accounts directly; this
// is the one that fails when the layout stops mounting the provider.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import type { ComponentProps, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const auth = vi.hoisted(() => ({
  status: 'authenticated',
  activeTenant: 't1' as string | null,
  grants: [] as { tenant: string }[],
  principal: { user_id: 'root', aal: 1 },
  can: (_permission: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({
    select,
  }: {
    select: (s: {
      location: { pathname: string; href: string }
      resolvedLocation: { href: string } | undefined
    }) => unknown
  }) =>
    select({
      location: { pathname: '/sessions', href: '/sessions' },
      resolvedLocation: { href: '/sessions' },
    }),
  useNavigate: () => vi.fn(),
  useRouter: () => ({
    state: {
      location: { href: '/sessions' },
      resolvedLocation: { href: '/sessions' },
    },
  }),
  // The routed page: one badge, for the account behind a profile.
  Outlet: () => <AccountBadge profileRef="ppf_b" />,
  Link: ({ children, to, ...props }: ComponentProps<'a'> & { to?: string }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => ({
    isPending: false,
    data: { setup_required: false },
  }),
}))
vi.mock('./sidebar', () => ({
  Sidebar: () => null,
  NavigationDrawer: () => null,
  PhoneNavigation: () => null,
}))
vi.mock('./topbar', () => ({ Topbar: () => <header data-slot="topbar" /> }))
vi.mock('./status-bar', () => ({ StatusBar: () => null }))
vi.mock('./title-gate', () => ({
  TitleGate: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('./command-menu', () => ({ CommandMenu: () => null }))
vi.mock('./shortcuts', () => ({ GlobalShortcuts: () => null }))
vi.mock('./session-notifications', () => ({ SessionNotifications: () => null }))
vi.mock('./destination-sections', () => ({ DestinationSections: () => null }))
vi.mock('./tenant-gate', () => ({
  TenantGate: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('@/features/first-hour/new-session-host', () => ({
  NewSessionHost: () => null,
}))
vi.mock('@/features/navigation/permitted-visit', () => ({
  SettingsVisit: () => null,
}))
vi.mock('@/features/navigation/personal-navigation', () => ({
  usePersonalNavigation: () => null,
  PersonalNavigationProvider: ({ children }: { children: ReactNode }) => (
    <>{children}</>
  ),
}))

import { AccountBadge } from '@/features/agent-tools/account-marks'
import { agentOpsApi } from '@/features/agentops/api'
import type { ProviderAccountDTO } from '@/features/agentops/types'
import { AppLayout } from './app-layout'

const account: ProviderAccountDTO = {
  account_ref: 'ppf_b',
  name: 'claude-b',
  display_name: 'Research',
  accent: 'teal',
  driver: 'claude',
  environment_ref: 'xenv_1',
  state: 'active',
  home_mode: 'managed',
  home_generation: 1,
  home_relative: '',
  isolation_level: 'shared',
  auth_source: 'provider_account_home',
  identity: '',
  identity_source: 'none',
  created_at: '2026-10-01T10:00:00Z',
  updated_at: '2026-10-01T10:00:00Z',
}

beforeEach(() => {
  vi.restoreAllMocks()
  auth.activeTenant = 't1'
  auth.can = () => true
  vi.spyOn(agentOpsApi, 'listAccounts').mockResolvedValue({
    items: [account],
    has_more: false,
  })
  vi.spyOn(agentOpsApi, 'listProfiles').mockResolvedValue({
    items: [],
    has_more: false,
  })
})

function renderLayout() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <AppLayout />
    </QueryClientProvider>,
  )
}

describe('the shell and the accounts', () => {
  it('lets a routed page wear an account with its dot and label', async () => {
    const { container } = renderLayout()
    expect(await screen.findByText('Research')).toBeInTheDocument()
    expect(
      container.querySelector('[data-provider-accent="teal"]'),
    ).not.toBeNull()
  })

  it('reads no accounts while no organization is active', async () => {
    auth.activeTenant = null
    renderLayout()
    await new Promise((resolve) => setTimeout(resolve, 30))
    expect(agentOpsApi.listAccounts).not.toHaveBeenCalled()
    expect(screen.queryByText('Research')).toBeNull()
  })
})
