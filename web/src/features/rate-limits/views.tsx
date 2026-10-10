// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Timer } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

const RateLimitsView = lazy(() =>
  import('./rate-limits-view').then((m) => ({
    default: m.RateLimitsView,
  })),
)

export const VIEWS = [
  {
    order: 620,
    id: 'rateLimits',
    path: '/rate-limits',
    navigation: {
      kind: 'feature',
      areaId: 'ai',
      sectionId: 'provider-reference',
    },
    helpHref: '/reference/modules/x-models',
    // Timer, not Gauge: Gauge belongs to Adoption; rate limits are about
    // time windows, and every registered view must carry a unique glyph.
    icon: Timer,
    permission: 'models:ratelimits:read',
    element: lazyView(RateLimitsView),
  },
] satisfies ViewEntry[]
