// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE SESSION, RESOLVED AGAINST THE ENGINE — the read the detail card has always done,
// lifted out of it so the work surface can do it ONCE.
//
// ⛔ WHY IT MOVED, AND WHAT WOULD HAVE HAPPENED OTHERWISE. The session lives in the
//    address bar, so a COLD DEEP LINK arrives with no list loaded behind it, and the
//    three panes have to answer from the engine exactly the way the card already did:
//    by `live_ref` when the row is profile-scoped, by the bare id for a legacy row,
//    through the run when the card was opened on one. Writing that a second time inside
//    the panes would have been a second answer to the question the card already
//    answers — and the two would have disagreed the first time B2 changed.
//
// ⛔ AND IT IS CALLED ONCE PER OPEN SESSION, NOT ONCE PER SURFACE. The stream
//    subscription lives in here. Two callers would mean two SSE connections to the same
//    row, so the work surface resolves and HANDS the result to the card rather than
//    letting the card resolve again.
//
// Nothing about the resolution itself changed in the move: every comment below is the
// one that was in `session-card.tsx`, and the card's own tests are the control.
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { useLiveStream } from '@/features/shared'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { sessionsApi, sessionsKeys } from './api'
import { isScopedRow, mergeSessions, type UnifiedSession } from './provenance'
import type { SessionTarget } from './session-target'
import type { LiveDTO } from './types'

/** What this principal may do with a session — read by the card AND by the panes. */
export interface SessionGrants {
  liveRead: boolean
  runRead: boolean
  runWrite: boolean
  runAdmin: boolean
}

/** One session as the engine answers for it, with what could NOT be read said out loud. */
export interface SessionResolution {
  /** What the caller asked for; null while nothing is open. */
  target: SessionTarget | null
  session: UnifiedSession
  /** The observed half. ABSENT is an answer (no telemetry yet), never a failure. */
  live: LiveDTO | undefined
  /** Every run the engine linked to this session, plus the seed run it was opened on. */
  runs: RunDTO[]
  /** B2: observation rows sharing a managed row's profile and provider id. */
  related: LiveDTO[]
  streamStatus: ReturnType<typeof useLiveStream>['status']
  /** The operate half was not READ — which is not the same as "there is none". */
  operateUnknown: boolean
  /** The observed half was not READ. A 404 is an answer and not this. */
  observeUnknown: boolean
  loading: boolean
  grants: SessionGrants
}

export function useSessionResolution(
  target: SessionTarget | null,
): SessionResolution {
  const { activeTenant, can } = useAuth()
  const canLiveRead = can('sessions:live:read')
  const canRunRead = can('sessions:run:read')
  const grants = {
    liveRead: canLiveRead,
    runRead: canRunRead,
    runWrite: can('sessions:run:write'),
    runAdmin: can('sessions:run:admin'),
  }

  // Opened from a run row: the run itself announces the session it drives — a
  // PROFILED run by the managed row the plane proved for it (live_ref), a legacy
  // run by the bare id it captured.
  const seedRunQuery = useQuery({
    queryKey: agentOpsKeys.run(activeTenant, target?.runRef ?? ''),
    queryFn: () => agentOpsApi.getRun(target?.runRef as string),
    enabled: !!target?.runRef && canRunRead,
    refetchInterval: 5_000,
  })
  const seed = seedRunQuery.data
  const seedProfiled = !!seed?.provider_profile_ref
  // The identity this card resolves by (B2). A live_ref names ONE row; a bare
  // session id names the LEGACY row and nothing else — two homes may announce the
  // same id, so it is never a name for a profile-scoped row. A profiled run whose
  // id has not been proven yet resolves by neither: its observed half is honestly
  // absent rather than borrowed from whichever home shares the string.
  const liveRef = target?.liveRef || (seedProfiled ? seed?.live_ref || '' : '')
  const sessionRef =
    target?.sessionRef ||
    (!target?.liveRef && seed && !seedProfiled
      ? seed.claude_session_id || ''
      : '')
  const observedKey = liveRef || sessionRef

  const liveQuery = useQuery({
    queryKey: liveRef
      ? sessionsKeys.liveById(activeTenant, liveRef)
      : sessionsKeys.liveOne(activeTenant, sessionRef),
    queryFn: () =>
      liveRef ? sessionsApi.liveById(liveRef) : sessionsApi.liveOne(sessionRef),
    enabled: !!observedKey && canLiveRead,
    // A session with no telemetry yet is a 404, and that is an ANSWER (the launched
    // half exists, the observed half has not started) — not an error to retry at.
    retry: false,
  })

  // THE PROVENANCE LOOKUP, and it is the engine's answer, not a page join: by
  // live_ref, the run the plane PROVED owns the row (none for an observed row, never
  // "the first match"); by bare id, the legacy runs that captured it.
  const runsParams = liveRef
    ? { live_ref: liveRef }
    : { claude_session_id: sessionRef }
  const runsQuery = useQuery({
    queryKey: agentOpsKeys.runs(activeTenant, runsParams),
    queryFn: () => agentOpsApi.listRuns(runsParams),
    enabled: !!observedKey && canRunRead,
    refetchInterval: 8_000,
  })

  // Freshness for the observed half while the card is open. The subscription is as
  // exact as the lookup: by live_ref it is ONE row; by bare id it is the legacy row,
  // and a scoped row that shares the id never reaches it.
  const matches = (snap: LiveDTO) =>
    liveRef
      ? snap.live_ref === liveRef
      : snap.session_ref === sessionRef && !isScopedRow(snap)
  const [override, setOverride] = useState<LiveDTO | null>(null)
  const { status: streamStatus } = useLiveStream<LiveDTO>({
    path: '/v1/m/sessions/stream',
    events: ['session'],
    query: liveRef
      ? { live_ref: liveRef }
      : sessionRef
        ? { ref: sessionRef }
        : undefined,
    enabled: !!observedKey && canLiveRead,
    onSnapshot: (snap) => {
      if (matches(snap)) setOverride(snap)
    },
  })

  // ⛔ A DISABLED READ STILL HAS A CACHE ENTRY. With `sessions:live:read` gone the query
  //    is not issued, but React Query keeps its last `data` for gcTime — so a target that
  //    arrives again through the ADDRESS (Back onto a retired link) painted the observed
  //    half read under the admission that LEFT, with no request under the current one.
  //    `observeUnknown` already says the half was not read; the pane must not be handed
  //    data to prefer over that answer (independent review, 2026-09-18).
  const live = !canLiveRead
    ? undefined
    : override && matches(override)
      ? override
      : (liveQuery.data ?? undefined)

  // B2: the observation rows that share a MANAGED row's profile and provider id —
  // telemetry that arrived through a source bound to the same profile. They are
  // shown BESIDE the run, never merged into it: the plane proved the process, not
  // that those frames are its.
  const relatedParams = {
    provider_profile_ref: live?.provider_profile_ref ?? '',
    session_ref: live?.session_ref ?? '',
  }
  const relatedQuery = useQuery({
    queryKey: sessionsKeys.live(activeTenant, relatedParams),
    queryFn: () => sessionsApi.live({ ...relatedParams, limit: 20 }),
    enabled:
      canLiveRead &&
      live?.attribution === 'managed' &&
      !!live.provider_profile_ref,
  })
  const related = (relatedQuery.data?.items ?? []).filter(
    (r) => r.live_ref !== live?.live_ref,
  )

  // Every run the engine linked to this session, plus the seed run when the card was
  // opened from a run that has not announced a session id (so it is never dropped).
  const linked = runsQuery.data?.items ?? []
  const runs: RunDTO[] =
    seed && !linked.some((r) => r.run_ref === seed.run_ref)
      ? [seed, ...linked]
      : linked

  const merged = mergeSessions(live ? [live] : [], runs)
  const session: UnifiedSession = merged.find((r) => r.live) ??
    merged[0] ?? {
      key: liveRef
        ? `live:${liveRef}`
        : sessionRef
          ? `sess:${sessionRef}`
          : `run:${target?.runRef ?? ''}`,
      sessionRef: sessionRef || undefined,
      liveRef: liveRef || undefined,
      runs: [],
      live,
      provenance: 'discovered',
      control: 'observe',
      lastActivityMs: 0,
    }
  // The operator can act on ANY of the runs driving this session, not only the one
  // the card opens on: before each run was its own selectable row, and folding
  // them into one card must not take that away (contrast finding, 2026-08-10).

  // The operate half was NOT READ — which is NOT the same as "there is none", and the
  // difference is the whole point of this card. Three ways to not know: no permission,
  // a failed lookup, or a failed seed read. All three must say "not read"; only an
  // answered lookup that came back empty may say "discovered".
  const operateUnknown =
    !canRunRead ||
    (!!observedKey && runsQuery.isError) ||
    (!!target?.runRef && seedRunQuery.isError)
  // Same rule on the observed half: a 404 is an ANSWER (nothing observed), any other
  // failure is not.
  const observeFailed =
    liveQuery.isError &&
    !(liveQuery.error instanceof ApiError && liveQuery.error.status === 404)
  const observeUnknown = (!canLiveRead || observeFailed) && !!observedKey

  const loading =
    (!!target?.runRef && seedRunQuery.isLoading) ||
    (!!observedKey && canRunRead && runsQuery.isLoading)
  return {
    target,
    session,
    live,
    runs,
    related,
    streamStatus,
    operateUnknown,
    observeUnknown,
    loading,
    grants,
  }
}
