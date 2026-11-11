// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import {
  administrationDeepLinkQuestion,
  administrationSurfaceQuestion,
} from './capabilities'
import { ChannelAdminContinuity } from './channel-admin-continuity'
import {
  Handshake,
  Inbox,
  KeyRound,
  MailPlus,
  MessagesSquare,
} from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// K3 first increment (I1): ONE room with FIVE doors, the pattern again. The
// catalog (`/communications`, sessions:channel:read), the personal inbox
// (`/communications/inbox`, sessions:delivery:read), channel creation
// (`/communications/new`, sessions:channel:write), handoffs and administration mount
// the same view opened on a different tab, because the engine declares those tiers
// independently: a principal holding only delivery:read must reach its own mailbox
// without the catalog, and one holding only channel:write must be able to create with
// explicit grants without reading anything. They are doors, not redirects —
// RequirePermission blocks a route on the ONE permission its entry declares.
//
// ONE ENTRY IN NAVIGATION. The catalog is the room's entry; every other door declares
// `doorTo: 'communications'`, so a person who can read channels sees "Communications"
// once and reaches the other tabs inside it, while a person who cannot still gets the
// door their own permission opens. The routes stay mounted for bookmarks and deep links.
const CommunicationsView = lazy(() =>
  import('./index').then((m) => ({
    default: m.CommunicationsView,
  })),
)

export const VIEWS = [
  {
    order: 130,
    // K3 I1 — the CATALOG door: visible channels and the channel card, gated on the
    // read tier the engine requires on `GET /channels` and `GET /channels/{id}`.
    // Sending gates inside on sessions:message-send:write and the channel's own bits.
    id: 'communications',
    path: '/communications',
    navigation: {
      kind: 'feature',
      areaId: 'work-communications',
      sectionId: 'work',
    },
    helpHref: '/reference/modules/ii-sessions',
    icon: MessagesSquare,
    permission: 'sessions:channel:read',
    changes: [
      { permission: 'sessions:channel:write' },
      { permission: 'sessions:message-send:write' },
      { permission: 'sessions:run:write' },
    ],
    element: lazyView(CommunicationsView, { entrance: 'catalog' as const }),
  },
  {
    order: 140,
    // K3 I1 — the INBOX door: the exact personal mailbox, delivery and message reads
    // and the explicit Ack. Its own permission because the engine declares
    // sessions:delivery:read independently of channel:read; message reads gate on
    // sessions:message:read and the Ack on sessions:delivery:write inside.
    id: 'communicationsInbox',
    path: '/communications/inbox',
    doorTo: 'communications',
    navigation: {
      kind: 'feature',
      areaId: 'work-communications',
      sectionId: 'work',
    },
    helpHref: '/reference/modules/ii-sessions',
    icon: Inbox,
    permission: 'sessions:delivery:read',
    changes: [
      { permission: 'sessions:delivery:write' },
      { permission: 'sessions:message-send:write' },
    ],
    element: lazyView(CommunicationsView, { entrance: 'inbox' as const }),
  },
  {
    order: 150,
    // K3 I1 — the CREATE door: `POST /channels` with explicit initial grants, usable
    // by a principal that cannot read the catalog at all.
    id: 'communicationsNew',
    path: '/communications/new',
    doorTo: 'communications',
    navigation: {
      kind: 'feature',
      areaId: 'work-communications',
      sectionId: 'work',
    },
    helpHref: '/reference/modules/ii-sessions',
    icon: MailPlus,
    permission: 'sessions:channel:write',
    element: lazyView(CommunicationsView, { entrance: 'new' as const }),
  },
  {
    order: 160,
    // K3 I3 — the Handoffs door: the personal page of work-responsibility offers
    // addressed to this principal, the protected offer context behind each one and
    // the accept/reject response. Its own route because the personal collection is
    // a `sessions:delivery:read` surface a principal may hold without the catalog,
    // like the ordinary inbox beside it; responding is gated apart, on
    // `sessions:handoff-response:write`, where the act happens. The icon is
    // distinct from the inbox's because every registered view needs its own glyph.
    id: 'communicationsHandoffs',
    path: '/communications/handoffs',
    doorTo: 'communications',
    navigation: {
      kind: 'feature',
      areaId: 'work-communications',
      sectionId: 'work',
    },
    helpHref: '/reference/modules/ii-sessions',
    icon: Handshake,
    permission: 'sessions:delivery:read',
    changes: [{ permission: 'sessions:handoff-response:write' }],
    element: lazyView(CommunicationsView, {
      entrance: 'handoffs' as const,
    }),
  },
  {
    order: 170,
    // K3 I2 — the ADMINISTRATION door: the administrable catalog
    // (`GET /channels/administration`), the grant history, `PATCH /channels`, grant
    // and revoke. Its own permission because the engine declares
    // sessions:channel:admin independently of channel:read: a principal holding
    // core admin and a local admin bit — and no local read bit — must reach it
    // without the catalog. The engine decides the local bit on every read.
    id: 'communicationsAdministration',
    path: '/communications/administration',
    doorTo: 'communications',
    navigation: {
      kind: 'feature',
      areaId: 'work-communications',
      sectionId: 'work',
    },
    helpHref: '/reference/modules/ii-sessions',
    icon: KeyRound,
    // ⛔ KEPT, AND IT NO LONGER DECIDES. `sessions:channel:admin` is a tenant-wide
    //    membership fact, and this door's authority is not: it may be held through a
    //    workspace-scoped authored grant the permission set never names, and it may be
    //    reflected here while an authored policy forbids the same operation. So the
    //    engine is asked (`capability` below) and this string stays for what it still
    //    truthfully is — the reflection, read by every unmigrated consumer and by the
    //    census that proves the console never asks for a permission the engine does not
    //    declare. Removing it would not tighten anything; it would delete the record.
    permission: 'sessions:channel:admin',
    capability: {
      surface: administrationSurfaceQuestion,
      deepLink: administrationDeepLinkQuestion,
    },
    // The one view whose answer expires on a budget while an operator is typing into it.
    // The boundary holds their touched fields and nothing else, above the cut that
    // rebuilds this room every few seconds; see channel-admin-continuity.tsx.
    continuity: ChannelAdminContinuity,
    element: lazyView(CommunicationsView, {
      entrance: 'administration' as const,
    }),
  },
] satisfies ViewEntry[]

/** The registered page of a route above; a wrong id fails as the module loads. */
function pageOf(id: string): string {
  const view = VIEWS.find((v) => v.id === id)
  if (!view) throw new Error(`no communications route ${id}`)
  return view.path
}

/** The page of each tab of the room, so that leaving /communications/new lands
 * on the page whose title is the list it shows. */
export const TAB_PAGES = {
  channels: pageOf('communications'),
  inbox: pageOf('communicationsInbox'),
  handoffs: pageOf('communicationsHandoffs'),
  create: pageOf('communicationsNew'),
  administration: pageOf('communicationsAdministration'),
}
