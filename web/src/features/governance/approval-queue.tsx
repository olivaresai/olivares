// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE QUEUE OF THE RULES PLACE (#2548, concepts-c2 estate/approvals.html). Each request
// is one row: who asks, what will run, the rule that asked and what it reaches. One row
// carries the orange edge: the first request still waiting that THIS person may decide.
// Selecting a row is a tone step and nothing else, so the edge never follows the
// selection (C2-IMPROVE L4): the flag says what needs you, the tone says what you read.
import { useTranslation } from 'react-i18next'
import { formatDateTime, formatShortDateTime } from '@/lib/format'
import { Badge } from '@/components/ui/badge'
import { StatusBadge } from '@/components/data/badges'
import { cn } from '@/lib/utils'
import { RelTimeLabel } from '@/features/shared'
import {
  ApprovalRequestCell,
  ApprovalWhy,
  AskedByName,
  useApprovalSession,
} from './approval-preview'
import './i18n'
import type { ApprovalDTO } from './types'

/** The request that needs this person: the first one still waiting that they may decide,
 * in the engine's order (oldest first). None when they may decide nothing here. */
export function flaggedApprovalId(
  items: readonly ApprovalDTO[],
  mayDecide: (approval: ApprovalDTO) => boolean,
): string | undefined {
  return items.find((a) => a.status === 'pending' && mayDecide(a))?.id
}

export function ApprovalQueue({
  items,
  flaggedId,
  selectedId,
  onSelect,
}: {
  items: readonly ApprovalDTO[]
  flaggedId: string | undefined
  selectedId: string | undefined
  onSelect: (id: string) => void
}) {
  const { t } = useTranslation('governance')
  // The flagged request leads; the others keep the engine's order.
  const ordered = flaggedId
    ? [
        ...items.filter((a) => a.id === flaggedId),
        ...items.filter((a) => a.id !== flaggedId),
      ]
    : items
  return (
    <ul
      aria-label={t('approvals.title')}
      className="flex flex-col overflow-hidden rounded-xl border border-line bg-surface"
    >
      {ordered.map((a) => (
        <li key={a.id} className="border-b border-line last:border-b-0">
          <QueueRow
            approval={a}
            flagged={a.id === flaggedId}
            selected={a.id === selectedId}
            onSelect={() => onSelect(a.id)}
          />
        </li>
      ))}
    </ul>
  )
}

function QueueRow({
  approval,
  flagged,
  selected,
  onSelect,
}: {
  approval: ApprovalDTO
  flagged: boolean
  selected: boolean
  onSelect: () => void
}) {
  const { t } = useTranslation('governance')
  const session = useApprovalSession(approval)
  return (
    <button
      type="button"
      data-approval-row
      data-flagged={flagged ? 'true' : undefined}
      aria-current={selected ? 'true' : undefined}
      onClick={onSelect}
      className={cn(
        'flex w-full min-w-0 flex-col gap-1.5 border-l-[3px] px-3 py-2.5 text-left outline-none transition-colors',
        'focus-visible:ring-2 focus-visible:ring-focus focus-visible:ring-inset',
        flagged ? 'border-l-accent' : 'border-l-transparent',
        selected ? 'bg-active' : 'hover:bg-hover',
      )}
    >
      {flagged ? <span className="sr-only">{t('queue.needsYou')}</span> : null}
      <span className="flex min-w-0 items-baseline gap-2 text-caption text-text-2">
        <span className="min-w-0 truncate font-medium text-foreground">
          <AskedByName approval={approval} />
        </span>
        <span className="ml-auto shrink-0 font-mono tabular-nums">
          {t('approvals.progressOf', {
            approved: approval.approve_count,
            required: approval.required_approvals,
          })}
        </span>
      </span>
      <ApprovalRequestCell approval={approval} className="w-full max-w-full" />
      <ApprovalWhy approval={approval} reaches={session.reaches} />
      <RowState approval={approval} />
    </button>
  )
}

/** Status, escalation and time, on one line; nothing for a plain waiting request with no
 * expiry, so a calm row stays calm. */
function RowState({ approval }: { approval: ApprovalDTO }) {
  const { t } = useTranslation('governance')
  const pending = approval.status === 'pending'
  const expiredAt =
    approval.status === 'expired' ? approval.decided_at : undefined
  if (pending && !approval.escalated && !approval.expires_at) return null
  return (
    <span className="flex min-w-0 flex-wrap items-center gap-1.5 text-caption text-text-2">
      {pending ? null : <StatusBadge status={approval.status} />}
      {approval.reject_count > 0 ? (
        <span className="text-danger">
          {t('approvals.rejectedCount', { count: approval.reject_count })}
        </span>
      ) : null}
      {approval.escalated ? (
        <Badge variant="warning" title={t('approvals.escalatedHint')}>
          {t('approvals.escalatedBadge')}
        </Badge>
      ) : null}
      {/* An expired request says so with the time the engine stored (acceptance: a no-expiry
          policy has no expires_at, but the bounded wait still ends it). */}
      {expiredAt ? (
        <span title={formatDateTime(expiredAt)}>
          {t('approvals.expiredAt', { time: formatShortDateTime(expiredAt) })}
        </span>
      ) : pending && approval.expires_at ? (
        <span>
          {t('approvals.expires')} <RelTimeLabel ts={approval.expires_at} />
        </span>
      ) : null}
    </span>
  )
}
