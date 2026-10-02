// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The pins of the principal who is acting NOW. The rules, the partition and the
// ceiling are in `session-pins.ts`, pure; this is the React side of them.
import { useCallback, useState } from 'react'
import { useAuth } from '@/lib/auth/context'
import {
  decodePins,
  encodePins,
  pinStorageKey,
  togglePin,
} from './session-pins'

export interface SessionPins {
  /** The pinned addresses, for the rail's ordering. */
  pinned: ReadonlySet<string>
  /**
   * Pin or unpin one session — null when there is nowhere to write (a token
   * principal, or no tenant). The rail reads the NULL as "do not offer it": a control
   * that silently does nothing is worse than one that is not there.
   */
  toggle: ((address: string) => void) | null
}

function read(key: string | null): readonly string[] {
  if (!key || typeof window === 'undefined') return []
  try {
    return decodePins(window.localStorage.getItem(key))
  } catch {
    // Private mode, a disabled store, a quota error: no pins, and nothing thrown at
    // the operator over a preference.
    return []
  }
}

export function useSessionPins(): SessionPins {
  const { principal, activeTenant } = useAuth()
  const key =
    typeof window === 'undefined'
      ? null
      : pinStorageKey(window.location.origin, principal, activeTenant)

  const [state, setState] = useState<{
    key: string | null
    pins: readonly string[]
  }>(() => ({ key, pins: read(key) }))

  // The partition moved — another tenant, another principal, or an identity that may
  // not have one at all. Re-read from the NEW key rather than carrying the previous
  // principal's pins across. Adjusted during render: the change is observable here,
  // and an effect may not set state in this codebase.
  if (state.key !== key) setState({ key, pins: read(key) })

  // ⛔ THE WRITE IS NOT INSIDE THE STATE UPDATER. An updater must be pure: React's
  //    StrictMode (the development build) runs it twice, and with the write inside it the
  //    second run read the first run's pin and removed it, so the pin showed and never
  //    reached storage (WEB2, 09b capture r4: stored {"pins":[]} after Pin).
  const toggle = useCallback(
    (address: string) => {
      if (!key) return
      // Re-read before writing: two tabs share one origin, and the last writer of a
      // whole array silently discards the other's pin.
      const next = togglePin(read(key), address)
      try {
        window.localStorage.setItem(key, encodePins([...next]))
      } catch {
        // Out of quota or blocked: the pin does not survive the reload, and saying
        // so with a toast would interrupt an operator over a bookmark.
      }
      setState({ key, pins: next })
    },
    [key],
  )

  return {
    pinned: new Set(state.key === key ? state.pins : []),
    toggle: key ? toggle : null,
  }
}
