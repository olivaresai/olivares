// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ToolsPanel — sidebar panel that aggregates tool usage from the timeline
// entries. Shows each unique tool_ref with its call count. Clicking a tool
// filters the unified timeline to show only events for that tool.
import { Wrench } from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import type { TimelineEntry, ToolAggregation } from './types'
import './i18n'

export interface ToolsPanelProps {
  /** The full timeline entries to aggregate over. */
  timeline: TimelineEntry[]
  /** The currently active tool filter (null = show all). */
  activeFilter: string | null
  /** Callback when a tool is clicked — toggles the filter. */
  onFilterChange: (tool: string | null) => void
}

export function ToolsPanel({
  timeline,
  activeFilter,
  onFilterChange,
}: ToolsPanelProps) {
  const { t } = useTranslation('session-viewer')

  const aggregations = useMemo<ToolAggregation[]>(() => {
    const map = new Map<string, { count: number; successCount: number; failCount: number }>()
    for (const entry of timeline) {
      if (entry.kind !== 'tool' || !entry.tool_ref) continue
      const existing = map.get(entry.tool_ref)
      if (existing) {
        existing.count++
        // Heuristic: entries without explicit outcome are counted as success.
        existing.successCount++
      } else {
        map.set(entry.tool_ref, { count: 1, successCount: 1, failCount: 0 })
      }
    }
    return Array.from(map.entries())
      .map(([tool, stats]) => ({ tool, ...stats }))
      .sort((a, b) => b.count - a.count)
  }, [timeline])

  return (
    <section className="flex flex-col gap-2">
      <h2 className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        <Wrench className="size-3.5" aria-hidden />
        {t('panels.tools')}
      </h2>

      {aggregations.length === 0 ? (
        <p className="text-xs text-muted-foreground">—</p>
      ) : (
        <ul className="flex flex-col gap-0.5">
          {aggregations.map((agg) => {
            const isActive = activeFilter === agg.tool
            return (
              <li key={agg.tool}>
                <button
                  type="button"
                  className={cn(
                    'flex w-full items-center justify-between gap-2 rounded-md px-2 py-1 text-left text-xs transition-colors',
                    'hover:bg-muted/50',
                    isActive && 'bg-accent-soft/60 ring-1 ring-accent-strong',
                  )}
                  aria-pressed={isActive}
                  onClick={() => onFilterChange(isActive ? null : agg.tool)}
                  title={agg.tool}
                >
                  {/* Active filter carries THREE independent signals, so it never
                      rests on colour alone (SC 1.4.1) nor on a sub-3:1 colour
                      (SC 1.4.11): the rail is a shape that is either present or
                      absent, aria-pressed says the same to AT, and the ring uses
                      --accent-strong (>=3:1, gated by at-run.ts). */}
                  <span className="flex min-w-0 items-center gap-1.5">
                    <span
                      aria-hidden
                      className={cn(
                        'h-3.5 w-1 shrink-0 rounded-full',
                        isActive ? 'bg-accent-strong' : 'bg-transparent',
                      )}
                    />
                    <span className="min-w-0 truncate font-mono text-foreground">
                      {agg.tool}
                    </span>
                  </span>
                  <Badge variant="outline" className="shrink-0 text-[10px]">
                    {t('tools.calls', { count: agg.count })}
                  </Badge>
                </button>
              </li>
            )
          })}
        </ul>
      )}
    </section>
  )
}
