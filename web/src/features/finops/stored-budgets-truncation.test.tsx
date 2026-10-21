// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The stored-budgets page says when its list is cut off, and never says it on top of the empty
// state. With `{items: [], has_more: true}` an unguarded badge reads "Loaded 0 budgets; there are
// more" under "No budgets yet": a message that contradicts itself. The view is mounted, not read
// as text, so a badge that is written but unreachable cannot pass.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import '@/features/_intel'
import { budgetsFixture } from './fixtures'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

const api = vi.hoisted(() => ({ budgets: vi.fn(), budgetStatus: vi.fn() }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, finopsApi: { ...actual.finopsApi, ...api } }
})

const { StoredBudgetsView } = await import('./stored-budgets-view')

beforeEach(() => {
  vi.clearAllMocks()
  // A status that never settles keeps each card on its skeleton: only the list envelope matters.
  api.budgetStatus.mockReturnValue(new Promise(() => {}))
})

describe('the stored-budgets list declares its cut', () => {
  it('has no "there are more" notice over the empty state', async () => {
    api.budgets.mockResolvedValue({ items: [], has_more: true })
    renderIntel(<StoredBudgetsView />)
    expect(await screen.findByText('No budgets yet')).toBeVisible()
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })

  it('names the loaded budgets when the page is cut and not empty', async () => {
    api.budgets.mockResolvedValue({
      items: budgetsFixture,
      has_more: true,
    })
    renderIntel(<StoredBudgetsView />)
    expect(
      await screen.findByText(
        `Loaded ${budgetsFixture.length} budgets; there are more`,
      ),
    ).toBeVisible()
  })

  it('has no notice when the page is complete', async () => {
    api.budgets.mockResolvedValue({ items: budgetsFixture, has_more: false })
    renderIntel(<StoredBudgetsView />)
    await vi.waitFor(() => expect(api.budgetStatus).toHaveBeenCalled())
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })
})
