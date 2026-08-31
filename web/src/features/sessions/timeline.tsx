// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useInfiniteQuery } from '@tanstack/react-query'
import {
  AlertTriangle,
  Coins,
  Plug,
  Wrench,
  type LucideIcon,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { AccessModeBadge } from '@/components/data/badges'
import { RelTimeLabel } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import { cn } from '@/lib/utils'
import { sessionsApi, sessionsKeys } from './api'
import type { TimelineDTO } from './types'
import './i18n'

const PAGE = 50
const ALL = '__all__'
const KINDS = ['tool', 'mcp', 'cost', 'finding'] as const

/** Icon + accent per timeline kind. Findings carry weight (warning); the rest stay calm. */
const KIND_META: Record<string, { icon: LucideIcon; tone: string }> = {
  tool: { icon: Wrench, tone: 'text-muted-foreground' },
  mcp: { icon: Plug, tone: 'text-info' },
  cost: { icon: Coins, tone: 'text-accent-text' },
  finding: { icon: AlertTriangle, tone: 'text-warning' },
}

/**
 * SessionTimeline — the session's reconstructible ingest order, keyset-paginated
 * (cursor + has_more). The `kind` facet filters server-side so the page only carries
 * the requested class; a "load more" walks the cursor. Each entry renders its time,
 * kind, refs and mode — never a payload (minimal-data, docs/SECURITY-HARDENING.md).
 */
export function SessionTimeline({ sessionRef }: { sessionRef: string }) {
  const { t } = useTranslation('sessions')
  const { activeTenant } = useAuth()
  const [kind, setKind] = useState<string>(ALL)
  const facetKind = kind === ALL ? undefined : kind

  const query = useInfiniteQuery({
    queryKey: sessionsKeys.timeline(activeTenant, sessionRef, { limit: PAGE }),
    queryFn: ({ pageParam }) =>
      sessionsApi.timeline(sessionRef, { limit: PAGE, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.cursor : undefined),
  })

  const allRows = useMemo(
    () => query.data?.pages.flatMap((p) => p.items) ?? [],
    [query.data],
  )
  // Kind filter is applied client-side over the loaded rows so the facet stays
  // instant and the keyset cursor is never invalidated mid-walk.
  const rows = useMemo(
    () => (facetKind ? allRows.filter((r) => r.kind === facetKind) : allRows),
    [allRows, facetKind],
  )

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between gap-2">
        <div className="min-w-0">
          <h3 className="text-sm font-semibold text-foreground">
            {t('timeline.title')}
          </h3>
          <p className="text-xs text-muted-foreground">
            {t('timeline.subtitle')}
          </p>
        </div>
        <Select value={kind} onValueChange={setKind}>
          <SelectTrigger
            className="h-7 w-auto min-w-[7rem] text-xs"
            aria-label={t('timeline.all')}
          >
            <SelectValue placeholder={t('timeline.all')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>{t('timeline.all')}</SelectItem>
            {KINDS.map((k) => (
              <SelectItem key={k} value={k}>
                {t(`kind.${k}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {query.isLoading ? (
        <div className="flex flex-col gap-2" data-testid="timeline-loading">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-9 w-full" />
          ))}
        </div>
      ) : query.error ? (
        query.error instanceof ApiError && query.error.isStepUpRequired ? (
          // ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es SÓLO el status 403
          // (lib/api/errors.ts:59) y un `step_up_required` lo satisface también, así que
          // leerlo primero acusaba al operador de un permiso que SÍ tiene, y sin salida.
          <StepUpRequiredState
            action="generic"
            // ⛔ ESTA CONSULTA ES INFINITA: «reintentar lo refusado» no es `refetch()` a
            // secas —eso relee la PÁGINA 0— ni «si hay páginas, la siguiente», que fue mi
            // primera versión y el contraste `sol max` la tumbó: con páginas en caché, un
            // refetch por montaje/foco que reciba la negativa se reintentaría como
            // `fetchNextPage`, o sea OTRA transición. TanStack ya distingue las dos
            // (`infiniteQueryObserver.ts:152-172`): `isFetchNextPageError` cuando la que
            // falló era la página siguiente, `isRefetchError` cuando era la relectura. Se
            // usa lo que la librería sabe, no lo que yo deduzca del número de páginas.
            onElevated={() => {
              if (query.isFetchNextPageError) void query.fetchNextPage()
              else void query.refetch()
            }}
          />
        ) : query.error instanceof ApiError && query.error.isForbidden ? (
          <ForbiddenState
            title={t('forbidden.title')}
            description={t('forbidden.description')}
          />
        ) : (
          <ErrorState retry={() => void query.refetch()} />
        )
      ) : rows.length === 0 ? (
        <EmptyState
          icon={<Wrench />}
          title={t('timeline.empty')}
          description={t('timeline.emptyHint')}
        />
      ) : (
        <ol className="flex flex-col" data-testid="timeline-list">
          {rows.map((row, i) => (
            <TimelineRow key={`${row.at}-${i}`} row={row} />
          ))}
        </ol>
      )}

      {/* Load-more walks the keyset cursor (only meaningful when not kind-filtered,
          but always offered while the server reports more pages). */}
      {query.hasNextPage && (
        <Button
          variant="ghost"
          size="sm"
          className="self-center"
          onClick={() => void query.fetchNextPage()}
          disabled={query.isFetchingNextPage}
        >
          {t('timeline.loadMore')}
        </Button>
      )}
    </div>
  )
}

function TimelineRow({ row }: { row: TimelineDTO }) {
  const { t } = useTranslation('sessions')
  const meta = KIND_META[row.kind] ?? KIND_META.tool
  const Icon = meta.icon
  const label = row.title || row.tool_ref || row.resource_ref || '—'
  return (
    <li className="flex items-start gap-2.5 border-b border-border py-2 last:border-0">
      <span
        className={cn(
          'mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-md bg-muted [&_svg]:size-3.5',
          meta.tone,
        )}
        aria-hidden
      >
        <Icon />
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-1.5">
          <Badge variant="outline" className="font-mono">
            {t(`kind.${row.kind}`, { defaultValue: row.kind })}
          </Badge>
          <span
            className="truncate text-sm font-medium text-foreground"
            title={label}
          >
            {label}
          </span>
          {row.mode && <AccessModeBadge mode={row.mode} />}
        </div>
        {(row.resource_ref && row.resource_ref !== label) || row.source ? (
          <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 font-mono text-xs text-muted-foreground">
            {row.resource_ref && row.resource_ref !== label && (
              <span className="truncate" title={row.resource_ref}>
                {row.resource_ref}
              </span>
            )}
            {row.source && (
              <span className="text-muted-foreground/80">
                {t('timeline.source')}: {row.source}
              </span>
            )}
          </div>
        ) : null}
      </div>
      <RelTimeLabel
        ts={row.at}
        className="shrink-0 text-xs text-muted-foreground"
      />
    </li>
  )
}
