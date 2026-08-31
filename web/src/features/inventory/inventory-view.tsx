// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { Boxes, RefreshCw } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { PageHeader } from '@/components/ui/page-header'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useAuth } from '@/lib/auth/context'
import { formatInt } from '@/lib/format'
import { useWorkspaceFilter } from '@/lib/hooks/use-workspace-filter'
import { cn } from '@/lib/utils'
import { inventoryApi, inventoryKeys } from './api'
import { CatalogTable } from './catalog-table'
import { EntityDetailSheet } from './entity-detail'
import { KIND_ORDER } from './entity-icons'
import { Topology } from './topology'
import type { CatalogEntry } from './types'
import './i18n'

const ALL = '__all__'

export function InventoryView() {
  const { t } = useTranslation('inventory')
  const { activeTenant } = useAuth()
  const { workspaceId, queryKey: wsKey } = useWorkspaceFilter()
  const [kind, setKind] = useState<string>(ALL)
  const [status, setStatus] = useState<string>(ALL)
  const [selected, setSelected] = useState<CatalogEntry | null>(null)

  const summary = useQuery({
    queryKey: [...inventoryKeys.summary(activeTenant), wsKey],
    queryFn: () => inventoryApi.summary({ workspace_id: workspaceId }),
  })

  const totals = useMemo(() => {
    const byKind = summary.data?.by_kind ?? {}
    let active = 0
    let stale = 0
    for (const v of Object.values(byKind)) {
      active += v.active
      stale += v.stale
    }
    return { active, stale, total: summary.data?.total ?? active + stale }
  }, [summary.data])

  const presentKinds = useMemo(
    () =>
      KIND_ORDER.filter((k) => (summary.data?.by_kind?.[k]?.total ?? 0) > 0),
    [summary.data],
  )

  const facetKind = kind === ALL ? undefined : kind
  const facetStatus = status === ALL ? undefined : status

  return (
    <div className="flex h-full flex-col gap-4">
      <PageHeader
        icon={Boxes}
        title={t('title')}
        description={t('subtitle')}
        actions={
          <Button
            variant="ghost"
            size="sm"
            onClick={() => void summary.refetch()}
            disabled={summary.isFetching}
          >
            <RefreshCw
              className={cn('size-3.5', summary.isFetching && 'animate-spin')}
            />
            {t('refresh')}
          </Button>
        }
      />

      {/* Summary tiles */}
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
        <StatTile
          label={t('summary.total')}
          value={totals.total}
          loading={summary.isLoading}
        />
        <StatTile
          label={t('summary.active')}
          value={totals.active}
          tone="success"
          loading={summary.isLoading}
        />
        <StatTile
          label={t('summary.stale')}
          value={totals.stale}
          tone={totals.stale > 0 ? 'warning' : undefined}
          loading={summary.isLoading}
        />
        <StatTile
          label={t('summary.kinds')}
          value={presentKinds.length}
          loading={summary.isLoading}
        />
      </div>

      <Tabs defaultValue="catalog" className="min-h-0 flex-1">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <TabsList>
            <TabsTrigger value="catalog">{t('tabs.catalog')}</TabsTrigger>
            <TabsTrigger value="topology">{t('tabs.topology')}</TabsTrigger>
          </TabsList>
          <div className="flex items-center gap-2">
            <Select value={kind} onValueChange={setKind}>
              <SelectTrigger
                className="h-7 w-auto min-w-[8rem] text-xs"
                aria-label={t('facets.allKinds')}
              >
                <SelectValue placeholder={t('facets.allKinds')} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>{t('facets.allKinds')}</SelectItem>
                {presentKinds.map((k) => (
                  <SelectItem key={k} value={k}>
                    {t(`kinds.${k}`, { defaultValue: k })}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select value={status} onValueChange={setStatus}>
              <SelectTrigger
                className="h-7 w-auto min-w-[7rem] text-xs"
                aria-label={t('facets.allStatus')}
              >
                <SelectValue placeholder={t('facets.allStatus')} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>{t('facets.allStatus')}</SelectItem>
                <SelectItem value="active">{t('status.active')}</SelectItem>
                <SelectItem value="stale">{t('status.stale')}</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>

        <TabsContent value="catalog">
          <CatalogTable
            kind={facetKind}
            status={facetStatus}
            onSelect={setSelected}
          />
        </TabsContent>
        <TabsContent value="topology">
          <Topology onSelect={setSelected} />
        </TabsContent>
      </Tabs>

      <EntityDetailSheet entry={selected} onClose={() => setSelected(null)} />
    </div>
  )
}

function StatTile({
  label,
  value,
  tone,
  loading,
}: {
  label: string
  value: number
  tone?: 'success' | 'warning'
  loading?: boolean
}) {
  return (
    <Card className="p-3">
      <div className="text-xs text-muted-foreground">{label}</div>
      {loading ? (
        <Skeleton className="mt-1 h-7 w-12" />
      ) : (
        <div
          className={cn(
            'font-display text-2xl font-semibold tabular-nums',
            tone === 'success' && 'text-success',
            tone === 'warning' && 'text-warning',
            !tone && 'text-foreground',
          )}
        >
          {formatInt(value)}
        </div>
      )}
    </Card>
  )
}
