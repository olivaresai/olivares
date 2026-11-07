// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// OVERVIEW (`/now`,): what needs a person across the installation, what runs and what
// ended, in one column and in that order:
//
//  1. the strip: need you, running, failed today, cost today (the Sessions strip, with the
//     same rules: a part with nothing to say, or not known, is left out);
//  2. Needs you: the flagged request decided here in one click, then the others as rows;
//  3. Running, then Recent: one row per session with a chevron to it.
//
// The last-hour lanes are Usage › Timeline; the composer on Sessions starts a session.
// Every figure is a read this page makes for its own regions: the runs Olivares operates
// and the live sessions it observes (one page each) and the pending requests. A read the
// role may not make is not made, and a source with no answer leaves its part out.
import { lazy, Suspense, useMemo, useState } from 'react'
import { LayoutDashboard } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { EmptyState } from '@/components/ui/empty-state'
import { IntelPage } from '@/features/_intel'
import { currentAnswer } from '@/features/executive/derive'
import { usePendingApprovals } from '@/features/governance/use-pending-approvals'
import { useViewAccess } from '@/features/navigation/authorization'
import { viewById } from '@/features/navigation/model'
import {
  decodeSessionAddress,
  SESSION_PARAM,
} from '@/features/sessions/session-address'
import { inSection } from '@/features/sessions/session-groups'
import { SessionStrip } from '@/features/sessions/session-strip'
import { useUrlStateNotice } from '@/features/shared'
import { useValidatedUrlState } from '@/lib/hooks/use-url-state'
import { dayCost, failedToday } from './now-sessions'
import { NowQueue, useFlaggedRequest, useNeedsYouRows } from './now-queue'
import { RecentWork } from './recent-work'
import { tileState } from './view-state'
import { useRecentSessions } from './use-recent-sessions'
import './i18n'

// The sheet carries the whole conversation renderer: loaded on the first open, not with
// every visit to Overview.
const SessionSheet = lazy(() =>
  import('@/features/sessions/session-sheet').then((m) => ({
    default: m.SessionSheet,
  })),
)

export function HomeView() {
  const { t } = useTranslation(['home', 'nav'])
  // THE SESSION OPEN OVER OVERVIEW: a place, so opening one is a history entry
  // and Back closes it; closing replaces it, so Back does not reopen it. The address is
  // untrusted: one that names no session is cleared and said, as on Sessions.
  const [sheet, setSheet, sheetIssues] = useValidatedUrlState(
    [SESSION_PARAM],
    decodeSessionAddress,
    { history: 'push' },
  )
  useUrlStateNotice(sheetIssues)
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
  const { navigable } = useViewAccess()

  // Each session once: a working one is a Running row (the strip's own count), every
  // other one, idle or waiting for a person included, a Recent row.
  const liveSessions = useMemo(
    () => (recentSessions ?? []).filter((s) => inSection(s, 'active')),
    [recentSessions],
  )
  const settledSessions = useMemo(
    () => recentSessions?.filter((s) => !inSection(s, 'active')),
    [recentSessions],
  )
  const readsSessions = canRuns || canSessions
  const recentState = tileState(canRuns ? runsQ : sessionsQ)
  // Recent steps aside only when it has answered and every session is live; Running then
  // carries the way to every session.
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
  const flag = useFlaggedRequest(approvals?.items)
  // Needs you, read once: the strip's count and the section's are this one count.
  const needs = useNeedsYouRows(
    liveAnswer?.items,
    canSessions,
    runsAnswer?.items,
    approvals?.items,
    tileState(sessionsQ),
    pendingApprovals.permitted ? tileState(pendingApprovals.query) : 'ready',
  )

  // THE STRIP (session-strip.tsx), by the Sessions strip's rules: without the run half
  // what runs, failed and cost is not known, a page cut short leaves today's failures and
  // cost out, and the cost is what today's sessions reported.
  // ponytail: running counts the recent run page (30 runs), so past 30 sessions working at
  // once it is a floor; read the Sessions page's working set when a fleet reaches it.
  const exact = !!runsAnswer && !floor(0)
  const today = recentSessions && exact ? dayCost(recentSessions, now) : null
  const sessionsView = viewById('sessions')
  const usageView = viewById('usage')
  const strip = recentSessions
    ? {
        needYou: needs.count,
        // This list holds the sessions only; the count also holds approvals, so it is text.
        needYouLinked: false,
        running: runsAnswer ? liveSessions.length : null,
        failed: exact ? failedToday(recentSessions, now) : null,
        costMicroUsd: today?.costMicroUsd ?? null,
        listPath:
          sessionsView && navigable(sessionsView) ? sessionsView.path : null,
        usagePath: usageView && navigable(usageView) ? usageView.path : null,
      }
    : null

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
      // The top bar names the page; the first screen does not say it twice.
      titleHidden
      className="gap-0"
    >
      <div className="flex max-w-3xl flex-col gap-6">
        {strip ? <SessionStrip {...strip} className="px-0 pb-0" /> : null}
        {canSessions || pendingApprovals.permitted ? (
          <NowQueue
            needs={needs}
            approvals={approvals?.items}
            flag={flag}
            approvalsState={
              pendingApprovals.permitted
                ? tileState(pendingApprovals.query)
                : 'ready'
            }
          />
        ) : null}
        {readsSessions && liveSessions.length > 0 ? (
          <RecentWork
            titled
            title={t('overview.running')}
            sessions={liveSessions}
            state="ready"
            openAll={!showRecent}
            onOpen={openSession}
          />
        ) : null}
        {readsSessions && showRecent ? (
          <RecentWork
            titled
            title={t('overview.recent')}
            sessions={settledSessions}
            state={recentState}
            onOpen={openSession}
          />
        ) : null}
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
