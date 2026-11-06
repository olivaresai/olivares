// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import axe from 'axe-core'
import i18n from 'i18next'
import type { QueryClient } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  createTestQueryClient,
  renderIntel,
  screen,
  within,
} from '@/test/intel'
import { ApiError } from '@/lib/api/errors'
import '@/features/_intel'

const mocks = vi.hoisted(() => ({
  listMembers: vi.fn(),
  listSuperadmins: vi.fn(),
  listTokens: vi.fn(),
  listApprovals: vi.fn(),
  killState: vi.fn(),
  listProviders: vi.fn(),
  admissionPolicy: vi.fn(),
  userStatus: vi.fn(),
  epoch: 1,
  auth: {
    activeTenant: 't1' as string | null,
    can: (_permission: string, _options?: unknown): boolean => true,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => mocks.auth }))
vi.mock('@/components/layout/step-up-state', () => ({
  StepUpRequiredState: () => <span>Step up required</span>,
}))
vi.mock('@tanstack/react-router', () => ({
  Link: ({
    to,
    children,
    className,
  }: {
    to: string
    children: ReactNode
    className?: string
  }) => (
    <a href={to} className={className}>
      {children}
    </a>
  ),
}))
vi.mock('@/features/console/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/console/api')>()
  return {
    ...actual,
    consoleApi: {
      ...actual.consoleApi,
      listMembers: mocks.listMembers,
      listSuperadmins: mocks.listSuperadmins,
      listTokens: mocks.listTokens,
    },
  }
})
vi.mock('@/features/governance/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/governance/api')>()
  return {
    ...actual,
    governanceApi: {
      ...actual.governanceApi,
      listApprovals: mocks.listApprovals,
    },
  }
})
vi.mock('@/features/killswitch/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/killswitch/api')>()
  return {
    ...actual,
    killswitchApi: { ...actual.killswitchApi, state: mocks.killState },
  }
})
vi.mock('@/features/providers/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/providers/api')>()
  return {
    ...actual,
    providersApi: { ...actual.providersApi, list: mocks.listProviders },
  }
})
vi.mock('@/features/agentops/auth-boundary', () => ({
  useAuthBoundary: () => ({ epoch: mocks.epoch }),
}))
vi.mock('@/features/models/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/models/api')>()
  return {
    ...actual,
    modelsApi: {
      ...actual.modelsApi,
      admissionPolicy: mocks.admissionPolicy,
    },
  }
})
vi.mock('@/features/identity/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/identity/api')>()
  return {
    ...actual,
    totpApi: { ...actual.totpApi, userStatus: mocks.userStatus },
  }
})

import { SecurityPostureOverview } from './posture-overview'
import './i18n'

function renderOverview(queryClient?: QueryClient) {
  return renderIntel(
    <main>
      <SecurityPostureOverview />
    </main>,
    { queryClient },
  )
}

function row(name: string | RegExp) {
  return within(screen.getByRole('listitem', { name }))
}

beforeEach(async () => {
  vi.clearAllMocks()
  await i18n.changeLanguage('en')
  mocks.epoch = 1
  mocks.auth.activeTenant = 't1'
  mocks.auth.can = () => true
  mocks.listMembers.mockResolvedValue({
    items: [
      {
        user_id: 'm1',
        role: 'owner',
        status: 'active',
        second_factor_enrolled: true,
      },
      {
        user_id: 'm2',
        role: 'viewer',
        status: 'active',
        second_factor_enrolled: false,
      },
      {
        user_id: 'm3',
        role: 'admin',
        status: 'inactive',
        second_factor_enrolled: false,
      },
    ],
    has_more: false,
  })
  mocks.listSuperadmins.mockResolvedValue({
    items: [{ id: 's1', status: 'active', is_superadmin: true }],
    has_more: false,
  })
  mocks.userStatus.mockImplementation((userID: string) =>
    Promise.resolve({
      enrolled: userID === 'm1',
      recovery_codes_remaining: userID === 'm1' ? 10 : 0,
    }),
  )
  mocks.listTokens.mockResolvedValue({
    items: [
      { id: 'k1', revoked: false, created_at: '2026-01-01T00:00:00Z' },
      {
        id: 'k2',
        revoked: false,
        expires_at: '2999-01-01T00:00:00Z',
        last_used_at: '2026-10-01T00:00:00Z',
        created_at: '2026-01-01T00:00:00Z',
      },
      { id: 'k3', revoked: true, created_at: '2026-01-01T00:00:00Z' },
      {
        id: 'k4',
        revoked: false,
        expires_at: '2020-01-01T00:00:00Z',
        created_at: '2026-01-01T00:00:00Z',
      },
    ],
    has_more: false,
  })
  mocks.listApprovals.mockResolvedValue({
    items: [{ id: 'a1' }, { id: 'a2' }],
    has_more: true,
  })
  mocks.killState.mockResolvedValue({
    estate_stopped: true,
    active: [{ scope_kind: 'agent' }],
  })
  mocks.listProviders.mockResolvedValue({
    items: [
      {
        provider_ref: 'p1',
        kind: 'anthropic',
        state: 'active',
        key_hint: '…ABCD',
      },
      {
        provider_ref: 'p2',
        kind: 'anthropic',
        state: 'revoked',
        key_hint: '…WXYZ',
      },
      { provider_ref: 'p3', kind: 'ollama', state: 'active' },
    ],
    has_more: false,
  })
  mocks.admissionPolicy.mockResolvedValue({
    configured: true,
    require_signed: false,
    require_artifact_digests: false,
  })
})

describe('SecurityPostureOverview', () => {
  it('answers every exposure row from existing reads and links each remedy', async () => {
    renderOverview()

    expect(
      await row(/People without a second factor/).findByText('1'),
    ).toBeInTheDocument()
    expect(await row(/Administrators/).findByText('1')).toBeInTheDocument()
    expect(
      await row(/API keys/).findByText('1 no expiry · 2 unused'),
    ).toBeInTheDocument()
    expect(await row(/Pending approvals/).findByText('2+')).toBeInTheDocument()
    expect(
      await row(/Kill switch/).findByText('Estate stopped'),
    ).toBeInTheDocument()
    expect(
      await row(/Providers with a key/).findByText('1'),
    ).toBeInTheDocument()
    expect(await row(/Policy mode/).findByText('Observe')).toBeInTheDocument()
    expect(mocks.userStatus).not.toHaveBeenCalled()
    expect(mocks.listSuperadmins).not.toHaveBeenCalled()

    const links = [
      '/identity?tab=people',
      '/identity?tab=apiKeys',
      '/permissions?tab=approvals',
      '/killswitch',
      '/providers',
      '/model-operations?tab=admission',
    ]
    for (const href of links) {
      expect(
        screen
          .getAllByRole('link', { name: /^Review/ })
          .some((link) => link.getAttribute('href') === href),
      ).toBe(true)
    }
  })

  it('shows not available instead of zero when the person cannot read the facts', async () => {
    mocks.auth.can = () => false
    renderOverview()

    expect(await screen.findAllByText('Not available')).toHaveLength(7)
    expect(screen.queryByText('0')).not.toBeInTheDocument()
    expect(mocks.listMembers).not.toHaveBeenCalled()
    expect(mocks.listTokens).not.toHaveBeenCalled()
    expect(mocks.listApprovals).not.toHaveBeenCalled()
    expect(mocks.killState).not.toHaveBeenCalled()
    expect(mocks.listProviders).not.toHaveBeenCalled()
    expect(mocks.admissionPolicy).not.toHaveBeenCalled()
  })

  it('maps a forbidden read to not available without painting retained data', async () => {
    mocks.listTokens.mockRejectedValue(
      new ApiError(403, 'forbidden', 'Token reads are not permitted'),
    )
    renderOverview()

    expect(
      await row(/API keys/).findByText('Not available'),
    ).toBeInTheDocument()
    expect(row(/API keys/).queryByText('0')).not.toBeInTheDocument()
  })

  it('keeps retry and request context on a failed read, never a zero', async () => {
    mocks.listTokens.mockRejectedValue(new Error('offline'))
    renderOverview()

    expect(
      await row(/API keys/).findByRole('button', { name: /retry/i }),
    ).toBeInTheDocument()
    expect(row(/API keys/).queryByText('0')).not.toBeInTheDocument()
  })

  it('shows loading rather than a zero while a read is pending', async () => {
    mocks.listTokens.mockReturnValue(new Promise(() => {}))
    renderOverview()

    expect(await row(/API keys/).findByText('Loading…')).toBeInTheDocument()
    expect(row(/API keys/).queryByText(/\d+ no expiry/)).not.toBeInTheDocument()
  })

  it('uses each row permission independently', async () => {
    mocks.auth.can = (permission: string) => permission !== 'token:read'
    renderOverview()

    expect(
      await row(/API keys/).findByText('Not available'),
    ).toBeInTheDocument()
    expect(await row(/Administrators/).findByText('1')).toBeInTheDocument()
    expect(mocks.listTokens).not.toHaveBeenCalled()
    expect(mocks.listMembers).toHaveBeenCalled()
  })

  it('shows every tenant-scoped row unavailable with no active tenant', async () => {
    mocks.auth.activeTenant = null
    renderOverview()

    expect(await screen.findAllByText('Not available')).toHaveLength(7)
    expect(mocks.listMembers).not.toHaveBeenCalled()
    expect(mocks.listTokens).not.toHaveBeenCalled()
    expect(mocks.listApprovals).not.toHaveBeenCalled()
    expect(mocks.killState).not.toHaveBeenCalled()
    expect(mocks.listProviders).not.toHaveBeenCalled()
    expect(mocks.admissionPolicy).not.toHaveBeenCalled()
  })

  it('keeps a step-up demand distinct from missing permission', async () => {
    mocks.listTokens.mockRejectedValue(
      new ApiError(403, 'step_up_required', 'Step up required'),
    )
    renderOverview()

    expect(
      await row(/API keys/).findByText('Step up required'),
    ).toBeInTheDocument()
    expect(row(/API keys/).queryByText('Not available')).not.toBeInTheDocument()
    expect(row(/API keys/).queryByText('0')).not.toBeInTheDocument()
  })

  it('does not paint an earlier credential answer while the next one loads', async () => {
    const queryClient = createTestQueryClient()
    const view = renderOverview(queryClient)
    await row(/API keys/).findByText('1 no expiry · 2 unused')

    mocks.epoch = 2
    mocks.listTokens.mockReturnValue(new Promise(() => {}))
    view.rerender(
      <main>
        <SecurityPostureOverview />
      </main>,
    )

    expect(await row(/API keys/).findByText('Loading…')).toBeInTheDocument()
    expect(row(/API keys/).queryByText(/\d+ no expiry/)).not.toBeInTheDocument()
  })

  it('shows agent-only stops and enforcing policy', async () => {
    mocks.killState.mockResolvedValue({
      estate_stopped: false,
      active: [{ scope_kind: 'agent' }, { scope_kind: 'agent' }],
    })
    mocks.admissionPolicy.mockResolvedValue({
      configured: true,
      require_signed: true,
      require_artifact_digests: true,
    })
    renderOverview()

    expect(
      await row(/Kill switch/).findByText('2 agents stopped'),
    ).toBeInTheDocument()
    expect(row(/Policy mode/).getByText('Enforce')).toBeInTheDocument()
  })

  it('shows no stops and an unconfigured policy', async () => {
    mocks.killState.mockResolvedValue({ estate_stopped: false, active: [] })
    mocks.admissionPolicy.mockResolvedValue({
      configured: false,
      require_signed: false,
      require_artifact_digests: false,
    })
    renderOverview()

    expect(
      await row(/Kill switch/).findByText('Not engaged'),
    ).toBeInTheDocument()
    expect(row(/Policy mode/).getByText('Not configured')).toBeInTheDocument()
  })

  it('renders a genuine empty approval queue as zero', async () => {
    mocks.listApprovals.mockResolvedValue({ items: [], has_more: false })
    renderOverview()

    expect(await row(/Pending approvals/).findByText('0')).toBeInTheDocument()
  })

  it('renders localized labels, values and error-free unavailable copy', async () => {
    await i18n.changeLanguage('es')
    renderOverview()

    expect(
      await screen.findByText('Personas sin un segundo factor'),
    ).toBeInTheDocument()
    expect(
      await row(/Personas sin un segundo factor/).findByText('1'),
    ).toBeInTheDocument()
    expect(
      await row(/Claves API/).findByText('1 sin caducidad · 2 sin uso'),
    ).toBeInTheDocument()
    expect(screen.getAllByRole('link', { name: /^Revisar/ })).toHaveLength(7)
  })

  it('has no accessibility violations', async () => {
    const { container } = renderOverview()
    await row(/Policy mode/).findByText('Observe')

    const results = await axe.run(container)
    expect(results.violations).toEqual([])
  })
})
