// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Code2 } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

//API Playground — interactive try-it console for the control-plane API.
const ApiPlaygroundView = lazy(() =>
  import('./api-playground-view').then((m) => ({
    default: m.ApiPlaygroundView,
  })),
)

export const VIEWS = [
  {
    order: 640,
    id: 'apiPlayground',
    path: '/api-playground',
    navigation: {
      kind: 'feature',
      areaId: 'system',
      sectionId: 'development',
    },
    helpHref: '/reference/modules/xix-api-manage-as-code',
    icon: Code2,
    permission: 'tenant:admin',
    element: lazyView(ApiPlaygroundView),
  },
] satisfies ViewEntry[]
