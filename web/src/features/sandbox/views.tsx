// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { FlaskConical } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

const SandboxView = lazy(() =>
  import('./sandbox-view').then((m) => ({ default: m.SandboxView })),
)

export const VIEWS = [
  {
    order: 550,
    id: 'sandbox',
    path: '/sandbox',
    navigation: { kind: 'feature', areaId: 'ai', sectionId: 'execution' },
    helpHref: '/reference/modules/xvii-sandbox',
    icon: FlaskConical,
    permission: 'sandbox:run:read',
    element: lazyView(SandboxView),
  },
] satisfies ViewEntry[]
