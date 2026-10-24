// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE MONTH ON ESTATE'S SPEND LINE (#2546): the tenant's monthly budget over all spend,
// read only from the canonical amount the Cost page reads. An amount it cannot prove is
// no figure at all, never a guess.
import type { ReactNode } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createTestQueryClient, renderHook, waitFor } from '@/test/intel'
import { finopsApi } from '@/features/finops/api'
import {
  budgetsFixture,
  budgetStatusFixtures,
} from '@/features/finops/fixtures'
import { useModulesStore } from '@/stores/modules'
import { useMonthBudget } from './month-budget'

const auth = vi.hoisted(() => ({ can: (_p: string): boolean => true }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 'demo', can: auth.can }),
}))

afterEach(() => {
  auth.can = () => true
  useModulesStore.getState().setOff([])
  vi.restoreAllMocks()
})

function hook() {
  const queryClient = createTestQueryClient()
  return renderHook(() => useMonthBudget(), {
    wrapper: ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    ),
  })
}

describe('useMonthBudget', () => {
  it('reads the monthly budget over all spend, with its proven amount', async () => {
    vi.spyOn(finopsApi, 'budgets').mockResolvedValue({
      items: budgetsFixture,
      has_more: false,
    } as never)
    const status = vi
      .spyOn(finopsApi, 'budgetStatus')
      .mockResolvedValue(budgetStatusFixtures['bdg-global']!)
    const { result } = hook()
    await waitFor(() =>
      expect(result.current).toEqual({
        spentMicroUsd: 12_400_000_000,
        limitMicroUsd: 100_000_000_000,
        atLeast: false,
      }),
    )
    // Only the global one: a per-model budget is not the month of the whole estate.
    expect(status).toHaveBeenCalledTimes(1)
    expect(status.mock.calls[0]![0]).toBe('bdg-global')
  })

  it('an amount the engine cannot prove gives no figure', async () => {
    vi.spyOn(finopsApi, 'budgets').mockResolvedValue({
      items: budgetsFixture,
      has_more: false,
    } as never)
    const { amount: _omitted, ...legacy } = budgetStatusFixtures['bdg-global']!
    void _omitted
    vi.spyOn(finopsApi, 'budgetStatus').mockResolvedValue(legacy as never)
    const queryClient = createTestQueryClient()
    const { result } = renderHook(() => useMonthBudget(), {
      wrapper: ({ children }: { children: ReactNode }) => (
        <QueryClientProvider client={queryClient}>
          {children}
        </QueryClientProvider>
      ),
    })
    await waitFor(() => expect(queryClient.isFetching()).toBe(0))
    expect(result.current).toBeNull()
  })

  it('a proven floor is a figure said as "at least"; a budget with no limit is none', async () => {
    const global = budgetStatusFixtures['bdg-global']!
    vi.spyOn(finopsApi, 'budgets').mockResolvedValue({
      items: budgetsFixture,
      has_more: false,
    } as never)
    const status = vi.spyOn(finopsApi, 'budgetStatus').mockResolvedValue({
      ...global,
      amount: {
        ...global.amount!,
        class: 'lower_bound',
        state: 'incomplete',
        components: {
          ...global.amount!.components,
          dynamic_reservation: {
            state: 'nonnegative_unknown',
            value_micro_usd: null,
          },
        },
      },
    })
    const first = hook()
    await waitFor(() =>
      expect(first.result.current).toEqual({
        spentMicroUsd: 12_400_000_000,
        limitMicroUsd: 100_000_000_000,
        atLeast: true,
      }),
    )
    status.mockResolvedValue({ ...global, limit_micro_usd: 0 })
    const queryClient = createTestQueryClient()
    const second = renderHook(() => useMonthBudget(), {
      wrapper: ({ children }: { children: ReactNode }) => (
        <QueryClientProvider client={queryClient}>
          {children}
        </QueryClientProvider>
      ),
    })
    await waitFor(() => expect(queryClient.isFetching()).toBe(0))
    expect(second.result.current).toBeNull()
  })

  it('reads nothing without the budget read, or with FinOps off', async () => {
    const budgets = vi.spyOn(finopsApi, 'budgets')
    auth.can = () => false
    expect(hook().result.current).toBeNull()
    auth.can = () => true
    useModulesStore.getState().setOff(['finops'])
    expect(hook().result.current).toBeNull()
    expect(budgets).not.toHaveBeenCalled()
  })
})
