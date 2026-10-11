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

// K3 first increment (I1): ONE room with THREE doors, the pattern again. The
// catalog (`/communications`, sessions:channel:read), the personal inbox
// (`/communications/inbox`, sessions:delivery:read) and channel creation
// (`/communications/new`, sessions:channel:write) mount the same view opened on a
// different tab, because the engine declares those three tiers independently: a
// principal holding only delivery:read must reach its own mailbox without the
// catalog, and one holding only channel:write must be able to create with explicit
// grants without reading anything. They are doors, not redirects — RequirePermission
// blocks a route on the ONE permission its entry declares.
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
    navigation: {
      kind: 'feature',
      areaId: 'work-communications',
      sectionId: 'work',
    },
    helpHref: '/reference/modules/ii-sessions',
    icon: Inbox,
    permission: 'sessions:delivery:read',
    element: lazyView(CommunicationsView, { entrance: 'inbox' as const }),
  },
  {
    order: 150,
    // K3 I1 — the CREATE door: `POST /channels` with explicit initial grants, usable
    // by a principal that cannot read the catalog at all.
    id: 'communicationsNew',
    path: '/communications/new',
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
    navigation: {
      kind: 'feature',
      areaId: 'work-communications',
      sectionId: 'work',
    },
    helpHref: '/reference/modules/ii-sessions',
    icon: Handshake,
    permission: 'sessions:delivery:read',
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
