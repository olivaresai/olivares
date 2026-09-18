// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SESSIONS THIS PERSON IS KEEPING AN EYE ON.
//
// A pin is a preference, not a permission and not a fact about the estate: it says
// "keep this one where I can find it". So it is stored where preferences are stored in
// this console — the browser, partitioned per (origin, user, tenant), exactly the
// discipline `features/navigation/personal-navigation-store.ts` established for
// favourites, and for its reasons:
//
// ⛔ A TOKEN PRINCIPAL CAN CARRY ITS CREATOR'S user_id. IT IS NOT THAT USER. With no
//    user principal, or with no tenant, there is no partition to write into and pinning
//    is NOT OFFERED — the key is null and the rail hides the affordance rather than
//    silently writing somebody else's preference.
//
// ⛔ ONLY ROW KEYS ARE STORED, and every one is re-validated on the way out
//    (`isSessionAddress`). Nothing reconstructs a label, a URL or a payload from
//    storage: what comes back is a set of references this plane could have minted, and
//    a session that no longer exists simply never matches a row.
//
// ⛔ AND THE LIST HAS A CEILING. Storage that grows with every click is storage nobody
//    ever cleans; past the ceiling the oldest pin is dropped, which is what an operator
//    means by pinning a twenty-first thing.
import type { Whoami } from '@/lib/api/types'
import { isSessionAddress } from './session-address'

/** How many sessions one person may keep pinned, per tenant. */
export const MAX_PINS = 20

const VERSION = 1

/** Where this principal's pins live, or null when there is no partition to write to. */
export function pinStorageKey(
  origin: string,
  principal: Whoami | null,
  tenant: string | null,
): string | null {
  if (
    principal?.kind !== 'user' ||
    typeof principal.user_id !== 'string' ||
    !principal.user_id.trim() ||
    !tenant
  )
    return null
  return `olivares.sessions.pins:${JSON.stringify([origin, 'user', principal.user_id, tenant])}`
}

const EMPTY: readonly string[] = Object.freeze([])

/** Read a stored value back. Anything unexpected reads as "no pins", never as a throw. */
export function decodePins(raw: string | null): readonly string[] {
  if (!raw) return EMPTY
  try {
    const value: unknown = JSON.parse(raw)
    if (
      !value ||
      typeof value !== 'object' ||
      !('version' in value) ||
      value.version !== VERSION ||
      !('pins' in value) ||
      !Array.isArray(value.pins)
    )
      return EMPTY
    const out: string[] = []
    for (const entry of value.pins) {
      if (typeof entry !== 'string') continue
      if (!isSessionAddress(entry)) continue
      if (out.includes(entry)) continue
      out.push(entry)
      if (out.length === MAX_PINS) break
    }
    return out
  } catch {
    return EMPTY
  }
}

export function encodePins(pins: readonly string[]): string {
  return JSON.stringify({ version: VERSION, pins })
}

/**
 * Add or remove one address. Newest last, so the ceiling drops the OLDEST pin — the one
 * the operator has gone longest without needing.
 */
export function togglePin(
  pins: readonly string[],
  address: string,
): readonly string[] {
  if (!isSessionAddress(address)) return pins
  if (pins.includes(address)) return pins.filter((p) => p !== address)
  const next = [...pins, address]
  return next.length > MAX_PINS ? next.slice(next.length - MAX_PINS) : next
}
