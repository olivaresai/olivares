// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Layers } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

const PlatformsView = lazy(() =>
  import('./platforms-view').then((m) => ({
    default: m.PlatformsView,
  })),
)

export const VIEWS = [
  {
    order: 610,
    id: 'platforms',
    path: '/platforms',
    navigation: {
      kind: 'feature',
      areaId: 'ai',
      sectionId: 'provider-reference',
    },
    helpHref: '/reference/modules/x-models',
    icon: Layers,
    permission: 'models:platforms:read',
    element: lazyView(PlatformsView),
  },
] satisfies ViewEntry[]
