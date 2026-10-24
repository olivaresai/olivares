// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Wrench } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// ONE TOOLS PLACE (#2552): each tool, who signs in with what, and the accounts and API
// keys sessions run as. The API key, account, profile and binding pages are doors into
// this room (their own views.tsx), each under its own read tier, so the room is exported
// once and every door mounts the same element.
const ToolsRoom = lazy(() =>
  import('./agent-tools-view').then((m) => ({
    default: m.AgentToolsView,
  })),
)
export const toolsRoom = lazyView(ToolsRoom)

export const VIEWS = [
  {
    order: 310,
    id: 'agent-tools',
    path: '/agent-tools',
    helpHref: '/how-to/add-a-provider',
    navigation: {
      kind: 'feature',
      areaId: 'ai',
      sectionId: 'environments',
    },
    icon: Wrench,
    // Every member sees the accounts and API keys sessions run as. The install side
    // (inventory, versions, login probes, sign-in) stays the deployment administrator's,
    // inside the room.
    permission: 'sessions:account:read',
    element: toolsRoom,
  },
] satisfies ViewEntry[]
