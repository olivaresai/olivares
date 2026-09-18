// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Card } from '@/components/ui/card'
import { EmptyState } from '@/components/ui/empty-state'
import { AsyncSection } from '@/features/_intel'
import { useAuth } from '@/lib/auth/context'
import { cn } from '@/lib/utils'
import type { ListResponse } from '@/lib/api/types'
import { inventoryApi, inventoryKeys } from './api'
import { Box, ENTITY_ICON, KIND_ORDER } from './entity-icons'
import { InvStatus } from './status'
import type { CatalogEntry } from './types'

/** Estate composition is read as three bands: who acts, what they can use, what
 * they touch. Honest about its limits — runtime usage edges live in Sessions and
 * access edges in the Access map (the dedicated /topology endpoint was retired,
 * decision A); this is the structural make-up of the estate. */
const BANDS: { id: string; kinds: string[] }[] = [
  { id: 'origins', kinds: ['agent', 'session', 'identity'] },
  {
    id: 'capabilities',
    kinds: ['mcp_server', 'tool', 'skill', 'model', 'provider'],
  },
  { id: 'resources', kinds: ['resource'] },
]

const TOPO_LIMIT = 200

const BAND_KIND_SET = new Set(BANDS.flatMap((b) => b.kinds))

export function Topology({
  onSelect,
}: {
  onSelect: (entry: CatalogEntry, launcher?: HTMLElement) => void
}) {
  const { activeTenant } = useAuth()

  // Tenant-wide, like the catalog: no workspace in the call or the key (api.ts).
  const query = useQuery({
    queryKey: inventoryKeys.entities(activeTenant, { limit: TOPO_LIMIT }),
    queryFn: () => inventoryApi.entities({ limit: TOPO_LIMIT }),
  })

  // AsyncSection is the house mapping: pending → skeleton, 403 → calm forbidden,
  // other failure → error with retry and request id, data → the page. It branches
  // on `isError` BEFORE `data`, so a refetch that fails after a success shows the
  // failure and never the previous rows or counts as current.
  return (
    <AsyncSection query={query} skeletonHeight={160}>
      {(data) => <TopologyPage data={data} onSelect={onSelect} />}
    </AsyncSection>
  )
}

function TopologyPage({
  data,
  onSelect,
}: {
  data: ListResponse<CatalogEntry>
  onSelect: (entry: CatalogEntry, launcher?: HTMLElement) => void
}) {
  const { t } = useTranslation('inventory')
  const items = data.items ?? []
  const byKind = useMemo(() => {
    const map = new Map<string, CatalogEntry[]>()
    for (const e of items) {
      const arr = map.get(e.kind) ?? []
      arr.push(e)
      map.set(e.kind, arr)
    }
    return map
  }, [items])

  if (items.length === 0)
    return (
      <EmptyState
        title={t('topology.emptyTitle')}
        description={t('topology.emptyHint')}
      />
    )

  // Strict boolean: a transport string is not a page-limit disclosure.
  const pageLimited = data.has_more === true
  const leftover = [...byKind.keys()]
    .filter((k) => !BAND_KIND_SET.has(k))
    .sort()

  return (
    <div className="space-y-5">
      <p className="text-body text-muted-foreground">{t('topology.note')}</p>
      {BANDS.map((band) => {
        const kinds = band.kinds.filter((k) => byKind.has(k))
        if (kinds.length === 0) return null
        return (
          <section key={band.id}>
            <h2 className="mb-2 text-caption font-semibold uppercase tracking-wide text-muted-foreground">
              {t(`topology.bands.${band.id}`)}
            </h2>
            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
              {KIND_ORDER.filter((k) => kinds.includes(k)).map((kind) => (
                <KindCard
                  key={kind}
                  kind={kind}
                  entries={byKind.get(kind) ?? []}
                  pageLimited={pageLimited}
                  onSelect={onSelect}
                />
              ))}
            </div>
          </section>
        )
      })}
      {leftover.length > 0 && (
        <section>
          <h2 className="mb-2 text-caption font-semibold uppercase tracking-wide text-muted-foreground">
            {t('topology.bands.other')}
          </h2>
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {leftover.map((kind) => (
              <KindCard
                key={kind}
                kind={kind}
                entries={byKind.get(kind) ?? []}
                pageLimited={pageLimited}
                onSelect={onSelect}
              />
            ))}
          </div>
        </section>
      )}
      {/* Appearance stays a discreet <p>, not ListTruncationBadge. The rule is
          the same one (`has_more === true` on a successful page): AsyncSection
          has already refused to render this branch on error, so a retired page
          cannot advertise a limit. */}
      {pageLimited && (
        <div className="space-y-1">
          <p className="text-caption text-muted-foreground">
            {t('topology.truncated', { n: TOPO_LIMIT })}
          </p>
          <p className="text-caption text-muted-foreground">
            {t('topology.countsAreLoaded')}
          </p>
        </div>
      )}
    </div>
  )
}

function KindCard({
  kind,
  entries,
  pageLimited,
  onSelect,
}: {
  kind: string
  entries: CatalogEntry[]
  pageLimited: boolean
  onSelect: (entry: CatalogEntry, launcher?: HTMLElement) => void
}) {
  const { t } = useTranslation('inventory')
  const Icon = ENTITY_ICON[kind] ?? Box
  const stale = entries.filter((e) => e.status === 'stale').length
  const shown = entries.slice(0, 8)
  const kindLabel = t(`kinds.${kind}`, { defaultValue: kind })
  return (
    <Card className="flex min-w-0 flex-col gap-2 p-3">
      <div className="flex min-w-0 items-start justify-between gap-2">
        <div className="flex min-w-0 items-start gap-2">
          <span className="flex size-7 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground [&_svg]:size-4">
            <Icon />
          </span>
          <span className="min-w-0 break-words font-medium text-foreground">
            {kindLabel}
          </span>
        </div>
        <Badge
          variant="neutral"
          className="shrink-0 tabular-nums"
          title={
            pageLimited
              ? t('topology.loadedOnPage', { count: entries.length })
              : undefined
          }
          aria-label={
            pageLimited
              ? t('topology.loadedOnPage', { count: entries.length })
              : t('topology.loadedCount', { count: entries.length })
          }
        >
          {entries.length}
        </Badge>
      </div>
      {stale > 0 && <InvStatus status="stale" className="w-fit" />}
      <div className="flex min-w-0 flex-wrap gap-1">
        {shown.map((e) => {
          const label = e.name || e.ref || e.entity_id
          return (
            <button
              key={e.entity_id}
              type="button"
              onClick={(event) => onSelect(e, event.currentTarget)}
              title={label}
              aria-label={label}
              className={cn(
                'min-w-0 max-w-full break-all rounded-sm border border-border bg-surface px-1.5 py-0.5 text-left font-mono text-caption text-muted-foreground transition-colors hover:bg-muted hover:text-foreground sm:max-w-[12rem] sm:truncate sm:whitespace-nowrap',
                'focus-visible:ring-2 focus-visible:ring-ring outline-none',
                e.status === 'stale' && 'opacity-60',
              )}
            >
              {label}
            </button>
          )
        })}
        {entries.length > shown.length && (
          <span className="px-1 text-caption text-muted-foreground">
            +{entries.length - shown.length}
          </span>
        )}
      </div>
    </Card>
  )
}
