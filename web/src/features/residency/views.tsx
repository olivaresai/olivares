// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Globe } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

//Data residency — set/clear each org's region pin with a two-step
// confirm + AAL3 step-up. Superadmin-only system view.
const ResidencyView = lazy(() =>
  import('./residency-view').then((m) => ({
    default: m.ResidencyView,
  })),
)

export const VIEWS = [
  //Data residency — org region pin set/clear with two-step confirm +
  // AAL3. Superadmin-only; the org roster + region PUT are authzSystem routes.
  {
    order: 680,
    id: 'residency',
    path: '/residency',
    navigation: {
      kind: 'feature',
      areaId: 'security-identity',
      sectionId: 'policy',
    },
    helpHref: '/reference/modules/xiii-compliance',
    icon: Globe,
    permission: 'system:admin',
    element: lazyView(ResidencyView),
  },
] satisfies ViewEntry[]
