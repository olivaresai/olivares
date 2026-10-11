// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Scale } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

const ComplianceView = lazy(() =>
  import('./compliance-view').then((m) => ({
    default: m.ComplianceView,
  })),
)

export const VIEWS = [
  {
    order: 510,
    id: 'compliance',
    path: '/compliance',
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'audit-recordings',
    },
    helpHref: '/reference/modules/xiii-compliance',
    // Scale, not ScrollText: ScrollText is the Claude-policy icon —
    // compliance is the scales of regulation, not a policy document.
    icon: Scale,
    permission: 'compliance:framework:read',
    element: lazyView(ComplianceView),
  },
] satisfies ViewEntry[]
