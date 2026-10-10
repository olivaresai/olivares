// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { ContactRound, IdCard, Link2 } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// Profiles, bindings and accounts share one view with independent read tiers.
// Each route opens its own tab so an operator can reach the permissions they hold
// without requiring access to runs, live sessions or another provider tab.
const ProviderAdminView = lazy(() =>
  import('./provider-admin-view').then((m) => ({
    default: m.ProviderAdminView,
  })),
)

export const VIEWS = [
  {
    order: 340,
    // The provider-profile plane's own door, gated on ITS read tier. The plane is
    // also a tab inside `/agentops` and `/sessions`, but those routes require run:read
    // or live:read, so a principal holding only sessions:profile:read could reach no
    // screen for a permission the engine declares. Same view as the next entry, opened
    // on the profiles tab; write/admin actions gate further inside (sessions:profile:
    // write/admin). Two doors into one room — not a redirect, and the generic route
    // guard keeps declaring exactly one permission per entry.
    id: 'providerProfiles',
    path: '/provider-profiles',
    navigation: {
      kind: 'feature',
      areaId: 'ai',
      sectionId: 'environments',
    },
    helpHref: '/reference/modules/ii-sessions',
    icon: IdCard,
    permission: 'sessions:profile:read',
    element: lazyView(ProviderAdminView, { entrance: 'profiles' as const }),
  },
  {
    order: 350,
    // The source-binding door, gated on the binding plane's OWN read tier, which is
    // independent of the profile tiers. Opens on the tenant-wide bindings table; bind
    // and revoke gate on sessions:profile-binding:write/admin inside, and binding also
    // needs the deployment-wide source authority the engine decides on the roster read.
    id: 'providerBindings',
    path: '/provider-bindings',
    navigation: {
      kind: 'feature',
      areaId: 'ai',
      sectionId: 'environments',
    },
    helpHref: '/reference/modules/ii-sessions',
    icon: Link2,
    permission: 'sessions:profile-binding:read',
    element: lazyView(ProviderAdminView, { entrance: 'bindings' as const }),
  },
  {
    order: 360,
    // Account read opens this tab; creating or adopting an account still needs
    // account write, and choosing from profiles needs profile read inside it.
    id: 'providerAccounts',
    path: '/provider-accounts',
    navigation: {
      kind: 'feature',
      areaId: 'ai',
      sectionId: 'environments',
    },
    helpHref: '/reference/modules/ii-sessions',
    icon: ContactRound,
    permission: 'sessions:account:read',
    element: lazyView(ProviderAdminView, { entrance: 'accounts' as const }),
  },
] satisfies ViewEntry[]
