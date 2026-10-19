// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The inbound SCIM server is a Business capability: a Community server answers every
// /v1/scim/v2 read with 501. The Identity view says so once, in plain words, instead
// of two "backend pending" notices and a line that promises inbound group provisioning.
// The leaver evidence below it still reads the audit ledger, which Community fills when
// a person is removed from the organization.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState, build } = vi.hoisted(() => ({
  build: { community: true as boolean | undefined },
  api: {
    ssoStatus: vi.fn(),
    scimServiceProviderConfig: vi.fn(),
    scimUsers: vi.fn(),
    audit: vi.fn(),
  },
  authState: {
    activeTenant: 't1' as string | null,
    principal: null,
    can: (_permission: string) => true,
  },
}))

vi.mock('@tanstack/react-router', () => ({ useNavigate: () => vi.fn() }))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/lib/hooks/use-server-info', () => ({
  useCommunityBuild: () => build.community,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, identityApi: api }
})

// Import AFTER the mocks are declared.
import { ApiError } from '@/lib/api/errors'
import { FederationTab } from './federation'

const business = () =>
  new ApiError(
    501,
    'not_implemented',
    'The inbound SCIM server (users, groups and event push) requires Business.',
  )

function mount(ui: React.ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  api.ssoStatus.mockRejectedValue(new Error('not under test'))
  api.scimServiceProviderConfig.mockRejectedValue(business())
  api.scimUsers.mockRejectedValue(business())
  api.audit.mockResolvedValue({
    items: [
      {
        id: 'ev_1',
        seq: 1,
        action: 'scim.user.deprovision',
        target_id: 'usr_1',
        occurred_at: '2026-06-07T00:00:00Z',
      },
    ],
    has_more: false,
    next_from: 2,
    head_seq: 1,
  })
})

afterEach(() => {
  vi.clearAllMocks()
  build.community = true
})

describe('SCIM card on a Community server', () => {
  it('asks no SCIM route: a Community build is not asked for what it refuses', async () => {
    mount(<FederationTab />)

    expect(
      await screen.findByText(/inbound SCIM server requires Business/i),
    ).toBeInTheDocument()
    expect(api.scimServiceProviderConfig).not.toHaveBeenCalled()
    expect(api.scimUsers).not.toHaveBeenCalled()
  })

  it('asks no SCIM route while server-info has not said which build this is', async () => {
    build.community = undefined
    mount(<FederationTab />)

    expect(await screen.findByText('usr_1')).toBeInTheDocument()
    expect(api.scimServiceProviderConfig).not.toHaveBeenCalled()
    expect(api.scimUsers).not.toHaveBeenCalled()
    expect(
      screen.queryByText(/inbound SCIM server requires Business/i),
    ).not.toBeInTheDocument()
    // Users are not read yet, so the table is loading: "No users" would say they were read and none exist.
    expect(screen.queryByText('No users synchronized')).not.toBeInTheDocument()
  })

  it('names Business once and keeps the leaver evidence', async () => {
    mount(<FederationTab />)

    expect(
      await screen.findByText(/inbound SCIM server requires Business/i),
    ).toBeInTheDocument()
    // The two "backend pending" seams and the inbound-groups promise are gone.
    expect(
      screen.queryByText(/SCIM service-provider configuration/i),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByText(/Group provisioning is supported inbound/i),
    ).not.toBeInTheDocument()
    // The leaver evidence is the ledger's, not SCIM's: a removal in People writes it.
    expect(await screen.findByText('usr_1')).toBeInTheDocument()
  })

  it('does not call a different failure a Business refusal', async () => {
    build.community = false
    api.scimServiceProviderConfig.mockRejectedValue(
      new ApiError(500, 'internal_error', 'boom'),
    )
    mount(<FederationTab />)

    expect(await screen.findByText('usr_1')).toBeInTheDocument()
    expect(
      screen.queryByText(/inbound SCIM server requires Business/i),
    ).not.toBeInTheDocument()
  })
})
