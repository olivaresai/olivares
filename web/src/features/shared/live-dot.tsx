// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import './i18n'
import type { StreamStatus } from './sse'

/**
 * LiveDot — an HONEST connection indicator for the SSE-backed views. It shows the
 * real stream status (open → a teal live pulse; connecting → amber; error → red;
 * closed → muted), never a fake "live" badge when the socket is down. The pulse is
 * the one sanctioned looping animation (`animate-pulse-live`, index.css).
 */
const DOT: Record<StreamStatus, string> = {
  open: 'bg-confidence-attributed animate-pulse-live',
  connecting: 'bg-warning',
  error: 'bg-danger',
  closed: 'bg-graphite-400',
}

export function LiveDot({
  status,
  className,
}: {
  status: StreamStatus
  className?: string
}) {
  const { t } = useTranslation('shared')
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 text-xs text-muted-foreground',
        className,
      )}
      title={t(`live.${status}`)}
    >
      <span className={cn('size-2 rounded-full', DOT[status])} aria-hidden />
      <span>{t(`live.${status}`)}</span>
    </span>
  )
}
