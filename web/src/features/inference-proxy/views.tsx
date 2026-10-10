// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Waypoints } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

//Inference proxy — the per-tenant policy-enforcing proxy admin: config gates,
// egress DLP rules and device-authorization approvals (AAL3 on writes).
const InferenceProxyView = lazy(() =>
  import('./inference-proxy-view').then((m) => ({
    default: m.InferenceProxyView,
  })),
)

export const VIEWS = [
  {
    order: 410,
    //Inference proxy admin — config gates, egress DLP rules and device
    // approvals. Gated on the proxy config-read perm; config writes need editor,
    // DLP writes need admin, and every write requires an AAL3 step-up in the view.
    id: 'inferenceProxy',
    path: '/inference-proxy',
    navigation: {
      kind: 'feature',
      areaId: 'security-identity',
      sectionId: 'policy',
    },
    helpHref: '/reference/modules/inferenceproxy',
    icon: Waypoints,
    permission: 'inferenceproxy:config:read',
    element: lazyView(InferenceProxyView),
  },
] satisfies ViewEntry[]
