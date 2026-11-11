// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Server } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// The lead of the Operations place: the host, the live sessions and their usage, on one page.
// Each section reads only what the person may read, so the page opens for anyone who can read
// the installation's health and shows a system administrator the host as well. It does not open
// on sessions:run:read: a member reads their own runs and has no Operations.
const OperationsView = lazy(() =>
  import('./operations-view').then((m) => ({ default: m.OperationsView })),
)

export const VIEWS = [
  {
    order: 75,
    id: 'operations',
    path: '/operations',
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'operations',
    },
    helpHref: '/reference/console',
    icon: Server,
    permission: 'health:status:read',
    element: lazyView(OperationsView),
  },
] satisfies ViewEntry[]
