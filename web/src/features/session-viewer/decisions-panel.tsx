// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DecisionsPanel — sidebar panel that groups consecutive tool calls within 60s
// on similar resources into logical "tasks" (decision clusters). This gives the
// viewer a coarse-grained view of what the agent was doing at each stage.
import { Layers } from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import type { DecisionGroup, TimelineEntry } from './types'
import './i18n'

export interface DecisionsPanelProps {
  /** The full timeline entries to cluster. */
  timeline: TimelineEntry[]
}

/** Maximum gap (ms) between consecutive tool events in the same cluster. */
const CLUSTER_GAP_MS = 60_000

/**
 * Groups consecutive tool-kind timeline entries that happen within 60 seconds
 * of each other into decision clusters. Non-tool entries break the chain.
 */
function clusterDecisions(timeline: TimelineEntry[]): DecisionGroup[] {
  const groups: DecisionGroup[] = []
  let current: DecisionGroup | null = null

  for (let i = 0; i < timeline.length; i++) {
    const entry = timeline[i]!
    if (entry.kind !== 'tool') {
      // Non-tool entry breaks the current cluster.
      if (current) {
        groups.push(current)
        current = null
      }
      continue
    }

    const entryMs = new Date(entry.at).getTime()
    if (Number.isNaN(entryMs)) continue

    if (current && current.tools.length > 0) {
      const lastTool = current.tools[current.tools.length - 1]!
      const lastMs = new Date(lastTool.at).getTime()
      if (entryMs - lastMs <= CLUSTER_GAP_MS) {
        // Within the time window — extend the current cluster.
        current.tools.push({
          tool: entry.tool_ref ?? '—',
          resource: entry.resource_ref ?? '',
          at: entry.at,
        })
        continue
      }
      // Gap too large — close the current cluster and start a new one.
      groups.push(current)
    }

    current = {
      startIdx: i,
      tools: [
        {
          tool: entry.tool_ref ?? '—',
          resource: entry.resource_ref ?? '',
          at: entry.at,
        },
      ],
    }
  }

  if (current) groups.push(current)
  return groups
}

export function DecisionsPanel({ timeline }: DecisionsPanelProps) {
  const { t } = useTranslation('session-viewer')

  const groups = useMemo(() => clusterDecisions(timeline), [timeline])

  return (
    <section className="flex flex-col gap-2">
      <h2 className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        <Layers className="size-3.5" aria-hidden />
        {t('panels.decisions')}
      </h2>

      {groups.length === 0 ? (
        <p className="text-xs text-muted-foreground">—</p>
      ) : (
        <ol className="flex flex-col gap-1">
          {groups.map((group, idx) => {
            // Summarize: show the dominant tool and unique resource count.
            const toolCounts = new Map<string, number>()
            const resourceSet = new Set<string>()
            for (const tool of group.tools) {
              toolCounts.set(tool.tool, (toolCounts.get(tool.tool) ?? 0) + 1)
              if (tool.resource) resourceSet.add(tool.resource)
            }
            // Dominant tool = most frequent.
            let dominant = ''
            let dominantCount = 0
            for (const [tool, count] of toolCounts) {
              if (count > dominantCount) {
                dominant = tool
                dominantCount = count
              }
            }

            return (
              <li
                key={group.startIdx}
                className="rounded-md border border-border px-2 py-1.5"
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="text-xs font-medium text-foreground">
                    {t('decisions.group', { idx: idx + 1 })}
                  </span>
                  <Badge variant="outline" className="text-[10px]">
                    {t('decisions.tools', { count: group.tools.length })}
                  </Badge>
                </div>
                <p className="mt-0.5 truncate font-mono text-[10px] text-muted-foreground" title={dominant}>
                  {dominant}
                  {resourceSet.size > 0 && ` (${t('decisions.fileCount', { count: resourceSet.size })})`}
                </p>
              </li>
            )
          })}
        </ol>
      )}
    </section>
  )
}
