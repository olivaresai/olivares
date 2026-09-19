// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ONE PLACE THAT TURNS AN ACTIVE TENANT INTO SOMETHING A PERSON CAN READ.
//
// ⛔ THE DEFECT THIS CLOSES WAS MEASURED, AND IT WAS A DUPLICATION, NOT AN OVERSIGHT.
//    A census of the rendered console found the scope line printing
//    `Organization 01a0b374-0d00-73a7-87e9-5641fc6c855b` three centimetres below a
//    topbar printing `Demo Estate`. Both were right about their own inputs: the
//    switcher had asked `GET /v1/m/system/orgs` and held the NAME, the scope line
//    only had `activeTenant` from the auth context, which is an id. Two renderings of
//    one fact, and the poorer one was on every screen.
//
// ⇒ So the rendering is a hook, not a copy. The switcher reads it, the scope line
//   reads it, and they cannot disagree because there is nothing to disagree about.
//
// ⛔ AND IT DOES NOT INVENT A NAME IT DOES NOT HAVE. The engine does not expose
//    organisation names to non-superadmins (minimum data), so for a member there IS no
//    name to show and the honest rendering is the short id — which is what the switcher
//    has always shown a member. A hook that fabricated a label would be worse than the
//    id it replaced.
import { useQuery } from '@tanstack/react-query'
import { systemApi } from '@/lib/api/endpoints'
import { queryKeys } from '@/lib/api/query'
import { useAuth } from '@/lib/auth/context'

/** Eight characters and an ellipsis: enough to tell two ids apart, short enough to read. */
export function shortId(id: string): string {
  return id.length > 10 ? `${id.slice(0, 8)}…` : id
}

/**
 * What to call the active organisation.
 *
 * `name` is the provisioned name when this principal may read it, and the short id
 * otherwise; `named` says which of the two it is, so a caller that wants to offer the
 * id as well (a copy affordance, a tooltip) knows whether it would be a repetition.
 * `null` when there is no active tenant at all — the caller decides what to say, because
 * "no organisation" reads differently in a switcher and in a scope line.
 *
 * The read is the switcher's own (same `queryKeys.orgs`, same 60 s staleness), so
 * mounting this in a second place costs no request.
 */
export function useTenantLabel(): {
  tenant: string | null
  name: string | null
  named: boolean
} {
  const { activeTenant, isSuperadmin } = useAuth()
  const orgs = useQuery({
    queryKey: queryKeys.orgs,
    queryFn: () => systemApi.listOrgs(),
    enabled: isSuperadmin,
    staleTime: 60_000,
  })
  if (!activeTenant) return { tenant: null, name: null, named: false }
  const match = orgs.data?.items.find((o) => o.tenant_id === activeTenant)
  return match
    ? { tenant: activeTenant, name: match.name, named: true }
    : { tenant: activeTenant, name: shortId(activeTenant), named: false }
}
