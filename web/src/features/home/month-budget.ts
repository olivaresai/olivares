// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE MONTH ON ESTATE'S SPEND LINE (#2546: "against the monthly budget, no day budget"):
// the tenant's monthly budget over all spend, the strictest one when there are several.
// It is a financial figure, so it reads only the canonical amount the Cost page reads
// (BudgetStatus.amount through `budgetAmount`), under the Cost page's own cache keys: an
// exact amount, or a proven floor said as "at least"; an amount the engine cannot prove
// gives no figure, and neither does a budget with no limit to measure against.
import { useQuery } from '@tanstack/react-query'
import { finopsApi, finopsKeys } from '@/features/finops/api'
import { budgetAmount } from '@/features/finops/evidence'
import { useAuth } from '@/lib/auth/context'
import { useModuleOn } from '@/stores/modules'

/** The permission that reads budgets and their status. */
export const BUDGET_READ = 'finops:budget:read'

export interface MonthBudget {
  spentMicroUsd: number
  limitMicroUsd: number
  /** The amount is a proven lower bound (part of the spend is still being counted). */
  atLeast: boolean
}

export function useMonthBudget(): MonthBudget | null {
  // Budgets live in finops: while it is off there is nothing to read (ARCH C1).
  const finopsOn = useModuleOn('finops')
  const { activeTenant, can } = useAuth()
  const readable = finopsOn && can(BUDGET_READ) && !!activeTenant
  const budgets = useQuery({
    queryKey: finopsKeys.budgets(activeTenant),
    queryFn: () => finopsApi.budgets(),
    enabled: readable,
  })
  const month = (budgets.data?.items ?? [])
    .filter(
      (b) => b.enabled && b.period === 'monthly' && b.dimension === 'global',
    )
    .sort((a, b) => a.limit_micro_usd - b.limit_micro_usd)[0]
  const status = useQuery({
    queryKey: finopsKeys.budgetStatus(activeTenant, month?.id ?? ''),
    queryFn: () => finopsApi.budgetStatus(month!.id, { tenant: activeTenant }),
    enabled: readable && !!month,
  })
  if (!readable || !month || !status.isSuccess) return null
  const limitMicroUsd = status.data.limit_micro_usd
  if (!(limitMicroUsd > 0)) return null
  // An exact amount, or a proven floor (said as "at least"); anything else is no figure.
  const amount = budgetAmount(status.data)
  if (amount.class !== 'exact' && amount.class !== 'lower_bound') return null
  if (amount.value === null) return null
  return {
    spentMicroUsd: Number(amount.value),
    limitMicroUsd,
    atLeast: amount.class === 'lower_bound',
  }
}
