// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// NHI lifecycle posture and working set. This surface intentionally does not
// reuse the identity roster: inventory answers what exists; lifecycle answers
// whether an NHI is stale, governed, blocked, owned or being offboarded.
import { useQuery } from '@tanstack/react-query'
import { ShieldCheck } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import {
  MetricStat,
  SectionCard,
  SelfAuditNotice,
  StatGrid,
} from '@/features/_intel'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { identityApi, identityKeys } from './api'
import { NhiSweepAction } from './nhi-actions'
import { NhiDetailSheet } from './nhi-detail'
import {
  criticalityVariant,
  enforcementVariant,
  offboardVariant,
  stalenessVariant,
} from './nhi-status'
import type { NhiLifecycleDTO } from './types'

const CRITICALITIES = ['low', 'medium', 'high', 'critical'] as const
const ENFORCEMENTS = ['monitor', 'alert', 'blocked'] as const

const PAGE_SIZE = 200

export function NhiLifecycleTab() {
  const { t } = useTranslation('identity')
  const { activeTenant, can } = useAuth()
  const canRead = can('governance:nhi:read')
  const [criticality, setCriticality] = useState('all')
  const [enforcement, setEnforcement] = useState('all')
  const [selectedRef, setSelectedRef] = useState<string | null>(null)
  // Filter SERVER-side: handleListNHI parses enforcement= and offboard_state=,
  // and the store pages at 1000. Filtering a single hardcoded page in the
  // browser silently hid identities beyond it — while the posture tiles above,
  // which drain every page, kept counting the whole estate. An operator would
  // read "12 blocked" and find eight in the table, with nothing saying why.
  const params = useMemo(
    () => ({
      limit: PAGE_SIZE,
      ...(enforcement === 'all' ? {} : { enforcement }),
    }),
    [enforcement],
  )

  const posture = useQuery({
    queryKey: identityKeys.nhiPosture(activeTenant),
    queryFn: () => identityApi.nhiPosture(),
    enabled: canRead,
    retry: false,
  })
  const lifecycle = useQuery({
    queryKey: identityKeys.nhi(activeTenant, params),
    queryFn: () => identityApi.nhiLifecycle(params),
    enabled: canRead,
    retry: false,
  })

  // Criticality has no server-side filter, so it stays client-side over the
  // page the server returned.
  const rows = useMemo(
    () =>
      (lifecycle.data?.items ?? []).filter(
        (row) => criticality === 'all' || row.criticality === criticality,
      ),
    [criticality, lifecycle.data],
  )
  // Never let the table imply it shows everything when it does not.
  const truncated = lifecycle.data?.has_more ?? false

  // ⛔ ASEGURAMIENTO ANTES QUE ROL, y con un cuidado que aquí importa: `!canRead` es una
  // puerta de CAPACIDAD —un booleano del cliente, no una respuesta del motor— y NO se toca:
  // convertirla en ceremonia sería el defecto simétrico. Lo que se separa es la negativa que
  // VIENE DEL MOTOR: `isForbidden` es sólo el status (lib/api/errors.ts:59) y un
  // `step_up_required` lo satisface también, así que resumir las dos lecturas en un solo
  // booleano acusaba al operador de un permiso que sí tiene.
  // ⛔ Y `canRead` MANDA SOBRE LA CEREMONIA, que es la mitad que me dejé: al poner el
  // aseguramiento delante sin más, un permiso de cliente que pasa a FALSO con un error de
  // step-up todavía en caché quedaba tapado — se le ofrecía la ceremonia a quien la puerta
  // de capacidad ya niega. Antes `!canRead` ganaba, y tiene que seguir ganando.
  const posturaStepUp =
    posture.error instanceof ApiError && posture.error.isStepUpRequired
  const lifecycleStepUp =
    lifecycle.error instanceof ApiError && lifecycle.error.isStepUpRequired
  const stepUp = canRead && (posturaStepUp || lifecycleStepUp)
  if (stepUp) {
    return (
      <StepUpRequiredState
        action="generic"
        // Y se repite SÓLO lo que se refusó: reintentar la lectura que respondió 200 es
        // trabajo que nadie pidió (components/layout/step-up-state.tsx:35-38).
        onElevated={() => {
          if (posturaStepUp) void posture.refetch()
          if (lifecycleStepUp) void lifecycle.refetch()
        }}
      />
    )
  }
  const forbidden =
    !canRead ||
    (posture.error instanceof ApiError && posture.error.isForbidden) ||
    (lifecycle.error instanceof ApiError && lifecycle.error.isForbidden)
  if (forbidden) {
    return (
      <ForbiddenState
        title={t('lifecycle.forbidden.title')}
        description={t('lifecycle.forbidden.description')}
      />
    )
  }

  return (
    <div className="flex flex-col gap-4">
      <SelfAuditNotice />
      <SectionCard
        title={t('lifecycle.posture.title')}
        description={t('lifecycle.posture.description')}
        actions={<NhiSweepAction />}
      >
        {posture.isLoading ? (
          <div className="flex items-center gap-2 py-6 text-sm text-muted-foreground">
            <Spinner size="sm" aria-hidden />
            {t('lifecycle.loading')}
          </div>
        ) : posture.error ? (
          <ErrorState retry={() => void posture.refetch()} />
        ) : posture.data ? (
          <StatGrid className="lg:grid-cols-6">
            <MetricStat
              label={t('lifecycle.posture.total')}
              value={posture.data.total}
              icon={<ShieldCheck />}
            />
            <MetricStat
              label={t('lifecycle.posture.stale')}
              value={posture.data.stale}
              tone={posture.data.stale > 0 ? 'warning' : 'default'}
            />
            <MetricStat
              label={t('lifecycle.posture.blocked')}
              value={posture.data.blocked}
              tone={posture.data.blocked > 0 ? 'danger' : 'default'}
            />
            <MetricStat
              label={t('lifecycle.posture.alerting')}
              value={posture.data.alerting}
              tone={posture.data.alerting > 0 ? 'warning' : 'default'}
            />
            <MetricStat
              label={t('lifecycle.posture.orphaned')}
              value={posture.data.orphaned}
              tone={posture.data.orphaned > 0 ? 'warning' : 'default'}
            />
            <MetricStat
              label={t('lifecycle.posture.critical')}
              value={posture.data.critical}
            />
          </StatGrid>
        ) : null}
      </SectionCard>

      <SectionCard
        title={t('lifecycle.table.title')}
        description={t('lifecycle.table.description')}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Select value={criticality} onValueChange={setCriticality}>
              <SelectTrigger
                className="w-40"
                aria-label={t('lifecycle.filters.criticality')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">
                  {t('lifecycle.filters.allCriticality')}
                </SelectItem>
                {CRITICALITIES.map((value) => (
                  <SelectItem key={value} value={value}>
                    {t(`lifecycle.criticality.${value}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select value={enforcement} onValueChange={setEnforcement}>
              <SelectTrigger
                className="w-40"
                aria-label={t('lifecycle.filters.enforcement')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">
                  {t('lifecycle.filters.allEnforcement')}
                </SelectItem>
                {ENFORCEMENTS.map((value) => (
                  <SelectItem key={value} value={value}>
                    {t(`lifecycle.enforcement.${value}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        }
        noPadding
      >
        {lifecycle.isLoading ? (
          <div className="flex items-center gap-2 p-6 text-sm text-muted-foreground">
            <Spinner size="sm" aria-hidden />
            {t('lifecycle.loading')}
          </div>
        ) : lifecycle.error ? (
          <ErrorState retry={() => void lifecycle.refetch()} />
        ) : rows.length === 0 ? (
          <EmptyState
            title={t('lifecycle.empty.title')}
            description={t('lifecycle.empty.description')}
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-sm">
              <thead>
                <tr className="border-b border-border-strong bg-muted text-left text-xs tracking-wide text-muted-foreground uppercase">
                  <th scope="col" className="px-3 py-2 font-medium">
                    {t('lifecycle.table.identity')}
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    {t('lifecycle.table.criticality')}
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    {t('lifecycle.table.staleness')}
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    {t('lifecycle.table.enforcement')}
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    {t('lifecycle.table.ownership')}
                  </th>
                  <th scope="col" className="px-3 py-2 font-medium">
                    {t('lifecycle.table.offboarding')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <LifecycleRow
                    key={row.identity_ref}
                    row={row}
                    onOpen={() => setSelectedRef(row.identity_ref)}
                  />
                ))}
              </tbody>
            </table>
            {truncated && (
              <p
                role="note"
                className="border-t border-border px-3 py-2 text-xs text-muted-foreground"
              >
                {t('lifecycle.table.truncated', { count: PAGE_SIZE })}
              </p>
            )}
          </div>
        )}
      </SectionCard>
      <NhiDetailSheet
        identityRef={selectedRef}
        open={selectedRef != null}
        onOpenChange={(open) => !open && setSelectedRef(null)}
      />
    </div>
  )
}

function LifecycleRow({
  row,
  onOpen,
}: {
  row: NhiLifecycleDTO
  onOpen: () => void
}) {
  const { t } = useTranslation('identity')
  return (
    <tr className="border-b border-border last:border-0 hover:bg-muted/60">
      <td className="px-3 py-2">
        <button
          type="button"
          onClick={onOpen}
          className="rounded-sm font-mono text-xs font-medium text-accent-text outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring"
          aria-label={t('lifecycle.table.openDetail', {
            ref: row.identity_ref,
          })}
        >
          {row.identity_ref}
        </button>
        {row.source && (
          <span className="mt-0.5 block text-xs text-muted-foreground">
            {row.source}
          </span>
        )}
      </td>
      <td className="px-3 py-2">
        <Badge variant={criticalityVariant(row.criticality)}>
          {t(`lifecycle.criticality.${row.criticality}`, {
            defaultValue: t('lifecycle.unknown'),
          })}
        </Badge>
      </td>
      <td className="px-3 py-2">
        <Badge variant={stalenessVariant(row.staleness_status)}>
          {t(`lifecycle.staleness.${row.staleness_status}`, {
            defaultValue: t('lifecycle.unknown'),
          })}
        </Badge>
      </td>
      <td className="px-3 py-2">
        <Badge variant={enforcementVariant(row.enforcement)}>
          {t(`lifecycle.enforcement.${row.enforcement}`, {
            defaultValue: t('lifecycle.unknown'),
          })}
        </Badge>
        {row.enforcement_reason && (
          <span className="mt-1 block max-w-56 text-xs text-muted-foreground">
            {row.enforcement_reason}
          </span>
        )}
      </td>
      <td className="px-3 py-2 text-xs">
        <span className="block">
          {t('lifecycle.table.ownerValue', {
            ref: row.owner_ref ?? t('lifecycle.notSet'),
          })}
        </span>
        <span className="block text-muted-foreground">
          {t('lifecycle.table.sponsorValue', {
            ref: row.sponsor_ref ?? t('lifecycle.notSet'),
          })}
        </span>
        {row.orphaned && (
          <Badge variant="danger" className="mt-1">
            {t('lifecycle.orphaned')}
          </Badge>
        )}
      </td>
      <td className="px-3 py-2">
        <Badge variant={offboardVariant(row.offboard_state)}>
          {t(`lifecycle.offboard.${row.offboard_state}`, {
            defaultValue: t('lifecycle.unknown'),
          })}
        </Badge>
      </td>
    </tr>
  )
}
