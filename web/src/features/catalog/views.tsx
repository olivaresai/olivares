// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Library } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

const CatalogView = lazy(() => import('./catalog-view'))

export const VIEWS = [
  {
    order: 260,
    id: 'catalog',
    path: '/catalog',
    navigation: {
      kind: 'feature',
      areaId: 'data-context',
      sectionId: 'capabilities',
    },
    helpHref: '/reference/modules/xiv-catalog',
    icon: Library,
    permission: 'catalog:entry:read',
    element: lazyView(CatalogView),
  },
] satisfies ViewEntry[]
