// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import type { RunState } from './types'
import './i18n'

/**
 * RunStateBadge — the operated-session lifecycle chip. Unlike the observe overlay's
 * cc_state (a cooperative-stream projection), this is the runtime's OWNED state machine
 * (it owns the process): pending → running ⇄ idle → stopped/failed → cleaned. `running`
 * carries the one sanctioned live pulse; `failed` is danger; terminal states are muted.
 */
const STYLE: Record<string, { box: string; dot: string }> = {
  pending: {
    box: 'border-warning-line bg-warning-soft text-warning',
    dot: 'bg-warning animate-pulse-live',
  },
  running: {
    box: 'border-success-line bg-success-soft text-success',
    dot: 'bg-success animate-pulse-live',
  },
  idle: {
    box: 'border-border bg-muted text-muted-foreground',
    dot: 'bg-graphite-400',
  },
  stopped: {
    box: 'border-border bg-muted text-muted-foreground',
    dot: 'bg-graphite-400',
  },
  failed: {
    box: 'border-danger-line bg-danger-soft text-danger',
    dot: 'bg-danger',
  },
  cleaned: {
    box: 'border-border bg-transparent text-muted-foreground',
    dot: 'bg-graphite-400',
  },
}

export function RunStateBadge({
  state,
  className,
}: {
  state: RunState
  className?: string
}) {
  const { t } = useTranslation('agentops')
  const key = String(state)
  const style = STYLE[key] ?? STYLE.idle
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 rounded-sm border px-1.5 py-0.5 text-xs font-medium whitespace-nowrap',
        style.box,
        className,
      )}
    >
      <span className={cn('size-1.5 rounded-full', style.dot)} aria-hidden />
      {t(`state.${key}`, { defaultValue: key })}
    </span>
  )
}
