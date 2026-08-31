// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Chargeback depth components — cost centers, rate catalog, model
// comparison, chargeback statements, enhanced forecast/budget views.

import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { MetricStat, SeamBadge } from '@/features/_intel'
import { formatInt, formatMicroUsd } from '@/lib/format'
import type {
  ChargebackStatement,
  ComparisonWithProjection,
  CostCenter,
  CostCenterMapping,
  DimensionForecast,
  ModelRate,
  StatementLine,
} from './types'

// --- Cost Center Components -------------------------------------------------

export function CostCenterList({
  items,
  onSelect,
}: {
  items: CostCenter[]
  onSelect?: (cc: CostCenter) => void
}) {
  const { t } = useTranslation()
  if (items.length === 0)
    return (
      <p className="text-sm text-muted-foreground">
        {t('No cost centers configured')}
      </p>
    )
  return (
    <div className="space-y-2">
      {items.map((cc) => (
        <button
          key={cc.id}
          className="flex w-full items-center justify-between rounded-md border p-3 text-left hover:bg-muted/50"
          onClick={() => onSelect?.(cc)}
        >
          <div>
            <span className="font-mono text-sm font-medium">{cc.code}</span>
            <span className="ml-2 text-sm text-muted-foreground">
              {cc.name}
            </span>
          </div>
          <Badge variant={cc.status === 'active' ? 'success' : 'neutral'}>
            {cc.status}
          </Badge>
        </button>
      ))}
    </div>
  )
}

export function CostCenterMappingList({
  mappings,
  onDelete,
}: {
  mappings: CostCenterMapping[]
  onDelete?: (id: string) => void
}) {
  if (mappings.length === 0)
    return <p className="text-sm text-muted-foreground">No mapping rules</p>
  return (
    <div className="space-y-1">
      {mappings.map((m) => (
        <div
          key={m.id}
          className="flex items-center justify-between rounded border px-3 py-2 text-sm"
        >
          <span>
            <span className="font-medium">{m.source_dimension}</span>
            {' = '}
            <code className="rounded bg-muted px-1">{m.source_key}</code>
            <span className="ml-2 text-muted-foreground">
              (priority {m.priority})
            </span>
          </span>
          {onDelete && (
            <button
              className="text-destructive hover:underline"
              onClick={() => onDelete(m.id)}
            >
              Remove
            </button>
          )}
        </div>
      ))}
    </div>
  )
}

// --- Rate Catalog Components ------------------------------------------------

export function RateCatalogTable({ rates }: { rates: ModelRate[] }) {
  if (rates.length === 0)
    return <p className="text-sm text-muted-foreground">No rates configured</p>
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b text-left text-muted-foreground">
            <th className="p-2">Provider</th>
            <th className="p-2">Model</th>
            <th className="p-2 text-right">Input $/MTok</th>
            <th className="p-2 text-right">Output $/MTok</th>
            <th className="p-2 text-right">Cache Read</th>
            <th className="p-2 text-right">Cache Create</th>
            <th className="p-2">Effective</th>
          </tr>
        </thead>
        <tbody>
          {rates.map((r) => (
            <tr key={r.id} className="border-b">
              <td className="p-2">{r.provider}</td>
              <td className="p-2 font-mono text-xs">{r.model}</td>
              <td className="p-2 text-right">
                {formatMicroUsd(r.input_rate_micro_usd)}
              </td>
              <td className="p-2 text-right">
                {formatMicroUsd(r.output_rate_micro_usd)}
              </td>
              <td className="p-2 text-right">
                {formatMicroUsd(r.cache_read_rate_micro_usd)}
              </td>
              <td className="p-2 text-right">
                {formatMicroUsd(r.cache_creation_rate_micro_usd)}
              </td>
              <td className="p-2 text-xs text-muted-foreground">
                {r.effective_from?.slice(0, 10)}
                {r.effective_until
                  ? ` → ${r.effective_until.slice(0, 10)}`
                  : ' → now'}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// --- Model Comparison Components --------------------------------------------

export function ModelComparisonCard({
  data,
}: {
  data: ComparisonWithProjection
}) {
  const retro = data.retrospective
  return (
    <div className="space-y-4">
      <div className="rounded-lg border p-4">
        <h4 className="mb-2 text-sm font-medium">Retrospective Re-pricing</h4>
        <div className="mb-3 flex gap-4">
          <MetricStat
            label="Source actual"
            value={formatMicroUsd(retro.source.actual_micro_usd)}
          />
          <MetricStat label="Samples" value={String(retro.total_samples)} />
        </div>
        <div className="space-y-2">
          {retro.targets.map((t) => (
            <div
              key={t.model}
              className="flex items-center justify-between rounded border p-2"
            >
              <span className="font-mono text-xs">{t.model}</span>
              <div className="flex items-center gap-3">
                <span className="text-sm">
                  {formatMicroUsd(t.rate_micro_usd)}
                </span>
                <Badge variant={t.savings_pct > 0 ? 'success' : 'neutral'}>
                  {t.savings_pct > 0
                    ? `−${t.savings_pct}%`
                    : `+${Math.abs(t.savings_pct)}%`}
                </Badge>
              </div>
            </div>
          ))}
        </div>
      </div>
      {data.projections && data.projections.length > 0 && (
        <div className="rounded-lg border p-4">
          <h4 className="mb-2 text-sm font-medium">Prospective Projection</h4>
          <p className="mb-2 text-xs text-muted-foreground">
            Forecast period: {data.forecast_period}
          </p>
          {data.projections.map((p) => (
            <div
              key={p.model}
              className="flex items-center justify-between rounded border p-2"
            >
              <span className="font-mono text-xs">{p.model}</span>
              <div className="flex items-center gap-3">
                <span className="text-sm">
                  {formatMicroUsd(p.projected_micro_usd)}
                </span>
                <span className="text-xs text-muted-foreground">
                  [{formatMicroUsd(p.confidence_low_micro_usd)} –{' '}
                  {formatMicroUsd(p.confidence_high_micro_usd)}]
                </span>
                <Badge variant={p.savings_pct > 0 ? 'success' : 'neutral'}>
                  {p.savings_pct > 0
                    ? `−${p.savings_pct}%`
                    : `+${Math.abs(p.savings_pct)}%`}
                </Badge>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// --- Chargeback Statement Components ----------------------------------------

export function StatementList({
  statements,
  onSelect,
}: {
  statements: ChargebackStatement[]
  onSelect?: (s: ChargebackStatement) => void
}) {
  const { t } = useTranslation('finops')
  if (statements.length === 0)
    return (
      <p className="text-sm text-muted-foreground">{t('statements.empty')}</p>
    )
  return (
    <div className="space-y-2">
      {statements.map((s) => (
        <button
          key={s.id}
          className="flex w-full items-center justify-between rounded-md border p-3 text-left hover:bg-muted/50"
          onClick={() => onSelect?.(s)}
        >
          <div>
            <span className="font-mono text-sm font-medium">
              {s.cost_center_code}
            </span>
            <span className="ml-2 text-sm">{s.cost_center_name}</span>
            <span className="ml-3 text-xs text-muted-foreground">
              {s.period_start?.slice(0, 10)} → {s.period_end?.slice(0, 10)}
            </span>
          </div>
          <div className="flex items-center gap-2">
            <span className="text-sm font-medium">
              {formatMicroUsd(s.total_micro_usd)}
            </span>
            {s.delta_pct !== 0 && (
              <Badge variant={s.delta_pct > 0 ? 'danger' : 'success'}>
                {s.delta_pct > 0 ? '+' : ''}
                {(s.delta_pct / 100).toFixed(1)}%
              </Badge>
            )}
            <Badge variant={s.status === 'final' ? 'success' : 'neutral'}>
              {s.status}
            </Badge>
          </div>
        </button>
      ))}
    </div>
  )
}

export function StatementDetail({
  statement,
  onExport,
}: {
  statement: ChargebackStatement
  onExport?: () => void
}) {
  const { t } = useTranslation('finops')
  const lines = statement.lines ?? []
  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h3 className="text-lg font-medium">
            {statement.cost_center_code} — {statement.cost_center_name}
          </h3>
          <p className="text-sm text-muted-foreground">
            {statement.period} | {statement.period_start?.slice(0, 10)} →{' '}
            {statement.period_end?.slice(0, 10)}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <MetricStat
            label={t('statements.total')}
            value={formatMicroUsd(statement.total_micro_usd)}
          />
          {onExport && (
            <button
              className="rounded border px-3 py-1 text-sm hover:bg-muted"
              onClick={onExport}
            >
              {t('statements.exportCsv')}
            </button>
          )}
        </div>
      </div>
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b text-left text-muted-foreground">
              <th className="p-2">{t('statements.model')}</th>
              <th className="p-2">{t('statements.provider')}</th>
              <th className="p-2">{t('statements.agent')}</th>
              <th className="p-2 text-right">{t('statements.inputTokens')}</th>
              <th className="p-2 text-right">{t('statements.outputTokens')}</th>
              <th className="p-2 text-right">{t('statements.cost')}</th>
              <th className="p-2 text-right">{t('statements.samples')}</th>
            </tr>
          </thead>
          <tbody>
            {lines.map((l: StatementLine) => (
              <tr key={l.id} className="border-b">
                <td className="p-2 font-mono text-xs">{l.model_ref}</td>
                <td className="p-2">{l.provider_ref}</td>
                <td className="p-2 text-xs text-muted-foreground">
                  {l.agent_ref || '—'}
                </td>
                <td className="p-2 text-right">{formatInt(l.input_tokens)}</td>
                <td className="p-2 text-right">{formatInt(l.output_tokens)}</td>
                <td className="p-2 text-right font-medium">
                  {formatMicroUsd(l.cost_micro_usd)}
                </td>
                <td className="p-2 text-right">{l.sample_count}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}

// --- Enhanced Forecast Components -------------------------------------------

export function EWAForecastCard({
  ewaRate,
  ewaProjected,
  ewaLow,
  ewaHigh,
  alpha,
}: {
  ewaRate: number
  ewaProjected: number
  ewaLow: number
  ewaHigh: number
  alpha: number
}) {
  return (
    <div className="rounded-lg border p-4">
      <div className="mb-1 flex items-center gap-2">
        <h4 className="text-sm font-medium">EWA Forecast</h4>
        <SeamBadge label={`α=${alpha}`} />
      </div>
      <div className="flex gap-4">
        <MetricStat label="Daily rate" value={formatMicroUsd(ewaRate)} />
        <MetricStat label="Projected" value={formatMicroUsd(ewaProjected)} />
        <MetricStat
          label="95% CI"
          value={`${formatMicroUsd(ewaLow)} – ${formatMicroUsd(ewaHigh)}`}
        />
      </div>
    </div>
  )
}

export function DimensionForecastTable({
  forecasts,
}: {
  forecasts: DimensionForecast[]
}) {
  if (forecasts.length === 0) return null
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b text-left text-muted-foreground">
            <th className="p-2">Key</th>
            <th className="p-2 text-right">Current Spend</th>
            <th className="p-2 text-right">EWA Rate/Day</th>
            <th className="p-2 text-right">Projected</th>
            <th className="p-2 text-right">95% CI Low</th>
            <th className="p-2 text-right">95% CI High</th>
          </tr>
        </thead>
        <tbody>
          {forecasts.map((f) => (
            <tr key={f.key} className="border-b">
              <td className="p-2 font-mono text-xs">{f.key || '(global)'}</td>
              <td className="p-2 text-right">
                {formatMicroUsd(f.spend_micro_usd)}
              </td>
              <td className="p-2 text-right">
                {formatMicroUsd(f.ewa_daily_rate_micro_usd)}
              </td>
              <td className="p-2 text-right font-medium">
                {formatMicroUsd(f.ewa_projected_micro_usd)}
              </td>
              <td className="p-2 text-right text-muted-foreground">
                {formatMicroUsd(f.ewa_confidence_low_micro_usd)}
              </td>
              <td className="p-2 text-right text-muted-foreground">
                {formatMicroUsd(f.ewa_confidence_high_micro_usd)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// --- Enhanced Budget Status -------------------------------------------------

export function BudgetExhaustionBadge({
  daysRemaining,
  confidence,
}: {
  daysRemaining: number
  confidence?: string
}) {
  if (daysRemaining < 0) return null
  if (daysRemaining === 0) {
    return <Badge variant="danger">Exhausted</Badge>
  }
  const variant =
    daysRemaining <= 7 ? 'danger' : daysRemaining <= 30 ? 'warning' : 'success'
  return (
    <Badge variant={variant}>
      {daysRemaining}d remaining
      {confidence && ` (${confidence})`}
    </Badge>
  )
}
