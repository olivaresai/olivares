// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// RecordingsView — the privileged-session-recording console. It lists the
// tenant's recording sessions (keyset-paginated), faceted by status server-side,
// and navigates each row to the unified session recording viewer.
// A recording session is operator activity, so the view itself is a recorded
// privileged surface — the RecordingNotice strip is mounted unconditionally.
import { useInfiniteQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { Disc3 } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { PageHeader } from '@/components/ui/page-header'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { SavedViewsMenu } from '@/features/saved-views'
import { isoDayBound, RelTimeLabel, UrlStateNotice } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { formatInt } from '@/lib/format'
import {
  useValidatedUrlState,
  type UrlState,
  type UrlStateDecoded,
} from '@/lib/hooks/use-url-state'
import { recordingApi, recordingKeys } from './api'
import { RecordingPolicyPanel } from './recording-config-panel'
import { RecordingNotice } from './recording-notice'
import type { RecordingStatus, SessionDTO } from './types'
import './i18n'

const PAGE = 50
const ALL = '__all__'
const STATUSES: RecordingStatus[] = ['active', 'sealed']
const SEAL_REASONS = [
  'idle',
  'closed',
  'breakglass_review',
  'sweep',
  'consent_change',
] as const

// The whole filter set is deep-linkable: "the unsealed recordings from
// the incident window" is a standard compliance ask, and it must survive being
// pasted into a ticket instead of being retyped from a screenshot. The keys are
// the SERVER's parameter names so a link, a saved view and the request all read
// the same.
const URL_KEYS = [
  'status',
  'seal_reason',
  'opened_after',
  'opened_before',
  'subject_contains',
  'grant',
] as const
type FilterKey = (typeof URL_KEYS)[number]
type RecordingsFilters = Partial<Record<FilterKey, string>>
const DATE_KEYS = ['opened_after', 'opened_before'] as const
const SEARCH_FIELDS = ['subject_contains', 'grant'] as const
type SearchField = (typeof SEARCH_FIELDS)[number]
/** Stable empty array: setState bails out on an identical reference. */
const NO_ISSUES: string[] = []

/**
 * Decode URL / saved-view values into the view's filters. Pure and total: a
 * malformed link is ordinary input, so every refusal falls back to the in-code
 * default and is NAMED in `issues` for the view to disclose. Silence here would
 * show the recipient a different slice of the recordings than the sender saw.
 */
function sanitizeParams(
  input: Record<string, unknown>,
): UrlStateDecoded<RecordingsFilters> {
  const value: RecordingsFilters = {}
  const issues: string[] = []

  // status and seal_reason are enumerations: only what this view itself offers
  // may reach the request, never a verbatim URL value.
  const enums: Array<[FilterKey, readonly string[]]> = [
    ['status', STATUSES as readonly string[]],
    ['seal_reason', SEAL_REASONS],
  ]
  for (const [key, allowed] of enums) {
    const raw = input[key]
    if (typeof raw !== 'string' || raw === '') continue
    if (allowed.includes(raw)) value[key] = raw
    else issues.push(key)
  }

  for (const key of DATE_KEYS) {
    const raw = input[key]
    if (typeof raw !== 'string' || raw === '') continue
    const day = isoDayBound(raw)
    if (day === undefined) {
      issues.push(key)
      continue
    }
    value[key] = day
  }

  for (const key of SEARCH_FIELDS) {
    const raw = input[key]
    if (raw === undefined || raw === '') continue
    if (typeof raw !== 'string' || raw.trim() === '') {
      issues.push(key)
      continue
    }
    value[key] = raw.trim()
  }
  // The UI exposes one deliberate predicate at a time. Keeping both from a
  // pasted link would silently apply the handler's AND semantics while the
  // single search control could display only one of them. Subject wins and the
  // rejected grant is named, then removed from the canonical URL.
  if (value.subject_contains && value.grant) {
    delete value.grant
    issues.push('grant')
  }

  return { value, issues }
}

/**
 * A bound, canonicalised to the UTC midnight of its day.
 *
 * The control is a DAY picker, so a bound it cannot express must not survive
 * into the request. Keeping the full instant meant a link reading 2026-06-01
 * could query from 14:30 that day: two links look identical on screen and
 * return different windows, which is precisely the way an evidence link lies.
 *
 * It also refuses a rolled calendar date. `new Date('2026-02-30')` is March 2
 * in JavaScript, so without this an impossible day would be silently answered
 * with a different, possible one.
 */
/** The UTC day of a bound, for `<input type="date">`. */
function dayInputValue(value?: string): string {
  if (!value) return ''
  const at = new Date(value)
  if (Number.isNaN(at.getTime())) return ''
  // Read the day back in UTC because that is how it was written: a bare day is
  // parsed as UTC midnight, so local getters would shift it west of Greenwich.
  return at.toISOString().slice(0, 10)
}

/** The RFC3339 instant for a day picked in `<input type="date">`. */
function rfc3339FromDay(value: string): string | undefined {
  if (!value) return undefined
  return isoDayBound(value)
}

export function RecordingsView() {
  const { t, i18n } = useTranslation('recordings')
  const lang = i18n.language
  const { activeTenant } = useAuth()
  const navigate = useNavigate()

  const [filters, patchUrlState, urlIssues] = useValidatedUrlState(
    URL_KEYS,
    sanitizeParams,
  )
  // Values restored from a saved view are server data, so they get the same
  // treatment as a pasted link — including being told what was dropped, which
  // the URL alone cannot report once applySavedView has cleaned it out.
  const [savedViewIssues, setSavedViewIssues] = useState<string[]>(NO_ISSUES)
  const initialSearchField: SearchField = filters.grant
    ? 'grant'
    : 'subject_contains'
  const [searchField, setSearchField] =
    useState<SearchField>(initialSearchField)
  const [searchDraft, setSearchDraft] = useState(
    filters[initialSearchField] ?? '',
  )

  useEffect(() => {
    if (filters.grant) {
      setSearchField('grant')
      setSearchDraft(filters.grant)
    } else if (filters.subject_contains) {
      setSearchField('subject_contains')
      setSearchDraft(filters.subject_contains)
    } else {
      setSearchDraft('')
    }
  }, [filters.grant, filters.subject_contains])

  const patchFilters = useCallback(
    (patch: UrlState) => {
      // Any hand edit supersedes the saved view the notice was about.
      setSavedViewIssues(NO_ISSUES)
      patchUrlState(patch)
    },
    [patchUrlState],
  )

  const applySavedView = useCallback(
    (params: Record<string, string>) => {
      const { value, issues } = sanitizeParams(params)
      const patch: UrlState = {}
      for (const key of URL_KEYS) patch[key] = value[key]
      setSavedViewIssues(issues.length > 0 ? issues : NO_ISSUES)
      patchUrlState(patch)
    },
    [patchUrlState],
  )

  const status = filters.status ?? ALL
  const sealReason = filters.seal_reason ?? ALL
  const openedAfter = dayInputValue(filters.opened_after)
  const openedBefore = dayInputValue(filters.opened_before)
  const submitSearch = useCallback(() => {
    const value = searchDraft.trim()
    setSearchDraft(value)
    patchFilters({
      subject_contains:
        searchField === 'subject_contains' && value ? value : undefined,
      grant: searchField === 'grant' && value ? value : undefined,
    })
  }, [patchFilters, searchDraft, searchField])
  const clearSearch = useCallback(() => {
    setSearchDraft('')
    patchFilters({ subject_contains: undefined, grant: undefined })
  }, [patchFilters])

  const query = useInfiniteQuery({
    queryKey: recordingKeys.sessions(activeTenant, {
      status: filters.status,
      seal_reason: filters.seal_reason,
      opened_after: filters.opened_after,
      opened_before: filters.opened_before,
      subject_contains: filters.subject_contains,
      grant: filters.grant,
      limit: PAGE,
    }),
    queryFn: ({ pageParam }) =>
      recordingApi.listSessions({
        status: filters.status,
        seal_reason: filters.seal_reason,
        opened_after: filters.opened_after,
        opened_before: filters.opened_before,
        subject_contains: filters.subject_contains,
        grant: filters.grant,
        limit: PAGE,
        cursor: pageParam,
      }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.cursor : undefined),
  })

  const rows = useMemo(
    () => query.data?.pages.flatMap((p) => p.items) ?? [],
    [query.data],
  )

  const columns = useMemo<TableColumn<SessionDTO>[]>(
    () => [
      {
        id: 'subject',
        accessorKey: 'subject',
        header: t('cols.subject'),
        cell: ({ row }) => {
          const s = row.original
          return (
            <div className="min-w-0">
              <div className="truncate font-mono text-xs font-medium text-foreground">
                {s.subject}
              </div>
              {s.subject_user && (
                <div className="truncate font-mono text-xs text-muted-foreground">
                  {s.subject_user}
                </div>
              )}
            </div>
          )
        },
      },
      {
        accessorKey: 'subject_kind',
        header: t('cols.kind'),
        cell: ({ getValue }) => (
          <Badge variant="outline">
            {t(`kind.${getValue<string>()}`, {
              defaultValue: getValue<string>(),
            })}
          </Badge>
        ),
      },
      {
        accessorKey: 'status',
        header: t('cols.status'),
        cell: ({ getValue }) => {
          const v = getValue<string>()
          return (
            <Badge variant={v === 'active' ? 'success' : 'neutral'}>
              {t(`status.${v}`, { defaultValue: v })}
            </Badge>
          )
        },
      },
      {
        accessorKey: 'opened_at',
        header: t('cols.opened'),
        cell: ({ getValue }) => <RelTimeLabel ts={getValue<string>()} />,
      },
      {
        accessorKey: 'frames_written',
        header: t('cols.frames'),
        cell: ({ getValue }) => (
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            {formatInt(getValue<number>(), lang)}
          </span>
        ),
      },
      {
        id: 'flags',
        accessorFn: (s) =>
          `${s.gap ? 'gap' : ''} ${
            s.breakglass_grant ? `breakglass grant:${s.breakglass_grant}` : ''
          }`,
        header: t('cols.flags'),
        enableSorting: false,
        cell: ({ row }) => {
          const s = row.original
          if (!s.gap && !s.breakglass_grant)
            return <span className="text-muted-foreground">—</span>
          return (
            <div className="flex flex-wrap gap-1">
              {s.gap && (
                <Badge variant="danger" title={t('badge.gapHint')}>
                  {t('badge.gap')}
                </Badge>
              )}
              {s.breakglass_grant && (
                <Badge variant="warning" title={t('badge.breakglassHint')}>
                  {t('badge.breakglass')}
                </Badge>
              )}
            </div>
          )
        },
      },
      {
        accessorKey: 'seal_reason',
        header: t('cols.sealReason'),
        cell: ({ getValue }) => {
          const v = getValue<string | undefined>()
          return v ? (
            <span className="text-xs text-muted-foreground">{v}</span>
          ) : (
            <span className="text-muted-foreground">—</span>
          )
        },
      },
    ],
    [t, lang],
  )

  return (
    <div className="flex h-full flex-col gap-4">
      <PageHeader icon={Disc3} title={t('title')} description={t('subtitle')} />

      {/* The recordings console is itself a recorded privileged surface. */}
      <RecordingNotice namespace="recording" always />

      {/* Recording-policy authoring (config:admin only; self-gated + read-only
          otherwise). Editing this policy is itself a recorded privileged action. */}
      <RecordingPolicyPanel />

      {/* What the link (or the saved view) asked for and did not get. */}
      <UrlStateNotice
        issues={savedViewIssues.length > 0 ? savedViewIssues : urlIssues}
        origin={savedViewIssues.length > 0 ? 'saved-view' : 'url'}
      />

      {/* Every control below is server-side and URL-backed. Search submits
          explicitly because each list read writes its own audit event: typing
          must never turn one operator intention into one read per keystroke. */}
      <div className="flex flex-wrap items-center justify-end gap-2">
        <form
          className="flex items-center gap-1"
          onSubmit={(event) => {
            event.preventDefault()
            submitSearch()
          }}
        >
          <Select
            value={searchField}
            onValueChange={(value) => setSearchField(value as SearchField)}
          >
            <SelectTrigger
              className="h-7 w-auto min-w-[9rem] text-xs"
              aria-label={t('search.field')}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="subject_contains">
                {t('search.subject')}
              </SelectItem>
              <SelectItem value="grant">{t('search.grant')}</SelectItem>
            </SelectContent>
          </Select>
          <Input
            className="h-7 w-52 text-xs"
            value={searchDraft}
            onChange={(event) => setSearchDraft(event.currentTarget.value)}
            aria-label={t('search.input')}
            placeholder={
              searchField === 'grant'
                ? t('search.grantPlaceholder')
                : t('search.subjectPlaceholder')
            }
          />
          <Button type="submit" size="sm" variant="secondary">
            {t('search.submit')}
          </Button>
          {(searchDraft || filters.subject_contains || filters.grant) && (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              onClick={clearSearch}
            >
              {t('search.clear')}
            </Button>
          )}
        </form>

        <Select
          value={status}
          onValueChange={(v) =>
            // The "all" sentinel is a DEFAULT: it lives in code and never in
            // the URL, so a pristine view stays clean and shareable.
            patchFilters({ status: v === ALL ? undefined : v })
          }
        >
          <SelectTrigger
            className="h-7 w-auto min-w-[9rem] text-xs"
            aria-label={t('filterByStatus')}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>{t('allStatuses')}</SelectItem>
            {STATUSES.map((s) => (
              <SelectItem key={s} value={s}>
                {t(`status.${s}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Select
          value={sealReason}
          onValueChange={(v) =>
            patchFilters({ seal_reason: v === ALL ? undefined : v })
          }
        >
          <SelectTrigger
            className="h-7 w-auto min-w-[10rem] text-xs"
            aria-label={t('filterBySealReason')}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>{t('allSealReasons')}</SelectItem>
            {SEAL_REASONS.map((r) => (
              <SelectItem key={r} value={r}>
                {t(`sealReasonOption.${r}`, { defaultValue: r })}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <div className="flex items-center gap-1">
          <span className="text-xs text-muted-foreground">
            {t('filterOpenedAfter')}
          </span>
          <input
            type="date"
            value={openedAfter}
            onChange={(e) =>
              patchFilters({ opened_after: rfc3339FromDay(e.target.value) })
            }
            aria-label={t('filterOpenedAfter')}
            className="h-7 rounded-md border border-input bg-background px-2 text-xs text-foreground focus:outline-none focus:ring-1 focus:ring-ring"
          />
        </div>

        <div className="flex items-center gap-1">
          <span className="text-xs text-muted-foreground">
            {t('filterOpenedBefore')}
          </span>
          <input
            type="date"
            value={openedBefore}
            onChange={(e) =>
              patchFilters({ opened_before: rfc3339FromDay(e.target.value) })
            }
            aria-label={t('filterOpenedBefore')}
            className="h-7 rounded-md border border-input bg-background px-2 text-xs text-foreground focus:outline-none focus:ring-1 focus:ring-ring"
          />
        </div>
      </div>

      <DataTable
        columns={columns}
        data={rows}
        isLoading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        getRowId={(r) => r.id}
        onRowClick={(r) =>
          void navigate({ to: `/session-viewer/${r.id}` as never })
        }
        toolbar={
          <SavedViewsMenu
            featureId="recordings"
            params={filters}
            onApply={applySavedView}
          />
        }
        stickyHeader
        hasMore={query.hasNextPage}
        onLoadMore={() => void query.fetchNextPage()}
        isFetchingMore={query.isFetchingNextPage}
        label={t('title')}
        empty={<EmptyHint />}
      />
    </div>
  )
}

function EmptyHint() {
  const { t } = useTranslation('recordings')
  return (
    <div className="px-6 py-12 text-center">
      <p className="text-sm font-medium text-foreground">{t('empty.title')}</p>
      <p className="mx-auto mt-1 max-w-sm text-sm text-muted-foreground">
        {t('empty.description')}
      </p>
    </div>
  )
}
