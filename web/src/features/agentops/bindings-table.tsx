// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useInfiniteQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, Link2, RefreshCw } from 'lucide-react'
import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
  type MouseEvent,
} from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { Field } from '@/components/ui/field'
import { KvList, KvRow } from '@/components/ui/kv'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { toast } from '@/components/ui/toaster'
import { consoleApi, type SourceRosterEntry } from '@/features/console/api'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { formatDateTime } from '@/lib/format'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { agentOpsApi, agentOpsKeys, PROFILE_PAGE } from './api'
import { AuthorityLostError, useAuthBoundary } from './auth-boundary'
import type { ProviderBindingDTO, ProviderProfileDTO } from './types'
import { useFreshRead, type FreshRead } from './use-fresh-read'
import './i18n'

const NONE = '__none__'
/** One page of the profile picker. The picker PAGES: every active profile is
 * reachable through "Load more", never only the first page. */
const PROFILE_PICK = 200

/** A profile that can take a binding: active, and of THIS node's environment. The
 * same two checks the engine makes first (provider_source_binding.go CreateBinding),
 * said before the 409 rather than after it. */
function bindable(p: ProviderProfileDTO): boolean {
  return p.state === 'active' && p.local_environment
}

/** A roster row this node can bind: it carries a persistent id AND a revision this
 * node's reconciler applied. Without either, nothing here can name a binding the
 * engine would accept — and nothing here guesses one from the stored definition. */
function appliedHere(s: SourceRosterEntry): boolean {
  return !!s.id && (s.applied_revision ?? 0) > 0
}

/**
 * BindingsTable — the source→profile bindings of the tenant (or of ONE profile when
 * `profile` is given): list, bind, revoke. A binding dedicates one configured source,
 * at the exact revision this node applied, to one profile; its identity is the
 * source's persistent id + revision, never its name, and rows are immutable except
 * for revocation. The three tiers are independent of the profile tiers:
 * `sessions:profile-binding:read` lists, `:write` creates, `:admin` revokes — and
 * creating ALSO needs deployment-wide source administration, which the engine
 * decides on the protected roster read this dialog makes when it opens.
 *
 * Authority is checked when it is USED, not only when a control was painted: the
 * revoke confirmation closes when the admin tier leaves, when the row it names is
 * no longer an active binding, or when the boundary (principal, tenant, credential)
 * moves; and the confirm itself, plus the mutation function, re-check the current
 * permission before anything is sent.
 *
 * DETAILS IS A POINT READ, NOT A ROW. Every row offers Details, and pressing it asks
 * the engine for THAT binding now (`GET /provider-source-bindings/{ref}`), under the
 * read tier alone: the dialog paints only what that answer says, never the list row
 * it was opened from, so a 403, a 404 or an error shows as itself rather than as a
 * stale success. The cycle ends with the dialog and with any change of permission,
 * principal, tenant or credential (BindingDetailDialog below).
 */
export function BindingsTable({
  profile,
  describe = true,
}: {
  profile?: ProviderProfileDTO
  /** Render the one-line subtitle (off when the page header already says it). */
  describe?: boolean
}) {
  const { t, i18n } = useTranslation('agentops')
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const canRead = can('sessions:profile-binding:read')
  const canWrite = can('sessions:profile-binding:write')
  const canAdmin = can('sessions:profile-binding:admin')

  const [createOpen, setCreateOpen] = useState(false)
  const [confirmRevoke, setConfirmRevoke] = useState<ProviderBindingDTO | null>(
    null,
  )
  // The binding whose details are open — its REFERENCE only. Nothing else of the
  // list row is handed to the dialog, so nothing of it can be painted there: the
  // dialog shows the engine's current answer for this reference or the reason it
  // has none. The opener is remembered for the return of focus.
  const [detailRef, setDetailRef] = useState<string | null>(null)
  const detailOpener = useRef<HTMLElement | null>(null)
  // The reference the point read is for, written by the Details gesture right
  // before the cycle starts (a ref, read only inside the request: useFreshRead
  // calls the latest `read` from a handler, never during render).
  const detailTarget = useRef<string | null>(null)

  const params = useMemo(
    () =>
      profile
        ? { profile_ref: profile.profile_ref, limit: PROFILE_PAGE }
        : { limit: PROFILE_PAGE },
    [profile],
  )
  const query = useInfiniteQuery({
    queryKey: agentOpsKeys.bindings(activeTenant, boundary.epoch, params),
    queryFn: ({ pageParam, signal }) =>
      agentOpsApi.listBindings({ ...params, cursor: pageParam }, { signal }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.cursor : undefined),
    enabled: canRead,
  })
  const rows = useMemo(
    () => query.data?.pages.flatMap((p) => p.items) ?? [],
    [query.data],
  )
  /** The binding named by a confirmation, as the CURRENT list knows it. Closures
   * here are rebuilt every render and react-query reads the mutation function of
   * the latest render at dispatch, so "current" is the last committed render. */
  const stillActive = (b: ProviderBindingDTO) =>
    rows.some((r) => r.binding_ref === b.binding_ref && r.state === 'active')

  // Revocation of a tier, or of the row, closes what it may have outlived —
  // adjusted DURING render (the sanctioned pattern), so a confirmation never
  // paints once more without its tier. A change of boundary remounts the table
  // from its parent and keys the dialog below.
  const [seenTier, setSeenTier] = useState({ canAdmin, canWrite, canRead })
  if (
    seenTier.canAdmin !== canAdmin ||
    seenTier.canWrite !== canWrite ||
    seenTier.canRead !== canRead
  ) {
    setSeenTier({ canAdmin, canWrite, canRead })
    if (!canAdmin) setConfirmRevoke(null)
    if (!canWrite) setCreateOpen(false)
    // Losing the read tier unmounts the table body below; the OPEN state must not
    // survive it either, or getting the tier back would reopen a dialog nobody asked
    // for. Restoring a permission is not a gesture.
    if (!canRead) setDetailRef(null)
  }
  if (confirmRevoke && !stillActive(confirmRevoke)) setConfirmRevoke(null)
  // A boundary move (principal, tenant, credential) closes the details: what was
  // being read was authorized for the previous boundary. The read cycle itself is
  // ended by the same move (useFreshRead aborts what is in flight and forgets what
  // arrived); nothing is re-read on its own — the next Details press reads under
  // the new authority.
  const [seenBoundary, setSeenBoundary] = useState(boundary.key)
  if (seenBoundary !== boundary.key) {
    setSeenBoundary(boundary.key)
    setDetailRef(null)
  }

  // WHO MAY BIND IS THE ENGINE'S ANSWER, NOW. The roster behind the bind picker is
  // the protected `GET /v1/console/sources`, authorized by the same deployment-wide
  // `system:admin` decision the binding port makes — including authored, scoped
  // decisions the console's RBAC set cannot see. So pressing Bind with the CURRENT
  // write tier issues that GET in an explicit cycle; closing ends it; a change of
  // permission or boundary ends it too. Reopening reads again — never a memory.
  const roster = useFreshRead<SourceRosterEntry[]>({
    read: (signal) =>
      consoleApi.listSources({ signal }).then((r) => r.sources ?? []),
    allowed: canWrite,
    boundary: boundary.key,
  })
  // DETAILS IS THE ENGINE'S ANSWER, NOW. Pressing Details on a row issues
  // `GET /provider-source-bindings/{ref}` for that row's reference in an explicit
  // cycle under the CURRENT read tier — the same tier that lists, independent of
  // every profile tier. Closing ends it; Refresh reads again; a change of
  // permission or boundary ends it, and the dialog with it (above). The list row
  // is never the answer: a 404 is kept as the engine's own "no such binding now".
  const detail = useFreshRead<BindingAnswer>({
    read: (signal) => {
      const target = detailTarget.current
      if (target === null) {
        return Promise.reject(new Error('no binding reference to read'))
      }
      return agentOpsApi.getBinding(target, { signal }).then(
        (binding) => ({
          kind: 'found',
          binding,
          readAt: new Date().toISOString(),
        }),
        (err: unknown) => {
          if (err instanceof ApiError && err.isNotFound) {
            return { kind: 'missing', readAt: new Date().toISOString() }
          }
          throw err
        },
      )
    },
    allowed: canRead,
    boundary: boundary.key,
  })
  // The control that opened the dialog gets focus back when it closes (Escape, the
  // close button, a completed bind): the dialog is controlled and opened from an
  // ordinary button, so the return of focus is stated here rather than assumed.
  const bindButton = useRef<HTMLButtonElement>(null)
  const openCreate = () => {
    setCreateOpen(true)
    roster.start()
  }
  const closeCreate = () => {
    setCreateOpen(false)
    roster.stop()
  }
  const returnFocus = () => bindButton.current?.focus()
  const openDetail = (
    e: MouseEvent<HTMLButtonElement>,
    b: ProviderBindingDTO,
  ) => {
    detailOpener.current = e.currentTarget
    detailTarget.current = b.binding_ref
    setDetailRef(b.binding_ref)
    detail.start()
  }
  const closeDetail = () => {
    setDetailRef(null)
    detail.stop()
  }
  /** Back to the Details control that opened the dialog while it is still in the
   * document (rows are keyed by reference, so it survives a list refetch). A row
   * that left the list leaves the primitive's default return of focus. */
  const returnFocusToDetailOpener = () => {
    const opener = detailOpener.current
    if (opener?.isConnected) opener.focus()
  }

  const revoke = usePrivilegedMutation<ProviderBindingDTO, ProviderBindingDTO>({
    mutationFn: (b) => {
      // Judged at DISPATCH: react-query takes the mutation function of the latest
      // render, so a click queued across a permission change meets the new answer.
      if (!canAdmin || !stillActive(b)) throw new AuthorityLostError()
      return agentOpsApi.revokeBinding(b.binding_ref)
    },
    invalidateKeys: () => [agentOpsKeys.bindings(activeTenant, boundary.epoch)],
    successMessage: t('profiles.bindings.revoke.success'),
    onDone: () => setConfirmRevoke(null),
    onError: (err) => {
      if (!(err instanceof AuthorityLostError)) return false
      setConfirmRevoke(null)
      toast.warning(t('profiles.authority.lost'))
      return true
    },
  })

  const onConfirmRevoke = () => {
    const target = confirmRevoke
    if (!target) return
    if (!canAdmin || !stillActive(target)) {
      setConfirmRevoke(null)
      toast.warning(t('profiles.authority.lost'))
      return
    }
    revoke.mutate(target)
  }

  const columns = useMemo<TableColumn<ProviderBindingDTO>[]>(() => {
    const cols: TableColumn<ProviderBindingDTO>[] = [
      {
        id: 'source',
        header: t('profiles.bindings.cols.source'),
        accessorFn: (b) => `${b.source_name ?? ''} ${b.source_id}`,
        cell: ({ row }) => (
          <span className="flex flex-col">
            <span className="font-medium text-foreground">
              {row.original.source_name || row.original.source_id}
            </span>
            <span className="font-mono text-[11px] text-muted-foreground">
              {row.original.source_id}
            </span>
          </span>
        ),
      },
      {
        accessorKey: 'source_revision',
        header: t('profiles.bindings.cols.revision'),
        cell: ({ getValue }) => (
          <span className="font-mono text-caption tabular-nums text-foreground">
            {getValue<number>()}
          </span>
        ),
      },
    ]
    if (!profile) {
      cols.push({
        accessorKey: 'profile_ref',
        header: t('profiles.bindings.cols.profile'),
        cell: ({ getValue }) => <Mono value={getValue<string>()} />,
      })
    }
    cols.push(
      {
        accessorKey: 'environment_ref',
        header: t('profiles.bindings.cols.environment'),
        cell: ({ getValue }) => <Mono value={getValue<string>()} />,
      },
      {
        accessorKey: 'state',
        header: t('profiles.bindings.cols.state'),
        cell: ({ getValue }) => {
          const s = getValue<string>()
          return (
            <Badge variant={s === 'active' ? 'success' : 'neutral'}>
              {t(`profiles.bindings.state.${s}`, { defaultValue: s })}
            </Badge>
          )
        },
      },
      {
        accessorKey: 'bound_at',
        header: t('profiles.bindings.cols.bound'),
        cell: ({ getValue }) => (
          <span className="text-caption text-muted-foreground">
            {formatDateTime(getValue<string>(), i18n.language)}
          </span>
        ),
      },
      {
        id: 'actions',
        header: '',
        enableSorting: false,
        cell: ({ row }) => (
          <div className="flex justify-end gap-1">
            <Button
              variant="ghost"
              size="sm"
              aria-label={t('profiles.bindings.details.open', {
                ref: row.original.binding_ref,
              })}
              onClick={(e) => openDetail(e, row.original)}
            >
              {t('profiles.actions.details')}
            </Button>
            {canAdmin && row.original.state === 'active' && (
              <Button
                variant="destructive"
                size="sm"
                onClick={() => setConfirmRevoke(row.original)}
              >
                {t('profiles.actions.revoke')}
              </Button>
            )}
          </div>
        ),
      },
    )
    return cols
  }, [t, i18n.language, canAdmin, profile])

  if (!canRead) {
    return (
      <p className="rounded-md border border-border bg-muted px-2.5 py-2 text-caption text-muted-foreground">
        {t('profiles.bindings.noRead')}
      </p>
    )
  }

  const canBindHere = !profile || bindable(profile)

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-start justify-between gap-3">
        {describe ? (
          <p className="text-body text-muted-foreground">
            {t('profiles.bindings.subtitle')}
          </p>
        ) : (
          <span />
        )}
        {canWrite && (
          <Button asChild variant="primary" size="sm">
            <button
              ref={bindButton}
              type="button"
              onClick={openCreate}
              disabled={!canBindHere}
              title={
                canBindHere ? undefined : t('profiles.bindings.notBindable')
              }
            >
              <Link2 className="size-3.5" />
              {t('profiles.actions.bind')}
            </button>
          </Button>
        )}
      </div>

      <DataTable
        columns={columns}
        data={rows}
        isLoading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        getRowId={(b) => b.binding_ref}
        stickyHeader
        hasMore={!!query.hasNextPage}
        onLoadMore={() => void query.fetchNextPage()}
        isFetchingMore={query.isFetchingNextPage}
        label={t('profiles.bindings.title')}
        empty={
          <EmptyState
            icon={<Link2 />}
            title={t('profiles.bindings.empty.title')}
            description={t('profiles.bindings.empty.description')}
          />
        }
      />

      {canWrite && (
        <BindingCreateDialog
          key={boundary.key}
          open={createOpen}
          onOpenChange={(o) => (o ? openCreate() : closeCreate())}
          onClosed={returnFocus}
          profile={profile}
          roster={roster}
          epoch={boundary.epoch}
        />
      )}

      <ConfirmDialog
        open={confirmRevoke !== null && canAdmin}
        onOpenChange={(o) => !o && setConfirmRevoke(null)}
        tone="danger"
        title={t('profiles.bindings.revoke.title')}
        description={t('profiles.bindings.revoke.description')}
        confirmLabel={t('profiles.bindings.revoke.confirm')}
        pending={revoke.isPending}
        onConfirm={onConfirmRevoke}
      />

      <BindingDetailDialog
        open={detailRef !== null}
        bindingRef={detailRef}
        read={detail}
        onOpenChange={(o) => !o && closeDetail()}
        onClosed={returnFocusToDetailOpener}
      />
    </div>
  )
}

/** What the engine answered for ONE reference, at the moment it answered. A 404 is
 * an answer too — "no such binding for this tenant, now" — and is kept apart from
 * a transport or server error, which is not an answer about the binding. */
type BindingAnswer =
  | { kind: 'found'; binding: ProviderBindingDTO; readAt: string }
  | { kind: 'missing'; readAt: string }

/**
 * BindingDetailDialog — the current, authorized point read of one binding.
 *
 * THE ANSWER IS THE ENGINE'S, NOW. The table starts the cycle when Details is
 * pressed (useFreshRead; `GET /provider-source-bindings/{ref}`) under the CURRENT
 * `sessions:profile-binding:read` — a tier independent of every profile tier, so
 * this dialog needs nothing else and asks for nothing else: no profile read, no
 * roster, no configuration. While the read is in flight there is nothing to show
 * but the reference asked about; a 403 is the engine refusing this principal now;
 * a 404 is the engine holding no such binding for this tenant now; an error is an
 * error; and only a fresh, current success paints fields. The list row the dialog
 * opened from is never painted here and never stands in for an answer the engine
 * did not give. Refresh and Retry read again, explicitly, and drop what was shown
 * until the new answer lands — an older answer is never left on screen as if it
 * were current. Closing, or any change of permission, principal, tenant or
 * credential, aborts the request in flight and discards whatever lands afterwards.
 * Nothing here writes: no revoke, no POST.
 */
function BindingDetailDialog({
  open,
  bindingRef,
  read,
  onOpenChange,
  onClosed,
}: {
  open: boolean
  /** The reference asked about — the dialog's subject, not an answer. */
  bindingRef: string | null
  /** The point-read cycle the table started when Details was pressed. */
  read: FreshRead<BindingAnswer>
  onOpenChange: (o: boolean) => void
  /** Where focus goes when the dialog has closed (the control that opened it). */
  onClosed?: () => void
}) {
  const { t, i18n } = useTranslation('agentops')
  const { can } = useAuth()
  const canRead = can('sessions:profile-binding:read')

  const status = read.state.status
  const answer =
    read.current && read.state.status === 'ready' ? read.state.data : null
  const found = answer?.kind === 'found' ? answer.binding : null
  const readAgainLabel =
    status === 'error' || status === 'forbidden' || answer?.kind === 'missing'
      ? t('profiles.bindings.details.retry')
      : t('profiles.bindings.details.refresh')

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="max-h-[calc(100dvh-2rem)] max-w-lg overflow-y-auto"
        onCloseAutoFocus={(e) => {
          if (!onClosed) return
          e.preventDefault()
          onClosed()
        }}
      >
        <DialogHeader>
          <DialogTitle>{t('profiles.bindings.details.title')}</DialogTitle>
          <DialogDescription className="flex flex-col gap-1">
            <span
              className="font-mono text-caption text-foreground"
              title={bindingRef ?? undefined}
            >
              {bindingRef}
            </span>
            <span>{t('profiles.bindings.details.description')}</span>
          </DialogDescription>
        </DialogHeader>

        {status === 'loading' && (
          <p
            className="flex items-center gap-2 text-caption text-muted-foreground"
            role="status"
          >
            <Spinner className="size-3.5" />
            {t('profiles.bindings.details.loading')}
          </p>
        )}
        {status === 'forbidden' && (
          <div
            className="flex items-start gap-2 rounded-md border border-warning-line bg-warning-soft px-2.5 py-2 text-caption text-warning"
            role="status"
          >
            <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
            <span>{t('profiles.bindings.details.forbidden')}</span>
          </div>
        )}
        {status === 'error' && (
          <div
            className="flex items-start gap-2 rounded-md border border-danger-line bg-danger-soft px-2.5 py-2 text-caption text-danger"
            role="alert"
          >
            <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
            <span>
              {t('profiles.bindings.details.error', {
                message: read.state.message,
              })}
            </span>
          </div>
        )}
        {answer?.kind === 'missing' && (
          <div
            className="flex items-start gap-2 rounded-md border border-border bg-muted px-2.5 py-2 text-caption text-muted-foreground"
            role="status"
          >
            <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
            <span>{t('profiles.bindings.details.missing')}</span>
          </div>
        )}
        {open &&
          ((status === 'ready' && !read.current) || status === 'idle') && (
            <p className="text-caption text-warning" role="status">
              {t('profiles.bindings.details.ended')}
            </p>
          )}

        {found && (
          <KvList>
            <KvRow label={t('profiles.bindings.details.ref')} mono>
              {found.binding_ref}
            </KvRow>
            <KvRow label={t('profiles.bindings.details.source')}>
              {found.source_name || found.source_id}
            </KvRow>
            <KvRow label={t('profiles.bindings.details.sourceId')} mono>
              {found.source_id}
            </KvRow>
            <KvRow label={t('profiles.bindings.details.revision')} mono>
              {found.source_revision}
            </KvRow>
            <KvRow label={t('profiles.bindings.details.profile')} mono>
              {found.profile_ref}
            </KvRow>
            <KvRow label={t('profiles.bindings.details.driver')} mono>
              {found.driver}
            </KvRow>
            <KvRow label={t('profiles.bindings.details.environment')} mono>
              {found.environment_ref}
            </KvRow>
            <KvRow label={t('profiles.bindings.details.selector')} mono>
              {found.selector_key}
            </KvRow>
            <KvRow label={t('profiles.bindings.details.state')}>
              <Badge variant={found.state === 'active' ? 'success' : 'neutral'}>
                {t(`profiles.bindings.state.${found.state}`, {
                  defaultValue: found.state,
                })}
              </Badge>
            </KvRow>
            <KvRow label={t('profiles.bindings.details.bound')}>
              {formatDateTime(found.bound_at, i18n.language)}
            </KvRow>
            {found.revoked_at && (
              <KvRow label={t('profiles.bindings.details.revoked')}>
                {formatDateTime(found.revoked_at, i18n.language)}
              </KvRow>
            )}
          </KvList>
        )}
        {answer && (
          <p className="text-caption text-muted-foreground">
            {t('profiles.bindings.details.readAt', {
              at: formatDateTime(answer.readAt, i18n.language),
            })}
          </p>
        )}

        <DialogFooter>
          <Button
            type="button"
            variant="secondary"
            onClick={() => onOpenChange(false)}
          >
            {t('profiles.bindings.details.close')}
          </Button>
          <Button
            type="button"
            variant="secondary"
            onClick={() => read.start()}
            disabled={status === 'loading' || !canRead}
          >
            <RefreshCw className="size-3.5" />
            {readAgainLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function Mono({ value }: { value: string }) {
  return (
    <span
      className="font-mono text-caption text-muted-foreground"
      title={value}
    >
      {value}
    </span>
  )
}

/**
 * BindingCreateDialog — dedicates a source to a profile.
 *
 * WHO MAY BIND IS THE ENGINE'S ANSWER, NOW. The roster behind the picker is the
 * protected `GET /v1/console/sources`, authorized by the same deployment-wide
 * `system:admin` decision the binding port makes — including authored, scoped
 * decisions the console's RBAC set cannot see. So this dialog does not guess from a
 * flag: opening Bind with the CURRENT profile-binding write permission issues that
 * GET in an explicit cycle (useFreshRead). While it loads there is nothing to pick;
 * a 403 is the engine saying this principal may not bind from here; an error is an
 * error; and only a fresh, current success offers rows. The cycle ends — and the
 * selection with it — when the permission, the principal, the tenant or the
 * credential changes, or when the dialog closes: reopening reads again, never a
 * remembered answer. The body posted is the roster row's persistent `id` and
 * `applied_revision` plus the profile reference; the engine re-authorizes the write.
 */
function BindingCreateDialog({
  open,
  onOpenChange,
  onClosed,
  profile,
  roster,
  epoch,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  /** Where focus goes when the dialog has closed (the control that opened it). */
  onClosed?: () => void
  /** The profile to bind to, when the dialog opened from a profile's own sheet. */
  profile?: ProviderProfileDTO
  /** The roster cycle the table started when Bind was pressed. */
  roster: FreshRead<SourceRosterEntry[]>
  /** The authority boundary the table read under (partitions the picker's cache). */
  epoch: number
}) {
  const { t } = useTranslation('agentops')
  const { activeTenant, can } = useAuth()
  const queryClient = useQueryClient()
  const canReadProfiles = can('sessions:profile:read')
  const canWrite = can('sessions:profile-binding:write')

  const [profileRef, setProfileRef] = useState<string>(NONE)
  const [sourceId, setSourceId] = useState<string>(NONE)

  // THE TARGET IS ONLY AS CURRENT AS THE READ THAT OFFERED IT. In the tenant-wide
  // picker a profile was chosen from a list read under `sessions:profile:read`; when
  // that tier leaves, the choice, the options derived from the earlier answer and
  // any request still in flight are over — adjusted during render so nothing of it
  // paints once more, cancelled in the effect below. A profile fixed by a sheet that
  // is itself mounted under its own read tier is not affected.
  const pickerKey = agentOpsKeys.profiles(activeTenant, epoch, {
    state: 'active',
    limit: PROFILE_PICK,
  })
  const [seenRead, setSeenRead] = useState(canReadProfiles)
  if (seenRead !== canReadProfiles) {
    setSeenRead(canReadProfiles)
    if (!canReadProfiles) setProfileRef(NONE)
  }
  useEffect(() => {
    if (!profile && !canReadProfiles) {
      void queryClient.cancelQueries({ queryKey: pickerKey })
      queryClient.removeQueries({ queryKey: pickerKey })
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- the key is rebuilt per render; its identity is (tenant, epoch)
  }, [profile, canReadProfiles, activeTenant, epoch, queryClient])
  // A selection is only meaningful against a current answer: when the cycle leaves
  // `ready` the selection goes with it, before this render paints.
  const rosterStatus = roster.state.status
  const [seenStatus, setSeenStatus] = useState(rosterStatus)
  if (seenStatus !== rosterStatus) {
    setSeenStatus(rosterStatus)
    if (rosterStatus !== 'ready') setSourceId(NONE)
  }

  // The profile picker PAGES through every active profile of the tenant: the first
  // page is a page, and "Load more" walks the keyset cursor until has_more is false.
  const profilesQuery = useInfiniteQuery({
    queryKey: pickerKey,
    queryFn: ({ pageParam, signal }) =>
      agentOpsApi.listProfiles(
        { state: 'active', limit: PROFILE_PICK, cursor: pageParam },
        { signal },
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.cursor : undefined),
    enabled: open && !profile && canReadProfiles,
  })
  // Options exist only under the read tier that produced them.
  const profiles = useMemo(
    () =>
      canReadProfiles
        ? (profilesQuery.data?.pages.flatMap((p) => p.items) ?? [])
        : [],
    [profilesQuery.data, canReadProfiles],
  )
  /** The target named by the picker, as the CURRENT authorized list knows it. */
  const targetIsCurrent = profile
    ? true
    : canReadProfiles && profiles.some((p) => p.profile_ref === profileRef)

  const sources: SourceRosterEntry[] =
    roster.current && roster.state.status === 'ready' ? roster.state.data : []
  const selectedSource = sources.find((s) => s.id === sourceId)
  const targetRef = profile ? profile.profile_ref : profileRef

  const reset = () => {
    setProfileRef(NONE)
    setSourceId(NONE)
  }

  const create = usePrivilegedMutation<void, ProviderBindingDTO>({
    mutationFn: () => {
      // Current permission, current roster answer, and a row inside it — at
      // dispatch (react-query reads the mutation function of the latest render).
      const src =
        roster.current && roster.state.status === 'ready'
          ? roster.state.data.find((s) => s.id === sourceId)
          : undefined
      // …and a target the CURRENT profile-read tier still vouches for, when it came
      // from the tenant-wide picker rather than from an authorized sheet.
      const targetCurrent = profile
        ? true
        : canReadProfiles && profiles.some((p) => p.profile_ref === targetRef)
      if (!canWrite || !src || !targetCurrent) throw new AuthorityLostError()
      if (!src.id || !src.applied_revision || targetRef === NONE) {
        throw new Error(t('profiles.bindings.create.sourceNotApplied'))
      }
      return agentOpsApi.createBinding({
        source_id: src.id,
        source_revision: src.applied_revision,
        profile_ref: targetRef,
      })
    },
    invalidateKeys: () => [agentOpsKeys.bindings(activeTenant, epoch)],
    successMessage: t('profiles.bindings.create.success'),
    onDone: () => {
      reset()
      onOpenChange(false)
    },
    onError: (err) => {
      if (!(err instanceof AuthorityLostError)) return false
      setSourceId(NONE)
      toast.warning(t('profiles.authority.lost'))
      return true
    },
  })

  const ready =
    canWrite &&
    roster.current &&
    targetRef !== NONE &&
    targetIsCurrent &&
    !!selectedSource &&
    appliedHere(selectedSource)

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (ready && !create.isPending) create.mutate()
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => (create.isPending ? undefined : onOpenChange(o))}
    >
      <DialogContent
        className="max-w-lg"
        onCloseAutoFocus={(e) => {
          if (!onClosed) return
          e.preventDefault()
          onClosed()
        }}
      >
        <DialogHeader>
          <DialogTitle>{t('profiles.bindings.create.title')}</DialogTitle>
          <DialogDescription>
            {t('profiles.bindings.create.description')}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="flex flex-col gap-3">
          {profile ? (
            <p className="font-mono text-caption text-muted-foreground">
              {profile.display_name || profile.profile_ref} ·{' '}
              {profile.profile_ref} · {profile.driver}
            </p>
          ) : (
            <div className="flex flex-col gap-2">
              <Field
                label={t('profiles.bindings.create.profile')}
                description={t('profiles.bindings.create.profileHint')}
              >
                <Select
                  value={profileRef}
                  onValueChange={setProfileRef}
                  disabled={!canReadProfiles}
                >
                  <SelectTrigger
                    aria-label={t('profiles.bindings.create.profile')}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={NONE} disabled>
                      {t('profiles.bindings.create.profileNone')}
                    </SelectItem>
                    {profiles.map((p) => (
                      <SelectItem
                        key={p.profile_ref}
                        value={p.profile_ref}
                        disabled={!bindable(p)}
                      >
                        {p.display_name || p.profile_ref} · {p.driver}
                        {!bindable(p) &&
                          ` — ${t('profiles.bindings.create.profileNotBindable')}`}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              {profilesQuery.isLoading && (
                <p className="text-caption text-muted-foreground">
                  {t('profiles.roster.loadingProfiles')}
                </p>
              )}
              {profilesQuery.hasNextPage && (
                <div className="flex items-center gap-2">
                  <p className="text-caption text-warning">
                    {t('profiles.roster.loadedProfiles', {
                      n: profiles.length,
                    })}
                  </p>
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={() => void profilesQuery.fetchNextPage()}
                    disabled={profilesQuery.isFetchingNextPage}
                  >
                    {profilesQuery.isFetchingNextPage && (
                      <Spinner className="size-3.5" />
                    )}
                    {t('profiles.roster.loadMoreProfiles')}
                  </Button>
                </div>
              )}
              {(!canReadProfiles || profilesQuery.isError) && (
                <p className="text-caption text-warning">
                  {t('profiles.bindings.create.profilesNotRead')}
                </p>
              )}
            </div>
          )}

          {rosterStatus === 'loading' && (
            <p className="text-caption text-muted-foreground" role="status">
              {t('profiles.roster.loading')}
            </p>
          )}
          {rosterStatus === 'forbidden' && (
            <div className="flex items-start gap-2 rounded-md border border-warning-line bg-warning-soft px-2.5 py-2 text-caption text-warning">
              <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
              <span>{t('profiles.roster.forbidden')}</span>
            </div>
          )}
          {rosterStatus === 'error' && (
            <div className="flex items-start gap-2 rounded-md border border-danger-line bg-danger-soft px-2.5 py-2 text-caption text-danger">
              <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
              <span>
                {t('profiles.roster.error', { message: roster.state.message })}
              </span>
            </div>
          )}
          {((rosterStatus === 'ready' && !roster.current) ||
            (rosterStatus === 'idle' && open)) && (
            <p className="text-caption text-warning">
              {t('profiles.roster.ended')}
            </p>
          )}
          {roster.current && (
            <Field
              label={t('profiles.bindings.create.source')}
              description={t('profiles.bindings.create.sourceHint')}
            >
              <Select value={sourceId} onValueChange={setSourceId}>
                <SelectTrigger
                  aria-label={t('profiles.bindings.create.source')}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NONE} disabled>
                    {t('profiles.bindings.create.sourceNone')}
                  </SelectItem>
                  {sources.map((s) => (
                    <SelectItem
                      key={s.id ?? `name:${s.name}`}
                      value={s.id ?? `${NONE}:${s.name}`}
                      disabled={!appliedHere(s)}
                    >
                      {s.name}
                      {s.kind ? ` · ${s.kind}` : ''}
                      {appliedHere(s)
                        ? ` · ${t('profiles.bindings.create.revision', { n: s.applied_revision })}`
                        : ` — ${t('profiles.bindings.create.sourceNotApplied')}`}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          )}
          {roster.current && sources.length === 0 && (
            <p className="text-caption text-muted-foreground">
              {t('profiles.bindings.create.rosterEmpty')}
            </p>
          )}
          {selectedSource && appliedHere(selectedSource) && (
            <p className="font-mono text-caption text-muted-foreground">
              {selectedSource.id} ·{' '}
              {t('profiles.bindings.create.revision', {
                n: selectedSource.applied_revision,
              })}{' '}
              · {selectedSource.tenant}
            </p>
          )}

          <DialogFooter>
            <Button
              type="button"
              variant="secondary"
              onClick={() => onOpenChange(false)}
              disabled={create.isPending}
            >
              {t('browser.cancel')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={!ready || create.isPending}
            >
              {create.isPending && <Spinner className="size-3.5" />}
              {create.isPending
                ? t('profiles.bindings.create.submitting')
                : t('profiles.bindings.create.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
