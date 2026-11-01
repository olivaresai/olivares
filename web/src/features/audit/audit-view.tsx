// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE LEDGER PLACE (console Estate + Ledger, issue #2554): the tamper-evident ledger
// an auditor expects to see. The query lives in the top bar as tokens over the audit API's
// own filters (query.ts); under it a 48 px strip of entries per hour; then the entries as
// sentences, grouped by session, the request that needs a person first; and the picked
// entry beside them with its place in the chain. The header keeps the scope (superadmin),
// saved views and distribution-supplied evidence operations.
//
// HONESTY (ARCHITECTURE.md, docs/SECURITY-HARDENING.md): the web RENDERS the engine's events and verdicts.
// It never recomputes, repairs, or fabricates the chain; reading the ledger is itself
// audited (the engine appends audit.read / audit.export) and shows up in the list.
import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { FileCheck2 } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { internalRecord } from '@/components/layout/bell-events'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { SidePanelContent } from '@/components/layout/side-panel'
import { TopbarSearch } from '@/components/layout/topbar-search'
import { Button } from '@/components/ui/button'
import { PageHeader } from '@/components/ui/page-header'
import { Segmented } from '@/components/ui/segmented'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { usePendingApprovals } from '@/features/governance/use-pending-approvals'
import { SavedViewsMenu } from '@/features/saved-views'
import { auditApi } from '@/lib/api/endpoints'
import { queryKeys } from '@/lib/api/query'
import { useMinWidth } from '@/lib/hooks/use-min-width'
import type { AuditEventDTO } from '@/lib/api/types'
import { useAuth } from '@/lib/auth/context'
import { formatInt } from '@/lib/format'
import { useUrlState } from '@/lib/hooks/use-url-state'
import { useNewRowIds } from '@/lib/use-new-row-ids'
import { AuditEntryDetail } from './audit-detail'
import { EvidenceControls } from './evidence'
import { DAY, SESSION_KIND, useEntryNames } from './ledger'
import { LedgerEntries } from './ledger-entries'
import { LedgerHistogram } from './ledger-histogram'
import { LedgerQuery } from './ledger-query'
import {
  apiFilters,
  EMPTY_DRAFT,
  FILTER_KEYS,
  validBound,
  type LedgerDraft,
  type QueryParams,
} from './query'
import { type LedgerScope } from './types'
import './i18n'

const PAGE = 100
const URL_KEYS = [...FILTER_KEYS, 'scope'] as const

/** Keyset next-page cursor of the forward order: one past the last event's seq. */
function forwardNextFrom(last: { items: AuditEventDTO[]; has_more: boolean }) {
  if (!last.has_more || last.items.length === 0) return undefined
  return last.items[last.items.length - 1].seq + 1
}

/** Sparse filtered pages must continue from the last EXAMINED event supplied by
 * the server. Falling back to the last matching item would skip or rescan data. */
function filteredNextFrom(last: { has_more: boolean; next_from?: number }) {
  return last.has_more ? last.next_from : undefined
}

/** A ledger page; `window_from` marks a newest-first window (the first sequence it read). */
type AuditPage = Awaited<ReturnType<typeof auditApi.list>> & {
  window_from?: number
}

/** Validate URL/saved-view values before they can select an endpoint or enter a
 * request. Unknown keys and non-string values are ignored. A bound is a relative one
 * (`7d`) or the minute of an ISO instant, read by the SHARED bound parser, so an
 * impossible day (2026-02-30) is refused rather than rolled into March. */
function sanitizeParams(
  input: Record<string, unknown>,
): Record<string, string> {
  const clean: Record<string, string> = {}
  for (const key of FILTER_KEYS) {
    const value = input[key]
    if (typeof value !== 'string' || value === '') continue
    const valid = key === 'since' || key === 'until' ? validBound(value) : value
    if (valid !== undefined) clean[key] = valid
  }
  if (input.scope === 'tenant' || input.scope === 'system') {
    clean.scope = input.scope
  }
  return clean
}

function queryOf(params: Record<string, string>): QueryParams {
  const out: QueryParams = {}
  for (const key of FILTER_KEYS) if (params[key]) out[key] = params[key]
  return out
}

export function AuditView() {
  const { t, i18n } = useTranslation(['audit', 'common'])
  const lang = i18n.language
  const { activeTenant, isSuperadmin } = useAuth()
  // acceptance / acceptance: the ledger's bookkeeping (authorization evidence, reads, views) is
  // folded away unless asked for, or unless a filter names the action.
  const [showInternal, setShowInternal] = useState(false)
  const [bySession, setBySession] = useState(true)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [draft, setDraft] = useState<LedgerDraft>(EMPTY_DRAFT)
  // Every pick opens the panel, the same row again included (SidePanelContent `reveal`).
  const [picks, setPicks] = useState(0)

  const [urlState, patchUrlState] = useUrlState(URL_KEYS)
  const validatedParams = useMemo(() => sanitizeParams(urlState), [urlState])
  const scope: LedgerScope =
    isSuperadmin && validatedParams.scope === 'system' ? 'system' : 'tenant'
  const query = useMemo(() => queryOf(validatedParams), [validatedParams])
  // A relative bound is counted back from the moment the query changed, to the minute:
  // the same filters keep the same instant, so loaded pages stay one window.
  const querySig = JSON.stringify(query)
  const [clock, setClock] = useState(() => ({ sig: querySig, now: new Date() }))
  if (clock.sig !== querySig) setClock({ sig: querySig, now: new Date() })
  const now = clock.now
  const filters = useMemo(() => apiFilters(query, now), [query, now])
  const queryInBar = useMinWidth(1100)
  const filtersActive = Object.keys(filters).length > 0
  const isSystem = scope === 'system'
  const listParams = useMemo(() => ({ limit: PAGE, ...filters }), [filters])

  // NEWEST FIRST. The ledger pages forward from ?from and has no sort
  // parameter; its documented way to the tail is head_seq. So the unfiltered view reads
  // the head (GET /v1/audit/recent, which is not itself recorded; the system ledger's own
  // list for the system scope), then the last window of PAGE positions, shown reversed,
  // and "load more" steps back one window. A filtered scan only walks forward: until the
  // engine can scan backwards (ARCH PLAN item 9, N1), a filtered view shows the matches it
  // has loaded newest first, which is a stop-gap.
  const headQuery = useQuery({
    queryKey: ['audit', activeTenant, scope, 'head'],
    queryFn: async () =>
      (isSystem
        ? (await auditApi.systemList({ limit: 1 })).head_seq
        : (await auditApi.recent({ limit: 1 })).head_seq) ?? null,
    enabled: !filtersActive,
  })
  // An engine that answers without head_seq (older than the field) pages forward as before.
  const head = headQuery.data
  const newest = !filtersActive && typeof head === 'number'
  const forward = filtersActive || (headQuery.isSuccess && head === null)
  const listQuery = useInfiniteQuery({
    queryKey: [
      ...(isSystem
        ? queryKeys.audit.systemList(activeTenant, listParams)
        : queryKeys.audit.list(activeTenant, listParams)),
      newest ? 'newest' : 'forward',
      head ?? null,
    ],
    queryFn: async ({ pageParam }): Promise<AuditPage> => {
      const list = isSystem ? auditApi.systemList : auditApi.list
      if (!newest) return list({ from: pageParam, ...listParams })
      const from = Math.max(1, pageParam - PAGE + 1)
      const page = await list({ from, limit: PAGE })
      return { ...page, items: [...page.items].reverse(), window_from: from }
    },
    initialPageParam: newest ? (head as number) : 1,
    getNextPageParam: (last) =>
      !newest
        ? filtersActive
          ? filteredNextFrom(last)
          : forwardNextFrom(last)
        : last.window_from !== undefined && last.window_from > 1
          ? last.window_from - 1
          : undefined,
    enabled: forward || (newest && (head as number) > 0),
  })

  const allRows = useMemo(() => {
    const items = listQuery.data?.pages.flatMap((p) => p.items) ?? []
    return filtersActive ? [...items].sort((a, b) => b.seq - a.seq) : items
  }, [listQuery.data, filtersActive])
  const foldInternal = !showInternal && !filters.action
  const rows = useMemo(
    () =>
      foldInternal ? allRows.filter((e) => !internalRecord(e.action)) : allRows,
    [allRows, foldInternal],
  )
  const internalCount = allRows.length - rows.length
  // C2-DESIGN §3: an entry that just arrived at the head of the feed slides in 6 px
  // (.feed-enter), once, then rests. The view (ledger, tenant, filters, fold) is the
  // feed's scope: an entry a filter or the fold reveals did not arrive.
  const feedIds = useNewRowIds(
    rows.map((r) => r.id),
    JSON.stringify([scope, activeTenant, listParams, foldInternal]),
  )
  const lastPage = listQuery.data?.pages[listQuery.data.pages.length - 1]
  const scannedThrough =
    filtersActive &&
    lastPage?.scan_complete === false &&
    lastPage.next_from !== undefined
      ? lastPage.next_from - 1
      : undefined
  const savedViewParams = useMemo(
    () => ({ ...query, scope: scope === 'system' ? 'system' : undefined }),
    [query, scope],
  )

  // The approvals still waiting: their request entry is the flagged row.
  const pendingQuery = usePendingApprovals().query
  const pending = useMemo(
    () => new Set((pendingQuery.data?.items ?? []).map((a) => a.id)),
    [pendingQuery.data],
  )
  const names = useEntryNames(rows)
  const selected = rows.find((e) => e.id === selectedId) ?? null
  // A pick reveals the panel once. When the entry leaves the list for a moment (a new
  // filter loading), its return must not reveal again a panel the person closed.
  if (!selected && picks > 0) setPicks(0)

  /** Filters change the URL only: the list, the strip and the tokens all read it. */
  function setQuery(next: QueryParams) {
    const patch: Record<string, string | undefined> = {}
    for (const key of FILTER_KEYS) patch[key] = next[key] || undefined
    patchUrlState(patch)
  }

  function applySavedView(params: Record<string, string>) {
    const clean = sanitizeParams(params)
    const patch: Record<string, string | undefined> = {}
    for (const key of FILTER_KEYS) patch[key] = clean[key]
    patch.scope =
      isSuperadmin && clean.scope === 'system' ? 'system' : undefined
    patchUrlState(patch)
  }

  // The strip's window: the query's own bounds; without `since`, the last day before `to`,
  // reaching back to the oldest entry loaded so that every loaded entry is counted.
  // Nothing is recorded after now: an until: ahead ends the strip now.
  const to = filters.until
    ? new Date(Math.min(new Date(filters.until).getTime(), now.getTime()))
    : now
  const from = filters.since
    ? new Date(filters.since)
    : new Date(
        Math.min(
          to.getTime() - DAY,
          ...allRows.map((e) => new Date(e.occurred_at).getTime()),
        ),
      )
  const caption = [
    t('ledger.histogram.loaded', { count: rows.length }),
    t('ledger.histogram.sessions', {
      count: new Set(
        rows
          .filter((e) => e.target_kind === SESSION_KIND)
          .map((e) => e.target_id),
      ).size,
    }),
    t('ledger.histogram.people', {
      count: new Set(
        rows.filter((e) => e.actor_kind === 'user').map((e) => e.actor),
      ).size,
    }),
    ...(listQuery.hasNextPage ? [t('ledger.histogram.more')] : []),
  ].join(' · ')
  const loading = headQuery.isLoading || listQuery.isLoading
  // A filtered list does not read the head: an error the head kept from before says nothing.
  const headError = filtersActive ? null : headQuery.error
  const error = headError ?? listQuery.error

  const queryField = (
    <LedgerQuery
      params={query}
      onChange={setQuery}
      draft={draft}
      onDraft={setDraft}
      resolvePerson={names.resolvePerson}
      personName={names.person}
      className={queryInBar ? undefined : 'w-full max-w-none min-w-0'}
    />
  )

  return (
    <div className="flex h-full min-h-0 flex-col gap-3">
      {/* Where the bar has the room of the shell's search (1100 px and up) the query is
          that search; narrower, the shell's search is an icon and the query heads the page. */}
      {queryInBar ? <TopbarSearch>{queryField}</TopbarSearch> : null}

      <div
        data-slot="work-chrome"
        className="flex min-h-9 min-w-0 items-center"
      >
        <PageHeader
          className="min-w-0 w-full"
          icon={FileCheck2}
          title={t('title')}
          description={t(isSystem ? 'scope.systemHint' : 'subtitle')}
          actions={
            <>
              {isSuperadmin ? (
                <Select
                  value={scope}
                  onValueChange={(value) =>
                    patchUrlState({
                      scope: value === 'system' ? 'system' : undefined,
                    })
                  }
                >
                  <SelectTrigger
                    className="h-7 w-auto min-w-[11rem] text-caption"
                    aria-label={t('scope.label')}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="tenant">{t('scope.tenant')}</SelectItem>
                    <SelectItem value="system">{t('scope.system')}</SelectItem>
                  </SelectContent>
                </Select>
              ) : null}
              <SavedViewsMenu
                featureId="audit"
                params={savedViewParams}
                onApply={applySavedView}
              />
              {isSystem ? null : <EvidenceControls filters={filters} />}
            </>
          }
        />
      </div>

      {scannedThrough !== undefined && (
        <div role="status" className="text-caption text-info">
          {t('scan.incomplete', { seq: formatInt(scannedThrough, lang) })}
        </div>
      )}

      {queryInBar ? null : queryField}

      <LedgerHistogram
        entries={rows}
        from={from}
        to={to}
        now={now}
        caption={caption}
        onPick={(b) =>
          setQuery({
            ...query,
            since: validBound(b.start.toISOString()),
            until: validBound(b.end.toISOString()),
          })
        }
      />

      <div className="flex flex-wrap items-center gap-3">
        <Segmented
          size="sm"
          aria-label={t('ledger.group.label')}
          value={bySession ? 'session' : 'events'}
          onValueChange={(v) => setBySession(v === 'session')}
          options={[
            { value: 'session', label: t('ledger.group.session') },
            { value: 'events', label: t('ledger.group.events') },
          ]}
        />
        {internalCount > 0 || (showInternal && !filters.action) ? (
          <div className="ml-auto flex items-center gap-2 text-caption text-muted-foreground">
            {showInternal ? null : (
              <span role="status">
                {t('internal.hidden', { count: internalCount })}
              </span>
            )}
            <Button
              variant="link"
              size="sm"
              onClick={() => setShowInternal((v) => !v)}
            >
              {t(showInternal ? 'internal.hide' : 'internal.show')}
            </Button>
          </div>
        ) : null}
      </div>

      <div
        className="min-h-0 flex-1 overflow-y-auto"
        aria-busy={loading || undefined}
      >
        {loading ? (
          <div className="flex flex-col gap-2">
            {Array.from({ length: 6 }, (_, i) => (
              <Skeleton key={i} className="h-12 w-full" />
            ))}
          </div>
        ) : error ? (
          <QueryErrorState
            error={error}
            retry={() =>
              void (headError ? headQuery.refetch() : listQuery.refetch())
            }
          />
        ) : rows.length === 0 ? (
          internalCount > 0 ? (
            <FoldedHint />
          ) : (
            <EmptyHint />
          )
        ) : (
          <LedgerEntries
            entries={rows}
            pending={pending}
            fresh={feedIds}
            bySession={bySession}
            names={names}
            selectedId={selectedId}
            onSelect={(e) => {
              setSelectedId(e.id)
              setPicks((n) => n + 1)
            }}
            onAdd={(f) => setQuery({ ...query, ...f })}
          />
        )}
        {listQuery.hasNextPage ? (
          <div className="flex justify-center p-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => void listQuery.fetchNextPage()}
              disabled={listQuery.isFetchingNextPage}
            >
              {listQuery.isFetchingNextPage
                ? t('common:states.loading')
                : t('common:table.loadMore')}
            </Button>
          </div>
        ) : null}
      </div>

      {selected ? (
        <SidePanelContent
          title={t('ledger.entry.title', {
            seq: formatInt(selected.seq, lang),
          })}
          reveal={picks > 0 ? String(picks) : undefined}
        >
          <AuditEntryDetail
            event={selected}
            loaded={allRows}
            scope={scope}
            names={names}
          />
        </SidePanelContent>
      ) : null}
    </div>
  )
}

function FoldedHint() {
  const { t } = useTranslation('audit')
  return (
    <p
      role="status"
      className="px-6 py-12 text-center text-body text-muted-foreground"
    >
      {t('internal.allHidden')}
    </p>
  )
}

function EmptyHint() {
  const { t } = useTranslation('audit')
  return (
    <div className="px-6 py-12 text-center">
      <p className="text-body font-medium text-foreground">
        {t('empty.title')}
      </p>
      <p className="mx-auto mt-1 max-w-sm text-body text-muted-foreground">
        {t('empty.description')}
      </p>
    </div>
  )
}
