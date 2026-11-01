// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { ChartNoAxesColumn } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// The lead of Usage & cost: each account's official windows, nearest its limit first, and
// what today's sessions used. Budgets and Adoption follow it in the place.
const UsageView = lazy(() =>
  import('./usage-view').then((m) => ({ default: m.UsageView })),
)

export const VIEWS = [
  {
    order: 449,
    id: 'usage',
    path: '/usage',
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'cost-adoption',
    },
    helpHref: '/reference/console',
    icon: ChartNoAxesColumn,
    permission: 'sessions:run:read',
    element: lazyView(UsageView),
  },
] satisfies ViewEntry[]
