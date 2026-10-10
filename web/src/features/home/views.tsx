// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { LayoutDashboard } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// Estate overview — the home front door (route `/`), the real overview that
// replaced the foundation placeholder (the last one). Named export → default for lazy().
const HomeView = lazy(() =>
  import('./home-view').then((m) => ({ default: m.HomeView })),
)

export const VIEWS = [
  {
    order: 10,
    id: 'home',
    path: '/',
    navigation: { kind: 'root' },
    helpHref: '/',
    icon: LayoutDashboard,
    element: lazyView(HomeView),
  },
] satisfies ViewEntry[]
