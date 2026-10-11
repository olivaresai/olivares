// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Siren } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

//Alerting — notify route CRUD, live route test, provisioned destinations and
// the read-only delivery log.
const AlertingView = lazy(() =>
  import('./alerting-view').then((m) => ({
    default: m.AlertingView,
  })),
)

export const VIEWS = [
  {
    order: 420,
    //Alerting — notify routes (event → destination) CRUD + live test, and the
    // read-only delivery log. Gated on the route-read perm; create/edit need write,
    // delete/test need admin (enforced server-side and mirrored inside the view).
    id: 'alerting',
    path: '/alerting',
    navigation: {
      kind: 'feature',
      areaId: 'automation',
      sectionId: 'events',
    },
    helpHref: '/reference/modules/xv-notify',
    icon: Siren,
    permission: 'notify:route:read',
    commandActions: [{ id: 'createRoute', permission: 'notify:route:write' }],
    element: lazyView(AlertingView),
  },
] satisfies ViewEntry[]
