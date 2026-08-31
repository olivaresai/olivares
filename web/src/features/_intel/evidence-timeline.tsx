// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// EvidenceTimeline reconstructs an incident's sequence from the append-only,
// hash-chained ledger (docs/SECURITY-HARDENING.md). It PRESENTS tamper-evident evidence — it never
// alters it: each event carries seq · hash · prev_hash so a reader can re-verify the
// link themselves, plus `signed` / `linked` (in the chain of custody) badges. There
// is never a payload behind an event — only the fingerprint (HashChip).
import { Link2, PenLine } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { cn } from '@/lib/utils'
import { formatDateTime, formatRelativeTime, humanize } from '@/lib/format'
import { HashChip } from './hash-chip'
// The `intel` namespace travels with the modules that translate: these are deep-
// imported across features (`@/features/_intel/notices`), where the barrel — and so
// the registration — is never in the chunk.
import './i18n'

/** A single ledger event on the forensic timeline (security contract §5).*/
export interface TimelineEvent {
  seq: number
  occurred_at: string
  actor: string
  actor_kind?: string
  action: string
  target_kind?: string
  target_id?: string
  hash: string
  prev_hash?: string
  signed?: boolean
  linked?: boolean
}

export function EvidenceTimeline({
  events,
  className,
}: {
  events: TimelineEvent[]
  className?: string
}) {
  const { t } = useTranslation('intel')
  if (events.length === 0) {
    return <EmptyState title={t('timeline.empty')} />
  }
  return (
    <ol className={cn('flex flex-col', className)}>
      {events.map((ev, i) => {
        const last = i === events.length - 1
        return (
          <li
            key={`${ev.seq}-${ev.hash}`}
            className="relative flex gap-3 pb-4 last:pb-0"
          >
            {/* rail */}
            <div className="flex flex-col items-center">
              <span className="mt-1 size-2.5 shrink-0 rounded-full border-2 border-accent-text bg-background" />
              {!last ? <span className="w-px flex-1 bg-border" /> : null}
            </div>
            {/* content */}
            <div className="min-w-0 flex-1 pb-1">
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <span className="text-sm font-medium text-foreground">
                  {humanize(ev.action)}
                </span>
                <span className="font-mono text-xs text-muted-foreground">
                  {t('timeline.seq')} {ev.seq}
                </span>
                {ev.signed ? (
                  <Badge variant="success" className="gap-1">
                    <PenLine className="size-3" />
                    {t('badges.signed')}
                  </Badge>
                ) : null}
                {ev.linked ? (
                  <Badge variant="accent" className="gap-1">
                    <Link2 className="size-3" />
                    {t('badges.linked')}
                  </Badge>
                ) : null}
              </div>
              <p className="mt-0.5 text-xs text-muted-foreground">
                <span className="font-mono">{ev.actor}</span>
                {ev.actor_kind ? ` · ${ev.actor_kind}` : ''}
                {ev.target_id ? (
                  <>
                    {' → '}
                    <span className="font-mono">
                      {ev.target_kind ? `${ev.target_kind}:` : ''}
                      {ev.target_id}
                    </span>
                  </>
                ) : null}
              </p>
              <div className="mt-1.5 flex flex-wrap items-center gap-2">
                <time
                  dateTime={ev.occurred_at}
                  title={formatDateTime(ev.occurred_at)}
                  className="text-xs text-muted-foreground"
                >
                  {formatRelativeTime(ev.occurred_at)}
                </time>
                <HashChip hash={ev.hash} label="hash" />
              </div>
            </div>
          </li>
        )
      })}
    </ol>
  )
}
