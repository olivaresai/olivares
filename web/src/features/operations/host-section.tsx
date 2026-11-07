// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The host the engine runs on: CPU and memory over the last 24 hours, each filesystem, and
// the disk each session folder uses. All of it is one read (`GET /v1/system/host`, a system
// administrator's); this section draws it and computes nothing the engine did not send but
// a peak, an average and a percentage of two numbers it did. A host that cannot be read
// (the engine answers 501 off Linux) is said so, never drawn empty.
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import type { TableColumn } from '@/components/data/data-table'
import { AsyncSection } from '@/features/_intel'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { formatBytes, formatPercent, formatTimeOfDay } from '@/lib/format'
import { operationsApi, operationsKeys, type HostStatus } from './api'
import { BarFigure, TrendFigure } from './figures'
import './i18n'

/** The engine samples once a minute. */
const HOST_REFETCH_MS = 60_000

type Sample = HostStatus['history'][number]
type Disk = HostStatus['filesystems'][number] & { used_percent: number }
type Folder = HostStatus['session_folders']['folders'][number]

const decimals = (lang: string, value: number) =>
  new Intl.NumberFormat(lang, { maximumFractionDigits: 2 }).format(value)

export function HostSection({ longRange }: { longRange: boolean }) {
  const { t } = useTranslation('operations')
  const { activeTenant } = useAuth()
  const hostQ = useQuery({
    queryKey: operationsKeys.host(activeTenant),
    queryFn: ({ signal }) => operationsApi.host({ signal }),
    // A host that cannot answer will not answer a minute later.
    refetchInterval: (query) =>
      query.state.status === 'error' ? false : HOST_REFETCH_MS,
  })
  const unavailable =
    hostQ.error instanceof ApiError && hostQ.error.status === 501
  return (
    <section aria-labelledby="operations-host" className="flex flex-col gap-3">
      <h2 id="operations-host" className="text-body font-semibold">
        {t('host.heading')}
      </h2>
      {unavailable ? (
        <p className="text-caption text-text-2">{t('host.unavailable')}</p>
      ) : (
        <>
          {longRange ? (
            <p className="text-caption text-text-2">{t('host.keeps24')}</p>
          ) : null}
          <AsyncSection query={hostQ} skeletonHeight={240}>
            {(host) => <HostBody host={host} />}
          </AsyncSection>
        </>
      )}
    </section>
  )
}

function HostBody({ host }: { host: HostStatus }) {
  const { t, i18n } = useTranslation('operations')
  const lang = i18n.language
  const when = (at: string) => formatTimeOfDay(Date.parse(at), lang)

  const facts = [
    t('host.facts.load', { load: decimals(lang, host.load.one) }),
    t('host.facts.cpu', { cpu: formatPercent(host.cpu_percent) }),
    t('host.facts.memory', {
      used: formatBytes(host.memory.used_bytes),
      total: formatBytes(host.memory.total_bytes),
    }),
  ]
  if (host.swap.total_bytes > 0)
    facts.push(
      t('host.facts.swap', {
        used: formatBytes(host.swap.used_bytes),
        total: formatBytes(host.swap.total_bytes),
      }),
    )

  const history = host.history
  const cpuPeak = Math.max(...history.map((s) => s.cpu_percent))
  const cpuAverage =
    history.reduce((sum, s) => sum + s.cpu_percent, 0) /
    Math.max(history.length, 1)
  const memoryPeak = Math.max(...history.map((s) => s.memory_used_bytes))

  const timeColumn: TableColumn<Sample> = {
    accessorKey: 'at',
    header: t('host.columns.time'),
    cell: ({ row }) => when(row.original.at),
  }
  const cpuColumns: TableColumn<Sample>[] = [
    timeColumn,
    {
      accessorKey: 'cpu_percent',
      header: t('host.columns.cpu'),
      cell: ({ row }) => formatPercent(row.original.cpu_percent),
    },
  ]
  const memoryColumns: TableColumn<Sample>[] = [
    timeColumn,
    {
      accessorKey: 'memory_used_bytes',
      header: t('host.columns.memory'),
      cell: ({ row }) => formatBytes(row.original.memory_used_bytes),
    },
    {
      accessorKey: 'swap_used_bytes',
      header: t('host.columns.swap'),
      cell: ({ row }) => formatBytes(row.original.swap_used_bytes),
    },
  ]

  const disks: Disk[] = host.filesystems
    .filter((f) => f.total_bytes > 0)
    .map((f) => ({ ...f, used_percent: (f.used_bytes / f.total_bytes) * 100 }))
    .sort((a, b) => b.used_percent - a.used_percent)
  const diskColumns: TableColumn<Disk>[] = [
    { accessorKey: 'mount', header: t('host.columns.mount') },
    { accessorKey: 'device', header: t('host.columns.device') },
    { accessorKey: 'type', header: t('host.columns.type') },
    {
      accessorKey: 'used_bytes',
      header: t('host.columns.used'),
      cell: ({ row }) => formatBytes(row.original.used_bytes),
    },
    {
      accessorKey: 'total_bytes',
      header: t('host.columns.size'),
      cell: ({ row }) => formatBytes(row.original.total_bytes),
    },
    {
      accessorKey: 'used_percent',
      header: t('host.columns.usedPercent'),
      cell: ({ row }) => formatPercent(row.original.used_percent),
    },
  ]

  const folders = host.session_folders
  const largest = folders.folders[0]
  const folderColumns: TableColumn<Folder>[] = [
    { accessorKey: 'name', header: t('host.columns.folder') },
    {
      accessorKey: 'bytes',
      header: t('host.columns.size'),
      cell: ({ row }) => formatBytes(row.original.bytes),
    },
  ]

  return (
    <div className="flex flex-col gap-4">
      <p className="text-body tabular-nums" data-testid="host-facts">
        {facts.join(' · ')}
      </p>
      {history.length === 0 ? (
        <p className="text-caption text-text-2">{t('host.noHistory')}</p>
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          <TrendFigure<Sample>
            title={t('host.cpu.title')}
            summary={t('host.cpu.summary', {
              now: formatPercent(host.cpu_percent),
              peak: formatPercent(cpuPeak),
              average: formatPercent(cpuAverage),
            })}
            rows={history}
            columns={cpuColumns}
            getRowId={(s) => s.at}
            valueFormatter={formatPercent}
            xKey="at"
            xTickFormatter={when}
            series={[{ key: 'cpu_percent', label: t('host.columns.cpu') }]}
          />
          <TrendFigure<Sample>
            title={t('host.memory.title')}
            summary={t('host.memory.summary', {
              used: formatBytes(host.memory.used_bytes),
              total: formatBytes(host.memory.total_bytes),
              peak: formatBytes(memoryPeak),
            })}
            rows={history}
            columns={memoryColumns}
            getRowId={(s) => s.at}
            valueFormatter={formatBytes}
            xKey="at"
            xTickFormatter={when}
            series={[
              { key: 'memory_used_bytes', label: t('host.columns.memory') },
              {
                key: 'swap_used_bytes',
                label: t('host.columns.swap'),
                kind: 'line',
              },
            ]}
            reference={{
              y: host.memory.total_bytes,
              label: t('host.memory.total'),
            }}
          />
        </div>
      )}
      {disks.length === 0 ? (
        <p className="text-caption text-text-2">{t('host.noFilesystems')}</p>
      ) : (
        <BarFigure<Disk>
          title={t('host.disks.title')}
          summary={t('host.disks.summary', {
            count: disks.length,
            mount: disks[0].mount,
            pct: formatPercent(disks[0].used_percent),
          })}
          rows={disks}
          columns={diskColumns}
          getRowId={(d) => d.mount}
          valueFormatter={formatPercent}
          categoryKey="mount"
          valueKey="used_percent"
        />
      )}
      {largest ? (
        <>
          <BarFigure<Folder>
            title={t('host.folders.title')}
            summary={t('host.folders.summary', {
              count: folders.folders.length,
              total: formatBytes(folders.total_bytes),
              largest: largest.name,
              size: formatBytes(largest.bytes),
            })}
            rows={folders.folders}
            columns={folderColumns}
            getRowId={(f) => f.name}
            valueFormatter={formatBytes}
            categoryKey="name"
            valueKey="bytes"
          />
          {folders.truncated ? (
            <p role="status" className="text-caption text-text-2">
              {t('host.folders.floor')}
            </p>
          ) : null}
        </>
      ) : (
        <p className="text-caption text-text-2">{t('host.noFolders')}</p>
      )}
    </div>
  )
}
