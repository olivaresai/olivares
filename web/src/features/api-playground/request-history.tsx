// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Clock, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import type { HistoryEntry } from './use-playground'

type Translator = (key: string, options?: Record<string, unknown>) => string

interface RequestHistoryProps {
  entries: HistoryEntry[]
  onClear: () => void
}

function relativeTime(ts: number, t: Translator): string {
  const diff = Date.now() - ts
  if (diff < 60_000) return t('historyPanel.relative.justNow')
  if (diff < 3_600_000)
    return t('historyPanel.relative.minutes', {
      count: Math.floor(diff / 60_000),
    })
  if (diff < 86_400_000)
    return t('historyPanel.relative.hours', {
      count: Math.floor(diff / 3_600_000),
    })
  return t('historyPanel.relative.days', {
    count: Math.floor(diff / 86_400_000),
  })
}

export function RequestHistory({ entries, onClear }: RequestHistoryProps) {
  const { t } = useTranslation('apiPlayground')
  if (entries.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center gap-2 py-8 text-muted-foreground">
        <Clock className="h-6 w-6 opacity-40" />
        <p className="text-xs">{t('noRequests')}</p>
      </div>
    )
  }

  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between px-3 py-1.5">
        <span className="text-xs font-semibold text-muted-foreground">
          {t('historyPanel.recentCount', { count: entries.length })}
        </span>
        <Button
          size="sm"
          variant="ghost"
          className="h-6 px-2 text-xs"
          onClick={onClear}
          aria-label={t('historyPanel.clearAria')}
        >
          <Trash2 className="mr-1 h-3 w-3" />
          {t('clearHistory')}
        </Button>
      </div>
      {entries.map((entry, i) => (
        <div
          key={`${entry.timestamp}-${i}`}
          className="flex items-center gap-2 px-3 py-1 text-xs"
        >
          <Badge
            variant="outline"
            className={cn(
              'h-5 w-12 justify-center font-mono text-[10px]',
              entry.status >= 200 &&
                entry.status < 300 &&
                'border-emerald-500/40 text-emerald-600',
              entry.status >= 400 && 'border-red-500/40 text-red-600',
            )}
          >
            {entry.status || t('historyPanel.statusError')}
          </Badge>
          <span className="w-12 shrink-0 font-mono text-[10px] font-semibold uppercase text-muted-foreground">
            {entry.method}
          </span>
          <span className="min-w-0 flex-1 truncate font-mono text-muted-foreground">
            {entry.path}
          </span>
          <span className="shrink-0 tabular-nums text-muted-foreground">
            {t('historyPanel.duration', { duration: entry.durationMs })}
          </span>
          <span className="shrink-0 text-muted-foreground">
            {relativeTime(entry.timestamp, t)}
          </span>
        </div>
      ))}
    </div>
  )
}
