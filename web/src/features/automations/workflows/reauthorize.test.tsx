// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Reauthorizing a run paused for reauthentication
// (POST /v1/m/orchestration/workflows/{id}/runs/{run}/reauthorize,
// modules/orchestration/workflow_reauthorize.go). The panel shows the plan hash
// the operator re-approves, posts nothing before an explicit confirmation, posts
// exactly the run's plan_hash, offers the action only to an administrator of a
// gated run, and maps each refusal to its own copy.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'

const { api, authState } = vi.hoisted(() => ({
  api: {
    run: vi.fn(),
    runs: vi.fn(),
    runDetail: vi.fn(),
    reauthorize: vi.fn(),
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

const PLAN =
  'sha256:4b2e8a0c6f1d93e7a5b0c2d4e6f8a1b3c5d7e9f0a2b4c6d8e0f1a3b5c7d9e1f3'

const gatedRun = {
  id: 'run-1',
  workflow_ref: 'workflow-1',
  status: 'running' as const,
  plan_hash: PLAN,
  approval_ref: 'approval-1',
  paused_reason: 'reauthentication_required',
  actor: 'admin',
  started_at: '2026-09-27T00:00:00Z',
  steps: [
    {
      ref: 'notify',
      kind: 'notify-test' as const,
      depends_on: [],
      status: 'reauthentication_required' as const,
    },
  ],
}

const resumedRun = {
  ...gatedRun,
  paused_reason: undefined,
  steps: [{ ...gatedRun.steps[0], status: 'pending' as const }],
}

const phaseOne = {
  op: 'run_request' as const,
  op_status: 'requested',
  plan_hash: PLAN,
  approval_ref: 'approval-2',
  gate_status: 'pending',
  requires_approval: true,
}

const EXPLANATION =
  /paused until its initiator continues it with a current credential no wider than the one it started with/i

function renderPanel(canAdmin: boolean) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <RunPanel workflowId="workflow-1" canAdmin={canAdmin} />
    </QueryClientProvider>,
  )
}

/** Opens the panel the way each role can and selects run-1 from the history. */
async function openRun(canAdmin: boolean) {
  const user = userEvent.setup()
  renderPanel(canAdmin)
  await user.click(
    screen.getByRole('button', { name: canAdmin ? 'Run' : 'Run history' }),
  )
  await user.click(
    await screen.findByRole('button', { name: 'View run run-1' }),
  )
  await screen.findByText('Run run-1')
  return user
}

async function confirmReauthorization(
  user: ReturnType<typeof userEvent.setup>,
) {
  await user.click(screen.getByRole('button', { name: 'Reauthorize run' }))
  const dialog = await screen.findByRole('dialog', {
    name: 'Reauthorize this run?',
  })
  await user.click(within(dialog).getByRole('button', { name: 'Reauthorize' }))
}

beforeEach(() => {
  vi.clearAllMocks()
  api.run.mockResolvedValue(phaseOne)
  api.runs.mockResolvedValue({ items: [gatedRun], has_more: false })
  api.runDetail.mockResolvedValue(gatedRun)
  api.reauthorize.mockResolvedValue({
    detail: 'run reauthorized',
    run: resumedRun,
  })
})

describe('reauthorizing a run paused for reauthentication', () => {
  it('shows the plan hash the operator re-approves, in full', async () => {
    await openRun(true)
    const section = screen.getByRole('region', {
      name: 'Waiting for reauthentication',
    })
    expect(within(section).getByText(EXPLANATION)).toBeInTheDocument()
    const hash = within(section).getByText(PLAN)
    expect(hash.tagName).toBe('CODE')
    expect(hash).toHaveClass('break-all', 'font-mono')
    expect(
      screen.getAllByText('Reauthentication required').length,
    ).toBeGreaterThan(0)
  })

  it('posts nothing until the operator confirms, and nothing on cancel', async () => {
    const user = await openRun(true)
    await user.click(screen.getByRole('button', { name: 'Reauthorize run' }))
    const dialog = await screen.findByRole('dialog', {
      name: 'Reauthorize this run?',
    })
    // The confirmation restates the exact plan being re-approved.
    expect(within(dialog).getByText(PLAN)).toBeInTheDocument()
    expect(api.reauthorize).not.toHaveBeenCalled()

    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    await waitFor(() =>
      expect(
        screen.queryByRole('dialog', { name: 'Reauthorize this run?' }),
      ).not.toBeInTheDocument(),
    )
    expect(api.reauthorize).not.toHaveBeenCalled()
  })

  it("posts exactly the run's plan hash and shows the returned run", async () => {
    const user = await openRun(true)
    const runsBefore = api.runs.mock.calls.length
    // After the POST the engine read never answers, so only the response can
    // show the resumed run.
    api.reauthorize.mockImplementation(async () => {
      api.runDetail.mockReturnValue(new Promise(() => {}))
      return { detail: 'run reauthorized', run: resumedRun }
    })

    await confirmReauthorization(user)

    await waitFor(() => expect(api.reauthorize).toHaveBeenCalledTimes(1))
    expect(api.reauthorize).toHaveBeenCalledWith('workflow-1', 'run-1', PLAN)
    await waitFor(() =>
      expect(
        screen.queryByRole('button', { name: 'Reauthorize run' }),
      ).not.toBeInTheDocument(),
    )
    expect(screen.getAllByText('Pending').length).toBeGreaterThan(0)
    expect(screen.queryByText('Reauthentication required')).toBeNull()
    await waitFor(() =>
      expect(api.runs.mock.calls.length).toBeGreaterThan(runsBefore),
    )
  })

  it('offers the action when only a step is gated', async () => {
    const stepGated = { ...gatedRun, paused_reason: undefined }
    api.runs.mockResolvedValue({ items: [stepGated], has_more: false })
    api.runDetail.mockResolvedValue(stepGated)
    await openRun(true)
    expect(
      screen.getByRole('button', { name: 'Reauthorize run' }),
    ).toBeInTheDocument()
  })

  it('does not offer the action to a caller who cannot administer', async () => {
    await openRun(false)
    // The explanation and the plan hash still inform the reader.
    expect(screen.getByText(EXPLANATION)).toBeInTheDocument()
    expect(screen.getByText(PLAN)).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Reauthorize run' }),
    ).not.toBeInTheDocument()
  })

  it('does not offer the action for a run that is not gated', async () => {
    const running = {
      ...gatedRun,
      paused_reason: undefined,
      steps: [{ ...gatedRun.steps[0], status: 'executing' as const }],
    }
    api.runs.mockResolvedValue({ items: [running], has_more: false })
    api.runDetail.mockResolvedValue(running)
    await openRun(true)
    expect(screen.queryByText(EXPLANATION)).toBeNull()
    expect(
      screen.queryByRole('button', { name: 'Reauthorize run' }),
    ).not.toBeInTheDocument()
  })

  it('does not offer the action for a finished run that still names the gate', async () => {
    const finished = {
      ...gatedRun,
      status: 'failed' as const,
      finished_at: '2026-09-27T00:01:00Z',
    }
    api.runs.mockResolvedValue({ items: [finished], has_more: false })
    api.runDetail.mockResolvedValue(finished)
    await openRun(true)
    expect(
      screen.queryByRole('button', { name: 'Reauthorize run' }),
    ).not.toBeInTheDocument()
  })
})

describe('reauthorization refusals', () => {
  it.each([
    [
      403,
      'this credential exceeds the authority the run was started with; reauthorize with a credential no wider',
      /This credential cannot continue the run\. Use a current credential of the run's initiator that is no wider than the one the run started with, or start a new run\./,
    ],
    [
      409,
      "plan_hash does not match the run's approved plan",
      /The run is not waiting for reauthentication, its plan hash does not match, or another reauthorization is in progress\. Refresh the run/,
    ],
    [404, 'not found', /The run was not found in this workflow\./],
    [
      503,
      'credential binding is unavailable; run not reauthorized, retry',
      /The run was not reauthorized because credential binding is unavailable\. Try again\./,
    ],
  ])('a %i renders its own copy', async (status, message, copy) => {
    api.reauthorize.mockRejectedValue(new ApiError(status, 'internal', message))
    const user = await openRun(true)
    await confirmReauthorization(user)
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(copy)
    // Never the run-approval copy of RunError.
    expect(alert).not.toHaveTextContent(/Approval was denied/)
  })

  it('a 409 offers to refresh the run, and refreshing reads it again', async () => {
    api.reauthorize.mockRejectedValue(
      new ApiError(
        409,
        'internal',
        'the run changed during reauthorization; retry',
      ),
    )
    const user = await openRun(true)
    await confirmReauthorization(user)
    const alert = await screen.findByRole('alert')
    const before = api.runDetail.mock.calls.length
    await user.click(within(alert).getByRole('button', { name: 'Refresh run' }))
    await waitFor(() =>
      expect(api.runDetail.mock.calls.length).toBeGreaterThan(before),
    )
  })

  it('a step-up demand shows the step-up copy, never the credential refusal', async () => {
    api.reauthorize.mockRejectedValue(
      new ApiError(403, 'step_up_required', 'assurance level too low'),
    )
    const user = await openRun(true)
    await confirmReauthorization(user)
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('This action needs an elevated session')
    expect(alert).not.toHaveTextContent(/cannot continue the run/)
  })
})
