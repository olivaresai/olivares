// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The thesis test: a wizard step goes green ONLY when the authoritative
// backend read confirms it — never on the mutation's optimistic success. The
// workspace test seeds an empty roster, drives the real create call, and asserts the
// step verifies only after the re-fetched roster contains the new workspace.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

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
  policyApi: {
    getDistribution: vi.fn(),
    getVersion: vi.fn(),
  },
  authState: { can: (_p: string): boolean => true },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('@tanstack/react-router', () => ({
  //useUrlState follows the location, so the mock has to answer it.
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
  authState.can = () => true
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

describe('OnboardingView', () => {
  it('forbids a non-superadmin', () => {
    authState.can = () => false
    wrap(<OnboardingView />)
    expect(screen.queryByText(/get started/i)).not.toBeInTheDocument()
  })

  it('reflects real backend state in the progress counter', async () => {
    wrap(<OnboardingView />)
    // Only the database step is complete server-side → 1 of 5.
    expect(await screen.findByText(/1 of 5 verified/i)).toBeInTheDocument()
  })

  it('does NOT verify the source step for a registered-but-failed source', async () => {
    consoleApi.listSources.mockResolvedValue({
      sources: [
        {
          name: 'wiki',
          kind: 'confluence',
          status: 'failed',
          enabled: true,
          tenant: 't1',
        },
      ],
    })
    wrap(<OnboardingView />)
    // The database step is verified, but a registered source whose live status is
    // 'failed' is NOT counted — the badge stays honest, so progress is still 1 of 5.
    expect(await screen.findByText(/1 of 5 verified/i)).toBeInTheDocument()
    expect(
      await screen.findByText(/registered but not yet running/i),
    ).toBeInTheDocument()
  })

  it('verifies the workspace step ONLY after the re-fetched roster confirms it', async () => {
    const user = userEvent.setup()
    const roster: (typeof defaultWs)[] = [defaultWs]
    consoleApi.listWorkspaces.mockImplementation(() =>
      Promise.resolve({ items: [...roster] }),
    )
    // The create call succeeds AND mutates the roster the next read observes — the
    // step must not go green on the call alone, only on that observed roster.
    consoleApi.createWorkspace.mockImplementation((input: { slug: string }) => {
      roster.push({
        ...defaultWs,
        id: 'w1',
        slug: input.slug,
        is_default: false,
      })
      return Promise.resolve(roster[roster.length - 1])
    })

    wrap(<OnboardingView />)
    expect(await screen.findByText(/1 of 5 verified/i)).toBeInTheDocument()

    await user.type(screen.getByLabelText(/workspace name/i), 'Platform')
    await user.click(screen.getByRole('button', { name: /create workspace/i }))

    // The real API was called with the entered name + derived slug.
    await waitFor(() =>
      expect(consoleApi.createWorkspace).toHaveBeenCalledWith({
        name: 'Platform',
        slug: 'platform',
      }),
    )
    // Progress advances to 2 of 5 — driven by the re-fetched roster, not the mutation.
    expect(await screen.findByText(/2 of 5 verified/i)).toBeInTheDocument()
  })

  it('shows the PEP step as active only when a host attested (distribution verified)', async () => {
    policyApi.getDistribution.mockResolvedValue({
      surface: 'managed-settings',
      latest_revision: 3,
      artifact: { revision: 3, artifact_sha256: 'aa', key_fingerprint: 'bb' },
      scopes: [
        {
          scope: 'ws:1',
          verified: true,
          current: true,
          reporter: 'agent-1',
          reported_revision: 3,
          content_reported: true,
        },
      ],
    })
    policyApi.getVersion.mockResolvedValue({ revision: 3, content: '{"x":1}' })

    wrap(<OnboardingView />)

    // The PEP step surfaces a "Verified (current)" host check-in from the real read.
    expect(await screen.findByText(/verified \(current\)/i)).toBeInTheDocument()
  })

  it('shows the PEP step pending with an author CTA when nothing is published', async () => {
    wrap(<OnboardingView />)
    expect(
      await screen.findByText(/no managed-settings policy is published/i),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('link', { name: /author a policy/i }),
    ).toBeInTheDocument()
  })

  // E4d: plugin kinds must not claim "no configuration" nor register empty.
  it('treats a plugin (out-of-process) kind honestly in the source step', async () => {
    // An out-of-process kind: the host knows no fields (fields_known=false). The old
    // wizard showed "needs no further configuration" and registered an empty config.
    consoleApi.listConnectors.mockResolvedValue({
      connectors: [
        { kind: 'claude', transport: 'plugin', fields_known: false },
      ],
    })
    consoleApi.putConnector.mockResolvedValue({
      name: 'cc',
      action: 'added',
      persisted: true,
      applied: true,
    })
    const user = userEvent.setup()
    wrap(<OnboardingView />)

    await user.click(
      await screen.findByRole('combobox', { name: /connector/i }),
    )
    await user.click(screen.getByRole('option', { name: 'claude' }))

    // The honest plugin note replaces the misleading "no further configuration".
    expect(await screen.findByText(/runs out-of-process/i)).toBeInTheDocument()
    expect(screen.queryByText(/needs no further configuration/i)).toBeNull()

    await user.type(screen.getByLabelText(/source name/i), 'cc')
    // Tenant may be prefilled from the store; set it explicitly.
    const tenant = screen.getByLabelText(/^tenant/i)
    await user.clear(tenant)
    await user.type(tenant, 'acme')

    // The host cannot Open a subprocess to probe it: Test stays disabled.
    expect(screen.getByRole('button', { name: /test/i })).toBeDisabled()

    // Free-form settings are the plugin kind's editor and reach the payload.
    await user.click(screen.getByRole('button', { name: /add setting/i }))
    await user.type(screen.getByLabelText(/^setting$/i), 'otlp_listen')
    await user.type(screen.getByLabelText(/^value$/i), ':4318')
    await user.click(screen.getByRole('button', { name: /register/i }))

    await waitFor(() =>
      expect(consoleApi.putConnector).toHaveBeenCalledWith(
        expect.objectContaining({
          kind: 'claude',
          name: 'cc',
          tenant: 'acme',
          config: { otlp_listen: ':4318' },
        }),
      ),
    )
  })
})
