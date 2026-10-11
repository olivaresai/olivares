// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMemo, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { ConsumptionBar, SectionCard, SeverityBadge } from '@/features/_intel'
import { formatDateTime, formatPercent } from '@/lib/format'
import {
  alertAmount,
  budgetAmount,
  budgetPercent,
  decimalMicro,
  formatEvidenceMoney,
  type PresentedAmount,
} from './evidence'
import type { Alert, BudgetStatus } from './types'

export function Stat({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-caption font-medium tracking-wide text-muted-foreground uppercase">
        {label}
      </span>
      <span className="font-display text-title tabular-nums text-foreground">
        {children}
      </span>
    </div>
  )
}

export function EvidenceAmount({ amount }: { amount: PresentedAmount }) {
  const { t, i18n } = useTranslation('finops')
  const money = formatEvidenceMoney(amount.value, i18n.language)
  const label =
    amount.class === 'lower_bound'
      ? t('evidence.atLeast', { amount: money })
      : amount.class === 'historical'
        ? t('evidence.original', { amount: money ?? t('evidence.unavailable') })
        : amount.class === 'exact' && money
          ? money
          : t('evidence.unknown')
  return (
    <span data-amount-class={amount.class}>
      {label}
      <span className="block text-caption font-normal text-muted-foreground">
        {t(`evidence.${amount.class}`)}
        {amount.cause && amount.cause !== 'legacy_unversioned'
          ? ` · ${t('evidence.cause')}: ${amount.cause}`
          : ''}
      </span>
    </span>
  )
}

export function BudgetCard({
  status,
  actions,
  details,
}: {
  status: BudgetStatus
  actions?: ReactNode
  details?: ReactNode
}) {
  const { t, i18n } = useTranslation('finops')
  const amount = budgetAmount(status)
  const pct = budgetPercent(status)
  const over =
    amount.class !== 'unknown' && status.amount?.over_limit?.result === 'proven'
  const limit = formatEvidenceMoney(
    status.amount?.over_limit?.target_micro_usd,
    i18n.language,
  )
  const remaining =
    amount.class === 'exact'
      ? formatEvidenceMoney(status.amount?.remaining_micro_usd, i18n.language)
      : null
  const reserved = status.amount?.components?.static_reservation
  return (
    <SectionCard className={over ? 'border-warning-line' : undefined}>
      <div className="flex flex-col gap-3">
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <span className="truncate text-body font-medium text-foreground">
                {status.name}
              </span>
              {!status.enabled ? (
                <Badge variant="neutral">{t('budgets.disabled')}</Badge>
              ) : over ? (
                <Badge variant="danger">{t('budgets.over')}</Badge>
              ) : null}
            </div>
            <p className="flex flex-wrap items-center gap-x-1.5 text-caption text-muted-foreground">
              <span className="font-mono">
                {status.dimension === 'global'
                  ? t('budgets.global')
                  : `${status.dimension}: ${status.key}`}
              </span>
              {' · '}
              {t(`periods.${status.period}`, { defaultValue: status.period })}
              {status.action && status.action !== 'alert' ? (
                <Badge variant="outline">
                  {t(`budgets.actions.${status.action}`, {
                    defaultValue: status.action,
                  })}
                </Badge>
              ) : null}
            </p>
          </div>
          <div className="flex items-start gap-2">
            <div className="text-right">
              <div className="font-mono text-body tabular-nums text-foreground">
                <EvidenceAmount amount={amount} />
              </div>
              <div className="text-caption text-muted-foreground">
                / {limit ?? t('evidence.unavailable')}
              </div>
            </div>
            {actions ? (
              <div className="flex shrink-0 items-center gap-1">{actions}</div>
            ) : null}
          </div>
        </div>
        {pct !== null ? (
          <ConsumptionBar consumedPct={pct} over={over} />
        ) : (
          <p className="text-caption text-muted-foreground">
            {t('evidence.barUnavailable')}
          </p>
        )}
        {details}
        {reserved?.state === 'known' &&
        decimalMicro(reserved.value_micro_usd) &&
        BigInt(reserved.value_micro_usd) > 0n ? (
          <p className="text-caption text-muted-foreground">
            {t('budgets.reservedHint', {
              amount: formatEvidenceMoney(
                reserved.value_micro_usd,
                i18n.language,
              ),
            })}
          </p>
        ) : null}
        <div className="flex items-center justify-between text-caption text-muted-foreground">
          <span>
            {t('budgets.remaining')}:{' '}
            <span className="font-mono text-foreground">
              {remaining ?? t('evidence.unavailable')}
            </span>
          </span>
          {status.truncated ? (
            <Badge variant="warning">{t('intel:notices.truncated')}</Badge>
          ) : null}
        </div>
      </div>
    </SectionCard>
  )
}

export function AlertsTable({
  alerts,
  tenant,
}: {
  alerts: Alert[]
  tenant?: string | null
}) {
  const { t, i18n } = useTranslation('finops')
  const columns = useMemo<TableColumn<Alert>[]>(
    () => [
      {
        accessorKey: 'triggered_at',
        header: t('alerts.columns.triggeredAt'),
        cell: ({ row }) => (
          <span className="text-caption text-muted-foreground">
            {formatDateTime(row.original.triggered_at, i18n.language)}
          </span>
        ),
      },
      {
        accessorKey: 'key',
        header: t('alerts.columns.budget'),
        cell: ({ row }) => (
          <span className="font-mono text-caption">
            {row.original.dimension === 'global'
              ? t('budgets.global')
              : `${row.original.dimension}: ${row.original.key}`}
          </span>
        ),
      },
      {
        accessorKey: 'threshold_pct',
        header: t('alerts.columns.threshold'),
        cell: ({ row }) => (
          <span className="font-mono tabular-nums">
            {formatPercent(row.original.threshold_pct)}
          </span>
        ),
      },
      {
        id: 'spend',
        header: t('alerts.columns.spend'),
        cell: ({ row }) => (
          <span className="font-mono text-caption tabular-nums text-muted-foreground">
            <EvidenceAmount amount={alertAmount(row.original, tenant)} />
          </span>
        ),
      },
      {
        accessorKey: 'id',
        header: t('evidence.reference'),
        cell: ({ row }) => (
          <div className="max-w-64 break-all font-mono text-caption">
            <span>{row.original.id}</span>
            {row.original.amount_evidence?.evidence_hash ? (
              <details>
                <summary>{t('evidence.digest')}</summary>
                {row.original.amount_evidence.evidence_hash}
              </details>
            ) : null}
          </div>
        ),
      },
      {
        accessorKey: 'severity',
        header: t('alerts.columns.severity'),
        cell: ({ row }) => <SeverityBadge severity={row.original.severity} />,
      },
    ],
    [t, i18n.language, tenant],
  )
  return (
    <DataTable<Alert>
      columns={columns}
      data={alerts}
      empty={
        <EmptyState
          title={t('empty.alerts.title')}
          description={t('empty.alerts.description')}
        />
      }
    />
  )
}
