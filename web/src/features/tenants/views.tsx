// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Building2 } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// C07-02 Tenants — withdraw/restore a tenant's service. Superadmin-only.
const TenantsView = lazy(() =>
  import('./tenants-view').then((m) => ({
    default: m.TenantsView,
  })),
)

export const VIEWS = [
  // C07-02 Tenants — suspend and restore tenant service.
  //
  // Keep lifecycle actions separate from Data residency, even though it lists the same
  // organizations: that screen describes where data lives. Its roster also omits `status`,
  // so it cannot currently show suspended tenants.
  //
  // This is separate from C07-09: `/admin/tenants*` did not exist (404 measured against
  // a live engine on 2026-08-18). This view uses the existing `/v1/system/orgs*` routes;
  // when the other API lands, extend this view instead of creating another.
  {
    order: 670,
    id: 'tenants',
    path: '/tenants',
    navigation: {
      kind: 'feature',
      areaId: 'system',
      sectionId: 'administration',
    },
    helpHref: '/how-to/troubleshooting',
    icon: Building2,
    permission: 'system:admin',
    element: lazyView(TenantsView),
  },
] satisfies ViewEntry[]
