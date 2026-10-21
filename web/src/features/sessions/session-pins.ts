// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SESSIONS THIS PERSON KEEPS FIRST.
//
// A pin is the signed-in person's own mark on a session, and it is KEPT BY THE ENGINE
// (`modules/sessions/run_pin.go`), not by this browser: it follows the person to another
// browser or machine, serves `olivares session ls`, and never changes anyone else's list.
// The engine says which sessions are pinned on the runs it already returns (`pinned`,
// `pinned_at` on `RunDTO`), so this file only turns those fields into the set the rail
// sorts by.
//
// ⛔ A PIN NAMES A RUN. A session the plane only observes (no run launched through
//    Olivares) has no run to hang a pin on, so it is not offered one (`isPinnable`) —
//    a control that silently does nothing is worse than one that is not there.
//
// ⛔ ONE SESSION, TWO NAMES. A launched session is `run:<ref>` until the plane proves its
//    managed live row and `live:<ref>` after (`provenance.ts`). The engine keeps the pin
//    on the run, so both names read it, and `pinnedKeys` returns the row's own key
//    whichever name it has now: a pin never goes missing across that change.
//
// ⛔ PINS KEPT IN THIS BROWSER BEFORE THE ENGINE KEPT THEM ARE STORED DATA. They are read
//    here (`decodePins`) only so `use-session-pins.ts` can move them to the engine once;
//    nothing writes that key any more. Only session addresses are read back, each
//    re-validated (`isSessionAddress`), and the list has the engine's ceiling.
import type { Whoami } from '@/lib/api/types'
import type { UnifiedSession } from './provenance'
import { isSessionAddress } from './session-address'

/** How many sessions one person keeps pinned: the engine's ceiling (`maxRunPins`), and the
 * one this browser kept before it. */
export const MAX_PINS = 20

const BROWSER_PINS_VERSION = 1

/** Where one named browser preference of this principal lives, or null when there is no
 * partition to write to (a token principal, or no tenant). The model favorites use it. */
export function preferenceStorageKey(
  name: string,
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
  return `${name}:${JSON.stringify([origin, 'user', principal.user_id, tenant])}`
}

/** Where this browser kept the principal's pins before the engine did, or null when
 * there was no partition to write to (a token principal, or no tenant). */
export function pinStorageKey(
  origin: string,
  principal: Whoami | null,
  tenant: string | null,
): string | null {
  return preferenceStorageKey(
    'olivares.sessions.pins',
    origin,
    principal,
    tenant,
  )
}

/** Read a stored value back, oldest pin first. Anything unexpected reads as "no pins",
 * never as a throw. */
export function decodePins(raw: string | null): readonly string[] {
  if (!raw) return []
  try {
    const value: unknown = JSON.parse(raw)
    if (
      !value ||
      typeof value !== 'object' ||
      !('version' in value) ||
      value.version !== BROWSER_PINS_VERSION ||
      !('pins' in value) ||
      !Array.isArray(value.pins)
    )
      return []
    const out: string[] = []
    for (const entry of value.pins) {
      if (typeof entry !== 'string' || !isSessionAddress(entry)) continue
      if (out.includes(entry)) continue
      out.push(entry)
      if (out.length === MAX_PINS) break
    }
    return out
  } catch {
    return []
  }
}

export function encodePins(pins: readonly string[]): string {
  return JSON.stringify({ version: BROWSER_PINS_VERSION, pins })
}

/** Whether a pin can be kept for this session: the engine keeps it against a run, and a
 * released run is out of every list for good. */
export function isPinnable(session: Pick<UnifiedSession, 'runs'>): boolean {
  return session.runs.some((run) => run.state !== 'cleaned')
}

/**
 * Whether this session is pinned, from the list's set or from its own runs. The open
 * card can be a session the list does not hold (a deep link past the page), and its
 * runs carry the engine's answer either way.
 */
export function isPinned(
  pinned: ReadonlySet<string>,
  session: Pick<UnifiedSession, 'key' | 'runs'>,
): boolean {
  return (
    pinned.has(session.key) || session.runs.some((run) => run.pinned === true)
  )
}

/**
 * The keys of the sessions this person has pinned, in the order they pinned them (oldest
 * pin first), which is the order the pinned section lists them in. A session with several
 * runs is pinned when any run is, from the earliest of those pins.
 */
export function pinnedKeys(
  sessions: readonly Pick<UnifiedSession, 'key' | 'runs'>[],
): ReadonlySet<string> {
  const pins: { key: string; at: string }[] = []
  for (const session of sessions) {
    const stamps = session.runs
      .filter((run) => run.pinned === true)
      .map((run) => run.pinned_at ?? '')
      .sort()
    if (stamps.length > 0) pins.push({ key: session.key, at: stamps[0] })
  }
  // A stable sort: two pins with one stamp keep the order the list gave them.
  pins.sort((a, b) => (a.at < b.at ? -1 : a.at > b.at ? 1 : 0))
  return new Set(pins.map((pin) => pin.key))
}
