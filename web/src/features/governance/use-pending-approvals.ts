// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PENDING APPROVALS, read once for the shell (the sidebar's Approvals count) and for Now
// ("Needs you"). It is the approval queue's own read with its own status filter, under the
// queue's own cache key, so the queue, the count and Now share one answer and one poll.
import { useQuery } from '@tanstack/react-query'
import { useAuth } from '@/lib/auth/context'
import { governanceApi, governanceKeys } from './api'

/** The permission the approval queue needs (governance-view.tsx `canReadApprovals`). */
export const APPROVAL_READ = 'governance:approval:read'
const PENDING = { status: 'pending' } as const
/** A glance, not a work surface: the queue itself polls faster while it is open. */
const PENDING_POLL_MS = 30_000

export function usePendingApprovals() {
  const { can, activeTenant } = useAuth()
  const permitted = can(APPROVAL_READ)
  const query = useQuery({
    queryKey: governanceKeys.approvals(activeTenant, PENDING),
    queryFn: () => governanceApi.listApprovals(PENDING),
    enabled: permitted && !!activeTenant,
    refetchInterval: PENDING_POLL_MS,
  })
  return { permitted, query }
}

/** The sidebar's count for a page of pending approvals: none for an empty queue (an empty
 * queue asks nothing of anyone), the page's size otherwise, with "+" when there are more. */
export function pendingCount(
  page: { items: readonly unknown[]; has_more?: boolean } | undefined,
): string | undefined {
  if (!page || (page.items.length === 0 && !page.has_more)) return undefined
  return `${page.items.length}${page.has_more ? '+' : ''}`
}
