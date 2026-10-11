// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Bell } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// Eventing (webhook event subscriptions) console — outbound webhooks,
// event log, delivery tracking, and dead-letter queue with redeliver.
const EventingView = lazy(() =>
  import('./eventing-view').then((m) => ({
    default: m.EventingView,
  })),
)

export const VIEWS = [
  {
    order: 390,
    // Eventing (webhook event subscriptions) — outbound webhooks, event log,
    // delivery tracking, and dead-letter queue. Gated on the subscription-read perm;
    // write actions gate further inside the view (eventing:subscription:write).
    id: 'eventing',
    path: '/eventing',
    navigation: {
      kind: 'feature',
      areaId: 'automation',
      sectionId: 'events',
    },
    helpHref: '/reference/modules/eventing',
    icon: Bell,
    permission: 'eventing:subscription:read',
    commandActions: [
      {
        id: 'createSubscription',
        permission: 'eventing:subscription:write',
      },
    ],
    element: lazyView(EventingView),
  },
] satisfies ViewEntry[]
