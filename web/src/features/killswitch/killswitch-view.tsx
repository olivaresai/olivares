// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ListTruncationBadge } from '@/features/_intel'
import { CircleCheck, Download, OctagonAlert, OctagonX } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { PageHeader } from '@/components/ui/page-header'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { toast } from '@/components/ui/toaster'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { useAuth } from '@/lib/auth/context'
import { RelTime } from '@/features/shared/rel-time'
import { killswitchApi, killswitchKeys } from './api'
import { StopScopeCell, StopSourceCell, StopStateBadge } from './components'
import { EmergencyStopCard } from './engage-card'
import { downloadEvidencePack, evidenceFilename } from './evidence'
import { GuardianSection } from './guardian'
import { ReenableDialog } from './reenable-dialog'
import { ReviewDialog } from './review-dialog'
import './i18n'
import { KILL_SWITCH_STATUSES } from './types'
import type { KillSwitchDTO, KillSwitchStateDTO } from './types'

// The live posture + stop list poll (no SSE — the stop must read fresh during an
// incident; same cadence as the governance approval queue).
// El techo real del motor (`maxLimit` en sqlstore/generic.go).
const KILLSWITCH_PAGE = 1000

const KILLSWITCH_POLL_MS = 12_000

/**
 * KillswitchView (registry id 'killswitch') is the estate emergency-stop console:
 * the live stop posture, the one-click engage (deliberately cheap server-side),
 * the dual-control re-enable loop, the forced post-review, the downloadable
 * incident evidence pack and the guardian containment rules. The web is a thin
 * client — every gate decision, floor and sentinel lives in the engine
 * (modules/governance/killswitch.go); this view renders DTOs and dispatches
 * privileged, audited, confirmed operations.
 */
export default function KillswitchView() {
  const { t } = useTranslation(['killswitch', 'common'])
  const { activeTenant, can } = useAuth()
  const canAdmin = can('governance:killswitch:admin')
  const canGuardian = can('governance:guardian:read')

  const stateQ = useQuery({
    queryKey: killswitchKeys.state(activeTenant),
    queryFn: () => killswitchApi.state(),
    refetchInterval: KILLSWITCH_POLL_MS,
  })

  return (
    <div className="flex flex-col gap-5 pb-10">
      <PageHeader
        title={t('title')}
        description={t('subtitle')}
        icon={OctagonAlert}
      />

      {stateQ.data && <StateBanner state={stateQ.data} />}

      {canAdmin && <EmergencyStopCard />}

      <StopsSection canAdmin={canAdmin} />

      {canGuardian && <GuardianSection />}
    </div>
  )
}

/** The live posture banner — what an operator checks first in an incident. An
 *  estate stop is loud (danger); agent stops are a warning; the calm green line
 *  answers "is anything stopped?" at a glance. */
function StateBanner({ state }: { state: KillSwitchStateDTO }) {
  const { t } = useTranslation('killswitch')
  const estateStop = state.active.find((s) => s.scope_kind === 'estate')
  const agentStops = state.active.filter((s) => s.scope_kind === 'agent')

  if (state.estate_stopped && estateStop) {
    return (
      <div
        role="alert"
        className="flex flex-col gap-1.5 rounded-lg border border-danger-line bg-danger-soft p-4"
      >
        <p className="flex items-center gap-2 font-semibold text-danger">
          <OctagonX className="size-4 shrink-0" aria-hidden />
          {t('banner.estateTitle')}
        </p>
        <p className="text-sm text-foreground">{t('banner.estateBody')}</p>
        <p className="text-xs text-muted-foreground">
          {t('banner.engagedBy')}{' '}
          <span className="font-mono">{estateStop.engaged_by || '—'}</span>
          {' · '}
          <RelTime ts={estateStop.engaged_at} />
          {estateStop.reason ? ` · ${estateStop.reason}` : ''}
          {' · '}
          {t('banner.revoked', { count: estateStop.revoked_approvals })}
        </p>
      </div>
    )
  }

  if (agentStops.length > 0) {
    return (
      <div
        role="alert"
        className="flex flex-col gap-1.5 rounded-lg border border-warning-line bg-warning-soft p-4"
      >
        <p className="flex items-center gap-2 font-medium text-warning">
          <OctagonAlert className="size-4 shrink-0" aria-hidden />
          {t('banner.agentCount', { count: agentStops.length })}
        </p>
        <p className="text-xs text-muted-foreground">{t('banner.agentBody')}</p>
      </div>
    )
  }

  return (
    <p className="flex items-center gap-2 text-sm text-muted-foreground">
      <CircleCheck className="size-4 shrink-0 text-success" aria-hidden />
      {t('banner.none')}
    </p>
  )
}

function StopsSection({ canAdmin }: { canAdmin: boolean }) {
  const { t } = useTranslation(['killswitch', 'common', 'errors'])
  const report = useFailedActionReporter()
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()

  const [statusFilter, setStatusFilter] = useState<string>('__all__')
  const [reenableStop, setReenableStop] = useState<KillSwitchDTO | null>(null)
  const [reviewStop, setReviewStop] = useState<KillSwitchDTO | null>(null)

  // ⛔ EL TECHO SE PIDE Y EL RECORTE SE DICE. Sin `limit` el repositorio genérico pagina a
  //    100 y `handleListKillSwitch` devuelve un `has_more` que nadie miraba: la pantalla
  //    enseñaba las primeras cien paradas y se leía «esto es lo que hay congelado», que en
  //    un kill switch es la afirmación con la que alguien decide que puede seguir.
  const params =
    statusFilter === '__all__'
      ? { limit: KILLSWITCH_PAGE }
      : { status: statusFilter, limit: KILLSWITCH_PAGE }
  const stops = useQuery({
    queryKey: killswitchKeys.stops(activeTenant, params),
    queryFn: () => killswitchApi.list(params),
    refetchInterval: KILLSWITCH_POLL_MS,
  })

  // The evidence pack is fetched as JSON and saved via a blob download — the
  // export itself is a privileged, self-audited read server-side.
  const evidence = useMutation({
    mutationFn: (stop: KillSwitchDTO) => killswitchApi.evidence(stop.id),
    onSuccess: (pack, stop) => {
      downloadEvidencePack(stop.id, pack)
      // ⛔ EL PAQUETE PUEDE VENIR ACOTADO, y las banderas que lo dicen viajan DENTRO del JSON
      //    (`modules/governance/killswitch_evidence.go:117-118`: `timeline_truncated`,
      //    `findings_truncated`). Quien no abra la cabecera del fichero se lleva una cronología
      //    de incidente **creyéndola completa** — y la completitud es justo lo que se le pide a
      //    una evidencia. El aviso no reemplaza al fichero: lo dice en el momento en que se
      //    descarga, que es cuando el lector decide si necesita pedir más.
      const acotado = pack.timeline_truncated || pack.findings_truncated
      if (acotado) {
        toast.warning(t('evidence.truncated'), {
          description: t('evidence.truncatedBody', {
            file: evidenceFilename(stop.id),
          }),
        })
        return
      }
      toast.success(t('evidence.done'), {
        description: t('evidence.doneBody', {
          file: evidenceFilename(stop.id),
        }),
      })
    },
    onError: (err, stop) => {
      report(err, () => evidence.mutate(stop))
    },
  })

  // Memoized so the cell renderers keep their identity across re-renders (four
  // queries settle on this page; fresh inline cells would remount every cell on
  // each settle — flexRender treats a new function as a new component type).
  const { mutate: downloadEvidence, isPending: evidencePending } = evidence
  const columns = useMemo<TableColumn<KillSwitchDTO, unknown>[]>(
    () => [
      {
        id: 'scope',
        header: t('stops.scope'),
        cell: ({ row }) => <StopScopeCell stop={row.original} />,
      },
      {
        accessorKey: 'reason',
        header: t('stops.reason'),
        cell: ({ row }) => (
          <span
            className="truncate text-xs text-muted-foreground"
            title={row.original.reason || undefined}
          >
            {row.original.reason || '—'}
          </span>
        ),
      },
      {
        id: 'source',
        header: t('stops.source'),
        cell: ({ row }) => <StopSourceCell stop={row.original} />,
      },
      {
        id: 'engaged',
        header: t('stops.engaged'),
        // MEDIDO: 349 px para TRES cosas —un ULID de 26 caracteres, la hora relativa
        // y la insignia de AAL— salía en dos líneas. El actor trunca con su valor en
        // el `title`, la hora no rompe nunca, y la columna pide el ancho que necesita.
        size: 300,
        cell: ({ row }) => (
          // ⛔ TRES valores en una celda, y cada uno rompia por su cuenta: el ULID de
          // `engaged_by` salia en CUATRO lineas y la hora relativa en TRES. El actor
          // corta solo por sus separadores (`user:01J8...` rompe tras los dos puntos y
          // en ningun otro sitio) y la hora NUNCA rompe, que es la regla del informe
          // para columnas de tiempo.
          <span className="flex min-w-0 items-center gap-1.5">
            <span className="min-w-0 truncate font-mono text-xs text-muted-foreground">
              {row.original.engaged_by || '—'}
            </span>
            <RelTime
              ts={row.original.engaged_at}
              className="shrink-0 whitespace-nowrap text-xs text-muted-foreground"
            />
            {row.original.source === 'operator' && (
              <Badge variant="outline" title={t('stops.aalHint')}>
                {t('stops.aalShort', { aal: row.original.engaged_aal })}
              </Badge>
            )}
          </span>
        ),
      },
      {
        accessorKey: 'revoked_approvals',
        header: t('stops.revoked'),
        cell: ({ row }) => (
          <span
            className="font-mono tabular-nums text-xs"
            title={t('stops.revokedHint')}
          >
            {row.original.revoked_approvals}
          </span>
        ),
      },
      {
        accessorKey: 'status',
        header: t('stops.status'),
        cell: ({ row }) => (
          <span className="flex items-center gap-1.5">
            <StopStateBadge stop={row.original} />
            {row.original.status === 'active' &&
              row.original.reenable_approval && (
                <Badge variant="info">{t('stops.pendingReenable')}</Badge>
              )}
          </span>
        ),
      },
      {
        id: 'actions',
        header: '',
        cell: ({ row }) => {
          if (!canAdmin) return null
          const stop = row.original
          const isActive = stop.status === 'active'
          const reviewDue = stop.status === 'reenabled' && !stop.reviewed
          return (
            <div className="flex items-center justify-end gap-1">
              {isActive && (
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={(e) => {
                    e.stopPropagation()
                    setReenableStop(stop)
                  }}
                >
                  {t('stops.reenable')}
                </Button>
              )}
              {reviewDue && (
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={(e) => {
                    e.stopPropagation()
                    setReviewStop(stop)
                  }}
                >
                  {t('stops.review')}
                </Button>
              )}
              <Button
                variant="ghost"
                size="icon"
                aria-label={t('evidence.download')}
                title={t('evidence.download')}
                disabled={evidencePending}
                onClick={(e) => {
                  e.stopPropagation()
                  downloadEvidence(stop)
                }}
              >
                <Download />
              </Button>
            </div>
          )
        },
      },
    ],
    [t, canAdmin, downloadEvidence, evidencePending],
  )

  return (
    <div className="flex flex-col gap-3">
      <div>
        <h2 className="text-base font-medium">{t('stops.title')}</h2>
        <p className="text-xs text-muted-foreground">{t('stops.caption')}</p>
      </div>

      {/* Fuera del bloque de datos a propósito: si un refetch falla, el aviso no debe
          quedarse flotando sobre una tabla que ya sólo enseña el error. */}
      <ListTruncationBadge
        query={stops}
        label={t('stops.truncated', { n: KILLSWITCH_PAGE })}
        hint={t('stops.truncatedHint')}
      />

      <DataTable
        columns={columns}
        data={stops.data?.items ?? []}
        isLoading={stops.isLoading}
        error={stops.error}
        onRetry={() => stops.refetch()}
        searchable
        searchPlaceholder={t('stops.search')}
        getRowId={(r) => r.id}
        label={t('stops.label')}
        empty={
          <EmptyState
            icon={<OctagonAlert />}
            title={t('stops.empty')}
            description={t('stops.emptyHint')}
          />
        }
        toolbar={
          <Select value={statusFilter} onValueChange={setStatusFilter}>
            <SelectTrigger
              className="w-40"
              aria-label={t('stops.filterStatus')}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="__all__">{t('stops.statusAll')}</SelectItem>
              {KILL_SWITCH_STATUSES.map((s) => (
                <SelectItem key={s} value={s}>
                  {t(`status.${s}`, { defaultValue: s })}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        }
      />

      {reenableStop && (
        <ReenableDialog
          stop={reenableStop}
          open={!!reenableStop}
          onOpenChange={(o) => {
            if (!o) {
              setReenableStop(null)
              void queryClient.invalidateQueries({
                queryKey: killswitchKeys.all(activeTenant),
              })
            }
          }}
        />
      )}

      {reviewStop && (
        <ReviewDialog
          stop={reviewStop}
          open={!!reviewStop}
          onOpenChange={(o) => !o && setReviewStop(null)}
        />
      )}
    </div>
  )
}
