// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Card } from '@/components/ui/card'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState } from '@/components/ui/error-state'
import { Skeleton } from '@/components/ui/skeleton'
import { useAuth } from '@/lib/auth/context'
import { useWorkspaceFilter } from '@/lib/hooks/use-workspace-filter'
import { cn } from '@/lib/utils'
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

export function Topology({
  onSelect,
}: {
  onSelect: (entry: CatalogEntry) => void
}) {
  const { t } = useTranslation('inventory')
  const { activeTenant } = useAuth()
  const { workspaceId, queryKey: wsKey } = useWorkspaceFilter()

  const query = useQuery({
    queryKey: [
      ...inventoryKeys.entities(activeTenant, { limit: TOPO_LIMIT }),
      wsKey,
    ],
    queryFn: () =>
      inventoryApi.entities({ workspace_id: workspaceId, limit: TOPO_LIMIT }),
  })

  const byKind = useMemo(() => {
    const map = new Map<string, CatalogEntry[]>()
    for (const e of query.data?.items ?? []) {
      const arr = map.get(e.kind) ?? []
      arr.push(e)
      map.set(e.kind, arr)
    }
    return map
  }, [query.data])

  if (query.isLoading) {
    return (
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {Array.from({ length: 6 }).map((_, i) => (
          <Skeleton key={i} className="h-40 w-full rounded-lg" />
        ))}
      </div>
    )
  }
  if (query.error) return <ErrorState retry={() => void query.refetch()} />

  const total = query.data?.items.length ?? 0
  if (total === 0)
    return (
      <EmptyState
        title={t('topology.emptyTitle')}
        description={t('topology.emptyHint')}
      />
    )

  return (
    <div className="space-y-5">
      <p className="text-sm text-muted-foreground">{t('topology.note')}</p>
      {BANDS.map((band) => {
        const kinds = band.kinds.filter((k) => byKind.has(k))
        if (kinds.length === 0) return null
        return (
          <section key={band.id}>
            <h2 className="mb-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              {t(`topology.bands.${band.id}`)}
            </h2>
            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
              {KIND_ORDER.filter((k) => kinds.includes(k)).map((kind) => (
                <KindCard
                  key={kind}
                  kind={kind}
                  entries={byKind.get(kind) ?? []}
                  onSelect={onSelect}
                />
              ))}
            </div>
          </section>
        )
      })}
      {/* ⛔ NO CONVERTIDO A ListTruncationBadge A PROPOSITO: esto pinta un <p> discreto, no un
          Badge de aviso. Convertirlo cambiaria el ASPECTO de la pantalla sin que nadie lo haya
          pedido, y la convergencia era para unificar la REGLA, no para uniformar el diseno.
          Le falta la guarda !error -- el aviso puede salir sobre datos viejos si la consulta
          fallo -- y eso es una decision de esta vista, no de la convergencia. */}
      {query.data?.has_more && (
        <p className="text-xs text-muted-foreground">
          {t('topology.truncated', { n: TOPO_LIMIT })}
        </p>
      )}
    </div>
  )
}

function KindCard({
  kind,
  entries,
  onSelect,
}: {
  kind: string
  entries: CatalogEntry[]
  onSelect: (entry: CatalogEntry) => void
}) {
  const { t } = useTranslation('inventory')
  const Icon = ENTITY_ICON[kind] ?? Box
  const stale = entries.filter((e) => e.status === 'stale').length
  const shown = entries.slice(0, 8)
  return (
    <Card className="flex flex-col gap-2 p-3">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <span className="flex size-7 items-center justify-center rounded-md bg-muted text-muted-foreground [&_svg]:size-4">
            <Icon />
          </span>
          <span className="font-medium text-foreground">
            {t(`kinds.${kind}`, { defaultValue: kind })}
          </span>
        </div>
        <Badge variant="neutral" className="tabular-nums">
          {entries.length}
        </Badge>
      </div>
      {stale > 0 && <InvStatus status="stale" className="w-fit" />}
      <div className="flex flex-wrap gap-1">
        {shown.map((e) => (
          <button
            key={e.entity_id}
            type="button"
            onClick={() => onSelect(e)}
            title={e.ref || e.name}
            className={cn(
              'max-w-[12rem] truncate rounded-sm border border-border bg-surface px-1.5 py-0.5 font-mono text-xs text-muted-foreground transition-colors hover:bg-muted hover:text-foreground',
              'focus-visible:ring-2 focus-visible:ring-ring outline-none',
              e.status === 'stale' && 'opacity-60',
            )}
          >
            {e.name || e.ref || e.entity_id.slice(0, 8)}
          </button>
        ))}
        {entries.length > shown.length && (
          <span className="px-1 text-xs text-muted-foreground">
            +{entries.length - shown.length}
          </span>
        )}
      </div>
    </Card>
  )
}
