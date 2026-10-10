// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Disc3 } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// Privileged session recording console (replay / verify / seal).
const RecordingsView = lazy(() =>
  import('./recordings-view').then((m) => ({
    default: m.RecordingsView,
  })),
)

export const VIEWS = [
  {
    order: 490,
    id: 'recordings',
    path: '/recordings',
    navigation: {
      kind: 'feature',
      areaId: 'observation',
      sectionId: 'audit-recordings',
    },
    savedViewsFeatureId: 'recordings',
    helpHref: '/reference/modules/recording',
    icon: Disc3,
    permission: 'recording:session:admin',
    element: lazyView(RecordingsView),
  },
] satisfies ViewEntry[]
