// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import {
  Activity,
  ExternalLink,
  HeartPulse,
  RefreshCw,
  ShieldCheck,
} from 'lucide-react'
import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { PageHeader } from '@/components/ui/page-header'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Spinner } from '@/components/ui/spinner'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  humanDurationSeconds,
  LiveDot,
  ppmToPercent,
  RelTimeLabel,
  useLiveStream,
} from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { ApiError } from '@/lib/api/errors'
import { formatInt, formatLatency, formatPercent } from '@/lib/format'
import { cn } from '@/lib/utils'
import { healthApi, healthKeys } from './api'
import { DependencyMap } from './dependency-map'
import { HealthDetailSheet } from './detail'
import { HealthStateBadge } from './health-state-badge'
import { Incidents } from './incidents'
import {
  ReliabilityTimeline,
  SubjectPicker,
  type SubjectRef,
} from './reliability-timeline'
import { SlaGauge } from './sla-gauge'
import { ConnectorHealthTab } from './connector-health'
import { ChecksTab } from './checks-tab'
import { SystemHealthTab } from './system-health-tab'
import type { SlaDTO, StatusDTO } from './types'
import './i18n'

const ALL = '__all__'
const STATUS_LIMIT = 200
type HealthTab =
  | 'status'
  | 'checks'
  | 'sla'
  | 'incidents'
  | 'timeline'
  | 'connectors'
  | 'dependencies'
  | 'system'

/** A subject's intent is not "active" → it is not alerting (paused/retired). */
function isAlerting(s: StatusDTO): boolean {
  return s.desired_status === 'active'
}

/** Worst-first rank so a down/degraded subject surfaces by default in the table. */
const STATE_RANK: Record<string, number> = {
  down: 3,
  degraded: 2,
  unknown: 1,
  healthy: 0,
}

export function HealthView() {
  const { t } = useTranslation('health')
  const { activeTenant, can } = useAuth()
  const canReadChecks = can('health:check:read')

  const [stateFacet, setStateFacet] = useState<string>(ALL)
  const [kindFacet, setKindFacet] = useState<string>(ALL)
  const [activeTab, setActiveTab] = useState<HealthTab>('status')
  const [selected, setSelected] = useState<StatusDTO | null>(null)
  const [slaSubject, setSlaSubject] = useState<SubjectRef | null>(null)
  const [timelineSubject, setTimelineSubject] = useState<SubjectRef | null>(
    null,
  )

  const facetState = stateFacet === ALL ? undefined : stateFacet
  const facetKind = kindFacet === ALL ? undefined : kindFacet

  const statusQuery = useQuery({
    queryKey: healthKeys.status(activeTenant, {
      state: facetState,
      subject_kind: facetKind,
      limit: STATUS_LIMIT,
    }),
    queryFn: () =>
      healthApi.status({
        state: facetState,
        subject_kind: facetKind,
        limit: STATUS_LIMIT,
      }),
  })

  // Live patches keyed by subject id, merged over the fetched page (the stream is
  // the authoritative latest snapshot; reconcile by id, never assume continuity).
  const [livePatches, setLivePatches] = useState<Record<string, StatusDTO>>({})
  const onSnapshot = useCallback((snapshot: StatusDTO) => {
    if (!snapshot?.id) return
    setLivePatches((prev) => ({ ...prev, [snapshot.id]: snapshot }))
  }, [])

  const { status: streamStatus } = useLiveStream<StatusDTO>({
    path: '/v1/m/health/stream',
    events: ['health'],
    onSnapshot,
  })

  const baseRows = useMemo(
    () => statusQuery.data?.items ?? [],
    [statusQuery.data],
  )
  const rows = useMemo(() => {
    if (Object.keys(livePatches).length === 0) return baseRows
    return baseRows.map((r) => livePatches[r.id] ?? r)
  }, [baseRows, livePatches])

  // The full subject list (unfiltered cache base) for the SLA/timeline pickers.
  const subjectsForPicker = baseRows

  const tiles = useMemo(() => {
    const c = { healthy: 0, degraded: 0, down: 0, unknown: 0 }
    for (const r of rows) {
      if (r.state === 'healthy') c.healthy++
      else if (r.state === 'degraded') c.degraded++
      else if (r.state === 'down') c.down++
      else c.unknown++
    }
    return c
  }, [rows])

  const error = statusQuery.error
  // ⛔ Mismo defecto dentro de un booleano: un `step_up_required` satisface `isForbidden`
  // (sólo el status, lib/api/errors.ts:59), así que `forbidden` salía cierto y la vista se
  // sustituía por la acusación. Se separa el aseguramiento ANTES de derivar el rol.
  const stepUp = error instanceof ApiError && error.isStepUpRequired
  const forbidden = !stepUp && error instanceof ApiError && error.isForbidden

  const onPickSubject = (s: StatusDTO) => {
    setSelected(s)
  }

  return (
    <div className="flex h-full flex-col gap-4">
      <PageHeader
        icon={HeartPulse}
        title={t('title')}
        description={
          <span className="space-y-1">
            <span className="block max-w-2xl">{t('subtitle')}</span>
            <span className="flex items-center gap-1.5 text-xs">
              <ShieldCheck className="size-3.5 shrink-0 text-confidence-attributed" />
              {t('auditedNote')}
            </span>
          </span>
        }
        actions={
          <div className="flex items-center gap-3">
            <LiveDot status={streamStatus} />
            {/*E4f: the public, session-free status page — shareable with
             * anyone who should see availability without console access. */}
            <Button asChild variant="ghost" size="sm">
              <Link to="/status-page" target="_blank" rel="noopener">
                <ExternalLink className="size-3.5" />
                {t('statusPageLink')}
              </Link>
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => void statusQuery.refetch()}
              disabled={statusQuery.isFetching}
            >
              <RefreshCw
                className={cn(
                  'size-3.5',
                  statusQuery.isFetching && 'animate-spin',
                )}
              />
              {t('refresh')}
            </Button>
          </div>
        }
      />

      {stepUp ? (
        <StepUpRequiredState
          action="generic"
          onElevated={() => void statusQuery.refetch()}
        />
      ) : forbidden ? (
        <ForbiddenState
          title={t('forbidden.title')}
          description={t('forbidden.description')}
        />
      ) : (
        <>
          {/* Stat tiles */}
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
            <StatTile
              label={t('tiles.healthy')}
              value={tiles.healthy}
              tone="success"
              loading={statusQuery.isLoading}
            />
            <StatTile
              label={t('tiles.degraded')}
              value={tiles.degraded}
              tone={tiles.degraded > 0 ? 'warning' : undefined}
              loading={statusQuery.isLoading}
            />
            <StatTile
              label={t('tiles.down')}
              value={tiles.down}
              tone={tiles.down > 0 ? 'danger' : undefined}
              loading={statusQuery.isLoading}
            />
            <StatTile
              label={t('tiles.unknown')}
              value={tiles.unknown}
              loading={statusQuery.isLoading}
            />
          </div>

          <Tabs
            value={activeTab}
            onValueChange={(value) => setActiveTab(value as HealthTab)}
            className="min-h-0 flex-1"
          >
            <div className="flex flex-wrap items-center justify-between gap-2">
              <TabsList>
                <TabsTrigger value="status">{t('tabs.status')}</TabsTrigger>
                {canReadChecks ? (
                  <TabsTrigger value="checks">{t('tabs.checks')}</TabsTrigger>
                ) : null}
                <TabsTrigger value="sla">{t('tabs.sla')}</TabsTrigger>
                <TabsTrigger value="incidents">
                  {t('tabs.incidents')}
                </TabsTrigger>
                <TabsTrigger value="timeline">{t('tabs.timeline')}</TabsTrigger>
                <TabsTrigger value="connectors">
                  {t('tabs.connectors')}
                </TabsTrigger>
                <TabsTrigger value="dependencies">
                  {t('tabs.dependencies')}
                </TabsTrigger>
                <TabsTrigger value="system">{t('tabs.system')}</TabsTrigger>
              </TabsList>
            </div>

            <TabsContent value="status">
              <StatusTab
                rows={rows}
                isLoading={statusQuery.isLoading}
                error={error}
                onRetry={() => void statusQuery.refetch()}
                onSelect={onPickSubject}
                stateFacet={stateFacet}
                setStateFacet={setStateFacet}
                kindFacet={kindFacet}
                setKindFacet={setKindFacet}
              />
            </TabsContent>

            {canReadChecks ? (
              <TabsContent value="checks">
                <ChecksTab tenant={activeTenant} />
              </TabsContent>
            ) : null}

            <TabsContent value="sla">
              <SlaTab
                tenant={activeTenant}
                subjects={subjectsForPicker}
                selected={slaSubject}
                onSelect={setSlaSubject}
              />
            </TabsContent>

            <TabsContent value="incidents">
              <Incidents tenant={activeTenant} />
            </TabsContent>

            <TabsContent value="timeline">
              <ReliabilityTimeline
                tenant={activeTenant}
                subjects={subjectsForPicker}
                selected={timelineSubject}
                onSelect={setTimelineSubject}
              />
            </TabsContent>

            <TabsContent value="connectors">
              <ConnectorHealthTab tenant={activeTenant} />
            </TabsContent>

            <TabsContent value="dependencies">
              <DependencyMap tenant={activeTenant} />
            </TabsContent>

            <TabsContent value="system">
              <SystemHealthTab />
            </TabsContent>
          </Tabs>
        </>
      )}

      <HealthDetailSheet
        status={selected}
        onClose={() => setSelected(null)}
        onViewSla={(s) => {
          setSlaSubject({
            subject_kind: s.subject_kind,
            subject_ref: s.subject_ref,
          })
          setActiveTab('sla')
          setSelected(null)
        }}
        onViewTimeline={(s) => {
          setTimelineSubject({
            subject_kind: s.subject_kind,
            subject_ref: s.subject_ref,
          })
          setActiveTab('timeline')
          setSelected(null)
        }}
      />
    </div>
  )
}

/** Status dashboard: the live, sortable, faceted table of monitored subjects. */
function StatusTab({
  rows,
  isLoading,
  error,
  onRetry,
  onSelect,
  stateFacet,
  setStateFacet,
  kindFacet,
  setKindFacet,
}: {
  rows: StatusDTO[]
  isLoading: boolean
  error: unknown
  onRetry: () => void
  onSelect: (s: StatusDTO) => void
  stateFacet: string
  setStateFacet: (v: string) => void
  kindFacet: string
  setKindFacet: (v: string) => void
}) {
  const { t } = useTranslation('health')

  const columns = useMemo<TableColumn<StatusDTO>[]>(
    () => [
      {
        id: 'subject',
        accessorKey: 'subject_ref',
        header: t('status.cols.subject'),
        cell: ({ row }) => {
          const s = row.original
          return (
            <div className="min-w-0">
              <div className="truncate font-medium text-foreground">
                {s.name || s.subject_ref}
              </div>
              {s.name && s.name !== s.subject_ref && (
                <div
                  className="truncate font-mono text-xs text-muted-foreground"
                  title={s.subject_ref}
                >
                  {s.subject_ref}
                </div>
              )}
            </div>
          )
        },
      },
      {
        accessorKey: 'subject_kind',
        header: t('status.cols.kind'),
        cell: ({ getValue }) => (
          <Badge variant="outline">
            {t(`subjectKind.${getValue<string>()}`, {
              defaultValue: getValue<string>(),
            })}
          </Badge>
        ),
      },
      {
        id: 'state',
        accessorFn: (s) => STATE_RANK[s.state] ?? -1,
        header: t('status.cols.state'),
        // Default the table to worst-first.
        sortDescFirst: true,
        cell: ({ row }) => {
          const s = row.original
          return (
            <div className="flex flex-wrap items-center gap-1.5">
              <HealthStateBadge state={s.state} />
              {s.sla_breach_open && (
                <Badge variant="danger" title={t('status.slaBreachHint')}>
                  {t('status.slaBreach')}
                </Badge>
              )}
            </div>
          )
        },
      },
      {
        id: 'desired',
        accessorKey: 'desired_status',
        header: t('status.cols.desired'),
        cell: ({ row }) => {
          const s = row.original
          const alerting = isAlerting(s)
          return (
            <span
              className={cn(
                'inline-flex items-center gap-1.5 text-xs',
                alerting ? 'text-muted-foreground' : 'text-muted-foreground/70',
              )}
              title={
                !alerting
                  ? t(`desiredHint.${s.desired_status}`, { defaultValue: '' })
                  : undefined
              }
            >
              {!alerting && (
                <span
                  className="size-1.5 rounded-full bg-graphite-400"
                  aria-hidden
                />
              )}
              {t(`desired.${s.desired_status}`, {
                defaultValue: s.desired_status,
              })}
            </span>
          )
        },
      },
      {
        id: 'sla',
        accessorFn: (s) => s.sla_target_ppm,
        header: t('status.cols.sla'),
        cell: ({ row }) => {
          const ppm = row.original.sla_target_ppm
          if (!ppm || ppm <= 0)
            return <span className="text-muted-foreground">—</span>
          return (
            <span className="font-mono text-xs tabular-nums text-muted-foreground">
              {ppmToPercent(ppm)}
            </span>
          )
        },
      },
      {
        accessorKey: 'last_latency_ms',
        header: t('status.cols.latency'),
        cell: ({ getValue }) => {
          const ms = getValue<number>()
          if (ms == null || ms < 0)
            return <span className="text-muted-foreground">—</span>
          return (
            <span className="font-mono text-xs tabular-nums text-muted-foreground">
              {formatLatency(ms)}
            </span>
          )
        },
      },
      {
        accessorKey: 'last_seen_at',
        header: t('status.cols.lastSeen'),
        cell: ({ getValue }) => (
          <RelTimeLabel ts={getValue<string | undefined>()} />
        ),
      },
    ],
    [t],
  )

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-xs text-muted-foreground">{t('live.note')}</p>
        <div className="flex items-center gap-2">
          <Select value={stateFacet} onValueChange={setStateFacet}>
            <SelectTrigger
              className="h-7 w-auto min-w-[8rem] text-xs"
              aria-label={t('facets.allStates')}
            >
              <SelectValue placeholder={t('facets.allStates')} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('facets.allStates')}</SelectItem>
              <SelectItem value="healthy">{t('state.healthy')}</SelectItem>
              <SelectItem value="degraded">{t('state.degraded')}</SelectItem>
              <SelectItem value="down">{t('state.down')}</SelectItem>
              <SelectItem value="unknown">{t('state.unknown')}</SelectItem>
            </SelectContent>
          </Select>
          <Select value={kindFacet} onValueChange={setKindFacet}>
            <SelectTrigger
              className="h-7 w-auto min-w-[7rem] text-xs"
              aria-label={t('facets.allKinds')}
            >
              <SelectValue placeholder={t('facets.allKinds')} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('facets.allKinds')}</SelectItem>
              <SelectItem value="agent">{t('subjectKind.agent')}</SelectItem>
              <SelectItem value="mcp">{t('subjectKind.mcp')}</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>

      <DataTable
        columns={columns}
        data={rows}
        isLoading={isLoading}
        error={error}
        onRetry={onRetry}
        getRowId={(r) => r.id}
        onRowClick={onSelect}
        searchable
        searchPlaceholder={t('status.searchPlaceholder')}
        stickyHeader
        empty={<StatusEmpty />}
      />
    </div>
  )
}

function StatusEmpty() {
  const { t } = useTranslation('health')
  return (
    <EmptyState
      icon={<HeartPulse />}
      title={t('status.empty.title')}
      description={t('status.empty.description')}
    />
  )
}

/** SLA tab: pick a subject, fetch /sla, render the SVG gauge + separated downtime. */
function SlaTab({
  tenant,
  subjects,
  selected,
  onSelect,
}: {
  tenant: string | null
  subjects: StatusDTO[]
  selected: SubjectRef | null
  onSelect: (s: SubjectRef | null) => void
}) {
  const { t } = useTranslation('health')
  // A window selector — the engine defaults to 30 days; we offer common windows.
  const [windowSeconds, setWindowSeconds] = useState<string>('2592000')

  const params = selected
    ? {
        subject_kind: selected.subject_kind,
        subject_ref: selected.subject_ref,
        window_seconds: Number(windowSeconds),
      }
    : null

  const query = useQuery({
    queryKey: healthKeys.sla(tenant, params ?? undefined),
    queryFn: () => healthApi.sla(params!),
    enabled: !!params,
  })

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <SubjectPicker
          subjects={subjects}
          selected={selected}
          onSelect={onSelect}
          labelKey="sla.pick"
          placeholderKey="sla.pickPlaceholder"
        />
        {selected && (
          <div className="flex items-center gap-2">
            <span className="text-sm text-muted-foreground">
              {t('sla.window')}
            </span>
            <Select value={windowSeconds} onValueChange={setWindowSeconds}>
              <SelectTrigger
                className="h-7 w-auto min-w-[6rem] text-xs"
                aria-label={t('sla.window')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="86400">24h</SelectItem>
                <SelectItem value="604800">7d</SelectItem>
                <SelectItem value="2592000">30d</SelectItem>
                <SelectItem value="7776000">90d</SelectItem>
              </SelectContent>
            </Select>
          </div>
        )}
      </div>

      {subjects.length === 0 ? (
        <EmptyState icon={<Activity />} title={t('sla.pickEmpty')} />
      ) : !selected ? (
        <EmptyState
          icon={<Activity />}
          title={t('sla.pickPrompt.title')}
          description={t('sla.pickPrompt.description')}
        />
      ) : query.isLoading ? (
        <div className="flex justify-center py-12">
          <Spinner />
        </div>
      ) : query.error ? (
        query.error instanceof ApiError && query.error.isStepUpRequired ? (
          // ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es SÓLO el status
          // (lib/api/errors.ts:59) y un `step_up_required` lo satisface también, así
          // que leerlo primero acusaba al operador de un permiso que SÍ tiene.
          <StepUpRequiredState
            action="generic"
            onElevated={() => void query.refetch()}
          />
        ) : query.error instanceof ApiError && query.error.isForbidden ? (
          <ForbiddenState
            title={t('forbidden.title')}
            description={t('forbidden.description')}
          />
        ) : (
          <ErrorState retry={() => void query.refetch()} />
        )
      ) : query.data && !query.data.has_check ? (
        <EmptyState
          icon={<Activity />}
          title={t('sla.noCheck.title')}
          description={t('sla.noCheck.description')}
        />
      ) : query.data && !query.data.has_data ? (
        // has_check but no ledger history yet → uptime is UNDEFINED, not 0%.
        <EmptyState
          icon={<Activity />}
          title={t('sla.noData.title')}
          description={t('sla.noData.description')}
        />
      ) : query.data ? (
        <SlaReport data={query.data} />
      ) : null}
    </div>
  )
}

function SlaReport({ data }: { data: SlaDTO }) {
  const { t } = useTranslation('health')
  return (
    <div className="grid gap-4 lg:grid-cols-[auto_1fr]">
      <Card className="flex flex-col items-center justify-center p-6">
        <SlaGauge
          uptimePercent={data.uptime_percent}
          targetPpm={data.sla_target_ppm}
          breaching={data.breaching}
        />
        <div className="mt-2">
          {data.breaching ? (
            <Badge variant="danger">{t('sla.breaching')}</Badge>
          ) : (
            <Badge variant="success">{t('sla.withinTarget')}</Badge>
          )}
        </div>
      </Card>

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3 lg:content-start">
        <Card className="p-4">
          <div className="text-xs text-muted-foreground">
            {t('sla.currentState')}
          </div>
          <div className="mt-1.5">
            <HealthStateBadge state={data.current_state} />
          </div>
        </Card>
        {/* Downtime and degraded are shown SEPARATELY — degraded is UP for the SLA. */}
        <Card className="p-4">
          <div className="text-xs text-muted-foreground">
            {t('sla.downtime')}
          </div>
          {/* ⛔ Rojo INCONDICIONAL: `modules/health/sla.go` inicializa ambos
              contadores a cero y sólo incrementa el del estado recorrido, así que
              una ventana completamente SANA llega aquí con cero — y se pintaba
              como una caída. Cero segundos caídos es la mejor noticia posible. */}
          <div
            className={cn(
              'mt-1 font-display text-xl font-semibold tabular-nums',
              data.downtime_seconds > 0 ? 'text-danger' : 'text-foreground',
            )}
          >
            <DurationSeconds seconds={data.downtime_seconds} />
          </div>
        </Card>
        <Card className="p-4">
          <div
            className="flex items-center gap-1 text-xs text-muted-foreground"
            title={t('sla.degradedNote')}
          >
            {t('sla.degraded')}
          </div>
          <div
            className={cn(
              'mt-1 font-display text-xl font-semibold tabular-nums',
              data.degraded_seconds > 0 ? 'text-warning' : 'text-foreground',
            )}
          >
            <DurationSeconds seconds={data.degraded_seconds} />
          </div>
        </Card>
        <Card className="p-4 sm:col-span-3">
          <p className="text-xs text-muted-foreground">
            {t('sla.degradedNote')}
          </p>
          <p className="mt-1 text-sm text-foreground">
            {t('sla.uptime')}:{' '}
            <span className="font-mono tabular-nums">
              {formatPercent(data.uptime_percent, { digits: 3 })}
            </span>
            {data.sla_target_ppm > 0 && (
              <>
                {' · '}
                {t('sla.target')}:{' '}
                <span className="font-mono tabular-nums">
                  {ppmToPercent(data.sla_target_ppm)}
                </span>
              </>
            )}
          </p>
        </Card>
      </div>
    </div>
  )
}

function DurationSeconds({ seconds }: { seconds: number }) {
  // Reuse the shared seconds-based humanizer (scales to hours/days).
  return <>{humanDurationSeconds(seconds)}</>
}

function StatTile({
  label,
  value,
  tone,
  loading,
}: {
  label: string
  value: number
  tone?: 'success' | 'warning' | 'danger'
  loading?: boolean
}) {
  return (
    <Card className="p-3">
      <div className="text-xs text-muted-foreground">{label}</div>
      {loading ? (
        <Skeleton className="mt-1 h-7 w-12" />
      ) : (
        <div
          role="status"
          aria-live="polite"
          aria-atomic="true"
          className={cn(
            'font-display text-2xl font-semibold tabular-nums',
            tone === 'success' && 'text-success',
            tone === 'warning' && 'text-warning',
            tone === 'danger' && 'text-danger',
            !tone && 'text-foreground',
          )}
        >
          {formatInt(value)}
        </div>
      )}
    </Card>
  )
}
