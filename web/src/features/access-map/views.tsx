// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Network } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

const AccessMapView = lazy(() =>
  import('./index').then((m) => ({ default: m.AccessMapView })),
)

export const VIEWS = [
  {
    order: 60,
    id: 'accessMap',
    path: '/access-map',
    navigation: {
      kind: 'feature',
      areaId: 'security-identity',
      sectionId: 'access',
    },
    helpHref: '/reference/modules/iii-access-map',
    icon: Network,
    permission: 'accessmap:graph:read',
    element: lazyView(AccessMapView),
  },
] satisfies ViewEntry[]
