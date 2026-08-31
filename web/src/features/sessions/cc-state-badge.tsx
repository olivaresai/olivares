// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import type { CcState } from './types'
import './i18n'

/**
 * CcStateBadge — the session control-plane state chip. NOT the generic StatusBadge:
 * the four `cc_state` values carry product meaning the generic map lacks.
 *  - `active`          success + a live pulse (the one sanctioned looping animation)
 *  - `idle`           neutral (quiet but fine)
 *  - `ended`          muted (closed, low emphasis)
 *  - `silent_evasion` DANGER, framed as "possible evasion": a session gone silent
 *                     within its expected cadence is a SIGNAL the operator should see
 *                     (docs/SECURITY-HARDENING.md) — surfaced firmly, never as a UI error.
 *
 * `cc_state` is DERIVED by the engine at read time; this only renders it (ARCHITECTURE.md).
 */
const STYLE: Record<string, { box: string; dot: string }> = {
  active: {
    box: 'border-success-line bg-success-soft text-success',
    dot: 'bg-success animate-pulse-live',
  },
  idle: {
    box: 'border-border bg-muted text-muted-foreground',
    dot: 'bg-graphite-400',
  },
  ended: {
    box: 'border-border bg-transparent text-muted-foreground',
    dot: 'bg-graphite-400',
  },
  silent_evasion: {
    box: 'border-danger-line bg-danger-soft text-danger',
    dot: 'bg-danger',
  },
}

export function CcStateBadge({
  state,
  className,
}: {
  state: CcState
  className?: string
}) {
  const { t } = useTranslation('sessions')
  const key = String(state)
  const style = STYLE[key] ?? STYLE.idle
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 rounded-sm border px-1.5 py-0.5 text-xs font-medium whitespace-nowrap',
        style.box,
        className,
      )}
      title={t(`stateHint.${key}`, { defaultValue: '' }) || undefined}
    >
      <span className={cn('size-1.5 rounded-full', style.dot)} aria-hidden />
      {t(`state.${key}`, { defaultValue: key })}
    </span>
  )
}
