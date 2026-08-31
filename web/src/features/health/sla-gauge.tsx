// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { ppmToPercent } from '@/features/shared'
import { formatPercent } from '@/lib/format'

/**
 * SlaGauge — a PURE SVG radial arc for an SLA report. It resolves every color from
 * a design token (currentColor / var(--color-*)) so it is theme-aware (light/dark)
 * without JS. It renders `uptime_percent` (NEVER recomputed from seconds — the engine
 * reconstructs it from the ledger; docs UI-CONTRACT-HEALTH §8) against the target
 * (`ppmToPercent(sla_target_ppm)`); a breaching report is drawn in danger. This is a
 * 270° gauge: a track arc + a value arc clamped to the same sweep.
 *
 * NOTE: we deliberately do NOT use components/charts here (it has an unrelated type
 * error) — this is a single self-contained SVG.
 */

const RADIUS = 54
const STROKE = 12
const SIZE = (RADIUS + STROKE) * 2
const CENTER = SIZE / 2
// A 270° gauge, opening at the bottom (gap centered on 90°/down).
const START_ANGLE = 135
const SWEEP = 270
const CIRC = 2 * Math.PI * RADIUS
const ARC_LEN = (SWEEP / 360) * CIRC

function polar(cx: number, cy: number, r: number, angleDeg: number) {
  const a = ((angleDeg - 90) * Math.PI) / 180
  return { x: cx + r * Math.cos(a), y: cy + r * Math.sin(a) }
}

/** Describe a circular arc path from startAngle sweeping `sweep` degrees clockwise. */
function arcPath(r: number, startAngle: number, sweep: number): string {
  const end = startAngle + sweep
  const start = polar(CENTER, CENTER, r, startAngle)
  const finish = polar(CENTER, CENTER, r, end)
  const largeArc = sweep > 180 ? 1 : 0
  return `M ${start.x} ${start.y} A ${r} ${r} 0 ${largeArc} 1 ${finish.x} ${finish.y}`
}

export function SlaGauge({
  uptimePercent,
  targetPpm,
  breaching,
}: {
  /** The engine's uptime_percent (already computed). */
  uptimePercent: number
  /** sla_target_ppm — 0 means no target declared. */
  targetPpm: number
  breaching: boolean
}) {
  const { t } = useTranslation('health')
  const hasTarget = targetPpm > 0
  // Clamp the visual fill to [0, 100]; the textual figure stays exact.
  const pct = Math.max(0, Math.min(100, uptimePercent))
  const valueColor = breaching ? 'var(--color-danger)' : 'var(--color-success)'

  // The value arc as a dash on the full arc length.
  const valueLen = (pct / 100) * ARC_LEN
  // Target tick position (only when a target is declared).
  const targetPct = hasTarget
    ? Math.max(0, Math.min(100, targetPpm / 10_000))
    : 0
  const targetAngle = START_ANGLE + (targetPct / 100) * SWEEP

  const tickInner = polar(CENTER, CENTER, RADIUS - STROKE / 2 - 3, targetAngle)
  const tickOuter = polar(CENTER, CENTER, RADIUS + STROKE / 2 + 3, targetAngle)

  return (
    <div className="flex flex-col items-center">
      <svg
        viewBox={`0 0 ${SIZE} ${SIZE}`}
        className="h-40 w-40"
        role="img"
        aria-label={`${t('sla.uptime')} ${formatPercent(uptimePercent, { digits: 2 })}`}
      >
        {/* Track */}
        <path
          d={arcPath(RADIUS, START_ANGLE, SWEEP)}
          fill="none"
          stroke="var(--color-muted)"
          strokeWidth={STROKE}
          strokeLinecap="round"
        />
        {/* Value */}
        <path
          d={arcPath(RADIUS, START_ANGLE, SWEEP)}
          fill="none"
          stroke={valueColor}
          strokeWidth={STROKE}
          strokeLinecap="round"
          strokeDasharray={`${valueLen} ${CIRC}`}
          className="transition-[stroke-dasharray] duration-500 ease-out"
        />
        {/* Target tick */}
        {hasTarget && (
          <line
            x1={tickInner.x}
            y1={tickInner.y}
            x2={tickOuter.x}
            y2={tickOuter.y}
            stroke="var(--color-foreground)"
            strokeWidth={2}
            strokeLinecap="round"
            opacity={0.7}
          />
        )}
        {/* Center figure */}
        <text
          x={CENTER}
          y={CENTER - 4}
          textAnchor="middle"
          className="fill-foreground font-display text-[26px] font-semibold tabular-nums"
        >
          {formatPercent(uptimePercent, { digits: 2 })}
        </text>
        <text
          x={CENTER}
          y={CENTER + 18}
          textAnchor="middle"
          className="fill-muted-foreground text-[11px]"
        >
          {t('sla.uptime')}
        </text>
      </svg>
      <div className="mt-1 text-xs text-muted-foreground">
        {hasTarget ? (
          <span>
            {t('sla.target')}:{' '}
            <span className="font-mono tabular-nums text-foreground">
              {ppmToPercent(targetPpm)}
            </span>
          </span>
        ) : (
          <span>{t('sla.noTarget')}</span>
        )}
      </div>
    </div>
  )
}
