// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Cpu } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// Intelligence views, code-split (charts / React Flow load on demand). These
// export named components, so map them to a default for lazy().
const ModelsView = lazy(() =>
  import('./models-view').then((m) => ({ default: m.ModelsView })),
)

export const VIEWS = [
  {
    order: 430,
    id: 'models',
    path: '/models',
    navigation: { kind: 'feature', areaId: 'ai', sectionId: 'models' },
    helpHref: '/reference/modules/x-models',
    icon: Cpu,
    permission: 'models:catalog:read',
    changes: [
      { permission: 'models:gpai:write' },
      { permission: 'models:keys:write' },
      { permission: 'models:model-access:admin' },
      { permission: 'models:routing:write' },
      { permission: 'models:routing:admin' },
      { permission: 'sessions:profile:write' },
      { permission: 'sessions:provider:write' },
    ],
    element: lazyView(ModelsView),
  },
] satisfies ViewEntry[]
