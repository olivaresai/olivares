// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// NEEDS YOU (console remake 26.10): the first list on Now. The rows are the session rail's
// own classification (components/layout/session-rail-model.ts): a session that went silent
// or that nobody claimed, and a handoff offered to this operator. The sessions are the page
// Now ALREADY holds for its tile and its list — no second sessions read — and the handoffs
// are the rail's own offered-handoffs read. The rail left the sidebar; its "Needs you"
// group lives here, where each row has room for its reason and one action.
//
// PENDING APPROVALS come first (slice 4): a request that waits on a person's decision is
// the most direct "needs you" there is. They are the approval queue's own pending read
// (features/governance/use-pending-approvals.ts) — the top bar's count reads the same
// cache. One is FLAGGED (#2546): the first this person may decide, by the queue's own rule
// (approval-queue.tsx `flaggedApprovalId`). Its card says what will run, the rule that
// asked, what it reaches, and offers Approve once, the one orange control on Estate,
// decided in one click when the card shows the whole request; otherwise it opens the
// request beside the queue. Deny asks for a reason in the queue's own dialog. The card
// holds that request until this person decides it (`useFlaggedRequest`).
// The others are quiet rows that open the queue.
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useCallback, useEffect, useRef, useState } from 'react'
import { ArrowRightLeft, Check, CircleAlert, ShieldCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { readableAction } from '@/components/layout/bell-events'
import {
  railGroups,
  type RailRow,
} from '@/components/layout/session-rail-model'
import {
  useMinuteClock,
  useOfferedHandoffs,
} from '@/components/layout/use-session-rail'
import {
  ApprovalRequestCell,
  ApprovalWhatRuns,
  ApprovalWhy,
  approvalCommandUnknown,
  approvalExpectsCommand,
  reviewIsLong,
  useApprovalSession,
} from '@/features/governance/approval-preview'
import { governanceApi, governanceKeys } from '@/features/governance/api'
import { flaggedApprovalId } from '@/features/governance/approval-queue'
import { DenyDialog, useDecision } from '@/features/governance/decision-dialog'
import {
  canDecideOnRequest,
  type ApprovalDTO,
} from '@/features/governance/types'
import { useAuth } from '@/lib/auth/context'
import '@/features/governance/i18n'
import type { LiveDTO } from '@/features/sessions/types'
import type { TileState } from './view-state'
import './i18n'

function age(row: RailRow, t: TFunction<readonly ['home', 'nav']>): string {
  if (row.minutes < 60)
    return t('nav:shell.rail.minutes', { count: row.minutes })
  if (row.minutes < 60 * 24)
    return t('nav:shell.rail.hours', { count: Math.floor(row.minutes / 60) })
  return t('nav:shell.rail.days', { count: Math.floor(row.minutes / 1440) })
}

function QueueRow({ row }: { row: RailRow }) {
  const { t } = useTranslation(['home', 'nav'])
  const handoff = row.kind === 'handoff'
  const title = handoff
    ? t('nav:shell.rail.handoffFrom', { from: row.from ?? '' })
    : (row.title ?? `${t('nav:shell.rail.session')} ${row.reference}`)
  const meta = handoff
    ? `${t('nav:shell.rail.workItem')} ${row.reference}`
    : row.meta
  const Icon = handoff ? ArrowRightLeft : CircleAlert
  return (
    <li className="border-b border-line last:border-b-0">
      <Link
        to={row.to as never}
        search={row.search as never}
        data-testid="now-needs-row"
        className="flex min-h-[52px] min-w-0 items-center gap-3 px-1.5 py-2 outline-none transition-colors hover:bg-hover focus-visible:ring-2 focus-visible:ring-focus focus-visible:ring-inset"
      >
        <span className="grid size-7 shrink-0 place-items-center rounded-lg bg-warning-soft text-warning">
          <Icon aria-hidden className="size-4" />
        </span>
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="truncate font-medium text-text">{title}</span>
          {meta ? (
            <span className="truncate text-caption text-text-2">{meta}</span>
          ) : null}
        </span>
        <span className="shrink-0 text-caption text-text-3 tabular-nums">
          {age(row, t)}
        </span>
      </Link>
    </li>
  )
}

/** The permission that decides a request (governance-view.tsx `canDecide`). */
const APPROVAL_DECIDE = 'governance:approval:admin'

/** How long a request that just took the flagged place ignores Approve: the queue's poll
 * can swap the card under a pointer already on its way to the button. */
export const FLAG_ARM_MS = 500

/**
 * One click approves only what the card shows in full: a reviewed command short enough
 * not to be cut, or a request that runs nothing by itself. A long command, one the tool
 * did not name, or a tool call stored without its review opens the request beside the
 * queue, where the whole of it is read above Approve once.
 */
function oneClickApprove(approval: ApprovalDTO): boolean {
  if (approvalCommandUnknown(approval)) return false
  const text = approval.review?.text
  return text ? !reviewIsLong(text) : !approvalExpectsCommand(approval)
}

/** The request Estate's card holds, and whether the queue still holds it. */
export interface FlaggedRequest {
  approval?: ApprovalDTO
  /** Decided or expired elsewhere since the card showed it: nothing left to decide. */
  gone: boolean
  /** This person decided it, or had already: the card moves to the next one. */
  settle: (id: string) => void
}

/**
 * THE REQUEST ON THE CARD STAYS ON THE CARD (the queue's own rule, governance-view.tsx).
 * The card takes the first request this person may decide and keeps it until they decide
 * it: a poll or another approver never swaps a different request in under their hand. One
 * decided or expired elsewhere stays and says so; it offers nothing to decide, only Close,
 * and the card moves on when the person closes it. `approvals`
 * is the current answer only; while there is none the card shows nothing.
 */
export function useFlaggedRequest(
  approvals: readonly ApprovalDTO[] | undefined,
): FlaggedRequest {
  const { can, principal, activeTenant } = useAuth()
  const [held, setHeld] = useState<{
    tenant: string | null
    approval: ApprovalDTO
  } | null>(null)
  const [settled, setSettled] = useState<ReadonlySet<string>>(() => new Set())
  const settle = useCallback(
    (id: string) => setSettled((s) => new Set(s).add(id)),
    [],
  )
  // Separation of duties decides the flag as it decides the buttons: the requester
  // cannot decide their own, and a token has no stable user id. Asked on every render,
  // so a role or a sign-in that changes takes the card with it.
  const mayDecide = (a: ApprovalDTO) =>
    !settled.has(a.id) &&
    can(APPROVAL_DECIDE) &&
    canDecideOnRequest(a.requested_by, principal?.actor, principal?.kind)
  // A request of another workspace is never this one's.
  const kept =
    held && held.tenant === activeTenant && mayDecide(held.approval)
      ? held.approval
      : null
  if (approvals && !kept) {
    const id = flaggedApprovalId(approvals, mayDecide)
    const next = approvals.find((a) => a.id === id) ?? null
    const have = held && held.tenant === activeTenant ? held.approval.id : null
    if ((next?.id ?? null) !== have)
      setHeld(next ? { tenant: activeTenant, approval: next } : null)
  }
  if (!approvals || !kept) return { gone: false, settle }
  const current = approvals.find((a) => a.id === kept.id)
  return { approval: current ?? kept, gone: !current, settle }
}

/** The flagged request, as a person decides it here. */
function FlaggedCard({
  approval,
  gone,
  onSettled,
}: {
  approval: ApprovalDTO
  gone: boolean
  onSettled: () => void
}) {
  const { t } = useTranslation(['home', 'governance', 'common'])
  const { principal, activeTenant } = useAuth()
  const navigate = useNavigate()
  const session = useApprovalSession(approval)
  const [denyOpen, setDenyOpen] = useState(false)
  // Decided (or refused as no longer pending): the card moves on. A request that needs
  // more than one person stays in the queue as a quiet row, waiting for the others.
  const approve = useDecision(approval.id, 'approve', onSettled)
  // The card is keyed by the request, so a new head of the queue mounts a new card and
  // arms its Approve afresh.
  const armed = useRef(false)
  useEffect(() => {
    const timer = setTimeout(() => {
      armed.current = true
    }, FLAG_ARM_MS)
    return () => clearTimeout(timer)
  }, [])
  // The request itself, beside the queue (as Needs you links it everywhere).
  const requestSearch = { tab: 'approvals', approval: approval.id }
  const onApprove = () => {
    if (!oneClickApprove(approval))
      void navigate({
        to: '/permissions' as never,
        search: requestSearch as never,
      })
    else if (armed.current) approve.mutate({ decision: 'approve' })
  }
  // The card only holds a request this person may decide (`useFlaggedRequest`). One that
  // needs more than one person stays pending after their approval, and the engine refuses
  // their second decision (409): one they already approved, after a reload too, is
  // settled and the card moves on (the request panel's own read, approval-detail.tsx).
  const decisions = useQuery({
    queryKey: governanceKeys.decisions(activeTenant, approval.id),
    queryFn: () => governanceApi.listDecisions(approval.id),
    enabled: approval.required_approvals > 1 && !gone,
  })
  const decidedByMe =
    !!principal?.actor &&
    !!decisions.data?.items.some((d) => d.decider === principal.actor)
  useEffect(() => {
    if (decidedByMe) onSettled()
  }, [decidedByMe, onSettled])
  const toolCall = approval.subject_kind?.endsWith('.tool')
  const head =
    [approval.review?.tool, session.name].filter(Boolean).join(' · ') ||
    (!toolCall && approval.action
      ? readableAction(approval.action)
      : t('governance:preview.requestSummary'))
  const headingId = `now-flagged-${approval.id}`
  return (
    <article
      aria-labelledby={headingId}
      data-testid="now-flagged-request"
      className="flex min-w-0 flex-col gap-3 rounded-lg border-l-2 border-accent bg-surface px-4 py-3"
    >
      <div className="flex min-w-0 flex-col gap-0.5">
        <h3 id={headingId} className="break-words text-heading text-text">
          {head}
        </h3>
        {session.folder ? (
          <span
            className="truncate font-mono text-caption text-text-2"
            title={session.folder}
          >
            {session.folder}
          </span>
        ) : null}
      </div>
      <ApprovalWhatRuns approval={approval} />
      {/* The rule that asked and what it reaches: the queue's own two lines. */}
      <ApprovalWhy approval={approval} reaches={session.reaches} />
      {gone ? (
        <p className="text-caption text-text-2">
          {t('governance:decide.notPending')}
        </p>
      ) : null}
      {approval.required_approvals > 1 ? (
        <p className="text-caption text-text-2 tabular-nums">
          {t('governance:approvals.progressOf', {
            approved: approval.approve_count,
            required: approval.required_approvals,
          })}
        </p>
      ) : null}
      <div className="flex flex-wrap items-center gap-2">
        {!gone && !decidedByMe ? (
          <>
            <Button
              variant="primary"
              disabled={approve.isPending}
              onClick={onApprove}
            >
              <Check aria-hidden />
              {t('governance:approvals.approveOnce')}
            </Button>
            <Button variant="ghost" onClick={() => setDenyOpen(true)}>
              {t('governance:approvals.deny')}
            </Button>
          </>
        ) : null}
        {gone ? (
          <Button variant="ghost" onClick={onSettled}>
            {t('common:actions.close')}
          </Button>
        ) : null}
        <Button variant="ghost" asChild>
          <Link to={'/permissions' as never} search={requestSearch as never}>
            {t('governance:preview.details')}
          </Link>
        </Button>
      </div>
      <DenyDialog
        open={denyOpen}
        onOpenChange={setDenyOpen}
        approval={approval}
        onDecided={onSettled}
      />
    </article>
  )
}

function ApprovalRow({ approval }: { approval: ApprovalDTO }) {
  const { t } = useTranslation(['governance'])
  return (
    <li className="border-b border-line last:border-b-0">
      <Link
        to={'/permissions' as never}
        // The request itself opens, beside the queue, as the rail's Needs you links it.
        search={{ tab: 'approvals', approval: approval.id } as never}
        data-testid="now-approval-row"
        className="flex min-h-[52px] min-w-0 items-center gap-3 px-1.5 py-2 outline-none transition-colors hover:bg-hover focus-visible:ring-2 focus-visible:ring-focus focus-visible:ring-inset"
      >
        <span className="grid size-7 shrink-0 place-items-center rounded-lg bg-warning-soft text-warning">
          <ShieldCheck aria-hidden className="size-4" />
        </span>
        <span className="min-w-0 flex-1">
          <ApprovalRequestCell
            approval={approval}
            className="w-full max-w-full"
          />
        </span>
        <span className="shrink-0 text-caption text-text-3 tabular-nums">
          {t('governance:approvals.progressOf', {
            approved: approval.approve_count,
            required: approval.required_approvals,
          })}
        </span>
      </Link>
    </li>
  )
}

/** What needs a person besides the requests: the sessions that went silent or that
 * nobody claimed, and the handoffs offered to this person. Estate's sentence counts the
 * same rows this list shows. */
export function useNeedsYouRows(
  sessions: LiveDTO[] | undefined,
  sessionsReadable = true,
) {
  const now = useMinuteClock()
  const { readHandoffs, handoffs } = useOfferedHandoffs()
  const rows =
    railGroups(
      {
        live: sessionsReadable ? (sessions ?? []) : [],
        handoffs: handoffs.data?.items ?? [],
      },
      now,
    ).find((g) => g.id === 'needsYou')?.rows ?? []
  return { rows, readHandoffs, handoffs }
}

export function NowQueue({
  sessions,
  state,
  sessionsReadable = true,
  approvals = [],
  approvalsState = 'ready',
  flag,
}: {
  /** The live page Estate already fetched (its sentence reads the same page). */
  sessions: LiveDTO[] | undefined
  state: TileState
  /** False for a principal who may not read live sessions: the queue then holds what it
   * may read (approvals, offered handoffs) and says nothing about sessions. */
  sessionsReadable?: boolean
  /** The pending approvals this principal may read (none when it may not). */
  approvals?: readonly ApprovalDTO[]
  /** The approvals read's own state: a failed read is said, never "nothing waits". */
  approvalsState?: TileState
  /** The request the card holds: Estate's own, which also decides its orange control. */
  flag: FlaggedRequest
}) {
  const { t } = useTranslation(['home', 'nav'])
  const { rows, readHandoffs, handoffs } = useNeedsYouRows(
    sessions,
    sessionsReadable,
  )
  const flagged = flag.approval
  const quiet = approvals.filter((a) => a.id !== flagged?.id)
  const { settle } = flag
  const flaggedId = flagged?.id
  const settleFlagged = useCallback(() => {
    if (flaggedId) settle(flaggedId)
  }, [settle, flaggedId])
  const sessionsState: TileState = sessionsReadable ? state : 'ready'
  const status: 'loading' | 'error' | 'ready' =
    sessionsState === 'unavailable' && (!readHandoffs || handoffs.isError)
      ? 'error'
      : sessionsState === 'loading' ||
          (approvalsState === 'loading' && approvals.length === 0) ||
          (readHandoffs &&
            handoffs.isPending &&
            handoffs.fetchStatus !== 'idle')
        ? 'loading'
        : 'ready'
  return (
    <section
      aria-labelledby="now-needs-heading"
      data-testid="now-needs"
      data-estate-region="needs"
    >
      <div className="mb-1.5 flex items-baseline gap-2">
        <h2
          id="now-needs-heading"
          className="text-heading font-semibold text-text"
        >
          {t('nav:shell.rail.groups.needsYou')}
        </h2>
        {status === 'ready' && approvalsState !== 'unavailable' ? (
          <span className="font-mono text-mono-s text-text-3 tabular-nums">
            {rows.length + approvals.length}
          </span>
        ) : null}
      </div>
      {approvalsState === 'unavailable' ? (
        <p
          className="border-t border-line py-3 text-caption text-text-2"
          data-testid="now-approvals-error"
        >
          {t('nav:shell.rail.approvalsError')}
        </p>
      ) : null}
      {flagged || quiet.length > 0 ? (
        <div className="flex flex-col gap-2" data-testid="now-approvals">
          {flagged ? (
            <FlaggedCard
              key={flagged.id}
              approval={flagged}
              gone={flag.gone}
              onSettled={settleFlagged}
            />
          ) : null}
          {quiet.length > 0 ? (
            <ul className="border-t border-line">
              {quiet.map((a) => (
                <ApprovalRow key={a.id} approval={a} />
              ))}
            </ul>
          ) : null}
        </div>
      ) : null}
      {status === 'loading' ? (
        <div className="flex flex-col gap-2 border-t border-line pt-2">
          <Skeleton className="h-10" />
          <Skeleton className="h-10" />
        </div>
      ) : status === 'error' ? (
        <p className="border-t border-line py-3 text-caption text-text-2">
          {t('nav:shell.rail.error')}
        </p>
      ) : rows.length === 0 ? (
        approvals.length > 0 ||
        flagged ||
        approvalsState === 'unavailable' ? null : (
          <p className="border-t border-line py-3 text-caption text-text-2">
            {t('nav:shell.rail.empty.needsYou')}
          </p>
        )
      ) : (
        <ul className="border-t border-line">
          {rows.map((row) => (
            <QueueRow key={row.key} row={row} />
          ))}
        </ul>
      )}
    </section>
  )
}
