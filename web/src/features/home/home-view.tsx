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
import { lazy, Suspense, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { LayoutDashboard } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { useAuth } from '@/lib/auth/context'
import { auditApi } from '@/lib/api/endpoints'
import { EmptyState } from '@/components/ui/empty-state'
import { IntelPage } from '@/features/_intel'
import { currentAnswer } from '@/features/executive/derive'
import { usePendingApprovals } from '@/features/governance/use-pending-approvals'
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
import { actsForPeople, dayCost, isLiveSession } from './now-sessions'
import { NowQueue, useFlaggedRequest, useNeedsYouRows } from './now-queue'
import { NowStart } from './now-start'
import { ProofLine } from './proof-line'
import { RecentWork } from './recent-work'
import { pendingReason, tileState } from './view-state'
import { useRecentSessions } from './use-recent-sessions'
import './i18n'

// The sheet carries the whole conversation renderer: loaded on the first open, not with
// every visit to Estate.
const SessionSheet = lazy(() =>
  import('@/features/sessions/session-sheet').then((m) => ({
    default: m.SessionSheet,
  })),
)

export function HomeView() {
  const { t } = useTranslation(['home', 'nav'])
  const { activeTenant, can } = useAuth()
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
  const {
    canSessions,
    canRuns,
    sessionsQ,
    runsQ,
    liveAnswer,
    runsAnswer,
    recentSessions,
    now,
    floor,
  } = useRecentSessions()
  // Pending requests for Needs you: the approval queue's own read (shared cache).
  const pendingApprovals = usePendingApprovals()
  const month = useMonthBudget()

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
  // A cut page leaves yesterday unknown: no figure rather than part of one.
  const yesterday =
    recentSessions && !floor(1) ? dayCost(recentSessions, now, 1) : null
  // Recent work steps aside only when it has answered and every session is a card.
  const showRecent = !(
    recentState === 'ready' &&
    liveSessions.length > 0 &&
    settledSessions?.length === 0
  )

  // The current answer only: a page whose refetch failed keeps its old rows on the query,
  // and a request that may have been decided since must not keep its Approve.
  const approvals = currentAnswer(
    pendingApprovals.permitted,
    pendingApprovals.query,
  )
  // The summary counts the same rows and marks as the list beside it.
  const needsRows = useNeedsYouRows(
    liveAnswer?.items,
    canSessions,
    runsAnswer?.items,
    approvals?.items,
  ).rows
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
        title={t('nav:places.now')}
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
      title={t('nav:places.now')}
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
                sessions={liveAnswer?.items}
                runs={runsAnswer?.items}
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
