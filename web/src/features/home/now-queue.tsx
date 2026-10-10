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
// (features/governance/use-pending-approvals.ts) — the sidebar's count reads the same
// cache — and each opens the queue, where the decision is made with its full context.
import { Link } from '@tanstack/react-router'
import { ArrowRightLeft, CircleAlert, ShieldCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { Skeleton } from '@/components/ui/skeleton'
import {
  railGroups,
  type RailRow,
} from '@/components/layout/session-rail-model'
import {
  useMinuteClock,
  useOfferedHandoffs,
} from '@/components/layout/use-session-rail'
import { ApprovalRequestCell } from '@/features/governance/approval-preview'
import type { ApprovalDTO } from '@/features/governance/types'
import '@/features/governance/i18n'
import type { LiveDTO } from '@/features/sessions/types'
import type { TileState } from './components'
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

function ApprovalRow({ approval }: { approval: ApprovalDTO }) {
  const { t } = useTranslation(['governance'])
  return (
    <li className="border-b border-line last:border-b-0">
      <Link
        to={'/permissions' as never}
        search={{ tab: 'approvals' } as never}
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

export function NowQueue({
  sessions,
  state,
  sessionsReadable = true,
  approvals = [],
  approvalsState = 'ready',
}: {
  /** The live page Now already fetched (its Sessions tile reads the same page). */
  sessions: LiveDTO[] | undefined
  state: TileState
  /** False for a principal who may not read live sessions: the queue then holds what it
   * may read (approvals, offered handoffs) and says nothing about sessions. */
  sessionsReadable?: boolean
  /** The pending approvals this principal may read (none when it may not). */
  approvals?: readonly ApprovalDTO[]
  /** The approvals read's own state: a failed read is said, never "nothing waits". */
  approvalsState?: TileState
}) {
  const { t } = useTranslation(['home', 'nav'])
  const now = useMinuteClock()
  const { readHandoffs, handoffs } = useOfferedHandoffs()
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
  const rows =
    railGroups(
      {
        live: sessionsReadable ? (sessions ?? []) : [],
        handoffs: handoffs.data?.items ?? [],
      },
      now,
    ).find((g) => g.id === 'needsYou')?.rows ?? []
  return (
    <section aria-labelledby="now-needs-heading" data-testid="now-needs">
      <div className="mb-1.5 flex items-baseline gap-2">
        <h2
          id="now-needs-heading"
          className="text-heading font-semibold text-text"
        >
          {t('nav:shell.rail.groups.needsYou')}
        </h2>
        {status === 'ready' ? (
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
      {approvals.length > 0 ? (
        <ul className="border-t border-line" data-testid="now-approvals">
          {approvals.map((a) => (
            <ApprovalRow key={a.id} approval={a} />
          ))}
        </ul>
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
        approvals.length > 0 || approvalsState === 'unavailable' ? null : (
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
