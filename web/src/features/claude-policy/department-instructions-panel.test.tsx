// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A department's instructions file is rendered by the engine from what the department carries:
// the panel previews that document and publishes it, and never sends a document of its own.
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

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

const policyApi = vi.hoisted(() => ({
  dryRun: vi.fn(),
  publish: vi.fn(),
  listVersions: vi.fn(),
}))
vi.mock('@/features/claude-policy/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/claude-policy/api')>()
  return { ...actual, claudePolicyApi: policyApi }
})

const consoleApiMock = vi.hoisted(() => ({ listWorkspaces: vi.fn() }))
vi.mock('@/features/console/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/console/api')>()
  return { ...actual, consoleApi: consoleApiMock }
})

import { ApiError } from '@/lib/api/errors'
import { DepartmentInstructionsPanel } from './department-instructions-panel'
import './i18n'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  consoleApiMock.listWorkspaces.mockResolvedValue({
    items: [
      { id: 'ws1', name: 'Engineering', slug: 'eng', status: 'active' },
      { id: 'ws2', name: 'Old team', slug: 'old', status: 'inactive' },
    ],
    has_more: false,
  })
  policyApi.listVersions.mockResolvedValue({ items: [], has_more: false })
})

describe('DepartmentInstructionsPanel', () => {
  it('previews the file the engine renders and publishes it without a document', async () => {
    policyApi.dryRun.mockResolvedValue({
      surface: 'managed-instructions@eng',
      content: '# Org standards\n\nUse tabs.\n',
      sha256: 'abc123',
      notes: [],
    })
    policyApi.publish.mockResolvedValue({
      surface: 'managed-instructions@eng',
      revision: 1,
    })
    const user = userEvent.setup()
    wrap(<DepartmentInstructionsPanel />)

    await user.click(
      await screen.findByRole('combobox', { name: /department/i }),
    )
    expect(
      screen.queryByRole('option', { name: 'Old team' }),
    ).not.toBeInTheDocument()
    await user.click(await screen.findByRole('option', { name: 'Engineering' }))

    await user.click(screen.getByRole('button', { name: /preview/i }))
    await waitFor(() =>
      expect(policyApi.dryRun).toHaveBeenCalledWith(
        'managed-instructions@eng',
        '',
      ),
    )
    expect(await screen.findByText(/Use tabs\./)).toBeInTheDocument()
    expect(screen.getByText(/abc123/)).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /^publish$/i }))
    await waitFor(() =>
      expect(policyApi.publish).toHaveBeenCalledWith(
        'managed-instructions@eng',
        '',
      ),
    )
  })

  it('names the Business boundary when this edition does not serve the surface', async () => {
    policyApi.listVersions.mockRejectedValue(
      new ApiError(501, 'not_implemented', 'not in this edition'),
    )
    const user = userEvent.setup()
    wrap(<DepartmentInstructionsPanel />)

    await user.click(
      await screen.findByRole('combobox', { name: /department/i }),
    )
    await user.click(await screen.findByRole('option', { name: 'Engineering' }))

    expect(await screen.findByRole('status')).toHaveTextContent(/business/i)
    expect(
      screen.queryByRole('button', { name: /^publish$/i }),
    ).not.toBeInTheDocument()
    expect(screen.queryByText(/0 revisions/i)).not.toBeInTheDocument()
  })

  it('says why a department has nothing to preview', async () => {
    policyApi.dryRun.mockRejectedValue(
      new Error('document is invalid: department eng carries no instructions'),
    )
    const user = userEvent.setup()
    wrap(<DepartmentInstructionsPanel />)

    await user.click(
      await screen.findByRole('combobox', { name: /department/i }),
    )
    await user.click(await screen.findByRole('option', { name: 'Engineering' }))
    await user.click(screen.getByRole('button', { name: /preview/i }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        expect.stringMatching(/could not preview/i),
        expect.objectContaining({
          description: expect.stringContaining('carries no instructions'),
        }),
      ),
    )
  })
})
