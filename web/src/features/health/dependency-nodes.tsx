// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  BaseEdge,
  type Edge,
  EdgeLabelRenderer,
  type EdgeProps,
  getBezierPath,
  Handle,
  type Node,
  type NodeProps,
  Position,
} from '@xyflow/react'
import { Bot, type LucideIcon, Radio, Server, Wrench } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import type { DepEdgeData, DepNodeData } from './dependency-model'

/**
 * DepNode — one dependency-graph node. It is COLORED BY ITS HEALTH ANNOTATION (the
 * left accent rail + the icon chip), never by kind, so the dependency map shares the
 * exact state palette of the status rows and stream frames (docs UI-CONTRACT-HEALTH
 * §8): healthy=green, degraded=amber, down=red, unknown=gray. The kind only chooses
 * the icon. Handles are source-right / target-left to match the layered layout.
 */
const KIND_ICON: Record<string, LucideIcon> = {
  agent: Bot,
  session: Radio,
  mcp: Server,
  mcp_tool: Wrench,
}

/** Health → the soft chip + accent rail classes (token-driven, theme-aware). */
const HEALTH_CHIP: Record<string, string> = {
  healthy: 'bg-success-soft text-success',
  degraded: 'bg-warning-soft text-warning',
  down: 'bg-danger-soft text-danger',
  unknown: 'bg-muted text-muted-foreground',
}

const HEALTH_RAIL: Record<string, string> = {
  healthy: 'bg-success',
  degraded: 'bg-warning',
  down: 'bg-danger',
  unknown: 'bg-graphite-400',
}

export function DepNode({ data, selected }: NodeProps<Node<DepNodeData>>) {
  const { t } = useTranslation('health')
  const Icon = KIND_ICON[data.kind] ?? Server
  const state = String(data.health)
  return (
    <div
      className={cn(
        'relative flex items-center gap-2 overflow-hidden rounded-lg border bg-elevated px-3 py-2 pl-4 shadow-sm transition-colors',
        'border-border',
        selected && 'border-accent-text ring-2 ring-accent-text/40',
      )}
      title={
        state === 'unknown'
          ? t('stateHint.unknown')
          : t(`state.${state}`, { defaultValue: state })
      }
    >
      <Handle
        type="target"
        position={Position.Left}
        className="!h-1.5 !w-1.5 !border-0 !bg-transparent"
      />
      {/* Health rail */}
      <span
        className={cn(
          'absolute inset-y-0 left-0 w-1.5',
          HEALTH_RAIL[state] ?? HEALTH_RAIL.unknown,
        )}
        aria-hidden
      />
      <span
        className={cn(
          'flex size-7 shrink-0 items-center justify-center rounded-md [&_svg]:size-4',
          HEALTH_CHIP[state] ?? HEALTH_CHIP.unknown,
        )}
      >
        <Icon />
      </span>
      <div className="min-w-0">
        <div
          className="max-w-[180px] truncate font-mono text-xs text-foreground"
          title={data.ref}
        >
          {data.ref}
        </div>
        <div className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
          {t(`subjectKind.${data.kind}`, { defaultValue: data.kind })}
        </div>
      </div>
      <Handle
        type="source"
        position={Position.Right}
        className="!h-1.5 !w-1.5 !border-0 !bg-transparent"
      />
    </div>
  )
}

/**
 * DepEdge — one dependency edge, styled by relation (the pure model fixes the color:
 * uses_mcp=blue, uses_tool=slate, delegates_to=copper). A small relation chip rides
 * any selected edge so the relation is unambiguous.
 */
export function DepEdgeComp({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  data,
  markerEnd,
  selected,
}: EdgeProps<Edge<DepEdgeData>>) {
  const [path, labelX, labelY] = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
  })
  if (!data) return <BaseEdge id={id} path={path} markerEnd={markerEnd} />
  return (
    <>
      <BaseEdge
        id={id}
        path={path}
        markerEnd={markerEnd}
        style={{
          stroke: data.color,
          strokeWidth: selected ? data.width + 0.75 : data.width,
          opacity: 0.9,
        }}
      />
      {selected && (
        <EdgeLabelRenderer>
          <div
            className="pointer-events-none absolute rounded-sm border bg-surface px-1 text-[10px] font-semibold leading-tight shadow-xs"
            style={{
              transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)`,
              color: data.color,
              borderColor: data.color,
            }}
          >
            {data.token}
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  )
}

export const depNodeTypes = { depNode: DepNode }
export const depEdgeTypes = { depEdge: DepEdgeComp }
