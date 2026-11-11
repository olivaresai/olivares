// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The fleet policy routes (publish, versions, distribution) are Business: a
// Community server mounts them and answers 501, not "the backend is not built".
// These cells pin that boundary on the panel itself.
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  info: vi.fn(),
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

const BUSINESS =
  'Editing custom policies, roles and grants requires Olivares AI Business.'
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

// AN ANSWER THAT NAMES THE EDITION is said by the one edition line, naming it whole; the
// sentence above stays for a 501 that names none.
describe('a fleet policy route refused by edition', () => {
  it('a 501 business_required on publish shows the edition line', async () => {
    api.publish.mockRejectedValue(
      new ApiError(
        501,
        'business_required',
        'Fleet-wide managed settings require Business.',
      ),
    )
    wrap(panel())

    await userEvent.click(screen.getByRole('button', { name: /^Publish$/i }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(
      within(dialog).getByRole('button', { name: /^Publish$/i }),
    )

    // The panel is wider than the publish route it refused: the line names neither.
    expect(
      await screen.findByText(/This is part of Olivares AI Business\./),
    ).toBeInTheDocument()
    expect(screen.queryByText(/Managed policy is part of/)).toBeNull()
    expect(screen.queryByText(BUSINESS)).not.toBeInTheDocument()
    expect(document.body).not.toHaveTextContent(
      /business_required|\b501\b|claude-policy/,
    )
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('a 403 edition_required on versions shows the edition line, never «not authorized»', async () => {
    api.listVersions.mockRejectedValue(
      new ApiError(
        403,
        'edition_required',
        'Managed policy files is part of Olivares AI Engineer.',
        undefined,
        { edition: 'engineer', capability: 'claude-policy' },
      ),
    )
    wrap(panel())

    await userEvent.click(screen.getByRole('tab', { name: /Versions/i }))

    expect(
      await screen.findByText(
        /Managed policy is part of Olivares AI Engineer\./,
      ),
    ).toBeInTheDocument()
    expect(screen.queryByText('Not authorized')).not.toBeInTheDocument()
    expect(document.body).not.toHaveTextContent(
      /edition_required|\b403\b|claude-policy/,
    )
  })
})

// A DRY RUN THE ENGINE REFUSES BY EDITION is the calm edition line, never «Something went
// wrong» in red; any other failure keeps that title and the engine's sentence.
describe('a dry run that fails', () => {
  it('refused by edition: the edition line, calm', async () => {
    api.dryRun.mockRejectedValue(
      new ApiError(403, 'edition_required', 'raw: 403', undefined, {
        edition: 'engineer',
        capability: 'claude-policy',
      }),
    )
    wrap(panel())

    await userEvent.click(screen.getByRole('button', { name: /^Dry-run$/i }))

    await waitFor(() => expect(toast.info).toHaveBeenCalledOnce())
    const { container } = render(<>{toast.info.mock.calls[0][0]}</>)
    expect(container).toHaveTextContent(
      'Managed policy is part of Olivares AI Engineer.',
    )
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('any other failure keeps its title and the engine’s sentence', async () => {
    api.dryRun.mockRejectedValue(
      new ApiError(400, 'invalid', 'document is not valid JSON'),
    )
    wrap(panel())

    await userEvent.click(screen.getByRole('button', { name: /^Dry-run$/i }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        expect.stringMatching(/something went wrong/i),
        { description: 'document is not valid JSON' },
      ),
    )
    expect(toast.info).not.toHaveBeenCalled()
  })
})
