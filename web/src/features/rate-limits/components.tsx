// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Rate Limits presentational pieces (ANT2-05) — PURE: props in, JSX out (no useQuery /
// useAuth). They present what the backend gave and recompute NOTHING (ARCHITECTURE.md):
//  • the count finding is rendered from its own `title` verbatim (the backend owns the
//    number — the UI never re-derives a count by parsing or by counting rows);
//  • workspace rows are overrides only; absent groups/limiters inherit org limits;
//  • each displayed row is one (group, limiter) pair from the backend DTO;
//  • `group_type` is OPEN vocabulary — unknown values render humanized, never rejected.
import { useMemo } from 'react'
import { Building2, CircleOff, Network } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import {
  CaveatNotice,
  IntelNotice,
  MetricStat,
  SeverityBadge,
  StatGrid,
} from '@/features/_intel'
import { formatDateTime, formatInt, humanize } from '@/lib/format'
import type { RateLimit, RateLimitFinding, RateLimitValue } from './types'

// --- the REAL count finding --------------------------------------------------

/** The live governance summary: how many rate limits a gateway/proxy must keep in
 *  sync. The COUNT lives in the finding's own `title` (backend-owned) — this presents
 *  that string verbatim, it does not parse or re-derive a number. */
export function RateLimitCountStat({ finding }: { finding: RateLimitFinding }) {
  const { t, i18n } = useTranslation('rateLimits')
  return (
    <StatGrid>
      <MetricStat
        icon={<Network />}
        label={t('count.label')}
        value={finding.title ?? t('count.unknown')}
        caption={
          finding.occurred_at
            ? t('count.observedAt', {
                at: formatDateTime(finding.occurred_at, i18n.language),
              })
            : undefined
        }
        aside={<SeverityBadge severity={finding.severity} />}
      />
    </StatGrid>
  )
}

// --- the mandatory verbatim caveats ------------------------------------------

/** The two documented caveats that must ALWAYS render, verbatim and prominently:
 *  gateways/proxies must mirror these limits, and Managed Agents are NOT covered by
 *  the Rate Limits API (a documented gap — NOT a zero, NOT an invented row). */
export function RateLimitCaveats() {
  const { t } = useTranslation('rateLimits')
  return (
    <div className="flex flex-col gap-2">
      <CaveatNotice tone="warning">{t('caveats.keepInSync')}</CaveatNotice>
      <CaveatNotice tone="info">{t('caveats.managedAgents')}</CaveatNotice>
    </div>
  )
}

// --- the degraded (no Admin API) state ---------------------------------------

/** Honest degradation: on a deploy surface without the Admin API (Bedrock Mantle/
 *  legacy, Vertex, Foundry) the governance ingest is structurally unavailable. Show
 *  this — never a fabricated empty inventory. */
export function IngestUnavailableNotice({
  finding,
}: {
  finding?: RateLimitFinding
}) {
  const { t } = useTranslation('rateLimits')
  return (
    <IntelNotice tone="warning" icon={<CircleOff />}>
      <span className="font-medium text-warning">
        {finding?.title ?? t('degraded.title')}
      </span>{' '}
      <span className="text-muted-foreground">{t('degraded.hint')}</span>
    </IntelNotice>
  )
}

// --- the LIVE inventory "unavailable" state (200, available=false) -----------

/** Honest unavailability for the LIVE inventory route (`GET /v1/m/models/rate-limits`
 *  answers 200 with `available=false`): the read-only Admin connector is unwired, or a
 *  fetch transiently failed. Show the backend's operator-facing `reason` — NEVER a
 *  fabricated empty inventory. Distinct from the now-removed 404 seam:
 *  the route IS live, it just has nothing authoritative to report on this surface. */
export function InventoryUnavailableNotice({ reason }: { reason?: string }) {
  const { t } = useTranslation('rateLimits')
  return (
    <IntelNotice tone="warning" icon={<CircleOff />}>
      <span className="font-medium text-warning">
        {t('inventory.unavailable')}
      </span>{' '}
      <span className="text-muted-foreground">
        {reason || t('inventory.unavailableHint')}
      </span>
    </IntelNotice>
  )
}

// --- the per-limiter inventory table -----------------------------------------

/** A scope tag derived ONLY from `workspace_ref` presence (org-wide vs a specific
 *  workspace) — this is grouping/labelling of given data, not a recomputed metric. */
function scopeOf(rl: RateLimit): 'org' | 'workspace' {
  return rl.workspace_ref ? 'workspace' : 'org'
}

type RateLimitRow = {
  group: RateLimit
  limiter: RateLimitValue
  index: number
}

function flattenLimitRows(limits: RateLimit[]): RateLimitRow[] {
  return limits.flatMap((group, groupIndex) =>
    group.limits.map((limiter, limiterIndex) => ({
      group,
      limiter,
      index: groupIndex * 1000 + limiterIndex,
    })),
  )
}

function inventoryColumns(
  t: ReturnType<typeof useTranslation>['t'],
  lang: string,
): TableColumn<RateLimitRow>[] {
  return [
    {
      id: 'scope',
      header: t('inventory.columns.scope'),
      // Sort/group org rows before per-workspace rows.
      accessorFn: (r) => (scopeOf(r.group) === 'org' ? 0 : 1),
      cell: ({ row }) => {
        const rl = row.original.group
        return scopeOf(rl) === 'org' ? (
          <Badge variant="neutral" className="gap-1">
            <Building2 className="size-3" />
            {t('inventory.scope.org')}
          </Badge>
        ) : (
          <Badge variant="outline" className="gap-1">
            <Network className="size-3" />
            <span className="font-mono text-[0.7rem]">{rl.workspace_ref}</span>
          </Badge>
        )
      },
    },
    {
      accessorFn: (r) => r.group.group_type,
      header: t('inventory.columns.groupType'),
      cell: ({ row }) => (
        // OPEN vocabulary: humanize whatever the provider sends, never reject it.
        <span className="text-foreground">
          {t(`groupType.${row.original.group.group_type}`, {
            defaultValue: humanize(row.original.group.group_type),
          })}
        </span>
      ),
    },
    {
      id: 'models',
      header: t('inventory.columns.models'),
      cell: ({ row }) => {
        const models = row.original.group.models
        if (!models?.length) {
          return <span className="text-muted-foreground">—</span>
        }
        const title = models.join(', ')
        return (
          <span
            className="block max-w-[20rem] truncate font-mono text-xs text-muted-foreground"
            title={title}
          >
            {title}
          </span>
        )
      },
    },
    {
      accessorFn: (r) => r.limiter.type,
      header: t('inventory.columns.limiterType'),
      cell: ({ row }) => (
        <span className="text-foreground">
          {t(`limitType.${row.original.limiter.type}`, {
            defaultValue: humanize(row.original.limiter.type),
          })}
        </span>
      ),
    },
    {
      accessorFn: (r) => r.limiter.value,
      header: t('inventory.columns.value'),
      cell: ({ row }) => (
        <span className="font-mono tabular-nums text-foreground">
          {formatInt(row.original.limiter.value, lang)}
        </span>
      ),
    },
    {
      accessorFn: (r) => r.limiter.org_limit ?? 0,
      header: t('inventory.columns.orgLimit'),
      cell: ({ row }) =>
        row.original.limiter.org_limit ? (
          <span className="font-mono tabular-nums text-muted-foreground">
            {formatInt(row.original.limiter.org_limit, lang)}
          </span>
        ) : (
          <span className="text-muted-foreground">—</span>
        ),
    },
  ]
}

export function RateLimitInventoryTable({ limits }: { limits: RateLimit[] }) {
  const { t, i18n } = useTranslation('rateLimits')
  const columns = useMemo(
    () => inventoryColumns(t, i18n.language),
    [t, i18n.language],
  )
  // Present one row per (group, limiter), org-scoped first, then per-workspace —
  // formatting only, not policy recomputation.
  const ordered = useMemo(
    () =>
      flattenLimitRows(limits).sort(
        (a, b) =>
          (scopeOf(a.group) === 'org' ? 0 : 1) -
          (scopeOf(b.group) === 'org' ? 0 : 1),
      ),
    [limits],
  )

  if (ordered.length === 0) {
    return (
      <EmptyState
        title={t('inventory.empty')}
        description={t('inventory.emptyHint')}
      />
    )
  }

  return (
    <DataTable<RateLimitRow>
      columns={columns}
      data={ordered}
      label={t('inventory.tableLabel')}
      getRowId={(r, i) =>
        `${r.group.workspace_ref || 'org'}:${r.group.group_type}:${r.limiter.type}:${r.index}:${i}`
      }
      empty={
        <EmptyState
          title={t('empty.rateLimitInventory.title')}
          description={t('empty.rateLimitInventory.description')}
        />
      }
    />
  )
}
