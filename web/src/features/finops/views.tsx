// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Wallet } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'
const StoredBudgetsView = lazy(() =>
  import('./stored-budgets-view').then((m) => ({
    default: m.StoredBudgetsView,
  })),
)
export const VIEWS = [
  {
    order: 451,
    id: 'storedBudgets',
    path: '/stored-budgets',
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'cost-adoption',
    },
    helpHref: '/reference/modules/xi-finops',
    icon: Wallet,
    permission: 'finops:budget:read',
    element: lazyView(StoredBudgetsView),
  },
] satisfies ViewEntry[]
