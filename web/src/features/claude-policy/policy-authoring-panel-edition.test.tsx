// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The fleet policy routes (publish, versions, distribution) are Business: a
// Community server mounts them and answers 501, not "the backend is not built".
// These cells pin that boundary on the panel itself.
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

const authState = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  can: (_p: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  dryRun: vi.fn(),
  publish: vi.fn(),
  listVersions: vi.fn(),
  getDistribution: vi.fn(),
}))
vi.mock('@/features/claude-policy/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/claude-policy/api')>()
  return { ...actual, claudePolicyApi: api }
})

import { PolicyAuthoringPanel } from './policy-authoring-panel'
import './i18n'

const BUSINESS = 'Editing custom policies, roles and grants requires Business.'
const PENDING = /Backend pending/i

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

function panel() {
  return (
    <PolicyAuthoringPanel
      surface="managed-settings"
      defaultDoc="{}"
      reference={<div />}
    />
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = () => true
  api.listVersions.mockResolvedValue({ items: [], has_more: false })
  api.getDistribution.mockResolvedValue({
    surface: 'managed-settings',
    scopes: [],
    notes: [],
  })
})

describe('Community fleet policy routes', () => {
  it('a 501 on publish shows Business, not a pending backend', async () => {
    api.publish.mockRejectedValue(
      new ApiError(
        501,
        'not_implemented',
        'Fleet-wide managed settings require Business.',
      ),
    )
    wrap(panel())

    await userEvent.click(screen.getByRole('button', { name: /^Publish$/i }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(
      within(dialog).getByRole('button', { name: /^Publish$/i }),
    )

    expect(await screen.findByText(BUSINESS)).toHaveAttribute('role', 'status')
    expect(screen.queryByText(PENDING)).not.toBeInTheDocument()
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('a 501 on versions shows Business, not a pending backend', async () => {
    api.listVersions.mockRejectedValue(
      new ApiError(
        501,
        'not_implemented',
        'Fleet-wide managed settings require Business.',
      ),
    )
    wrap(panel())

    await userEvent.click(screen.getByRole('tab', { name: /Versions/i }))

    expect(await screen.findByText(BUSINESS)).toHaveAttribute('role', 'status')
    expect(screen.queryByText(PENDING)).not.toBeInTheDocument()
  })

  it('a 501 on distribution shows Business, not a pending backend', async () => {
    api.getDistribution.mockRejectedValue(
      new ApiError(
        501,
        'not_implemented',
        'Fleet-wide managed settings require Business.',
      ),
    )
    wrap(panel())

    await userEvent.click(screen.getByRole('tab', { name: /Distribution/i }))

    expect(await screen.findByText(BUSINESS)).toHaveAttribute('role', 'status')
    expect(screen.queryByText(PENDING)).not.toBeInTheDocument()
  })

  it('a 404 still renders the pending seam', async () => {
    api.listVersions.mockRejectedValue(
      new ApiError(404, 'not_found', 'no route'),
    )
    wrap(panel())

    await userEvent.click(screen.getByRole('tab', { name: /Versions/i }))

    expect(await screen.findByText(PENDING)).toBeInTheDocument()
    expect(screen.queryByText(BUSINESS)).not.toBeInTheDocument()
  })
})
