// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, it } from 'vitest'
import { FEATURE_EXTENSIONS } from '@/features/extensions'
import { FEATURE_VIEWS, ROUTE_ALIASES } from '@/features/registry'
it('keeps a budget-read destination for the stored-budget warning', () => {
  const stored = FEATURE_VIEWS.find((view) => view.id === 'storedBudgets')
  expect(stored).toMatchObject({
    path: '/stored-budgets',
    permission: 'finops:budget:read',
  })
  if (!FEATURE_EXTENSIONS.some((view) => view.id === 'finops'))
    expect(ROUTE_ALIASES.find((alias) => alias.from === '/finops')?.to).toBe(
      '/stored-budgets',
    )
})
