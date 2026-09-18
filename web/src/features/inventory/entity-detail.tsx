// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery, type UseQueryResult } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Activity, Network } from 'lucide-react'
import { useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { KvList, KvRow } from '@/components/ui/kv'
import { Separator } from '@/components/ui/separator'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { AsyncSection, CaveatNotice } from '@/features/_intel'
import { useViewAccess } from '@/features/navigation/authorization'
import { FEATURE_VIEWS } from '@/features/registry'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { formatInt, humanize } from '@/lib/format'
import { inventoryApi, inventoryKeys } from './api'
import { Box, ENTITY_ICON } from './entity-icons'
import { EvidenceTime, ObservationHistory } from './observation-history'
import { InvStatus } from './status'
import type { CatalogEntry, EntityDetail } from './types'

const ORIGIN_KINDS = new Set(['agent', 'session', 'identity', 'resource'])

/** The registered destinations this sheet may offer — looked up once from the
 *  view table, never restated as a permission string or an ad-hoc role check. */
const ACCESS_MAP_VIEW = FEATURE_VIEWS.find((v) => v.id === 'accessMap')
const SESSIONS_VIEW = FEATURE_VIEWS.find((v) => v.id === 'sessions')

const POINT_READ_POLICY = {
  staleTime: 0,
  gcTime: 0,
  retry: false as const,
  refetchOnWindowFocus: false,
  refetchOnReconnect: false,
}

/** Render a core-detail value (primitives only — never an object/secret blob). */
function renderValue(v: unknown): string | null {
  if (v === null || v === undefined || v === '') return null
  if (typeof v === 'object') return null
  return String(v)
}

export function EntityDetailSheet({
  entry,
  onClose,
  episodeToken,
  onRestoreFocus,
}: {
  entry: CatalogEntry | null
  onClose: () => void
  episodeToken?: number
  onRestoreFocus?: (closedToken: number) => void
}) {
  const { t } = useTranslation('inventory')
  const { activeTenant } = useAuth()
  const { navigable } = useViewAccess()
  const open = entry !== null
  const pendingCloseTokenRef = useRef<number | null>(null)
  const Icon = entry ? (ENTITY_ICON[entry.kind] ?? Box) : Activity
  const identity = entry ? entry.name || entry.ref || entry.entity_id : ''
  // Kind eligibility is "this class of catalog entry has a legitimate next
  // room"; navigable() is whether that room is offered RIGHT NOW. The two are
  // not a verified edge: a generic /sessions or /access-map href never names
  // this entity and never claims a relationship the catalog has not shown.
  const showAccessMap =
    !!entry &&
    ORIGIN_KINDS.has(entry.kind) &&
    ACCESS_MAP_VIEW !== undefined &&
    navigable(ACCESS_MAP_VIEW)
  const showSessions =
    !!entry &&
    (entry.kind === 'session' || entry.kind === 'agent') &&
    SESSIONS_VIEW !== undefined &&
    navigable(SESSIONS_VIEW)

  return (
    <Sheet
      open={open}
      onOpenChange={(o) => {
        if (!o) {
          pendingCloseTokenRef.current = episodeToken ?? 0
          onClose()
        }
      }}
    >
      <SheetContent
        className="w-full min-w-0 sm:max-w-md"
        onCloseAutoFocus={(event) => {
          // Cancel implicit restore; the selection owner returns focus.
          event.preventDefault()
          const token = pendingCloseTokenRef.current ?? episodeToken ?? 0
          pendingCloseTokenRef.current = null
          onRestoreFocus?.(token)
        }}
      >
        {entry && (
          <>
            <SheetHeader className="pr-8">
              <SheetTitle className="flex min-w-0 items-start gap-2">
                <Icon className="mt-0.5 size-4 shrink-0 text-accent-text" />
                <span className="min-w-0 break-words">{identity}</span>
              </SheetTitle>
              <SheetDescription className="flex flex-wrap items-center gap-2">
                <Badge variant="outline" className="max-w-full break-words">
                  {t(`kinds.${entry.kind}`, { defaultValue: entry.kind })}
                </Badge>
              </SheetDescription>
            </SheetHeader>

            <EntityDetailBody
              key={`${activeTenant ?? 'none'}:${entry.kind}:${entry.entity_id}`}
              entry={entry}
              tenant={activeTenant}
              showAccessMap={showAccessMap}
              showSessions={showSessions}
            />
          </>
        )}
      </SheetContent>
    </Sheet>
  )
}

/**
 * Local body of the selected entity. Mounted only while a selection exists and
 * keyed by tenant/kind/id so close, a different entity, or TenantEstate's
 * tenant key retires both the point read and the history. The selection
 * identity titles the dialog; it does not stand in for a current point read.
 */
function EntityDetailBody({
  entry,
  tenant,
  showAccessMap,
  showSessions,
}: {
  entry: CatalogEntry
  tenant: string | null
  showAccessMap: boolean
  showSessions: boolean
}) {
  const { t } = useTranslation('inventory')
  const detailQuery = useQuery({
    queryKey: inventoryKeys.detail(tenant, entry.kind, entry.entity_id),
    queryFn: ({ signal }) =>
      inventoryApi.detail(entry.kind, entry.entity_id, { tenant, signal }),
    ...POINT_READ_POLICY,
  })

  return (
    <div className="min-w-0 overflow-y-auto">
      <KvList>
        {entry.ref && (
          <KvRow label={t('detail.ref')} mono align="start">
            <span className="break-all">{entry.ref}</span>
          </KvRow>
        )}
        <KvRow label={t('detail.id')} mono align="start">
          <span className="break-all">{entry.entity_id}</span>
        </KvRow>
        <KvRow label={t('cols.signals')} align="start">
          <span className="flex flex-wrap justify-end gap-1">
            {entry.signal_sources.map((s) => (
              <Badge key={s} variant="neutral" className="font-mono">
                {s}
              </Badge>
            ))}
          </span>
        </KvRow>
        {entry.hosts && entry.hosts.length > 0 && (
          <KvRow label={t('cols.hosts')} align="start">
            <span className="flex flex-wrap justify-end gap-1 font-mono text-caption">
              {entry.hosts.map((h) => (
                <span key={h} className="break-all">
                  {h}
                </span>
              ))}
            </span>
          </KvRow>
        )}
      </KvList>

      <PointReadSection query={detailQuery} />

      <ObservationHistory
        tenant={tenant}
        kind={entry.kind}
        id={entry.entity_id}
      />

      {(showAccessMap || showSessions) && (
        <>
          <Separator className="my-3" />
          <div className="flex flex-wrap gap-2">
            {showAccessMap && ACCESS_MAP_VIEW && (
              <Button variant="secondary" size="sm" asChild>
                {/* Feature routes are generated dynamically from the
                    registry, so their paths aren't in the static route
                    union — the shell uses the same `as never` escape
                    hatch (sidebar/command-menu). The href is the
                    registered path and nothing else: no entity id, no
                    implied edge. */}
                <Link to={ACCESS_MAP_VIEW.path as never}>
                  <Network className="size-3.5" />
                  {t('detail.viewAccess')}
                </Link>
              </Button>
            )}
            {showSessions && SESSIONS_VIEW && (
              <Button variant="secondary" size="sm" asChild>
                <Link to={SESSIONS_VIEW.path as never}>
                  <Activity className="size-3.5" />
                  {t('detail.viewSessions')}
                </Link>
              </Button>
            )}
          </div>
        </>
      )}
    </div>
  )
}

/**
 * Freshness and the core-entity projection of the CURRENT point read.
 *
 * ⛔ THE FAILURE IS SHOWN, NOT SWALLOWED. Until 2026-09-08 `detailQuery.error` was
 * never read: a 403, a 503 or a dropped connection rendered exactly like an entity
 * with no relations — the fields simply were not there. The point read has its own
 * authorization and its own failure modes, so its state is rendered with the same
 * mapping every other panel uses (AsyncSection): pending → skeleton, 403 → calm
 * forbidden, failure → error with retry and request id.
 *
 * C3 freshness uses `data.entry` of a successful current point read. Pending,
 * 403, 404 and error do not fall back to the selected catalog row as if it were
 * fresh. Selection identity may title the dialog; it does not accredit freshness.
 *
 * And an EMPTY list of printable fields is not a "no relations" verdict — nor is it
 * proof that the engine returned nothing. This branch is reached both when the
 * response omits `detail` (the engine drops it when its core getter fails,
 * modules/inventory/api.go:106 → coreDetail) and when `detail` is present but every
 * value is null, empty or structured, which `renderValue` does not print. The sheet
 * cannot tell those apart, so the copy states only what it knows: nothing additional
 * is available to display here, and that establishes nothing about relations.
 * (INV-R1, independent review of 2026-09-08.)
 *
 * 404 is handled before the generic mapping: the entry was listed a moment ago, so
 * "not found" on the point means the catalog no longer holds it (swept or retired),
 * which is a fact about the catalog, not a broken control plane.
 */
function PointReadSection({ query }: { query: UseQueryResult<EntityDetail> }) {
  const { t } = useTranslation('inventory')
  if (query.error instanceof ApiError && query.error.isNotFound) {
    return (
      <p role="status" className="mt-3 text-caption text-muted-foreground">
        {t('detail.gone')}
      </p>
    )
  }
  return (
    <AsyncSection query={query} skeletonHeight={72} className="mt-3">
      {(data) => (
        <>
          <FreshnessBlock entry={data.entry} />
          <CoreFields detail={data.detail} />
        </>
      )}
    </AsyncSection>
  )
}

function FreshnessBlock({ entry }: { entry: CatalogEntry }) {
  const { t } = useTranslation('inventory')
  return (
    <section
      className="mt-3 min-w-0"
      aria-labelledby="inventory-catalog-freshness"
    >
      <h3
        id="inventory-catalog-freshness"
        className="mb-2 text-caption font-semibold uppercase tracking-wide text-muted-foreground"
      >
        {t('freshness.title')}
      </h3>
      <CaveatNotice tone="info">{t('freshness.independent')}</CaveatNotice>
      <KvList>
        <KvRow label={t('freshness.storedState')} align="start">
          <span className="flex min-w-0 flex-col items-end gap-1">
            <InvStatus status={entry.status} />
            <span className="text-caption text-muted-foreground">
              {t('freshness.storedHint')}
            </span>
          </span>
        </KvRow>
        <KvRow label={t('freshness.firstLocalReception')} align="start">
          <EvidenceTime
            ts={entry.first_seen}
            unknown={t('freshness.timeUnknown')}
          />
        </KvRow>
        <KvRow label={t('freshness.localReception')} align="start">
          <EvidenceTime
            ts={entry.last_seen}
            unknown={t('freshness.timeUnknown')}
          />
        </KvRow>
        <KvRow label={t('freshness.sourceOccurred')} align="start">
          <EvidenceTime
            ts={entry.occurred_at}
            unknown={t('freshness.sourceOccurredUnknown')}
          />
        </KvRow>
        <KvRow label={t('freshness.occurrenceCount')} align="start">
          <span className="flex min-w-0 flex-col items-end gap-1">
            <span className="font-mono tabular-nums">
              {formatInt(entry.occurrence_count)}
            </span>
            <span className="text-caption text-muted-foreground">
              {t('freshness.occurrenceHint')}
            </span>
          </span>
        </KvRow>
      </KvList>
    </section>
  )
}

function CoreFields({ detail }: { detail?: Record<string, unknown> }) {
  const { t } = useTranslation('inventory')
  const fields = Object.entries(detail ?? {})
    .map(([k, v]) => [k, renderValue(v)] as const)
    .filter(([, v]) => v !== null)
  if (fields.length === 0) {
    return (
      <p className="mt-3 text-caption text-muted-foreground">
        {t('detail.noProjection')}
      </p>
    )
  }
  return (
    <>
      <h3 className="mt-3 mb-1 text-caption font-semibold uppercase tracking-wide text-muted-foreground">
        {t('detail.relations')}
      </h3>
      <KvList>
        {fields.map(([k, v]) => (
          <KvRow key={k} label={humanize(k)} mono align="start">
            {v}
          </KvRow>
        ))}
      </KvList>
    </>
  )
}
