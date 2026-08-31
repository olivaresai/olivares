// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  type Edge,
  Handle,
  MarkerType,
  type Node,
  type NodeProps,
  Position,
} from '@xyflow/react'
import {
  Bot,
  Box,
  Radio,
  Server,
  Sparkles,
  Wrench,
  type LucideIcon,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { EmptyState } from '@/components/ui/empty-state'
import { KvList, KvRow } from '@/components/ui/kv'
import { GraphCanvas, layeredLayout } from '@/features/shared'
import { RelTimeLabel } from '@/features/shared'
import './i18n'
import type { WiringEdgeDTO, WiringGraphDTO } from './types'

const KIND_ICON: Record<string, LucideIcon> = {
  session: Radio,
  agent: Bot,
  mcp_server: Server,
  tool: Wrench,
  skill: Sparkles,
  resource: Box,
}

interface WiringNodeData extends Record<string, unknown> {
  kind: string
  label: string
  isOrigin: boolean
}

function WiringNode({ data, selected }: NodeProps<Node<WiringNodeData>>) {
  const { t } = useTranslation('capabilities')
  const Icon = KIND_ICON[data.kind] ?? Box
  return (
    <div
      className={cn(
        'flex items-center gap-2 rounded-lg border bg-elevated px-3 py-2 shadow-sm',
        selected
          ? 'border-accent-text ring-2 ring-accent-text/40'
          : 'border-border',
      )}
    >
      <Handle
        type="target"
        position={Position.Left}
        className="!h-1.5 !w-1.5 !border-0 !bg-transparent"
      />
      <span
        className={cn(
          'flex size-7 shrink-0 items-center justify-center rounded-md [&_svg]:size-4',
          data.isOrigin
            ? 'bg-accent-soft text-accent-soft-foreground'
            : 'bg-muted text-muted-foreground',
        )}
      >
        <Icon />
      </span>
      <div className="min-w-0">
        <div
          className="max-w-[180px] truncate font-mono text-xs text-foreground"
          title={data.label}
        >
          {data.label}
        </div>
        <div className="text-[10px] font-medium tracking-wide text-muted-foreground uppercase">
          {t(`wiring.nodeKinds.${data.kind}`, { defaultValue: data.kind })}
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

const wiringNodeTypes = { wiring: WiringNode }

/** observed = directly seen on the wire (e.g. otel); declared = only self-reported
 * by an MCP annotation. Declared-only edges are lower-trust → render dashed/muted. */
function isObserved(sources: string[]): boolean {
  return sources.some((s) => s !== 'mcp_annotation')
}

function nodeId(kind: string, ref: string): string {
  return `${kind}:${ref}`
}

export function WiringGraph({ graph }: { graph: WiringGraphDTO }) {
  const { t } = useTranslation(['capabilities', 'shared'])
  const [selectedEdge, setSelectedEdge] = useState<WiringEdgeDTO | null>(null)

  const { nodes, edges } = useMemo(() => {
    const originIds = new Set(
      graph.edges.map((e) => nodeId(e.origin_kind, e.origin_ref)),
    )
    const layoutNodes = graph.nodes.map((n) => ({
      id: nodeId(n.kind, n.ref),
      layer: originIds.has(nodeId(n.kind, n.ref)) ? 0 : 1,
    }))
    const layoutEdges = graph.edges.map((e) => ({
      source: nodeId(e.origin_kind, e.origin_ref),
      target: nodeId(e.capability_kind, e.capability_ref),
    }))
    const layout = layeredLayout(layoutNodes, layoutEdges)

    const rfNodes: Node<WiringNodeData>[] = graph.nodes.map((n) => {
      const id = nodeId(n.kind, n.ref)
      return {
        id,
        type: 'wiring',
        position: layout.positions[id] ?? { x: 0, y: 0 },
        data: { kind: n.kind, label: n.ref, isOrigin: originIds.has(id) },
      }
    })

    const rfEdges: Edge[] = graph.edges.map((e, i) => {
      const observed = isObserved(e.signal_sources)
      return {
        id: `e${i}`,
        source: nodeId(e.origin_kind, e.origin_ref),
        target: nodeId(e.capability_kind, e.capability_ref),
        animated: false,
        markerEnd: { type: MarkerType.ArrowClosed },
        style: observed
          ? { stroke: 'var(--color-accent-text)', strokeWidth: 1.5 }
          : {
              stroke: 'var(--color-graphite-400)',
              strokeWidth: 1.5,
              strokeDasharray: '5 4',
            },
        data: { edge: e },
      }
    })
    return { nodes: rfNodes, edges: rfEdges }
  }, [graph])

  if (graph.nodes.length === 0) {
    return (
      <EmptyState
        title={t('shared:graph.empty')}
        description={t('shared:graph.emptyHint')}
      />
    )
  }

  return (
    <div className="flex flex-col gap-3">
      <p className="text-xs text-muted-foreground">
        {t('wiring.notAccessMap')}
      </p>
      {graph.truncated && (
        <p className="rounded-md border border-warning-line bg-warning-soft px-3 py-2 text-xs text-warning">
          {t('wiring.partial')}
        </p>
      )}
      {/* The graph degrades to a summary on mobile — a force-directed canvas is
          not usable at phone width (dignified degradation). */}
      <div className="rounded-lg border border-border bg-surface p-6 text-center md:hidden">
        <Box
          className="mx-auto mb-2 size-6 text-muted-foreground"
          aria-hidden
        />
        <p className="text-sm text-muted-foreground">
          {t('wiring.mobileHint', { count: graph.edges.length })}
        </p>
      </div>
      <div className="relative hidden h-[560px] md:block">
        <GraphCanvas
          nodes={nodes}
          edges={edges}
          nodeTypes={wiringNodeTypes}
          ariaLabel={t('wiring.title')}
          onEdgeClick={(e) =>
            setSelectedEdge((e.data as { edge: WiringEdgeDTO })?.edge ?? null)
          }
          onPaneClick={() => setSelectedEdge(null)}
          fitKey={`${nodes.length}:${edges.length}`}
        >
          {/* Provenance legend. */}
          <div className="absolute top-3 left-3 z-10 flex flex-col gap-1 rounded-md border border-border bg-elevated/90 px-2.5 py-2 text-xs backdrop-blur-sm">
            <span className="font-medium text-foreground">
              {t('wiring.legend')}
            </span>
            <span className="flex items-center gap-1.5 text-muted-foreground">
              <span className="h-0 w-5 border-t-2 border-accent-text" />
              {t('wiring.observed')}
            </span>
            <span className="flex items-center gap-1.5 text-muted-foreground">
              <span className="h-0 w-5 border-t-2 border-dashed border-graphite-400" />
              {t('wiring.declared')}
            </span>
          </div>

          {selectedEdge && (
            <div className="absolute right-3 bottom-3 z-10 w-[min(18rem,calc(100%-1.5rem))] rounded-lg border border-border bg-elevated/95 p-3 shadow-md backdrop-blur-sm">
              <div className="mb-1 truncate font-mono text-xs text-foreground">
                {selectedEdge.origin_ref}
                <span className="px-1 text-muted-foreground">→</span>
                {selectedEdge.tool_ref || selectedEdge.capability_ref}
              </div>
              <KvList>
                <KvRow label={t('wiring.legend')}>
                  {isObserved(selectedEdge.signal_sources)
                    ? t('wiring.observed')
                    : t('wiring.declared')}
                </KvRow>
                <KvRow label={t('wiring.occurrences')} mono>
                  {selectedEdge.occurrence_count}
                </KvRow>
                <KvRow label={t('wiring.firstSeen')}>
                  <RelTimeLabel ts={selectedEdge.first_seen} />
                </KvRow>
                <KvRow label={t('wiring.lastSeen')}>
                  <RelTimeLabel ts={selectedEdge.last_seen} />
                </KvRow>
              </KvList>
              <p className="mt-1 truncate text-[10px] text-muted-foreground">
                {t('wiring.via')} {selectedEdge.signal_sources.join(', ')}
              </p>
            </div>
          )}
        </GraphCanvas>
      </div>
    </div>
  )
}
