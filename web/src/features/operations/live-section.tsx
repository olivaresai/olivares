// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The live sessions by the tool each runs and the person it acts for. It reads the sessions
// Now already reads (one hook, one cache key), so this page never counts a session Now does
// not; a live page that was cut says the counts are a minimum.
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import type { TableColumn } from '@/components/data/data-table'
import { useRecentSessions } from '@/features/home/use-recent-sessions'
import { formatInt } from '@/lib/format'
import { BarFigure } from './figures'
import { liveBreakdown, type Bucket } from './live'
import './i18n'

type Row = { name: string; count: number }

export function LiveSection() {
  const { t, i18n } = useTranslation('operations')
  const lang = i18n.language
  const { liveAnswer, recentSessions, sessionsQ } = useRecentSessions()
  const breakdown = useMemo(
    () => (recentSessions ? liveBreakdown(recentSessions) : null),
    [recentSessions],
  )

  const rowsOf = (buckets: Bucket[]): Row[] =>
    buckets.map((b) => ({
      name: b.kind === 'named' ? b.name : t(`live.${b.kind}`),
      count: b.count,
    }))
  const columnsOf = (header: string): TableColumn<Row>[] => [
    { accessorKey: 'name', header },
    {
      accessorKey: 'count',
      header: t('live.columns.sessions'),
      cell: ({ row }) => formatInt(row.original.count, lang),
    },
  ]
  const summaryOf = (key: 'byTool' | 'byPerson', rows: Row[]) =>
    t(`live.${key}.summary`, {
      count: breakdown?.total ?? 0,
      list: rows.map((r) => `${r.name} ${formatInt(r.count, lang)}`).join(', '),
    })

  let body
  if (sessionsQ.isError) {
    body = (
      <p role="alert" className="text-caption text-bad">
        {t('live.error')}
      </p>
    )
  } else if (!breakdown) {
    body = (
      <p role="status" className="text-caption text-text-2">
        {t('live.loading')}
      </p>
    )
  } else if (breakdown.total === 0) {
    body = <p className="text-caption text-text-2">{t('live.none')}</p>
  } else {
    const byTool = rowsOf(breakdown.byTool)
    const byPerson = rowsOf(breakdown.byPerson)
    body = (
      <>
        <div className="grid gap-4 lg:grid-cols-2">
          <BarFigure<Row>
            title={t('live.byTool.title')}
            summary={summaryOf('byTool', byTool)}
            rows={byTool}
            columns={columnsOf(t('live.columns.tool'))}
            getRowId={(r) => r.name}
            valueFormatter={(v) => formatInt(v, lang)}
            categoryKey="name"
            valueKey="count"
          />
          <BarFigure<Row>
            title={t('live.byPerson.title')}
            summary={summaryOf('byPerson', byPerson)}
            rows={byPerson}
            columns={columnsOf(t('live.columns.person'))}
            getRowId={(r) => r.name}
            valueFormatter={(v) => formatInt(v, lang)}
            categoryKey="name"
            valueKey="count"
          />
        </div>
        {liveAnswer?.has_more ? (
          <p className="text-caption text-text-2">{t('live.more')}</p>
        ) : null}
      </>
    )
  }

  return (
    <section aria-labelledby="operations-live" className="flex flex-col gap-3">
      <h2 id="operations-live" className="text-body font-semibold">
        {t('live.heading')}
      </h2>
      {body}
    </section>
  )
}
