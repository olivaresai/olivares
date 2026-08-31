// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { BadgeVariant } from '@/components/ui/badge'
import { Badge } from '@/components/ui/badge'
import { useTranslation } from 'react-i18next'
import type { HealthState } from './types'

/**
 * HealthStateBadge — THE single source of the health-state color mapping (docs
 * UI-CONTRACT-HEALTH §8), applied identically to status rows, live stream frames and
 * the dependency-map nodes: healthy=success(green), degraded=warning(amber),
 * down=danger(red), observed=info(blue), unknown=neutral(gray). `unknown` is
 * deliberately NEUTRAL, never green — it means NO liveness signal at all. `observed`
 * is the honest intermediate (info, never green): the subject was seen alive by an
 * edge but has no declared check, so its health is NOT measured — distinct from both
 * "healthy" (measured) and "unknown" (no signal). Dependency-map annotation only.
 */

/** Health-state → semantic Badge variant. Exported so the dependency-map nodes can
 * resolve the SAME color through a token, keeping every view consistent. */
export const HEALTH_VARIANT: Record<string, BadgeVariant> = {
  healthy: 'success',
  degraded: 'warning',
  down: 'danger',
  observed: 'info',
  unknown: 'neutral',
}

/** Health-state → the design TOKEN var (for the SVG gauge / React Flow node stroke,
 * which can't use a Tailwind class). Observed resolves to info; unknown to the muted
 * neutral. */
export const HEALTH_TOKEN: Record<string, string> = {
  healthy: 'var(--color-success)',
  degraded: 'var(--color-warning)',
  down: 'var(--color-danger)',
  observed: 'var(--color-info)',
  unknown: 'var(--color-muted-foreground)',
}

export function healthVariant(state: HealthState): BadgeVariant {
  return HEALTH_VARIANT[String(state)] ?? 'neutral'
}

export function healthToken(state: HealthState): string {
  return HEALTH_TOKEN[String(state)] ?? HEALTH_TOKEN.unknown!
}

export function HealthStateBadge({
  state,
  className,
}: {
  state: HealthState
  className?: string
}) {
  const { t } = useTranslation('health')
  const key = String(state)
  const hint =
    key === 'unknown' || key === 'observed' ? t(`stateHint.${key}`) : undefined
  return (
    <Badge variant={healthVariant(state)} className={className} title={hint}>
      {t(`state.${key}`, { defaultValue: key })}
    </Badge>
  )
}
