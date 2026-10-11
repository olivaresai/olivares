// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { BadgeCheck } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

const ModelOpsView = lazy(() =>
  import('./model-ops-view').then((m) => ({
    default: m.ModelOpsView,
  })),
)

export const VIEWS = [
  {
    order: 440,
    id: 'modelOps',
    path: '/model-operations',
    navigation: { kind: 'feature', areaId: 'ai', sectionId: 'models' },
    helpHref: '/reference/modules/xxiii-model-operations',
    icon: BadgeCheck,
    permission: 'models:registry:read',
    element: lazyView(ModelOpsView),
  },
] satisfies ViewEntry[]
