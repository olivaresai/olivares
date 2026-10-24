// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The table alternative of the reach view (issue #2553): one row per solid link,
// one per dashed link (the tool whose session asked), and one honest row per
// waiting destination no tool asked for. The graph's waiting encoding
// (dash + clock) becomes a Status word here; the same model drives both.
import { useTranslation } from 'react-i18next'
import { StaticTable } from '@/components/data/static-table'
import { KIND_LABEL_KEY, type ReachModel } from './model'

interface ReachRow {
  key: string
  tool: string
  destination: string
  kind: string
  status: string
}

export function ReachTable({ model }: { model: ReachModel }) {
  const { t } = useTranslation('reach')
  const toolName = new Map(model.tools.map((tool) => [tool.id, tool.name]))
  const destById = new Map(model.destinations.map((d) => [d.id, d]))

  const rows: ReachRow[] = model.links.map((link) => {
    const dest = destById.get(link.destinationId)!
    return {
      key: link.id,
      tool: toolName.get(link.toolId) ?? '',
      destination: dest.ref,
      kind: t(KIND_LABEL_KEY[dest.family]),
      // A request ON a reached destination keeps the reached row and says so.
      status: dest.waiting ? t('statusReachedWaiting') : t('statusReached'),
    }
  })
  for (const link of model.waitingLinks) {
    const dest = destById.get(link.destinationId)!
    rows.push({
      key: link.id,
      tool: toolName.get(link.toolId) ?? '',
      destination: dest.ref,
      kind: t(KIND_LABEL_KEY[dest.family]),
      status: t('statusWaiting'),
    })
  }
  for (const dest of model.destinations) {
    if (dest.waiting && !model.linkedDestIds.has(dest.id)) {
      rows.push({
        key: `wait-${dest.id}`,
        tool: '—',
        destination: dest.ref,
        kind: t(KIND_LABEL_KEY[dest.family]),
        status: t('statusWaiting'),
      })
    }
  }

  return (
    <StaticTable>
      <thead>
        <tr>
          <th>{t('thTool')}</th>
          <th>{t('thDestination')}</th>
          <th>{t('thKind')}</th>
          <th>{t('thStatus')}</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => (
          <tr key={row.key}>
            <td>{row.tool}</td>
            <td>{row.destination}</td>
            <td>{row.kind}</td>
            <td>{row.status}</td>
          </tr>
        ))}
      </tbody>
    </StaticTable>
  )
}
