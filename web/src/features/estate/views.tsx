// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Shapes } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

const EstateView = lazy(() => import('./estate-view'))

export const VIEWS = [
  {
    order: 280,
    id: 'estate',
    path: '/estate',
    navigation: {
      kind: 'feature',
      areaId: 'infrastructure',
      sectionId: 'estate',
    },
    helpHref: '/reference/modules/ii-sessions',
    icon: Shapes,
    permission: 'sessions:run:read',
    element: lazyView(EstateView),
  },
] satisfies ViewEntry[]
