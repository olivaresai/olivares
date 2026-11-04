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
  Folder,
  Globe,
  KeyRound,
  type LucideIcon,
  Radio,
  Server,
  Wrench,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { useSessionNames } from '@/features/shared'
import { cn } from '@/lib/utils'
import type { OriginNodeData, ResourceNodeData } from './graph-model'
import type { GroupNodeData } from './groups'

type Grouped = Partial<GroupNodeData>

/** A node that stands for several looks like a small stack of cards. */
const STACK =
  'shadow-[4px_4px_0_-1px_var(--color-surface),4px_4px_0_0_var(--color-border)]'

/** The name and the second line a group or "+N more" node paints. */
function useGroupText(data: Grouped & { label: string }) {
  const { t } = useTranslation('accessMap')
  if (!data.cluster) return null
  const count = data.clusterCount ?? 0
  return data.more
    ? { name: t('group.more', { count }), line: null }
    : { name: data.label, line: t('group.items', { count }) }
}

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
  folder: Folder,
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
}: NodeProps<Node<OriginNodeData & Grouped>>) {
  const { t } = useTranslation('accessMap')
  const sessionNames = useSessionNames()
  const group = useGroupText(data)
  const sessionName =
    data.kind === 'session' && !group ? sessionNames.nameOf(data.ref) : null
  const Icon = ORIGIN_ICON[data.kind] ?? Bot
  return (
    <div
      title={data.ref}
      className={cn(
        'flex items-center gap-2 rounded-lg border bg-elevated px-3 py-2 shadow-sm transition-opacity',
        'border-border',
        group && STACK,
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
        {/* ⛔ A SESSION NODE IS NAMED BY WHAT IT WAS DOING. An origin that is an agent
            or an identity carries a name a person chose; a session carries only the
            reference the ingest stream gave it, and the census counted two of them on
            this map. The live page says what the session was doing; where it says
            nothing — and for a reader without `sessions:live:read` — the reference
            stands, because on a graph a node with no label at all is worse than a
            node labelled by its reference. The reference is on `title` either way. */}
        <div
          className={cn(
            'max-w-[170px] whitespace-normal text-caption [overflow-wrap:anywhere] text-foreground',
            !sessionName && !group && 'font-mono',
            group && 'font-medium',
          )}
        >
          {group?.name ?? sessionName ?? data.label}
        </div>
        {(!group || group.line) && (
          <div className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
            {group?.line ??
              t(`kinds.${data.kind}`, { defaultValue: data.kind })}
          </div>
        )}
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
}: NodeProps<Node<ResourceNodeData & Grouped>>) {
  const { t } = useTranslation('accessMap')
  const group = useGroupText(data)
  const Icon =
    RESOURCE_ICONS[
      group && data.ref.includes('/') && !data.ref.includes('://')
        ? 'folder'
        : resourceIconKey(data.resourceKind)
    ]
  const tier = group ? undefined : data.coverageTier
  return (
    <div
      title={data.ref}
      className={cn(
        group && STACK,
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
          className={cn(
            'max-w-[190px] whitespace-normal text-caption [overflow-wrap:anywhere] text-foreground',
            group ? 'font-medium' : 'font-mono',
          )}
        >
          {group?.name ?? data.label}
        </div>
        <div className="flex items-center gap-1.5">
          {(!group || group.line) && (
            <span className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
              {group?.line ??
                t(`kinds.${data.resourceKind}`, {
                  defaultValue: data.resourceKind ?? 'resource',
                })}
            </span>
          )}
          {tier && (
            <span
              className={cn(
                'rounded-sm border px-1 text-xs font-medium uppercase leading-tight',
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
