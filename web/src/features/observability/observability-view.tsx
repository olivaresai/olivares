// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Observability (ADM-OBS-01) — the container. It wires the two tabs (ingestion health,
// trace drill-down) and composes the pure presentational pieces. LIVE since:
// modules/observability serves the per-standard/per-source ingestion read-model and
// the ledger-correlation trace read-model (W3C trace ids on audit-ledger events).
// Always-200 reads use <AsyncSection>; the trace detail may legitimately 404 for an
// unknown/ledger-evicted id, which renders as a retryable error state. The view
// PRESENTS, it never recomputes (ARCHITECTURE.md) and never fabricates.
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { Activity, Download, Search } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { isoMinuteBound, UrlStateNotice } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { useValidatedUrlState } from '@/lib/hooks/use-url-state'
import {
  AsyncSection,
  CaveatNotice,
  DisclaimerNote,
  IntelPage,
  SeamBadge,
  SectionCard,
} from '@/features/_intel'
import { observabilityApi, observabilityKeys } from './api'
import type { TraceListParams } from './api'
import {
  IngestionHealthTable,
  IngestionSourcesTable,
  SpanDetailPanel,
  TraceList,
  TraceWaterfall,
} from './components'
import type { TraceListItem, TraceSpan } from './types'
import './i18n'

/** The two tabs, as the URL may name them. Anything else falls back. */
const OUTER_TABS = ['ingestion', 'traces'] as const

/**
 * The outer decoder owns `tab`, but it READS the trace filter keys too — see
 * decodeOuterTab. Declaring them here does not make it write them; useUrlState
 * only ever writes the keys present in a patch.
 */
const OUTER_URL_KEYS = ['tab', 'q', 'service', 'status', 'from', 'to'] as const

/**
 * Which tab a link opens.
 *
 * The historical defect was not only that the tab was uncontrolled: the links
 * that exist in the wild carry TRACE FILTERS AND NO TAB — /observability?q=…
 * &service=… is what every pre share produced. Landing those on Ingestion
 * is the same failure, one layer down: the recipient sees a page that ignores
 * every parameter they were sent.
 *
 * `inferred` says the answer came from the filters rather than from an explicit
 * `tab`. The view CANONICALISES that once — see the effect below — and the
 * reason is worth stating, because two earlier attempts were wrong in opposite
 * directions. Re-deriving the tab from the current filters on every render tied
 * the tab TO the filters, so clearing the last one (including the hook's own
 * cleanup of a refused value) bounced the operator to Ingestion and unmounted
 * the notice explaining why. Freezing the inference at mount fixed that and
 * broke the mirror image: a later navigation to `?q=…` within the route kept
 * the stale Ingestion answer and reproduced the original defect. Writing the
 * inferred tab once makes it explicit from then on, so neither can happen.
 */
function decodeOuterTab(raw: Record<string, string | undefined>): {
  value: { tab: string; inferred: boolean }
  issues: string[]
} {
  const carriesTraceState = TRACE_URL_KEYS.some((k) => raw[k] !== undefined)
  const fallback = carriesTraceState ? 'traces' : 'ingestion'
  if (raw.tab === undefined) {
    return { value: { tab: fallback, inferred: carriesTraceState }, issues: [] }
  }
  const ok = (OUTER_TABS as readonly string[]).includes(raw.tab)
  return {
    value: { tab: ok ? raw.tab : fallback, inferred: false },
    issues: ok ? [] : ['tab'],
  }
}

export function ObservabilityView() {
  const { t } = useTranslation(['observability', 'common'])
  const { activeTenant } = useAuth()

  // The outer tab used to be Radix-uncontrolled, and Radix UNMOUNTS the inactive
  // TabsContent. So the one place URL state had shipped was unreachable by link:
  // /observability?q=…&service=… landed the recipient on Ingestion and the trace
  // filters did nothing until they clicked Traces. Half a feature, delivered.
  const [outer, patchOuter, outerIssues] = useValidatedUrlState(
    OUTER_URL_KEYS,
    decodeOuterTab,
  )

  // Canonicalise an inferred tab exactly once, with a replace. From then on the
  // tab is explicit, so neither the hook's cleanup of a refused filter nor a
  // later navigation can move it behind the operator's back.
  useEffect(() => {
    if (outer.inferred) patchOuter({ tab: outer.tab })
  }, [outer.inferred, outer.tab, patchOuter])

  return (
    <IntelPage
      icon={Activity}
      title={t('title')}
      description={t('description')}
      actions={<SeamBadge label={t('engineScope.badge')} />}
    >
      <UrlStateNotice issues={outerIssues} />
      <Tabs
        value={outer.tab}
        // An EXPLICIT choice is always written, even when it matches what the
        // page would have shown anyway. Clearing the key instead looked tidier
        // and trapped the operator: with a link that infers Traces, the absence
        // of `tab` resolves back to Traces, so clicking Ingestion did nothing.
        // A pristine view still carries no `tab` — nobody has chosen yet.
        onValueChange={(value) => patchOuter({ tab: value })}
      >
        <TabsList>
          <TabsTrigger value="ingestion">{t('tabs.ingestion')}</TabsTrigger>
          <TabsTrigger value="traces">{t('tabs.traces')}</TabsTrigger>
        </TabsList>

        <TabsContent value="ingestion" className="flex flex-col gap-4">
          <IngestionTab tenant={activeTenant} />
        </TabsContent>

        <TabsContent value="traces" className="flex flex-col gap-4">
          <TracesTab tenant={activeTenant} />
        </TabsContent>
      </Tabs>
    </IntelPage>
  )
}

// --- ingestion health tab ----------------------------------------------------

function IngestionTab({ tenant }: { tenant: string | null }) {
  const { t } = useTranslation('observability')

  const healthQ = useQuery({
    queryKey: observabilityKeys.ingestionHealth(tenant),
    queryFn: () => observabilityApi.ingestionHealth(),
  })

  return (
    <SectionCard
      title={t('ingestion.title')}
      description={t('ingestion.description')}
    >
      <div className="flex flex-col gap-3">
        <CaveatNotice tone="info">{t('engineScope.note')}</CaveatNotice>
        <CaveatNotice tone="neutral">{t('ingestion.liveNote')}</CaveatNotice>
        <AsyncSection query={healthQ} skeletonHeight={260}>
          {(data) => (
            <div className="flex flex-col gap-3">
              <IngestionHealthTable standards={data.standards} />
              {/* Ledger push is still blocked on the live-forwarder seam — the one
                  row whose status the backend reports as `blocked`. */}
              <CaveatNotice tone="warning">
                {t('ingestion.blockedNote')}
              </CaveatNotice>
              <h3 className="mt-2 text-sm font-medium text-foreground">
                {t('sources.title')}
              </h3>
              <p className="text-xs text-muted-foreground">
                {t('sources.description')}
              </p>
              <IngestionSourcesTable
                sources={data.sources}
                since={data.since}
              />
            </div>
          )}
        </AsyncSection>
      </div>
    </SectionCard>
  )
}

// --- trace drill-down tab ----------------------------------------------------

const TRACE_PAGE_SIZE = 50
const TRACE_URL_KEYS = ['q', 'service', 'status', 'from', 'to'] as const

/** URL values are untrusted. Keep free-text filters verbatim, accept only the
 * backend's emitted status, and normalize parseable timestamps to RFC3339 UTC.
 *
 * It also REPORTS what it refused. Dropping a bad status or an unparseable
 * bound in silence is what made a shared trace link indistinguishable from a
 * good one: the recipient's page looks the same and queries something else. */
function traceParamsFromUrl(state: Record<string, string | undefined>): {
  value: Omit<TraceListParams, 'cursor' | 'limit'>
  issues: string[]
} {
  const params: Omit<TraceListParams, 'cursor' | 'limit'> = {}
  const issues: string[] = []
  if (state.q) params.q = state.q
  if (state.service) params.service = state.service
  if (state.status !== undefined) {
    // 'unset' is the only status the backend emits for this filter.
    if (state.status === 'unset') params.status = state.status
    else issues.push('status')
  }
  for (const key of ['from', 'to'] as const) {
    if (state[key] === undefined) continue
    // The SHARED bound parser, not a local NaN check. A bare `new Date` accepts
    // 2026-02-30 and answers with the 2nd of March, so an impossible day was
    // being queried silently — the defect recordings had already been hardened
    // against while this copy kept it, which is the argument for one
    // implementation rather than two. Minute precision, because that is what
    // this view's control can show.
    const bound = isoMinuteBound(state[key] as string)
    if (bound === undefined) {
      issues.push(key)
      continue
    }
    params[key] = bound
  }
  return { value: params, issues }
}

function localDateTimeValue(value?: string): string {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  const pad = (part: number) => String(part).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(
    date.getDate(),
  )}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function rfc3339FromLocal(value: string): string | undefined {
  if (!value) return undefined
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? undefined : date.toISOString()
}

function TracesTab({ tenant }: { tenant: string | null }) {
  const { t } = useTranslation('observability')
  const { can } = useAuth()
  const [selected, setSelected] = useState<string | null>(null)
  const [selectedSpan, setSelectedSpan] = useState<TraceSpan | null>(null)
  const [filters, patchUrlState, filterIssues] = useValidatedUrlState(
    TRACE_URL_KEYS,
    traceParamsFromUrl,
  )

  const canDrill = can('observability:traces:read')

  const listParams = useMemo<TraceListParams>(
    () => ({ limit: TRACE_PAGE_SIZE, ...filters }),
    [filters],
  )
  const listQ = useInfiniteQuery({
    queryKey: observabilityKeys.traces(tenant, listParams),
    queryFn: ({ pageParam }) =>
      observabilityApi.traces({
        ...listParams,
        cursor: pageParam,
      }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) =>
      last.has_more && last.cursor ? last.cursor : undefined,
  })
  const traces = useMemo(
    () => listQ.data?.pages.flatMap((page) => page.items) ?? [],
    [listQ.data],
  )
  const filtersActive = Object.keys(filters).length > 0
  const detailQ = useQuery({
    queryKey: observabilityKeys.trace(tenant, selected ?? ''),
    queryFn: () => observabilityApi.trace(selected as string),
    enabled: selected !== null,
  })

  const handleExport = useCallback(async () => {
    if (!selected) return
    const data = await observabilityApi.exportTrace(selected)
    const blob = new Blob([JSON.stringify(data, null, 2)], {
      type: 'application/json',
    })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `trace-${selected.slice(0, 16)}.json`
    a.click()
    URL.revokeObjectURL(url)
  }, [selected])

  return (
    <>
      <CaveatNotice tone="info">{t('traces.liveNote')}</CaveatNotice>

      <SectionCard
        title={t('traces.listTitle')}
        description={t('traces.listDescription')}
        noPadding
      >
        <div className="p-4">
          {/* Beside the filters it is about: a rejected bound or status now
              names itself instead of vanishing, so a shared trace link cannot
              look identical to a good one while querying something else. */}
          <div className="mb-3">
            <UrlStateNotice issues={filterIssues} />
          </div>
          <div className="mb-3 grid gap-2 sm:grid-cols-2 lg:grid-cols-5">
            <div className="relative">
              <Search className="absolute top-1/2 left-2.5 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
              <input
                type="text"
                value={filters.q ?? ''}
                onChange={(e) => patchUrlState({ q: e.target.value })}
                placeholder={t('traces.searchPlaceholder')}
                className="h-8 w-full rounded-md border bg-background pl-8 pr-3 text-xs focus:outline-none focus:ring-1 focus:ring-ring"
                aria-label={t('traces.searchPlaceholder')}
              />
            </div>
            <input
              type="text"
              value={filters.service ?? ''}
              onChange={(e) => patchUrlState({ service: e.target.value })}
              placeholder={t('traces.filters.servicePlaceholder')}
              className="h-8 w-full rounded-md border bg-background px-3 text-xs focus:outline-none focus:ring-1 focus:ring-ring"
              aria-label={t('traces.filters.service')}
            />
            <select
              value={filters.status ?? ''}
              onChange={(e) => patchUrlState({ status: e.target.value })}
              className="h-8 w-full rounded-md border bg-background px-3 text-xs text-foreground focus:outline-none focus:ring-1 focus:ring-ring"
              aria-label={t('traces.filters.status')}
            >
              <option value="">{t('traces.filters.allStatuses')}</option>
              <option value="unset">{t('traces.status.unset')}</option>
            </select>
            <label className="flex items-center gap-2">
              <span className="shrink-0 text-xs text-muted-foreground">
                {t('traces.filters.from')}
              </span>
              <input
                type="datetime-local"
                value={localDateTimeValue(filters.from)}
                onChange={(e) =>
                  patchUrlState({ from: rfc3339FromLocal(e.target.value) })
                }
                className="h-8 min-w-0 flex-1 rounded-md border bg-background px-2 text-xs focus:outline-none focus:ring-1 focus:ring-ring"
                aria-label={t('traces.filters.from')}
              />
            </label>
            <label className="flex items-center gap-2">
              <span className="shrink-0 text-xs text-muted-foreground">
                {t('traces.filters.to')}
              </span>
              <input
                type="datetime-local"
                value={localDateTimeValue(filters.to)}
                onChange={(e) =>
                  patchUrlState({ to: rfc3339FromLocal(e.target.value) })
                }
                className="h-8 min-w-0 flex-1 rounded-md border bg-background px-2 text-xs focus:outline-none focus:ring-1 focus:ring-ring"
                aria-label={t('traces.filters.to')}
              />
            </label>
          </div>
          <AsyncSection query={listQ} skeletonHeight={160}>
            {() =>
              traces.length === 0 ? (
                <EmptyState
                  title={t('traces.empty')}
                  description={
                    filtersActive
                      ? t('traces.filterNoResults')
                      : t('traces.emptyHint')
                  }
                />
              ) : (
                <div className="flex flex-col gap-3">
                  <TraceList
                    traces={traces}
                    onSelect={
                      canDrill
                        ? (trace: TraceListItem) => {
                            setSelected(trace.trace_id)
                            setSelectedSpan(null)
                          }
                        : undefined
                    }
                  />
                  {listQ.hasNextPage && (
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      className="self-center"
                      onClick={() => void listQ.fetchNextPage()}
                      disabled={listQ.isFetchingNextPage}
                    >
                      {listQ.isFetchingNextPage
                        ? t('traces.loadingMore')
                        : t('traces.loadMore')}
                    </Button>
                  )}
                </div>
              )
            }
          </AsyncSection>
        </div>
      </SectionCard>

      <SectionCard
        title={t('traces.waterfallTitle')}
        actions={
          canDrill && selected ? (
            <button
              onClick={handleExport}
              className="inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
              title={t('traces.exportTitle')}
            >
              <Download className="h-3.5 w-3.5" />
              {t('traces.exportBtn')}
            </button>
          ) : undefined
        }
      >
        {!canDrill || selected === null ? (
          <EmptyState title={t('traces.selectPrompt')} />
        ) : (
          <AsyncSection query={detailQ} skeletonHeight={220}>
            {(trace) => (
              <div className="flex flex-col gap-3">
                <TraceWaterfall trace={trace} onSelectSpan={setSelectedSpan} />
                {selectedSpan && (
                  <SpanDetailPanel
                    span={selectedSpan}
                    onClose={() => setSelectedSpan(null)}
                  />
                )}
              </div>
            )}
          </AsyncSection>
        )}
        <DisclaimerNote className="mt-3" text={t('traces.level1Note')} />
      </SectionCard>
    </>
  )
}
