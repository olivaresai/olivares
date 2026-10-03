// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHETHER THIS PERSON HAS OPENED COMPLIANCE. Now shows the compliance score only after
// that (HU2-25, Root 19:53Z): on an install that carries the module, the engine maps
// every framework from platform evidence by itself, and "Compliance 10% · 68 gaps" on
// the front page of someone who never asked for it read as alarm, not information.
// Kept in this browser, per organization and user: the cost of another browser is one
// tile not shown until Compliance is opened there, never a figure shown too early.
import { useEffect, useState } from 'react'
import type { Whoami } from '@/lib/api/types'
import { useAuth } from '@/lib/auth/context'

function openedKey(
  principal: Whoami | null,
  tenant: string | null,
): string | null {
  if (
    typeof window === 'undefined' ||
    principal?.kind !== 'user' ||
    typeof principal.user_id !== 'string' ||
    !principal.user_id.trim() ||
    !tenant
  )
    return null
  return `olivares.compliance.opened:${JSON.stringify([window.location.origin, 'user', principal.user_id, tenant])}`
}

function readOpened(key: string | null): boolean {
  if (!key) return false
  try {
    return window.localStorage.getItem(key) === '1'
  } catch {
    return false
  }
}

/** Whether this person has opened Compliance in this organization, in this browser. */
export function useComplianceOpened(): boolean {
  const { principal, activeTenant } = useAuth()
  const key = openedKey(principal, activeTenant)
  const [state, setState] = useState(() => ({ key, opened: readOpened(key) }))
  // Another organization or person: read their own answer. Adjusted during render, as
  // the pins hook does; an effect may not set state in this codebase.
  if (state.key !== key) setState({ key, opened: readOpened(key) })
  return state.key === key && state.opened
}

/** Called by the Compliance page: from now on Now may show its score. */
export function useMarkComplianceOpened() {
  const { principal, activeTenant } = useAuth()
  const key = openedKey(principal, activeTenant)
  useEffect(() => {
    if (!key) return
    try {
      window.localStorage.setItem(key, '1')
    } catch {
      // A blocked store: the score stays off Now, which is the quiet side.
    }
  }, [key])
}
