// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { Pencil, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { KvList, KvRow } from '@/components/ui/kv'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Separator } from '@/components/ui/separator'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { RelTimeLabel } from '@/features/shared'
import { ListTruncationBadge } from '@/features/_intel'
import { knowledgeApi, knowledgeKeys } from './api'
import { DataProductEditorDialog } from './data-product-editor'
import './i18n'
import type {
  DataContractDTO,
  DataProductDTO,
  DataProductHealthDTO,
  DPEventDTO,
} from './types'

export interface DataProductDetailSheetProps {
  productId: string | null
  open: boolean
  onOpenChange: (open: boolean) => void
}

const HEALTH_VARIANT: Record<string, 'success' | 'warning' | 'danger'> = {
  healthy: 'success',
  degraded: 'warning',
  unhealthy: 'danger',
}

const FRESHNESS_VARIANT: Record<string, 'success' | 'danger' | 'neutral'> = {
  fresh: 'success',
  stale: 'danger',
  unknown: 'neutral',
}

const QUALITY_VARIANT: Record<string, 'success' | 'danger' | 'neutral'> = {
  passing: 'success',
  failing: 'danger',
  unconfigured: 'neutral',
}

const STATUS_VARIANT: Record<
  string,
  'neutral' | 'success' | 'warning' | 'danger'
> = {
  draft: 'neutral',
  published: 'success',
  deprecated: 'warning',
  archived: 'danger',
}

export function DataProductDetailSheet({
  productId,
  open,
  onOpenChange,
}: DataProductDetailSheetProps) {
  const { t } = useTranslation(['knowledge', 'common'])
  const { activeTenant, can } = useAuth()
  const canWrite = can('knowledge:data_product:write')
  const canAdmin = can('knowledge:data_product:admin')

  const [editorOpen, setEditorOpen] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)

  const productQuery = useQuery({
    queryKey: knowledgeKeys.dataProduct(activeTenant, productId ?? ''),
    queryFn: () => knowledgeApi.getDataProduct(productId!),
    enabled: open && !!productId,
  })
  const product = productQuery.data

  const healthQuery = useQuery({
    queryKey: knowledgeKeys.dataProductHealth(activeTenant, productId ?? ''),
    queryFn: () => knowledgeApi.dataProductHealth(productId!),
    enabled: open && !!productId,
  })

  const contractsQuery = useQuery({
    queryKey: knowledgeKeys.dataProductContracts(activeTenant, productId ?? ''),
    queryFn: () => knowledgeApi.listContracts(productId!),
    enabled: open && !!productId,
  })

  // ⛔ EL CONTRATO ACTIVO SE PREGUNTA, NO SE DEDUCE. Derivarlo de `contractsQuery`
  //    —una pagina— hacia que la pantalla dijera «ningun contrato activo» cuando el activo
  //    quedaba fuera de la pagina: una afirmacion falsa sobre que rige AHORA. El motor tiene
  //    la respuesta exacta y responde 404 cuando de verdad no hay (`dataproduct.go:897`).
  const activeContractQuery = useQuery({
    queryKey: knowledgeKeys.dataProductActiveContract(
      activeTenant,
      productId ?? '',
    ),
    queryFn: () => knowledgeApi.activeContract(productId!),
    enabled: open && !!productId,
    retry: false,
  })

  const eventsQuery = useQuery({
    queryKey: knowledgeKeys.dataProductEvents(activeTenant, productId ?? ''),
    queryFn: () => knowledgeApi.listDPEvents(productId!),
    enabled: open && !!productId,
  })

  const remove = usePrivilegedMutation<void, { deleted: boolean }>({
    mutationFn: () => knowledgeApi.deleteDataProduct(productId!),
    invalidateKeys: () => [knowledgeKeys.dataProducts(activeTenant)],
    successMessage: t('dataProducts.deleteProduct'),
    onDone: () => {
      setConfirmDelete(false)
      onOpenChange(false)
    },
  })

  const publish = usePrivilegedMutation<void, DataProductDTO>({
    mutationFn: () => knowledgeApi.publishDataProduct(productId!),
    invalidateKeys: () => [
      knowledgeKeys.dataProduct(activeTenant, productId ?? ''),
      knowledgeKeys.dataProducts(activeTenant),
    ],
    successMessage: t('dataProducts.lifecycle.publish'),
  })

  const deprecate = usePrivilegedMutation<void, DataProductDTO>({
    mutationFn: () => knowledgeApi.deprecateDataProduct(productId!),
    invalidateKeys: () => [
      knowledgeKeys.dataProduct(activeTenant, productId ?? ''),
      knowledgeKeys.dataProducts(activeTenant),
    ],
    successMessage: t('dataProducts.lifecycle.deprecate'),
  })

  const archive = usePrivilegedMutation<void, DataProductDTO>({
    mutationFn: () => knowledgeApi.archiveDataProduct(productId!),
    invalidateKeys: () => [
      knowledgeKeys.dataProduct(activeTenant, productId ?? ''),
      knowledgeKeys.dataProducts(activeTenant),
    ],
    successMessage: t('dataProducts.lifecycle.archive'),
  })

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle>{product?.name ?? t('dataProducts.name')}</SheetTitle>
          {product && (
            <SheetDescription className="flex flex-wrap items-center gap-1.5">
              <Badge variant={STATUS_VARIANT[product.status] ?? 'neutral'}>
                {t(`dataProducts.statuses.${product.status}`, {
                  defaultValue: product.status,
                })}
              </Badge>
              <Badge variant="outline">
                {t(`dataProducts.enforcement.${product.enforcement_mode}`, {
                  defaultValue: product.enforcement_mode,
                })}
              </Badge>
            </SheetDescription>
          )}
        </SheetHeader>

        <ScrollArea className="-mr-4 flex-1 pr-4">
          {productQuery.isLoading ? (
            <div className="flex flex-col gap-3">
              {Array.from({ length: 5 }).map((_, i) => (
                <Skeleton key={i} className="h-16 w-full" />
              ))}
            </div>
          ) : productQuery.error instanceof ApiError &&
            productQuery.error.isStepUpRequired ? (
            // ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es SÓLO el status 403
            // (lib/api/errors.ts:59) y un `step_up_required` lo satisface también, así que
            // leerlo primero sustituía el producto de datos por una acusación falsa y sin
            // salida — el permiso lo tiene; lo que está por debajo de AAL3 es la sesión.
            <StepUpRequiredState
              action="generic"
              onElevated={() => void productQuery.refetch()}
            />
          ) : productQuery.error instanceof ApiError &&
            productQuery.error.isForbidden ? (
            <ForbiddenState />
          ) : productQuery.error || !product ? (
            <ErrorState retry={() => productQuery.refetch()} />
          ) : (
            <DetailBody
              product={product}
              health={healthQuery.data}
              healthLoading={healthQuery.isLoading}
              contracts={contractsQuery.data?.items ?? []}
              contractsQuery={contractsQuery}
              activeContract={activeContractQuery.data}
              activeContractLoading={activeContractQuery.isLoading}
              activeContractError={activeContractQuery.error}
              events={eventsQuery.data?.items ?? []}
              eventsQuery={eventsQuery}
              eventsLoading={eventsQuery.isLoading}
              canWrite={canWrite}
              canAdmin={canAdmin}
              publishPending={publish.isPending}
              deprecatePending={deprecate.isPending}
              archivePending={archive.isPending}
              onEdit={() => setEditorOpen(true)}
              onDelete={() => setConfirmDelete(true)}
              onPublish={() => publish.mutate()}
              onDeprecate={() => deprecate.mutate()}
              onArchive={() => archive.mutate()}
            />
          )}
        </ScrollArea>
      </SheetContent>

      {product && (
        <DataProductEditorDialog
          open={editorOpen}
          onOpenChange={setEditorOpen}
          product={product}
        />
      )}

      {product && (
        <ConfirmDialog
          open={confirmDelete}
          onOpenChange={setConfirmDelete}
          title={t('dataProducts.deleteProduct')}
          description={t('dataProducts.deleteConfirm')}
          tone="danger"
          confirmLabel={t('dataProducts.deleteProduct')}
          pending={remove.isPending}
          onConfirm={() => remove.mutate()}
        />
      )}
    </Sheet>
  )
}

function DetailBody({
  product,
  health,
  healthLoading,
  contracts,
  contractsQuery,
  activeContract,
  activeContractLoading,
  activeContractError,
  events,
  eventsQuery,
  eventsLoading,
  canWrite,
  canAdmin,
  publishPending,
  deprecatePending,
  archivePending,
  onEdit,
  onDelete,
  onPublish,
  onDeprecate,
  onArchive,
}: {
  product: DataProductDTO
  health?: DataProductHealthDTO
  healthLoading: boolean
  contracts: DataContractDTO[]
  contractsQuery: { data?: unknown; error?: unknown }
  activeContract?: DataContractDTO
  activeContractLoading: boolean
  activeContractError?: unknown
  events: DPEventDTO[]
  eventsQuery: { data?: unknown; error?: unknown }
  eventsLoading: boolean
  canWrite: boolean
  canAdmin: boolean
  publishPending: boolean
  deprecatePending: boolean
  archivePending: boolean
  onEdit: () => void
  onDelete: () => void
  onPublish: () => void
  onDeprecate: () => void
  onArchive: () => void
}) {
  const { t } = useTranslation('knowledge')

  return (
    <div className="flex flex-col gap-5">
      {/* Action bar. */}
      <div className="flex flex-wrap gap-2">
        {canWrite && product.status === 'draft' && (
          <Button
            variant="primary"
            size="sm"
            onClick={onPublish}
            disabled={publishPending}
          >
            {t('dataProducts.lifecycle.publish')}
          </Button>
        )}
        {canWrite && product.status === 'published' && (
          <Button
            variant="secondary"
            size="sm"
            onClick={onDeprecate}
            disabled={deprecatePending}
          >
            {t('dataProducts.lifecycle.deprecate')}
          </Button>
        )}
        {canWrite &&
          (product.status === 'published' ||
            product.status === 'deprecated') && (
            <Button
              variant="secondary"
              size="sm"
              onClick={onArchive}
              disabled={archivePending}
            >
              {t('dataProducts.lifecycle.archive')}
            </Button>
          )}
        {canWrite && (
          <Button variant="ghost" size="sm" onClick={onEdit}>
            <Pencil />
            {t('dataProducts.editProduct')}
          </Button>
        )}
        {canAdmin && (
          <Button variant="destructive" size="sm" onClick={onDelete}>
            <Trash2 />
            {t('dataProducts.deleteProduct')}
          </Button>
        )}
      </div>

      {/* Metadata. */}
      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-medium text-foreground">
          {t('dataProducts.name')}
        </h3>
        <KvList>
          {product.description && (
            <KvRow label={t('dataProducts.description')}>
              {product.description}
            </KvRow>
          )}
          <KvRow label={t('dataProducts.owner')}>
            <span className="font-mono text-xs">{product.owner_ref}</span>
          </KvRow>
          <KvRow label={t('dataProducts.kbBinding')}>
            {product.kb_ref ? (
              <span className="font-mono text-xs">{product.kb_ref}</span>
            ) : (
              <span className="text-muted-foreground">
                {t('dataProducts.kbUnbound')}
              </span>
            )}
          </KvRow>
          <KvRow label={t('dataProducts.enforcementMode')}>
            <Badge variant="outline">
              {t(`dataProducts.enforcement.${product.enforcement_mode}`, {
                defaultValue: product.enforcement_mode,
              })}
            </Badge>
          </KvRow>
          {product.availability_target && (
            <KvRow label={t('dataProducts.availabilityTarget')}>
              {product.availability_target}
            </KvRow>
          )}
          <KvRow label={t('dataProducts.slaDays')} mono>
            {product.freshness_sla_seconds}s
          </KvRow>
          {product.tags && Object.keys(product.tags).length > 0 && (
            <KvRow label={t('dataProducts.tags')} align="start">
              <div className="flex flex-wrap gap-1">
                {Object.entries(product.tags).map(([k, v]) => (
                  <Badge key={k} variant="outline">
                    {k}: {v}
                  </Badge>
                ))}
              </div>
            </KvRow>
          )}
        </KvList>
      </section>

      <Separator />

      {/* Health. */}
      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-medium text-foreground">
          {t('dataProducts.health.title')}
        </h3>
        {healthLoading ? (
          <div className="flex flex-col gap-2">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-8 w-full" />
            ))}
          </div>
        ) : health ? (
          <KvList>
            <KvRow label={t('dataProducts.health.overall')}>
              <Badge
                variant={HEALTH_VARIANT[health.overall_health] ?? 'neutral'}
              >
                {t(`dataProducts.health.${health.overall_health}`, {
                  defaultValue: health.overall_health,
                })}
              </Badge>
            </KvRow>
            <KvRow label={t('dataProducts.health.freshStatus')}>
              <Badge
                variant={
                  FRESHNESS_VARIANT[health.freshness.status] ?? 'neutral'
                }
              >
                {t(`dataProducts.health.${health.freshness.status}`, {
                  defaultValue: health.freshness.status,
                })}
              </Badge>
            </KvRow>
            <KvRow label={t('dataProducts.health.ageSeconds')} mono>
              {health.freshness.age_seconds}s
            </KvRow>
            <KvRow label={t('dataProducts.health.slaSeconds')} mono>
              {health.freshness.sla_seconds}s
            </KvRow>
            <KvRow label={t('dataProducts.health.qualityStatus')}>
              <Badge
                variant={QUALITY_VARIANT[health.quality.status] ?? 'neutral'}
              >
                {t(`dataProducts.health.${health.quality.status}`, {
                  defaultValue: health.quality.status,
                })}
              </Badge>
              <span className="ml-2 font-mono tabular-nums text-xs">
                {health.quality.score}
              </span>
            </KvRow>
            <KvRow label={t('dataProducts.health.threshold')} mono>
              {health.quality.threshold}
            </KvRow>
            <KvRow label={t('dataProducts.usageCount')} mono>
              {health.usage.total}
            </KvRow>
            {health.kb && (
              <KvRow label={t('dataProducts.kbBinding')}>
                <span className="font-mono text-xs">{health.kb.name}</span>
                <span className="ml-2 text-xs text-muted-foreground">
                  ({health.kb.doc_count} docs, {health.kb.chunk_count} chunks)
                </span>
              </KvRow>
            )}
          </KvList>
        ) : (
          <p className="text-xs text-muted-foreground">
            {t('dataProducts.health.unknown')}
          </p>
        )}
      </section>

      <Separator />

      {/* Active contract. */}
      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-medium text-foreground">
          {t('dataProducts.contract.title')}
        </h3>
        {/* ⛔ 404 ES UNA RESPUESTA; cualquier otro error NO. El motor contesta 404 cuando de
            verdad no hay contrato activo (`dataproduct.go:897`), y eso SI se puede afirmar. Un
            500, un permiso o una red caida no dicen nada sobre si existe: pintar «ningun
            contrato activo» ahi seria afirmar la ausencia sin haberla comprobado. */}
        {activeContractLoading ? (
          <Skeleton className="h-16 w-full" />
        ) : activeContract ? (
          <KvList>
            <KvRow label={t('dataProducts.contract.version')} mono>
              v{activeContract.version}
            </KvRow>
            <KvRow label={t('dataProducts.contract.validationMode')}>
              <Badge variant="outline">
                {t(
                  `dataProducts.contract.modes.${activeContract.validation_mode}`,
                  { defaultValue: activeContract.validation_mode },
                )}
              </Badge>
            </KvRow>
            <KvRow
              label={t('dataProducts.contract.completenessThreshold')}
              mono
            >
              {activeContract.completeness_threshold}%
            </KvRow>
            <KvRow label={t('dataProducts.contract.freshnessOverride')} mono>
              {activeContract.freshness_override_seconds}s
            </KvRow>
            {activeContract.schema_definition && (
              <KvRow label={t('dataProducts.contract.schema')} align="start">
                <pre className="max-h-40 overflow-auto rounded-md border border-border bg-muted p-2 font-mono text-xs">
                  {JSON.stringify(activeContract.schema_definition, null, 2)}
                </pre>
              </KvRow>
            )}
            {activeContract.note && (
              <KvRow label={t('dataProducts.contract.note')}>
                {activeContract.note}
              </KvRow>
            )}
          </KvList>
        ) : activeContractError instanceof ApiError &&
          activeContractError.status === 404 ? (
          <EmptyState
            title={t('dataProducts.contract.noContract')}
            description=""
          />
        ) : (
          <EmptyState
            title={t('dataProducts.contract.undetermined')}
            description=""
          />
        )}

        <ListTruncationBadge
          query={contractsQuery}
          label={t('dataProducts.contract.truncated', {
            n: contracts.length,
          })}
          hint={t('dataProducts.contract.truncatedHint')}
          className="px-0 pt-0 pb-0"
        />

        {/* Contract version history. */}
        {contracts.length > 1 && (
          <div className="mt-2 flex flex-col gap-1">
            <p className="text-xs font-medium text-muted-foreground">
              {t('dataProducts.contract.version')} history
            </p>
            <ul className="flex flex-col gap-1">
              {contracts.map((c) => (
                <li
                  key={c.id}
                  className="flex items-center gap-2 text-xs text-muted-foreground"
                >
                  <span className="font-mono tabular-nums">v{c.version}</span>
                  <Badge
                    variant={c.status === 'active' ? 'success' : 'neutral'}
                  >
                    {c.status === 'active'
                      ? t('dataProducts.contract.active')
                      : t('dataProducts.contract.superseded')}
                  </Badge>
                  {c.note && <span className="truncate">{c.note}</span>}
                </li>
              ))}
            </ul>
          </div>
        )}
      </section>

      <Separator />

      {/* Events. */}
      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-medium text-foreground">
          {t('dataProducts.events.title')}
        </h3>
        <ListTruncationBadge
          query={eventsQuery}
          label={t('dataProducts.events.truncated', { n: events.length })}
          hint={t('dataProducts.events.truncatedHint')}
          className="px-0 pt-0 pb-0"
        />
        {eventsLoading ? (
          <div className="flex flex-col gap-2">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-10 w-full" />
            ))}
          </div>
        ) : events.length === 0 ? (
          <p className="text-xs text-muted-foreground">
            {t('dataProducts.events.title')} — none
          </p>
        ) : (
          <ul className="flex flex-col gap-2">
            {events.map((ev) => (
              <li
                key={ev.id}
                className="rounded-md border border-border bg-surface p-3"
              >
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <Badge variant="outline">
                    {t(`dataProducts.events.types.${ev.event_type}`, {
                      defaultValue: ev.event_type,
                    })}
                  </Badge>
                  <Badge
                    variant={
                      ev.severity === 'critical' || ev.severity === 'high'
                        ? 'danger'
                        : ev.severity === 'medium'
                          ? 'warning'
                          : 'neutral'
                    }
                  >
                    {t(`dataProducts.events.severities.${ev.severity}`, {
                      defaultValue: ev.severity,
                    })}
                  </Badge>
                </div>
                <div className="mt-1 flex items-center gap-2 text-xs text-muted-foreground">
                  {ev.subject_kind && (
                    <span>
                      {ev.subject_kind}: {ev.subject_ref}
                    </span>
                  )}
                  <RelTimeLabel ts={ev.occurred_at} />
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}
