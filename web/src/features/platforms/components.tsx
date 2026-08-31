// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Platforms presentational pieces — PURE (props in, JSX out): no fetching, no auth.
// They render the surface/lifecycle reference (served live by GET /v1/m/models/
// platforms since) as matrices and honesty-stamped panels. They never recompute
// a value the data does not give and never present a to-confirm fact as
// authoritative (ARCHITECTURE.md).
import { useMemo } from 'react'
import { Check, CircleHelp, Minus } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { CaveatNotice } from '@/features/_intel'
import { formatCalendarDate } from '@/lib/format'
import { cn } from '@/lib/utils'
import type {
  ApiSupport,
  ConfirmStatus,
  LifecycleNote,
  ModelLifecycle,
  ModelRetirement,
  ParamDeprecation,
  Surface,
} from './types'

// --- honesty badge -----------------------------------------------------------

/** Confirm-status chip: "confirmed" reads calm-success, "to-confirm" reads as an
 *  open question (warning, with a help glyph) — never collapsed into a plain value. */
export function ConfirmStatusBadge({
  status,
  className,
}: {
  status: ConfirmStatus
  className?: string
}) {
  const { t } = useTranslation('platforms')
  const isConfirmed = status === 'confirmed'
  return (
    <Badge variant={isConfirmed ? 'success' : 'warning'} className={className}>
      {isConfirmed ? null : <CircleHelp className="size-3" />}
      {isConfirmed ? t('confirm.confirmed') : t('confirm.toConfirm')}
    </Badge>
  )
}

/** HIPAA value with its confirm status. A "to-confirm" posture is rendered as the
 *  to-confirm chip — NEVER a hard yes/no. "yes"/"no" show as the value
 *  plus a confirmed marker. */
export function HipaaCell({ surface }: { surface: Surface }) {
  const { t } = useTranslation('platforms')
  if (surface.hipaa_status !== 'confirmed') {
    return <ConfirmStatusBadge status="to-confirm" />
  }
  const yes = surface.hipaa === 'yes'
  return (
    <Badge variant={yes ? 'success' : 'neutral'}>
      {yes ? <Check className="size-3" /> : <Minus className="size-3" />}
      <span className="font-medium uppercase">{surface.hipaa}</span>
      <span className="sr-only">{t('confirm.confirmed')}</span>
    </Badge>
  )
}

// --- surface attribute matrix ------------------------------------------------

export function SurfaceMatrix({ surfaces }: { surfaces: Surface[] }) {
  // No date/number formatting in this matrix, so columns depend on `t` only.
  const { t } = useTranslation('platforms')
  const columns = useMemo<TableColumn<Surface>[]>(
    () => [
      {
        accessorKey: 'display_name',
        header: t('surfaces.columns.surface'),
        cell: ({ row }) => (
          <div className="flex min-w-0 flex-col gap-0.5">
            <span className="flex items-center gap-2 text-sm font-medium text-foreground">
              {row.original.display_name}
              {row.original.deprecated ? (
                <Badge variant="danger">{t('surfaces.deprecatedBadge')}</Badge>
              ) : null}
            </span>
            <span className="font-mono text-xs text-muted-foreground">
              {row.original.gateway}
            </span>
          </div>
        ),
      },
      {
        accessorKey: 'operator',
        header: t('surfaces.columns.operator'),
        cell: ({ row }) => (
          <Tooltip>
            <TooltipTrigger asChild>
              <span className="text-sm text-foreground">
                {row.original.operator}
              </span>
            </TooltipTrigger>
            <TooltipContent>{row.original.operator_data_access}</TooltipContent>
          </Tooltip>
        ),
      },
      {
        accessorKey: 'base_url_pattern',
        header: t('surfaces.columns.baseUrl'),
        cell: ({ row }) => (
          <span className="font-mono text-xs break-all text-muted-foreground">
            {row.original.base_url_pattern}
          </span>
        ),
      },
      {
        accessorKey: 'sigv4_service',
        header: t('surfaces.columns.sigv4'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.sigv4_service || t('surfaces.noSigv4')}
          </span>
        ),
      },
      {
        accessorKey: 'workspace_header',
        header: t('surfaces.columns.workspaceHeader'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.workspace_header || t('surfaces.noWorkspaceHeader')}
          </span>
        ),
      },
      {
        accessorKey: 'model_id_form',
        header: t('surfaces.columns.modelIdForm'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {row.original.model_id_form}
          </span>
        ),
      },
      {
        accessorKey: 'billing',
        header: t('surfaces.columns.billing'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {row.original.billing}
          </span>
        ),
      },
      {
        accessorKey: 'hipaa',
        header: t('surfaces.columns.hipaa'),
        cell: ({ row }) => <HipaaCell surface={row.original} />,
      },
      {
        id: 'fedramp',
        header: t('surfaces.columns.fedramp'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {/* FedRAMP/IL4-5 is declared only for the Bedrock surfaces (residency line,
                surfaces.go). It is NOT inferred for any other surface — honest gap. */}
            {row.original.gateway === 'bedrock-mantle' ||
            row.original.gateway === 'bedrock-legacy'
              ? t('surfaces.fedramp.eligible')
              : t('surfaces.fedramp.na')}
          </span>
        ),
      },
      {
        accessorKey: 'zdr',
        header: t('surfaces.columns.zdr'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {row.original.zdr}
          </span>
        ),
      },
      {
        accessorKey: 'residency',
        header: t('surfaces.columns.residency'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {row.original.residency}
          </span>
        ),
      },
    ],
    [t],
  )

  return (
    <DataTable<Surface>
      columns={columns}
      data={surfaces}
      getRowId={(r) => r.gateway}
      label={t('surfaces.matrixTitle')}
      empty={
        <EmptyState
          title={t('empty.surface.title')}
          description={t('empty.surface.description')}
        />
      }
    />
  )
}

// --- API-support matrix ------------------------------------------------------

const API_FAMILIES: (keyof ApiSupport)[] = [
  'messages',
  'admin',
  'compliance',
  'models',
  'batches',
  'mcp_connector',
]

function ApiCell({ on, label }: { on: boolean; label: string }) {
  return on ? (
    <Check
      role="img"
      className="mx-auto size-3.5 text-confidence-attributed"
      aria-label={label}
    />
  ) : (
    <Minus
      role="img"
      className="mx-auto size-3.5 text-border-strong"
      aria-label={label}
    />
  )
}

export function ApiSupportMatrix({ surfaces }: { surfaces: Surface[] }) {
  const { t } = useTranslation('platforms')
  const colKey: Record<keyof ApiSupport, string> = {
    messages: 'surfaces.columns.messages',
    admin: 'surfaces.columns.admin',
    compliance: 'surfaces.columns.compliance',
    models: 'surfaces.columns.models',
    batches: 'surfaces.columns.batches',
    mcp_connector: 'surfaces.columns.mcp',
  }
  return (
    <div className="overflow-x-auto rounded-lg border border-border bg-surface">
      <table
        className="w-full border-collapse text-sm"
        aria-label={t('surfaces.apiSupportTitle')}
      >
        <thead>
          <tr className="border-b border-border-strong">
            <th
              scope="col"
              className="sticky left-0 z-10 bg-muted px-3 py-2 text-left text-xs font-medium tracking-wide text-muted-foreground uppercase"
            >
              {t('surfaces.columns.surface')}
            </th>
            {API_FAMILIES.map((fam) => (
              <th
                key={fam}
                scope="col"
                className="bg-muted px-2 py-2 text-center text-[11px] font-medium whitespace-nowrap text-muted-foreground"
              >
                {t(colKey[fam])}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {surfaces.map((s) => (
            <tr
              key={s.gateway}
              className="border-b border-border last:border-0"
            >
              <th
                scope="row"
                className="sticky left-0 z-10 bg-surface px-3 py-2 text-left font-normal whitespace-nowrap"
              >
                <span className="font-mono text-xs text-foreground">
                  {s.gateway}
                </span>
              </th>
              {API_FAMILIES.map((fam) => (
                <td key={fam} className="px-2 py-2 text-center">
                  <ApiCell
                    on={s.apis[fam]}
                    label={
                      s.apis[fam]
                        ? t('surfaces.api.supported')
                        : t('surfaces.api.unsupported')
                    }
                  />
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// --- unmodeled surface (open Gateway string) ---------------------------------

/** An unmodeled gateway keeps its value and is labelled honestly — never dropped,
 *  never given a fabricated attribute matrix (surfaces.go SurfaceFor → ok=false). */
export function UnmodeledSurfaceNotice({ gateway }: { gateway: string }) {
  const { t } = useTranslation('platforms')
  return (
    <CaveatNotice tone="warning">
      <span className="font-medium text-warning">
        {t('surfaces.unmodeled')}
      </span>{' '}
      <span className="font-mono text-foreground">{gateway}</span>
      {' — '}
      <span className="text-muted-foreground">
        {t('surfaces.unmodeledHint')}
      </span>
    </CaveatNotice>
  )
}

// --- surface notes -----------------------------------------------------------

export function SurfaceNotes({ surfaces }: { surfaces: Surface[] }) {
  const { t } = useTranslation('platforms')
  return (
    <dl className="flex flex-col gap-3">
      {surfaces.map((s) => (
        <div key={s.gateway} className="flex flex-col gap-0.5">
          <dt className="flex items-center gap-2 text-sm font-medium text-foreground">
            {s.display_name}
            {s.deprecated ? (
              <Badge variant="danger">{t('surfaces.deprecatedBadge')}</Badge>
            ) : null}
          </dt>
          <dd className="text-xs leading-relaxed text-muted-foreground">
            {s.notes}
          </dd>
        </div>
      ))}
    </dl>
  )
}

// --- model lifecycle matrix --------------------------------------------------

interface LifecycleRow {
  id: string
  model_id: string
  model_display: string
  surface: string
  retirement: ModelRetirement
  isFirst: boolean
}

function flattenLifecycles(lifecycles: ModelLifecycle[]): LifecycleRow[] {
  const rows: LifecycleRow[] = []
  for (const lc of lifecycles) {
    lc.retirements.forEach((r, i) => {
      rows.push({
        id: `${lc.model_id}-${r.surface}`,
        model_id: lc.model_id,
        model_display: lc.display_name,
        surface: r.surface,
        retirement: r,
        isFirst: i === 0,
      })
    })
  }
  return rows
}

export function LifecycleMatrix({
  lifecycles,
}: {
  lifecycles: ModelLifecycle[]
}) {
  const { t, i18n } = useTranslation('platforms')
  const rows = useMemo(() => flattenLifecycles(lifecycles), [lifecycles])
  const columns = useMemo<TableColumn<LifecycleRow>[]>(
    () => [
      {
        accessorKey: 'model_display',
        header: t('lifecycle.columns.model'),
        cell: ({ row }) =>
          row.original.isFirst ? (
            <span className="flex flex-col">
              <span className="text-sm font-medium text-foreground">
                {row.original.model_display}
              </span>
              <span className="font-mono text-xs text-muted-foreground">
                {row.original.model_id}
              </span>
            </span>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        accessorKey: 'surface',
        header: t('lifecycle.columns.surface'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-foreground">
            {row.original.surface}
          </span>
        ),
      },
      {
        id: 'retiresOn',
        header: t('lifecycle.columns.retiresOn'),
        cell: ({ row }) => {
          const r = row.original.retirement
          return r.retires_on ? (
            <span className="font-mono tabular-nums text-foreground">
              {formatCalendarDate(r.retires_on, i18n.language)}
            </span>
          ) : (
            // NEVER "never retires": an unpublished date is an honest pending value.
            <span className="text-xs text-warning">
              {t('lifecycle.datePending')}
            </span>
          )
        },
      },
      {
        id: 'status',
        header: t('lifecycle.columns.status'),
        cell: ({ row }) => (
          <ConfirmStatusBadge status={row.original.retirement.status} />
        ),
      },
      {
        id: 'replacement',
        header: t('lifecycle.columns.replacement'),
        cell: ({ row }) =>
          row.original.retirement.replacement_ref ? (
            <span className="font-mono text-xs text-foreground">
              {row.original.retirement.replacement_ref}
            </span>
          ) : (
            // Empty replacement_ref = the authority named NO successor for this
            // family (claude-2.x) — none is invented.
            <Tooltip>
              <TooltipTrigger asChild>
                <Badge variant="outline">{t('lifecycle.noSuccessor')}</Badge>
              </TooltipTrigger>
              <TooltipContent>{t('lifecycle.noSuccessorHint')}</TooltipContent>
            </Tooltip>
          ),
      },
    ],
    [t, i18n.language],
  )

  return (
    <DataTable<LifecycleRow>
      columns={columns}
      data={rows}
      getRowId={(r) => r.id}
      label={t('lifecycle.matrixTitle')}
      empty={
        <EmptyState
          title={t('empty.lifecycle.title')}
          description={t('empty.lifecycle.description')}
        />
      }
    />
  )
}

// --- param-deprecation pre-advice --------------------------------------------

export function ParamDeprecationCard({ dep }: { dep: ParamDeprecation }) {
  const { t } = useTranslation('platforms')
  return (
    <CaveatNotice tone="info">
      <span className="inline-flex flex-wrap items-baseline gap-x-2 gap-y-1">
        <Badge variant="info">{t('lifecycle.paramDeprecation.badge')}</Badge>
        <span>
          {t('lifecycle.paramDeprecation.body', {
            affected: dep.affected,
            params: dep.params.join(t('lifecycle.paramDeprecation.paramsJoin')),
            status: dep.http_status,
          })}
        </span>
      </span>
    </CaveatNotice>
  )
}

// --- lifecycle / bedrock honesty notes ---------------------------------------

export function LifecycleNotes({ notes }: { notes: LifecycleNote[] }) {
  const { t } = useTranslation('platforms')
  return (
    <ul className="flex flex-col gap-2">
      {notes.map((n) => (
        <li
          key={n.key}
          className={cn(
            'flex items-start gap-2 rounded-md border border-border bg-surface px-3 py-2 text-xs leading-relaxed',
          )}
        >
          <ConfirmStatusBadge status={n.status} className="mt-px shrink-0" />
          <span className="min-w-0 text-muted-foreground">
            {t(`lifecycle.notes.${n.key}`)}
          </span>
        </li>
      ))}
    </ul>
  )
}
