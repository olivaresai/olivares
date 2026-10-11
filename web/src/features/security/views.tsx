// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { ShieldAlert } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

const SecurityView = lazy(() =>
  import('./security-view').then((m) => ({ default: m.SecurityView })),
)

export const VIEWS = [
  {
    order: 480,
    id: 'security',
    path: '/security',
    navigation: {
      kind: 'feature',
      areaId: 'security-identity',
      sectionId: 'defense',
    },
    helpHref: '/reference/modules/ix-security',
    icon: ShieldAlert,
    permission: 'security:finding:read',
    element: lazyView(SecurityView),
  },
] satisfies ViewEntry[]
