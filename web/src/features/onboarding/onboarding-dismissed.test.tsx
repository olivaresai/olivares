// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// E4b — a dismissed onboarding wizard must NOT render a blank page: the nav
// entry stays visible (registry-driven), so the route renders a reversible
// EmptyState whose action clears the persisted dismissal.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ can: () => true, activeTenant: 't1' }),
}))

vi.mock('@/stores/tenant', () => ({
  useTenantStore: (sel: (s: { activeTenant: string }) => unknown) =>
    sel({ activeTenant: 't1' }),
}))

vi.mock('@tanstack/react-router', () => ({
  //useUrlState follows the location, so the mock has to answer it.
  useRouterState: () => '',
  Link: ({ to, children, ...rest }: { to: string; children: ReactNode }) => (
    <a href={to} {...rest}>
      {children}
    </a>
  ),
}))

vi.mock('@/features/identity/assurance', () => ({
  AAL: { HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))

vi.mock('@/features/_intel', () => ({
  CaveatNotice: ({ children }: { children: ReactNode }) => <div>{children}</div>,
}))

vi.mock('@/lib/hooks/use-privileged-mutation', () => ({
  usePrivilegedMutation: () => ({ mutate: vi.fn(), isPending: false }),
}))

vi.mock('@/features/console/api', () => ({
  consoleApi: {
    setupStatus: vi.fn().mockResolvedValue({ steps: [] }),
    listWorkspaces: vi.fn().mockResolvedValue({ items: [] }),
    listMembers: vi.fn().mockResolvedValue({ items: [] }),
    listSources: vi.fn().mockResolvedValue({ sources: [] }),
    listConnectors: vi.fn().mockResolvedValue({ connectors: [] }),
    testConnector: vi.fn(),
    putConnector: vi.fn(),
    createWorkspace: vi.fn(),
    onboard: vi.fn(),
  },
  consoleKeys: {
    setupStatus: () => ['setup'],
    workspaces: (t: string | null) => ['ws', t],
    members: (t: string | null) => ['members', t],
    invites: (t: string | null) => ['invites', t],
    sources: () => ['sources'],
    connectors: () => ['connectors'],
  },
}))

vi.mock('@/features/claude-policy/api', () => ({
  claudePolicyApi: {
    getDistribution: vi.fn().mockResolvedValue({ scopes: [] }),
    getVersion: vi.fn(),
  },
}))

import { OnboardingView } from './onboarding-view'

const DISMISS_KEY = 'olivares.onboarding.dismissed'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  localStorage.clear()
})

describe('OnboardingView dismissed state', () => {
  it('renders a reversible empty state instead of a blank page', () => {
    localStorage.setItem(DISMISS_KEY, 'true')
    const { container } = wrap(<OnboardingView />)

    expect(container.textContent).not.toBe('')
    expect(screen.getByText(/setup guide dismissed/i)).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /resume setup guide/i }),
    ).toBeInTheDocument()
  })

  it('resume clears the persisted dismissal and shows the wizard again', async () => {
    localStorage.setItem(DISMISS_KEY, 'true')
    const user = userEvent.setup()
    wrap(<OnboardingView />)

    await user.click(
      screen.getByRole('button', { name: /resume setup guide/i }),
    )

    expect(localStorage.getItem(DISMISS_KEY)).toBeNull()
    expect(await screen.findByText('Get started')).toBeInTheDocument()
  })
})
