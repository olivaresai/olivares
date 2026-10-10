// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { lazy } from 'react'
import { CalendarCog, CloudUpload, ShieldCheck } from 'lucide-react'
import { lazyView } from '@/features/lazy-view'
import type { ViewEntry } from '@/features/registry'

const GovernanceView = lazy(() => import('./governance-view'))
//(plan 3.6) routine-policy console: the operator surface for the
// controls the plane has ENFORCED since. Its own route rather than a sixth
// tab under /permissions, because it carries its own permission pair
// (governance:routine:read / :admin) — behind the identity-gated route an
// operator holding only the routine perms could not reach it at all.
const RoutinePoliciesView = lazy(() => import('./routine-policies-view'))
//AgentCore Cedar export (engine). Its own route for the SAME reason
// the routine policies above have one, and it is not a style preference: the
// engine declares governance:agentcore-export:admin in Module.Permissions()
// (governance.go:397), which is what makes a permission independently grantable
// — the note beside it exists because undeclared route permissions "would
// have stayed undelegable". Hanging this off the identity-gated /permissions
// view would have put an operator holding exactly the delegated export
// permission behind a governance:identity:read wall and made the surface
// unreachable for the one principal it was delegated to.
const AgentCoreExportView = lazy(() => import('./agentcore-export-route'))

export const VIEWS = [
  {
    order: 180,
    id: 'permissions',
    path: '/permissions',
    navigation: {
      kind: 'feature',
      areaId: 'security-identity',
      sectionId: 'access',
    },
    helpHref: '/reference/modules/vi-governance',
    icon: ShieldCheck,
    permission: 'governance:identity:read',
    element: lazyView(GovernanceView),
  },
  {
    order: 210,
    //(plan 3.6) routine governance: cadence floors, concurrency caps,
    // approval requirements, cron allowlists and blocked environments for
    // Claude Code Routines. Gated on governance:routine:read — the same RBAC
    // the six engine routes enforce (governance.go:528-533); authoring gates
    // separately on governance:routine:admin inside the view.
    id: 'routinePolicies',
    path: '/routine-policies',
    navigation: {
      kind: 'feature',
      areaId: 'security-identity',
      sectionId: 'policy',
    },
    helpHref: '/reference/modules/vi-governance',
    // CalendarCog, not Timer (taken by Orchestration) and not Workflow: this is
    // governance OVER a schedule, not the schedule itself.
    icon: CalendarCog,
    permission: 'governance:routine:read',
    element: lazyView(RoutinePoliciesView),
  },
  {
    order: 220,
    //the console half of the AgentCore Cedar export. Both engine
    // routes require governance:agentcore-export:admin (governance.go:563-564):
    // planning reads remote AWS policy metadata and applying mutates the remote
    // engine, so there is no read tier to gate on and the ADMIN permission is
    // the honest gate. The registry already carries admin-gated entries for the
    // same reason (system:admin at :453, tenant:admin at :536,
    // recording:session:admin at :794) — a nav permission is "what this
    // principal may reach", not "a :read suffix".
    id: 'agentcoreExport',
    path: '/agentcore-export',
    navigation: {
      kind: 'feature',
      areaId: 'security-identity',
      sectionId: 'policy',
    },
    helpHref: '/reference/modules/vi-governance',
    // CloudUpload, not Upload (a plain upload) and not ShieldCheck (taken by
    // Permissions): this pushes local policy OUT to a remote cloud engine.
    icon: CloudUpload,
    permission: 'governance:agentcore-export:admin',
    element: lazyView(AgentCoreExportView),
  },
] satisfies ViewEntry[]
