// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ListTruncationBadge } from '@/features/_intel'
import { AlertTriangle, EyeOff } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { RelTimeLabel } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { cn } from '@/lib/utils'
import { healthApi, healthKeys, type IncidentParams } from './api'
import type { IncidentDTO } from './types'

const ALL = '__all__'

/** Severity → a Badge variant (best-effort; the engine's severity is a free string). */
const SEVERITY_VARIANT: Record<string, BadgeVariant> = {
  critical: 'danger',
  high: 'danger',
  medium: 'warning',
  low: 'info',
  info: 'neutral',
}

/** A coarse rank so the table can sort "worst first" on a free-string severity. */
const SEVERITY_RANK: Record<string, number> = {
  critical: 4,
  high: 3,
  medium: 2,
  low: 1,
  info: 0,
}

/** A down summary mentioning the sweep is silence-within-cadence — possible evasion
 * (docs UI-CONTRACT-HEALTH §8). The engine carries no error text, but a sweep-caused
 * down is reflected in the incident's short summary, so we surface the framing. */
function looksLikeEvasion(inc: IncidentDTO): boolean {
  if (inc.kind !== 'down') return false
  return /sweep|silent|stale/i.test(inc.summary ?? '')
}

const INCIDENT_LIMIT = 1000

export function Incidents({ tenant }: { tenant: string | null }) {
  const { t } = useTranslation('health')
  const { can } = useAuth()
  const canResolve = can('health:check:admin')
  const queryClient = useQueryClient()
  const report = useFailedActionReporter()
  const [state, setState] = useState<string>('open')

  // El techo real del motor es `maxLimit = 1000`; pedir más no trae más y no pedir nada
  // trae 100 en silencio. Se pide el máximo y se DECLARA el recorte, con el mismo gesto
  // que el mapa de dependencias ya usa en esta misma feature (dependency-map.tsx).
  const params: IncidentParams =
    state === ALL ? { limit: INCIDENT_LIMIT } : { state, limit: INCIDENT_LIMIT }
  const query = useQuery({
    queryKey: healthKeys.incidents(tenant, params),
    queryFn: () => healthApi.incidents(params),
  })

  const resolve = useMutation({
    mutationFn: (id: string) => healthApi.resolveIncident(id),
    onSuccess: () => {
      // Invalidate every incidents query for this tenant (any state filter).
      void queryClient.invalidateQueries({
        queryKey: ['health', tenant, 'incidents'],
      })
    },
    // ⛔ SIN ESTO, RESOLVER UN INCIDENTE FALLABA EN SILENCIO.
    // Medido el 2026-08-15: de las 128 mutaciones de la consola, ésta era la ÚNICA sin `onError`
    // ni lectura de `isError`. El operador pulsaba «resolver», el POST devolvia 403/409/500, la
    // fila se quedaba igual y la pantalla no decia nada — que es indistinguible de «aun no ha
    // cargado». En una vista de INCIDENTES eso es lo peor que puede pasar: quien la usa cree que
    // ha cerrado algo que sigue abierto.
    onError: (error) => {
      report(error)
    },
  })

  const rows = useMemo(() => query.data?.items ?? [], [query.data])

  const columns = useMemo<TableColumn<IncidentDTO>[]>(
    () => [
      {
        id: 'subject',
        accessorKey: 'subject_ref',
        header: t('incidents.cols.subject'),
        cell: ({ row }) => {
          const inc = row.original
          const evasion = looksLikeEvasion(inc)
          return (
            <div className="flex items-center gap-2">
              <div className="min-w-0">
                <div
                  className="truncate font-mono text-xs text-foreground"
                  title={inc.subject_ref}
                >
                  {inc.subject_ref}
                </div>
                {inc.summary && (
                  <div
                    className="truncate text-xs text-muted-foreground"
                    title={inc.summary}
                  >
                    {inc.summary}
                  </div>
                )}
              </div>
              {evasion && (
                <span
                  className="inline-flex shrink-0 items-center gap-1 rounded-sm border border-warning-line bg-warning-soft px-1 py-0.5 text-[10px] font-medium text-warning"
                  title={t('incidents.possibleEvasionHint')}
                >
                  <EyeOff className="size-3" />
                  {t('incidents.possibleEvasion')}
                </span>
              )}
            </div>
          )
        },
      },
      {
        accessorKey: 'subject_kind',
        header: t('incidents.cols.subjectKind'),
        cell: ({ row }) => (
          <Badge variant="outline">
            {t(`subjectKind.${row.original.subject_kind}`, {
              defaultValue: row.original.subject_kind,
            })}
          </Badge>
        ),
      },
      {
        id: 'incidentKind',
        accessorKey: 'kind',
        header: t('incidents.cols.incidentKind'),
        cell: ({ getValue }) => {
          const k = getValue<string>()
          const variant: BadgeVariant =
            k === 'down' ? 'danger' : k === 'sla_breach' ? 'danger' : 'warning'
          return (
            <Badge variant={variant}>
              {t(`incidents.kind.${k}`, { defaultValue: k })}
            </Badge>
          )
        },
      },
      {
        id: 'severity',
        accessorFn: (inc) => SEVERITY_RANK[inc.severity?.toLowerCase()] ?? -1,
        header: t('incidents.cols.severity'),
        cell: ({ row }) => {
          const sev = row.original.severity ?? ''
          const variant = SEVERITY_VARIANT[sev.toLowerCase()] ?? 'neutral'
          return <Badge variant={variant}>{sev || '—'}</Badge>
        },
      },
      {
        accessorKey: 'opened_at',
        header: t('incidents.cols.opened'),
        cell: ({ getValue }) => <RelTimeLabel ts={getValue<string>()} />,
      },
      {
        id: 'lifecycle',
        accessorKey: 'state',
        header: t('incidents.cols.state'),
        cell: ({ row }) => {
          const inc = row.original
          if (inc.state === 'resolved') {
            return (
              <span className="text-xs text-muted-foreground">
                <RelTimeLabel ts={inc.resolved_at} />
              </span>
            )
          }
          return <Badge variant="warning">{t('incidents.open')}</Badge>
        },
      },
      {
        id: 'action',
        header: t('incidents.cols.action'),
        enableSorting: false,
        cell: ({ row }) => {
          const inc = row.original
          if (inc.state !== 'open' || !canResolve) return null
          const isResolving = resolve.isPending && resolve.variables === inc.id
          return (
            <Button
              variant="secondary"
              size="sm"
              title={t('incidents.resolveHint')}
              disabled={resolve.isPending}
              onClick={(e) => {
                e.stopPropagation()
                resolve.mutate(inc.id)
              }}
            >
              {isResolving ? (
                <>
                  <Spinner size="sm" /> {t('incidents.resolving')}
                </>
              ) : (
                t('incidents.resolve')
              )}
            </Button>
          )
        },
      },
    ],
    [t, canResolve, resolve],
  )

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <AlertTriangle className="size-3.5 shrink-0" />
          {t('incidents.resolveHint')}
        </p>
        <Select value={state} onValueChange={setState}>
          <SelectTrigger
            className="h-7 w-auto min-w-[8rem] text-xs"
            aria-label={t('incidents.allStates')}
          >
            <SelectValue placeholder={t('incidents.allStates')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="open">{t('incidents.open')}</SelectItem>
            <SelectItem value="resolved">{t('incidents.resolved')}</SelectItem>
            <SelectItem value={ALL}>{t('incidents.allStates')}</SelectItem>
          </SelectContent>
        </Select>
      </div>

      {/* ⛔ EL RECORTE SE DICE. Con más incidentes que el techo, la tabla se ve completa y
          no lo está: en una vista de salud «no hay más incidentes abiertos» es exactamente
          la afirmación que un operador usa para irse a dormir. Mismo gesto que el mapa de
          dependencias de esta misma feature. */}
      {/* ⛔ `&& !query.error` NO SOBRA. TanStack conserva la última `data` junto al error
          nuevo, y `DataTable` sustituye las filas por el error: sin esta condición el
          aviso quedaba flotando sobre una tabla vacía, hablando de un recorte de datos
          que ya no se están enseñando. Lo devolvió el contraste con una sonda:
          `hasData:true, hasMore:true, isRefetchError:true`. */}
      <ListTruncationBadge
        query={query}
        label={t('incidents.truncated', { n: INCIDENT_LIMIT })}
        hint={t('incidents.truncatedHint')}
      />

      <DataTable
        columns={columns}
        data={rows}
        isLoading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        getRowId={(r) => r.id}
        searchable
        searchPlaceholder={t('incidents.searchPlaceholder')}
        stickyHeader
        empty={<EmptyIncidents />}
      />
    </div>
  )
}

function EmptyIncidents() {
  const { t } = useTranslation('health')
  return (
    <div className={cn('px-6 py-12 text-center')}>
      <p className="text-sm font-medium text-foreground">
        {t('incidents.empty.title')}
      </p>
      <p className="mx-auto mt-1.5 max-w-sm text-sm text-muted-foreground">
        {t('incidents.empty.description')}
      </p>
    </div>
  )
}
