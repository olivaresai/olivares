// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { PlugZap, SlidersHorizontal } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

// Control console (FASE X): user onboarding, SSO/IdP, workspaces &
// agent-groups, scoped admin — the configure surface hang panels off.
const ConsoleView = lazy(() => import('./console-view'))
const MCPServersView = lazy(() =>
  import('./mcp-servers-view').then((m) => ({
    default: m.MCPServersView,
  })),
)

export const VIEWS = [
  {
    order: 90,
    // Control console: the configure surface (onboard users, SSO/IdP,
    // workspaces & agent-groups, scoped admin). Gated on tenant:admin so org
    // admins/owners + superadmins see it; each tab gates its writes further.
    id: 'console',
    path: '/console',
    navigation: {
      kind: 'feature',
      areaId: 'system',
      sectionId: 'administration',
    },
    helpHref: '/reference/modules/xx-multi-tenancy',
    icon: SlidersHorizontal,
    permission: 'tenant:admin',
    element: lazyView(ConsoleView),
  },
  {
    order: 320,
    // The tenant MCP gateway's servers, beside the AI tools that use them. The same
    // surface stays in Administration as the MCP gateway tab.
    id: 'mcpServers',
    path: '/mcp-servers',
    helpHref: '/how-to/connectors/mcp-governance',
    navigation: {
      kind: 'feature',
      areaId: 'ai',
      sectionId: 'environments',
    },
    icon: PlugZap,
    permission: 'tenant:admin',
    element: lazyView(MCPServersView),
  },
] satisfies ViewEntry[]
