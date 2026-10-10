// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Boxes } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// Visibility views, code-split (the access-map / dependency-map React Flow
// graphs load on demand). Named exports mapped to a default for lazy().
const InventoryView = lazy(() =>
  import('./index').then((m) => ({ default: m.InventoryView })),
)

export const VIEWS = [
  {
    order: 40,
    id: 'inventory',
    path: '/inventory',
    navigation: {
      kind: 'feature',
      areaId: 'infrastructure',
      sectionId: 'estate',
    },
    helpHref: '/reference/modules/i-inventory',
    icon: Boxes,
    permission: 'inventory:catalog:read',
    element: lazyView(InventoryView),
  },
] satisfies ViewEntry[]
