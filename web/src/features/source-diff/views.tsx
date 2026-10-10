// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { GitCompare } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

const SourceDiffView = lazy(() => import('./source-diff-view'))

export const VIEWS = [
  {
    order: 100,
    id: 'sourceDiff',
    path: '/console/sources/diff',
    navigation: {
      kind: 'feature',
      areaId: 'system',
      sectionId: 'administration',
    },
    helpHref: '/reference/console',
    icon: GitCompare,
    permission: 'system:admin',
    element: lazyView(SourceDiffView),
  },
] satisfies ViewEntry[]
