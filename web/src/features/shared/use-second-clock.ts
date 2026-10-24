// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect, useState } from 'react'
import { usePreferencesStore } from '@/stores/preferences'

const REDUCE_QUERY = '(prefers-reduced-motion: reduce)'
// The engine's active window is about two minutes; past it the figure stops growing.
const MAX_TICK_SECONDS = 120

/**
 * The honest elapsed seconds of a live session. The engine advances
 * duration_seconds only when a new event lands, so an ACTIVE session's figure is
 * extrapolated from its last event to "now" (C2-DESIGN §3: the tabular figure
 * ticks each second). A missing, unparseable or future last_event_at leaves the
 * engine value untouched; a session that is not active never extrapolates.
 */
export function tickingDurationSeconds(
  durationSeconds: number | null | undefined,
  lastEventAt: string | null | undefined,
  nowMs: number,
): number | null | undefined {
  if (durationSeconds === null || durationSeconds === undefined)
    return durationSeconds
  if (!lastEventAt) return durationSeconds
  const last = Date.parse(lastEventAt)
  if (Number.isNaN(last) || last > nowMs) return durationSeconds
  // Capped: a stale last event never runs the figure away.
  const extra = Math.max(0, Math.floor((nowMs - last) / 1000))
  return durationSeconds + Math.min(MAX_TICK_SECONDS, extra)
}

/**
 * The live row as it reads this second: an ACTIVE session's duration_seconds ticked to
 * `nowMs` (tickingDurationSeconds); any other row comes back unchanged, as the same object.
 */
export function tickLive<
  T extends {
    cc_state: string
    duration_seconds: number
    last_event_at?: string | null
  },
>(live: T, nowMs: number): T {
  if (live.cc_state !== 'active') return live
  const seconds = tickingDurationSeconds(
    live.duration_seconds,
    live.last_event_at,
    nowMs,
  )
  return typeof seconds === 'number' && seconds !== live.duration_seconds
    ? { ...live, duration_seconds: seconds }
    : live
}

/**
 * A one-second clock for the live figures. `?still` freezes it for captures,
 * and the reduced-motion preference (the operating system's or the console's
 * Reduce motion setting) stops it: a ticking figure is motion. When `enabled`
 * is false the clock is off (nothing active to tick) and restarts when it
 * turns back on.
 */
export function useSecondClock(enabled = true): number {
  const reduceMotion = usePreferencesStore((s) => s.reduceMotion)
  const [nowMs, setNowMs] = useState(() => Date.now())

  useEffect(() => {
    if (!enabled) return
    const still = new URLSearchParams(window.location.search).has('still')
    if (still) return
    const query = window.matchMedia(REDUCE_QUERY)

    let id: number | undefined
    const sync = () => {
      if (reduceMotion || query.matches) {
        if (id !== undefined) window.clearInterval(id)
        id = undefined
        return
      }
      if (id === undefined) {
        // No setState here: the first tick (within the second) carries it, and
        // the effect body stays free of cascading-render state updates.
        id = window.setInterval(() => setNowMs(Date.now()), 1000)
      }
    }
    sync()
    query.addEventListener('change', sync)
    return () => {
      if (id !== undefined) window.clearInterval(id)
      query.removeEventListener('change', sync)
    }
  }, [enabled, reduceMotion])

  return nowMs
}
