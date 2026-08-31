// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Detail and append-only event trail for one governed non-human identity.
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { KvList, KvRow } from '@/components/ui/kv'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Spinner } from '@/components/ui/spinner'
import { SectionCard, SelfAuditNotice } from '@/features/_intel'
import { RelTimeLabel } from '@/features/shared'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { identityApi, identityKeys } from './api'
import { NhiActions } from './nhi-actions'
import {
  criticalityVariant,
  enforcementVariant,
  offboardVariant,
  stalenessVariant,
} from './nhi-status'

export function NhiDetailSheet({
  identityRef,
  open,
  onOpenChange,
}: {
  identityRef: string | null
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation('identity')
  const { activeTenant } = useAuth()
  const ref = identityRef ?? ''
  const detail = useQuery({
    queryKey: identityKeys.nhiDetail(activeTenant, ref),
    queryFn: () => identityApi.nhiDetail(ref),
    enabled: open && ref !== '',
    retry: false,
  })
  const events = useQuery({
    queryKey: identityKeys.nhiEvents(activeTenant, ref),
    queryFn: () => identityApi.nhiEvents(ref),
    enabled: open && ref !== '',
    retry: false,
  })

  // ⛔ Este panel resume DOS lecturas en un solo booleano, y por eso el defecto era doble:
  // `isForbidden` es sólo el status (lib/api/errors.ts:59), así que un `step_up_required`
  // de CUALQUIERA de las dos se pintaba como «tu rol no alcanza». El gemelo va DELANTE y
  // se calcula igual: si una sola de las dos pide ceremonia, la hoja la ofrece.
  const stepUp =
    (detail.error instanceof ApiError && detail.error.isStepUpRequired) ||
    (events.error instanceof ApiError && events.error.isStepUpRequired)
  const forbidden =
    !stepUp &&
    ((detail.error instanceof ApiError && detail.error.isForbidden) ||
      (events.error instanceof ApiError && events.error.isForbidden))

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="overflow-y-auto sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle className="font-mono text-base break-all">
            {identityRef}
          </SheetTitle>
          <SheetDescription>
            {t('lifecycle.detail.description')}
          </SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-4 px-4 pb-6">
          <SelfAuditNotice />
          {stepUp ? (
            <StepUpRequiredState
              action="generic"
              onElevated={() => {
                void detail.refetch()
                void events.refetch()
              }}
            />
          ) : forbidden ? (
            <ForbiddenState
              title={t('lifecycle.forbidden.title')}
              description={t('lifecycle.forbidden.description')}
            />
          ) : detail.isLoading ? (
            <div className="flex items-center gap-2 py-8 text-sm text-muted-foreground">
              <Spinner size="sm" aria-hidden />
              {t('lifecycle.loading')}
            </div>
          ) : detail.error ? (
            <ErrorState retry={() => void detail.refetch()} />
          ) : detail.data ? (
            <>
              <SectionCard title={t('lifecycle.detail.posture')}>
                <KvList>
                  <KvRow label={t('lifecycle.table.criticality')}>
                    <Badge
                      variant={criticalityVariant(detail.data.criticality)}
                    >
                      {t(`lifecycle.criticality.${detail.data.criticality}`, {
                        defaultValue: t('lifecycle.unknown'),
                      })}
                    </Badge>
                  </KvRow>
                  <KvRow label={t('lifecycle.table.staleness')}>
                    <Badge
                      variant={stalenessVariant(detail.data.staleness_status)}
                    >
                      {t(
                        `lifecycle.staleness.${detail.data.staleness_status}`,
                        {
                          defaultValue: t('lifecycle.unknown'),
                        },
                      )}
                    </Badge>
                  </KvRow>
                  <KvRow label={t('lifecycle.table.enforcement')}>
                    <Badge
                      variant={enforcementVariant(detail.data.enforcement)}
                    >
                      {t(`lifecycle.enforcement.${detail.data.enforcement}`, {
                        defaultValue: t('lifecycle.unknown'),
                      })}
                    </Badge>
                  </KvRow>
                  <KvRow label={t('lifecycle.detail.enforcementReason')}>
                    {detail.data.enforcement_reason ?? t('lifecycle.notSet')}
                  </KvRow>
                  <KvRow label={t('lifecycle.detail.owner')} mono align="start">
                    {detail.data.owner_ref ?? t('lifecycle.notSet')}
                  </KvRow>
                  <KvRow
                    label={t('lifecycle.detail.sponsor')}
                    mono
                    align="start"
                  >
                    {detail.data.sponsor_ref ?? t('lifecycle.notSet')}
                  </KvRow>
                  <KvRow label={t('lifecycle.detail.rotatedAt')}>
                    <RelTimeLabel ts={detail.data.rotated_at} />
                  </KvRow>
                  <KvRow label={t('lifecycle.detail.maxAge')}>
                    {detail.data.max_age_seconds
                      ? t('lifecycle.detail.seconds', {
                          count: detail.data.max_age_seconds,
                        })
                      : t('lifecycle.detail.inherited')}
                  </KvRow>
                  <KvRow
                    label={t('lifecycle.detail.rotationTarget')}
                    mono
                    align="start"
                  >
                    {detail.data.rotation_target ?? t('lifecycle.notSet')}
                  </KvRow>
                  <KvRow label={t('lifecycle.table.offboarding')}>
                    <Badge
                      variant={offboardVariant(detail.data.offboard_state)}
                    >
                      {t(`lifecycle.offboard.${detail.data.offboard_state}`, {
                        defaultValue: t('lifecycle.unknown'),
                      })}
                    </Badge>
                  </KvRow>
                  <KvRow label={t('lifecycle.detail.recoveryUntil')}>
                    <RelTimeLabel ts={detail.data.recovery_until} />
                  </KvRow>
                </KvList>
              </SectionCard>
              <SectionCard title={t('lifecycle.actions.title')}>
                <NhiActions identity={detail.data} />
              </SectionCard>
            </>
          ) : null}

          {/* ⛔ `!stepUp` TAMBIÉN, y ésta es la mitad que me faltaba: al forzar `forbidden=false`
            cuando hay ceremonia, este bloque —condicionado sólo por `!forbidden`— volvía a
            aparecer, y si el `step_up_required` venía de ESTA query caía además en el
            `ErrorState` genérico. La hoja pintaba la ceremonia Y una avería roja para UNA
            sola negativa. Lo encontró el contraste `sol max`, no mis celdas: mi arreglo del
            flag creó una tercera composición y no la probé. */}
          {!forbidden && !stepUp && (
            <SectionCard
              title={t('lifecycle.events.title')}
              description={t('lifecycle.events.description')}
              noPadding
            >
              {events.isLoading ? (
                <div className="flex items-center gap-2 p-6 text-sm text-muted-foreground">
                  <Spinner size="sm" aria-hidden />
                  {t('lifecycle.loading')}
                </div>
              ) : events.error ? (
                <ErrorState retry={() => void events.refetch()} />
              ) : (events.data?.items.length ?? 0) === 0 ? (
                <p className="p-6 text-sm text-muted-foreground">
                  {t('lifecycle.events.empty')}
                </p>
              ) : (
                <div className="overflow-x-auto">
                  <table className="w-full border-collapse text-sm">
                    <thead>
                      <tr className="border-b border-border-strong bg-muted text-left text-xs tracking-wide text-muted-foreground uppercase">
                        <th scope="col" className="px-3 py-2 font-medium">
                          {t('lifecycle.events.event')}
                        </th>
                        <th scope="col" className="px-3 py-2 font-medium">
                          {t('lifecycle.events.actor')}
                        </th>
                        <th scope="col" className="px-3 py-2 font-medium">
                          {t('lifecycle.events.detail')}
                        </th>
                        <th scope="col" className="px-3 py-2 font-medium">
                          {t('lifecycle.events.when')}
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {events.data?.items.map((event, index) => (
                        <tr
                          key={`${event.occurred_at}-${event.event}-${index}`}
                          className="border-b border-border last:border-0"
                        >
                          <td className="px-3 py-2">
                            <Badge variant="outline">{event.event}</Badge>
                          </td>
                          <td className="px-3 py-2 font-mono text-xs">
                            {event.actor}
                          </td>
                          <td className="max-w-64 px-3 py-2 text-xs text-muted-foreground">
                            {event.detail ?? t('lifecycle.notSet')}
                          </td>
                          <td className="px-3 py-2 whitespace-nowrap">
                            <RelTimeLabel ts={event.occurred_at} />
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </SectionCard>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}
