// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// "Get started" is the FIRST screen a customer is asked to use, and three of its five
// steps are privileged: they render `RequireAssurance`, which translates from the
// `identity` namespace. The wizard's chunk never imported it, so steps 2, 3 and 4
// showed `assurance.stepUpTitle`, `assurance.stepUpBody` and a button reading
// `assurance.authenticate` — the operator was blocked on his first task by a screen
// that would not say why.
//
// Why onboarding.test.tsx never saw it: it mocks `@/features/identity/assurance` with
// a pass-through, so the panel it should have been reading never rendered. This file
// runs the REAL gate at AAL1 (a password session — the state of every first boot) and
// asserts on the DOM the operator actually gets.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { expectNoRawI18nKeys } from '@/test/i18n-keys'
// No `import './i18n'` and no hand-registered `identity` bundle: every namespace this
// file needs must come from the modules the view itself pulls in. Compensating for a
// missing registration in the test is exactly how the defect stayed invisible.

const { consoleApi, policyApi, authState } = vi.hoisted(() => ({
  consoleApi: {
    setupStatus: vi.fn(),
    listWorkspaces: vi.fn(),
    listMembers: vi.fn(),
    listSources: vi.fn(),
    listConnectors: vi.fn(),
    createWorkspace: vi.fn(),
    onboard: vi.fn(),
    testConnector: vi.fn(),
    putConnector: vi.fn(),
  },
  policyApi: { getDistribution: vi.fn(), getVersion: vi.fn() },
  // A freshly installed operator: superadmin, but a PASSWORD session (AAL1). That is
  // what makes the step-up panel — and therefore the identity namespace — render.
  authState: {
    can: (_p: string): boolean => true,
    principal: { aal: 1, amr: ['pwd'] },
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
// NOTE: `@/features/identity/assurance` is deliberately NOT mocked.
vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({ to, children }: { to: string; children: ReactNode }) => (
    <a href={to}>{children}</a>
  ),
}))
vi.mock('@/features/console/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/console/api')>()
  return { ...actual, consoleApi }
})
vi.mock('@/features/claude-policy/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/claude-policy/api')>()
  return { ...actual, claudePolicyApi: policyApi }
})

import { OnboardingView } from './onboarding-view'

const defaultWs = {
  id: 'w0',
  tenant_id: 't1',
  name: 'Default',
  slug: 'default',
  status: 'active',
  is_default: true,
  created_at: '',
  updated_at: '',
  version: 1,
}

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  try {
    localStorage.removeItem('olivares.onboarding.dismissed')
  } catch {
    /* ignore */
  }
  consoleApi.setupStatus.mockResolvedValue({
    completed: false,
    steps: [
      { id: 'database', completed: true },
      { id: 'connectors', completed: false },
      { id: 'identity', completed: false },
      { id: 'users', completed: false },
    ],
  })
  consoleApi.listWorkspaces.mockResolvedValue({ items: [defaultWs] })
  consoleApi.listMembers.mockResolvedValue({ items: [{ user_id: 'u0' }] })
  consoleApi.listSources.mockResolvedValue({ sources: [] })
  consoleApi.listConnectors.mockResolvedValue({ connectors: [] })
  policyApi.getDistribution.mockResolvedValue({
    surface: 'managed-settings',
    scopes: [],
  })
})

describe('OnboardingView first-boot i18n', () => {
  it('shows the REAL step-up copy on the privileged steps, never the key', async () => {
    wrap(<OnboardingView />)
    await screen.findByText(/1 of 5 verified/i)

    // Steps 2 (workspace), 3 (source) and 4 (people) each gate their form.
    const titles = await screen.findAllByText('Step-up authentication required')
    expect(titles.length).toBeGreaterThanOrEqual(3)
    expect(
      screen.getAllByRole('button', { name: 'Authenticate with security key' }),
    ).toHaveLength(titles.length)
    // The interpolated fragments resolve too (the body is the "why", the AAL labels
    // are the "what is missing") — a lost namespace prints the key for each.
    expect(
      screen.getAllByText(/requires AAL3 \(hardware, phishing-resistant\)/)
        .length,
    ).toBe(titles.length)
    expect(screen.queryByText(/^assurance\./)).not.toBeInTheDocument()
  })

  it('renders the whole wizard with no unresolved key anywhere in the DOM', async () => {
    const { container } = wrap(<OnboardingView />)
    // Settle on the progress counter — a string from the wizard's OWN namespace, so
    // the wait cannot depend on the namespace under scrutiny. Once the steps are on
    // screen the step-up panels are too (they render with their step, synchronously),
    // and the sweep below is then the ONLY assertion: it must be what goes red.
    await screen.findByText(/1 of 5 verified/i)
    await waitFor(() =>
      expect(screen.getAllByRole('button').length).toBeGreaterThan(3),
    )

    // `managed-settings` is the PEP surface identifier, printed on purpose.
    expectNoRawI18nKeys(container, ['managed-settings'])
  })
})
