// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ContactRound, IdCard, Link2 } from 'lucide-react'
import { toolsRoom } from '@/features/agent-tools/views'
import type { ViewEntry } from '@/features/registry'

// The profile, binding and account pages are doors into Tools (#2552): an account and
// its profile are one reference, and its settings and bindings are in its detail there.
// Each door keeps its url and its OWN read tier (the route guard checks exactly the one
// permission an entry declares), and navigation lists a door only to a principal who
// cannot open Tools.
export const VIEWS = [
  {
    order: 340,
    // The profile tier's door. A profile is read in its account's detail on Tools; a
    // reader without account read gets the profile list there on its own.
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
    doorTo: 'agent-tools',
    element: toolsRoom,
  },
  {
    order: 350,
    // The binding tier's door, independent of the profile tiers. An account's bindings
    // are in its detail on Tools; a reader without profile read gets the bindings table
    // there on its own. Bind and revoke gate on their own write/admin tiers.
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
    doorTo: 'agent-tools',
    element: toolsRoom,
  },
  {
    order: 360,
    // The account tier's door. Creating or adopting an account still needs account
    // write, and choosing from profiles needs profile read inside it.
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
    doorTo: 'agent-tools',
    element: toolsRoom,
  },
] satisfies ViewEntry[]
