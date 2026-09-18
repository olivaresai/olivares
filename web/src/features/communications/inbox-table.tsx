// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useInfiniteQuery, useQueryClient } from '@tanstack/react-query'
import { Inbox, RefreshCcw, ScanEye } from 'lucide-react'
import { useEffect, useId, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { toast } from '@/components/ui/toaster'
import { AuthorityLostError } from '@/features/agentops/auth-boundary'
import { ListTruncationBadge, SectionCard } from '@/features/_intel'
import { formatDateTime } from '@/lib/format'
import {
  communicationsKeys,
  getCursorToken,
  listInbox,
  type CursorAdvanceOutcome,
} from './api'
import type { CommunicationsScope } from './boundary'
import { Mono } from './content-blocks'
import { classifyFailure, type Failure } from './errors'
import {
  InboxCursorDialog,
  type CursorPhase,
  type CursorPreparation,
  type MintedCursor,
} from './inbox-cursor-dialog'
import {
  buildCursorIntent,
  dispatchGuardFor,
  snapshotAuthority,
  useIntentGuard,
  type CursorIntent,
} from './intent'
import type { Me } from './subject-picker'
import { PAGE_LIMIT_DEFAULT, PAGE_LIMITS, type InboxItem } from './types'

/**
 * InboxTable — the exact personal mailbox, one page at a time on the server's
 * continuation. Reading it acknowledges nothing and moves no cursor; it shows no
 * unread count because the engine reports none, and it is not a history of any
 * channel: only what is addressed to this principal and visible now.
 *
 * I2 adds «Mark seen up to here», bound to ONE real, non-empty page the operator
 * loaded: the page's own `cursor_target` and its last Delivery travel together —
 * never a sequence number, never the last row of a re-sorted table, never a
 * continuation of another catalog. The action is offered only while no local
 * filter hides rows, so the scope it names is the scope the engine will see. The
 * first click MINTS (a GET that advances nothing) and opens a confirmation; only
 * confirming sends the PUT. After a committed advance the page chain is dropped
 * and the inbox is re-read from the durable cursor with no old continuation.
 */
export function InboxTable({
  scope,
  canDeliveryRead,
  canDeliveryWrite = false,
  me,
  onOpenDelivery,
}: {
  scope: CommunicationsScope
  canDeliveryRead: boolean
  canDeliveryWrite?: boolean
  me?: Me
  onOpenDelivery: (deliveryId: string) => void
}) {
  const { t, i18n } = useTranslation('communications')
  const idp = useId()
  const queryClient = useQueryClient()
  const workspace = scope.workspace ?? ''
  const tenant = scope.tenant
  const [limit, setLimit] = useState<number>(PAGE_LIMIT_DEFAULT)
  const [filter, setFilter] = useState('')

  const queryKey = communicationsKeys.inbox(tenant, scope.epoch, workspace, {
    limit,
  })
  const query = useInfiniteQuery({
    queryKey,
    queryFn: ({ signal, pageParam }) =>
      listInbox(
        { workspace_id: workspace, limit, continuation: pageParam },
        { tenant },
        signal,
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) =>
      last.has_more && last.continuation ? last.continuation : undefined,
    enabled: canDeliveryRead && workspace !== '',
  })

  const pages = useMemo(() => query.data?.pages ?? [], [query.data])
  const rows = useMemo(() => pages.flatMap((p) => p.items), [pages])
  const lastPage = pages[pages.length - 1]

  // ─── the seen cursor ─────────────────────────────────────────────────────────
  const recipient = me?.userId ?? null
  const [phase, setPhase] = useState<CursorPhase | 'idle'>('idle')
  const [preparation, setPreparation] = useState<CursorPreparation | null>(null)
  const [intent, setIntent] = useState<CursorIntent | null>(null)
  const [outcome, setOutcome] = useState<CursorAdvanceOutcome | null>(null)
  const [failure, setFailure] = useState<Failure | null>(null)
  const [lostCount, setLostCount] = useState(0)
  // BOTH permissions the offered act depends on, re-evaluated live at every
  // dispatch: the preparation is a `delivery:read` GET and the act it offers is a
  // `delivery:write` PUT. `allowed` needs both too — a reader who cannot write must
  // not be offered the preparation, and a writer who lost read cannot mint a target.
  const guard = useIntentGuard({
    allowed: canDeliveryRead && canDeliveryWrite,
    boundary: scope.key,
    permission: 'sessions:delivery:write',
    alsoRequires: 'sessions:delivery:read',
  })
  const resetCursor = () => {
    setPhase('idle')
    setPreparation(null)
    setIntent(null)
    setOutcome(null)
    setFailure(null)
  }
  // A preparation is bound to the PAGE it was made for. When that page is no longer
  // in the loaded chain (a refetch, another page size, another scope), or the
  // filter changed, the preparation is over before the next paint — except while a
  // PUT is in flight, whose intention was frozen and is judged by the transport.
  const preparedTarget = preparation?.target ?? null
  const pageStillLoaded =
    preparedTarget === null ||
    pages.some((p) => p.cursor_target === preparedTarget)
  if (
    phase !== 'idle' &&
    phase !== 'advancing' &&
    phase !== 'applied' &&
    (!pageStillLoaded || filter !== '')
  ) {
    resetCursor()
  }
  if (!canDeliveryWrite && phase !== 'idle' && phase !== 'applied') {
    resetCursor()
    setLostCount((n) => n + 1)
  }
  useEffect(() => {
    if (lostCount > 0) toast.warning(t('authority.confirmationClosed'))
  }, [lostCount, t])

  const canPrepare = (p: (typeof pages)[number]) =>
    canDeliveryWrite &&
    recipient !== null &&
    filter === '' &&
    !!p.cursor_target &&
    p.items.length > 0 &&
    p.items[p.items.length - 1].delivery.recipient.kind === 'user' &&
    p.items[p.items.length - 1].delivery.recipient.ref === recipient
  const prepareReason = (p: (typeof pages)[number]): string | null => {
    if (!canDeliveryWrite) return t('cursor.reasons.noPermission')
    if (recipient === null) return t('cursor.reasons.noRecipient')
    if (filter !== '') return t('cursor.reasons.filtered')
    if (p.items.length === 0 || !p.cursor_target)
      return t('cursor.reasons.emptyPage')
    const last = p.items[p.items.length - 1].delivery.recipient
    if (last.kind !== 'user' || last.ref !== recipient)
      return t('cursor.reasons.otherRecipient')
    return null
  }

  const prepare = async (index: number) => {
    const p = pages[index]
    if (!p || !canPrepare(p) || !recipient || !workspace) return
    const last = p.items[p.items.length - 1]
    const target = p.cursor_target as string
    resetCursor()
    setPreparation({
      pageNumber: index + 1,
      target,
      deliveries: p.items.length,
      last,
    })
    setPhase('preparing')
    // The GET is part of the act and runs under the act's guard: a scope that moves
    // between the click and the answer ends it, and a late answer is not painted.
    const signal = guard.begin()
    if (!signal) {
      resetCursor()
      setLostCount((n) => n + 1)
      return
    }
    // The authority frozen at the OPERATOR'S ACT, exactly as a confirmed intention
    // freezes it. The preparation has no intent yet — the GET is what mints the
    // token an intent will carry — so this snapshot IS that intent's
    // authority-to-be, and the transport compares it with the live stores
    // immediately before each fetch (IR-I2-1).
    const authority = snapshotAuthority()
    try {
      const got = await getCursorToken(
        recipient,
        { workspace_id: workspace, target },
        { tenant, guard: dispatchGuardFor({ authority }, guard.check) },
        signal,
      )
      if (signal.aborted || !guard.alive()) return
      const m: MintedCursor = {
        cursor: got.result.cursor,
        cursorId: got.result.cursor_id,
        version: got.result.version,
        etag: got.etag,
      }
      // Frozen together NOW: body, cursor ETag, recipient, scope and key.
      setIntent(
        buildCursorIntent(
          { tenant, workspace, boundary: scope.key },
          recipient,
          m,
          last.delivery.id,
        ),
      )
      setPhase('confirm')
    } catch (err) {
      if (signal.aborted) return
      if (err instanceof AuthorityLostError) {
        resetCursor()
        setLostCount((n) => n + 1)
        return
      }
      const f = classifyFailure(err)
      if (f.kind === 'aborted') {
        resetCursor()
        return
      }
      setFailure(f)
      setPhase('prepareFailed')
    }
  }
  const onAdvanced = (o: CursorAdvanceOutcome) => {
    setOutcome(o)
    setPhase('applied')
    // The chain of pages read before the advance is over: the inbox is re-read
    // from the durable cursor, first page, NO old continuation.
    void queryClient.resetQueries({ queryKey, exact: true })
  }
  const onFailed = (err: unknown) => {
    if (err instanceof AuthorityLostError) {
      resetCursor()
      setLostCount((n) => n + 1)
      return
    }
    const f = classifyFailure(err)
    if (f.kind === 'aborted') {
      setPhase('confirm')
      return
    }
    setFailure(f)
    if (
      f.kind === 'version_mismatch' ||
      f.kind === 'version_required' ||
      f.kind === 'conflict' ||
      f.kind === 'invalid'
    ) {
      // The intention dies with the cursor version it was minted on.
      setIntent(null)
      setPhase('conflict')
    } else if (f.kind === 'ambiguous') {
      setPhase('ambiguous')
    } else {
      setIntent(null)
      setPhase('refused')
    }
  }
  const dismiss = () => {
    guard.end()
    resetCursor()
  }
  const refreshInbox = () => {
    dismiss()
    void queryClient.resetQueries({ queryKey, exact: true })
  }

  const columns = useMemo<TableColumn<InboxItem>[]>(
    () => [
      {
        id: 'subject',
        accessorFn: (r) => r.message.content.subject,
        header: t('inbox.columns.subject'),
        cell: ({ row }) => (
          <span className="break-words font-medium">
            {row.original.message.content.subject || t('content.empty')}
          </span>
        ),
      },
      {
        id: 'sender',
        accessorFn: (r) => `${r.message.sender.kind}:${r.message.sender.ref}`,
        header: t('inbox.columns.sender'),
        cell: ({ row }) => (
          <Mono>
            {row.original.message.sender.kind}:{row.original.message.sender.ref}
          </Mono>
        ),
      },
      {
        id: 'urgency',
        accessorFn: (r) => r.message.urgency,
        header: t('inbox.columns.urgency'),
        cell: ({ row }) => (
          <Badge
            variant={
              row.original.message.urgency === 'critical'
                ? 'danger'
                : row.original.message.urgency === 'high'
                  ? 'warning'
                  : 'outline'
            }
          >
            {t(`urgency.${row.original.message.urgency}`, {
              defaultValue: row.original.message.urgency,
            })}
          </Badge>
        ),
      },
      {
        id: 'state',
        accessorFn: (r) => r.delivery.state,
        header: t('inbox.columns.state'),
        cell: ({ row }) => (
          <Badge variant="neutral">{row.original.delivery.state}</Badge>
        ),
      },
      {
        id: 'available',
        accessorFn: (r) => r.delivery.available_at,
        header: t('inbox.columns.available'),
        cell: ({ row }) =>
          formatDateTime(row.original.delivery.available_at, i18n.language),
      },
      {
        id: 'ackDue',
        accessorFn: (r) => r.delivery.ack_due_at ?? '',
        header: t('inbox.columns.ackDue'),
        cell: ({ row }) =>
          formatDateTime(row.original.delivery.ack_due_at, i18n.language),
      },
      {
        id: 'acknowledged',
        accessorFn: (r) => r.delivery.acknowledged_at ?? '',
        header: t('inbox.columns.acknowledged'),
        cell: ({ row }) =>
          formatDateTime(row.original.delivery.acknowledged_at, i18n.language),
      },
      {
        id: 'delivery',
        accessorFn: (r) => r.delivery.id,
        header: t('inbox.columns.delivery'),
        cell: ({ row }) => <Mono>{row.original.delivery.id}</Mono>,
      },
    ],
    [t, i18n.language],
  )

  return (
    <SectionCard
      title={t('inbox.title')}
      description={t('inbox.description')}
      noPadding
      actions={
        <div className="flex flex-wrap items-center gap-2">
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
            disabled={!canDeliveryRead}
          >
            <RefreshCcw className="size-4" aria-hidden="true" />
            {t('actions.refresh')}
          </Button>
        </div>
      }
    >
      <ListTruncationBadge
        query={{ data: lastPage, error: query.error }}
        label={t('inbox.truncated')}
        hint={t('inbox.truncatedHint')}
        filas={rows.length}
      />
      <DataTable
        columns={columns}
        data={rows}
        isLoading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        getRowId={(r) => r.delivery.id}
        onRowClick={(r) => onOpenDelivery(r.delivery.id)}
        searchable
        onSearchChange={setFilter}
        label={t('inbox.title')}
        hasMore={query.hasNextPage}
        onLoadMore={() => void query.fetchNextPage()}
        isFetchingMore={query.isFetchingNextPage}
        empty={
          <EmptyState
            icon={<Inbox />}
            title={t('inbox.emptyTitle')}
            description={t('inbox.emptyBody')}
          />
        }
      />
      {canDeliveryWrite && pages.length > 0 && rows.length > 0 ? (
        <section
          aria-label={t('cursor.section.title')}
          className="flex flex-col gap-2 border-t border-border px-3 py-3"
          data-slot="cursor-section"
        >
          <p className="text-body font-medium">{t('cursor.section.title')}</p>
          <p className="text-caption text-muted-foreground">
            {t('cursor.section.hint')}
          </p>
          {filter !== '' ? (
            <p className="text-caption text-warning" role="status">
              {t('cursor.reasons.filtered')}
            </p>
          ) : null}
          <ul className="flex flex-col gap-1">
            {pages.map((p, i) => {
              const last = p.items[p.items.length - 1]
              const reason = prepareReason(p)
              return (
                <li
                  key={p.cursor_target ?? `page-${i}`}
                  className="flex flex-wrap items-center gap-2 text-caption"
                  data-slot="cursor-page"
                >
                  <span>
                    {t('cursor.section.page', {
                      page: i + 1,
                      count: p.items.length,
                    })}
                  </span>
                  {last ? (
                    <span className="text-muted-foreground">
                      {t('cursor.section.upTo')}{' '}
                      <span className="font-medium text-foreground">
                        {last.message.content.subject || t('content.empty')}
                      </span>{' '}
                      <span data-slot="cursor-page-last-delivery">
                        <Mono>{last.delivery.id}</Mono>
                      </span>
                    </span>
                  ) : null}
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    disabled={reason !== null || phase !== 'idle'}
                    title={reason ?? undefined}
                    onClick={() => void prepare(i)}
                  >
                    <ScanEye className="size-4" aria-hidden="true" />
                    {t('actions.markSeen')}
                  </Button>
                  {reason ? (
                    <span className="text-muted-foreground">{reason}</span>
                  ) : null}
                </li>
              )
            })}
          </ul>
        </section>
      ) : null}
      {recipient ? (
        <InboxCursorDialog
          open={phase !== 'idle'}
          phase={phase === 'idle' ? 'preparing' : phase}
          preparation={preparation}
          intent={intent}
          recipient={recipient}
          workspaceName={scope.workspaceName || workspace}
          outcome={outcome}
          failure={failure}
          canDeliveryWrite={canDeliveryWrite}
          guard={guard}
          onConfirm={() => setPhase('advancing')}
          onRetrySame={() => setPhase('advancing')}
          onDismiss={dismiss}
          onOpenDelivery={(id) => {
            dismiss()
            onOpenDelivery(id)
          }}
          onRefreshInbox={refreshInbox}
          onAdvanced={onAdvanced}
          onFailed={onFailed}
        />
      ) : null}
    </SectionCard>
  )
}
