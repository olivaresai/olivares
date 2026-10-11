// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { GraduationCap } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// The skills catalog (modules/skills): browse packs, see where each is pinned, assign and unassign.
const SkillsView = lazy(() =>
  import('./index').then((m) => ({ default: m.SkillsView })),
)

export const VIEWS = [
  {
    order: 115,
    id: 'skills',
    path: '/skills',
    navigation: {
      kind: 'feature',
      areaId: 'data-context',
      sectionId: 'capabilities',
    },
    helpHref: '/reference/console',
    icon: GraduationCap,
    permission: 'skills:catalog:read',
    element: lazyView(SkillsView),
  },
] satisfies ViewEntry[]
