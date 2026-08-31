// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import './i18n'

const { api, authState } = vi.hoisted(() => ({
  api: {
    nhiLifecycle: vi.fn(),
    nhiPosture: vi.fn(),
    nhiDetail: vi.fn(),
    nhiEvents: vi.fn(),
    setNhiOwnership: vi.fn(),
    setNhiPolicy: vi.fn(),
    rotateNhi: vi.fn(),
    offboardNhi: vi.fn(),
    finalizeNhi: vi.fn(),
    restoreNhi: vi.fn(),
    sweepNhi: vi.fn(),
  },
  authState: {
    activeTenant: 'tenant-a' as string | null,
    principal: { aal: 3, amr: ['webauthn'] } as {
      aal?: number
      amr?: string[]
    } | null,
    can: (_permission: string) => true,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, identityApi: { ...actual.identityApi, ...api } }
})

import { NhiActions } from './nhi-actions'
import { NhiLifecycleTab } from './nhi-lifecycle'
import type { NhiLifecycleDTO } from './types'

const baseIdentity: NhiLifecycleDTO = {
  identity_ref: 'vault:approle:ci',
  source: 'vault',
  criticality: 'critical',
  owner_ref: 'human:owner',
  sponsor_ref: 'human:sponsor',
  rotated_at: '2026-07-01T00:00:00Z',
  max_age_seconds: 86400,
  rotation_target: 'approle:ci',
  staleness_status: 'stale',
  enforcement: 'blocked',
  orphaned: false,
  offboard_state: 'none',
}
const OWNERSHIP_TRIM_CONTRACT =
  'NHI_OWNERSHIP_TRIM_CONTRACT: surrounding whitespace must neither create a change nor bypass clear refusal'

function wrap(ui: ReactElement) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>,
  )
}

beforeEach(() => {
  authState.principal = { aal: 3, amr: ['webauthn'] }
  api.nhiPosture.mockResolvedValue({
    total: 12,
    rotation_known: 10,
    stale: 4,
    blocked: 2,
    alerting: 3,
    orphaned: 1,
    soft_deleted: 1,
    finalized: 1,
    critical: 5,
  })
  api.nhiLifecycle.mockResolvedValue({
    items: [
      baseIdentity,
      {
        ...baseIdentity,
        identity_ref: 'spiffe:worker',
        criticality: 'medium',
        staleness_status: 'ok',
        enforcement: 'monitor',
      },
    ],
    has_more: false,
  })
  api.nhiEvents.mockResolvedValue({ items: [] })
  api.nhiDetail.mockResolvedValue(baseIdentity)
  api.setNhiOwnership.mockResolvedValue(undefined)
  api.setNhiPolicy.mockResolvedValue(undefined)
  api.rotateNhi.mockResolvedValue({ status: 'done' })
  api.offboardNhi.mockResolvedValue({ status: 'done' })
  api.finalizeNhi.mockResolvedValue({ status: 'done' })
  api.restoreNhi.mockResolvedValue({ status: 'done' })
  api.sweepNhi.mockResolvedValue({
    scanned: 2,
    registered: 0,
    stale: 1,
    blocked: 1,
    orphaned: 0,
    unsponsored: 0,
  })
})

afterEach(() => {
  vi.clearAllMocks()
})

describe('NHI lifecycle posture and filtering', () => {
  it('renders the posture aggregate and filters the table by enforcement', async () => {
    const user = userEvent.setup()
    wrap(<NhiLifecycleTab />)

    const posture = await screen.findByText('Lifecycle posture')
    const card = (posture.closest('.rounded-lg') ??
      posture.parentElement!) as HTMLElement
    await within(card).findByText('12')
    expect(within(card).getByText('12')).toBeInTheDocument()
    expect(within(card).getByText('4')).toBeInTheDocument()
    expect(within(card).getByText('2')).toBeInTheDocument()
    expect(within(card).getByText('3')).toBeInTheDocument()
    expect(within(card).getByText('1')).toBeInTheDocument()
    expect(within(card).getByText('5')).toBeInTheDocument()

    expect(screen.getByText('vault:approle:ci')).toBeInTheDocument()
    expect(screen.getByText('spiffe:worker')).toBeInTheDocument()
    // Enforcement filters on the SERVER (handleListNHI parses enforcement=), so
    // the filter must travel in the request rather than hiding rows from a
    // single page the browser happens to hold — otherwise identities beyond
    // that page silently disappear while the posture tiles keep counting them.
    await user.click(
      screen.getByRole('combobox', { name: 'Filter by enforcement' }),
    )
    await user.click(screen.getByRole('option', { name: 'Blocked' }))
    await waitFor(() =>
      expect(api.nhiLifecycle).toHaveBeenCalledWith(
        expect.objectContaining({ enforcement: 'blocked' }),
      ),
    )
  })

  it('says so when the server has more identities than the page it returned', async () => {
    api.nhiLifecycle.mockResolvedValue({
      items: [baseIdentity],
      has_more: true,
    })
    wrap(<NhiLifecycleTab />)
    // The count must never imply completeness the response did not claim.
    expect(
      await screen.findByText(/more match this filter than are listed/i),
    ).toBeInTheDocument()
  })

  it('renders a calm not-authorized state for a 403, never a generic error', async () => {
    api.nhiPosture.mockRejectedValue(
      new ApiError(403, 'forbidden', 'permission denied'),
    )
    api.nhiLifecycle.mockRejectedValue(
      new ApiError(403, 'forbidden', 'permission denied'),
    )
    wrap(<NhiLifecycleTab />)
    expect(await screen.findByText('Not authorized')).toBeInTheDocument()
    expect(screen.queryByText('Server error')).not.toBeInTheDocument()
  })
})

describe('NHI lifecycle action honesty', () => {
  it('declares dual control and AAL3 before rotate, then shows the credential only once', async () => {
    const user = userEvent.setup()
    api.rotateNhi.mockResolvedValue({
      status: 'done',
      new_secret: 'one-time-secret-value',
      new_credential_ref: 'credential:new',
    })
    wrap(<NhiActions identity={baseIdentity} />)

    await user.click(screen.getByRole('button', { name: 'Rotate' }))
    const requestDialog = screen.getByRole('dialog')
    expect(
      within(requestDialog).getByText(
        /two distinct user accounts, each over an AAL3 session/i,
      ),
    ).toBeInTheDocument()
    // Rotation is CRITICAL but break-glass CAN authorize it (the server passes
    // AllowBreakGlass true), so the dialog must say so rather than promising
    // that a second human is structurally unavoidable — a false reassurance
    // about an emergency bypass is worse than none.
    expect(
      within(requestDialog).getByText(/break-glass grant can authorize it/i),
    ).toBeInTheDocument()
    expect(
      within(requestDialog).queryByText(/break-glass cannot authorize/i),
    ).not.toBeInTheDocument()
    expect(api.rotateNhi).not.toHaveBeenCalled()

    await user.click(
      within(requestDialog).getByRole('button', { name: 'Request rotation' }),
    )
    const credentialDialog = await screen.findByRole('dialog')
    expect(
      within(credentialDialog).getByText(/shown once.*cannot be recovered/i),
    ).toBeInTheDocument()
    expect(
      within(credentialDialog).getByDisplayValue('one-time-secret-value'),
    ).toBeInTheDocument()

    await user.click(
      within(credentialDialog).getByRole('button', {
        name: 'I have stored it securely',
      }),
    )
    await user.click(screen.getByRole('button', { name: 'Rotate' }))
    expect(
      screen.queryByDisplayValue('one-time-secret-value'),
    ).not.toBeInTheDocument()
  })

  it('warns that finalize is irreversible and excludes break-glass, confirms it, and explains a missing soft-delete', async () => {
    const user = userEvent.setup()
    api.finalizeNhi.mockRejectedValue(
      new ApiError(409, 'conflict', 'soft-delete required'),
    )
    wrap(
      <NhiActions
        identity={{ ...baseIdentity, offboard_state: 'soft_deleted' }}
      />,
    )

    await user.click(screen.getByRole('button', { name: 'Finalize' }))
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText(/irreversible/i)).toBeInTheDocument()
    expect(
      within(dialog).getByText(/break-glass does not apply/i),
    ).toBeInTheDocument()
    expect(
      within(dialog).getByText(
        /two distinct user accounts, each over an AAL3 session/i,
      ),
    ).toBeInTheDocument()
    const confirm = within(dialog).getByRole('button', {
      name: 'Finalize permanently',
    })
    expect(confirm).toBeDisabled()
    await user.type(
      within(dialog).getByLabelText('Confirmation phrase'),
      baseIdentity.identity_ref,
    )
    expect(confirm).toBeEnabled()
    await user.click(confirm)
    expect(
      await within(dialog).findByText(/requires a prior soft-delete/i),
    ).toBeInTheDocument()
  })

  it('explains that ownership references must resolve to human identities after a 400', async () => {
    const user = userEvent.setup()
    api.setNhiOwnership.mockRejectedValue(
      new ApiError(400, 'bad_request', 'owner is not human'),
    )
    wrap(<NhiActions identity={baseIdentity} />)

    await user.click(screen.getByRole('button', { name: 'Assign ownership' }))
    const dialog = screen.getByRole('dialog')
    const owner = within(dialog).getByLabelText('Owner identity reference')
    await user.clear(owner)
    await user.type(owner, 'service-account:not-human')
    await user.click(
      within(dialog).getByRole('button', { name: 'Save ownership' }),
    )
    await waitFor(() =>
      expect(
        api.setNhiOwnership,
        'NHI_OWNERSHIP_CHANGED_ONLY_CONTRACT',
      ).toHaveBeenCalledWith(baseIdentity.identity_ref, {
        owner_ref: 'service-account:not-human',
        sponsor_ref: '',
      }),
    )
    expect(
      await within(dialog).findByText(
        /must be human identities from the roster/i,
      ),
    ).toBeInTheDocument()
  })

  it('preserves the mandatory sponsor on an agent owner-only update', async () => {
    const user = userEvent.setup()
    wrap(<NhiActions identity={{ ...baseIdentity, kind: 'agent' }} />)

    await user.click(screen.getByRole('button', { name: 'Assign ownership' }))
    const dialog = screen.getByRole('dialog')
    const owner = within(dialog).getByLabelText('Owner identity reference')
    await user.clear(owner)
    await user.type(owner, 'human:new-owner')
    await user.click(
      within(dialog).getByRole('button', { name: 'Save ownership' }),
    )

    await waitFor(() =>
      expect(
        api.setNhiOwnership,
        'NHI_AGENT_SPONSOR_CONTRACT',
      ).toHaveBeenCalledWith(baseIdentity.identity_ref, {
        owner_ref: 'human:new-owner',
        // The handler rejects an empty sponsor_ref for kind=agent even when
        // the stored sponsor is unchanged; owner-only must carry it forward.
        sponsor_ref: 'human:sponsor',
      }),
    )
  })

  it('deny-closes an agent owner update when no sponsor exists to preserve', async () => {
    const user = userEvent.setup()
    wrap(
      <NhiActions
        identity={{ ...baseIdentity, kind: 'agent', sponsor_ref: undefined }}
      />,
    )

    await user.click(screen.getByRole('button', { name: 'Assign ownership' }))
    const dialog = screen.getByRole('dialog')
    const owner = within(dialog).getByLabelText('Owner identity reference')
    await user.clear(owner)
    await user.type(owner, 'human:new-owner')
    const confirm = within(dialog).getByRole('button', {
      name: 'Save ownership',
    })
    const witness = 'NHI_AGENT_SPONSOR_CONTRACT'
    expect(confirm, witness).toBeDisabled()
    expect(
      within(dialog).getByText(
        'Owner and sponsor references cannot be cleared here. Assign another human identity instead.',
      ),
      witness,
    ).toBeInTheDocument()
    await user.click(confirm)
    expect(api.setNhiOwnership, witness).not.toHaveBeenCalled()
  })

  it('keeps a no-op ownership update disabled and explains why', async () => {
    const user = userEvent.setup()
    wrap(<NhiActions identity={baseIdentity} />)

    await user.click(screen.getByRole('button', { name: 'Assign ownership' }))
    const dialog = screen.getByRole('dialog')
    const confirm = within(dialog).getByRole('button', {
      name: 'Save ownership',
    })
    const witness = 'NHI_OWNERSHIP_NOOP_CONTRACT'
    expect(confirm, witness).toBeDisabled()
    expect(
      within(dialog).getByText('Change the owner or sponsor before saving.'),
      witness,
    ).toBeInTheDocument()
    await user.click(confirm)
    expect(api.setNhiOwnership, witness).not.toHaveBeenCalled()
  })

  it('normalizes surrounding owner whitespace before detecting a change', async () => {
    const user = userEvent.setup()
    wrap(<NhiActions identity={baseIdentity} />)

    await user.click(screen.getByRole('button', { name: 'Assign ownership' }))
    const dialog = screen.getByRole('dialog')
    await user.type(
      within(dialog).getByLabelText('Owner identity reference'),
      '   ',
    )
    const confirm = within(dialog).getByRole('button', {
      name: 'Save ownership',
    })
    expect(confirm, OWNERSHIP_TRIM_CONTRACT).toBeDisabled()
    await user.click(confirm)
    expect(api.setNhiOwnership, OWNERSHIP_TRIM_CONTRACT).not.toHaveBeenCalled()
  })

  it.each([
    ['Owner identity reference', 'human:owner'],
    ['Sponsor identity reference', 'human:sponsor'],
  ])(
    'blocks clearing %s instead of sending an ambiguous empty ref',
    async (label) => {
      const user = userEvent.setup()
      wrap(<NhiActions identity={baseIdentity} />)

      await user.click(screen.getByRole('button', { name: 'Assign ownership' }))
      const dialog = screen.getByRole('dialog')
      await user.clear(within(dialog).getByLabelText(label))
      const confirm = within(dialog).getByRole('button', {
        name: 'Save ownership',
      })
      const witness = 'NHI_OWNERSHIP_CLEAR_CONTRACT'
      expect(confirm, witness).toBeDisabled()
      expect(
        within(dialog).getByText(
          'Owner and sponsor references cannot be cleared here. Assign another human identity instead.',
        ),
        witness,
      ).toBeInTheDocument()
      await user.click(confirm)
      expect(api.setNhiOwnership, witness).not.toHaveBeenCalled()
    },
  )

  it('treats a whitespace-only sponsor as an attempted clear', async () => {
    const user = userEvent.setup()
    wrap(<NhiActions identity={baseIdentity} />)

    await user.click(screen.getByRole('button', { name: 'Assign ownership' }))
    const dialog = screen.getByRole('dialog')
    const sponsor = within(dialog).getByLabelText('Sponsor identity reference')
    await user.clear(sponsor)
    await user.type(sponsor, '   ')
    const confirm = within(dialog).getByRole('button', {
      name: 'Save ownership',
    })
    expect(confirm, OWNERSHIP_TRIM_CONTRACT).toBeDisabled()
    await user.click(confirm)
    expect(api.setNhiOwnership, OWNERSHIP_TRIM_CONTRACT).not.toHaveBeenCalled()
  })
})
