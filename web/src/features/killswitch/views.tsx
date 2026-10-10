// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { OctagonAlert } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// Estate kill switch console (one-click emergency stop, dual-control
// re-enable, forced post-review, evidence pack, guardian containment rules).
const KillswitchView = lazy(() => import('./killswitch-view'))

export const VIEWS = [
  {
    order: 270,
    id: 'killswitch',
    path: '/killswitch',
    navigation: {
      kind: 'feature',
      areaId: 'security-identity',
      sectionId: 'defense',
    },
    helpHref: '/how-to/cookbook/kill-switch-drill',
    icon: OctagonAlert,
    permission: 'governance:killswitch:read',
    // ⌘K "Engage kill switch" (console remake 26.10, CONCEPT-IA command menu): opens the
    // engage form at its mandatory reason; engaging stays the form's own confirmation.
    commandActions: [
      { id: 'engage', permission: 'governance:killswitch:admin' },
    ],
    element: lazyView(KillswitchView),
  },
] satisfies ViewEntry[]
