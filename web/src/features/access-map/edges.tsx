// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  BaseEdge,
  type Edge,
  EdgeLabelRenderer,
  type EdgeProps,
  getBezierPath,
} from '@xyflow/react'
import type { AccessEdgeData } from './graph-model'

/**
 * AccessEdgeComp draws one R/RW edge. The stroke color/width/dash come from the
 * pure model (graph-model.styleEdge): read = blue, write/readwrite = copper and
 * thicker (the risk), approximate = dashed; the diff overlay recolors findings
 * danger/amber. A small R / RW chip rides the risk-bearing edges (and any selected
 * edge) so the mode is unambiguous at a glance — read edges stay clean.
 */
export function AccessEdgeComp({
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
}: EdgeProps<Edge<AccessEdgeData>>) {
  const [path, labelX, labelY] = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
  })
  if (!data) return <BaseEdge id={id} path={path} markerEnd={markerEnd} />
  const showLabel = data.showLabel || !!selected
  return (
    <>
      <BaseEdge
        id={id}
        path={path}
        markerEnd={markerEnd}
        style={{
          stroke: data.color,
          strokeWidth: selected ? data.width + 0.75 : data.width,
          strokeDasharray: data.dashed ? '6 5' : undefined,
          opacity: data.opacity,
        }}
      />
      {showLabel && (
        <EdgeLabelRenderer>
          <div
            className="pointer-events-none absolute rounded-sm border bg-surface px-1 text-[10px] font-semibold leading-tight tabular-nums shadow-xs"
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

export const accessEdgeTypes = { access: AccessEdgeComp }
