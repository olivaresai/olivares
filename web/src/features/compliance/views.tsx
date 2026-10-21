// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Archive } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

const RetentionView = lazy(() =>
  import('./retention-page').then((m) => ({ default: m.RetentionView })),
)

/** The Community part of the compliance module; the Compliance page itself is Business (business-views.tsx). */
export const VIEWS = [
  {
    order: 515,
    id: 'retention',
    path: '/retention',
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'audit-recordings',
    },
    helpHref: '/reference/modules/xiii-compliance',
    icon: Archive,
    permission: 'compliance:retention:read',
    element: lazyView(RetentionView),
  },
] satisfies ViewEntry[]
