// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { savedViewsApi } from '@/features/saved-views/api'
import { systemApi } from '@/lib/api/endpoints'
import type { Whoami } from '@/lib/api/types'

// The organization choice must be discoverable before an organization is selected.
// Keep it on a stable permitted row, independently of the active request scope.
export async function tenantPreferenceHome(
  principal: Whoami,
  guard: () => void,
) {
  guard()
  const grants = principal.grants.map((g) => g.tenant).sort()
  if (grants.length) return grants[0]
  if (!principal.superadmin) return undefined
  const orgs = await systemApi.listOrgs({
    signal: AbortSignal.timeout(5000),
    sessionEffects: 'none',
    dispatchGuard: guard,
  })
  guard()
  return orgs.items.map((org) => org.tenant_id).sort()[0]
}

export async function restoreTenant(
  principal: Whoami,
  guard: () => void,
  current: string | null = null,
) {
  let selected =
    current &&
    (principal.superadmin || principal.grants.some((g) => g.tenant === current))
      ? current
      : (principal.grants[0]?.tenant ?? null)
  if (principal.kind !== 'user') return selected
  try {
    const home = await tenantPreferenceHome(principal, guard)
    if (!home) return selected
    const state = await savedViewsApi.uiState({
      tenant: home,
      signal: AbortSignal.timeout(5000),
      sessionEffects: 'none',
      dispatchGuard: guard,
    })
    guard()
    const raw = state.preferences?.['olivares.tenant']
    const choice: unknown = raw ? JSON.parse(raw)?.state?.activeTenant : null
    if (
      typeof choice === 'string' &&
      choice &&
      (principal.grants.some((g) => g.tenant === choice) ||
        (principal.superadmin &&
          /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(
            choice,
          )))
    )
      selected = choice
  } catch {
    // Keep the permitted live choice on failure; membership supplies the default.
  }
  guard()
  return selected
}
