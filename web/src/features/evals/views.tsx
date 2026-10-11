// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { ClipboardCheck } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

const EvalsView = lazy(() =>
  import('./evals-view').then((m) => ({ default: m.EvalsView })),
)

export const VIEWS = [
  {
    order: 470,
    id: 'evals',
    path: '/evals',
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'audit-recordings',
    },
    helpHref: '/reference/modules/xii-evals',
    icon: ClipboardCheck,
    permission: 'evals:run:read',
    element: lazyView(EvalsView),
  },
] satisfies ViewEntry[]
