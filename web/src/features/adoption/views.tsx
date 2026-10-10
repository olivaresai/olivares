// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Gauge } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

//(gap #12) Claude Code adoption / productivity dashboard.
const AdoptionView = lazy(() =>
  import('./adoption-view').then((m) => ({ default: m.AdoptionView })),
)

export const VIEWS = [
  {
    order: 460,
    id: 'adoption',
    path: '/adoption',
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'cost-adoption',
    },
    helpHref: '/reference/modules/claudeadoption',
    icon: Gauge,
    // Team/org adoption views are viewer-read; the per-developer drill-down is gated
    // deny-closed inside the view (adoption:developer:read).
    permission: 'adoption:metrics:read',
    element: lazyView(AdoptionView),
  },
] satisfies ViewEntry[]
