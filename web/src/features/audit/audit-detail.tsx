// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// AuditEventSheet — one ledger event's full evidence facts: who/what/when, the
// chain links (prev_hash → hash) as copyable fingerprints, and the signed-checkpoint
// signature when present. The event is passed in from the row the operator clicked —
// there is NO per-event GET, and crucially NO payload behind the hash to expand: the
// fingerprint IS the evidence (the minimal-data rule, docs/SECURITY-HARDENING.md). The web renders the
// engine's record; it never recomputes or repairs the chain (ARCHITECTURE.md, docs/SECURITY-HARDENING.md).
import { FileCheck2, ScrollText } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { KvList, KvRow } from '@/components/ui/kv'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { HashChip } from '@/features/_intel'
import { RelTimeLabel } from '@/features/shared'
import { useMemberNames } from '@/features/shared'
import { eventLabel } from './event-label'
import { readableAction } from '@/components/layout/bell-events'
import type { AuditEventDTO } from '@/lib/api/types'
import { formatDateTime } from '@/lib/format'
import { useAuth } from '@/lib/auth/context'

export function AuditEventSheet({
  event,
  open,
  onOpenChange,
}: {
  event: AuditEventDTO | null
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation('audit')
  const members = useMemberNames()
  const { can } = useAuth()

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle className="flex items-center gap-2">
            <ScrollText className="size-4 text-accent-text" aria-hidden />
            <span className="text-heading">
              {event ? eventLabel(event.action, t) : t('detail.title')}
            </span>
          </SheetTitle>
          {event && (
            <SheetDescription className="flex flex-wrap items-center gap-1.5">
              <span>
                {(event.actor_kind === 'user' && can('user:read')
                  ? members.nameOf(event.actor)
                  : null) ??
                  t(`actorKind.${event.actor_kind}`, {
                    defaultValue: readableAction(event.actor_kind || 'system'),
                  })}
              </span>
              <RelTimeLabel ts={event.occurred_at} />
              {event.sig ? (
                <Badge variant="success" className="gap-1">
                  <FileCheck2 className="size-3" aria-hidden />
                  {t('detail.signed')}
                </Badge>
              ) : null}
            </SheetDescription>
          )}
        </SheetHeader>

        {event && (
          <>
            <details className="min-w-0">
              <summary className="cursor-pointer text-caption text-muted-foreground">
                {t('detail.details')}
              </summary>
              <div className="mt-3">
                <KvList>
                  <KvRow label={t('detail.seq')} mono>
                    {event.seq}
                  </KvRow>
                  <KvRow label={t('detail.occurred')}>
                    <span
                      className="inline-flex items-center gap-2"
                      title={formatDateTime(event.occurred_at)}
                    >
                      <RelTimeLabel ts={event.occurred_at} />
                    </span>
                  </KvRow>
                  <KvRow label={t('detail.action')} mono>
                    {event.action}
                  </KvRow>
                  <KvRow label={t('detail.actor')} mono align="start">
                    <span className="inline-flex flex-wrap items-center gap-1.5">
                      {event.actor || t('detail.actorSystem')}
                      <Badge variant="neutral">
                        {t(`actorKind.${event.actor_kind}`, {
                          defaultValue: event.actor_kind || '—',
                        })}
                      </Badge>
                    </span>
                  </KvRow>
                  <KvRow label={t('detail.target')} mono align="start">
                    {event.target_id || event.target_kind ? (
                      <span className="break-all">
                        {event.target_kind ? `${event.target_kind}:` : ''}
                        {event.target_id || '—'}
                      </span>
                    ) : (
                      <span className="text-muted-foreground">—</span>
                    )}
                  </KvRow>
                  <KvRow label={t('detail.id')} mono align="start">
                    <span className="break-all text-caption">{event.id}</span>
                  </KvRow>
                </KvList>

                {/* Chain links — the tamper-evidence fingerprints. No payload behind a
                hash; copying yields the full value (docs/SECURITY-HARDENING.md). */}
                <section className="mt-4 flex flex-col gap-2">
                  <h3 className="text-body font-semibold text-foreground">
                    {t('detail.chainTitle')}
                  </h3>
                  <KvList>
                    <KvRow label={t('detail.prevHash')}>
                      <HashChip
                        hash={event.prev_hash}
                        label={t('detail.prev')}
                      />
                    </KvRow>
                    <KvRow label={t('detail.hash')}>
                      <HashChip hash={event.hash} label={t('detail.this')} />
                    </KvRow>
                    {event.sig && (
                      <KvRow label={t('detail.sig')}>
                        <HashChip
                          hash={event.sig}
                          label={t('detail.checkpoint')}
                        />
                      </KvRow>
                    )}
                  </KvList>
                  <p className="text-caption text-muted-foreground">
                    {t('detail.chainHint')}
                  </p>
                </section>
              </div>
            </details>
          </>
        )}
      </SheetContent>
    </Sheet>
  )
}
