// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// How full a session's context is, as a ring beside send. The figures
// are the side pane's Activity: the same query, so an open pane asks once. A tool that
// reports no window draws nothing.
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { useAuth } from '@/lib/auth/context'
import { formatInt, formatPercent } from '@/lib/format'
import { cn } from '@/lib/utils'
import './i18n'

const RADIUS = 7
const CIRCUMFERENCE = 2 * Math.PI * RADIUS
/** From here the ring warns: the tool compacts or stops soon. */
const NEARLY_FULL = 80

export function ContextRing({ run }: { run: RunDTO }) {
  const { t } = useTranslation('sessions')
  const { activeTenant } = useAuth()
  const { data } = useQuery({
    queryKey: [
      ...agentOpsKeys.runActivity(activeTenant, run.run_ref),
      run.state,
    ],
    queryFn: ({ signal }) => agentOpsApi.activity(run.run_ref, { signal }),
    placeholderData: keepPreviousData,
  })
  if (!data?.context_window || data.context_percent == null) return null
  const percent = Math.min(100, Math.max(0, data.context_percent))
  const label = t('activity.contextUsage', {
    tokens: formatInt(data.context_tokens),
    percent: formatPercent(data.context_percent, { digits: 1 }),
    window: formatInt(data.context_window),
  })
  return (
    <span
      role="img"
      aria-label={label}
      title={label}
      data-testid="composer-context"
      className={cn(
        'inline-flex size-8 shrink-0 items-center justify-center',
        percent >= NEARLY_FULL ? 'text-warning' : 'text-muted-foreground',
      )}
    >
      <svg viewBox="0 0 18 18" className="size-[18px] -rotate-90" aria-hidden>
        <circle
          cx="9"
          cy="9"
          r={RADIUS}
          fill="none"
          stroke="currentColor"
          strokeOpacity={0.25}
          strokeWidth={2}
        />
        <circle
          cx="9"
          cy="9"
          r={RADIUS}
          fill="none"
          stroke="currentColor"
          strokeWidth={2}
          strokeLinecap="round"
          strokeDasharray={CIRCUMFERENCE}
          strokeDashoffset={CIRCUMFERENCE * (1 - percent / 100)}
        />
      </svg>
    </span>
  )
}
