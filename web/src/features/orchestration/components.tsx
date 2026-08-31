// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Orchestration presentational pieces — PURE: data via props, no fetching/auth, so
// each is trivially testable with fixtures. The communication graph renders the
// engine's FLAT node/edge list with React Flow: the layout is DERIVED here (layered
// by role then index — never from a randomness source) so it is deterministic for the
// screenshot. Honesty rules are encoded: `confidence: approximate` → dashed stroke
// (certainty is never faked), a missed cadence → alert border, `tool_ref` is the verb
// only (never arguments), and `declared_not_fired` reads as "approved, not actuated".
import { useMemo } from 'react'
import type { CSSProperties } from 'react'
import {
  Background,
  Controls,
  Handle,
  Position,
  ReactFlow,
  type Edge,
  type Node,
  type NodeProps,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import {
  Archive,
  Bot,
  Flame,
  History,
  MoreHorizontal,
  Pause,
  Pencil,
  Play,
  Radio,
  Server,
  TriangleAlert,
  Wrench,
  type LucideIcon,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { StatusBadge } from '@/components/data/badges'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useChartTheme } from '@/components/charts'
import { HashChip, SectionCard, GateBadge } from '@/features/_intel'
import { formatDateTime, formatInt, formatRelativeTime } from '@/lib/format'
import { cn } from '@/lib/utils'
import type {
  DecisionDTO,
  DecisionOpStatus,
  FlowDTO,
  FlowState,
  GraphEdge,
  GraphNode,
  GraphResponse,
  NodeKind,
  ScheduleDTO,
  DesiredStatus,
  TimelineItem,
} from './types'

// --- pure edge-style helper (tested directly) --------------------------------

/** Map a graph edge to its React Flow stroke style. `approximate` confidence is the
 *  ONLY case that dashes — solid otherwise. A missed/inactive edge never invents a
 *  solid line. Exported so the honesty rule can be asserted without mounting a graph. */
export function edgeStyleForConfidence(
  confidence: GraphEdge['confidence'],
  stroke: string,
): CSSProperties {
  return {
    stroke,
    strokeWidth: 1.6,
    ...(confidence === 'approximate' ? { strokeDasharray: '4 3' } : {}),
  }
}

// --- graph node ---------------------------------------------------------------

const NODE_ICON: Record<NodeKind, LucideIcon> = {
  session: Radio,
  agent: Bot,
  mcp_server: Server,
  mcp_tool: Wrench,
}

/** Data we attach to each React Flow node — the redacted ref + role + alert flag. */
export interface CommNodeData extends Record<string, unknown> {
  ref: string
  kind: NodeKind
  role: GraphNode['role']
  alert: boolean
}

function CommNode({ data }: NodeProps<Node<CommNodeData>>) {
  const { t } = useTranslation('orchestration')
  const Icon = NODE_ICON[data.kind] ?? Bot
  return (
    <div
      className={cn(
        'flex items-center gap-2 rounded-lg border bg-surface px-3 py-2 shadow-sm',
        data.alert ? 'border-danger ring-2 ring-danger/40' : 'border-border',
      )}
    >
      <Handle
        type="target"
        position={Position.Top}
        className="!h-1.5 !w-1.5 !border-0 !bg-transparent"
      />
      <span
        className={cn(
          'flex size-7 shrink-0 items-center justify-center rounded-md [&_svg]:size-4',
          data.alert
            ? 'bg-danger-soft text-danger'
            : 'bg-accent-soft text-accent-soft-foreground',
        )}
      >
        <Icon />
      </span>
      <div className="min-w-0">
        <div
          className="max-w-[170px] truncate font-mono text-xs text-foreground"
          title={data.ref}
        >
          {data.ref}
        </div>
        <div className="flex items-center gap-1.5">
          <span className="text-[10px] font-medium tracking-wide text-muted-foreground uppercase">
            {t(`graph.kind.${data.kind}`, { defaultValue: data.kind })}
          </span>
          <span className="text-[10px] font-medium tracking-wide text-muted-foreground uppercase">
            · {t(`graph.role.${data.role}`, { defaultValue: data.role })}
          </span>
        </div>
      </div>
      {data.alert ? (
        <TriangleAlert className="size-3.5 shrink-0 text-danger" aria-hidden />
      ) : null}
      <Handle
        type="source"
        position={Position.Bottom}
        className="!h-1.5 !w-1.5 !border-0 !bg-transparent"
      />
    </div>
  )
}

const commNodeTypes = { comm: CommNode }

// --- deterministic layered layout --------------------------------------------

/** Tier a node by role: supervisors top, workers middle, peers (MCP) bottom. The
 *  x position is derived from the node's index WITHIN its tier — stable, never random. */
function tierForRole(role: GraphNode['role']): number {
  if (role === 'supervisor') return 0
  if (role === 'worker') return 1
  return 2
}

const TIER_Y = 150
const COL_X = 220

/** Build the React Flow node list from the contract's flat nodes, positioned by a
 *  deterministic layered layout (role tier → index within tier). */
export function layoutNodes(nodes: GraphNode[]): Node<CommNodeData>[] {
  const perTier: Record<number, number> = {}
  return nodes.map((n) => {
    const tier = tierForRole(n.role)
    const col = perTier[tier] ?? 0
    perTier[tier] = col + 1
    return {
      id: n.id,
      type: 'comm',
      position: { x: col * COL_X, y: tier * TIER_Y },
      data: {
        ref: n.ref,
        kind: n.kind,
        role: n.role,
        alert: n.health === 'missed' || n.schedule_status === 'missed',
      },
      draggable: false,
    }
  })
}

/** Build the React Flow edge list: animated per the contract, dashed when approximate
 *  (never faking certainty), label = `tool_ref` (verb only). */
export function layoutEdges(edges: GraphEdge[], stroke: string): Edge[] {
  return edges.map((e) => ({
    id: e.id,
    source: e.source,
    target: e.target,
    animated: e.animated,
    label: e.tool_ref,
    style: edgeStyleForConfidence(e.confidence, stroke),
    labelStyle: { fill: 'var(--color-muted-foreground)', fontSize: 10 },
    labelBgStyle: { fill: 'var(--color-surface)' },
  }))
}

// --- communication graph ------------------------------------------------------

export function CommunicationGraph({ graph }: { graph: GraphResponse }) {
  const { t } = useTranslation('orchestration')
  const theme = useChartTheme()
  const nodes = useMemo(() => layoutNodes(graph.nodes), [graph.nodes])
  const edges = useMemo(
    () => layoutEdges(graph.edges, theme.border),
    [graph.edges, theme.border],
  )
  return (
    <div
      role="group"
      aria-label={t('graph.title')}
      className="h-[520px] w-full overflow-hidden rounded-lg border border-border bg-surface"
    >
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={commNodeTypes}
        fitView
        fitViewOptions={{ padding: 0.2 }}
        minZoom={0.2}
        maxZoom={2}
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable
        panOnScroll
        proOptions={{ hideAttribution: true }}
      >
        <Background gap={22} size={1} color={theme.grid} />
        <Controls
          showInteractive={false}
          className="!shadow-md [&_button]:!border-border [&_button]:!bg-surface [&_button]:!text-muted-foreground hover:[&_button]:!bg-muted"
        />
      </ReactFlow>
    </div>
  )
}

/** A compact legend explaining the graph encodings (solid vs dashed, animated). */
export function GraphLegend() {
  const { t } = useTranslation('orchestration')
  return (
    <ul className="flex flex-wrap gap-x-4 gap-y-1.5 text-xs text-muted-foreground">
      <li className="flex items-center gap-1.5">
        <svg width="22" height="6" aria-hidden>
          <line
            x1="0"
            y1="3"
            x2="22"
            y2="3"
            stroke="currentColor"
            strokeWidth="1.6"
          />
        </svg>
        {t('graph.legend.attributed')}
      </li>
      <li className="flex items-center gap-1.5">
        <svg width="22" height="6" aria-hidden>
          <line
            x1="0"
            y1="3"
            x2="22"
            y2="3"
            stroke="currentColor"
            strokeWidth="1.6"
            strokeDasharray="4 3"
          />
        </svg>
        {t('graph.legend.approximate')}
      </li>
      <li className="flex items-center gap-1.5">
        <TriangleAlert className="size-3.5 text-danger" aria-hidden />
        {t('graph.legend.missed')}
      </li>
    </ul>
  )
}

// --- flows table --------------------------------------------------------------

const FLOW_STATE_VARIANT: Record<FlowState, BadgeVariant> = {
  active: 'success',
  idle: 'neutral',
  stalled: 'warning',
  completed: 'info',
}

export function FlowStateBadge({ state }: { state: FlowState }) {
  const { t } = useTranslation('orchestration')
  return (
    <Badge variant={FLOW_STATE_VARIANT[state] ?? 'neutral'}>
      {t(`flows.state.${state}`, { defaultValue: state })}
    </Badge>
  )
}

export function FlowsTable({ flows }: { flows: FlowDTO[] }) {
  const { t, i18n } = useTranslation('orchestration')
  const columns = useMemo<TableColumn<FlowDTO>[]>(
    () => [
      {
        accessorKey: 'supervisor_ref',
        header: t('flows.columns.supervisor'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-foreground">
            {row.original.supervisor_ref}
          </span>
        ),
      },
      {
        accessorKey: 'worker_count',
        header: t('flows.columns.workers'),
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="font-mono tabular-nums">
              {formatInt(row.original.worker_count, i18n.language)}
            </span>
            <span className="max-w-[260px] truncate text-[11px] text-muted-foreground">
              {row.original.workers.join(', ')}
            </span>
          </div>
        ),
      },
      {
        accessorKey: 'delegation_total',
        header: t('flows.columns.delegations'),
        cell: ({ row }) => (
          <span className="font-mono tabular-nums text-muted-foreground">
            {formatInt(row.original.delegation_total, i18n.language)}
          </span>
        ),
      },
      {
        accessorKey: 'state',
        header: t('flows.columns.state'),
        cell: ({ row }) => <FlowStateBadge state={row.original.state} />,
      },
      {
        accessorKey: 'last_seen_at',
        header: t('flows.columns.lastSeen'),
        cell: ({ row }) => (
          <span
            className="text-xs text-muted-foreground"
            title={formatDateTime(row.original.last_seen_at, i18n.language)}
          >
            {formatRelativeTime(row.original.last_seen_at, i18n.language)}
          </span>
        ),
      },
    ],
    [t, i18n.language],
  )
  return (
    <DataTable<FlowDTO>
      columns={columns}
      data={flows}
      getRowId={(r) => r.supervisor_ref}
      empty={
        <EmptyState
          title={t('empty.flows.title')}
          description={t('empty.flows.description')}
        />
      }
    />
  )
}

// --- schedules table ----------------------------------------------------------

export interface SchedulesTableProps {
  schedules: ScheduleDTO[]
  canRead?: boolean
  canWrite?: boolean
  canFire?: boolean
  onEdit?: (schedule: ScheduleDTO) => void
  onStatusChange?: (schedule: ScheduleDTO, status: DesiredStatus) => void
  onFire?: (schedule: ScheduleDTO) => void
  onHistory?: (schedule: ScheduleDTO) => void
}

export function SchedulesTable({
  schedules,
  canRead = false,
  canWrite = false,
  canFire = false,
  onEdit,
  onStatusChange,
  onFire,
  onHistory,
}: SchedulesTableProps) {
  const { t, i18n } = useTranslation('orchestration')
  const columns = useMemo<TableColumn<ScheduleDTO>[]>(() => {
    const base: TableColumn<ScheduleDTO>[] = [
      {
        accessorKey: 'name',
        header: t('schedules.columns.name'),
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="text-sm font-medium text-foreground">
              {row.original.name}
            </span>
            <span className="font-mono text-[11px] text-muted-foreground">
              {t(`schedules.subjectKind.${row.original.subject_kind}`, {
                defaultValue: row.original.subject_kind,
              })}
              {' · '}
              {row.original.subject_ref}
            </span>
          </div>
        ),
      },
      {
        accessorKey: 'trigger_kind',
        header: t('schedules.columns.trigger'),
        cell: ({ row }) => (
          <Badge variant="outline">
            {t(`schedules.trigger.${row.original.trigger_kind}`, {
              defaultValue: row.original.trigger_kind,
            })}
          </Badge>
        ),
      },
      {
        accessorKey: 'cadence_spec',
        header: t('schedules.columns.cadence'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.cadence_spec}
          </span>
        ),
      },
      {
        accessorKey: 'desired_status',
        header: t('schedules.columns.desired'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {t(`schedules.desired.${row.original.desired_status}`, {
              defaultValue: row.original.desired_status,
            })}
          </span>
        ),
      },
      {
        accessorKey: 'health',
        header: t('schedules.columns.health'),
        cell: ({ row }) => <StatusBadge status={row.original.health} />,
      },
      {
        accessorKey: 'last_fired_at',
        header: t('schedules.columns.lastFired'),
        cell: ({ row }) => (
          <span
            className="text-xs text-muted-foreground"
            title={formatDateTime(row.original.last_fired_at, i18n.language)}
          >
            {row.original.last_fired_at
              ? formatRelativeTime(row.original.last_fired_at, i18n.language)
              : '—'}
          </span>
        ),
      },
    ]

    if (!canRead && !canWrite && !canFire) return base

    base.push({
      id: 'actions',
      header: t('schedules.columns.actions'),
      enableSorting: false,
      cell: ({ row }) => {
        const schedule = row.original
        const active = schedule.desired_status === 'active'
        const paused = schedule.desired_status === 'paused'
        const retired = schedule.desired_status === 'retired'

        return (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={t('schedules.actions.menu', {
                  name: schedule.name,
                })}
              >
                <MoreHorizontal />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              {canRead && onHistory ? (
                <DropdownMenuItem onSelect={() => onHistory(schedule)}>
                  <History />
                  {t('schedules.actions.history')}
                </DropdownMenuItem>
              ) : null}
              {canWrite && onEdit ? (
                <DropdownMenuItem onSelect={() => onEdit(schedule)}>
                  <Pencil />
                  {t('schedules.actions.edit')}
                </DropdownMenuItem>
              ) : null}
              {canWrite && onStatusChange && !retired ? (
                <DropdownMenuItem
                  onSelect={() =>
                    onStatusChange(schedule, active ? 'paused' : 'active')
                  }
                >
                  {paused ? <Play /> : <Pause />}
                  {paused
                    ? t('schedules.actions.resume')
                    : t('schedules.actions.pause')}
                </DropdownMenuItem>
              ) : null}
              {canWrite && onStatusChange && !retired ? (
                <>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem
                    variant="destructive"
                    onSelect={() => onStatusChange(schedule, 'retired')}
                  >
                    <Archive />
                    {t('schedules.actions.retire')}
                  </DropdownMenuItem>
                </>
              ) : null}
              {canFire && onFire && !retired ? (
                <>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem onSelect={() => onFire(schedule)}>
                    <Flame />
                    {t('schedules.actions.fire')}
                  </DropdownMenuItem>
                </>
              ) : null}
            </DropdownMenuContent>
          </DropdownMenu>
        )
      },
    })
    return base
  }, [
    t,
    i18n.language,
    canRead,
    canWrite,
    canFire,
    onEdit,
    onStatusChange,
    onFire,
    onHistory,
  ])
  return (
    <DataTable<ScheduleDTO>
      columns={columns}
      data={schedules}
      getRowId={(r) => r.id}
      empty={
        <EmptyState
          title={t('empty.schedules.title')}
          description={t('empty.schedules.description')}
        />
      }
    />
  )
}

// --- decision (ledger) list ---------------------------------------------------

/** op_status → badge variant. `declared_not_fired` is NEUTRAL/INFO (approved, not
 *  actuated) — never `success`; `dispatched` is the only success. */
const OP_STATUS_VARIANT: Record<DecisionOpStatus, BadgeVariant> = {
  requested: 'info',
  blocked: 'danger',
  dispatched: 'success',
  declared_not_fired: 'info',
  failed: 'danger',
  budget_blocked: 'danger',
  budget_throttled: 'warning',
  missed: 'warning',
  // Los cuatro de workflow. `completed` es el ÚNICO éxito de esta tanda: `gate_passed`
  // dice que una puerta aprobó, no que el trabajo se hiciera; `skipped` es un paso que no
  // llegó a correr porque uno anterior falló o fue denegado —informativo, no benigno—; y
  // `reconciled` registra lo que un efecto hizo de verdad cuando su resultado llegó tarde:
  // información, ni fallo ni éxito.
  skipped: 'warning',
  gate_passed: 'info',
  completed: 'success',
  reconciled: 'info',
}

export function DecisionOpStatusBadge({
  status,
}: {
  status: DecisionOpStatus
}) {
  const { t } = useTranslation('orchestration')
  return (
    <Badge variant={OP_STATUS_VARIANT[status] ?? 'neutral'}>
      {t(`decisions.opStatus.${status}`, { defaultValue: status })}
    </Badge>
  )
}

// ⛔ `showSubject` NO ES UNA OPCIÓN DE ESTILO. En el modo por schedule el sujeto ya está
//    fijado por el selector y repetirlo en cada fila es ruido; en el ledger del ESTATE, en
//    cambio, hay filas de schedules distintos Y filas que no pertenecen a ninguno —las de
//    ejecución de workflow, que no escriben `schedule_ref`—, así que sin el sujeto la lista
//    es una sucesión de veredictos sobre algo que no se nombra. Por defecto va apagado
//    para no cambiar la superficie que ya existe.
export function DecisionList({
  decisions,
  showSubject = false,
}: {
  decisions: DecisionDTO[]
  showSubject?: boolean
}) {
  const { t, i18n } = useTranslation('orchestration')
  return (
    <ol className="flex flex-col gap-2">
      {decisions.map((d, i) => (
        <li
          key={`${d.op}-${d.occurred_at}-${i}`}
          className="flex flex-col gap-1.5 rounded-lg border border-border bg-surface p-3"
        >
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm font-medium text-foreground">
              {t(`decisions.op.${d.op}`, { defaultValue: d.op })}
            </span>
            <DecisionOpStatusBadge status={d.op_status} />
            {/* ⛔ ESTO ERA TEXTO GRIS CRUDO, y por eso importaba: al lado de un
                `DecisionOpStatusBadge` que SÍ va coloreado, un `rejected` en gris del mismo
                peso que un `approved` era **lo más silencioso de la fila**. El ojo va al
                color y se salta el gris, justo en la línea de decisiones que existe para
                auditar quién aprobó qué. El badge compartido ya distinguía los seis estados
                en deploy; aquí no se usaba. */}
            <GateBadge gate={d.gate_status} />
          </div>
          {showSubject && d.subject_ref ? (
            <p className="text-xs text-foreground">
              {/* El tipo declara `subject_kind` opcional, así que no se compone la clave
                  con un `undefined` —daría `decisions.subjectKind.undefined` y pintaría el
                  literal—: sin él se enseña sólo el ref, que es lo cierto. */}
              {d.subject_kind ? (
                <span className="text-muted-foreground">
                  {t(`decisions.subjectKind.${d.subject_kind}`, {
                    defaultValue: d.subject_kind,
                  })}{' '}
                </span>
              ) : null}
              <span className="font-mono">{d.subject_ref}</span>
              {/* Sin `schedule_ref` la fila NO es huérfana: es de un run de workflow, que
                  por construcción no cuelga de ningún schedule. Se dice, en vez de dejar
                  un hueco que se lee como dato que falta. */}
              {d.schedule_ref ? null : (
                <span className="text-muted-foreground">
                  {' '}
                  · {t('decisions.noSchedule')}
                </span>
              )}
            </p>
          ) : null}
          <p className="text-xs text-muted-foreground">
            <span className="font-mono">{d.actor}</span>
            {d.result ? <> · {d.result}</> : null}
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <time
              dateTime={d.occurred_at}
              title={formatDateTime(d.occurred_at, i18n.language)}
              className="text-xs text-muted-foreground"
            >
              {formatRelativeTime(d.occurred_at, i18n.language)}
            </time>
            {d.plan_hash ? (
              <HashChip hash={d.plan_hash} label={t('decisions.plan')} />
            ) : null}
          </div>
        </li>
      ))}
    </ol>
  )
}

// --- timeline -----------------------------------------------------------------

export function CommTimeline({ items }: { items: TimelineItem[] }) {
  const { t, i18n } = useTranslation('orchestration')
  return (
    <SectionCard
      title={t('timeline.title')}
      description={t('timeline.description')}
    >
      <ol className="flex flex-col">
        {items.map((it, i) => {
          const last = i === items.length - 1
          const isMiss = it.kind === 'cadence_miss'
          return (
            <li
              key={`${it.at}-${i}`}
              className="relative flex gap-3 pb-4 last:pb-0"
            >
              <div className="flex flex-col items-center">
                <span
                  className={cn(
                    'mt-1 size-2.5 shrink-0 rounded-full border-2 bg-background',
                    isMiss ? 'border-danger' : 'border-accent-text',
                  )}
                />
                {!last ? <span className="w-px flex-1 bg-border" /> : null}
              </div>
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-sm font-medium text-foreground">
                    {it.title}
                  </span>
                  <Badge variant={isMiss ? 'warning' : 'outline'}>
                    {t(`timeline.kind.${it.kind}`, { defaultValue: it.kind })}
                  </Badge>
                </div>
                <p className="mt-0.5 text-xs text-muted-foreground">
                  <span className="font-mono">{it.source}</span>
                  {it.source !== it.target ? (
                    <>
                      {' → '}
                      <span className="font-mono">{it.target}</span>
                    </>
                  ) : null}
                </p>
                <time
                  dateTime={it.at}
                  title={formatDateTime(it.at, i18n.language)}
                  className="text-xs text-muted-foreground"
                >
                  {formatRelativeTime(it.at, i18n.language)}
                </time>
              </div>
            </li>
          )
        })}
      </ol>
    </SectionCard>
  )
}
