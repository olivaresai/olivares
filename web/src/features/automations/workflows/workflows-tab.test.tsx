// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'

const { api, authState } = vi.hoisted(() => ({
  api: {
    list: vi.fn(),
    create: vi.fn(),
    detail: vi.fn(),
    patch: vi.fn(),
    updateSteps: vi.fn(),
    revisions: vi.fn(),
    restore: vi.fn(),
    dryRun: vi.fn(),
    run: vi.fn(),
    runs: vi.fn(),
    runDetail: vi.fn(),
    schedules: vi.fn(),
    routes: vi.fn(),
  },
  authState: {
    activeTenant: 'tenant-a' as string | null,
    can: (_permission: string): boolean => true,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  workflowsApi: api,
}))

import { WorkflowsTab } from './workflows-tab'

const workflow = {
  id: 'workflow-1',
  name: 'Daily governance',
  description: 'Governed daily process',
  enabled: true,
  version: 3,
  step_count: 2,
  plan_hash: 'hash-1',
  owner_actor: 'admin',
  created_at: '2026-07-01T00:00:00Z',
  updated_at: '2026-07-02T00:00:00Z',
}

function wrap(ui: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = () => true
  api.list.mockResolvedValue({ items: [workflow], has_more: false })
  api.create.mockResolvedValue({ ...workflow, steps: [] })
  api.detail.mockResolvedValue({ ...workflow, steps: [] })
  api.schedules.mockResolvedValue({ items: [], has_more: false })
  api.routes.mockResolvedValue({ items: [], has_more: false })
  api.runs.mockResolvedValue({ items: [], has_more: false })
})

describe('WorkflowsTab', () => {
  it('renders the workflow list', async () => {
    wrap(<WorkflowsTab />)
    expect(await screen.findByText('Daily governance')).toBeInTheDocument()
    expect(screen.getAllByText('Enabled')).toHaveLength(2)
    expect(screen.getByText('3')).toBeInTheDocument()
  })

  it('renders the forbidden state for an API denial', async () => {
    api.list.mockRejectedValue(new ApiError(403, 'forbidden', 'denied'))
    wrap(<WorkflowsTab />)
    expect(
      await screen.findByText('You are not authorized to view workflows.'),
    ).toBeInTheDocument()
  })

  it('creates a workflow and opens its editor', async () => {
    const user = userEvent.setup()
    wrap(<WorkflowsTab />)
    await user.click(
      await screen.findByRole('button', { name: 'New workflow' }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText(/^Name/), 'Incident response')
    await user.type(
      within(dialog).getByLabelText('Description'),
      'Governed response path',
    )
    await user.click(
      within(dialog).getByRole('button', { name: 'Create workflow' }),
    )
    await waitFor(() => expect(api.create).toHaveBeenCalled())
    expect(api.create.mock.calls[0]?.[0]).toEqual({
      name: 'Incident response',
      description: 'Governed response path',
    })
    expect(await screen.findByText('Back to workflows')).toBeInTheDocument()
  })
})
