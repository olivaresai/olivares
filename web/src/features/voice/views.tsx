// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { AudioLines } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

const VoiceView = lazy(() =>
  import('./voice-view').then((m) => ({ default: m.VoiceView })),
)

export const VIEWS = [
  {
    order: 540,
    id: 'voice',
    path: '/voice',
    navigation: { kind: 'feature', areaId: 'ai', sectionId: 'execution' },
    helpHref: '/reference/modules/xvi-voice',
    icon: AudioLines,
    permission: 'voice:session:read',
    element: lazyView(VoiceView),
  },
] satisfies ViewEntry[]
