// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { Activity, Terminal } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

//ONE destination for sessions, whichever way they reached the plane. Both
// `/sessions` (observe) and `/agentops` (operate) mount this view and open the SAME
// card, so an operator no longer has to know whether a session was DISCOVERED or
// LAUNCHED in order to pick a nav section. Each route keeps its own path, permission
// and nav entry — they are two doors into one room, NOT redirects: RequirePermission
// blocks a route on the one permission its entry declares, so pointing `/agentops` at
// `/sessions` would hand a run-only operator a Forbidden page instead of their runs.
const SessionsWorkspaceView = lazy(() =>
  import('./sessions-workspace-view').then((m) => ({
    default: m.SessionsWorkspaceView,
  })),
)

export const VIEWS = [
  {
    order: 50,
    //the OBSERVE door into the unified sessions room. Same view and same card
    // as `/agentops`; this entrance keeps the visibility framing and the live-read
    // permission the observed half needs.
    id: 'sessions',
    path: '/sessions',
    navigation: { kind: 'feature', areaId: 'ai', sectionId: 'sessions' },
    helpHref: '/reference/modules/ii-sessions',
    icon: Activity,
    permission: 'sessions:live:read',
    element: lazyView(SessionsWorkspaceView, {
      entrance: 'observe' as const,
    }),
  },
  {
    order: 300,
    //Claude Code operate portal (FASE V) unified. Gated on the base
    // run-read perm; create/stop/cleanup actions gate further inside the view
    // (sessions:run:write/admin). Same view and same card as `/sessions`: this
    // entrance keeps the operate framing (launch, workspaces) and its own permission,
    // so nothing an operator could reach before became unreachable.
    id: 'agentops',
    path: '/agentops',
    doorTo: 'sessions',
    // The door into the same screen, for a principal who may read only runs.
    navigation: { kind: 'feature', areaId: 'ai', sectionId: 'sessions' },
    helpHref: '/how-to/run-claude-code-with-olivares',
    icon: Terminal,
    permission: 'sessions:run:read',
    element: lazyView(SessionsWorkspaceView, {
      entrance: 'operate' as const,
    }),
  },
] satisfies ViewEntry[]
