// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The budgets tile's financial rule, apart from its rendering (see budgets-tile.tsx).
import { budgetAmount } from '@/features/finops/evidence'
import type { BudgetStatus } from '@/features/finops/types'

/** The most statuses Now reads; the Cost page reads them all. */
export const BUDGET_STATUS_READS = 20

export const BUDGET_READ = 'finops:budget:read'

type Verdict = 'over' | 'unknown' | 'within'

/** One status, judged only on the canonical amount (see the header). */
export function budgetVerdict(status: BudgetStatus): Verdict {
  const amount = budgetAmount(status)
  const crossing = status.amount?.over_limit?.result
  if (amount.class !== 'unknown' && crossing === 'proven') return 'over'
  if (amount.class === 'unknown' || crossing !== 'not_reached') return 'unknown'
  return 'within'
}
