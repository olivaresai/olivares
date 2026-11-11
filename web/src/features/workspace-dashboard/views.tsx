// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { PanelsTopLeft } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// The files of the folders sessions work in; the per-workspace overview (agents,
// sessions, resources and groups) is its `?tab=overview`.
const WorkspaceView = lazy(() =>
  import('./workspace-files').then((m) => ({
    default: m.WorkspaceView,
  })),
)

export const VIEWS = [
  // Gated on the folder read (sessions:workspace:read), so a member who works in their
  // folders finds Files; without the tenant read the workspace list stays empty and the
  // overview tab shows the session folders. The folder read belongs to the sessions
  // module, a kernel module that runs on every installation (docs/module-spec.md), so no
  // module switch hides this page. The files divide the viewport into columns; the
  // overview tab scrolls inside its own page. ponytail: the folder read stands in for the
  // member's granted-folders read; that read takes this permission when the engine
  // declares it.
  {
    order: 30,
    id: 'workspaceDashboard',
    path: '/workspace',
    frame: 'work',
    navigation: {
      kind: 'feature',
      areaId: 'infrastructure',
      sectionId: 'estate',
    },
    helpHref: '/reference/modules/xx-multi-tenancy',
    // PanelsTopLeft, not Layers: Layers belongs to Platforms; a dashboard
    // of scoped panels is what this view actually is.
    icon: PanelsTopLeft,
    permission: 'sessions:workspace:read',
    element: lazyView(WorkspaceView),
  },
] satisfies ViewEntry[]
