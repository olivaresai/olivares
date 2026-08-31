// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'

const { api, authState } = vi.hoisted(() => ({
  api: {
    run: vi.fn(),
    runs: vi.fn(),
    runDetail: vi.fn(),
  },
  authState: {
    activeTenant: 'tenant-a' as string | null,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  workflowsApi: api,
}))

import { RunPanel } from './run-panel'

const phaseOne = {
  op: 'run_request' as const,
  op_status: 'requested',
  plan_hash: 'hash-1',
  approval_ref: 'approval-1',
  gate_status: 'pending',
  requires_approval: true,
}

const run = {
  id: 'run-1',
  workflow_ref: 'workflow-1',
  status: 'completed' as const,
  plan_hash: 'hash-1',
  approval_ref: 'approval-1',
  actor: 'admin',
  started_at: '2026-07-01T00:00:00Z',
  finished_at: '2026-07-01T00:00:01Z',
  steps: [
    {
      ref: 'emit',
      kind: 'eventing-emit' as const,
      depends_on: [],
      status: 'emitted' as const,
    },
  ],
}

function renderPanel(canAdmin = true) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <RunPanel workflowId="workflow-1" canAdmin={canAdmin} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  api.runs.mockResolvedValue({ items: [], has_more: false })
  api.runDetail.mockResolvedValue(run)
  api.run.mockImplementation(async (_id: string, approvalRef?: string) =>
    approvalRef
      ? {
          op: 'run' as const,
          op_status: 'dispatched',
          plan_hash: 'hash-1',
          approval_ref: approvalRef,
          gate_status: 'approved',
          run,
        }
      : phaseOne,
  )
})

describe('RunPanel', () => {
  /**
   * ⛔ ESTE BADGE ERA `variant="warning"` FIJO, así que los seis estados de la puerta pintaban
   * la MISMA advertencia: una puerta aprobada se leía como un problema y una rechazada como el
   * mismo problema. El estado ya venía en `gate_status`; la variante ahora sale de él.
   *
   * EL MUTANTE: volver al warning fijo. Esta casilla lo mata porque afirma el TEXTO del veredicto
   * —que antes tampoco se pintaba: el literal iba interpolado en una frase genérica— y no el
   * color, que un test de DOM no debe perseguir.
   */
  it('pinta el veredicto de la puerta, no una advertencia genérica', async () => {
    const user = userEvent.setup()
    renderPanel()
    await user.click(screen.getByRole('button', { name: 'Run' }))
    expect(await screen.findByText(/^Pending$/i)).toBeInTheDocument()
  })

  it('shows the approval reference after phase one', async () => {
    const user = userEvent.setup()
    renderPanel()
    await user.click(screen.getByRole('button', { name: 'Run' }))
    expect(await screen.findByText('approval-1')).toBeInTheDocument()
    expect(api.run).toHaveBeenCalledWith('workflow-1')
  })

  it('executes phase two and renders the run', async () => {
    const user = userEvent.setup()
    renderPanel()
    await user.click(screen.getByRole('button', { name: 'Run' }))
    await user.click(
      await screen.findByRole('button', { name: 'Execute with approval' }),
    )
    await waitFor(() =>
      expect(api.run).toHaveBeenCalledWith('workflow-1', 'approval-1'),
    )
    expect(await screen.findByText('Run run-1')).toBeInTheDocument()
    expect(screen.getAllByText('Emitted')).toHaveLength(2)
  })

  it('explains an approval denial from phase two', async () => {
    api.run.mockImplementation(async (_id: string, approvalRef?: string) => {
      if (approvalRef) throw new ApiError(403, 'forbidden', 'denied')
      return phaseOne
    })
    const user = userEvent.setup()
    renderPanel()
    await user.click(screen.getByRole('button', { name: 'Run' }))
    await user.click(
      await screen.findByRole('button', { name: 'Execute with approval' }),
    )
    expect(
      await screen.findByText(/Approval was denied, is still pending/),
    ).toBeInTheDocument()
  })

  it('does not turn a failed run-detail read into a blank selection', async () => {
    api.runs.mockResolvedValue({ items: [run], has_more: false })
    api.runDetail.mockRejectedValue(new Error('detail offline'))
    const user = userEvent.setup()
    renderPanel(false)

    await user.click(screen.getByRole('button', { name: 'Run history' }))
    await user.click(
      await screen.findByRole('button', { name: 'View run run-1' }),
    )

    expect(
      await screen.findByText('The workflow run request failed.'),
    ).toBeInTheDocument()
  })
})
