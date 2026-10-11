// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { LayoutTemplate } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// Workspace templates catalog — create/edit/duplicate/archive reusable
// session configuration templates (hooks, settings, connectors, policies).
const TemplatesView = lazy(() =>
  import('./templates-view').then((m) => ({
    default: m.TemplatesView,
  })),
)

export const VIEWS = [
  {
    order: 380,
    // Workspace templates catalog — reusable session configuration snapshots
    // (hooks, settings, connectors, policies). Gated on the base template-read perm;
    // create/edit/archive actions gate further inside the view.
    id: 'workspace-templates',
    path: '/workspace-templates',
    navigation: {
      kind: 'feature',
      areaId: 'ai',
      sectionId: 'environments',
    },
    helpHref: '/reference/modules/ii-sessions',
    icon: LayoutTemplate,
    permission: 'sessions:template:read',
    element: lazyView(TemplatesView),
  },
] satisfies ViewEntry[]
