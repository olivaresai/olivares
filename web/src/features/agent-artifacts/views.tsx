// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { PackageSearch } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

const AgentArtifactsView = lazy(() => import('./agent-artifacts-view'))

export const VIEWS = [
  {
    order: 370,
    // Agent-artifact supply chain. This is tenant-estate metadata and its
    // own models.agent_aibom ledger, not the lineage of one owned model.
    id: 'agentArtifacts',
    path: '/agent-artifacts',
    navigation: {
      kind: 'feature',
      areaId: 'data-context',
      sectionId: 'knowledge',
    },
    helpHref: '/reference/modules/xxiii-model-operations',
    icon: PackageSearch,
    permission: 'models:registry:read',
    element: lazyView(AgentArtifactsView),
  },
] satisfies ViewEntry[]
