// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import type { WorkIntent } from './api'
import type { Plan } from './types'
import './i18n'

const wire = vi.hoisted(() => ({ planWork: vi.fn(), applyWork: vi.fn() }))
vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  planWork: wire.planWork,
  applyWork: wire.applyWork,
}))

import { ApplyFlow } from './apply-flow'

const intent = (over: Partial<WorkIntent> = {}): WorkIntent => ({
  key: '11111111-1111-4111-8111-111111111111',
  command: 'item.complete',
  path: '/v1/m/sessions/work-items/w1/transitions',
  method: 'POST',
  body: { command: 'item.complete' },
  etag: '"v4"',
  ...over,
  tenant: over.tenant ?? null,
})

const plan: Plan = {
  verdict: 'LIMPIO',
  code: 'ok',
  observed_at: '2026-08-27T06:00:00Z',
  checks: [],
  plan_hash: 'plan-hash-1',
  command: 'item.complete',
  row_effects: ['work_items:update'],
  event_type: 'work.item.transitioned',
  audit_action: 'sessions.work.item.complete',
  permission: 'sessions:work:write',
  external_calls: [],
}

function show(nextIntent = intent()) {
  return render(
    <ApplyFlow
      open
      onOpenChange={() => {}}
      intent={nextIntent}
      title="Apply change"
    />,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  wire.planWork.mockResolvedValue(plan)
  wire.applyWork.mockResolvedValue({
    result: {
      verdict: 'LIMPIO',
      code: 'ok',
      command_id: 'cmd-1',
      result_kind: 'sessions.work-item',
      event_id: 'event-1',
      event_seq: 5,
      owner_epoch: 1,
      plan_hash: 'plan-hash-1',
      audit_seq: 9,
    },
    replayed: false,
    etag: '"v5"',
  })
})

describe('ApplyFlow keeps plan and apply as two ordered wire requests', () => {
  it('shows the plan before applying it with the exact plan hash', async () => {
    const user = userEvent.setup()
    const workIntent = intent()
    show(workIntent)

    await waitFor(() => expect(wire.planWork).toHaveBeenCalledWith(workIntent))
    await user.click(await screen.findByRole('button', { name: 'Apply' }))

    await waitFor(() =>
      expect(wire.applyWork).toHaveBeenCalledWith(workIntent, 'plan-hash-1'),
    )
    expect(wire.planWork.mock.invocationCallOrder[0]).toBeLessThan(
      wire.applyWork.mock.invocationCallOrder[0],
    )
  })

  it('retries a failed plan as a plan and never bypasses it with apply', async () => {
    const unknown = new ApiError(
      503,
      'evidence_unavailable',
      'could not look',
      undefined,
      {},
      {
        verdict: 'NO_HE_PODIDO_MIRAR',
        code: 'evidence_unavailable',
      },
    )
    wire.planWork.mockRejectedValueOnce(unknown).mockResolvedValueOnce(plan)
    const user = userEvent.setup()
    show()

    await user.click(
      await screen.findByRole('button', {
        name: 'Retry with the same key',
      }),
    )

    await waitFor(() => expect(wire.planWork).toHaveBeenCalledTimes(2))
    expect(wire.applyWork).not.toHaveBeenCalled()
    expect(await screen.findByRole('button', { name: 'Apply' })).toBeEnabled()
  })

  it('keeps caller-known lease fields instead of making the operator retype them', async () => {
    const user = userEvent.setup()
    const leaseIntent = intent({
      command: 'lease.renew',
      path: '/v1/m/sessions/work-items/w1/lease/renew',
      body: { command: 'lease.renew', holder_sid: 's-holder-1', fence: 7 },
    })
    show(leaseIntent)

    expect(await screen.findByLabelText(/Holder session/)).toHaveValue(
      's-holder-1',
    )
    expect(screen.getByLabelText(/Fence/)).toHaveValue('7')
    await user.click(screen.getByRole('button', { name: 'Continue' }))

    await waitFor(() =>
      expect(wire.planWork).toHaveBeenCalledWith(
        expect.objectContaining({
          key: leaseIntent.key,
          body: expect.objectContaining({
            holder_sid: 's-holder-1',
            fence: 7,
          }),
        }),
      ),
    )
  })
})
