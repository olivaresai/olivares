// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ConsumptionBar — a budget consumption track with a run-rate PROJECTION marker.
// `consumed_pct` is the filled bar; `projected_pct` is a dashed marker (it can sit
// past 100% — a budget on track to overspend). Color crosses to warning at the
// nearest threshold and to danger when over / projected-over, matching the
// budgetStatus render rule. Presentation only — the module computes the numbers.
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { formatPercent } from '@/lib/format'
// The `intel` namespace travels with the modules that translate: these are deep-
// imported across features (`@/features/_intel/notices`), where the barrel — and so
// the registration — is never in the chunk.
import './i18n'

export function ConsumptionBar({
  consumedPct,
  projectedPct,
  over = false,
  height = 8,
  showLabels = true,
  className,
}: {
  consumedPct: number
  projectedPct?: number
  over?: boolean
  height?: number
  showLabels?: boolean
  className?: string
}) {
  const { t } = useTranslation('intel')
  const consumed = Math.max(0, consumedPct)
  const fillWidth = Math.min(100, consumed)
  const projectedOver = (projectedPct ?? 0) >= 100
  const tone =
    over || consumed >= 100
      ? 'danger'
      : projectedOver || consumed >= 80
        ? 'warning'
        : 'accent'
  const fillColor =
    tone === 'danger'
      ? 'bg-danger'
      : tone === 'warning'
        ? 'bg-warning'
        : 'bg-accent-text'
  // Project marker, clamped into the visible track (its true value shows as a label).
  const markerLeft =
    projectedPct !== undefined
      ? Math.min(100, Math.max(0, projectedPct))
      : undefined

  return (
    <div className={cn('flex flex-col gap-1.5', className)}>
      <div
        className="relative w-full overflow-visible rounded-full bg-muted"
        style={{ height }}
        role="progressbar"
        aria-valuenow={Math.round(consumed)}
        aria-valuemin={0}
        aria-valuemax={100}
      >
        <div
          className={cn('h-full rounded-full transition-all', fillColor)}
          style={{ width: `${fillWidth}%` }}
        />
        {markerLeft !== undefined ? (
          <span
            className="absolute top-1/2 z-10 -translate-y-1/2"
            style={{ left: `${markerLeft}%` }}
            title={t('consumption.projected', {
              defaultValue: 'projected {{pct}}',
              pct: formatPercent(projectedPct),
            })}
          >
            <span
              className={cn(
                'block h-3.5 w-0.5 -translate-x-1/2 rounded-full',
                projectedOver ? 'bg-danger' : 'bg-foreground/70',
              )}
              style={{ height: height + 6 }}
            />
          </span>
        ) : null}
      </div>
      {showLabels ? (
        <div className="flex items-center justify-between text-xs">
          <span
            className={cn(
              'font-mono tabular-nums',
              tone === 'danger'
                ? 'text-danger'
                : tone === 'warning'
                  ? 'text-warning'
                  : 'text-foreground',
            )}
          >
            {formatPercent(consumed)}
          </span>
          {projectedPct !== undefined ? (
            <span
              className={cn(
                'text-muted-foreground',
                projectedOver && 'text-danger',
              )}
            >
              {t('consumption.projected', {
                defaultValue: 'projected {{pct}}',
                pct: formatPercent(projectedPct),
              })}
            </span>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}
