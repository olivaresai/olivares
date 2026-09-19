// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef } from 'react'
import {
  useAuthBoundary,
  type AuthBoundary,
} from '@/features/agentops/auth-boundary'
import { providerKeys } from './api'

/**
 * The provider plane's view of the AUTHORITY BOUNDARY.
 *
 * It does not re-derive the boundary: `useAuthBoundary` already computes the epoch
 * from (principal, tenant, credential generation), and two derivations of one fact
 * drift. What it adds is this plane's OWN cleanup — the shared hook cancels and
 * removes the profile plane's entries for the boundary that left, and it cannot
 * know about ours.
 *
 * Without this, a provider list read for one operator would sit in the cache while
 * the next operator's read was in flight, which is exactly the state the shared
 * hook exists to prevent for the plane beside it.
 */
export function useProviderBoundary(): AuthBoundary {
  const boundary = useAuthBoundary()
  const queryClient = useQueryClient()
  const previous = useRef<{ tenant: string | null; epoch: number } | null>(null)
  useEffect(() => {
    const prev = previous.current
    if (prev && prev.epoch !== boundary.epoch) {
      const scope = providerKeys.boundaryScope(prev.tenant, prev.epoch)
      void queryClient.cancelQueries({ queryKey: scope })
      queryClient.removeQueries({ queryKey: scope })
    }
    previous.current = { tenant: boundary.tenant, epoch: boundary.epoch }
  }, [boundary, queryClient])
  return boundary
}
