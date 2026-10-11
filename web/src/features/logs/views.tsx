// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Logs } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

//Log Viewer — real-time engine log stream with SSE, filters, search.
// Superadmin-only system view.
const LogsView = lazy(() =>
  import('./logs-view').then((m) => ({
    default: m.LogsView,
  })),
)

export const VIEWS = [
  //Log Viewer — real-time engine log stream (SSE), with level/module
  // filters, search, pause/resume. Superadmin-only.
  {
    order: 660,
    id: 'logs',
    path: '/logs',
    navigation: {
      kind: 'feature',
      areaId: 'system',
      sectionId: 'maintenance',
    },
    helpHref: '/how-to/troubleshooting',
    // Logs, not ScrollText: ScrollText is the Claude-policy icon, and
    // lucide ships a literal Logs glyph for a log stream.
    icon: Logs,
    permission: 'system:admin',
    element: lazyView(LogsView),
  },
] satisfies ViewEntry[]
