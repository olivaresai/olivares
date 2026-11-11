// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseQueryResult,
} from '@tanstack/react-query'
import { ListTruncationBadge } from '@/features/_intel'
import {
  Ban,
  Download,
  Inbox,
  OctagonAlert,
  OctagonX,
  Power,
  ScrollText,
  Square,
  Users,
} from 'lucide-react'
import { useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { Badge } from '@/components/ui/badge'
import { BoxedList, BoxedListRow } from '@/components/ui/boxed-list'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { EmptyState } from '@/components/ui/empty-state'
import { Notice } from '@/components/ui/notice'
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
import { cn } from '@/lib/utils'
import { RelTime } from '@/features/shared/rel-time'
import { killswitchApi, killswitchKeys } from './api'
import { WRAP_HEADER } from './columns'
import { StopScopeCell, StopSourceCell, StopStateBadge } from './components'
import { StopAgentAction, StopAllAction } from './engage-card'
import { downloadEvidencePack, evidenceFilename } from './evidence'
import { GuardianSection } from './guardian'
import { LiveRunsSection } from './live-runs'
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
 * KillswitchView (registry id 'killswitch') is the estate emergency-stop console: the
 * state block (is anything stopped, with Stop one agent and Stop all agents beside it;
 * stopping is deliberately cheap server-side), what stopping every agent does and who
 * starts it again, the live session list that flips to Stopped, the history with the
 * dual-control re-enable loop, the forced post-review and the downloadable incident
 * evidence pack, and the guardian containment rules. The web is a thin client — every
 * gate decision, floor and sentinel lives in the engine (modules/governance/killswitch.go);
 * this view renders DTOs and dispatches privileged, audited, confirmed operations.
 */
export default function KillswitchView() {
  const { t } = useTranslation(['killswitch', 'common'])
  const { activeTenant, can } = useAuth()
  const canAdmin = can('governance:killswitch:admin')
  const canGuardian = can('governance:guardian:read')
  const canRuns = can('sessions:run:read')

  const stateQ = useQuery({
    queryKey: killswitchKeys.state(activeTenant),
    queryFn: () => killswitchApi.state(),
    refetchInterval: KILLSWITCH_POLL_MS,
  })

  return (
    <div className="flex flex-col gap-6 pb-10">
      {/* The tab names the page: its heading stays for assistive technology, and the
          stops sit in the state block rather than in a header verb. */}
      <PageHeader title={t('title')} titleHidden />

      <StopState
        query={stateQ}
        actions={
          canAdmin ? (
            <>
              <StopAgentAction />
              {/* A failed read closes nothing: an emergency stop never waits
                  on a read, and a stop already on answers 409. */}
              <StopAllAction
                estateStopped={!stateQ.isError && !!stateQ.data?.estate_stopped}
              />
            </>
          ) : undefined
        }
      />

      <StopEffects />

      <Notice icon={<Users />}>
        {t(canAdmin ? 'subtitle' : 'subtitleNoStop')}
      </Notice>

      {/* The stop state says which sessions a stop ended: a failed read marks
          none, and the state block above says it failed, with Retry. */}
      {canRuns && (
        <LiveRunsSection
          active={(!stateQ.isError && stateQ.data?.active) || []}
        />
      )}

      <StopsSection canAdmin={canAdmin} />

      {canGuardian && <GuardianSection />}
    </div>
  )
}

/** The state block: whether anything is stopped, in one title and one sentence, with the
 *  stops beside it. A stop is an alert, its colour on the icon and its words in the title;
 *  the calm state answers "is anything stopped?" at a glance. A read that failed, the
 *  first or a later poll, says so in place of the last answer: a stale "Nothing is
 *  stopped" is the one sentence this page must never show. The stops stay offered while
 *  the state cannot be read: an emergency stop never waits on a read. */
function StopState({
  query,
  actions,
}: {
  query: UseQueryResult<KillSwitchStateDTO>
  actions?: ReactNode
}) {
  const { t } = useTranslation(['killswitch', 'common'])
  const state = query.isError ? undefined : query.data
  const estateStop = state?.estate_stopped
    ? state.active.find((s) => s.scope_kind === 'estate')
    : undefined
  const agentStops =
    state?.active.filter((s) => s.scope_kind === 'agent').length ?? 0
  const Icon = estateStop ? OctagonX : agentStops > 0 ? OctagonAlert : Power
  const tone = !state
    ? 'text-muted-foreground'
    : estateStop
      ? 'text-danger'
      : agentStops > 0
        ? 'text-warning'
        : 'text-success'

  return (
    <Card role="region" aria-labelledby="ks-state-title">
      <CardContent>
        <div className="flex flex-col gap-4 sm:flex-row sm:items-center">
          <div
            aria-hidden
            className="flex size-12 shrink-0 items-center justify-center rounded-card bg-muted"
          >
            <Icon className={cn('size-6', tone)} />
          </div>
          <div
            role={estateStop || agentStops > 0 ? 'alert' : undefined}
            className="min-w-0 flex-1"
          >
            {state ? (
              <>
                <h2
                  id="ks-state-title"
                  className="font-display text-title text-foreground"
                >
                  {estateStop
                    ? t('banner.estateTitle')
                    : agentStops > 0
                      ? t('banner.agentCount', { count: agentStops })
                      : t('banner.noneTitle')}
                </h2>
                <p className="mt-1 text-body text-muted-foreground">
                  {estateStop
                    ? t('banner.estateBody')
                    : agentStops > 0
                      ? t('banner.agentBody')
                      : t('banner.noneBody')}
                </p>
                {estateStop ? (
                  <p className="mt-1 text-caption text-muted-foreground">
                    {t('banner.engagedBy')}{' '}
                    <span className="font-mono">
                      {estateStop.engaged_by || '—'}
                    </span>
                    {' · '}
                    <RelTime ts={estateStop.engaged_at} />
                    {estateStop.reason ? ` · ${estateStop.reason}` : ''}
                    {' · '}
                    {t('banner.revoked', {
                      count: estateStop.revoked_approvals,
                    })}
                  </p>
                ) : null}
              </>
            ) : (
              <>
                <h2 id="ks-state-title" className="sr-only">
                  {t('title')}
                </h2>
                {query.isError ? (
                  <QueryErrorState
                    error={query.error}
                    retry={() => void query.refetch()}
                  />
                ) : (
                  <p role="status" className="text-body text-muted-foreground">
                    {t('common:states.loading')}
                  </p>
                )}
              </>
            )}
          </div>
          {actions ? (
            <div className="flex flex-wrap items-center gap-2">{actions}</div>
          ) : null}
        </div>
      </CardContent>
    </Card>
  )
}

/** What stopping every agent does, in the engine's order: said before anyone stops. */
const EFFECTS = [
  { key: 'sessions', icon: Square },
  { key: 'actions', icon: Ban },
  { key: 'approvals', icon: Inbox },
  { key: 'ledger', icon: ScrollText },
] as const

function StopEffects() {
  const { t } = useTranslation('killswitch')
  return (
    <section className="flex flex-col gap-3">
      <h2 id="ks-effects-title" className="text-heading">
        {t('effects.title')}
      </h2>
      <BoxedList role="list" aria-labelledby="ks-effects-title">
        {EFFECTS.map(({ key, icon: Icon }) => (
          <BoxedListRow key={key} role="listitem">
            <span
              aria-hidden
              className="flex size-8 shrink-0 items-center justify-center rounded-card bg-muted text-muted-foreground"
            >
              <Icon className="size-4" />
            </span>
            <span className="min-w-0">
              <span className="block text-body font-medium text-foreground">
                {t(`effects.${key}`)}
              </span>
              <span className="block text-caption text-muted-foreground">
                {t(`effects.${key}Hint`)}
              </span>
            </span>
          </BoxedListRow>
        ))}
      </BoxedList>
    </section>
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
            className="line-clamp-2 text-caption break-words whitespace-normal text-muted-foreground"
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
        // Measured on the 0.2 binary: actor handle (41 characters), relative time and AAL
        // badge on one line took 501 px, and with the other columns the table was 1540 px
        // wide in a 1140 px container (1440 viewport). The actor now sits over the time
        // and the badge, and truncates at 9 rem with its full value in the `title`; the
        // time never breaks, which is the rule for time columns.
        cell: ({ row }) => (
          <span className="flex flex-col items-start gap-0.5">
            <span
              className="max-w-36 truncate font-mono text-caption text-muted-foreground"
              title={row.original.engaged_by || undefined}
            >
              {row.original.engaged_by || '—'}
            </span>
            <span className="flex items-center gap-1.5">
              <RelTime
                ts={row.original.engaged_at}
                className="shrink-0 whitespace-nowrap text-caption text-muted-foreground"
              />
              {row.original.source === 'operator' && (
                <Badge variant="outline" title={t('stops.aalHint')}>
                  {t('stops.aalShort', { aal: row.original.engaged_aal })}
                </Badge>
              )}
            </span>
          </span>
        ),
      },
      {
        accessorKey: 'revoked_approvals',
        header: t('stops.revoked'),
        meta: WRAP_HEADER,
        cell: ({ row }) => (
          <span
            className="font-mono tabular-nums text-caption"
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
          <span className="flex flex-col items-start gap-0.5">
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
            <div className="flex flex-wrap items-center justify-end gap-1">
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
        <h2 className="text-heading">{t('stops.title')}</h2>
        <p className="text-caption text-muted-foreground">
          {t('stops.caption')}
        </p>
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
          // A kill switch never pulled is the good state: one line, no panel,
          // and nothing to press. Pulling it is the control above, and it is not
          // this surface's "next action".
          <EmptyState
            quiet
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
