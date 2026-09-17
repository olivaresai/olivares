// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { Eye, EyeOff, Fingerprint, Plus } from 'lucide-react'
import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
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
import { Input } from '@/components/ui/input'
import { KvList, KvRow } from '@/components/ui/kv'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Spinner } from '@/components/ui/spinner'
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import { formatDateTime } from '@/lib/format'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { agentOpsApi, agentOpsKeys, PROFILE_PAGE } from './api'
import { AuthorityLostError, useAuthBoundary } from './auth-boundary'
import { BindingsTable } from './bindings-table'
import {
  LaunchReadinessPanel,
  useProfileLaunchReadiness,
} from './launch-readiness-panel'
import {
  DEFAULT_READINESS_ISOLATION,
  DEFAULT_READINESS_TRANSPORT,
  type LaunchReadinessTransport,
} from './launch-readiness'
import { ProfileCreateDialog } from './profile-create-dialog'
import type { PatchProfileRequest, ProviderProfileDTO } from './types'
import { useFreshRead } from './use-fresh-read'
import './i18n'

const ALL = '__all__'
const STATES = ['active', 'disabled', 'retired'] as const

/**
 * ProfilesPanel — the administration surface of the provider-profile plane (B1):
 * list, register, rename, enable/disable, retire, and the admin-only configuration
 * read, with the source bindings of each profile beside it.
 *
 * Three tiers, checked at the sites that act: `sessions:profile:read` lists and
 * opens the sheet; `:write` registers, renames and moves active↔disabled; `:admin`
 * retires (irreversible, confirmed by typing) and reveals the stored homes. The
 * ordinary list and detail carry references and labels only — the homes travel on
 * ONE authorized read, fetched when an admin asks and dropped when they stop asking.
 *
 * Everything shown is what the SERVER said: `local_environment`, `operable` and
 * `state` are rendered as reported, never derived from the driver name.
 * `operable` is the legacy enablement flag, not a launch guarantee; the sheet
 * loads the selected profile's launch-readiness point read.
 */
export function ProfilesPanel({ describe = true }: { describe?: boolean }) {
  const boundary = useAuthBoundary()
  // Remount on the AUTHORITY BOUNDARY — principal, tenant, credential. The query keys
  // are tenant-scoped, which isolates the CACHE; the remount is what clears what is
  // on SCREEN — an open sheet, a revealed configuration, a pending confirmation, a
  // selection, a filter — so nothing read for one operator is painted, or acted on,
  // under the next.
  return <ProfilesPanelInner key={boundary.key} describe={describe} />
}

function ProfilesPanelInner({ describe }: { describe: boolean }) {
  const { t, i18n } = useTranslation('agentops')
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const canRead = can('sessions:profile:read')
  const canWrite = can('sessions:profile:write')

  const [state, setState] = useState<string>(ALL)
  const [createOpen, setCreateOpen] = useState(false)
  const [selected, setSelected] = useState<ProviderProfileDTO | null>(null)
  // The state facet's trigger stays mounted whatever the table shows; the empty
  // state's "Show all states" button does not — it leaves with the empty state it
  // lived in. So the clear action moves focus HERE, synchronously, before the
  // re-render takes its own button away (WCAG 2.4.3: focus never falls to body).
  const stateTriggerRef = useRef<HTMLButtonElement>(null)
  // Clearing the facet is the whole of this action: `state` returns to ALL, the list
  // re-keys under the SAME tenant and epoch (a filter change is not an authority
  // change), and DataTable stays mounted, so its free-text search — and anything
  // else on screen — survives. Nothing is created, patched, retired or launched.
  const clearStateFilter = () => {
    setState(ALL)
    stateTriggerRef.current?.focus()
  }

  // A registration draft ends when the write tier leaves — the dialog was already
  // unmounted without it, but its OPEN state used to survive, so getting the tier
  // back reopened a dialog nobody asked for. Restoring a permission is not a
  // gesture; only the button is offered again. Adjusted during render.
  const [seenWrite, setSeenWrite] = useState(canWrite)
  if (seenWrite !== canWrite) {
    setSeenWrite(canWrite)
    if (!canWrite) setCreateOpen(false)
  }

  const params = useMemo(
    () =>
      state === ALL ? { limit: PROFILE_PAGE } : { state, limit: PROFILE_PAGE },
    [state],
  )
  const query = useInfiniteQuery({
    queryKey: agentOpsKeys.profiles(activeTenant, boundary.epoch, params),
    queryFn: ({ pageParam, signal }) =>
      agentOpsApi.listProfiles({ ...params, cursor: pageParam }, { signal }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.cursor : undefined),
    enabled: canRead,
  })
  const rows = useMemo(
    () => query.data?.pages.flatMap((p) => p.items) ?? [],
    [query.data],
  )
  // This node's execution environment, exactly as the server reported it on a
  // local profile. Unknown until one exists; the dialog says so rather than guess.
  const localEnvironment = useMemo(
    () => rows.find((p) => p.local_environment)?.environment_ref,
    [rows],
  )

  const columns = useMemo<TableColumn<ProviderProfileDTO>[]>(
    () => [
      {
        id: 'name',
        header: t('profiles.cols.name'),
        accessorFn: (p) => `${p.display_name ?? ''} ${p.profile_ref}`,
        cell: ({ row }) => (
          <span className="flex flex-col">
            <span className="font-medium text-foreground">
              {row.original.display_name || row.original.profile_ref}
            </span>
            {row.original.display_name && (
              <span className="font-mono text-[11px] text-muted-foreground">
                {row.original.profile_ref}
              </span>
            )}
          </span>
        ),
      },
      {
        accessorKey: 'driver',
        header: t('profiles.cols.driver'),
        cell: ({ getValue }) => (
          <span className="font-mono text-xs text-foreground">
            {getValue<string>()}
          </span>
        ),
      },
      {
        id: 'environment',
        header: t('profiles.cols.environment'),
        accessorFn: (p) => p.environment_ref,
        cell: ({ row }) => <EnvironmentCell profile={row.original} />,
      },
      {
        accessorKey: 'state',
        header: t('profiles.cols.state'),
        cell: ({ getValue }) => <StateBadge state={getValue<string>()} />,
      },
      {
        id: 'operable',
        header: t('profiles.cols.launch'),
        accessorFn: (p) => (p.operable ? 1 : 0),
        cell: ({ row }) => <OperableBadge profile={row.original} />,
      },
      {
        accessorKey: 'created_at',
        header: t('profiles.cols.created'),
        cell: ({ getValue }) => (
          <span className="text-xs text-muted-foreground">
            {formatDateTime(getValue<string>(), i18n.language)}
          </span>
        ),
      },
      {
        id: 'actions',
        header: '',
        enableSorting: false,
        cell: ({ row }) => (
          <div className="flex justify-end">
            <Button
              variant="ghost"
              size="sm"
              onClick={(e) => {
                e.stopPropagation()
                setSelected(row.original)
              }}
            >
              {t('profiles.actions.details')}
            </Button>
          </div>
        ),
      },
    ],
    [t, i18n.language],
  )

  if (!canRead) {
    return (
      <p className="rounded-md border border-border bg-muted px-2.5 py-2 text-xs text-muted-foreground">
        {t('profiles.noRead')}
      </p>
    )
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-start justify-between gap-3">
        {describe ? (
          <p className="text-sm text-muted-foreground">
            {t('profiles.subtitle')}
          </p>
        ) : (
          <span />
        )}
        {canWrite && (
          <Button
            variant="primary"
            size="sm"
            onClick={() => setCreateOpen(true)}
          >
            <Plus className="size-3.5" />
            {t('profiles.register')}
          </Button>
        )}
      </div>

      <DataTable
        columns={columns}
        data={rows}
        isLoading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        getRowId={(p) => p.profile_ref}
        onRowClick={(p) => setSelected(p)}
        searchable
        searchPlaceholder={t('profiles.search')}
        toolbar={
          <Select value={state} onValueChange={setState}>
            <SelectTrigger
              ref={stateTriggerRef}
              className="h-7 w-auto min-w-[9rem] text-xs"
              aria-label={t('profiles.allStates')}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('profiles.allStates')}</SelectItem>
              {STATES.map((s) => (
                <SelectItem key={s} value={s}>
                  {t(`profiles.state.${s}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        }
        stickyHeader
        hasMore={!!query.hasNextPage}
        onLoadMore={() => void query.fetchNextPage()}
        isFetchingMore={query.isFetchingNextPage}
        label={t('profiles.title')}
        // DataTable paints `empty` only for a SUCCESSFUL read with no rows: loading,
        // 403, step-up and lookup failures take precedence inside it, so neither node
        // below can appear over an error, and the clear action never reaches one.
        // Which node is a question the CALLER must answer, because `state` narrows the
        // list on the SERVER (`params`): an empty page under a state filter is what the
        // engine returned for that state, and says nothing about the other states — so
        // the copy names the filter and offers to drop it, and claims nothing else.
        empty={
          state === ALL ? (
            <EmptyState
              icon={<Fingerprint />}
              title={t('profiles.empty.title')}
              // No create workflow of its own: the one control that registers a
              // profile is the header button, gated by `sessions:profile:write`. A
              // writer is pointed at it by its own label; a reader is told which
              // permission is missing, not that the product cannot be used.
              description={
                canWrite
                  ? t('profiles.empty.registerHint', {
                      control: t('profiles.register'),
                    })
                  : t('profiles.empty.noWrite')
              }
            />
          ) : (
            <EmptyState
              icon={<Fingerprint />}
              title={t('profiles.filteredEmpty.title')}
              description={t('profiles.filteredEmpty.description', {
                state: t(`profiles.state.${state}`),
              })}
              action={
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={clearStateFilter}
                >
                  {t('profiles.filteredEmpty.clear')}
                </Button>
              }
            />
          )
        }
      />

      {canWrite && (
        <ProfileCreateDialog
          open={createOpen}
          onOpenChange={setCreateOpen}
          localEnvironment={localEnvironment}
        />
      )}

      <Sheet
        open={selected !== null}
        onOpenChange={(o) => !o && setSelected(null)}
      >
        <SheetContent className="flex w-full flex-col gap-4 overflow-y-auto sm:max-w-3xl">
          {selected && (
            // Keyed by the profile: choosing another one starts from nothing revealed.
            <ProfileSheetBody key={selected.profile_ref} initial={selected} />
          )}
        </SheetContent>
      </Sheet>
    </div>
  )
}

function EnvironmentCell({ profile }: { profile: ProviderProfileDTO }) {
  const { t } = useTranslation('agentops')
  const local = profile.local_environment
  return (
    <span className="flex flex-col items-start gap-0.5">
      <span
        className="font-mono text-xs text-muted-foreground"
        title={profile.environment_ref}
      >
        {profile.environment_ref}
      </span>
      <Badge
        variant={local ? 'accent' : 'warning'}
        title={local ? undefined : t('profiles.environment.foreignHint')}
      >
        {local
          ? t('profiles.environment.local')
          : t('profiles.environment.foreign')}
      </Badge>
    </span>
  )
}

function StateBadge({ state }: { state: string }) {
  const { t } = useTranslation('agentops')
  const variant =
    state === 'active' ? 'success' : state === 'retired' ? 'danger' : 'neutral'
  return (
    <Badge variant={variant}>
      {t(`profiles.state.${state}`, { defaultValue: state })}
    </Badge>
  )
}

/** Legacy enablement flag as the server reported it — not a launch guarantee. */
function OperableBadge({ profile }: { profile: ProviderProfileDTO }) {
  const { t } = useTranslation('agentops')
  return profile.operable ? (
    <Badge variant="success">{t('profiles.operable.yes')}</Badge>
  ) : (
    <Badge variant="neutral" title={t('profiles.operable.noHint')}>
      {t('profiles.operable.no')}
    </Badge>
  )
}

function ProfileSheetBody({ initial }: { initial: ProviderProfileDTO }) {
  const { t, i18n } = useTranslation('agentops')
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const canWrite = can('sessions:profile:write')
  const canAdmin = can('sessions:profile:admin')
  const ref = initial.profile_ref
  const [readinessTransport, setReadinessTransport] =
    useState<LaunchReadinessTransport>(DEFAULT_READINESS_TRANSPORT)

  const detail = useQuery({
    queryKey: agentOpsKeys.profile(activeTenant, boundary.epoch, ref),
    queryFn: ({ signal }) => agentOpsApi.getProfile(ref, { signal }),
    placeholderData: initial,
  })
  const profile = detail.data ?? initial
  const retired = profile.state === 'retired'
  const readinessQuery = useProfileLaunchReadiness({
    enabled: true,
    profileRef: ref,
    transport: readinessTransport,
    isolation: DEFAULT_READINESS_ISOLATION,
    profileState: profile.state,
    authSource: profile.auth_source,
    updatedAt: profile.updated_at,
  })

  const [renameOpen, setRenameOpen] = useState(false)
  const [retireOpen, setRetireOpen] = useState(false)

  // THE ONLY READ THAT CARRIES A PATH, as an explicit cycle: it starts on Reveal, it
  // ends on Hide, on unmount, or on any change of permission, principal, tenant or
  // credential — and a response landing after any of those is discarded, never
  // painted, never cached. Nothing of it enters the QueryCache; there is no key.
  const reveal = useFreshRead({
    read: (signal) => agentOpsApi.profileConfiguration(ref, { signal }),
    allowed: canAdmin,
    boundary: boundary.key,
  })
  const revealed =
    reveal.state.status === 'loading' || reveal.state.status === 'ready'

  // A confirmation or a draft opened under a tier that is gone, or for a row that
  // moved on, closes by itself — adjusted DURING render (the sanctioned pattern),
  // so it never paints again without its tier. The boundary remounts this body from
  // its parent. The notice is a side effect, announced by the effect below once per
  // closure the revocation caused.
  const [closedByRevocation, setClosedByRevocation] = useState(0)
  const [seenTier, setSeenTier] = useState({ canAdmin, canWrite, retired })
  if (
    seenTier.canAdmin !== canAdmin ||
    seenTier.canWrite !== canWrite ||
    seenTier.retired !== retired
  ) {
    setSeenTier({ canAdmin, canWrite, retired })
    if ((!canAdmin || retired) && retireOpen) {
      setRetireOpen(false)
      if (!canAdmin) setClosedByRevocation((n) => n + 1)
    }
    if (!canWrite && renameOpen) setRenameOpen(false)
  }
  useEffect(() => {
    if (closedByRevocation > 0) {
      toast.warning(t('profiles.authority.confirmationClosed'))
    }
  }, [closedByRevocation, t])

  const patch = usePrivilegedMutation<PatchProfileRequest, ProviderProfileDTO>({
    mutationFn: (body) => {
      if (!canWrite || retired) {
        throw new AuthorityLostError()
      }
      return agentOpsApi.patchProfile(ref, body)
    },
    invalidateKeys: () => [
      agentOpsKeys.profiles(activeTenant, boundary.epoch),
      agentOpsKeys.profile(activeTenant, boundary.epoch, ref),
    ],
    successMessage: (_data, vars) =>
      vars.state === 'disabled'
        ? t('profiles.disable.success')
        : vars.state === 'active'
          ? t('profiles.enable.success')
          : t('profiles.rename.success'),
    onDone: () => setRenameOpen(false),
    onError: (err) => {
      if (!(err instanceof AuthorityLostError)) return false
      setRenameOpen(false)
      toast.warning(t('profiles.authority.lost'))
      return true
    },
  })

  const retire = usePrivilegedMutation<void, ProviderProfileDTO>({
    mutationFn: () => {
      // Judged at DISPATCH: a queued click or a stale closure gets the same answer.
      if (!canAdmin || retired) {
        throw new AuthorityLostError()
      }
      return agentOpsApi.retireProfile(ref)
    },
    invalidateKeys: () => [
      agentOpsKeys.profiles(activeTenant, boundary.epoch),
      agentOpsKeys.profile(activeTenant, boundary.epoch, ref),
      agentOpsKeys.bindings(activeTenant, boundary.epoch),
    ],
    successMessage: t('profiles.retire.success'),
    onDone: () => setRetireOpen(false),
    onError: (err) => {
      if (!(err instanceof AuthorityLostError)) return false
      setRetireOpen(false)
      toast.warning(t('profiles.authority.lost'))
      return true
    },
  })

  const onConfirmRetire = () => {
    if (!canAdmin || retired) {
      setRetireOpen(false)
      toast.warning(t('profiles.authority.lost'))
      return
    }
    retire.mutate()
  }

  const onPatch = (body: PatchProfileRequest) => {
    if (!canWrite || retired) {
      setRenameOpen(false)
      toast.warning(t('profiles.authority.lost'))
      return
    }
    patch.mutate(body)
  }

  return (
    <>
      <SheetHeader>
        <SheetTitle className="flex items-center gap-2">
          <Fingerprint className="size-4 text-accent-text" />
          <span className="truncate">
            {profile.display_name || profile.profile_ref}
          </span>
        </SheetTitle>
        <SheetDescription className="font-mono text-xs">
          {profile.profile_ref}
        </SheetDescription>
      </SheetHeader>

      {detail.isError && (
        <p className="text-xs text-warning">
          {t('profiles.details.loadFailed')}
        </p>
      )}

      <KvList>
        <KvRow label={t('profiles.details.ref')} mono>
          {profile.profile_ref}
        </KvRow>
        <KvRow label={t('profiles.details.driver')} mono>
          {profile.driver}
        </KvRow>
        <KvRow label={t('profiles.details.environment')}>
          <EnvironmentCell profile={profile} />
        </KvRow>
        <KvRow label={t('profiles.details.state')}>
          <StateBadge state={profile.state} />
        </KvRow>
        <KvRow label={t('profiles.details.launch')}>
          <OperableBadge profile={profile} />
        </KvRow>
        <KvRow label={t('profiles.details.created')}>
          {formatDateTime(profile.created_at, i18n.language)}
        </KvRow>
        <KvRow label={t('profiles.details.updated')}>
          {formatDateTime(profile.updated_at, i18n.language)}
        </KvRow>
        {profile.retired_at && (
          <KvRow label={t('profiles.details.retiredAt')}>
            {formatDateTime(profile.retired_at, i18n.language)}
          </KvRow>
        )}
      </KvList>

      <div className="flex flex-col gap-2">
        <Field label={t('readiness.transport')}>
          <Select
            value={readinessTransport}
            onValueChange={(v) =>
              setReadinessTransport(v as LaunchReadinessTransport)
            }
          >
            <SelectTrigger aria-label={t('readiness.transport')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="stream-json">
                {t('transport.stream-json')}
              </SelectItem>
              <SelectItem value="remote-control">
                {t('transport.remote-control')}
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>
        <LaunchReadinessPanel
          query={readinessQuery}
          profileRef={ref}
          transport={readinessTransport}
          isolation={DEFAULT_READINESS_ISOLATION}
        />
      </div>

      {(canWrite || canAdmin) && !retired && (
        <div className="flex flex-wrap items-center gap-2">
          {canWrite && (
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setRenameOpen(true)}
            >
              {t('profiles.actions.rename')}
            </Button>
          )}
          {canWrite &&
            (profile.state === 'active' ? (
              <Button
                variant="secondary"
                size="sm"
                title={t('profiles.disable.hint')}
                disabled={patch.isPending}
                onClick={() => onPatch({ state: 'disabled' })}
              >
                {t('profiles.actions.disable')}
              </Button>
            ) : (
              <Button
                variant="secondary"
                size="sm"
                disabled={patch.isPending}
                onClick={() => onPatch({ state: 'active' })}
              >
                {t('profiles.actions.enable')}
              </Button>
            ))}
          {canAdmin && (
            <Button
              variant="destructive"
              size="sm"
              onClick={() => setRetireOpen(true)}
            >
              {t('profiles.actions.retire')}
            </Button>
          )}
        </div>
      )}

      {canAdmin && (
        <section className="flex flex-col gap-2">
          <div className="flex items-center justify-between gap-2">
            <h3 className="text-sm font-medium text-foreground">
              {t('profiles.configuration.title')}
            </h3>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => (revealed ? reveal.stop() : reveal.start())}
            >
              {revealed ? (
                <EyeOff className="size-3.5" />
              ) : (
                <Eye className="size-3.5" />
              )}
              {revealed
                ? t('profiles.actions.hide')
                : t('profiles.actions.reveal')}
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">
            {t('profiles.configuration.hint')}
          </p>
          {reveal.state.status === 'loading' && (
            <p className="text-xs text-muted-foreground" role="status">
              {t('profiles.configuration.loading')}
            </p>
          )}
          {(reveal.state.status === 'error' ||
            reveal.state.status === 'forbidden') && (
            <p className="text-xs text-warning">
              {t('profiles.configuration.failed')}
            </p>
          )}
          {reveal.current && reveal.state.status === 'ready' && (
            <>
              <KvList>
                <KvRow
                  label={t('profiles.configuration.configHome')}
                  mono
                  align="start"
                >
                  {reveal.state.data.config_home}
                </KvRow>
                <KvRow
                  label={t('profiles.configuration.userHome')}
                  mono
                  align="start"
                >
                  {reveal.state.data.user_home}
                </KvRow>
              </KvList>
              <p className="text-xs text-muted-foreground">
                {t('profiles.configuration.validatedOn', {
                  env: reveal.state.data.environment_ref,
                })}
              </p>
            </>
          )}
        </section>
      )}

      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-medium text-foreground">
          {t('profiles.bindings.title')}
        </h3>
        <BindingsTable profile={profile} />
      </section>

      {canWrite && (
        <RenameDialog
          open={renameOpen}
          onOpenChange={setRenameOpen}
          current={profile.display_name ?? ''}
          pending={patch.isPending}
          onSubmit={(name) => onPatch({ display_name: name })}
        />
      )}

      <ConfirmDialog
        open={retireOpen && canAdmin && !retired}
        onOpenChange={(o) => !o && setRetireOpen(false)}
        tone="danger"
        title={t('profiles.retire.title')}
        description={t('profiles.retire.description')}
        confirmLabel={t('profiles.retire.confirm')}
        confirmPhrase={profile.display_name?.trim() || profile.profile_ref}
        pending={retire.isPending}
        onConfirm={onConfirmRetire}
      />
    </>
  )
}

function RenameDialog({
  open,
  onOpenChange,
  current,
  pending,
  onSubmit,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  current: string
  pending: boolean
  onSubmit: (name: string) => void
}) {
  const { t } = useTranslation('agentops')
  const [name, setName] = useState(current)
  // Start from the CURRENT label each time the dialog opens (adjusted in render, the
  // sanctioned pattern), so a stale draft never outlives a rename made elsewhere.
  const [prevOpen, setPrevOpen] = useState(open)
  if (open !== prevOpen) {
    setPrevOpen(open)
    if (open) setName(current)
  }
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!pending) onSubmit(name.trim())
  }
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => (pending ? undefined : onOpenChange(o))}
    >
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t('profiles.rename.title')}</DialogTitle>
          <DialogDescription>
            {t('profiles.rename.description')}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="flex flex-col gap-3">
          <Field label={t('profiles.rename.label')}>
            <Input value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
          <DialogFooter>
            <Button
              type="button"
              variant="secondary"
              onClick={() => onOpenChange(false)}
              disabled={pending}
            >
              {t('browser.cancel')}
            </Button>
            <Button type="submit" variant="primary" disabled={pending}>
              {pending && <Spinner className="size-3.5" />}
              {pending
                ? t('profiles.rename.submitting')
                : t('profiles.rename.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
