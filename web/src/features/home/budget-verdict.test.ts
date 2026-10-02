// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The budgets tile's financial rule: "over" only when the crossing is proven on a known
// amount; anything it cannot prove is "unknown", never "within".
import { describe, expect, it } from 'vitest'
import { budgetStatusFixtures } from '@/features/finops/fixtures'
import type { BudgetStatus } from '@/features/finops/types'
import { budgetVerdict } from './budget-verdict'

const base = Object.values(budgetStatusFixtures)[0]
const withCrossing = (
  result: 'proven' | 'not_reached' | 'unproven',
): BudgetStatus =>
  structuredClone({
    ...base,
    amount: {
      ...base.amount!,
      over_limit: { ...base.amount!.over_limit, result },
    },
  })

describe('budgetVerdict', () => {
  it('is within a limit the amount proves it has not reached', () => {
    expect(budgetVerdict(withCrossing('not_reached'))).toBe('within')
  })

  it('is over only where the crossing is proven', () => {
    expect(budgetVerdict(withCrossing('proven'))).toBe('over')
  })

  it('is unknown when the crossing cannot be proven, never within', () => {
    expect(budgetVerdict(withCrossing('unproven'))).toBe('unknown')
  })

  it('is unknown when the server sent no canonical amount (absent is not zero)', () => {
    const { amount: _omitted, ...legacy } = base
    void _omitted
    expect(budgetVerdict({ ...legacy, over: true } as BudgetStatus)).toBe(
      'unknown',
    )
    expect(budgetVerdict({ ...legacy, over: false } as BudgetStatus)).toBe(
      'unknown',
    )
  })
})
