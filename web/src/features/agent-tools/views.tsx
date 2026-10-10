// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Wrench } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// The CREDENTIAL plane, beside the profile plane above it and deliberately not
// inside it. A profile says which home an official CLI runs under; a provider says
// which credential it runs with. They have independent permission tiers, and before
// this route `sessions:provider:read` was a permission with no screen.
const AgentToolsView = lazy(() =>
  import('./agent-tools-view').then((m) => ({
    default: m.AgentToolsView,
  })),
)

export const VIEWS = [
  {
    order: 310,
    id: 'agent-tools',
    path: '/agent-tools',
    helpHref: '/how-to/add-a-provider',
    navigation: {
      kind: 'feature',
      areaId: 'ai',
      sectionId: 'environments',
    },
    icon: Wrench,
    permission: 'system:admin',
    element: lazyView(AgentToolsView),
  },
] satisfies ViewEntry[]
