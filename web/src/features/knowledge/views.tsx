// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { BookOpen } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

const KnowledgeView = lazy(() => import('./knowledge-view'))

export const VIEWS = [
  {
    order: 250,
    id: 'knowledge',
    path: '/knowledge',
    navigation: {
      kind: 'feature',
      areaId: 'data-context',
      sectionId: 'knowledge',
    },
    helpHref: '/reference/modules/viii-knowledge',
    icon: BookOpen,
    permission: 'knowledge:kb:read',
    changes: [
      { permission: 'knowledge:kb:write' },
      { permission: 'knowledge:kb:admin' },
      { permission: 'knowledge:context:write' },
      { permission: 'knowledge:data_product:write' },
      { permission: 'knowledge:data_product:admin' },
      { permission: 'knowledge:dlp:admin' },
      { permission: 'knowledge:memory:write' },
      { permission: 'knowledge:memory:admin' },
      { permission: 'knowledge:prompt:write' },
      { permission: 'knowledge:scan:write' },
    ],
    element: lazyView(KnowledgeView),
  },
] satisfies ViewEntry[]
