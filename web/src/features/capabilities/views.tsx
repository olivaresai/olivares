// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Plug } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// Management views, code-split so the heavier graph/editor deps load on demand.
const CapabilitiesView = lazy(() => import('./capabilities-view'))

export const VIEWS = [
  {
    order: 110,
    id: 'capabilities',
    path: '/capabilities',
    navigation: {
      kind: 'feature',
      areaId: 'data-context',
      sectionId: 'capabilities',
    },
    helpHref: '/reference/modules/v-capabilities',
    icon: Plug,
    permission: 'capabilities:catalog:read',
    element: lazyView(CapabilitiesView),
  },
] satisfies ViewEntry[]
