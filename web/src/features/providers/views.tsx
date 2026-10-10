// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { KeySquare } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

const ProvidersView = lazy(() =>
  import('./providers-view').then((m) => ({
    default: m.ProvidersView,
  })),
)

export const VIEWS = [
  {
    order: 330,
    // The credential a session launches with. It sits FIRST in the environments
    // section because it is the first thing a new operator needs and the last thing
    // the product used to offer: a profile with no credential launches nothing, and
    // the answer to "where does my API key go" used to be a variable in the server's
    // shell.
    id: 'providers',
    path: '/providers',
    navigation: {
      kind: 'feature',
      areaId: 'ai',
      sectionId: 'environments',
    },
    helpHref: '/how-to/add-a-provider',
    // KeySquare and not KeyRound: the icon guard requires one lucide glyph per view,
    // and KeyRound is the channel-administration view's. Two screens sharing a glyph
    // is how a sidebar stops being scannable.
    icon: KeySquare,
    permission: 'sessions:provider:read',
    element: lazyView(ProvidersView),
  },
] satisfies ViewEntry[]
