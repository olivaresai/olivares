// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The map as a list: one row per origin, under the same group the graph puts it in, with
// what it reads and writes counted from the same built edges. A row opens the same
// detail as a click on the graph.
import type { Edge, Node } from '@xyflow/react'
import { ChevronRight } from 'lucide-react'
import { useId } from 'react'
import { useTranslation } from 'react-i18next'
import type { AccessEdgeData, OriginNodeData } from './graph-model'
import type { GroupKey } from './groups'
import { isWriteMode } from './types'

interface Row {
  id: string
  label: string
  ref: string
  reads: number
  writes: number
}

export function OriginList({
  nodes,
  edges,
  groupOf,
  onSelect,
}: {
  nodes: readonly Node[]
  edges: readonly Edge[]
  groupOf: (data: OriginNodeData) => GroupKey
  onSelect: (id: string) => void
}) {
  const { t } = useTranslation('accessMap')
  const counts = new Map<string, { reads: number; writes: number }>()
  for (const e of edges) {
    const mode = (e.data as AccessEdgeData).mode
    const c = counts.get(e.source) ?? { reads: 0, writes: 0 }
    if (mode === 'read') c.reads++
    if (isWriteMode(mode)) c.writes++
    counts.set(e.source, c)
  }
  const groups = new Map<string, { label: string; rows: Row[] }>()
  for (const n of nodes) {
    const data = n.data as OriginNodeData
    if (data.role !== 'origin') continue
    const g = groupOf(data)
    const group = groups.get(g.key) ?? { label: g.label, rows: [] }
    group.rows.push({
      id: n.id,
      label: data.label,
      ref: data.ref,
      ...(counts.get(n.id) ?? { reads: 0, writes: 0 }),
    })
    groups.set(g.key, group)
  }
  const byLabel = (a: { label: string }, b: { label: string }) =>
    a.label.localeCompare(b.label)

  return (
    <section className="flex flex-col gap-4">
      <h2 className="sr-only">{t('list.title')}</h2>
      {[...groups.entries()]
        .sort(([, a], [, b]) => byLabel(a, b))
        .map(([key, group]) => (
          <OriginGroup
            key={key}
            label={group.label}
            rows={group.rows.sort(byLabel)}
            onSelect={onSelect}
          />
        ))}
    </section>
  )
}

function OriginGroup({
  label,
  rows,
  onSelect,
}: {
  label: string
  rows: Row[]
  onSelect: (id: string) => void
}) {
  const { t } = useTranslation('accessMap')
  const id = useId()
  return (
    <div>
      <h3
        id={id}
        className="mb-1.5 text-caption font-medium text-muted-foreground"
      >
        {label}
      </h3>
      <ul
        aria-labelledby={id}
        className="divide-y divide-border rounded-lg border border-border bg-surface"
      >
        {rows.map((row) => (
          <li key={row.id}>
            <button
              type="button"
              onClick={() => onSelect(row.id)}
              title={row.ref}
              className="flex w-full items-center gap-3 px-3 py-2 text-left outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring"
            >
              <span className="min-w-0 flex-1 truncate text-body font-medium text-foreground">
                {row.label}
              </span>
              <span className="shrink-0 text-caption tabular-nums text-muted-foreground">
                {t('list.counts', { writes: row.writes, reads: row.reads })}
              </span>
              <ChevronRight
                className="size-4 shrink-0 text-muted-foreground"
                aria-hidden
              />
            </button>
          </li>
        ))}
      </ul>
    </div>
  )
}
