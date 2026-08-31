// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Handle, type Node, type NodeProps, Position } from '@xyflow/react'
import {
  AlertTriangle,
  Bot,
  Box,
  Cloud,
  Database,
  FileText,
  Globe,
  KeyRound,
  type LucideIcon,
  Radio,
  Server,
  Wrench,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import type { OriginNodeData, ResourceNodeData } from './graph-model'

const ORIGIN_ICON: Record<string, LucideIcon> = {
  agent: Bot,
  session: Radio,
  identity: KeyRound,
}

const RESOURCE_ICONS = {
  database: Database,
  cloud: Cloud,
  tool: Wrench,
  server: Server,
  api: Globe,
  file: FileText,
  box: Box,
} as const

/** Map a resource kind (e.g. `postgres.table`, `s3.bucket`, `mcp.tool`) to an icon
 * KEY by its leading segment. Returning a key (not the component) and indexing
 * RESOURCE_ICONS at the call site keeps the lint from reading a function call as
 * "creating a component during render". */
function resourceIconKey(
  kind: string | undefined,
): keyof typeof RESOURCE_ICONS {
  const k = (kind ?? '').toLowerCase()
  if (
    k.startsWith('postgres') ||
    k.startsWith('mysql') ||
    k.startsWith('sqlite') ||
    k.includes('table') ||
    k.includes('.db')
  )
    return 'database'
  if (
    k.startsWith('s3') ||
    k.startsWith('gcs') ||
    k.includes('bucket') ||
    k.includes('object')
  )
    return 'cloud'
  if (
    k.startsWith('mcp.tool') ||
    k.startsWith('claude.tool') ||
    k.includes('tool')
  )
    return 'tool'
  if (k.startsWith('mcp')) return 'server'
  if (k.startsWith('http') || k.includes('api') || k.includes('url'))
    return 'api'
  if (k.startsWith('file') || k.includes('fs')) return 'file'
  if (k.startsWith('redis') || k.startsWith('mongo') || k.includes('store'))
    return 'database'
  return 'box'
}

const COVERAGE_VARIANT: Record<string, string> = {
  clean: 'border-success-line bg-success-soft text-success',
  lossy: 'border-warning-line bg-warning-soft text-warning',
  opaque: 'border-border bg-muted text-muted-foreground',
  mixed: 'border-info-line bg-info-soft text-info',
}

export function OriginNode({
  data,
  selected,
}: NodeProps<Node<OriginNodeData>>) {
  const { t } = useTranslation('accessMap')
  const Icon = ORIGIN_ICON[data.kind] ?? Bot
  return (
    <div
      className={cn(
        'flex items-center gap-2 rounded-lg border bg-elevated px-3 py-2 shadow-sm transition-opacity',
        'border-border',
        selected && 'border-accent-text ring-2 ring-accent-text/40',
        data.dimmed && 'opacity-40',
      )}
    >
      <Handle
        type="target"
        position={Position.Left}
        className="!h-1.5 !w-1.5 !border-0 !bg-transparent"
      />
      <span className="flex size-7 shrink-0 items-center justify-center rounded-md bg-accent-soft text-accent-soft-foreground [&_svg]:size-4">
        <Icon />
      </span>
      <div className="min-w-0">
        <div
          className="max-w-[170px] truncate font-mono text-xs text-foreground"
          title={data.label}
        >
          {data.label}
        </div>
        <div className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
          {t(`kinds.${data.kind}`, { defaultValue: data.kind })}
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

export function ResourceNode({
  data,
  selected,
}: NodeProps<Node<ResourceNodeData>>) {
  const { t } = useTranslation('accessMap')
  const Icon = RESOURCE_ICONS[resourceIconKey(data.resourceKind)]
  const tier = data.coverageTier
  return (
    <div
      className={cn(
        'flex items-center gap-2 rounded-lg border bg-surface px-3 py-2 shadow-sm transition-opacity',
        data.hasUnexpected
          ? 'border-danger ring-2 ring-danger/40'
          : 'border-border',
        selected &&
          !data.hasUnexpected &&
          'border-accent-text ring-2 ring-accent-text/40',
        data.dimmed && 'opacity-40',
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
          data.hasUnexpected
            ? 'bg-danger-soft text-danger'
            : 'bg-muted text-muted-foreground',
        )}
      >
        <Icon />
      </span>
      <div className="min-w-0">
        <div
          className="max-w-[190px] truncate font-mono text-xs text-foreground"
          title={data.label}
        >
          {data.label}
        </div>
        <div className="flex items-center gap-1.5">
          <span className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
            {t(`kinds.${data.resourceKind}`, {
              defaultValue: data.resourceKind ?? 'resource',
            })}
          </span>
          {tier && (
            <span
              className={cn(
                'rounded-sm border px-1 text-[9px] font-medium uppercase leading-tight',
                COVERAGE_VARIANT[tier] ?? COVERAGE_VARIANT.opaque,
              )}
              title={t(`coverageHint.${tier}`, { defaultValue: '' })}
            >
              {t(`coverage.${tier}`, { defaultValue: tier })}
            </span>
          )}
        </div>
      </div>
      {data.hasUnexpected && (
        <AlertTriangle className="size-3.5 shrink-0 text-danger" aria-hidden />
      )}
      <Handle
        type="source"
        position={Position.Right}
        className="!h-1.5 !w-1.5 !border-0 !bg-transparent"
      />
    </div>
  )
}

export const accessNodeTypes = { origin: OriginNode, resource: ResourceNode }
