// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { BarChart3 } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// Executive dashboards (module XXI), code-split (charts load on demand).
const ExecutiveView = lazy(() =>
  import('./executive-view').then((m) => ({
    default: m.ExecutiveView,
  })),
)

export const VIEWS = [
  // No dedicated backend permission: module XXI is a web-only
  // rollup of the other modules' read APIs, so the route is open to any signed-in
  // user and each KPI pillar is gated INSIDE the view by its source's read
  // permission (a reader who can't see /finops never sees the cost KPI, and the
  // exported PDF therefore can't leak it). docs/SECURITY-HARDENING.md.
  {
    order: 570,
    id: 'dashboards',
    path: '/dashboards',
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'operations',
    },
    helpHref: '/reference/modules/xxi-executive-dashboards',
    icon: BarChart3,
    element: lazyView(ExecutiveView),
  },
] satisfies ViewEntry[]
