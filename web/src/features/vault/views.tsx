// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { LockKeyhole } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// The personal vault: a person's own logins, notes and authenticator keys (core/auth/vault_item.go).
const VaultView = lazy(() =>
  import('./index').then((m) => ({ default: m.VaultView })),
)

export const VIEWS = [
  {
    order: 195,
    id: 'vault',
    path: '/vault',
    // A page of Settings > You (navigation/places.ts SETTINGS_PLACE), filed under the
    // Settings utility's own section, never under the Security area.
    navigation: {
      kind: 'feature',
      areaId: 'system',
      sectionId: 'preferences',
    },
    helpHref: '/reference/console',
    icon: LockKeyhole,
    // Every role may keep a vault; the engine admits a person to their own items only.
    permission: 'tenant:read',
    element: lazyView(VaultView),
  },
] satisfies ViewEntry[]
