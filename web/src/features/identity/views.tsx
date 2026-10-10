// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Fingerprint } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// Identity & NHI console (WebGL WIF graph loads on demand).
const IdentityView = lazy(() => import('./identity-view'))

export const VIEWS = [
  {
    order: 190,
    id: 'identity',
    path: '/identity',
    navigation: {
      kind: 'feature',
      areaId: 'security-identity',
      sectionId: 'access',
    },
    helpHref: '/reference/modules/vi-governance',
    icon: Fingerprint,
    permission: 'governance:identity:read',
    // ⌘K "Invite people" (console remake 26.10): People with its onboarding dialog open
    // in invite mode. The engine's authority for that write is membership:write.
    commandActions: [{ id: 'invite', permission: 'membership:write' }],
    element: lazyView(IdentityView),
  },
] satisfies ViewEntry[]
