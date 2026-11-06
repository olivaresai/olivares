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
import { sessionsApi, sessionsKeys } from './api'
import { useOfferUndo } from './list-undo'
import { primaryRun, sessionTitle, type UnifiedSession } from './provenance'
import { addressOf, targetFromAddress } from './session-address'
import {
  MAX_PINS,
  decodePins,
  encodePins,
  isPinnable,
  observedLiveRef,
  pinStorageKey,
  pinnedKeys,
} from './session-pins'

export interface SessionPins {
  /** The pinned rows' keys, oldest pin first, for the rail's ordering. */
  pinned: ReadonlySet<string>
  /** Pin the session at this address, or unpin it when it is pinned. The open card
   * passes its own session, which a deep link can make one the list does not hold. A
   * session with neither a run nor an observed live row is not offered the control. */
  toggle: (address: string, session?: UnifiedSession) => void
}

/** What the engine pins for an observed session: its live row, named as the engine names
 * it in a pin list (`live:<live_ref>`), against a run's bare ref. */
const LIVE_SUBJECT = 'live:'

/** The pin subjects a stored address names: its runs, newest first, or the live row of a
 * session with no run (one the plane only observes). `live:` and `sess:` ask the engine
 * (exact lookups), so a session past the loaded page moves too, and every run of the
 * session comes back so one the engine already pins is seen. None when it names nothing,
 * and null when the exact lookup of its live row finds it gone (404): it never will pin. */
async function subjectsOf(
  address: string,
  tenant: string,
): Promise<string[] | null> {
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
  if (answer.items.length > 0) return answer.items.map((run) => run.run_ref)
  if (target?.liveRef) return [LIVE_SUBJECT + target.liveRef]
  if (!target?.sessionRef) return []
  try {
    const row = await sessionsApi.liveOne(target.sessionRef, { tenant })
    return row.live_ref ? [LIVE_SUBJECT + row.live_ref] : []
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) return null
    throw err
  }
}

/** Pin one subject on the engine, for the tenant its address was stored for. */
function pinSubject(subject: string, tenant: string) {
  return subject.startsWith(LIVE_SUBJECT)
    ? sessionsApi.pinLive(subject.slice(LIVE_SUBJECT.length), { tenant })
    : agentOpsApi.pin(subject, { tenant })
}

/** Whether the engine will never pin this session: it is gone (404) or released (409). */
function isGone(err: unknown): boolean {
  return err instanceof ApiError && (err.status === 404 || err.status === 409)
}

/** Pin the sessions the stored addresses name on the engine, oldest first, so the Pinned
 * section keeps the order the person gave it. Every call names the tenant the addresses
 * were stored for, so a tenant switch in between cannot send them elsewhere. A subject the
 * engine already holds (`held`) takes no slot, two names of one session pin it once, and
 * only the newest `free` subjects are pinned, as the ceiling lets the 20th pin push out
 * the oldest (none when the engine is full: they stay stored). Returns the addresses to
 * keep stored: a session that is gone or released (a 404 or 409 on the pin, or a live row
 * the exact lookup does not find) never will pin and is dropped, but a failure that may pass, such as the network, stays
 * stored, in the order it was. */
async function movePinsToEngine(
  addresses: readonly string[],
  tenant: string,
  held: ReadonlySet<string>,
  free: number,
): Promise<string[]> {
  const keep = new Set<string>()
  const movable: { address: string; subject: string }[] = []
  for (const address of addresses) {
    try {
      const subjects = await subjectsOf(address, tenant)
      if (subjects === null) continue // gone: nothing to move, ever
      // Nothing to pin and nothing said it is gone: it stays stored.
      if (subjects.length === 0) keep.add(address)
      else if (!subjects.some((subject) => held.has(subject)))
        movable.push({ address, subject: subjects[0] })
    } catch {
      // A failed lookup says nothing about the session (a 404 can be a route or a tenant
      // problem): the address stays stored for the next load.
      keep.add(address)
    }
  }
  const newest = movable.filter(
    (pin, i) =>
      !movable.slice(i + 1).some((later) => later.subject === pin.subject),
  )
  // With the engine full nothing can move: pinning would push out the engine's oldest.
  if (free <= 0) for (const { address } of newest) keep.add(address)
  for (const { address, subject } of free > 0 ? newest.slice(-free) : []) {
    try {
      await pinSubject(subject, tenant)
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
  const offerUndo = useOfferUndo()
  const pinned = useMemo(() => pinnedKeys(sessions), [sessions])

  // ⛔ PINS KEPT IN THIS BROWSER BEFORE THE ENGINE KEPT THEM MOVE ONCE (#2130, linux rule
  //    1: stored data keeps working). Once per (origin, person, tenant), after the engine's
  //    own pins are known: the runs the browser stored are pinned there oldest first,
  //    within the ceiling the engine's existing pins leave, and the old key is removed. What
  //    could not be pinned stays stored: a failure that may pass (the network).
  const moved = useRef<string | null>(null)
  const held = useMemo(
    () =>
      new Set(
        sessions.flatMap((session) => [
          ...session.runs
            .filter((run) => run.pinned === true)
            .map((run) => run.run_ref),
          ...(session.live?.pinned === true && observedLiveRef(session)
            ? [LIVE_SUBJECT + session.live.live_ref]
            : []),
        ]),
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
    // Returns what it unpinned (runs oldest pin first, and an observed session's live ref): an unpin is undone, a pin is its own undo.
    mutationFn: async (session: UnifiedSession) => {
      const pinnedRuns = session.runs
        .filter((run) => run.pinned === true)
        .sort((a, b) => (a.pinned_at ?? '').localeCompare(b.pinned_at ?? ''))
      const liveRef = observedLiveRef(session)
      const livePinned = liveRef !== undefined && session.live?.pinned === true
      if (pinnedRuns.length > 0 || livePinned) {
        await Promise.all([
          ...pinnedRuns.map((run) => agentOpsApi.unpin(run.run_ref)),
          ...(livePinned ? [sessionsApi.unpinLive(liveRef)] : []),
        ])
        return { runs: pinnedRuns, liveRef: livePinned ? liveRef : undefined }
      }
      // The primary run among those still listed: a released run cannot be pinned. A
      // session with no run is pinned on its live row.
      const run = primaryRun(session.runs.filter((r) => r.state !== 'cleaned'))
      if (run) await agentOpsApi.pin(run.run_ref)
      else if (liveRef) await sessionsApi.pinLive(liveRef)
      return { runs: [], liveRef: undefined }
    },
    // ponytail: Undo pins the runs again, so the session returns as the newest pin, not at its old
    // place among the others; the engine stamps the moment itself. Upgrade when pin accepts one.
    onSuccess: ({ runs, liveRef }, session) => {
      if (runs.length === 0 && !liveRef) return
      offerUndo({
        message: t('undo.unpinned', {
          name: sessionTitle(session, t('untitled')),
        }),
        // An observed session has no run to re-read; its pin is idempotent.
        expect: runs.map((run) => ({
          ref: run.run_ref,
          still: (now) => now.pinned !== true,
        })),
        restore: async () => {
          // One after the other, so the pins keep the order they had among themselves.
          for (const run of runs) await agentOpsApi.pin(run.run_ref)
          if (liveRef) await sessionsApi.pinLive(liveRef)
        },
      })
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
