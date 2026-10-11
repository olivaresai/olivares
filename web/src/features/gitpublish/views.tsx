// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { GitBranchPlus } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// Governed Git publication (modules/gitpublish): approved targets, the push / pull request /
// merge intents with their receipts, and reconcile — never a resend — for an uncertain one.
const GitPublicationView = lazy(() =>
  import('./index').then((m) => ({ default: m.GitPublicationView })),
)

export const VIEWS = [
  {
    order: 240,
    id: 'gitPublication',
    path: '/git-publication',
    navigation: {
      kind: 'feature',
      areaId: 'deployment',
      sectionId: 'deployments',
    },
    helpHref: '/reference/modules/gitpublish',
    icon: GitBranchPlus,
    permission: 'gitpublish:target:read',
    element: lazyView(GitPublicationView),
  },
] satisfies ViewEntry[]
