// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ONE LEDGER ENTRY, drawn in the side panel beside the list: what happened in a sentence,
// its place in the chain (the previous hash it carries, its own, and the next entry's,
// which carries this one's), the fields the engine signed, and edition-supplied evidence.
//
// The event is the row the person picked; the next entry is read only when the list does
// not already hold it. There is NO payload behind a hash to expand: the fingerprint IS the
// evidence (the minimal-data rule, docs/SECURITY-HARDENING.md). The web renders the engine's record and
// verdict; it never recomputes or repairs the chain (ARCHITECTURE.md, docs/SECURITY-HARDENING.md).
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { KvList, KvRow } from '@/components/ui/kv'
import { Skeleton } from '@/components/ui/skeleton'
import { HashChip } from '@/features/_intel'
import { RelTimeLabel } from '@/features/shared'
import { auditApi } from '@/lib/api/endpoints'
import type { AuditEventDTO } from '@/lib/api/types'
import { useAuth } from '@/lib/auth/context'
import { formatDateTime, formatInt } from '@/lib/format'
import { cn } from '@/lib/utils'
import type { EntryNames } from './ledger'
import { EntryVerification } from './evidence'
import type { LedgerScope } from './types'

function ChainLink({
  label,
  hash,
  current,
}: {
  label: string
  hash: string | null
  current?: boolean
}) {
  return (
    <div
      className={cn(
        'flex min-w-0 flex-1 flex-col gap-0.5 rounded-ctl border px-2.5 py-2',
        current
          ? 'border-line-strong bg-raised text-text'
          : 'border-line text-text-2',
      )}
    >
      <span className="truncate text-caption">{label}</span>
      <span
        className="truncate font-mono text-mono-s text-text"
        title={hash ?? undefined}
      >
        {hash ? hash.slice(0, 6) : '—'}
      </span>
    </div>
  )
}

export function AuditEntryDetail({
  event,
  loaded,
  scope,
  names,
}: {
  event: AuditEventDTO
  /** Every entry the list holds: the next entry is usually among them. */
  loaded: readonly AuditEventDTO[]
  scope: LedgerScope
  names: EntryNames
}) {
  const { t, i18n } = useTranslation('audit')
  const lang = i18n.language
  const { activeTenant } = useAuth()
  const held = loaded.find((e) => e.seq === event.seq + 1)
  const nextRead = useQuery({
    queryKey: ['audit', activeTenant, scope, 'entry-after', event.seq] as const,
    queryFn: async () => {
      const list = scope === 'system' ? auditApi.systemList : auditApi.list
      const page = await list({ from: event.seq + 1, limit: 1 })
      return page.items[0] ?? null
    },
    enabled: !held,
    // An entry that follows never changes; "newest" holds only at the moment it was read.
    staleTime: (q) => (q.state.data ? 60_000 : 0),
  })
  const next = held ?? nextRead.data
  const target = names.target(event)

  return (
    <div className="flex flex-col gap-5 p-4">
      <header className="flex flex-col gap-1">
        <h3 className="text-heading text-text">{names.sentence(event)}</h3>
        <p className="flex flex-wrap items-center gap-x-3 text-caption text-text-2">
          <span title={formatDateTime(event.occurred_at)}>
            <RelTimeLabel ts={event.occurred_at} />
          </span>
          {target ? <span>{target}</span> : null}
          {event.sig ? <span>{t('ledger.signed')}</span> : null}
        </p>
      </header>

      <section className="flex flex-col gap-2">
        <h4 className="text-body font-semibold text-text">
          {t('ledger.entry.chain')}
        </h4>
        <div
          role="group"
          aria-label={t('ledger.entry.chain')}
          className="flex items-stretch gap-1.5"
        >
          <ChainLink
            label={t('ledger.entry.previous', {
              seq: formatInt(event.seq - 1, lang),
            })}
            hash={event.seq > 1 ? event.prev_hash : null}
          />
          <ChainLink
            current
            label={t('ledger.entry.this', { seq: formatInt(event.seq, lang) })}
            hash={event.hash}
          />
          {!held && nextRead.isLoading ? (
            <Skeleton className="h-auto min-w-0 flex-1" />
          ) : (
            <ChainLink
              label={
                next
                  ? t('ledger.entry.next', { seq: formatInt(next.seq, lang) })
                  : nextRead.isError
                    ? t('ledger.entry.nextUnread')
                    : t('ledger.entry.newest')
              }
              hash={next?.hash ?? null}
            />
          )}
        </div>
        <p className="text-caption text-text-2">
          {t('ledger.entry.chainHint')}
        </p>
        {scope === 'tenant' ? <EntryVerification /> : null}
      </section>

      <section className="flex flex-col gap-2">
        <h4 className="text-body font-semibold text-text">
          {t('ledger.entry.signedEntry')}
        </h4>
        <KvList>
          <KvRow label={t('detail.seq')} mono>
            {event.seq}
          </KvRow>
          <KvRow label={t('detail.occurred')} mono>
            {event.occurred_at}
          </KvRow>
          <KvRow label={t('detail.action')} mono>
            {event.action}
          </KvRow>
          <KvRow label={t('detail.actor')} mono align="start">
            <span className="break-all">
              {event.actor || t('detail.actorSystem')}
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
          <KvRow label={t('detail.prevHash')}>
            <HashChip hash={event.prev_hash} label={t('detail.prev')} />
          </KvRow>
          <KvRow label={t('detail.hash')}>
            <HashChip hash={event.hash} label={t('detail.this')} />
          </KvRow>
          {event.sig && (
            <KvRow label={t('detail.sig')}>
              <HashChip hash={event.sig} label={t('detail.checkpoint')} />
            </KvRow>
          )}
        </KvList>
      </section>
    </div>
  )
}
