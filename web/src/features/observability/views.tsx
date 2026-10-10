// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Radar } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// System dashboards: cross-cutting admin views over the Fase-F depth —
// observability (ingestion-health + trace drill-down), platform surfaces /
// compliance matrix + per-platform model lifecycle, the read-only rate-limit
// inventory, and supply-chain attestation. Several are honest declared-contract
// seams where the backend exposes no live API yet. Code-split.
const ObservabilityView = lazy(() =>
  import('./observability-view').then((m) => ({
    default: m.ObservabilityView,
  })),
)

export const VIEWS = [
  // Cross-cutting admin dashboards over the Fase-F depth. Each route
  // gates on its source module's existing read permission (the backend stays the
  // source of truth); the views themselves are honest about what is live vs a
  // declared-contract seam.
  {
    order: 600,
    id: 'observability',
    path: '/observability',
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'operations',
    },
    savedViewsFeatureId: 'observability',
    helpHref: '/reference/modules/observability',
    icon: Radar,
    permission: 'health:status:read',
    element: lazyView(ObservabilityView),
  },
] satisfies ViewEntry[]
