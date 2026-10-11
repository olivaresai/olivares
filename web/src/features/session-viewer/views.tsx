// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Play } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// Session recording viewer — detail page reached from RecordingsView rows.
const SessionViewerPage = lazy(() =>
  import('./session-viewer-page').then((m) => ({
    default: m.SessionViewerPage,
  })),
)

export const VIEWS = [
  {
    order: 500,
    // Session recording viewer — detail page reached by clicking a row in
    // RecordingsView. Not a sidebar entry; navigation is deep-link only.
    id: 'session-viewer',
    path: '/session-viewer/$id',
    navigation: {
      kind: 'detail',
      areaId: 'observation',
      sectionId: 'audit-recordings',
      parentViewId: 'recordings',
    },
    helpHref: '/reference/modules/recording',
    icon: Play,
    permission: 'recording:session:admin',
    element: lazyView(SessionViewerPage),
    hideInNav: true,
  },
] satisfies ViewEntry[]
