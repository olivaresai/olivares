// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Download } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

const UpdatesView = lazy(() => import('./updates-view'))

export const VIEWS = [
  // System updates of this installation: a page of Operations, no longer a section of the
  // personal Settings (0.5 one navigation). `/settings?section=updates` lands here.
  {
    order: 655,
    id: 'updates',
    path: '/updates',
    navigation: {
      kind: 'feature',
      areaId: 'system',
      sectionId: 'maintenance',
    },
    helpHref: '/how-to/upgrade-and-rollback',
    icon: Download,
    // The engine checks system:admin (and the step-up) on every update call.
    permission: 'system:admin',
    element: lazyView(UpdatesView),
  },
] satisfies ViewEntry[]
