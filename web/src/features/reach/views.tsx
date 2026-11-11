// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Route } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// What one identity can reach, projected from the access map: a page of Audit, an
// administrator place (navigation/places.ts), filed with Audit's pages so the palette and
// the console reference place it there. Order 315 keeps the registry's step-of-ten
// spacing (320 is taken).
const ReachView = lazy(() => import('./reach-view'))

export const VIEWS = [
  {
    order: 315,
    id: 'reach',
    path: '/reach',
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'audit-recordings',
    },
    helpHref: '/reference/modules/iii-access-map',
    icon: Route,
    permission: 'accessmap:graph:read',
    element: lazyView(ReachView),
  },
] satisfies ViewEntry[]
