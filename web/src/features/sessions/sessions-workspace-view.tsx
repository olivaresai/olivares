// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  useQuery,
  useQueryClient,
  type QueryClient,
} from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import {
  Activity,
  Eye,
  HelpCircle,
  Plus,
  RefreshCw,
  Terminal,
} from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { EmptyState } from '@/components/ui/empty-state'
import { PageHeader } from '@/components/ui/page-header'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import {
  useAuthBoundary,
  type AuthBoundary,
} from '@/features/agentops/auth-boundary'
import { ProfilesPanel } from '@/features/agentops/profiles-panel'
import { RunCreateDialog } from '@/features/agentops/run-create-dialog'
import { RunStateBadge } from '@/features/agentops/run-state-badge'
import type { RunState } from '@/features/agentops/types'
import { WorkspacesPanel } from '@/features/agentops/workspaces-panel'
import { LiveDot, RelTimeLabel, useLiveStream } from '@/features/shared'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { formatInt, formatMicroUsd, formatTokens } from '@/lib/format'
import { cn } from '@/lib/utils'
import { sessionsApi, sessionsKeys } from './api'
import { AttributionChip } from './attribution-chip'
import { CcStateBadge } from './cc-state-badge'
import {
  mergeSessions,
  primaryRun,
  sessionLabel,
  sessionSearchKey,
  sessionTarget,
  type Provenance,
  type UnifiedSession,
} from './provenance'
import { SessionCard } from './session-card'
import type { SessionTarget } from './session-target'
import type { CcState, LiveDTO } from './types'
import './i18n'

const LIVE_LIMIT = 200
const RUNS_LIMIT = 200
const ALL = '__all__'

/**
 * WAS THIS HALF REFUSED, or did it merely BREAK? The two are not the same answer and
 * this list must not conflate them:
 *
 *  · REFUSED — the engine said "not you" for THIS read: 403, including the assurance
 *    403 `step_up_required` (errors.ts:77), and 401. The half's admission is gone NOW,
 *    so nothing it returned earlier may keep being painted: rows, stream overrides,
 *    origin labels, counts and the row-backed open target all leave with it.
 *  · BROKE — a 5xx, a network failure, a 404. Admission was never withdrawn; the read
 *    failed. The last answer stays on screen under the "could not be read" notice, and
 *    the OTHER half is not touched. Calling this a permission denial would teach the
 *    operator to go ask for a role they already hold.
 *
 * Read by STATUS/CODE, never by matching the message: the message is prose and gets
 * reworded and translated; the status is the contract (errors.ts).
 */
function esRechazo(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false
  return error.isStepUpRequired || error.isForbidden || error.isUnauthenticated
}

/**
 * THE STANDING REFUSALS — one mark per (context, half), written from the read itself.
 *
 * ⛔ WHY A HISTORY AND NOT THE LAST ERROR (independent review R1, 2026-09-08). React
 *    Query keeps ONE error per query, and a successful `data` survives every later
 *    failure. So `success → 500 → 403 → 500` ends with the 500 as the whole of the
 *    query's error state, and a rule that reads only that error re-admits the
 *    PRE-REFUSAL page without any successful answer having been given: admitted →
 *    admitted → excluded → admitted again. The refusal is not "the current error"; it
 *    is an event whose consequence lasts until it is lifted.
 *
 * A mark is set the moment a read is refused and cleared ONLY by a read that
 * SUCCEEDS in the same context. An outage, a 404, a network failure or a pending
 * refetch lift nothing: they are not answers. Both are written from the query
 * function, which sees every outcome of every read — a rule that watched renders
 * instead could miss a refusal that a later failure re-renders over.
 *
 * The mark is per (episode, half). A departure ends the episode that owned the refusal,
 * and the returning one is a different reader that must establish its own admission
 * anyway — which it does through `marcasHeredadas` below, not by inheriting a flag.
 * The map holds one boolean per episode this mount has actually acted in.
 */
export type RefusalMarks = Readonly<Record<string, true>>
const NO_REFUSALS: RefusalMarks = Object.freeze({})

/** The marks a newly mounted reader starts with: one for every half whose cached query
 * is already sitting in a failed state it did not witness. */
function marcasHeredadas(
  client: QueryClient,
  halves: readonly (readonly [readonly unknown[], string])[],
): RefusalMarks {
  let marks: RefusalMarks = NO_REFUSALS
  for (const [key, mark] of halves) {
    if (client.getQueryState(key)?.status === 'error')
      marks = conMarca(marks, mark)
  }
  return marks
}

const conMarca = (prev: RefusalMarks, mark: string): RefusalMarks =>
  prev[mark] ? prev : { ...prev, [mark]: true }

const sinMarca = (prev: RefusalMarks, mark: string): RefusalMarks => {
  if (!prev[mark]) return prev
  const { [mark]: _lifted, ...rest } = prev
  return rest
}

/**
 * The admission a half is CURRENTLY in, and how many times it has left.
 *
 * `generation` only ever moves forward, one step per withdrawal, and that is the whole
 * point: an intent stamped with an older generation can never be current again, so
 * regaining a read bit, or a later answer that happens to contain the same row, cannot
 * reopen something chosen under an authority that ended. Recorded by adjusting state
 * during render — the transition is observable there and nowhere else, and an effect
 * may not set state in this codebase.
 */
interface AdmissionState {
  live: boolean
  run: boolean
  liveGen: number
  runGen: number
}
const ADMISSION_AT_MOUNT: AdmissionState = {
  live: true,
  run: true,
  liveGen: 0,
  runGen: 0,
}

/**
 * THE AUTHORITY EPISODE — a local, one-way number naming the stretch of time this view
 * has been acting for ONE principal, in ONE tenant, under ONE credential.
 *
 * ⛔ WHY THE BOUNDARY KEY COULD NOT BE THIS (independent review, 2026-09-08).
 *    `boundaryEpochOf` memoises one epoch PER DISTINCT KEY, so principal A → B → A
 *    hands back A's original epoch. That is right for a cache key — the same boundary
 *    should address the same partition — and wrong for a lifetime: a card chosen by
 *    the first A matched again when A returned, and stayed open through two pending
 *    reads and then an answer with no rows in it. **Key equality is not continuity.**
 *
 * So the episode counts DEPARTURES rather than naming boundaries: every change of the
 * boundary key starts a new one, whichever key it changes to. It is local to this
 * mount, never reused, and deliberately NOT put in a query key — the shared epoch keeps
 * its cache semantics untouched for every other consumer of that hook.
 *
 * One episode owns three things, so they cannot disagree: which row intent may still be
 * open, which stream connection may still deliver, and which queued refresh may still
 * run.
 */
interface AuthorityEpisode {
  key: string
  n: number
}

/**
 * THE OWNER OF THE QUEUED HINT WORK — the queue and its right to act, in ONE object.
 *
 * A stream frame does not carry rows; it asks the current admitted read to run again
 * (see the hint queue below). That request can outlive the thing that authorised it:
 * the promise settles after the view has gone, after the boundary has moved, after the
 * read was refused. So the queue is not a record with a stamp on it — it IS the owner.
 * One object per (episode, live admission), installed by an effect and killed by that
 * effect's cleanup. `vivo` is one-way: a killed owner is never revived, and a returning
 * boundary or a restored read bit is given a NEW one, empty. Every callback re-checks
 * the owner it captured before it touches the record or launches anything, so a dead
 * owner's completion can neither clear nor start a live owner's work.
 *
 * The comparison is the object's IDENTITY, never a field: an owner cannot be
 * reconstructed by anything that happens to hold the same numbers.
 */
interface ColaPista {
  /** The authority episode this queue was installed for. RECORDED, not compared — it
   * says what the object is, while identity is what decides whether it may act. */
  episodio: number
  /** A read this queue started — or joined — that has not answered yet. */
  enVuelo: boolean
  /** A hint arrived while that read was out. Worth exactly one trailing read. */
  sucio: boolean
  /** Still entitled to act. Set false once, never back. */
  vivo: boolean
}

/**
 * ⛔ THE STREAM DELTAS THAT USED TO LIVE HERE ARE GONE, AND THAT IS THE CORRECTION.
 *
 * A `session` frame used to be merged into the list: it replaced the matching row, and
 * one for a session absent from the last page was PREPENDED as an extra row, with its
 * own counts, origin and clickable target. The ratified effective-context contract
 * (2026-09-08) removes that, and the reason is not tidiness:
 *
 *  · the engine's stream is TENANT-WIDE (`modules/sessions/stream.go`) and carries no
 *    core-workspace attribution, and `LiveDTO` has no such field either — so a frame
 *    cannot show which slice of the inventory it belongs to;
 *  · a frame is stamped by the context it ARRIVES in, which is not evidence of the
 *    context it was PRODUCED in;
 *  · and a row that never came through an admitted GET was, in the end, the console
 *    asserting inventory it had not been given.
 *
 * The event still does its real job: it says "the tenant inventory may have changed",
 * and the current admitted read goes and finds out. Rows, counts, provenance, the
 * origin column and every row-backed action now have exactly one source — an accepted
 * response under the current authority.
 */

/**
 * An open card, with what it was opened ON.
 *
 * A row-backed target is DERIVED from halves: clicking a row that exists because the
 * observed half answered is an act of that half's authority. If that half is refused
 * or its read bit leaves, the selection made under it goes too — otherwise a refused
 * origin keeps a card open, and the card is the surface that then asks the engine for
 * more. `needsLive`/`needsRun` record which halves the clicked ROW was made of; a
 * target the card itself navigated to is not row-backed and only carries the episode.
 */
interface Selection {
  /** The authority EPISODE that owned the click (see `AuthorityEpisode`). */
  episode: number
  needsLive: boolean
  needsRun: boolean
  /** The admission generation of each half AT THE CLICK. A withdrawal moves the
   * generation on, and this stamp then never matches again — which is what makes the
   * retirement irreversible rather than merely hidden while the half is out. */
  liveGen: number
  runGen: number
  target: SessionTarget
}

// ONE state facet over BOTH halves. The observed states are the engine's derived
// cc_state; the run states are the runtime's stored lifecycle. They are prefixed
// because they are different vocabularies that share words ("idle" means "quiet within
// tolerance" on one side and "no recent activity, process alive" on the other), and
// collapsing them would filter for a state the operator did not choose.
//
// The run half is here because `/agentops` HAD it (origin/main
// web/src/features/agentops/agentops-view.tsx:36-45) and a unified screen that could no
// longer isolate failed or stopped runs would have taken a function away.
const OBSERVED_STATES: CcState[] = ['active', 'idle', 'ended', 'silent_evasion']
const RUN_STATES: RunState[] = [
  'pending',
  'running',
  'idle',
  'stopped',
  'failed',
  'cleaned',
]

/** Which entrance opened this view. Both land on the SAME room and the SAME card;
 * the entrance only decides which source the list opens focused on, and whether the
 * operate affordances (launch, workspaces) are offered. */
export type SessionsEntrance = 'observe' | 'operate'

/**
 * SessionsWorkspaceView — ONE destination for sessions, whichever way they reached
 * the plane.
 *
 * Before this, a session lived in one of two screens in two different nav sections:
 * observed sessions under Visibility (`/sessions`) and launched runs under Management
 * (`/agentops`, labelled "Claude Code"), with a detail card on only one of the two.
 * The operator had to know whether a session had been DISCOVERED or LAUNCHED to pick
 * a section — which is precisely what they came to the screen to find out.
 *
 * Both routes now mount this view, and both keep their own path, permission and nav
 * entry. They are NOT redirects, and that is a measured decision, not a preference:
 * `RequirePermission` (components/layout/require-permission.tsx:25) blocks a route on
 * the ONE permission its registry entry declares, so sending `/agentops` to
 * `/sessions` would hand an operator holding only `sessions:run:read` a Forbidden
 * page instead of their own runs. Two doors into one room removes the split without
 * taking anything away — and the canon's first hard rule is that the answer to a
 * problem is never to remove a function.
 */
export function SessionsWorkspaceView({
  entrance = 'observe',
}: {
  entrance?: SessionsEntrance
}) {
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()
  /**
   * WHO IS ACTING, IN WHICH TENANT, UNDER WHICH CREDENTIAL — the boundary the provider
   * profile plane already partitions its cache by (agentops/auth-boundary.ts). This
   * screen needs the same three facts and had only one: `Inner` remounted on the
   * TENANT, and both query keys carried the tenant alone. Two principals of one tenant
   * therefore shared cache entries, and a credential renewal — which keeps the session
   * id (core/auth/authenticator.go RefreshSession) — changed nothing at all, so a new
   * boundary was handed the rows the previous one had read.
   */
  const boundary = useAuthBoundary()

  /**
   * WHEN THE BOUNDARY MOVES, THIS PLANE'S READS END WITH IT. `useAuthBoundary` already
   * cancels and removes the `agentops` scope of the boundary that left — which is where
   * this screen's run half now lives — so only the `sessions` scope is retired here.
   * The in-flight read is aborted through the signal its query owns, so a late answer
   * has nowhere to land, and the removal is what makes a return to that boundary have
   * to ask again instead of finding its old rows waiting.
   *
   * It lives in THIS component and not in `Inner` on purpose: `Inner` is remounted by
   * its key on a tenant switch and would come back with an empty `previous`, i.e. with
   * nothing to retire, on exactly the switch that most needs it.
   */
  const previous = useRef<{ tenant: string | null; epoch: number } | null>(null)
  useEffect(() => {
    const prev = previous.current
    if (prev && prev.epoch !== boundary.epoch) {
      const scope = sessionsKeys.boundaryScope(prev.tenant, prev.epoch)
      void queryClient.cancelQueries({ queryKey: scope })
      queryClient.removeQueries({ queryKey: scope })
    }
    previous.current = { tenant: boundary.tenant, epoch: boundary.epoch }
  }, [boundary, queryClient])

  // Remount on a tenant switch so no row, override or open card can outlive it. A
  // principal or credential change does NOT remount — a silent token renewal must not
  // throw away the operator's filters — and does not need to: everything derived from
  // a read is fenced by the boundary below.
  return (
    <Inner
      key={activeTenant ?? 'none'}
      entrance={entrance}
      boundary={boundary}
    />
  )
}

function Inner({
  entrance,
  boundary,
}: {
  entrance: SessionsEntrance
  boundary: AuthBoundary
}) {
  const { t, i18n } = useTranslation('sessions')
  // The provider-profile plane's own copy lives in the agentops namespace, which the
  // panels below register when they load.
  const { t: ta } = useTranslation('agentops')
  const lang = i18n.language
  const { t: tn } = useTranslation('nav')
  const { activeTenant, can } = useAuth()

  const canLiveRead = can('sessions:live:read')
  const canRunRead = can('sessions:run:read')
  const canRunWrite = can('sessions:run:write')

  const [tab, setTab] = useState('sessions')
  const [state, setState] = useState<string>(ALL)
  const [source, setSource] = useState<string>(ALL)
  const [selection, setSelection] = useState<Selection | null>(null)
  const [createOpen, setCreateOpen] = useState(false)

  const queryClient = useQueryClient()

  /**
   * THE EPISODE THIS SCREEN IS CURRENTLY ACTING IN. It starts again whenever the
   * boundary key changes — to a key never seen or to one seen before, which is the
   * whole point (see `AuthorityEpisode`). Adjusted during render because that is where
   * the change is observable and an effect may not set state here.
   */
  const [episode, setEpisode] = useState<AuthorityEpisode>({
    key: boundary.key,
    n: 1,
  })
  if (episode.key !== boundary.key) {
    setEpisode({ key: boundary.key, n: episode.n + 1 })
  }
  const episodio = episode.n

  /** Where each half's standing refusal is recorded, for THIS episode. */
  const liveMark = `${episodio}|live`
  const runMark = `${episodio}|run`

  /**
   * ⛔ NO WORKSPACE IN THE KEY, THE REQUEST OR THE FENCE — and this is a correction,
   *    not a simplification. This view used to send `workspace_id` to the live list and
   *    append the selected workspace to its cache key. The engine's handler never reads
   *    that field (`modules/sessions/api.go` builds its query from limit/cursor alone),
   *    the run list has no such parameter at all, and the stream is tenant-wide — so the
   *    console was partitioning its cache, and its authority fence, by a filter that did
   *    not exist. What the operator saw was a global inventory dressed as a scoped one.
   *
   *    The scope is now stated instead of implied (the tenant-wide notice below), and
   *    the topbar selector is left alone: it is a real selection for the views that DO
   *    scope by it, and changing it here manufactures no departure — see the label.
   */
  const liveKey = useMemo(
    () =>
      sessionsKeys.liveScoped(activeTenant, boundary.epoch, {
        limit: LIVE_LIMIT,
      }),
    [activeTenant, boundary.epoch],
  )

  const runsKey = useMemo(
    () =>
      agentOpsKeys.runsScoped(activeTenant, boundary.epoch, {
        limit: RUNS_LIMIT,
      }),
    [activeTenant, boundary.epoch],
  )

  /**
   * ⛔ A NEW READER DOES NOT INHERIT THE PREVIOUS ONE'S PERMISSION TO PAINT.
   *
   * The marks live with this mount; the QueryCache outlives it. So a page read
   * successfully, then refused, then failed with an ordinary outage leaves an entry
   * holding PRE-REFUSAL rows whose last error is a plain 500 — and a fresh mount, with
   * no memory of the refusal, would have read that as "an outage over good data" and
   * painted it. The refusal was real and nothing has answered since.
   *
   * A reader arriving over a query that is already in a failed state cannot tell an
   * inherited outage from an inherited refusal, and must not guess: it starts with the
   * mark set and lifts it the same way as any other — with an answer of its own. React
   * Query re-fetches a query left in error on mount, so that answer is one round trip
   * away and nothing is lost but the right to paint in the meantime. A cached entry in
   * a SUCCESSFUL state is a different matter: its data is what a read last returned,
   * and it is admitted as it always was.
   */
  const [refusals, setRefusals] = useState<RefusalMarks>(() =>
    marcasHeredadas(queryClient, [
      [liveKey, `${episodio}|live`],
      [runsKey, `${episodio}|run`],
    ]),
  )

  const liveQuery = useQuery({
    queryKey: liveKey,
    // The request is pinned to the context its KEY was built for, and carries the
    // query's own abort signal: a read cannot be answered under a tenant this entry
    // never named, and a boundary that moves aborts it instead of letting it land.
    //
    // The outcome is also where this half's admission is decided: a refusal leaves a
    // mark that only a SUCCEEDING read in the same context lifts. An abort is not an
    // outcome of the read at all and marks nothing.
    queryFn: async ({ signal }) => {
      try {
        const answer = await sessionsApi.live(
          { limit: LIVE_LIMIT },
          { tenant: activeTenant, signal },
        )
        setRefusals((prev) => sinMarca(prev, liveMark))
        return answer
      } catch (fallo) {
        if (esRechazo(fallo)) setRefusals((prev) => conMarca(prev, liveMark))
        throw fallo
      }
    },
    enabled: canLiveRead,
  })

  const runsQuery = useQuery({
    queryKey: runsKey,
    queryFn: async ({ signal }) => {
      try {
        const answer = await agentOpsApi.listRuns(
          { limit: RUNS_LIMIT },
          { tenant: activeTenant, signal },
        )
        setRefusals((prev) => sinMarca(prev, runMark))
        return answer
      } catch (fallo) {
        if (esRechazo(fallo)) setRefusals((prev) => conMarca(prev, runMark))
        throw fallo
      }
    },
    enabled: canRunRead,
    refetchInterval: 8_000,
  })

  /**
   * WHAT MAY BE PAINTED IS WHAT IS ADMITTED NOW — one decision per half, made before
   * anything is derived, so the grid, the tiles, the origin column, the filters, the
   * search index and the open card all answer to the same rule instead of each
   * repeating it.
   *
   * ⛔ THE DEFECT THIS REPLACES. `enabled` only stops a query being ISSUED. React Query
   *    keeps the last `data` when a later fetch fails, and holds it for `gcTime` (five
   *    minutes, query.ts:24) when the read bit leaves. Neither list was gated on the
   *    CURRENT permission or the CURRENT error, so after a success the refused half
   *    kept its rows in the grid, its stream overrides, its share of the summary tiles
   *    and its clickable target — under a warning strip that said the half could not be
   *    read. A strip beside the table is not exclusion (review 2026-09-07).
   *
   * A half that BROKE is not a half that was refused: it keeps its last answer (see
   * `esRechazo`). And neither state says anything about the other half, or about
   * `sessions:run:write`, which is its own grant and stays where it is.
   *
   * ⛔ AND A REFUSAL OUTLIVES THE ERROR THAT REPORTED IT (review R1). Reading only the
   *    CURRENT error made `success → 500 → 403 → 500` end admitted, because by the last
   *    step the 403 was no longer the query's error and the pre-refusal page was still
   *    in the cache. The standing mark is therefore consulted as well, and it is lifted
   *    by one thing only: a read that succeeded in this context. An outage after a
   *    refusal is still a refused half that has not answered since.
   */
  const liveAdmitted =
    canLiveRead && !esRechazo(liveQuery.error) && !refusals[liveMark]
  const runAdmitted =
    canRunRead && !esRechazo(runsQuery.error) && !refusals[runMark]

  /**
   * EVERY DEPARTURE IS COUNTED, AND COUNTED ONCE. The generation of a half moves on
   * whenever it stops being admitted — a refusal, a read bit that left, a context that
   * changed — and never moves back. Anything stamped with the generation it was chosen
   * under is therefore retired for good at that moment, instead of waiting to see
   * whether the half ever looks admitted again.
   *
   * Adjusting state during render is how a transition is observed here: an effect may
   * not set state in this codebase (react-hooks/set-state-in-effect), and there is no
   * callback for "a permission stopped being granted". React re-renders with the new
   * value before committing, so nothing is painted from the stale one.
   */
  const [admission, setAdmission] = useState<AdmissionState>(ADMISSION_AT_MOUNT)
  if (admission.live !== liveAdmitted || admission.run !== runAdmitted) {
    setAdmission({
      live: liveAdmitted,
      run: runAdmitted,
      liveGen: admission.liveGen + (admission.live && !liveAdmitted ? 1 : 0),
      runGen: admission.runGen + (admission.run && !runAdmitted ? 1 : 0),
    })
  }

  /**
   * A READ BIT THAT LEFT TAKES ITS CACHE WITH IT. Without this the entry survives for
   * `gcTime` and, being inside `staleTime`, is served INSTANTLY and silently the moment
   * the bit comes back — the old rows painted for a permission that has only just been
   * restored, with no request made under it. Regaining a permission has to mean asking
   * again, so the entry is dropped and re-admission starts from a fetch.
   *
   * Removing by the boundary-scoped prefix takes every workspace variant of that half's
   * list, which is the point: no workspace's rows survive the bit either.
   *
   * A REFUSAL is deliberately not removed here: the refusal IS the query's error, the
   * notice above the table is derived from it, and dropping it would erase the only
   * account the operator gets of what happened. Nothing can be resurrected from it in
   * the meantime — `liveAdmitted`/`runAdmitted` exclude a refused half until a fetch
   * SUCCEEDS under the current admission, and React Query re-fetches a query left in
   * error on its next mount.
   */
  useEffect(() => {
    if (canLiveRead) return
    queryClient.removeQueries({
      queryKey: sessionsKeys.liveScoped(activeTenant, boundary.epoch),
    })
  }, [canLiveRead, queryClient, activeTenant, boundary.epoch])

  useEffect(() => {
    if (canRunRead) return
    queryClient.removeQueries({
      queryKey: agentOpsKeys.runsScoped(activeTenant, boundary.epoch),
    })
  }, [canRunRead, queryClient, activeTenant, boundary.epoch])

  /**
   * ⛔ REFRESCAR SÓLO LO QUE SE PUEDE LEER. Las dos consultas ya están guardadas con `enabled`,
   *    pero el botón llamaba a `refetch()` de LAS DOS sin mirar el permiso, y `refetch` es una
   *    orden explícita: pide al motor una lectura que este operador no tiene derecho a hacer, y
   *    la respuesta es un 403 que nadie enseña. La guarda de `enabled` protege la carga
   *    automática y no ésta.
   */
  const refrescarLegibles = useCallback(() => {
    if (canLiveRead) void liveQuery.refetch()
    if (canRunRead) void runsQuery.refetch()
  }, [canLiveRead, canRunRead, liveQuery, runsQuery])

  /** Ninguna fuente legible ⇒ el botón no tiene nada que refrescar y no se pinta. */
  const hayFuenteLegible = canLiveRead || canRunRead

  /**
   * ONE REFRESH IN FLIGHT, AND ONE MORE IF SOMETHING HAPPENED WHILE IT RAN — held by
   * an owner that can END.
   *
   * A burst of frames must not become a burst of requests, and it must not keep
   * cancelling a read that was about to answer. So a hint either starts the read or
   * marks the in-flight one dirty; the dirty flag buys exactly one trailing read when
   * the current one finishes. The record is a ref because it is only ever touched from
   * callbacks and effects — never read while rendering.
   *
   * ⛔ WHY AN OWNER AND NOT AN EPISODE STAMP (independent review R3, 2026-09-08). The
   *    queue used to stamp `episodio` on its work and compare it in a `finally`. That
   *    question — "is the same principal still acting?" — is not the one a queued read
   *    has to answer. Three things end the work and none of them moves the episode:
   *
   *     · THE READ WAS REFUSED. `finally` runs whatever the outcome, and
   *       `refetchQueries` swallows the failure unless told not to (query-core 5.102.8,
   *       `queryClient.js`: `if (!fetchOptions.throwOnError) promise =
   *       promise.catch(noop)`), so a 403 and an admitted answer were the SAME event to
   *       that callback. A hint queued during a read that came back 403 therefore
   *       started another read under the refusal — the returned defect.
   *     · THE VIEW WENT AWAY. The effect had no cleanup, and the promise outlives the
   *       component: React Query cancels the fetch when the last observer leaves
   *       (`query.js` removeObserver → `cancel({ revert: true })`), the callback fires,
   *       and the trailing `refetchQueries` finds the cached entry still there —
   *       `isDisabled()` is false for a query that has been fetched — and issues a
   *       request for a screen nobody is looking at.
   *     · THE LIVE READ BIT LEFT. A queue that survives the gap hands work queued
   *       BEFORE the permission was granted to the permission that has just come back.
   *
   * So the queue IS the owner: one object per (episode, live admission), installed by
   * the effect below and killed by its cleanup — on unmount, on a departure, and on the
   * withdrawal of the live admission (a refusal, a read bit, a boundary). See
   * `ColaPista`.
   */
  const cola = useRef<ColaPista | null>(null)
  useEffect(() => {
    // No admitted live half ⇒ nothing for a hint to refresh, so there is no queue to
    // own. Whatever the previous owner had queued died in the cleanup of the run that
    // installed it, which React has already called by the time this body runs.
    if (!liveAdmitted) {
      cola.current = null
      return
    }
    const mia: ColaPista = {
      episodio,
      enVuelo: false,
      sucio: false,
      vivo: true,
    }
    cola.current = mia
    return () => {
      mia.vivo = false
      if (cola.current === mia) cola.current = null
    }
  }, [episodio, liveAdmitted])

  const refrescarPorPista = useCallback(() => {
    const mia = cola.current
    if (!mia?.vivo) return

    /** Is it still THIS queue's turn? A dead owner — or one already replaced — acts on
     * nothing: not on its own record, and least of all on its successor's. */
    const vigente = () => mia.vivo && cola.current === mia

    const lanzar = () => {
      if (!vigente()) return
      mia.enVuelo = true
      void queryClient
        .refetchQueries(
          { queryKey: liveKey, exact: true },
          {
            // `cancelRefetch` DEFAULTS TO TRUE, i.e. "kill the read that is running and
            // start again" (`queryClient.js` refetchQueries → `Query.fetch`). A hint has
            // no business doing that: the operator's own Refresh, a mount fetch or an
            // invalidation may already be out, and this queue only ever knew about the
            // reads IT started. `false` makes the hint JOIN the running read instead —
            // `Query.fetch` returns the live retryer's promise — so the answer already
            // on its way is what settles this queue, and nothing in flight is thrown
            // away to ask the same question again.
            cancelRefetch: false,
            // And this is what makes the OUTCOME visible at all. Without it the promise
            // is fulfilled however the read ended, so the queue could not tell an
            // admitted answer from a refusal and authorised its trailing read anyway.
            throwOnError: true,
          },
        )
        .then(
          () => false,
          (fallo: unknown) => esRechazo(fallo),
        )
        .then((rechazada) => {
          if (!vigente()) return
          mia.enVuelo = false
          if (rechazada) {
            // THE READ THIS QUEUE EXISTS TO REPEAT WAS REFUSED. The admission it was
            // acting under is gone NOW — not at the next render, which may well commit
            // after this callback — so the owner ends here and its queued work with it.
            // A restored admission installs a queue of its own, empty.
            mia.vivo = false
            mia.sucio = false
            if (cola.current === mia) cola.current = null
            return
          }
          // An ordinary outage is not a withdrawal (see `esRechazo`): the half keeps its
          // last answer, it is still admitted, and the hint that arrived is still worth
          // exactly one read.
          const seguia = mia.sucio
          mia.sucio = false
          if (seguia) lanzar()
        })
    }

    if (mia.enVuelo) {
      mia.sucio = true
      return
    }
    lanzar()
  }, [queryClient, liveKey])

  /**
   * A FRAME IS A HINT, NOT A ROW. `session` means "the tenant inventory may have
   * changed"; what is on screen still comes from a response this principal was given.
   * The payload is deliberately not read — not merged, not appended, not counted, not
   * turned into a click target, and its timestamp is not admission.
   *
   * A hint can only ask the CURRENT admitted read to run again. It cannot bootstrap a
   * half that is refused, has lost its read bit, or has not yet had an answer of its
   * own: in those states there is no read for it to refresh, and a frame must never be
   * the thing that re-admits one.
   */
  const alLlegarPista = useCallback(() => {
    if (!liveAdmitted) return
    refrescarPorPista()
  }, [liveAdmitted, refrescarPorPista])

  /**
   * The subscription belongs to the EPISODE, not to the URL. Path, token and tenant are
   * unchanged by a same-tenant principal or credential change, so without an explicit
   * owner the previous connection would keep delivering into the new one — and on an
   * A → B → A return the reused boundary key would even make it look correct. The
   * episode is opaque, local and never reused, which is exactly what that lifetime
   * needs; it carries no id, no token and no secret.
   *
   * It is opened only over a live half that is admitted AND has an answer of its own:
   * an initial pending read, a refusal and a lost read bit each leave nothing for a
   * hint to refresh.
   */
  const { status: streamStatus } = useLiveStream<LiveDTO>({
    path: '/v1/m/sessions/stream',
    events: ['session'],
    enabled: liveAdmitted && liveQuery.data !== undefined,
    contextKey: episodio,
    onSnapshot: alLlegarPista,
  })

  /** The observed half is the ADMITTED ANSWER and nothing else. */
  const live = useMemo(
    () => (liveAdmitted ? (liveQuery.data?.items ?? []) : []),
    [liveAdmitted, liveQuery.data],
  )

  /**
   * EXACT-ORIGIN STRIPPING, AT THE INPUT OF THE JOIN. A half that is not admitted
   * contributes an EMPTY list rather than being subtracted afterwards, so a JOINED row
   * loses exactly the half that left and keeps the one that stayed: a session whose run
   * half is refused stops reading "Launched" and remains as the observed row it still
   * is, and one whose observed half is refused keeps its run and loses the telemetry.
   * Doing it here is also what makes every downstream answer agree — rows, counts,
   * filters, search and the open target read the same list.
   */
  const sessions = useMemo(
    () => mergeSessions(live, runAdmitted ? (runsQuery.data?.items ?? []) : []),
    [live, runAdmitted, runsQuery.data],
  )

  const rows = useMemo(
    () =>
      sessions.filter((s) => {
        if (source !== ALL && s.provenance !== (source as Provenance))
          return false
        if (state !== ALL) {
          const [half, value] = state.split(':')
          if (half === 'obs' && s.live?.cc_state !== value) return false
          if (half === 'run' && !s.runs.some((r) => r.state === value))
            return false
        }
        return true
      }),
    [sessions, state, source],
  )

  const counts = useMemo(() => {
    let launched = 0
    let discovered = 0
    let attention = 0
    for (const s of sessions) {
      if (s.provenance === 'launched') launched++
      else discovered++
      if (s.live?.cc_state === 'silent_evasion' || s.live?.unclaimed)
        attention++
      else if (s.runs.some((r) => r.state === 'failed')) attention++
    }
    return { total: sessions.length, launched, discovered, attention }
  }, [sessions])

  // A page that reports has_more is a page, not the estate. Provenance in THIS TABLE
  // is joined over what is loaded, so a run older than the run page would leave its
  // session reading "discovered" here. Say so rather than let the column imply a
  // completeness it does not have — the card resolves each session against the
  // engine, which is where the answer has to be right.
  //
  // Only an ADMITTED half can declare its page truncated: `has_more` from a read that
  // is no longer admitted is a fact about somebody else's answer, and it would make the
  // notice appear — or, worse, disappear — for a page that is not being shown.
  const truncated =
    (liveAdmitted && (liveQuery.data?.has_more ?? false)) ||
    (runAdmitted && (runsQuery.data?.has_more ?? false))

  // THE RUN HALF WAS NOT READ. Missing permission, a refusal or a failed lookup all
  // mean the same thing here: a row with no runs is a row nothing could be checked
  // against, and rendering it "Discovered" would state a fact this list does not hold
  // (contrast finding, 2026-08-10 — a notice beside the table does not unsay the
  // column).
  const originUnknown = !runAdmitted || runsQuery.isError

  // An error blanks the table ONLY when it leaves nothing to show. A failed run lookup
  // while the observed half answered must not throw away the rows that DID arrive: it
  // becomes the "origin not read" column and the notice above, which is the honest
  // reading — half the picture, said out loud, beats an error page over data we have.

  const columns = useMemo<TableColumn<UnifiedSession>[]>(
    () => [
      {
        id: 'session',
        // Searched by label AND by every reference: naming a row after the run an
        // operator typed must not make it unfindable by the session id the ledger,
        // the API and any saved deep link use.
        accessorFn: (s) => sessionSearchKey(s),
        header: t('cols.session'),
        cell: ({ row }) => {
          const label = sessionLabel(row.original)
          const ref = row.original.sessionRef
          return (
            <span className="flex flex-col">
              <span className="font-mono text-xs font-medium text-foreground">
                {label}
              </span>
              {ref && ref !== label && (
                <span className="font-mono text-[11px] text-muted-foreground">
                  {ref}
                </span>
              )}
            </span>
          )
        },
      },
      {
        id: 'provenance',
        accessorFn: (s) =>
          originUnknown && s.provenance === 'discovered'
            ? 'unknown'
            : s.provenance,
        header: t('cols.provenance'),
        cell: ({ row }) => (
          <ProvenanceChip
            provenance={row.original.provenance}
            unknown={originUnknown}
          />
        ),
      },
      {
        // B2: which provider INSTANCE the row belongs to — the profile it is attributed
        // to and the channel it was folded from. Legacy rows honestly show nothing.
        id: 'instance',
        accessorFn: (s) => s.profileRef ?? s.attribution ?? '',
        header: t('cols.instance'),
        cell: ({ row }) => {
          const s = row.original
          const a = s.live?.attribution
          const scoped = !!a && a !== 'legacy'
          if (!scoped && !s.profileRef)
            return <span className="text-muted-foreground">—</span>
          return (
            <span className="flex flex-col items-start gap-0.5">
              {scoped && <AttributionChip attribution={a} />}
              {s.profileRef && (
                <span
                  className="font-mono text-[11px] text-muted-foreground"
                  title={s.profileRef}
                >
                  {s.profileRef}
                </span>
              )}
            </span>
          )
        },
      },
      {
        id: 'state',
        accessorFn: (s) => s.live?.cc_state ?? primaryRun(s.runs)?.state ?? '',
        header: t('cols.state'),
        cell: ({ row }) => {
          const s = row.original
          const run = primaryRun(s.runs)
          return (
            <span className="flex flex-wrap items-center gap-1">
              {s.live && <CcStateBadge state={s.live.cc_state} />}
              {run && <RunStateBadge state={run.state} />}
              {!s.live && !run && (
                <span className="text-muted-foreground">—</span>
              )}
            </span>
          )
        },
      },
      {
        id: 'control',
        accessorFn: (s) => s.control,
        header: t('cols.control'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {t(`card.control.${row.original.control}`)}
          </span>
        ),
      },
      {
        id: 'action',
        accessorFn: (s) => s.live?.current_action ?? '',
        header: t('cols.action'),
        enableSorting: false,
        cell: ({ row }) => {
          const v = row.original.live?.current_action
          return v ? (
            <span className="truncate text-foreground" title={v}>
              {v}
            </span>
          ) : (
            <span className="text-muted-foreground">—</span>
          )
        },
      },
      {
        id: 'model',
        accessorFn: (s) =>
          s.live?.model_ref ?? primaryRun(s.runs)?.model_ref ?? '',
        header: t('cols.model'),
        cell: ({ getValue }) => {
          const v = getValue<string>()
          return v ? (
            <span className="font-mono text-xs text-muted-foreground">{v}</span>
          ) : (
            <span className="text-muted-foreground">—</span>
          )
        },
      },
      {
        id: 'tokens',
        accessorFn: (s) =>
          s.live ? s.live.input_tokens + s.live.output_tokens : -1,
        header: t('cols.tokens'),
        cell: ({ row }) => {
          const l = row.original.live
          return l ? (
            <span className="font-mono text-xs tabular-nums text-muted-foreground">
              {formatTokens(l.input_tokens, lang)} /{' '}
              {formatTokens(l.output_tokens, lang)}
            </span>
          ) : (
            <span className="text-muted-foreground">—</span>
          )
        },
      },
      {
        id: 'cost',
        accessorFn: (s) => s.live?.cost_micro_usd ?? -1,
        header: t('cols.cost'),
        cell: ({ row }) => {
          const l = row.original.live
          return l ? (
            <span className="font-mono text-xs tabular-nums text-foreground">
              {formatMicroUsd(l.cost_micro_usd, { locale: lang })}
            </span>
          ) : (
            <span className="text-muted-foreground">—</span>
          )
        },
      },
      {
        id: 'lastSeen',
        accessorFn: (s) => s.lastActivityMs,
        header: t('cols.lastSeen'),
        cell: ({ row }) =>
          row.original.lastActivityMs > 0 ? (
            <RelTimeLabel
              ts={new Date(row.original.lastActivityMs).toISOString()}
            />
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
    ],
    [t, lang, originUnknown],
  )

  /**
   * The open card, admitted NOW. A selection survives only while the context it was
   * made in still holds and every half the clicked row was made of is STILL IN THE SAME
   * ADMISSION it was chosen under.
   *
   * ⛔ HIDING IS NOT RETIRING (review R2). This used to ask only whether the half looks
   *    admitted at this instant, so the intent sat in state waiting: restoring the read
   *    bit brought the old card straight back — before the new read had answered, and
   *    again after that read answered with NO ROWS AT ALL. The card was reopened by a
   *    permission returning, not by anything the operator chose or the engine sent.
   *
   * Comparing the generation instead makes the retirement one-way: the moment the half
   * left, its generation moved, and this stamp cannot match again. Coming back is a new
   * admission, and opening a session under it takes a fresh click on a row that is
   * currently there.
   *
   * ⛔ AND THE SAME HAD TO BE TRUE OF THE CONTEXT ITSELF (review, 2026-09-08). The stamp
   *    used to be the boundary FENCE, which A → B → A recreates: with both read bits
   *    still effective, neither generation moved, the fence matched again, and A's old
   *    card reopened — through two pending reads and then an answer with no rows in it.
   *    The stamp is now the EPISODE, which counts departures and never repeats.
   */
  const target =
    selection &&
    selection.episode === episodio &&
    (!selection.needsLive ||
      (liveAdmitted && selection.liveGen === admission.liveGen)) &&
    (!selection.needsRun ||
      (runAdmitted && selection.runGen === admission.runGen))
      ? selection.target
      : null

  const abrirFila = useCallback(
    (s: UnifiedSession) => {
      setSelection({
        episode: episodio,
        needsLive: !!s.live,
        needsRun: s.runs.length > 0,
        liveGen: admission.liveGen,
        runGen: admission.runGen,
        target: sessionTarget(s),
      })
    },
    [episodio, admission.liveGen, admission.runGen],
  )

  /** The card navigating to a RELATED session: resolved by the card against the engine,
   * not backed by a row here, so it depends on the context and on nothing else. */
  const navegarDesdeTarjeta = useCallback(
    (next: SessionTarget) => {
      setSelection({
        episode: episodio,
        needsLive: false,
        needsRun: false,
        liveGen: admission.liveGen,
        runGen: admission.runGen,
        target: next,
      })
    },
    [episodio, admission.liveGen, admission.runGen],
  )

  const cerrarTarjeta = useCallback(() => setSelection(null), [])

  const showWorkspaces = canRunRead
  // B1: the provider-profile plane has its OWN read tier, independent of runs — the
  // tab follows that permission, and the panel checks write/admin at each control.
  const showProfiles = can('sessions:profile:read')
  const canBindingRead = can('sessions:profile-binding:read')
  const title = entrance === 'operate' ? t('operateTitle') : t('title')
  const subtitle =
    entrance === 'operate' ? t('operateSubtitle') : t('unifiedSubtitle')

  return (
    <div className="flex h-full flex-col gap-4">
      <PageHeader
        icon={entrance === 'operate' ? Terminal : Activity}
        title={title}
        description={subtitle}
        actions={
          <div className="flex items-center gap-2">
            {canLiveRead && <LiveDot status={streamStatus} />}
            {hayFuenteLegible && (
              <Button
                variant="ghost"
                size="sm"
                onClick={refrescarLegibles}
                disabled={
                  (canLiveRead && liveQuery.isFetching) ||
                  (canRunRead && runsQuery.isFetching)
                }
              >
                <RefreshCw
                  className={cn(
                    'size-3.5',
                    ((canLiveRead && liveQuery.isFetching) ||
                      (canRunRead && runsQuery.isFetching)) &&
                      'animate-spin',
                  )}
                />
                {t('refresh')}
              </Button>
            )}
            {canRunWrite && (
              <Button
                variant="primary"
                size="sm"
                onClick={() => setCreateOpen(true)}
              >
                <Plus className="size-3.5" />
                {t('launch')}
              </Button>
            )}
          </div>
        }
      />

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="sessions">{t('tabs.sessions')}</TabsTrigger>
          {showWorkspaces && (
            <TabsTrigger value="workspaces">{t('tabs.workspaces')}</TabsTrigger>
          )}
          {showProfiles && (
            <TabsTrigger value="profiles">{t('tabs.profiles')}</TabsTrigger>
          )}
        </TabsList>

        <TabsContent value="sessions" className="mt-4 flex flex-col gap-4">
          {/* WHAT THIS INVENTORY ACTUALLY COVERS. Every read behind it — the observed
              list, the run list and the stream — is tenant-wide: none of them takes a
              core-workspace selector, and neither DTO carries a workspace. Saying so
              beside the counts is the honest half of removing the filter that was being
              sent and ignored; a number the operator reads as "in my workspace" is
              worse than a number labelled for what it is. It describes the SCOPE, not a
              grant and not completeness — `partial.truncated` still owns the page
              disclosure, and the notices still own what could not be read. */}
          <p
            className="text-xs text-muted-foreground"
            data-testid="sessions-scope-note"
          >
            {tn('workspace.tenantWide')}
          </p>
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <Tile
              label={t('summary.total')}
              value={counts.total}
              locale={lang}
              loading={liveQuery.isLoading || runsQuery.isLoading}
            />
            {/* ⛔ MISSING METADATA IS NOT ZERO INVENTORY. These two tiles repeat the
                ORIGIN column's claim, so they answer to the column's rule: with the run
                half unread there is nothing to count launches against, and "0" would
                report an empty inventory where the honest answer is that nobody looked.
                The mark is the same "—" the row cells use, carrying the column's own
                explanation. */}
            <Tile
              label={t('summary.launched')}
              value={counts.launched}
              locale={lang}
              loading={runsQuery.isLoading}
              unknown={originUnknown}
              unknownHint={t('card.provenance.unknownExplain')}
            />
            <Tile
              label={t('summary.discovered')}
              value={counts.discovered}
              locale={lang}
              loading={liveQuery.isLoading}
              unknown={originUnknown}
              unknownHint={t('card.provenance.unknownExplain')}
            />
            <Tile
              label={t('summary.attention')}
              value={counts.attention}
              tone={counts.attention > 0 ? 'danger' : undefined}
              locale={lang}
              loading={liveQuery.isLoading || runsQuery.isLoading}
            />
          </div>

          {!canLiveRead && (
            <p className="rounded-md border border-border bg-muted px-2.5 py-2 text-xs text-muted-foreground">
              {t('partial.noLiveRead')}
            </p>
          )}
          {!canRunRead && (
            <p className="rounded-md border border-border bg-muted px-2.5 py-2 text-xs text-muted-foreground">
              {t('partial.noRunRead')}
            </p>
          )}
          {canRunRead && runsQuery.isError && (
            <p className="rounded-md border border-warning-line bg-warning-soft px-2.5 py-2 text-xs text-warning">
              {t('partial.runLookupFailed')}
            </p>
          )}
          {canLiveRead && liveQuery.isError && (
            <p className="rounded-md border border-warning-line bg-warning-soft px-2.5 py-2 text-xs text-warning">
              {t('partial.liveLookupFailed')}
            </p>
          )}
          {truncated && (
            <p className="rounded-md border border-warning-line bg-warning-soft px-2.5 py-2 text-xs text-warning">
              {t('partial.truncated')}
            </p>
          )}

          <div className="flex flex-wrap items-center justify-end gap-2">
            <Select value={source} onValueChange={setSource}>
              <SelectTrigger
                className="h-7 w-auto min-w-[9rem] text-xs"
                aria-label={t('allSources')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>{t('allSources')}</SelectItem>
                <SelectItem value="launched">
                  {t('card.provenance.launched')}
                </SelectItem>
                <SelectItem value="discovered">
                  {t('card.provenance.discovered')}
                </SelectItem>
              </SelectContent>
            </Select>
            <Select value={state} onValueChange={setState}>
              <SelectTrigger
                className="h-7 w-auto min-w-[11rem] text-xs"
                aria-label={t('allStates')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>{t('allStates')}</SelectItem>
                {OBSERVED_STATES.map((s) => (
                  <SelectItem key={`obs:${s}`} value={`obs:${s}`}>
                    {t('facet.observed', { state: t(`state.${s}`) })}
                  </SelectItem>
                ))}
                {RUN_STATES.map((s) => (
                  <SelectItem key={`run:${s}`} value={`run:${s}`}>
                    {t('facet.run', { state: t(`runState.${s}`) })}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <DataTable
            columns={columns}
            data={rows}
            isLoading={liveQuery.isLoading || runsQuery.isLoading}
            /* THE REPLACEMENT IS FOR WHEN NOTHING IS LEFT TO SHOW, and its condition is
               unchanged on purpose: both readable halves have to be out before the grid
               becomes a state. Admission decides what is PAINTED; this decides whether
               there is a table at all, and conflating them would turn one half's refusal
               into a whole-screen Forbidden — the exact over-reach this correction is
               against. `TableError` then reports what actually happened (step-up before
               plain 403, network apart from both — data-table.tsx:932), so a 5xx is never
               dressed as a permission denial. */
            error={
              (!canLiveRead || liveQuery.isError) &&
              (!canRunRead || runsQuery.isError)
                ? (liveQuery.error ?? runsQuery.error)
                : undefined
            }
            onRetry={refrescarLegibles}
            getRowId={(s) => s.key}
            onRowClick={abrirFila}
            searchable
            searchPlaceholder={t('search')}
            stickyHeader
            label={t('title')}
            empty={
              <EmptyState
                icon={<Activity />}
                title={t('empty.title')}
                description={t('empty.description')}
              />
            }
          />
        </TabsContent>

        {showWorkspaces && (
          <TabsContent value="workspaces" className="mt-4">
            <WorkspacesPanel />
          </TabsContent>
        )}
        {showProfiles && (
          <TabsContent value="profiles" className="mt-4 flex flex-col gap-3">
            {/* The plane also has its own doors, each entered on its own read tier. */}
            <p className="text-xs text-muted-foreground">
              {ta('profiles.view.crossHint')}{' '}
              <Link to={'/provider-profiles' as never} className="underline">
                {ta('profiles.view.openProfiles')}
              </Link>
              {canBindingRead && (
                <>
                  {' · '}
                  <Link
                    to={'/provider-bindings' as never}
                    className="underline"
                  >
                    {ta('profiles.view.openBindings')}
                  </Link>
                </>
              )}
            </p>
            <ProfilesPanel />
          </TabsContent>
        )}
      </Tabs>

      <RunCreateDialog open={createOpen} onOpenChange={setCreateOpen} />
      <SessionCard
        target={target}
        onClose={cerrarTarjeta}
        onNavigate={navegarDesdeTarjeta}
      />
    </div>
  )
}

function ProvenanceChip({
  provenance,
  unknown,
}: {
  provenance: Provenance
  unknown?: boolean
}) {
  const { t } = useTranslation('sessions')
  const launched = provenance === 'launched'
  // A linked run outranks not having looked: with one in hand the origin is known.
  const notKnown = !!unknown && !launched
  const key = notKnown ? 'unknown' : provenance
  return (
    <span
      title={t(`card.provenance.${key}Explain`)}
      className={cn(
        'inline-flex items-center gap-1 rounded-sm border px-1.5 py-0.5 text-[10px] font-medium',
        launched
          ? 'border-accent-line bg-accent-soft text-accent-text'
          : notKnown
            ? 'border-warning-line bg-warning-soft text-warning'
            : 'border-border bg-muted text-muted-foreground',
      )}
    >
      {launched ? (
        <Terminal className="size-3" />
      ) : notKnown ? (
        <HelpCircle className="size-3" />
      ) : (
        <Eye className="size-3" />
      )}
      {t(`card.provenance.${key}`)}
    </span>
  )
}

function Tile({
  label,
  value,
  tone,
  locale,
  loading,
  unknown,
  unknownHint,
}: {
  label: string
  value: number
  tone?: 'danger'
  locale?: string
  loading?: boolean
  /** The half this number counts was not read: say so instead of printing a total
   * derived from evidence nobody has. */
  unknown?: boolean
  unknownHint?: string
}) {
  return (
    <Card className="p-3">
      <div className="text-xs text-muted-foreground">{label}</div>
      {loading ? (
        <Skeleton className="mt-1 h-7 w-12" />
      ) : unknown ? (
        <div
          className="font-display text-2xl font-semibold tabular-nums text-muted-foreground"
          title={unknownHint}
        >
          —
        </div>
      ) : (
        <div
          className={cn(
            'font-display text-2xl font-semibold tabular-nums',
            tone === 'danger' ? 'text-danger' : 'text-foreground',
          )}
        >
          {formatInt(value, locale)}
        </div>
      )}
    </Card>
  )
}
