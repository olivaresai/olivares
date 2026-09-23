// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  useInfiniteQuery,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import { ContactRound, Plus } from 'lucide-react'
import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { FillingTable, TableRegion } from '@/components/data/filling-table'
import { RowOpenButton } from '@/components/data/row-open-button'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
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
import { PagePrimaryAction } from '@/components/ui/page-actions'
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
import { NamedRef } from '@/features/shared'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { formatDateTime } from '@/lib/format'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { HIDDEN_ON_PHONE } from '@/lib/hooks/use-is-phone'
import { agentOpsApi, agentOpsKeys, PROFILE_PAGE } from './api'
import { AuthorityLostError, useAuthBoundary } from './auth-boundary'
import { ProfileListError, StateBadge } from './profiles-panel'
import type { ProviderAccountDTO } from './types'
import './i18n'

/** The picker's "enter a reference" choice: never a profile reference. */
const MANUAL = '__manual__'

/**
 * THE LIFETIME OF ACCOUNT BYTES. Every account read — the list, a point read, the
 * reconciliation read — lives exactly as long as a mounted observer of it. When the last
 * one unmounts, TanStack cancels a read still in flight (its signal was handed to the
 * client) and removes the entry at once. The page is unmounted by whatever gates it, the
 * provider room's Accounts tab or the route guard, before it could ever render without
 * the read tier. Tying the bytes to the observer is therefore the one place a loss of
 * read is always seen. A regrant mounts a new observer, which must read before it paints.
 */
const ACCOUNT_READ_LIFETIME = { gcTime: 0 } as const

/**
 * The one piece of account state that outlives the page: an adoption submitted in this
 * boundary whose answer the page has not shown. It holds the profile reference the
 * operator submitted and nothing the engine said about an account, so it may survive a
 * loss of read. It sits under the boundary scope, so a change of principal, tenant or
 * credential removes it with everything else of that boundary.
 */
interface AdoptionIntent {
  profileRef: string
}

/** A known answer shown by the page once the draft dialog is gone. */
type AdoptionOutcome =
  | { kind: 'adopted'; profileRef: string; account: ProviderAccountDTO }
  | { kind: 'refused'; profileRef: string; reason: string }
  | { kind: 'forbidden'; profileRef: string }

/** What an adopt failure means, or null when nothing was answered to this owner (a
 *  retired attempt, a tier that left before dispatch). `assurance` is the engine's
 *  step-up demand: a definite refusal decided BEFORE any effect, whose ceremony the
 *  privileged-mutation lifecycle owns. `unknown` means the request may have been
 *  applied: a network failure or a server error. */
type AdoptFailure =
  | { kind: 'assurance' }
  | { kind: 'forbidden' }
  | { kind: 'refused'; reason: string }
  | { kind: 'unknown' }

function adoptFailure(err: unknown): AdoptFailure | null {
  if (err instanceof AuthorityLostError) return null
  if (err instanceof DOMException && err.name === 'AbortError') return null
  if (err instanceof ApiError) {
    if (err.isStepUpRequired) return { kind: 'assurance' }
    if (err.isForbidden || err.isUnauthenticated) return { kind: 'forbidden' }
    if (err.isServerError) return { kind: 'unknown' }
    // 400, 404, 409, 422: the engine's own sentence names the reason (the name that is
    // taken, the profile that is already an account, the retired profile, the shape).
    return { kind: 'refused', reason: err.message }
  }
  return { kind: 'unknown' }
}

interface AdoptVars {
  profileRef: string
  name: string
}

/**
 * ProviderAccountsPanel — the named provider accounts of the active tenant: the list,
 * one account's detail, and adopting an existing provider profile as an account. It is
 * the provider room's Accounts page and is mounted only where the account read tier
 * admits it (the tab and the route guard both gate on it).
 *
 * This panel owns every account read, the submitted adoption and its answer. Tiers are
 * checked where they act: `sessions:account:write` adopts; choosing the profile from a
 * list additionally needs `sessions:profile:read`, and only that choice needs it.
 *
 * Everything shown is what the SERVER said. The isolation level is rendered as stated,
 * the signed-in identity is "not checked" while nothing asked the provider, and no home
 * path is painted. Success is announced only after the engine answered; an answer that
 * cannot be known is reconciled by reading, never by sending the adopt again.
 */
export function ProviderAccountsPanel() {
  const boundary = useAuthBoundary()
  // Remount on the AUTHORITY BOUNDARY (principal, tenant, credential): an open detail,
  // an adopt draft or a request in flight belongs to the boundary that started it.
  return <AccountsPanelInner key={boundary.key} />
}

function AccountsPanelInner() {
  const { t } = useTranslation(['agentops', 'common'])
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const queryClient = useQueryClient()
  const canRead = can('sessions:account:read')
  const canWrite = can('sessions:account:write')
  const accountsKey = agentOpsKeys.accounts(activeTenant, boundary.epoch)
  const intentKey = agentOpsKeys.accountAdoption(activeTenant, boundary.epoch)

  const [adoptOpen, setAdoptOpen] = useState(false)
  // Each opening starts a fresh draft: the dialog body is keyed by it.
  const [adoptRound, setAdoptRound] = useState(0)
  const [dialogFailure, setDialogFailure] = useState<AdoptFailure | null>(null)
  const [outcome, setOutcome] = useState<AdoptionOutcome | null>(null)
  const [selected, setSelected] = useState<ProviderAccountDTO | null>(null)

  // Losing write ends the draft; getting it back is not a gesture that reopens it.
  // A POST already sent is not a draft: its answer is still owned below.
  const [seenWrite, setSeenWrite] = useState(canWrite)
  if (seenWrite !== canWrite) {
    setSeenWrite(canWrite)
    if (!canWrite) setAdoptOpen(false)
  }

  // An answer arrives after the render that submitted it: it reads the CURRENT dialog
  // state and read tier, never the ones captured at submit.
  const latest = useRef({ adoptOpen, canRead })
  useEffect(() => {
    latest.current = { adoptOpen, canRead }
  })

  const query = useInfiniteQuery({
    queryKey: accountsKey,
    queryFn: ({ pageParam, signal }) =>
      agentOpsApi.listAccounts({ cursor: pageParam }, { signal }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.cursor : undefined),
    ...ACCOUNT_READ_LIFETIME,
  })

  // Never fetched: written on submit and cleared once the answer is shown.
  const intent =
    useQuery<AdoptionIntent | null>({
      queryKey: intentKey,
      queryFn: () => null,
      enabled: false,
      staleTime: Infinity,
      gcTime: Infinity,
    }).data ?? null
  const setIntent = (value: AdoptionIntent | null) =>
    queryClient.setQueryData<AdoptionIntent | null>(intentKey, value)

  /** Re-read the account reads this answer may have changed, while this boundary still
   *  holds the read tier. A stale answer never gets here: the owner retires first. */
  const rereadAccounts = (profileRef: string) =>
    latest.current.canRead
      ? [
          accountsKey,
          agentOpsKeys.account(activeTenant, boundary.epoch, profileRef),
        ]
      : []

  const reportFailure = (profileRef: string, err: unknown) => {
    const failure = adoptFailure(err)
    if (!failure) return
    if (failure.kind === 'assurance') {
      // Refused before any effect: nothing to reconcile. The draft stays open while the
      // ceremony runs, and its resume is a new dispatch that reports on its own.
      setIntent(null)
      return
    }
    if (failure.kind === 'unknown') {
      // The request may have been applied. The draft closes, the intent stays for the
      // reconciliation notice, and the accounts are read again.
      setAdoptOpen(false)
      for (const queryKey of rereadAccounts(profileRef)) {
        void queryClient.invalidateQueries({ queryKey })
      }
      return
    }
    setIntent(null)
    if (latest.current.adoptOpen) {
      setDialogFailure(failure)
    } else {
      setOutcome(
        failure.kind === 'refused'
          ? { kind: 'refused', profileRef, reason: failure.reason }
          : { kind: 'forbidden', profileRef },
      )
    }
  }

  const adopt = usePrivilegedMutation<AdoptVars, ProviderAccountDTO>({
    // The submitted variables live in the MutationCache under this boundary's adoption key,
    // so the shell's boundary custody removes them when this boundary retires.
    mutationKey: intentKey,
    mutationFn: async (vars, { signal, dispatchGuard }) => {
      // Judged at DISPATCH: a queued submit after the tier left sends nothing.
      if (!canWrite) throw new AuthorityLostError()
      // From here on the request may take effect: keep what an unknown answer needs.
      setIntent({ profileRef: vars.profileRef })
      try {
        return await agentOpsApi.adoptAccount(
          vars.profileRef,
          vars.name === '' ? {} : { name: vars.name },
          { tenant: activeTenant, dispatchGuard },
        )
      } catch (err) {
        // Reported HERE, around the one call site, and not through the options of the
        // operator's call: a step-up resume re-dispatches this execution without them,
        // and its answer must be reported all the same. A retired attempt (unmounted
        // page, moved boundary) reports nothing: its owner is gone.
        if (!signal.aborted) reportFailure(vars.profileRef, err)
        throw err
      }
    },
    invalidateKeys: (account) => rereadAccounts(account.account_ref),
    successMessage: (account) =>
      t('accounts.adoptDialog.success', { name: account.name }),
    onDone: (account, vars) => {
      setIntent(null)
      if (latest.current.adoptOpen) {
        setAdoptOpen(false)
        setSelected(account)
      } else {
        setOutcome({ kind: 'adopted', profileRef: vars.profileRef, account })
      }
    },
    onError: (err) => {
      if (err instanceof AuthorityLostError) {
        setAdoptOpen(false)
        toast.warning(t('profiles.authority.lost'))
        return true
      }
      // The engine's answers were reported inside the mutation, beside the adoption.
      return err instanceof ApiError || err instanceof NetworkError
    },
  })

  const submit = (vars: AdoptVars) => {
    if (adopt.isPending) return
    if (!canWrite) {
      setAdoptOpen(false)
      toast.warning(t('profiles.authority.lost'))
      return
    }
    setDialogFailure(null)
    setOutcome(null)
    adopt.mutate(vars)
  }

  const rows = useMemo(
    () => query.data?.pages.flatMap((p) => p.items) ?? [],
    [query.data],
  )
  const lastPage = query.data?.pages[query.data.pages.length - 1]
  const knownAccountRefs = useMemo(
    () => new Set(rows.map((a) => a.account_ref)),
    [rows],
  )

  const openAdopt = () => {
    setDialogFailure(null)
    setAdoptRound((n) => n + 1)
    setAdoptOpen(true)
  }
  const pendingRef = intent?.profileRef ?? adopt.variables?.profileRef ?? ''
  const firstPageFailed = query.isError && !query.isFetchNextPageError

  return (
    <div className="flex flex-col gap-3">
      {canWrite ? (
        <PagePrimaryAction>
          <Button variant="primary" size="sm" onClick={openAdopt}>
            <Plus className="size-3.5" />
            {t('accounts.adopt')}
          </Button>
        </PagePrimaryAction>
      ) : null}

      {adopt.isPending && !adoptOpen ? (
        <p
          role="status"
          className="rounded-md border border-border bg-muted px-3 py-2 text-caption text-foreground"
        >
          {t('accounts.adoption.pending', { ref: pendingRef })}
        </p>
      ) : outcome ? (
        <AdoptionOutcomeNotice
          outcome={outcome}
          onOpen={setSelected}
          onDismiss={() => setOutcome(null)}
        />
      ) : intent && !adopt.isPending ? (
        <UnresolvedAdoption
          profileRef={intent.profileRef}
          onOpen={setSelected}
          onDismiss={() => setIntent(null)}
        />
      ) : null}

      {query.isLoading ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : firstPageFailed ? (
        <ProfileListError
          error={query.error}
          onRetry={() => void query.refetch()}
        />
      ) : rows.length === 0 ? (
        <TableRegion fill>
          <EmptyState
            className="flex-1"
            icon={<ContactRound />}
            title={t('accounts.empty.title')}
            description={
              canWrite
                ? t('accounts.empty.adoptHint', {
                    control: t('accounts.adopt'),
                  })
                : t('accounts.empty.noWrite')
            }
          />
        </TableRegion>
      ) : (
        <AccountsTable
          accounts={rows}
          onOpen={setSelected}
          more={!!lastPage?.has_more}
          nextPageFailed={query.isFetchNextPageError}
          hasNextPage={!!query.hasNextPage}
          onLoadMore={() => void query.fetchNextPage()}
          isFetchingMore={query.isFetchingNextPage}
        />
      )}

      {canWrite && (
        <AdoptAccountDialog
          key={adoptRound}
          open={adoptOpen}
          onOpenChange={setAdoptOpen}
          knownAccountRefs={knownAccountRefs}
          pending={adopt.isPending}
          pendingRef={pendingRef}
          failure={dialogFailure}
          onSubmit={submit}
        />
      )}

      <Sheet
        open={selected !== null}
        onOpenChange={(o) => !o && setSelected(null)}
      >
        <SheetContent className="flex w-full flex-col gap-4 overflow-y-auto sm:max-w-xl">
          {selected && (
            <AccountSheetBody key={selected.account_ref} initial={selected} />
          )}
        </SheetContent>
      </Sheet>
    </div>
  )
}

/** A known answer to an adoption whose draft is gone: the write tier left while the
 *  POST was parked. */
function AdoptionOutcomeNotice({
  outcome,
  onOpen,
  onDismiss,
}: {
  outcome: AdoptionOutcome
  onOpen: (account: ProviderAccountDTO) => void
  onDismiss: () => void
}) {
  const { t } = useTranslation('agentops')
  return (
    <div className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-muted px-3 py-2 text-caption text-foreground">
      <p role="status" className="min-w-0 flex-1">
        {outcome.kind === 'adopted'
          ? t('accounts.adoption.adopted', {
              ref: outcome.profileRef,
              name: outcome.account.name,
            })
          : outcome.kind === 'refused'
            ? `${t('accounts.adoption.refused', { ref: outcome.profileRef })} ${outcome.reason}`
            : t('accounts.adoption.forbidden', { ref: outcome.profileRef })}
      </p>
      {outcome.kind === 'adopted' ? (
        <Button
          variant="secondary"
          size="sm"
          onClick={() => onOpen(outcome.account)}
        >
          {t('accounts.adoption.open')}
        </Button>
      ) : null}
      <Button variant="ghost" size="sm" onClick={onDismiss}>
        {t('accounts.adoption.dismiss')}
      </Button>
    </div>
  )
}

/**
 * An adoption whose answer is not known here: a network failure or a server error, or
 * an answer that arrived while this page was not mounted. It reconciles by READING the
 * profile's account (the account reference is the profile reference). A 200 means the
 * profile is an account now. A 404 means it is not, at this read. It never sends the
 * adopt again.
 */
function UnresolvedAdoption({
  profileRef,
  onOpen,
  onDismiss,
}: {
  profileRef: string
  onOpen: (account: ProviderAccountDTO) => void
  onDismiss: () => void
}) {
  const { t } = useTranslation('agentops')
  const { activeTenant } = useAuth()
  const boundary = useAuthBoundary()
  const check = useQuery({
    queryKey: agentOpsKeys.account(activeTenant, boundary.epoch, profileRef),
    queryFn: ({ signal }) => agentOpsApi.getAccount(profileRef, { signal }),
    ...ACCOUNT_READ_LIFETIME,
  })
  const notAccount = check.error instanceof ApiError && check.error.isNotFound
  const current = check.isFetching
    ? t('accounts.adoption.checking', { ref: profileRef })
    : notAccount
      ? t('accounts.adoption.notAccount', { ref: profileRef })
      : check.isError
        ? t('accounts.adoption.checkFailed')
        : check.data
          ? t('accounts.adoption.isAccount', {
              ref: profileRef,
              name: check.data.name,
            })
          : null
  const found = !check.isFetching && !check.isError ? check.data : undefined
  return (
    <div className="flex flex-col gap-2 rounded-md border border-warning-line bg-warning-soft px-3 py-2 text-caption text-foreground">
      <p className="font-medium">
        {t('accounts.adoption.unknown', { ref: profileRef })}
      </p>
      <p role="status">{current}</p>
      <div className="flex flex-wrap gap-2">
        {found ? (
          <Button variant="secondary" size="sm" onClick={() => onOpen(found)}>
            {t('accounts.adoption.open')}
          </Button>
        ) : null}
        <Button
          variant="secondary"
          size="sm"
          onClick={() => void check.refetch()}
          disabled={check.isFetching}
        >
          {t('accounts.adoption.checkAgain')}
        </Button>
        <Button variant="ghost" size="sm" onClick={onDismiss}>
          {t('accounts.adoption.dismiss')}
        </Button>
      </div>
    </div>
  )
}

function AccountsTable({
  accounts,
  onOpen,
  more,
  nextPageFailed,
  hasNextPage,
  onLoadMore,
  isFetchingMore,
}: {
  accounts: ProviderAccountDTO[]
  onOpen: (account: ProviderAccountDTO) => void
  /** The last page the engine answered said more accounts exist. */
  more: boolean
  nextPageFailed: boolean
  hasNextPage: boolean
  onLoadMore: () => void
  isFetchingMore: boolean
}) {
  const { t, i18n } = useTranslation(['agentops', 'common'])
  return (
    <FillingTable
      fill
      oneLine
      colSpan={6}
      after={
        more || nextPageFailed ? (
          <div className="flex flex-col items-center gap-1 border-t border-border p-2">
            {/* Never a silent first page: the screen says the list goes on. */}
            <p role="status" className="text-caption text-muted-foreground">
              {nextPageFailed
                ? t('accounts.moreFailed')
                : t('accounts.more', { shown: accounts.length })}
            </p>
            {hasNextPage ? (
              <Button
                variant="ghost"
                size="sm"
                onClick={onLoadMore}
                disabled={isFetchingMore}
              >
                {isFetchingMore
                  ? t('common:states.loading')
                  : t('common:table.loadMore')}
              </Button>
            ) : null}
          </div>
        ) : null
      }
    >
      <thead>
        <tr>
          <th>{t('accounts.cols.name')}</th>
          <th className={HIDDEN_ON_PHONE}>{t('accounts.cols.driver')}</th>
          <th className={HIDDEN_ON_PHONE}>{t('accounts.cols.environment')}</th>
          <th>{t('accounts.cols.state')}</th>
          <th className={HIDDEN_ON_PHONE}>{t('accounts.cols.isolation')}</th>
          <th className={HIDDEN_ON_PHONE}>{t('accounts.cols.created')}</th>
        </tr>
      </thead>
      <tbody>
        {accounts.map((account) => (
          <tr
            key={account.account_ref}
            className="cursor-pointer"
            onClick={() => onOpen(account)}
          >
            <td title={account.account_ref}>
              <RowOpenButton onOpen={() => onOpen(account)}>
                <NamedRef
                  className="font-medium text-foreground"
                  name={account.name}
                  reference={account.account_ref}
                  fallback={account.account_ref}
                  mono
                />
              </RowOpenButton>
            </td>
            <td
              className={`font-mono text-caption text-muted-foreground ${HIDDEN_ON_PHONE}`}
            >
              {account.driver}
            </td>
            <td
              className={`font-mono text-caption text-muted-foreground ${HIDDEN_ON_PHONE}`}
              title={account.environment_ref}
            >
              {account.environment_ref}
            </td>
            <td>
              <StateBadge state={account.state} />
            </td>
            <td className={HIDDEN_ON_PHONE}>
              <IsolationBadge level={account.isolation_level} />
            </td>
            <td
              className={`text-caption text-muted-foreground ${HIDDEN_ON_PHONE}`}
            >
              {account.created_at
                ? formatDateTime(account.created_at, i18n.language)
                : null}
            </td>
          </tr>
        ))}
      </tbody>
    </FillingTable>
  )
}

/** The isolation the server STATED. Shared is not dressed up as dedicated. */
function IsolationBadge({ level }: { level: string }) {
  const { t } = useTranslation('agentops')
  if (level === 'shared') {
    return (
      <Badge variant="neutral" title={t('accounts.isolation.sharedMeaning')}>
        {t('accounts.isolation.shared')}
      </Badge>
    )
  }
  if (level === 'dedicated') {
    return (
      <Badge variant="info" title={t('accounts.isolation.dedicatedMeaning')}>
        {t('accounts.isolation.dedicated')}
      </Badge>
    )
  }
  return <Badge variant="outline">{level}</Badge>
}

/** What the stated isolation means, in words; nothing for a level this console does
 *  not know, rather than a guess. */
function IsolationMeaning({ level }: { level: string }) {
  const { t } = useTranslation('agentops')
  if (level !== 'shared' && level !== 'dedicated') return null
  return (
    <span className="text-caption text-muted-foreground">
      {level === 'shared'
        ? t('accounts.isolation.sharedMeaning')
        : t('accounts.isolation.dedicatedMeaning')}
    </span>
  )
}

/** A failed point read of one account. A 404 has an account meaning — not found, or not
 *  visible to this member — and is said so explicitly; every other failure is the
 *  provider room's shared read-failure state. */
function AccountReadError({
  error,
  onRetry,
}: {
  error: unknown
  onRetry: () => void
}) {
  const { t } = useTranslation('agentops')
  if (error instanceof ApiError && error.isNotFound) {
    return (
      <p role="status" className="text-body text-muted-foreground">
        {t('accounts.details.notFound')}
      </p>
    )
  }
  return <ProfileListError error={error} onRetry={onRetry} />
}

function AccountSheetBody({ initial }: { initial: ProviderAccountDTO }) {
  const { t, i18n } = useTranslation('agentops')
  const { activeTenant } = useAuth()
  const boundary = useAuthBoundary()
  const ref = initial.account_ref

  // The point read of THIS account as the engine holds it now. The row that opened it
  // names the sheet; every field below comes from this answer or is not shown.
  const detail = useQuery({
    queryKey: agentOpsKeys.account(activeTenant, boundary.epoch, ref),
    queryFn: ({ signal }) => agentOpsApi.getAccount(ref, { signal }),
    ...ACCOUNT_READ_LIFETIME,
  })
  const account = detail.data

  return (
    <>
      <SheetHeader>
        <SheetTitle className="flex items-center gap-2">
          <ContactRound className="size-4 text-accent-text" />
          <span className="truncate">{account?.name ?? initial.name}</span>
        </SheetTitle>
        <SheetDescription className="font-mono text-caption">
          {ref}
        </SheetDescription>
      </SheetHeader>

      {detail.isPending ? (
        <p role="status" className="text-caption text-muted-foreground">
          {t('accounts.details.loading')}
        </p>
      ) : detail.isError ? (
        <AccountReadError
          error={detail.error}
          onRetry={() => void detail.refetch()}
        />
      ) : account ? (
        <KvList>
          <KvRow label={t('accounts.details.name')} mono>
            {account.name}
          </KvRow>
          <KvRow label={t('accounts.details.ref')} mono>
            {account.account_ref}
          </KvRow>
          <KvRow label={t('accounts.details.driver')} mono>
            {account.driver}
          </KvRow>
          <KvRow label={t('accounts.details.environment')} mono>
            {account.environment_ref}
          </KvRow>
          <KvRow label={t('accounts.details.state')}>
            <StateBadge state={account.state} />
          </KvRow>
          <KvRow label={t('accounts.details.home')}>
            {t(`accounts.home.${account.home_mode}`, {
              defaultValue: account.home_mode,
            })}
          </KvRow>
          <KvRow label={t('accounts.details.isolation')} align="start">
            <span className="flex flex-col gap-1">
              <span>
                <IsolationBadge level={account.isolation_level} />
              </span>
              <IsolationMeaning level={account.isolation_level} />
            </span>
          </KvRow>
          {account.os_user ? (
            <KvRow label={t('accounts.details.osUser')} mono>
              {account.os_user}
            </KvRow>
          ) : null}
          <KvRow label={t('accounts.details.authSource')}>
            {t(`accounts.authSource.${account.auth_source || 'none'}`, {
              defaultValue: account.auth_source,
            })}
          </KvRow>
          {account.provider_record_ref ? (
            <KvRow label={t('accounts.details.providerRecord')} mono>
              {account.provider_record_ref}
            </KvRow>
          ) : null}
          <KvRow label={t('accounts.details.identity')} align="start">
            {account.identity_source === 'none'
              ? t('accounts.identity.notChecked')
              : account.identity
                ? t('accounts.identity.reported', {
                    identity: account.identity,
                    source: account.identity_source,
                  })
                : t('accounts.identity.empty', {
                    source: account.identity_source,
                  })}
          </KvRow>
          {account.release_ref ? (
            <KvRow label={t('accounts.details.release')} mono>
              {account.release_ref}
            </KvRow>
          ) : null}
          {account.pending_release ? (
            <KvRow label={t('accounts.details.pendingRelease')} mono>
              {account.pending_release}
            </KvRow>
          ) : null}
          <KvRow label={t('accounts.details.created')}>
            {formatDateTime(account.created_at, i18n.language)}
          </KvRow>
          <KvRow label={t('accounts.details.updated')}>
            {formatDateTime(account.updated_at, i18n.language)}
          </KvRow>
        </KvList>
      ) : null}
    </>
  )
}

/**
 * The adoption DRAFT: which profile, under which name. The submitted request and its
 * answer belong to the panel, so a draft that ends (the write tier leaves) never takes
 * a request already sent down with it.
 */
function AdoptAccountDialog({
  open,
  onOpenChange,
  knownAccountRefs,
  pending,
  pendingRef,
  failure,
  onSubmit,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  /** Profiles the loaded account pages show are already accounts. Absence from this
   *  set is not a claim: the engine decides. */
  knownAccountRefs: ReadonlySet<string>
  pending: boolean
  /** The profile the pending request adopts. */
  pendingRef: string
  /** A refusal the engine answered while this draft was open. */
  failure: AdoptFailure | null
  onSubmit: (vars: AdoptVars) => void
}) {
  const { t } = useTranslation(['agentops', 'common'])
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const canListProfiles = can('sessions:profile:read')

  const [choice, setChoice] = useState('')
  const [reference, setReference] = useState('')
  const [name, setName] = useState('')

  // Discovery is the profile plane's own read, under its own tier, and only while
  // this dialog is open. It shares the profile list's key and cache.
  const profiles = useInfiniteQuery({
    queryKey: agentOpsKeys.profiles(activeTenant, boundary.epoch, {
      limit: PROFILE_PAGE,
    }),
    queryFn: ({ pageParam, signal }) =>
      agentOpsApi.listProfiles(
        { limit: PROFILE_PAGE, cursor: pageParam },
        { signal },
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.cursor : undefined),
    enabled: open && canListProfiles,
  })
  const candidates = useMemo(
    () =>
      (profiles.data?.pages.flatMap((p) => p.items) ?? []).filter(
        (p) => p.state !== 'retired',
      ),
    [profiles.data],
  )
  const profilesPartial =
    !!profiles.data?.pages[profiles.data.pages.length - 1]?.has_more
  const profilesLoading = canListProfiles && profiles.isLoading
  const showPicker =
    canListProfiles &&
    !profiles.isError &&
    !(profiles.isSuccess && candidates.length === 0 && !profilesPartial)
  const picked = choice !== '' && choice !== MANUAL
  // The typed reference is always a way in: while the picker loads, when there is no
  // picker, when the operator asks for it, and once something has been typed.
  const showReference =
    !showPicker || choice === MANUAL || profilesLoading || reference !== ''
  const profileRef = picked ? choice : reference.trim()

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (profileRef === '' || pending) return
    // The name travels exactly as typed; an empty one asks the server to generate it.
    onSubmit({ profileRef, name })
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => (pending ? undefined : onOpenChange(o))}
    >
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('accounts.adoptDialog.title')}</DialogTitle>
          <DialogDescription>
            {t('accounts.adoptDialog.description')}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="flex flex-col gap-3">
          {showPicker ? (
            <Field
              label={t('accounts.adoptDialog.profile')}
              description={
                profilesPartial
                  ? t('accounts.adoptDialog.profilesPartial')
                  : undefined
              }
            >
              <Select
                value={choice}
                onValueChange={(value) => {
                  setChoice(value)
                  // A listed profile replaces a typed reference; never both.
                  if (value !== MANUAL) setReference('')
                }}
                disabled={pending || profilesLoading}
              >
                <SelectTrigger>
                  <SelectValue
                    placeholder={t('accounts.adoptDialog.profilePlaceholder')}
                  />
                </SelectTrigger>
                <SelectContent>
                  {candidates.map((p) => {
                    const label = p.display_name
                      ? t('accounts.adoptDialog.profileOption', {
                          name: p.display_name,
                          ref: p.profile_ref,
                        })
                      : p.profile_ref
                    const known = knownAccountRefs.has(p.profile_ref)
                    return (
                      <SelectItem
                        key={p.profile_ref}
                        value={p.profile_ref}
                        disabled={known}
                      >
                        {known
                          ? t('accounts.adoptDialog.knownAccount', { label })
                          : label}
                      </SelectItem>
                    )
                  })}
                  <SelectItem value={MANUAL}>
                    {t('accounts.adoptDialog.manual')}
                  </SelectItem>
                </SelectContent>
              </Select>
            </Field>
          ) : null}
          {showPicker && profilesLoading ? (
            <p role="status" className="text-caption text-muted-foreground">
              {t('accounts.adoptDialog.profilesLoading')}
            </p>
          ) : null}
          {showPicker && profiles.hasNextPage ? (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="self-start"
              onClick={() => void profiles.fetchNextPage()}
              disabled={profiles.isFetchingNextPage || pending}
            >
              {profiles.isFetchingNextPage
                ? t('common:states.loading')
                : t('accounts.adoptDialog.loadMoreProfiles')}
            </Button>
          ) : null}
          {canListProfiles && profiles.isError ? (
            <p className="text-caption text-warning">
              {t('accounts.adoptDialog.profilesFailed')}
            </p>
          ) : null}
          {showReference ? (
            <Field
              label={t('accounts.adoptDialog.reference')}
              description={
                canListProfiles
                  ? t('accounts.adoptDialog.referenceHint')
                  : t('accounts.adoptDialog.referenceHintNoList')
              }
            >
              <Input
                value={reference}
                onChange={(e) => setReference(e.target.value)}
                autoComplete="off"
                disabled={pending}
                mono
              />
            </Field>
          ) : null}
          <Field
            label={t('accounts.adoptDialog.name')}
            description={t('accounts.adoptDialog.nameHint')}
          >
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              autoComplete="off"
              disabled={pending}
              mono
            />
          </Field>
          {pending ? (
            <p role="status" className="text-caption text-muted-foreground">
              {t('accounts.adoptDialog.pendingStatus', { ref: pendingRef })}
            </p>
          ) : null}
          {failure?.kind === 'refused' || failure?.kind === 'forbidden' ? (
            <div
              role="alert"
              className="flex flex-col gap-0.5 rounded-md border border-danger-line bg-danger-soft px-2.5 py-2 text-caption text-foreground"
            >
              {failure.kind === 'refused' ? (
                <>
                  <p className="font-medium">
                    {t('accounts.adoptDialog.failedTitle')}
                  </p>
                  <p>{failure.reason}</p>
                </>
              ) : (
                <p>{t('accounts.adoptDialog.forbidden')}</p>
              )}
            </div>
          ) : null}
          <DialogFooter>
            <Button
              type="button"
              variant="secondary"
              onClick={() => onOpenChange(false)}
              disabled={pending}
            >
              {t('browser.cancel')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={profileRef === '' || pending}
            >
              {pending && <Spinner className="size-3.5" />}
              {pending
                ? t('accounts.adoptDialog.submitting')
                : t('accounts.adoptDialog.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
