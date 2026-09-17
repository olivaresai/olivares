// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useInfiniteQuery } from '@tanstack/react-query'
import { KeyRound, RefreshCcw } from 'lucide-react'
import { useId, useMemo, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { ListTruncationBadge, SectionCard } from '@/features/_intel'
import { useCapability } from '@/lib/auth/capabilities'
import { formatDateTime } from '@/lib/format'
import {
  communicationsKeys,
  listAdministrableChannels,
  UnadmittedReadError,
} from './api'
import type { CommunicationsScope } from './boundary'
import { administrationSurfaceQuestion } from './capabilities'
import { CapabilityNotice } from './capability-notice'
import { Mono } from './content-blocks'
import { classifyFailure } from './errors'
import { FailureNotice } from './failure-notice'
import {
  ADMINISTRATION_STATES,
  PAGE_LIMIT_DEFAULT,
  PAGE_LIMITS,
  type AdministrationItem,
  type AdministrationState,
} from './types'

const ID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

/**
 * ChannelAdministration — the page of `GET /channels/administration`: the Channels
 * this principal may ADMINISTER in the explicit workspace (core `channel:admin`
 * and a current local admin bit), archived ones included when the persisted-state
 * filter says so. It is a different collection from the read catalog: a Channel
 * the principal cannot read can still be listed and administered here, and a
 * Channel it can read is absent without the local admin bit. Every page names its
 * size and follows the server's own continuation; changing the filter starts a
 * new query with no continuation. The list opens the administrative sheet, which
 * re-reads through the grant history read, never through the read-tier Channel
 * read. Nothing here sends `If-None-Match` or treats the body's `etag` as a page
 * validator.
 */
export function ChannelAdministration({
  scope,
  onOpenChannel,
}: {
  scope: CommunicationsScope
  onOpenChannel: (channelId: string) => void
}) {
  const { t, i18n } = useTranslation('communications')
  const idp = useId()
  const workspace = scope.workspace ?? ''
  const tenant = scope.tenant
  const [limit, setLimit] = useState<number>(PAGE_LIMIT_DEFAULT)
  const [state, setState] = useState<AdministrationState>('all')
  const [byId, setById] = useState('')
  // ⛔ THE COLLECTION LOADS ON ITS OWN CURRENT ADMISSION AND ON NOTHING ELSE. The same
  //    question the tab and the route asked, so the three share ONE observation and one
  //    request — and when it expires, all three stop together instead of this list
  //    fetching under an answer the screen around it has already retired.
  const admission = useCapability(
    administrationSurfaceQuestion(scope.workspace),
  )
  const reachable = admission.access === 'reachable'

  const query = useInfiniteQuery({
    queryKey: communicationsKeys.administration(
      tenant,
      scope.epoch,
      workspace,
      { state, limit },
    ),
    // ⛔ AND THE ADMISSION TRAVELS WITH THE REQUEST, not only with the render that
    //    installed it. `enabled` decided ONCE, at a render; between that decision and
    //    the bytes there is the library's own scheduling, an explicit Refresh, a Retry,
    //    a next page, a focus refetch — and, inside the shared client, an AWAITED
    //    credential refresh and the single 401 replay. The permit goes down to the
    //    transport so it is re-read in every one of those gaps: expiry, an owner that
    //    moved, or a permit answering another workspace's question refuses there, with
    //    zero bytes on the wire. Measured before this line existed (independent review
    //    of c04cb75de1): a Refresh clicked with the retained admission ALREADY expired
    //    put a second collection request out — two fetches where one was admitted.
    queryFn: ({ signal, pageParam }) =>
      listAdministrableChannels(
        { workspace_id: workspace, state, limit, continuation: pageParam },
        { tenant, admission: admission.permit },
        signal,
      ),
    initialPageParam: undefined as string | undefined,
    // `last?`: a page that is not there is not a page with a successor. See the
    // same guard in channel-admin-sheet.tsx for the render that reaches it.
    getNextPageParam: (last) =>
      last?.has_more && last.continuation ? last.continuation : undefined,
    enabled: reachable && workspace !== '',
    // An authorization-dependent page: never served from memory after its scope
    // or its moment. The query is re-asked on focus and on every explicit refresh.
    staleTime: 0,
    refetchOnWindowFocus: true,
    // ⛔ AND IT IS NEVER RE-ASKED ON ITS OWN — the same `retry: false` its sibling grant
    //    read already carries, for the same two reasons. A LOCAL REFUSAL is not a
    //    transient failure: nothing was sent, a spent permit is a one-way door
    //    (`createCapabilityPermit`), and re-running the callback could only refuse again;
    //    what resumes this read is a NEW exact admission, which arrives as a render. And
    //    an ANSWER from the engine about who may administer here is exactly what the
    //    operator has to see, not something to ask three times first — the notice below
    //    names it and the table offers Retry, which is the operator's decision to make.
    retry: false,
  })

  // ⛔ AND IT IS NOT REPORTED AS AN ANSWER EITHER. `classifyFailure` describes what the
  //    ENGINE said; a local refusal means the engine was never asked, so putting it
  //    through that classifier would print "the request failed" for a request that does
  //    not exist. The surface's own admission is what the screen reacts to — this
  //    component's `enabled`, and the tab around it (communications-view.tsx) — so the
  //    refusal empties the rows like any other error and says nothing on the engine's
  //    behalf. Nor is it an EMPTY ANSWER: the engine returned no collection at all, so
  //    the successful-empty copy and any continuation retained from an earlier page must
  //    stay off screen. The existing neutral capability notice says exactly that the
  //    client could not establish access, without inventing a server verdict.
  const refusedLocally = query.error instanceof UnadmittedReadError
  const answered = refusedLocally ? null : query.error

  const rows = useMemo(
    () => query.data?.pages.flatMap((p) => p.items) ?? [],
    [query.data],
  )
  const lastPage = query.data?.pages[query.data.pages.length - 1]

  const columns = useMemo<TableColumn<AdministrationItem>[]>(
    () => [
      {
        id: 'name',
        accessorFn: (r) => r.channel.name,
        header: t('administration.columns.name'),
        cell: ({ row }) => (
          <span className="font-medium">{row.original.channel.name}</span>
        ),
      },
      {
        id: 'slug',
        accessorFn: (r) => r.channel.slug,
        header: t('administration.columns.slug'),
        cell: ({ row }) => <Mono>{row.original.channel.slug}</Mono>,
      },
      {
        id: 'kind',
        accessorFn: (r) => r.channel.kind,
        header: t('administration.columns.kind'),
        cell: ({ row }) => (
          <Badge variant="outline">
            {t(`kind.${row.original.channel.kind}`, {
              defaultValue: row.original.channel.kind,
            })}
          </Badge>
        ),
      },
      {
        id: 'state',
        accessorFn: (r) => r.channel.state,
        header: t('administration.columns.state'),
        cell: ({ row }) => (
          <Badge
            variant={
              row.original.channel.state === 'archived' ? 'warning' : 'neutral'
            }
          >
            {t(`channelState.${row.original.channel.state}`, {
              defaultValue: row.original.channel.state,
            })}
          </Badge>
        ),
      },
      {
        id: 'sensitivity',
        accessorFn: (r) => r.channel.sensitivity,
        header: t('administration.columns.sensitivity'),
        cell: ({ row }) =>
          t(`sensitivity.${row.original.channel.sensitivity}`, {
            defaultValue: row.original.channel.sensitivity,
          }),
      },
      {
        id: 'protection',
        accessorFn: (r) => r.channel.content_protection,
        header: t('administration.columns.protection'),
        cell: ({ row }) =>
          t(`protection.${row.original.channel.content_protection}`, {
            defaultValue: row.original.channel.content_protection,
          }),
      },
      {
        id: 'etag',
        accessorFn: (r) => r.etag,
        header: t('administration.columns.etag'),
        cell: ({ row }) => <Mono>{row.original.etag}</Mono>,
      },
      {
        id: 'updated',
        accessorFn: (r) => r.channel.updated_at,
        header: t('administration.columns.updated'),
        cell: ({ row }) =>
          formatDateTime(row.original.channel.updated_at, i18n.language),
      },
    ],
    [t, i18n.language],
  )

  const openById = (e: FormEvent) => {
    e.preventDefault()
    const id = byId.trim()
    if (ID.test(id)) onOpenChannel(id)
  }

  return (
    <SectionCard
      title={t('administration.title')}
      description={t('administration.description')}
      noPadding
      actions={
        <div className="flex flex-wrap items-center gap-2">
          <Select
            value={state}
            onValueChange={(v) => setState(v as AdministrationState)}
          >
            <SelectTrigger
              className="w-36"
              aria-label={t('administration.stateFilter')}
              id={`${idp}-state`}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {ADMINISTRATION_STATES.map((s) => (
                <SelectItem key={s} value={s}>
                  {t(`administration.states.${s}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select
            value={String(limit)}
            onValueChange={(v) => setLimit(Number(v))}
          >
            <SelectTrigger
              className="w-28"
              aria-label={t('actions.pageSize')}
              id={`${idp}-limit`}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {PAGE_LIMITS.map((n) => (
                <SelectItem key={n} value={String(n)}>
                  {n}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button
            variant="outline"
            size="sm"
            onClick={() => void query.refetch()}
            disabled={!reachable}
          >
            <RefreshCcw className="size-4" aria-hidden="true" />
            {t('actions.refresh')}
          </Button>
        </div>
      }
    >
      <form
        onSubmit={openById}
        className="flex flex-wrap items-end gap-2 px-3 pt-3"
      >
        <label
          htmlFor={`${idp}-by-id`}
          className="text-xs text-muted-foreground"
        >
          {t('administration.openById')}
        </label>
        <Input
          id={`${idp}-by-id`}
          value={byId}
          onChange={(e) => setById(e.target.value)}
          className="w-full sm:w-80"
          autoComplete="off"
          mono
        />
        <Button
          type="submit"
          variant="outline"
          size="sm"
          disabled={!ID.test(byId.trim()) || !reachable}
        >
          {t('actions.administer')}
        </Button>
        <span className="basis-full text-xs text-muted-foreground">
          {t('administration.openByIdHint')}
        </span>
      </form>
      {answered && !query.isFetching ? (
        <div className="px-3 pt-3">
          <FailureNotice failure={classifyFailure(answered)} />
        </div>
      ) : null}
      {refusedLocally ? (
        <div className="px-3 py-3">
          <CapabilityNotice access="unknown" />
        </div>
      ) : (
        <>
          <ListTruncationBadge
            query={{ data: lastPage, error: query.error }}
            label={t('administration.truncated')}
            hint={t('administration.truncatedHint')}
            filas={rows.length}
          />
          <DataTable
            columns={columns}
            data={query.error ? [] : rows}
            isLoading={query.isLoading}
            error={answered}
            onRetry={() => void query.refetch()}
            getRowId={(r) => r.channel.id}
            onRowClick={(r) => onOpenChannel(r.channel.id)}
            searchable
            label={t('administration.title')}
            hasMore={query.hasNextPage}
            onLoadMore={() => void query.fetchNextPage()}
            isFetchingMore={query.isFetchingNextPage}
            empty={
              <EmptyState
                icon={<KeyRound />}
                title={t('administration.emptyTitle')}
                description={t('administration.emptyBody')}
              />
            }
          />
        </>
      )}
    </SectionCard>
  )
}
