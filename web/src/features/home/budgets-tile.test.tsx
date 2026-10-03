// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Budgets on Now: the count of enabled budgets, and a caption that claims only what the
// canonical amounts prove.
import type { ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, DEFAULT_AUTH } from '@/test/intel'
import { finopsApi } from '@/features/finops/api'
import {
  budgetStatusFixtures,
  budgetsFixture,
} from '@/features/finops/fixtures'
import type { BudgetStatus } from '@/features/finops/types'
import { BudgetsTile } from './budgets-tile'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ ...DEFAULT_AUTH, activeTenant: 't-demo', can: () => true }),
}))
vi.mock('@tanstack/react-router', () => ({
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

const statusOf = (id: string, result?: 'proven'): BudgetStatus => {
  const s = structuredClone(budgetStatusFixtures[id])
  if (result && s.amount) s.amount.over_limit.result = result
  return s
}

afterEach(() => vi.restoreAllMocks())

describe('BudgetsTile', () => {
  it('says none is over its limit only when every status proves it', async () => {
    vi.spyOn(finopsApi, 'budgets').mockResolvedValue({
      items: budgetsFixture,
      has_more: false,
    })
    vi.spyOn(finopsApi, 'budgetStatus').mockImplementation(async (id) =>
      statusOf(id),
    )
    renderIntel(<BudgetsTile />)
    expect(await screen.findByText('None over limit')).toBeInTheDocument()
  })

  it('names a budget proven over its limit', async () => {
    vi.spyOn(finopsApi, 'budgets').mockResolvedValue({
      items: budgetsFixture,
      has_more: false,
    })
    vi.spyOn(finopsApi, 'budgetStatus').mockImplementation(async (id) =>
      statusOf(id, id === budgetsFixture[0].id ? 'proven' : undefined),
    )
    renderIntel(<BudgetsTile />)
    expect(await screen.findByText('1 over limit')).toBeInTheDocument()
  })

  it('counts a status it could not read as unknown, never as within', async () => {
    vi.spyOn(finopsApi, 'budgets').mockResolvedValue({
      items: budgetsFixture,
      has_more: false,
    })
    vi.spyOn(finopsApi, 'budgetStatus').mockImplementation(async (id) => {
      if (id === budgetsFixture[0].id) throw new Error('status down')
      return statusOf(id)
    })
    renderIntel(<BudgetsTile />)
    expect(await screen.findByText('1 amount unknown')).toBeInTheDocument()
    expect(screen.queryByText('None over limit')).toBeNull()
  })

  // HU2-25: "Budgets · No budgets yet" on an upgraded install's Now.
  it('draws no tile while no budget is enabled', async () => {
    const budgets = vi
      .spyOn(finopsApi, 'budgets')
      .mockResolvedValue({ items: [], has_more: false })
    const { container } = renderIntel(<BudgetsTile />)
    await vi.waitFor(() => expect(budgets).toHaveBeenCalled())
    await vi.waitFor(() => expect(screen.queryByText('Budgets')).toBeNull())
    expect(container.querySelector('a')).toBeNull()
  })
})
