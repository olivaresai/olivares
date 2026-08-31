// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// the budget lifecycle beyond create — edit (PUT) and delete (DELETE), which
// the engine always exposed but the console never wired. Drives the real FinOpsView
// budgets tab against a mocked finopsApi and asserts the update/delete calls.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  DEFAULT_AUTH,
  renderIntel,
  screen,
  userEvent,
  within,
} from '@/test/intel'
import '@/features/_intel'
import './i18n'
import type { Budget, BudgetStatus } from './types'

const { api } = vi.hoisted(() => ({
  api: {
    budgets: vi.fn(),
    budgetStatus: vi.fn(),
    alerts: vi.fn(),
    createBudget: vi.fn(),
    updateBudget: vi.fn(),
    deleteBudget: vi.fn(),
  },
}))

vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  finopsApi: api,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ ...DEFAULT_AUTH, activeTenant: 't1', can: () => true }),
}))

const budget: Budget = {
  id: 'b1',
  name: 'Opus cap',
  enabled: true,
  dimension: 'global',
  key: '',
  limit_micro_usd: 100_000_000, // $100
  period: 'monthly',
  thresholds: [],
  currency: 'USD',
  action: 'alert',
}
const status: BudgetStatus = {
  id: 'b1',
  name: 'Opus cap',
  enabled: true,
  dimension: 'global',
  key: '',
  period: 'monthly',
  period_start: '2026-07-01',
  currency: 'USD',
  action: 'alert',
  limit_micro_usd: 100_000_000,
  spend_micro_usd: 10_000_000,
  remaining_micro_usd: 90_000_000,
  consumed_pct: 10,
  projected_micro_usd: 30_000_000,
  projected_pct: 30,
  over: false,
  samples: 5,
  truncated: false,
}

beforeEach(() => {
  vi.clearAllMocks()
  api.budgets.mockResolvedValue({ items: [budget], has_more: false })
  api.budgetStatus.mockResolvedValue(status)
  api.alerts.mockResolvedValue({ items: [], has_more: false })
  api.updateBudget.mockResolvedValue({ ...budget, name: 'Opus cap v2' })
  api.deleteBudget.mockResolvedValue(undefined)
})

async function openBudgetsTab() {
  const { FinOpsView } = await import('./finops-view')
  renderIntel(<FinOpsView />)
  await userEvent.click(await screen.findByRole('tab', { name: /budgets/i }))
  // Wait for the budget card (its status resolves the spend figures).
  return screen.findByText('Opus cap')
}

describe('FinOpsView — budget lifecycle', () => {
  it('edits a budget with PUT, carrying the current spec', async () => {
    await openBudgetsTab()
    await userEvent.click(
      await screen.findByRole('button', { name: /edit opus cap/i }),
    )
    const dialog = await screen.findByRole('dialog')
    const nameInput = within(dialog).getByLabelText(/^name/i)
    await userEvent.clear(nameInput)
    await userEvent.type(nameInput, 'Opus cap v2')
    await userEvent.click(
      within(dialog).getByRole('button', { name: /save budget/i }),
    )

    await vi.waitFor(() =>
      expect(api.updateBudget).toHaveBeenCalledWith(
        'b1',
        expect.objectContaining({
          name: 'Opus cap v2',
          dimension: 'global',
          limit_micro_usd: 100_000_000,
          period: 'monthly',
          action: 'alert',
        }),
      ),
    )
  })

  it('deletes a budget only after a confirm step', async () => {
    await openBudgetsTab()
    await userEvent.click(
      await screen.findByRole('button', { name: /delete opus cap/i }),
    )
    // A confirm dialog gates the destructive call — nothing deleted yet.
    const confirm = await screen.findByRole('dialog')
    expect(api.deleteBudget).not.toHaveBeenCalled()
    await userEvent.click(
      within(confirm).getByRole('button', { name: /delete budget/i }),
    )
    await vi.waitFor(() => expect(api.deleteBudget).toHaveBeenCalledWith('b1'))
  })
})
