// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { Boxes, RefreshCw } from 'lucide-react'
import { useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { PageHeader } from '@/components/ui/page-header'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { AsyncSection, CaveatNotice, TruncatedNotice } from '@/features/_intel'
import { useAuth } from '@/lib/auth/context'
import { formatInt } from '@/lib/format'
import { cn } from '@/lib/utils'
import { inventoryApi, inventoryKeys } from './api'
import { CatalogTable } from './catalog-table'
import { EntityDetailSheet } from './entity-detail'
import { KIND_ORDER } from './entity-icons'
import { Topology } from './topology'
import type { CatalogEntry, InventorySummary } from './types'
import './i18n'

const ALL = '__all__'

/**
 * InventoryView — the estate catalog, READ TENANT-WIDE.
 *
 * ⛔ NO `useWorkspaceFilter` HERE, ON PURPOSE. The engine ignores `workspace_id` on
 * every Inventory route and the catalog has no workspace lineage (see api.ts), so
 * keying the summary by the topbar selector made a W1→W2 switch re-fetch the SAME
 * tenant-wide set and present it as if it were W2's. The view says so instead (the
 * scope notice below) and keys by tenant only. The selector itself is untouched:
 * other views do filter by it.
 */
export function InventoryView() {
  const { t } = useTranslation('inventory')
  const { activeTenant } = useAuth()
  const [kind, setKind] = useState<string>(ALL)
  const [status, setStatus] = useState<string>(ALL)

  const summary = useQuery({
    queryKey: inventoryKeys.summary(activeTenant),
    queryFn: () => inventoryApi.summary(),
  })

  /**
   * Kind facet options. A summary that FAILED, is still PENDING, or came back
   * TRUNCATED (aggregated over the first 1,000 entries — modules/inventory/api.go:123)
   * proves nothing about which kinds are absent, so every kind stays selectable;
   * retiring an option on that evidence would hide a whole class behind a guess.
   * Only a complete summary narrows the list — and it also widens it with any kind
   * the engine reports that this UI has no icon for, rather than dropping it.
   */
  const kindOptions = useMemo(() => {
    const byKind = summary.data?.by_kind
    const known = [...KIND_ORDER]
    if (byKind)
      for (const k of Object.keys(byKind)) if (!known.includes(k)) known.push(k)
    if (!byKind || summary.isError || summary.data?.truncated === true)
      return known
    return known.filter((k) => (byKind[k]?.total ?? 0) > 0)
  }, [summary.data, summary.isError])

  const facetKind = kind === ALL ? undefined : kind
  const facetStatus = status === ALL ? undefined : status

  return (
    <div className="flex h-full flex-col gap-4">
      <PageHeader
        icon={Boxes}
        title={t('title')}
        description={t('subtitle')}
        actions={
          <Button
            variant="ghost"
            size="sm"
            onClick={() => void summary.refetch()}
            disabled={summary.isFetching}
          >
            <RefreshCw
              className={cn('size-3.5', summary.isFetching && 'animate-spin')}
            />
            {t('refresh')}
          </Button>
        }
      />

      {/* Scope of the read, stated where the numbers are. This names what the
          QUERY covers — not access granted, not a complete estate (truncation is
          flagged separately below). */}
      <CaveatNotice tone="info">{t('scope.note')}</CaveatNotice>

      {/* Summary tiles. AsyncSection maps the query to the four honest states:
          pending → skeleton, 403 → calm forbidden (never a zero estate), failure →
          error with retry and request id, data → tiles. It branches on `isError`
          BEFORE `data`, so a refetch that fails after a success shows the failure,
          not yesterday's figures presented as current. A successful EMPTY estate is
          data, and renders its zeros. */}
      <AsyncSection query={summary} skeletonHeight={72}>
        {(data) => <SummaryTiles data={data} />}
      </AsyncSection>

      <Tabs defaultValue="catalog" className="min-h-0 flex-1">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <TabsList>
            <TabsTrigger value="catalog">{t('tabs.catalog')}</TabsTrigger>
            <TabsTrigger value="topology">{t('tabs.topology')}</TabsTrigger>
          </TabsList>
          <div className="flex items-center gap-2">
            <Select value={kind} onValueChange={setKind}>
              <SelectTrigger
                className="h-7 w-auto min-w-[8rem] text-caption"
                aria-label={t('facets.allKinds')}
              >
                <SelectValue placeholder={t('facets.allKinds')} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>{t('facets.allKinds')}</SelectItem>
                {kindOptions.map((k) => (
                  <SelectItem key={k} value={k}>
                    {t(`kinds.${k}`, { defaultValue: k })}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select value={status} onValueChange={setStatus}>
              <SelectTrigger
                className="h-7 w-auto min-w-[7rem] text-caption"
                aria-label={t('facets.allStatus')}
              >
                <SelectValue placeholder={t('facets.allStatus')} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>{t('facets.allStatus')}</SelectItem>
                <SelectItem value="active">{t('status.active')}</SelectItem>
                <SelectItem value="stale">{t('status.stale')}</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>

        {/* Keyed by tenant: the rows, the topology cards and the selected entry are
            owned by the tenant they were read in (see TenantEstate). The facets, the
            tab strip and the tiles above stay where they are across a switch. */}
        <TenantEstate
          key={activeTenant ?? 'none'}
          kind={facetKind}
          status={facetStatus}
        />
      </Tabs>
    </div>
  )
}

/**
 * TenantEstate — the estate of ONE tenant: its catalog rows, its topology cards, and
 * the entry selected among them, whose sheet reads the point under the same tenant.
 *
 * ⛔ THE SELECTION BELONGS TO THE TENANT IT WAS MADE IN. Until 2026-09-08 `selected`
 * lived in InventoryView beside the facets, and a CatalogEntry picked under T1 stayed
 * in state across a switch to T2 while the sheet's point read keyed by `activeTenant`:
 * T1's card stayed open and the read went out AS T2 with T1's kind and id. The same
 * id in T2 came back as T2's projection under T1's name; an id T2 does not hold came
 * back 404 and was shown as "no longer in the catalog". Not an authorization bypass —
 * the engine checks `inventory:catalog:read` per request in the tenant the request
 * names (modules/inventory/api.go) — but a card and a read that did not belong to the
 * tenant on screen.
 *
 * So the state moves into a component that InventoryView mounts with `key` = tenant
 * (the pattern sessions-workspace-view.tsx uses for the same reason: no row or open
 * card outlives a tenant switch). A different tenant is a different instance: the T1
 * instance unmounts — its sheet, its query observers — and the T2 instance starts with
 * nothing selected. Consequences that are the point:
 *
 *   · No T2 read ever carries a T1 selection, same id or not: the observer that would
 *     have re-keyed is gone before the new instance renders.
 *   · A T1 point read still in flight at the switch stays T1's — the transport reads
 *     the tenant header when the request is made (lib/api/client.ts) — and lands under
 *     T1's key with no observer. It is not shown, and a T1→T2→T1 bounce does not bring
 *     the selection back: the bounce is a third instance.
 *   · Reselecting in the current tenant is the only way to a card, and it always reads
 *     under that tenant.
 *
 * ⛔ NOT AN EFFECT THAT CLEARS `selected` ON `activeTenant`. TanStack applies a query's
 *    new key in the CHILD's own `useEffect` (useBaseQuery: `observer.setOptions`), and
 *    React runs a child's effects before its parent's — so on the render where the
 *    tenant changed the sheet would already have issued the T2 read with T1's id before
 *    the parent's effect retired the entry. The key retires the instance before that
 *    render exists.
 *
 * Losing the permission itself is decided one level up: RequirePermission re-evaluates
 * `can('inventory:catalog:read')` on every principal or tenant change and unmounts the
 * whole view — selection included — on a refusal. A 403 the engine returns between two
 * reflections is rendered by the sheet as the calm forbidden state (entity-detail.tsx).
 *
 * What a switch DOES retire with this instance, on purpose: the DataTable's sort and
 * search text, which were controls over rows that no longer exist. The kind and status
 * facets, the active tab and the summary tiles are InventoryView's and stay.
 *
 * Selection and focus destination are the same episode: the real activating node, a
 * local fallback if that node is gone, and a token so a prior close cannot steal focus.
 */
function isConnectedAndShown(el: HTMLElement): boolean {
  if (!el.isConnected) return false
  const view = el.ownerDocument.defaultView
  let node: HTMLElement | null = el
  while (node) {
    const style = view?.getComputedStyle(node)
    if (style && (style.display === 'none' || style.visibility === 'hidden')) {
      return false
    }
    node = node.parentElement
  }
  return true
}

function focusIfShown(el: HTMLElement | null | undefined): boolean {
  if (!el || !isConnectedAndShown(el)) return false
  el.focus({ preventScroll: true })
  return el.ownerDocument.activeElement === el
}

function TenantEstate({ kind, status }: { kind?: string; status?: string }) {
  const [episode, setEpisode] = useState<{
    entry: CatalogEntry
    token: number
  } | null>(null)
  const fallbackRef = useRef<HTMLDivElement>(null)
  const recRef = useRef<{
    token: number
    launcher: HTMLElement | null
    alive: boolean
  }>({ token: 0, launcher: null, alive: true })

  useLayoutEffect(() => {
    const rec = recRef.current
    rec.alive = true
    return () => {
      rec.alive = false
    }
  }, [])

  const selectEntry = (entry: CatalogEntry, launcher?: HTMLElement) => {
    recRef.current.token += 1
    recRef.current.launcher = launcher ?? null
    setEpisode({ entry, token: recRef.current.token })
  }

  const restoreFocus = (closedToken: number) => {
    const rec = recRef.current
    if (!rec.alive) return
    if (closedToken !== rec.token) return
    if (focusIfShown(rec.launcher)) return
    focusIfShown(fallbackRef.current)
  }

  return (
    <div
      ref={fallbackRef}
      tabIndex={-1}
      data-testid="inventory-estate-focus-fallback"
      className="min-h-0 outline-none"
    >
      <TabsContent value="catalog">
        <CatalogTable kind={kind} status={status} onSelect={selectEntry} />
      </TabsContent>
      <TabsContent value="topology">
        <Topology onSelect={selectEntry} />
      </TabsContent>
      <EntityDetailSheet
        entry={episode?.entry ?? null}
        episodeToken={episode?.token ?? 0}
        onClose={() => setEpisode(null)}
        onRestoreFocus={restoreFocus}
      />
    </div>
  )
}

/** The four headline figures of a summary the engine actually returned. */
function SummaryTiles({ data }: { data: InventorySummary }) {
  const { t } = useTranslation('inventory')
  const totals = useMemo(() => {
    let active = 0
    let stale = 0
    let kinds = 0
    for (const v of Object.values(data.by_kind ?? {})) {
      active += v.active
      stale += v.stale
      if (v.total > 0) kinds += 1
    }
    return { active, stale, kinds, total: data.total ?? active + stale }
  }, [data])

  return (
    <div className="flex flex-col gap-3">
      {/* `truncated` means the engine aggregated one bounded page and there was
          more: every figure below is a floor, so it is flagged as partial, and the
          kind facet above keeps every option (see kindOptions). Strict `=== true`:
          a cast is not a runtime check. */}
      {data.truncated === true ? <TruncatedNotice /> : null}
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
        <StatTile label={t('summary.total')} value={totals.total} />
        <StatTile
          label={t('summary.active')}
          value={totals.active}
          tone="success"
        />
        <StatTile
          label={t('summary.stale')}
          value={totals.stale}
          tone={totals.stale > 0 ? 'warning' : undefined}
        />
        <StatTile label={t('summary.kinds')} value={totals.kinds} />
      </div>
    </div>
  )
}

function StatTile({
  label,
  value,
  tone,
}: {
  label: string
  value: number
  tone?: 'success' | 'warning'
}) {
  return (
    <Card className="p-3">
      <div className="text-caption text-muted-foreground">{label}</div>
      <div
        className={cn(
          'font-display text-display tabular-nums',
          tone === 'success' && 'text-success',
          tone === 'warning' && 'text-warning',
          !tone && 'text-foreground',
        )}
      >
        {formatInt(value)}
      </div>
    </Card>
  )
}
