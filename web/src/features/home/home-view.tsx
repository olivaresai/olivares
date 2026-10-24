// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ESTATE, the home (route `/`, #2546, C2-IMPROVE E2 and E3). Three regions and nothing
// else, so the screen has one subject:
//
//  1. the summary sentence in large type ("7 sessions open for 6 people · $6.84 today
//     · 3 need you"), with today against yesterday and the month under it;
//  2. the sessions: what runs and what it costs, each one a card (the last-hour swimlane
//     takes this place, #2547), then the sessions that ended;
//  3. Needs you: the flagged request with what will run and the rule that asked, decided
//     here in one click, then the others as quiet rows.
//
// The start line and the proof line ("Chain verified at 16:40 · 12,408 entries") share one
// row under the title, reserved from the first paint. The figure tiles left the home: inventory, security, compliance, health,
// budgets and the kill switch each have their place, Stop all is in the status bar while
// a session runs, and spend by tool belongs to the Ledger. The home asks nothing of those modules.
//
// Every figure is a read this page makes for its own regions: the runs Olivares operates
// and the live sessions it observes (one page each), the pending requests, the ledger's
// head and the monthly budget. A read the role may not make is not made, and a source
// with no answer leaves its clause out: nothing here is a made-up zero.
import { lazy, Suspense, useEffect, useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { LayoutDashboard } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { useAuth } from '@/lib/auth/context'
import { auditApi } from '@/lib/api/endpoints'
import { EmptyState } from '@/components/ui/empty-state'
import { IntelPage } from '@/features/_intel'
import { currentAnswer } from '@/features/executive/derive'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { sessionsApi, sessionsKeys } from '@/features/sessions/api'
import { mergeSessions } from '@/features/sessions/provenance'
import { usePendingApprovals } from '@/features/governance/use-pending-approvals'
import { useMinuteClock } from '@/components/layout/use-session-rail'
import { EstateLanes } from './estate-lanes'
import {
  decodeSessionAddress,
  SESSION_PARAM,
} from '@/features/sessions/session-address'
import { UrlStateNotice } from '@/features/shared'
import { useValidatedUrlState } from '@/lib/hooks/use-url-state'
import { EstateSummary } from './estate-summary'
import { LiveCards } from './live-cards'
import { useMonthBudget } from './month-budget'
import {
  actsForPeople,
  dayCost,
  dayIsFloor,
  isLiveSession,
} from './now-sessions'
import { NowQueue, useFlaggedRequest, useNeedsYouRows } from './now-queue'
import { NowStart } from './now-start'
import { ProofLine } from './proof-line'
import { RecentWork } from './recent-work'
import { pendingReason, tileState } from './view-state'
import './i18n'

// The sheet carries the whole conversation renderer: loaded on the first open, not with
// every visit to Estate.
const SessionSheet = lazy(() =>
  import('@/features/sessions/session-sheet').then((m) => ({
    default: m.SessionSheet,
  })),
)

/** How many runs Estate reads for the sessions region (it shows the newest few). */
const HOME_RUN_PARAMS = { limit: 30 } as const

export function HomeView() {
  const { t } = useTranslation(['home', 'nav'])
  const { activeTenant, can } = useAuth()
  const queryClient = useQueryClient()
  // THE SESSION OPEN OVER THE ESTATE (#2549): a place, so opening one is a history entry
  // and Back closes it; closing replaces it, so Back does not reopen it. The address is
  // untrusted: one that names no session is cleared and said, as on Sessions.
  const [sheet, setSheet, sheetIssues] = useValidatedUrlState(
    [SESSION_PARAM],
    decodeSessionAddress,
    { history: 'push' },
  )
  const openSession = (address: string) =>
    setSheet({ [SESSION_PARAM]: address })
  // Mounted from the first open on, so it can slide out when it closes.
  const [sheetUsed, setSheetUsed] = useState(false)
  if (sheet.address && !sheetUsed) setSheetUsed(true)

  // --- the reads (only the permitted ones run) --------------------------------
  // Live sessions are tenant-wide (GET /v1/m/sessions/live takes no workspace selector).
  const canSessions = can('sessions:live:read')
  const sessionsQ = useQuery({
    queryKey: sessionsKeys.live(activeTenant),
    queryFn: () => sessionsApi.live(),
    enabled: canSessions,
    // The clock ticks the figures between polls; this cadence is what certifies
    // new usage (the costs are the engine's numbers, never extrapolated).
    refetchInterval: 15_000,
  })
  // The sessions Olivares operates, so a launched session is on Estate by its name and
  // state (one row with what was observed of it, merged below), with its session facts.
  const canRuns = can('sessions:run:read')
  const runsQ = useQuery({
    queryKey: [...agentOpsKeys.runs(activeTenant, HOME_RUN_PARAMS), 'home'],
    queryFn: () => agentOpsApi.listRuns(HOME_RUN_PARAMS),
    enabled: canRuns,
  })
  // Pending requests for Needs you: the approval queue's own read (shared cache).
  const pendingApprovals = usePendingApprovals()
  const month = useMonthBudget()

  // ⛔ A PAUSED READ OUTLIVES THE PERMISSION THAT DISPATCHED IT unless it is cancelled.
  //    `enabled: false` stops a query from STARTING, not one already dispatched and
  //    paused (query-core continues every paused retryer when the client comes back
  //    online). Cancelling with TanStack's default `revert` puts it back to pending/idle,
  //    where a later re-grant starts it afresh. Scoped to the exact key.
  useEffect(() => {
    if (canSessions) return
    void queryClient.cancelQueries({
      queryKey: sessionsKeys.live(activeTenant),
      exact: true,
    })
  }, [canSessions, activeTenant, queryClient])

  // Only CURRENT, permitted answers (`currentAnswer`): a read the role lost, or one whose
  // refetch failed, keeps its old rows on the query, and those must not paint a card or
  // count toward a figure.
  const liveAnswer = currentAnswer(canSessions, sessionsQ)
  const runsAnswer = currentAnswer(canRuns, runsQ)
  const recentSessions = useMemo(
    () =>
      liveAnswer || runsAnswer
        ? mergeSessions(liveAnswer?.items ?? [], runsAnswer?.items ?? [])
        : undefined,
    [liveAnswer, runsAnswer],
  )
  // Each session once: a live one is a card, an ended one a Recent work row.
  const liveSessions = useMemo(
    () => (recentSessions ?? []).filter(isLiveSession),
    [recentSessions],
  )
  const settledSessions = useMemo(
    () => recentSessions?.filter((s) => !isLiveSession(s)),
    [recentSessions],
  )
  const sessionReads = [
    ...(canRuns ? [runsQ] : []),
    ...(canSessions ? [sessionsQ] : []),
  ]
  const readsSessions = sessionReads.length > 0
  const recentState = tileState(canRuns ? runsQ : sessionsQ)
  // The minute clock moves "today" past midnight without a reload.
  const now = useMinuteClock()
  // A page cut short hides a day's sessions only if all it holds is from that day on.
  const activity = {
    live: (liveAnswer?.items ?? []).map((l) => Date.parse(l.last_event_at)),
    runs: (runsAnswer?.items ?? []).map((r) =>
      Date.parse(r.last_activity_at ?? r.started_at ?? r.created_at ?? ''),
    ),
  }
  const floor = (daysAgo: number) =>
    dayIsFloor(liveAnswer?.has_more, activity.live, now, daysAgo) ||
    dayIsFloor(runsAnswer?.has_more, activity.runs, now, daysAgo)
  // A cut page leaves yesterday unknown: no figure rather than part of one.
  const yesterday =
    recentSessions && !floor(1) ? dayCost(recentSessions, now, 1) : null
  // Recent work steps aside only when it has answered and every session is a card.
  const showRecent = !(
    recentState === 'ready' &&
    liveSessions.length > 0 &&
    settledSessions?.length === 0
  )

  // What needs a person: the pending requests and the rows of the list beside them.
  const needsRows = useNeedsYouRows(
    canSessions ? sessionsQ.data?.items : undefined,
    canSessions,
  ).rows
  // The current answer only: a page whose refetch failed keeps its old rows on the query,
  // and a request that may have been decided since must not keep its Approve.
  const approvals = currentAnswer(
    pendingApprovals.permitted,
    pendingApprovals.query,
  )
  // A requests read that failed or has not answered leaves the count unknown: the clause
  // waits for it rather than counting the sessions half alone.
  const approvalsUnknown = pendingApprovals.permitted && !approvals
  const flag = useFlaggedRequest(approvals?.items)
  // Approve on the flagged request is Estate's one orange control; while it shows, the
  // start line steps back.
  const approveShown = !!flag.approval && !flag.gone

  // THE PROOF: the chain verdict the run read carries, and the ledger's head for whoever
  // may read the ledger. Read once a verdict is there to qualify.
  const proof = runsAnswer?.items.find(
    (r) => r.proof?.chain_verified != null,
  )?.proof
  const canAudit = can('audit:read')
  const headQ = useQuery({
    queryKey: ['audit', activeTenant, 'tenant', 'head'],
    queryFn: async () => (await auditApi.recent({ limit: 1 })).head_seq ?? null,
    enabled: canAudit && !!proof,
  })

  if (!readsSessions && !pendingApprovals.permitted) {
    return (
      <IntelPage
        icon={LayoutDashboard}
        title={t('nav:shell.journeys.home')}
        className="gap-0"
      >
        <EmptyState
          title={t('empty.title')}
          description={t('empty.description')}
        />
      </IntelPage>
    )
  }

  return (
    <IntelPage
      icon={LayoutDashboard}
      title={t('nav:shell.journeys.home')}
      className="gap-0"
      // THE START LINE (#3121): the verb first, then what it runs on, and the proof at the
      // right, in one row under the title that is there from the first paint. Nothing
      // arrives above the page late, so nothing below it moves.
      // A role that cannot start sessions gets the row only when there is a proof to show.
      notices={
        can('sessions:run:write') || proof ? (
          <div
            data-testid="now-header-line"
            className="flex min-h-9 flex-wrap items-center gap-x-3 gap-y-2"
          >
            <NowStart quiet={approveShown} />
            {proof ? (
              <span className="ml-auto">
                <ProofLine
                  verified={proof.chain_verified}
                  at={runsQ.dataUpdatedAt}
                  entries={canAudit && headQ.isSuccess ? headQ.data : null}
                />
              </span>
            ) : null}
          </div>
        ) : null
      }
    >
      <UrlStateNotice issues={sheetIssues} />
      <div className="flex flex-col gap-6">
        {readsSessions ? (
          <EstateSummary
            // A read with no answer yet that is not fetching (paused offline, or not
            // started) is no answer either: no sentence, and the reason is said.
            state={
              recentSessions || tileState(...sessionReads) === 'unavailable'
                ? tileState(...sessionReads)
                : 'loading'
            }
            pending={
              recentSessions
                ? null
                : (sessionReads.map(pendingReason).find(Boolean) ?? null)
            }
            running={recentSessions ? liveSessions.length : null}
            // The live read is one page: more behind it makes the count a floor.
            runningMore={!!liveAnswer?.has_more}
            people={actsForPeople(liveSessions)}
            today={recentSessions ? dayCost(recentSessions, now) : null}
            todayFloor={floor(0)}
            yesterday={yesterday}
            needs={
              !approvalsUnknown && (approvals || canSessions)
                ? {
                    count: needsRows.length + (approvals?.items.length ?? 0),
                    more: !!approvals?.has_more,
                  }
                : null
            }
            month={month}
          />
        ) : null}
        {/* Needs you comes first in reading order: at 1024 it is a full-width strip above
            the sessions, and from 1280 it is the right-hand column beside them. */}
        <div className="grid gap-x-8 gap-y-6 xl:grid-cols-[minmax(0,1fr)_400px]">
          {canSessions || pendingApprovals.permitted ? (
            <div className="min-w-0 xl:col-start-2 xl:row-start-1">
              <NowQueue
                sessions={sessionsQ.data?.items}
                state={tileState(sessionsQ)}
                sessionsReadable={canSessions}
                approvals={approvals?.items}
                flag={flag}
                approvalsState={
                  pendingApprovals.permitted
                    ? tileState(pendingApprovals.query)
                    : 'ready'
                }
              />
            </div>
          ) : null}
          {readsSessions ? (
            <div
              data-estate-region="sessions"
              className="flex min-w-0 flex-col gap-6 xl:col-start-1 xl:row-start-1"
            >
              {/* THE LAST HOUR (#2547): every live session over time. It reads the
                  sessions above and each one's own timeline (which needs the live read).
                  Past FLEET_SESSIONS live sessions a Business build draws its fleet grid
                  instead (#2565). */}
              {canSessions && recentSessions ? (
                <EstateLanes
                  sessions={recentSessions}
                  approvals={approvals?.items ?? []}
                  approvalsRead={
                    pendingApprovals.permitted &&
                    !pendingApprovals.query.isError
                  }
                  now={now}
                  onOpen={openSession}
                />
              ) : null}
              {liveSessions.length > 0 ? (
                <LiveCards
                  sessions={liveSessions}
                  onOpen={openSession}
                  // Recent work carries the link only once it lists rows; loading,
                  // failed or stepped aside, the cards carry it.
                  showAll={!showRecent || recentState !== 'ready'}
                />
              ) : null}
              {showRecent ? (
                <RecentWork
                  titled
                  sessions={settledSessions}
                  state={recentState}
                  onOpen={openSession}
                />
              ) : null}
            </div>
          ) : null}
        </div>
      </div>
      {sheetUsed ? (
        <Suspense fallback={null}>
          <SessionSheet
            address={sheet.address}
            onClose={() =>
              setSheet({ [SESSION_PARAM]: undefined }, { history: 'replace' })
            }
          />
        </Suspense>
      ) : null}
    </IntelPage>
  )
}
