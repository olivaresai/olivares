// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { AlertTriangle, Eye, History, Plus, ShieldAlert } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { Field } from '@/components/ui/field'
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
import { Textarea } from '@/components/ui/textarea'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { cn } from '@/lib/utils'
import { RelTimeLabel } from '@/features/shared'
import { ActivateBreakGlassDialog } from './activate-dialog'
import { governanceApi, governanceKeys } from './api'
import './i18n'
import type {
  BreakGlassDTO,
  BreakGlassStatus,
  BreakGlassUseDTO,
  ReviewBreakGlassInput,
} from './types'

const BREAK_GLASS_POLL_MS = 12_000
const BREAK_GLASS_PAGE_SIZE = 100
const MAX_REVIEW_NOTE_LENGTH = 4_096

export function BreakGlassView({ active = true }: { active?: boolean }) {
  const { t } = useTranslation(['governance', 'common'])
  const { activeTenant, can, principal } = useAuth()
  const canRead = can('governance:breakglass:read')
  const canAdmin = can('governance:breakglass:admin')
  const [selected, setSelected] = useState<string | null>(null)
  const [detailOpen, setDetailOpen] = useState(false)
  const [activateOpen, setActivateOpen] = useState(false)

  const activeGrants = useQuery({
    queryKey: governanceKeys.breakGlassList(activeTenant, {
      status: 'active',
    }),
    queryFn: () =>
      governanceApi.listBreakGlass({
        status: 'active',
        limit: BREAK_GLASS_PAGE_SIZE,
      }),
    enabled: canRead && active,
    refetchInterval: active ? BREAK_GLASS_POLL_MS : false,
  })

  const history = useInfiniteQuery({
    queryKey: governanceKeys.breakGlassList(activeTenant),
    queryFn: ({ pageParam }) =>
      governanceApi.listBreakGlass({
        cursor: pageParam,
        limit: BREAK_GLASS_PAGE_SIZE,
      }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) =>
      page.has_more && page.cursor ? page.cursor : undefined,
    enabled: canRead && active,
    refetchInterval: active ? BREAK_GLASS_POLL_MS : false,
  })

  if (!canRead) {
    return (
      <ForbiddenState
        title={t('breakGlass.forbiddenTitle')}
        description={t('breakGlass.forbiddenBody')}
      />
    )
  }

  const queryError = activeGrants.error ?? history.error
  // ⛔ ASEGURAMIENTO ANTES QUE ROL, y aquí no es hipotético: `modules/governance/breakglass.go:187`
  // devuelve un 403 con `code: step_up_required` cuando la activación viene de una sesión AAL1.
  // `isForbidden` es SÓLO el status (lib/api/errors.ts:59-61), así que ese 403 caía en la rama de
  // rol y la pantalla de ACCESO DE EMERGENCIA decía «no tienes autorización» — en el momento en
  // que alguien intenta abrir un break-glass, que es cuando peor sienta que te manden a pedir un
  // permiso que ya tienes. La ceremonia es la salida, y estaba escondida.
  //
  // El `!canRead` de arriba se queda ANTES a propósito: es una capacidad del cliente, no una
  // respuesta del motor. Convertirlo en ceremonia sería el defecto espejo.
  if (queryError instanceof ApiError && queryError.isStepUpRequired) {
    return (
      <StepUpRequiredState
        action="generic"
        onElevated={() => {
          void activeGrants.refetch()
          void history.refetch()
        }}
      />
    )
  }
  if (queryError instanceof ApiError && queryError.isForbidden) {
    return (
      <ForbiddenState
        title={t('breakGlass.forbiddenTitle')}
        description={t('breakGlass.forbiddenBody')}
      />
    )
  }

  const activeItems = (activeGrants.data?.items ?? []).filter(
    (grant) => grant.status === 'active',
  )
  const historyItems = history.data?.pages.flatMap((page) => page.items) ?? []
  // The server blocks a new activation while ANY prior grant is unreviewed, and
  // it knows about grants this client has not paged in. Deciding from the loaded
  // pages alone would enable the button on an estate whose blocking grant simply
  // has not been fetched — so an unfetched tail counts as "unknown", not "clear".
  const historyIncomplete = history.hasNextPage ?? false
  const unreviewedExists = historyItems.some((grant) => !grant.reviewed)
  const lifecycleUnknown =
    activeGrants.isLoading ||
    history.isLoading ||
    !!queryError ||
    (historyIncomplete && !unreviewedExists)
  const activationBlock = lifecycleUnknown
    ? t('breakGlass.activationStateUnknown')
    : unreviewedExists
      ? t('breakGlass.activationBlockedUnreviewed')
      : principal?.kind !== 'user' || !principal.user_id
        ? t('breakGlass.activationHumanRequired')
        : (principal?.aal ?? 1) < 3
          ? t('breakGlass.activationHardwareRequired')
          : null

  function inspect(id: string) {
    setSelected(id)
    setDetailOpen(true)
  }

  return (
    <div className="flex flex-col gap-5 pt-1">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="max-w-3xl">
          <h2 className="text-base font-semibold text-foreground">
            {t('breakGlass.title')}
          </h2>
          <p className="mt-1 text-sm text-muted-foreground">
            {t('breakGlass.caption')}
          </p>
        </div>
        {canAdmin && (
          <Button
            variant="destructive-solid"
            size="sm"
            disabled={activationBlock != null}
            onClick={() => setActivateOpen(true)}
          >
            <Plus aria-hidden />
            {t('breakGlass.activateButton')}
          </Button>
        )}
      </div>

      {canAdmin && activationBlock && (
        <p
          role="status"
          className="flex items-start gap-2 rounded-lg border border-warning-line bg-warning-soft p-3 text-sm text-foreground"
        >
          <AlertTriangle
            className="mt-0.5 size-4 shrink-0 text-warning"
            aria-hidden
          />
          {activationBlock}
        </p>
      )}

      <section
        aria-labelledby="break-glass-current"
        className="flex flex-col gap-3"
      >
        <div>
          <h3
            id="break-glass-current"
            className="text-sm font-semibold text-foreground"
          >
            {t('breakGlass.currentTitle')}
          </h3>
          <p className="text-xs text-muted-foreground">
            {t('breakGlass.currentCaption')}
          </p>
        </div>

        {activeGrants.isLoading ? (
          <Skeleton className="h-36 w-full" />
        ) : activeGrants.error ? (
          <ErrorState retry={() => void activeGrants.refetch()} />
        ) : activeItems.length === 0 ? (
          <EmptyState
            icon={<ShieldAlert />}
            title={t('breakGlass.noActive')}
            description={t('breakGlass.noActiveHint')}
          />
        ) : (
          <div className="grid gap-3 lg:grid-cols-2">
            {activeItems.map((grant) => (
              <ActiveGrantCard
                key={grant.id}
                grant={grant}
                onInspect={() => inspect(grant.id)}
              />
            ))}
          </div>
        )}
      </section>

      <Separator />

      <section
        aria-labelledby="break-glass-history"
        className="flex flex-col gap-3"
      >
        <div>
          <h3
            id="break-glass-history"
            className="text-sm font-semibold text-foreground"
          >
            {t('breakGlass.historyTitle')}
          </h3>
          <p className="text-xs text-muted-foreground">
            {t('breakGlass.historyCaption')}
          </p>
        </div>
        <GrantHistoryTable
          items={historyItems}
          loading={history.isLoading}
          error={history.error}
          onRetry={() => void history.refetch()}
          onInspect={inspect}
          hasMore={!!history.hasNextPage}
          loadingMore={history.isFetchingNextPage}
          onLoadMore={() => void history.fetchNextPage()}
        />
      </section>

      <BreakGlassDetailSheet
        grantId={selected}
        open={detailOpen}
        onOpenChange={setDetailOpen}
      />
      <ActivateBreakGlassDialog
        open={activateOpen}
        onOpenChange={setActivateOpen}
      />
    </div>
  )
}

function ActiveGrantCard({
  grant,
  onInspect,
}: {
  grant: BreakGlassDTO
  onInspect: () => void
}) {
  const { t } = useTranslation('governance')
  const needsReview = grant.use_count > 0 && !grant.reviewed
  return (
    <article
      role="alert"
      className={cn(
        'flex flex-col gap-3 rounded-lg border border-danger-line bg-danger-soft p-4',
        needsReview && 'ring-1 ring-warning',
      )}
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Badge variant="danger">{t('breakGlass.activeCritical')}</Badge>
        <span className="font-mono text-xs text-muted-foreground">
          {grant.id}
        </span>
      </div>
      <dl className="grid gap-2 text-sm sm:grid-cols-2">
        <div>
          <dt className="text-xs text-muted-foreground">
            {t('breakGlass.scope')}
          </dt>
          <dd className="font-mono text-xs text-foreground">
            <ScopeLabel matchAction={grant.match_action} />
          </dd>
        </div>
        <div>
          <dt className="text-xs text-muted-foreground">
            {t('breakGlass.expiresAt')}
          </dt>
          <dd className="text-foreground">
            <RelTimeLabel ts={grant.expires_at} />
          </dd>
        </div>
        <div>
          <dt className="text-xs text-muted-foreground">
            {t('breakGlass.activatedBy')}
          </dt>
          <dd className="font-mono text-xs text-foreground">
            {grant.activated_by ?? t('breakGlass.notAvailable')}
          </dd>
        </div>
        <div>
          <dt className="text-xs text-muted-foreground">
            {t('breakGlass.activatedAt')}
          </dt>
          <dd className="text-foreground">
            <RelTimeLabel ts={grant.activated_at} />
          </dd>
        </div>
      </dl>
      {grant.reason && (
        <p className="text-sm text-foreground">{grant.reason}</p>
      )}
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant={needsReview ? 'warning' : 'info'}>
            {t('breakGlass.useCount', { count: grant.use_count })}
          </Badge>
          {needsReview && (
            <Badge variant="warning">{t('breakGlass.useReviewPending')}</Badge>
          )}
        </div>
        <Button variant="secondary" size="sm" onClick={onInspect}>
          <Eye aria-hidden />
          {t('breakGlass.inspect')}
        </Button>
      </div>
    </article>
  )
}

function GrantHistoryTable({
  items,
  loading,
  error,
  onRetry,
  onInspect,
  hasMore,
  loadingMore,
  onLoadMore,
}: {
  items: BreakGlassDTO[]
  loading: boolean
  error: unknown
  onRetry: () => void
  onInspect: (id: string) => void
  hasMore: boolean
  loadingMore: boolean
  onLoadMore: () => void
}) {
  const { t } = useTranslation('governance')

  if (loading) return <Skeleton className="h-52 w-full" />
  // Segunda decisión del fichero: la tabla de histórico se gatea aparte de la vista que la
  // contiene, así que una guarda de POSICIÓN sobre el fichero no la ve. Ver breakglass.go:187.
  if (error instanceof ApiError && error.isStepUpRequired) {
    return <StepUpRequiredState action="generic" onElevated={onRetry} />
  }
  if (error instanceof ApiError && error.isForbidden) {
    return (
      <ForbiddenState
        title={t('breakGlass.forbiddenTitle')}
        description={t('breakGlass.forbiddenBody')}
      />
    )
  }
  if (error) return <ErrorState retry={onRetry} />
  if (items.length === 0) {
    return (
      <EmptyState
        icon={<History />}
        title={t('breakGlass.historyEmpty')}
        description={t('breakGlass.historyEmptyHint')}
      />
    )
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="overflow-x-auto rounded-lg border border-border">
        <table
          className="w-full text-sm"
          aria-label={t('breakGlass.tableLabel')}
        >
          <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
            <tr>
              <th scope="col" className="px-3 py-2 font-medium">
                {t('breakGlass.scope')}
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                {t('breakGlass.status')}
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                {t('breakGlass.activatedBy')}
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                {t('breakGlass.window')}
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                {t('breakGlass.uses')}
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                {t('breakGlass.review')}
              </th>
            </tr>
          </thead>
          <tbody>
            {items.map((grant) => {
              const pendingUseReview = grant.use_count > 0 && !grant.reviewed
              return (
                <tr
                  key={grant.id}
                  data-review-pending={pendingUseReview ? 'true' : undefined}
                  className={cn(
                    'border-t border-border align-top',
                    pendingUseReview && 'bg-warning-soft/60',
                  )}
                >
                  <td className="px-3 py-2">
                    <Button
                      variant="link"
                      onClick={() => onInspect(grant.id)}
                      aria-label={t('breakGlass.inspectGrant', {
                        id: grant.id,
                      })}
                    >
                      <ScopeLabel matchAction={grant.match_action} />
                    </Button>
                    <p className="mt-1 font-mono text-xs text-muted-foreground">
                      {grant.id}
                    </p>
                  </td>
                  <td className="px-3 py-2">
                    <BreakGlassStatusBadge status={grant.status} />
                  </td>
                  <td className="px-3 py-2 font-mono text-xs text-muted-foreground">
                    {grant.activated_by ?? t('breakGlass.notAvailable')}
                  </td>
                  <td className="px-3 py-2 text-xs text-muted-foreground">
                    <WindowLabel grant={grant} />
                  </td>
                  <td className="px-3 py-2">
                    <Badge variant={pendingUseReview ? 'warning' : 'neutral'}>
                      {grant.use_count}
                    </Badge>
                    {pendingUseReview && (
                      <p className="mt-1 text-xs font-medium text-warning">
                        {t('breakGlass.useReviewPending')}
                      </p>
                    )}
                  </td>
                  <td className="px-3 py-2">
                    {grant.reviewed ? (
                      <>
                        <Badge variant="success">
                          {t('breakGlass.reviewed')}
                        </Badge>
                        {grant.reviewed_by && (
                          <p className="mt-1 font-mono text-xs text-muted-foreground">
                            {grant.reviewed_by}
                          </p>
                        )}
                        {grant.reviewed_at && (
                          <p className="mt-1 text-xs text-muted-foreground">
                            <RelTimeLabel ts={grant.reviewed_at} />
                          </p>
                        )}
                      </>
                    ) : (
                      <Badge variant="warning">
                        {t('breakGlass.reviewPending')}
                      </Badge>
                    )}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
      {hasMore && (
        <div className="flex justify-center">
          <Button
            variant="secondary"
            size="sm"
            disabled={loadingMore}
            onClick={onLoadMore}
          >
            {loadingMore
              ? t('breakGlass.loadingMore')
              : t('breakGlass.loadMore')}
          </Button>
        </div>
      )}
    </div>
  )
}

function BreakGlassDetailSheet({
  grantId,
  open,
  onOpenChange,
}: {
  grantId: string | null
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation('governance')
  const { activeTenant, can, principal } = useAuth()
  const canAdmin = can('governance:breakglass:admin')
  const [confirmRevoke, setConfirmRevoke] = useState(false)
  const [confirmReview, setConfirmReview] = useState(false)
  const [reviewNote, setReviewNote] = useState('')

  const detailQuery = useQuery({
    queryKey: governanceKeys.breakGlassDetail(activeTenant, grantId ?? ''),
    queryFn: () => governanceApi.getBreakGlass(grantId!),
    enabled: open && !!grantId,
    refetchInterval: open ? BREAK_GLASS_POLL_MS : false,
  })
  const usesQuery = useQuery({
    queryKey: governanceKeys.breakGlassUses(activeTenant, grantId ?? ''),
    queryFn: () => governanceApi.listBreakGlassUses(grantId!),
    enabled: open && !!grantId,
    refetchInterval: open ? BREAK_GLASS_POLL_MS : false,
  })

  const revoke = usePrivilegedMutation<void, BreakGlassDTO>({
    mutationFn: () => governanceApi.revokeBreakGlass(grantId!),
    invalidateKeys: () => [
      governanceKeys.breakGlass(activeTenant),
      governanceKeys.breakGlassDetail(activeTenant, grantId ?? ''),
    ],
    successMessage: t('breakGlass.revokeDone'),
    onDone: () => setConfirmRevoke(false),
  })

  const review = usePrivilegedMutation<ReviewBreakGlassInput, BreakGlassDTO>({
    mutationFn: (input) => governanceApi.reviewBreakGlass(grantId!, input),
    invalidateKeys: () => [
      governanceKeys.breakGlass(activeTenant),
      governanceKeys.breakGlassDetail(activeTenant, grantId ?? ''),
      governanceKeys.breakGlassUses(activeTenant, grantId ?? ''),
    ],
    successMessage: t('breakGlass.reviewDone'),
    onDone: () => {
      setConfirmReview(false)
      setReviewNote('')
    },
  })

  const detail = detailQuery.data
  const humanReviewBlocked = principal?.kind !== 'user' || !principal.user_id
  const selfReviewBlocked =
    !!principal?.actor && principal.actor === detail?.activated_by
  // Route on the CODE the server sends, never on its prose. A message is human
  // copy that gets reworded and translated; matching it would break silently —
  // the refusal would fall back to a generic error and the operator would just
  // retry the one thing that can never succeed for them.
  const separationDenied =
    review.error instanceof ApiError &&
    review.error.code === 'separation_of_duty'

  return (
    <Sheet
      open={open}
      onOpenChange={(nextOpen) => {
        if (!nextOpen) {
          setConfirmRevoke(false)
          setConfirmReview(false)
          setReviewNote('')
          revoke.reset()
          review.reset()
        }
        onOpenChange(nextOpen)
      }}
    >
      <SheetContent className="w-full sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle>{t('breakGlass.detailTitle')}</SheetTitle>
          <SheetDescription>
            {detail ? (
              <span className="flex flex-wrap items-center gap-2">
                <BreakGlassStatusBadge status={detail.status} />
                <span className="font-mono text-xs">{detail.id}</span>
              </span>
            ) : (
              t('breakGlass.detailCaption')
            )}
          </SheetDescription>
        </SheetHeader>

        <ScrollArea className="-mr-4 flex-1 pr-4">
          {detailQuery.isLoading ? (
            <div className="flex flex-col gap-3">
              {Array.from({ length: 5 }).map((_, index) => (
                <Skeleton key={index} className="h-16 w-full" />
              ))}
            </div>
          ) : detailQuery.error instanceof ApiError &&
            detailQuery.error.isStepUpRequired ? (
            // Tercera decisión del fichero: la hoja de detalle del grant. Cada una se gatea por
            // su cuenta, así que arreglar la primera no arregla ésta — y una guarda de posición
            // sobre el fichero deja de verla en cuanto la primera es correcta.
            <StepUpRequiredState
              action="generic"
              onElevated={() => void detailQuery.refetch()}
            />
          ) : detailQuery.error instanceof ApiError &&
            detailQuery.error.isForbidden ? (
            <ForbiddenState
              title={t('breakGlass.forbiddenTitle')}
              description={t('breakGlass.forbiddenBody')}
            />
          ) : detailQuery.error || !detail ? (
            <ErrorState retry={() => void detailQuery.refetch()} />
          ) : (
            <div className="flex flex-col gap-5">
              <GrantOverview detail={detail} />

              {!detail.reviewed && (
                <ReviewPanel
                  detail={detail}
                  canAdmin={canAdmin}
                  humanReviewBlocked={humanReviewBlocked}
                  selfReviewBlocked={selfReviewBlocked}
                  reviewNote={reviewNote}
                  onReviewNoteChange={setReviewNote}
                  onConfirmReview={() => setConfirmReview(true)}
                />
              )}

              <Separator />

              <section className="flex flex-col gap-2">
                <h3 className="text-sm font-medium text-foreground">
                  {t('breakGlass.trailTitle')}
                </h3>
                <p className="text-xs text-muted-foreground">
                  {t('breakGlass.trailCaption')}
                </p>
                <UseTrail
                  items={usesQuery.data?.items ?? []}
                  loading={usesQuery.isLoading}
                  error={usesQuery.error}
                  onRetry={() => void usesQuery.refetch()}
                />
              </section>
            </div>
          )}
        </ScrollArea>

        {detail?.status === 'active' && canAdmin && (
          <div className="flex justify-end border-t border-border pt-3">
            <Button
              variant="destructive"
              size="sm"
              onClick={() => setConfirmRevoke(true)}
            >
              {t('breakGlass.revokeButton')}
            </Button>
          </div>
        )}
      </SheetContent>

      <ConfirmDialog
        open={confirmRevoke}
        onOpenChange={setConfirmRevoke}
        title={t('breakGlass.revokeTitle')}
        description={t('breakGlass.revokeBody')}
        tone="danger"
        confirmLabel={t('breakGlass.revokeConfirm')}
        pending={revoke.isPending}
        onConfirm={() => revoke.mutate()}
      />

      <ConfirmDialog
        open={confirmReview}
        onOpenChange={(nextOpen) => {
          setConfirmReview(nextOpen)
          if (!nextOpen) review.reset()
        }}
        title={t('breakGlass.reviewTitle')}
        description={t('breakGlass.reviewBody')}
        confirmLabel={t('breakGlass.reviewConfirm')}
        pending={review.isPending}
        onConfirm={() =>
          review.mutate({
            note: reviewNote.trim(),
          })
        }
      >
        {separationDenied && (
          <p
            role="status"
            className="rounded-md border border-warning-line bg-warning-soft p-3 text-sm text-foreground"
          >
            {t('breakGlass.reviewSeparationDenied')}
          </p>
        )}
        <p className="mt-2 text-sm text-foreground">{reviewNote}</p>
      </ConfirmDialog>
    </Sheet>
  )
}

function GrantOverview({ detail }: { detail: BreakGlassDTO }) {
  const { t } = useTranslation('governance')
  return (
    <KvList>
      <KvRow label={t('breakGlass.scope')} mono>
        <ScopeLabel matchAction={detail.match_action} />
      </KvRow>
      <KvRow label={t('breakGlass.activatedBy')} mono>
        {detail.activated_by ?? t('breakGlass.notAvailable')}
      </KvRow>
      {detail.activated_at && (
        <KvRow label={t('breakGlass.activatedAt')}>
          <RelTimeLabel ts={detail.activated_at} />
        </KvRow>
      )}
      {detail.expires_at && (
        <KvRow label={t('breakGlass.expiresAt')}>
          <RelTimeLabel ts={detail.expires_at} />
        </KvRow>
      )}
      {detail.revoked_at && (
        <KvRow label={t('breakGlass.revokedAt')}>
          <RelTimeLabel ts={detail.revoked_at} />
        </KvRow>
      )}
      <KvRow label={t('breakGlass.uses')} mono>
        {detail.use_count}
      </KvRow>
      <KvRow label={t('breakGlass.review')}>
        <Badge variant={detail.reviewed ? 'success' : 'warning'}>
          {detail.reviewed
            ? t('breakGlass.reviewed')
            : t('breakGlass.reviewPending')}
        </Badge>
      </KvRow>
      {detail.reviewed_by && (
        <KvRow label={t('breakGlass.reviewedBy')} mono>
          {detail.reviewed_by}
        </KvRow>
      )}
      {detail.reviewed_at && (
        <KvRow label={t('breakGlass.reviewedAt')}>
          <RelTimeLabel ts={detail.reviewed_at} />
        </KvRow>
      )}
      {detail.reason && (
        <KvRow label={t('breakGlass.reason')} align="start">
          {detail.reason}
        </KvRow>
      )}
      {detail.review_note && (
        <KvRow label={t('breakGlass.reviewNote')} align="start">
          {detail.review_note}
        </KvRow>
      )}
    </KvList>
  )
}

function ReviewPanel({
  detail,
  canAdmin,
  humanReviewBlocked,
  selfReviewBlocked,
  reviewNote,
  onReviewNoteChange,
  onConfirmReview,
}: {
  detail: BreakGlassDTO
  canAdmin: boolean
  humanReviewBlocked: boolean
  selfReviewBlocked: boolean
  reviewNote: string
  onReviewNoteChange: (note: string) => void
  onConfirmReview: () => void
}) {
  const { t } = useTranslation('governance')
  const isActive = detail.status === 'active'
  return (
    <section
      aria-labelledby="break-glass-review"
      className="flex flex-col gap-3 rounded-lg border border-warning-line bg-warning-soft/60 p-3"
    >
      <div>
        <h3
          id="break-glass-review"
          className="text-sm font-semibold text-foreground"
        >
          {t('breakGlass.reviewPending')}
        </h3>
        <p className="mt-1 text-xs text-muted-foreground">
          {detail.use_count > 0
            ? t('breakGlass.useReviewPendingHint', {
                count: detail.use_count,
              })
            : t('breakGlass.reviewPendingHint')}
        </p>
      </div>

      {isActive ? (
        <p role="status" className="text-sm text-foreground">
          {t('breakGlass.reviewActiveBlocked')}
        </p>
      ) : humanReviewBlocked || selfReviewBlocked ? (
        <div className="flex flex-col gap-2">
          <p role="status" className="text-sm text-foreground">
            {humanReviewBlocked
              ? t('breakGlass.reviewHumanBlocked')
              : t('breakGlass.reviewSelfBlocked')}
          </p>
          {canAdmin && (
            <Button variant="secondary" size="sm" disabled>
              {t('breakGlass.reviewButton')}
            </Button>
          )}
        </div>
      ) : canAdmin ? (
        <>
          <Field
            label={t('breakGlass.reviewNoteInput')}
            htmlFor="break-glass-review-note"
            description={t('breakGlass.reviewNoteHint')}
            required
          >
            <Textarea
              id="break-glass-review-note"
              value={reviewNote}
              onChange={(event) => onReviewNoteChange(event.target.value)}
              maxLength={MAX_REVIEW_NOTE_LENGTH}
              rows={3}
            />
          </Field>
          <div className="flex justify-end">
            <Button
              variant="primary"
              size="sm"
              disabled={reviewNote.trim().length === 0}
              onClick={onConfirmReview}
            >
              {t('breakGlass.reviewButton')}
            </Button>
          </div>
        </>
      ) : null}
    </section>
  )
}

function UseTrail({
  items,
  loading,
  error,
  onRetry,
}: {
  items: BreakGlassUseDTO[]
  loading: boolean
  error: unknown
  onRetry: () => void
}) {
  const { t } = useTranslation('governance')
  if (loading) return <Skeleton className="h-32 w-full" />
  // Cuarta decisión: el rastro de uso del grant. Mismo motivo, mismo orden.
  if (error instanceof ApiError && error.isStepUpRequired) {
    return <StepUpRequiredState action="generic" onElevated={onRetry} />
  }
  if (error instanceof ApiError && error.isForbidden) {
    return (
      <ForbiddenState
        title={t('breakGlass.forbiddenTitle')}
        description={t('breakGlass.forbiddenBody')}
      />
    )
  }
  if (error) return <ErrorState retry={onRetry} />
  if (items.length === 0) {
    return <EmptyState title={t('breakGlass.trailEmpty')} />
  }

  return (
    <div className="overflow-x-auto rounded-lg border border-border">
      <table className="w-full text-sm" aria-label={t('breakGlass.trailLabel')}>
        <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
          <tr>
            <th scope="col" className="px-3 py-2 font-medium">
              {t('breakGlass.useAction')}
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              {t('breakGlass.useSubject')}
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              {t('breakGlass.usedBy')}
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              {t('breakGlass.usedAt')}
            </th>
          </tr>
        </thead>
        <tbody>
          {items.map((use, index) => (
            <tr
              key={`${use.action}:${use.used_at ?? index}`}
              className="border-t border-border align-top"
            >
              <td className="px-3 py-2 font-mono text-xs text-foreground">
                {use.action}
              </td>
              <td className="px-3 py-2">
                {use.subject_kind || use.subject_ref ? (
                  <span className="flex flex-wrap items-center gap-1.5">
                    {use.subject_kind && (
                      <Badge variant="neutral">{use.subject_kind}</Badge>
                    )}
                    {use.subject_ref && (
                      <span className="font-mono text-xs text-muted-foreground">
                        {use.subject_ref}
                      </span>
                    )}
                  </span>
                ) : (
                  <span className="text-muted-foreground">
                    {t('breakGlass.notAvailable')}
                  </span>
                )}
              </td>
              <td className="px-3 py-2 font-mono text-xs text-muted-foreground">
                {use.used_by ?? t('breakGlass.notAvailable')}
              </td>
              <td className="px-3 py-2 text-xs text-muted-foreground">
                <RelTimeLabel ts={use.used_at} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function ScopeLabel({ matchAction }: { matchAction?: string }) {
  const { t } = useTranslation('governance')
  if (!matchAction || matchAction === '*') return t('breakGlass.scopeAll')
  if (matchAction.endsWith('*')) {
    return t('breakGlass.scopePrefix', { scope: matchAction })
  }
  return t('breakGlass.scopeExact', { scope: matchAction })
}

function BreakGlassStatusBadge({ status }: { status: BreakGlassStatus }) {
  const { t } = useTranslation('governance')
  const variants: Record<string, BadgeVariant> = {
    active: 'danger',
    revoked: 'neutral',
    expired: 'warning',
  }
  return (
    <Badge variant={variants[status] ?? 'neutral'}>
      {t(`breakGlass.statusValue.${status}`, {
        defaultValue: t('breakGlass.statusValue.unknown'),
      })}
    </Badge>
  )
}

function WindowLabel({ grant }: { grant: BreakGlassDTO }) {
  const { t } = useTranslation('governance')
  if (grant.status === 'revoked') {
    return (
      <span className="flex flex-col gap-0.5">
        <span>{t('breakGlass.revokedAt')}</span>
        <RelTimeLabel ts={grant.revoked_at} />
      </span>
    )
  }
  return (
    <span className="flex flex-col gap-0.5">
      <span>
        {grant.status === 'active'
          ? t('breakGlass.expiresAt')
          : t('breakGlass.expiredAt')}
      </span>
      <RelTimeLabel ts={grant.expires_at} />
    </span>
  )
}
