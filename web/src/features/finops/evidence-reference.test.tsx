// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  DEFAULT_AUTH,
  createTestQueryClient,
  renderIntel,
  screen,
  userEvent,
  act,
} from '@/test/intel'
import { ApiError } from '@/lib/api/errors'
import { BudgetsTab } from './finops-view'
import raw from './evidence-fixtures.json'
import './i18n'
const state = vi.hoisted(() => ({
  tenant: 'tenant-one',
  api: { budgets: vi.fn(), budgetStatus: vi.fn(), alerts: vi.fn() },
}))
vi.mock('./api', async (original) => ({
  ...(await original<typeof import('./api')>()),
  finopsApi: state.api,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ ...DEFAULT_AUTH, activeTenant: state.tenant }),
}))
beforeEach(() => {
  vi.clearAllMocks()
  state.tenant = 'tenant-one'
  state.api.budgets.mockResolvedValue({ items: [], has_more: false })
  state.api.alerts.mockResolvedValue({ items: [], has_more: false })
})
describe('reference authority', () => {
  it('uses an explicit tenant, withdraws on error and clears the reference on tenant change', async () => {
    const qc = createTestQueryClient()
    const view = renderIntel(<BudgetsTab canWrite={false} />, {
      queryClient: qc,
    })
    await userEvent.type(
      screen.getByLabelText('Alert reference'),
      'reference-one',
    )
    await userEvent.click(screen.getByRole('button', { name: 'Look up' }))
    await vi.waitFor(() =>
      expect(state.api.alerts).toHaveBeenCalledWith(
        { alert_id: 'reference-one' },
        { tenant: 'tenant-one' },
      ),
    )
    // A response about another tenant cannot become a monetary claim, even
    // before a request error. This also checks the view-to-table tenant binding.
    state.api.alerts.mockResolvedValue(raw.exact.alerts)
    await userEvent.click(screen.getByRole('button', { name: 'Look up' }))
    await screen.findAllByText('Amount unknown')
    expect(screen.queryByText('$12.00')).toBeNull()
    const alert = raw.exact.alerts.items[0]
    state.api.alerts.mockResolvedValue({
      ...raw.exact.alerts,
      items: [
        {
          ...alert,
          amount_evidence: {
            ...alert.amount_evidence,
            envelope: {
              ...alert.amount_evidence.envelope!,
              tenant_id: 'tenant-one',
            },
          },
        },
      ],
    })
    await userEvent.click(screen.getByRole('button', { name: 'Look up' }))
    expect(await screen.findByText('$12.00')).toBeInTheDocument()
    state.api.alerts.mockRejectedValue(
      new ApiError(500, 'internal', 'fixture failure'),
    )
    await userEvent.click(screen.getByRole('button', { name: 'Look up' }))
    await vi.waitFor(() => expect(screen.queryByText('$12.00')).toBeNull())
    state.tenant = 'tenant-two'
    state.api.alerts.mockResolvedValue({ items: [], has_more: false })
    await act(async () => view.rerender(<BudgetsTab canWrite={false} />))
    expect(screen.getByLabelText('Alert reference')).toHaveValue('')
    await vi.waitFor(() =>
      expect(state.api.alerts).toHaveBeenLastCalledWith(undefined, {
        tenant: 'tenant-two',
      }),
    )
    expect(screen.queryByText('$12.00')).toBeNull()
  })
})
