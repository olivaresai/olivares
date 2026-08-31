// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { Plus } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ForbiddenState } from '@/components/ui/error-state'
import { EmptyState } from '@/components/ui/empty-state'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { useAuth } from '@/lib/auth/context'
import { ListTruncationBadge } from '@/features/_intel'
import { knowledgeApi, knowledgeKeys } from './api'
import { DataProductDetailSheet } from './data-product-detail'
import { DataProductEditorDialog } from './data-product-editor'
import './i18n'
import type { DataProductDTO } from './types'

const STATUS_VARIANT: Record<string, string> = {
  draft: 'neutral',
  published: 'success',
  deprecated: 'warning',
  archived: 'danger',
}

function QualityBadge({ score }: { score: number }) {
  const variant = score >= 80 ? 'success' : score >= 50 ? 'warning' : 'danger'
  return (
    <Badge variant={variant}>
      <span className="font-mono tabular-nums">{score}</span>
    </Badge>
  )
}

function FreshnessBadge({
  lastIngest,
  sla,
}: {
  lastIngest?: string
  sla: number
}) {
  const { t } = useTranslation('knowledge')

  if (!lastIngest) {
    return <Badge variant="neutral">{t('dataProducts.health.unknown')}</Badge>
  }

  const ageSeconds = Math.floor(
    (Date.now() - new Date(lastIngest).getTime()) / 1000,
  )
  const ratio = sla > 0 ? ageSeconds / sla : 0

  const variant = ratio <= 0.8 ? 'success' : ratio <= 1 ? 'warning' : 'danger'
  const label =
    ratio <= 1 ? t('dataProducts.health.fresh') : t('dataProducts.health.stale')

  return <Badge variant={variant}>{label}</Badge>
}

export interface DataProductListProps {
  canRead: boolean
  canWrite: boolean
}

export function DataProductList({ canRead, canWrite }: DataProductListProps) {
  const { t } = useTranslation(['knowledge', 'common'])
  const { activeTenant } = useAuth()

  const [statusFilter, setStatusFilter] = useState<string>('all')
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [detailOpen, setDetailOpen] = useState(false)
  const [editorOpen, setEditorOpen] = useState(false)

  const filterParams =
    statusFilter === 'all' ? undefined : { status: statusFilter }

  const products = useQuery({
    queryKey: knowledgeKeys.dataProducts(activeTenant, filterParams ?? null),
    queryFn: () => knowledgeApi.listDataProducts(filterParams),
    enabled: canRead,
  })

  const columns: TableColumn<DataProductDTO, unknown>[] = [
    {
      accessorKey: 'name',
      header: t('dataProducts.name'),
      cell: ({ row }) => (
        <span className="font-medium text-foreground">{row.original.name}</span>
      ),
    },
    {
      accessorKey: 'owner_ref',
      header: t('dataProducts.owner'),
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted-foreground">
          {row.original.owner_ref}
        </span>
      ),
    },
    {
      accessorKey: 'status',
      header: t('dataProducts.status'),
      cell: ({ row }) => {
        const s = row.original.status
        return (
          <Badge
            variant={
              (STATUS_VARIANT[s] as
                'neutral' | 'success' | 'warning' | 'danger') ?? 'neutral'
            }
          >
            {t(`dataProducts.statuses.${s}`, { defaultValue: s })}
          </Badge>
        )
      },
    },
    {
      accessorKey: 'kb_ref',
      header: t('dataProducts.kbBinding'),
      cell: ({ row }) =>
        row.original.kb_ref ? (
          <span className="font-mono text-xs text-foreground">
            {row.original.kb_ref}
          </span>
        ) : (
          <span className="text-xs text-muted-foreground">
            {t('dataProducts.kbUnbound')}
          </span>
        ),
    },
    {
      accessorKey: 'quality_score',
      header: t('dataProducts.qualityScore'),
      cell: ({ row }) => <QualityBadge score={row.original.quality_score} />,
    },
    {
      accessorKey: 'usage_count',
      header: t('dataProducts.usageCount'),
      cell: ({ row }) => (
        <span className="font-mono tabular-nums">
          {row.original.usage_count}
        </span>
      ),
    },
    {
      id: 'freshness',
      header: t('dataProducts.freshness'),
      cell: ({ row }) => (
        <FreshnessBadge
          lastIngest={row.original.last_ingest_at}
          sla={row.original.freshness_sla_seconds}
        />
      ),
    },
    {
      accessorKey: 'enforcement_mode',
      header: t('dataProducts.enforcementMode'),
      cell: ({ row }) => {
        const m = row.original.enforcement_mode
        return (
          <Badge variant="outline">
            {t(`dataProducts.enforcement.${m}`, { defaultValue: m })}
          </Badge>
        )
      },
    },
  ]

  if (!canRead) {
    return <ForbiddenState />
  }

  return (
    <>
      <ListTruncationBadge
        query={products}
        label={t('dataProducts.truncated', {
          n: products.data?.items?.length ?? 0,
        })}
        hint={t('dataProducts.truncatedHint')}
        className="px-0 pt-0 pb-3"
      />
      <DataTable
        columns={columns}
        data={products.data?.items ?? []}
        isLoading={products.isLoading}
        error={products.error}
        onRetry={() => products.refetch()}
        searchable
        searchPlaceholder={t('dataProducts.search')}
        getRowId={(r) => r.id}
        onRowClick={(r) => {
          setSelectedId(r.id)
          setDetailOpen(true)
        }}
        toolbar={
          <div className="flex items-center gap-2">
            <Select value={statusFilter} onValueChange={setStatusFilter}>
              <SelectTrigger
                aria-label={t('dataProducts.status')}
                className="w-[10rem]"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">{t('kbs.statusAll')}</SelectItem>
                <SelectItem value="draft">
                  {t('dataProducts.statuses.draft')}
                </SelectItem>
                <SelectItem value="published">
                  {t('dataProducts.statuses.published')}
                </SelectItem>
                <SelectItem value="deprecated">
                  {t('dataProducts.statuses.deprecated')}
                </SelectItem>
                <SelectItem value="archived">
                  {t('dataProducts.statuses.archived')}
                </SelectItem>
              </SelectContent>
            </Select>
            {canWrite && (
              <Button
                variant="primary"
                size="sm"
                onClick={() => setEditorOpen(true)}
              >
                <Plus />
                {t('dataProducts.newProduct')}
              </Button>
            )}
          </div>
        }
        empty={
          <EmptyState
            title={t('empty.dataProduct.title')}
            description={t('empty.dataProduct.description')}
          />
        }
      />

      <DataProductDetailSheet
        productId={selectedId}
        open={detailOpen}
        onOpenChange={setDetailOpen}
      />
      <DataProductEditorDialog open={editorOpen} onOpenChange={setEditorOpen} />
    </>
  )
}
