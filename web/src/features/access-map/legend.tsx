// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'

/**
 * Legend — makes the edge encoding unambiguous "at a glance" (the screenshot must
 * sell itself): R vs RW by color, approximate by dash, and — when the diff overlay
 * is on — the unexpected/pending/unused findings. Lives in a canvas panel.
 */
function Swatch({
  color,
  dashed = false,
  thick = false,
  label,
}: {
  color: string
  dashed?: boolean
  thick?: boolean
  label: string
}) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <svg
        width="22"
        height="8"
        viewBox="0 0 22 8"
        aria-hidden
        className="shrink-0"
      >
        <line
          x1="1"
          y1="4"
          x2="21"
          y2="4"
          stroke={color}
          strokeWidth={thick ? 2.5 : 1.5}
          strokeDasharray={dashed ? '4 3' : undefined}
          strokeLinecap="round"
        />
      </svg>
      <span className="text-[11px] text-muted-foreground">{label}</span>
    </span>
  )
}

export function AccessLegend({
  overlay,
  className,
}: {
  overlay: boolean
  className?: string
}) {
  const { t } = useTranslation('accessMap')
  return (
    <div
      className={cn(
        'pointer-events-none flex flex-col gap-1.5 rounded-md border border-border bg-surface/90 px-2.5 py-2 shadow-sm backdrop-blur',
        className,
      )}
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <Swatch color="var(--color-info)" label={t('legend.read')} />
        <Swatch color="var(--color-accent-text)" thick label={t('legend.writeRw')} />
        <Swatch
          color="var(--color-muted-foreground)"
          label={t('legend.unknown')}
        />
        <Swatch
          color="var(--color-muted-foreground)"
          dashed
          label={t('legend.approximate')}
        />
      </div>
      {overlay && (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-border pt-1.5">
          <Swatch
            color="var(--color-danger)"
            thick
            label={t('legend.unexpected')}
          />
          <Swatch
            color="var(--color-warning)"
            thick
            dashed
            label={t('legend.pending')}
          />
          <Swatch
            color="var(--color-warning)"
            dashed
            label={t('legend.unused')}
          />
        </div>
      )}
    </div>
  )
}
