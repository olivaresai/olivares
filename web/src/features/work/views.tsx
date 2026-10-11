// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { ClipboardList } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// Work cockpit — the console over the K1 durable cross-session work kernel
// (work items, dependencies, acceptance criteria and the decision record).
const WorkView = lazy(() =>
  import('./index').then((m) => ({ default: m.WorkView })),
)

export const VIEWS = [
  {
    order: 290,
    // Work cockpit — the durable cross-session backlog (K1). Gated on the base
    // work-read perm; write/admin actions gate further inside the view
    // (sessions:work:write / :admin), and the decisions tab on
    // sessions:decision:read. All six reach whoami's effective set, measured on the
    // wire by cmd/olivares/work_console_whoami_reach_test.go.
    id: 'work',
    path: '/work',
    navigation: {
      kind: 'feature',
      areaId: 'work-communications',
      sectionId: 'work',
    },
    helpHref: '/reference/modules/ii-sessions',
    icon: ClipboardList,
    permission: 'sessions:work:read',
    element: lazyView(WorkView),
  },
] satisfies ViewEntry[]
