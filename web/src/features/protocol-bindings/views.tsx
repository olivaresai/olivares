// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Cable } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

const ProtocolBindingsView = lazy(() =>
  import('./index').then((m) => ({
    default: m.ProtocolBindingsView,
  })),
)

export const VIEWS = [
  {
    order: 120,
    id: 'protocolBindings',
    path: '/communications/protocol-bindings',
    navigation: {
      kind: 'feature',
      areaId: 'work-communications',
      sectionId: 'work',
    },
    helpHref: '/reference/modules/ii-sessions',
    icon: Cable,
    permission: 'sessions:protocol-binding:read',
    element: lazyView(ProtocolBindingsView),
  },
] satisfies ViewEntry[]
