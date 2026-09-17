// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { identityKeys } from './api'

/** True only when the current authenticated whoami field is exactly false.
 *  A missing field, failed whoami, or absent principal is not unconfigured. */
export function isPivKnownUnconfigured(
  principal:
    | {
        authentication_configuration?: { piv_configured?: boolean }
      }
    | null
    | undefined,
): boolean {
  return principal?.authentication_configuration?.piv_configured === false
}

/** Bind the PIV status read to the current principal and credential generation. */
export function pivStatusQueryKey(
  tenant: string | null,
  principal:
    | {
        kind?: string
        user_id?: string
        actor?: string
      }
    | null
    | undefined,
  credentialGeneration: number,
) {
  return [
    ...identityKeys.piv(tenant),
    principal?.kind ?? null,
    principal?.user_id ?? null,
    principal?.actor ?? null,
    credentialGeneration,
  ] as const
}
