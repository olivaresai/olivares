// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { PackageCheck } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

const AttestationView = lazy(() =>
  import('./attestation-view').then((m) => ({
    default: m.AttestationView,
  })),
)

export const VIEWS = [
  {
    order: 630,
    id: 'attestation',
    path: '/attestation',
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'audit-recordings',
    },
    helpHref: '/how-to/verify-a-release',
    icon: PackageCheck,
    permission: 'observability:attestation:read',
    element: lazyView(AttestationView),
  },
] satisfies ViewEntry[]
