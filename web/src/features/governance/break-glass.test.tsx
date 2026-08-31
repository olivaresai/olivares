// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import type { BreakGlassDTO } from './types'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

const authState = vi.hoisted(() => ({
  activeTenant: 'tenant-one' as string | null,
  can: (_permission: string): boolean => true,
  principal: {
    actor: 'user:reviewer',
    kind: 'user',
    user_id: 'reviewer',
    aal: 3,
  } as {
    actor: string
    kind: 'user' | 'token'
    user_id: string
    aal: number
  } | null,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  listBreakGlass: vi.fn(),
  activateBreakGlass: vi.fn(),
  getBreakGlass: vi.fn(),
  listBreakGlassUses: vi.fn(),
  revokeBreakGlass: vi.fn(),
  reviewBreakGlass: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, governanceApi: api }
})

import { BreakGlassView } from './break-glass'

function wrap(ui: ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
  return render(
    <QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>,
  )
}

const activeGrant: BreakGlassDTO = {
  id: 'bg-active',
  match_action: 'deploy.release',
  reason: 'Restore the production service',
  activated_by: 'user:operator',
  status: 'active',
  activated_at: '2099-01-01T00:00:00Z',
  expires_at: '2099-01-01T01:00:00Z',
  use_count: 0,
  reviewed: false,
}

const closedGrant: BreakGlassDTO = {
  ...activeGrant,
  id: 'bg-closed',
  status: 'revoked',
  activated_by: 'user:activator',
  revoked_at: '2099-01-01T00:30:00Z',
}

beforeEach(() => {
  authState.can = () => true
  authState.principal = {
    actor: 'user:reviewer',
    kind: 'user',
    user_id: 'reviewer',
    aal: 3,
  }
  for (const fn of Object.values(api)) fn.mockReset()
  toast.success.mockReset()
  toast.error.mockReset()
  toast.warning.mockReset()
  api.listBreakGlass.mockResolvedValue({ items: [], has_more: false })
  api.getBreakGlass.mockResolvedValue(closedGrant)
  api.listBreakGlassUses.mockResolvedValue({ items: [], has_more: false })
})

afterEach(() => vi.clearAllMocks())

describe('BreakGlassView', () => {
  it('renders active grants with their expiry and exact action scope', async () => {
    api.listBreakGlass.mockResolvedValue({
      items: [activeGrant],
      has_more: false,
    })

    const { container } = wrap(<BreakGlassView />)

    expect(
      await screen.findByText('Active — dual-control bypassed'),
    ).toBeInTheDocument()
    expect(screen.getAllByText('Exact: deploy.release').length).toBeGreaterThan(
      0,
    )
    expect(screen.getAllByText('user:operator').length).toBeGreaterThan(0)
    expect(
      container.querySelectorAll('time[datetime="2099-01-01T01:00:00Z"]')
        .length,
    ).toBeGreaterThan(0)
  })

  it('requires a reason and clamps activation duration to the hard bounds', async () => {
    api.activateBreakGlass.mockResolvedValue(activeGrant)
    wrap(<BreakGlassView />)

    const activate = await screen.findByRole('button', {
      name: 'Activate emergency access',
    })
    await waitFor(() => expect(activate).toBeEnabled())
    await userEvent.click(activate)
    const dialog = await screen.findByRole('dialog')
    const submit = within(dialog).getByRole('button', {
      name: 'Activate emergency access',
    })
    const duration = within(dialog).getByLabelText(/^Duration \(seconds\)/)

    expect(submit).toBeDisabled()
    expect(duration).toHaveValue(3600)
    expect(duration).toHaveAttribute('min', '1')
    expect(duration).toHaveAttribute('max', '86400')

    await userEvent.type(
      within(dialog).getByLabelText(/^Justification/),
      'Database recovery cannot wait for quorum',
    )
    expect(submit).toBeEnabled()

    await userEvent.clear(duration)
    await userEvent.type(duration, '90000')
    expect(submit).toBeDisabled()
    await userEvent.tab()
    expect(duration).toHaveValue(86400)
    expect(submit).toBeEnabled()

    await userEvent.click(submit)
    await waitFor(() => expect(api.activateBreakGlass).toHaveBeenCalledTimes(1))
    expect(api.activateBreakGlass).toHaveBeenCalledWith({
      reason: 'Database recovery cannot wait for quorum',
      expires_in_seconds: 86400,
    })
  })

  it('explains separation of duties when the server rejects a self-review', async () => {
    api.listBreakGlass.mockResolvedValue({
      items: [closedGrant],
      has_more: false,
    })
    // The server sends a CODE for this refusal; the console must route on it and
    // not on the prose, so the message here is deliberately worded differently
    // from the server's — a client that still matched text would fail this.
    api.reviewBreakGlass.mockRejectedValue(
      new ApiError(
        403,
        'separation_of_duty',
        'a different reviewer is required for this grant',
      ),
    )
    wrap(<BreakGlassView />)

    await userEvent.click(
      await screen.findByRole('button', {
        name: 'Inspect break-glass grant bg-closed',
      }),
    )
    await userEvent.type(
      await screen.findByLabelText(/^Review note/),
      'The emergency action was justified and evidence was inspected',
    )
    await userEvent.click(
      screen.getByRole('button', { name: 'Complete post-review' }),
    )

    const confirmation = await screen.findByRole('dialog', {
      name: 'Sign off this post-review?',
    })
    await userEvent.click(
      within(confirmation).getByRole('button', {
        name: 'Record post-review',
      }),
    )

    expect(
      await within(confirmation).findByText(
        /do not retry; ask a different user account/i,
      ),
    ).toBeInTheDocument()
    expect(toast.error).not.toHaveBeenCalled()
    expect(toast.warning).toHaveBeenCalled()
  })

  it('highlights a used, unreviewed grant as pending post-review', async () => {
    api.listBreakGlass.mockResolvedValue({
      items: [{ ...closedGrant, use_count: 2, reviewed: false }],
      has_more: false,
    })
    wrap(<BreakGlassView />)

    const warning = await screen.findByText('Used and not reviewed')
    const row = warning.closest('tr')
    expect(row).toHaveAttribute('data-review-pending', 'true')
    expect(row).toHaveClass('bg-warning-soft/60')
  })

  it('renders a calm not-authorized state for a server 403', async () => {
    api.listBreakGlass.mockRejectedValue(
      new ApiError(403, 'forbidden', 'not permitted'),
    )
    wrap(<BreakGlassView />)

    expect(await screen.findByText('Not authorized')).toBeInTheDocument()
    expect(
      screen.getByText(
        'You do not have permission to view break-glass grants or their immutable use trails.',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByText(/something went wrong/i)).not.toBeInTheDocument()
  })
})
