// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The pins of the person who is signed in NOW. The engine keeps them (see
// `session-pins.ts`); this is the React side: the set the rail sorts by, the one
// action that pins or unpins a session and then re-reads the sessions, and the one-time
// move of the pins this browser kept before the engine did.
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useMemo, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from '@/components/ui/toaster'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { sessionsKeys } from './api'
import { primaryRun, type UnifiedSession } from './provenance'
import { addressOf, targetFromAddress } from './session-address'
import {
  MAX_PINS,
  decodePins,
  encodePins,
  isPinnable,
  pinStorageKey,
  pinnedKeys,
} from './session-pins'

export interface SessionPins {
  /** The pinned rows' keys, oldest pin first, for the rail's ordering. */
  pinned: ReadonlySet<string>
  /** Pin the session at this address, or unpin it when it is pinned. The open card
   * passes its own session, which a deep link can make one the list does not hold. A
   * session the engine holds no run for is not offered the control. */
  toggle: (address: string, session?: UnifiedSession) => void
}

/** The runs a stored address names, newest first, or none when it names none: an observed
 * session has no run to pin. `live:` and `sess:` ask the engine (exact lookups), so a
 * session past the loaded page moves too, and every run of the session comes back so one
 * the engine already pins is seen. */
async function runRefsOf(address: string, tenant: string): Promise<string[]> {
  const target = targetFromAddress(address)
  if (target?.runRef) return [target.runRef]
  const lookup = target?.liveRef
    ? { live_ref: target.liveRef }
    : target?.sessionRef
      ? { claude_session_id: target.sessionRef }
      : null
  if (!lookup) return []
  const answer = await agentOpsApi.listRuns(
    { ...lookup, limit: MAX_PINS },
    { tenant },
  )
  return answer.items.map((run) => run.run_ref)
}

/** Whether the engine will never pin this run: it is gone (404) or released (409). */
function isGone(err: unknown): boolean {
  return err instanceof ApiError && (err.status === 404 || err.status === 409)
}

/** Pin the runs the stored addresses name on the engine, oldest first, so the Pinned
 * section keeps the order the person gave it. Every call names the tenant the addresses
 * were stored for, so a tenant switch in between cannot send them elsewhere. A run the
 * engine already holds (`held`) takes no slot, two names of one run pin it once, and only
 * the newest `free` runs are pinned, as the ceiling lets the 20th pin push out the oldest
 * (none when the engine is full: they stay stored). Returns the addresses to keep stored: a run that is
 * gone or released (a 404 or 409 on the pin) never will pin and is dropped, but a failure
 * that may pass, a failed lookup, and a
 * session with no run at all (one the plane only observes, which the engine cannot pin
 * yet), stay stored, in the order they were. */
async function movePinsToEngine(
  addresses: readonly string[],
  tenant: string,
  held: ReadonlySet<string>,
  free: number,
): Promise<string[]> {
  const keep = new Set<string>()
  const movable: { address: string; ref: string }[] = []
  for (const address of addresses) {
    try {
      const refs = await runRefsOf(address, tenant)
      if (refs.length === 0) keep.add(address)
      else if (!refs.some((ref) => held.has(ref)))
        movable.push({ address, ref: refs[0] })
    } catch {
      // A failed lookup says nothing about the run (a 404 can be a route or a tenant
      // problem): the address stays stored for the next load.
      keep.add(address)
    }
  }
  const newest = movable.filter(
    (pin, i) => !movable.slice(i + 1).some((later) => later.ref === pin.ref),
  )
  // With the engine full nothing can move: pinning would push out the engine's oldest.
  if (free <= 0) for (const { address } of newest) keep.add(address)
  for (const { address, ref } of free > 0 ? newest.slice(-free) : []) {
    try {
      await agentOpsApi.pin(ref, { tenant })
    } catch (err) {
      if (!isGone(err)) keep.add(address)
    }
  }
  return addresses.filter((address) => keep.has(address))
}

/**
 * @param runsReady the engine's run list has been read for the active tenant, so
 *   `sessions` already shows the pins the person has there.
 */
export function useSessionPins(
  sessions: readonly UnifiedSession[],
  runsReady: boolean,
): SessionPins {
  const { t } = useTranslation('sessions')
  const { principal, activeTenant } = useAuth()
  const qc = useQueryClient()
  const pinned = useMemo(() => pinnedKeys(sessions), [sessions])

  // ⛔ PINS KEPT IN THIS BROWSER BEFORE THE ENGINE KEPT THEM MOVE ONCE (#2130, linux rule
  //    1: stored data keeps working). Once per (origin, person, tenant), after the engine's
  //    own pins are known: the runs the browser stored are pinned there oldest first,
  //    within the ceiling the engine's existing pins leave, and the old key is removed. What
  //    could not be pinned stays stored: a failure that may pass (the network), and a
  //    session with no run, which the engine has nothing to pin yet.
  //    ponytail: a session with no run is looked up again on every load while it stays
  //    stored (at most 20 exact lookups); upgrade when the engine can pin it.
  const moved = useRef<string | null>(null)
  const held = useMemo(
    () =>
      new Set(
        sessions
          .flatMap((session) =>
            session.runs.filter((run) => run.pinned === true),
          )
          .map((run) => run.run_ref),
      ),
    [sessions],
  )
  const free = MAX_PINS - held.size
  useEffect(() => {
    if (!runsReady || !activeTenant || typeof window === 'undefined') return
    const key = pinStorageKey(window.location.origin, principal, activeTenant)
    if (!key || moved.current === key) return
    let stored: readonly string[]
    try {
      // No key: nothing was kept here, so nothing to move and nothing to re-read.
      const raw = window.localStorage.getItem(key)
      if (raw === null) return
      stored = decodePins(raw)
    } catch {
      return
    }
    moved.current = key
    void movePinsToEngine(stored, activeTenant, held, free).then((again) => {
      try {
        if (again.length === 0) window.localStorage.removeItem(key)
        else window.localStorage.setItem(key, encodePins(again))
      } catch {
        // A store that refuses the write: the next load moves them again, and the
        // engine's pin is idempotent.
      }
      void qc.invalidateQueries({ queryKey: agentOpsKeys.all(activeTenant) })
      void qc.invalidateQueries({ queryKey: sessionsKeys.all(activeTenant) })
    })
  }, [runsReady, principal, activeTenant, held, free, qc])

  const change = useMutation({
    mutationFn: async (session: UnifiedSession) => {
      const pinnedRuns = session.runs.filter((run) => run.pinned === true)
      if (pinnedRuns.length > 0) {
        await Promise.all(
          pinnedRuns.map((run) => agentOpsApi.unpin(run.run_ref)),
        )
        return
      }
      // The primary run among those still listed: a released run cannot be pinned.
      const run = primaryRun(session.runs.filter((r) => r.state !== 'cleaned'))
      if (run) await agentOpsApi.pin(run.run_ref)
    },
    onError: (err) =>
      toast.error(err instanceof ApiError ? err.message : t('rail.pinFailed')),
    // Success or not, the engine's answer is the truth: re-read it.
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: agentOpsKeys.all(activeTenant) })
      void qc.invalidateQueries({ queryKey: sessionsKeys.all(activeTenant) })
    },
  })

  const { mutate, isPending } = change
  const toggle = useCallback(
    (address: string, given?: UnifiedSession) => {
      if (isPending) return
      const session = given ?? sessions.find((s) => addressOf(s) === address)
      // The control is only offered where this holds, so reaching here is a defect:
      // say so rather than do nothing.
      if (!session || !isPinnable(session)) {
        toast.error(t('rail.pinFailed'))
        return
      }
      mutate(session)
    },
    [sessions, mutate, isPending, t],
  )

  return { pinned, toggle }
}
