// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// LedgerPanel renders the semantic audit events correlated to the recording
// window. It is deliberately distinct from the frame rail: frames prove the
// request chain, while these rows explain the matching evidence-ledger actions.
import { ScrollText } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { EmptyState } from '@/components/ui/empty-state'
import { CaveatNotice } from '@/features/_intel'
import type { LedgerEventDTO } from '@/features/recordings/types'
import { RelTimeLabel } from '@/features/shared'
import './i18n'

export function LedgerPanel({
  events,
  truncated,
}: {
  events: LedgerEventDTO[]
  truncated: boolean
}) {
  const { t } = useTranslation('session-viewer')

  return (
    <section
      className="flex flex-col gap-3 rounded-lg border border-border bg-background p-4"
      aria-labelledby="session-ledger-title"
    >
      <div>
        <h2
          id="session-ledger-title"
          className="flex items-center gap-1.5 text-sm font-semibold text-foreground"
        >
          <ScrollText className="size-4 text-muted-foreground" aria-hidden />
          {t('ledger.title')}
        </h2>
        <p className="text-xs text-muted-foreground">{t('ledger.subtitle')}</p>
      </div>

      {events.length === 0 ? (
        <EmptyState
          icon={<ScrollText />}
          title={t('ledger.empty')}
          description=""
        />
      ) : (
        <ol className="flex flex-col" data-testid="session-ledger">
          {events.map((event) => {
            const target = event.target_id
              ? `${event.target_kind ? `${event.target_kind}:` : ''}${event.target_id}`
              : '—'
            return (
              <li
                key={event.seq}
                className="grid gap-1 border-b border-border py-2 last:border-0 sm:grid-cols-[auto_minmax(0,1fr)_auto]"
              >
                <span className="font-mono text-xs tabular-nums text-muted-foreground">
                  {t('ledger.seq', { seq: event.seq })}
                </span>
                <div className="min-w-0">
                  <p className="break-all font-mono text-xs font-medium text-foreground">
                    {event.action}
                  </p>
                  <p className="break-all font-mono text-xs text-muted-foreground">
                    {event.actor} {'→'} {target}
                  </p>
                </div>
                <RelTimeLabel
                  ts={event.occurred_at}
                  className="text-xs text-muted-foreground"
                />
              </li>
            )
          })}
        </ol>
      )}

      {truncated && (
        <CaveatNotice tone="warning">{t('ledger.truncated')}</CaveatNotice>
      )}
    </section>
  )
}
