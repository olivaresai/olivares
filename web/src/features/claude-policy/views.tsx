// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { ScrollText } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

//Claude Code governance console (CodeMirror editors load on demand).
const ClaudePolicyView = lazy(() => import('./claude-policy-view'))

export const VIEWS = [
  {
    order: 200,
    id: 'claudePolicy',
    path: '/claude-policy',
    navigation: {
      kind: 'feature',
      areaId: 'security-identity',
      sectionId: 'policy',
    },
    helpHref: '/how-to/connectors/claude-code-hooks-pep',
    icon: ScrollText,
    permission: 'governance:claude-policy:read',
    element: lazyView(ClaudePolicyView),
  },
] satisfies ViewEntry[]
