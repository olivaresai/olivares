// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { PanelsTopLeft } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// Per-workspace dashboard (overview group). Shows agents, sessions,
// resources and groups scoped to the workspace selected in the topbar switcher.
const WorkspaceDashboardView = lazy(() =>
  import('./workspace-dashboard-view').then((m) => ({
    default: m.WorkspaceDashboardView,
  })),
)

export const VIEWS = [
  // Per-workspace dashboard: agents, sessions, resources and groups scoped
  // to the workspace selected in the topbar switcher. Gated on tenant:read (the
  // same permission the workspace list requires).
  {
    order: 30,
    id: 'workspaceDashboard',
    path: '/workspace',
    navigation: {
      kind: 'feature',
      areaId: 'infrastructure',
      sectionId: 'estate',
    },
    helpHref: '/reference/modules/xx-multi-tenancy',
    // PanelsTopLeft, not Layers: Layers belongs to Platforms; a dashboard
    // of scoped panels is what this view actually is.
    icon: PanelsTopLeft,
    permission: 'tenant:read',
    element: lazyView(WorkspaceDashboardView),
  },
] satisfies ViewEntry[]
