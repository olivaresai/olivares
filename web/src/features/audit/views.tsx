// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { FileCheck2 } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

//Audit / Evidence Explorer — the tamper-evident ledger over the CORE /v1/audit
// surface (list, verify chain + checkpoints, export to WORM/SIEM, Ed25519 pubkey).
const AuditView = lazy(() =>
  import('./index').then((m) => ({ default: m.AuditView })),
)

export const VIEWS = [
  {
    order: 70,
    //Audit / Evidence Explorer over the core ledger (/v1/audit). Gated on
    // audit:read (the same RBAC the backend enforces); export/verify gate the same
    // perm server-side, the superadmin system-ledger toggle is hidden otherwise.
    id: 'audit',
    path: '/audit',
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'audit-recordings',
    },
    savedViewsFeatureId: 'audit',
    helpHref: '/reference/modules/ix-security',
    icon: FileCheck2,
    permission: 'audit:read',
    element: lazyView(AuditView),
  },
] satisfies ViewEntry[]
