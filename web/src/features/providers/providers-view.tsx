// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  firstHourKeys,
  installLatest,
  signInApi,
  type SignInTool,
} from '@/features/first-hour/api'
import { agentToolsKeys } from '@/features/agent-tools/api'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { toolName } from '@/features/agentops/engine-tools'
import { useNewSessionDialog } from '@/features/first-hour/new-session-store'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import {
  KeyRound,
  MoreHorizontal,
  PlugZap,
  Plus,
  RefreshCw,
  Trash2,
} from 'lucide-react'
import { useId, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { withInlineCommands } from '@/components/ui/inline-commands'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Field, FieldError } from '@/components/ui/field'
import { EmptyState } from '@/components/ui/empty-state'
import { ForbiddenState } from '@/components/ui/error-state'
import { Spinner } from '@/components/ui/spinner'
import { ListTruncationBadge } from '@/features/_intel'
import { useAuth } from '@/lib/auth/context'
import { consoleReturnPath } from '@/lib/auth/return-path'
import { formatDateTime } from '@/lib/format'
import { useIsPhone } from '@/lib/hooks/use-is-phone'
import {
  failureText,
  usePrivilegedMutation,
} from '@/lib/hooks/use-privileged-mutation'
import { cn } from '@/lib/utils'
import { providerKeys, providersApi } from './api'
import { useProviderBoundary } from './auth-boundary'
import { ProviderCreateDialog } from './provider-create-dialog'
import { ProviderBindDialog } from './provider-bind-dialog'
import { ProviderRotateDialog } from './provider-rotate-dialog'
import { PROVIDER_KINDS } from './kinds'
import type { ProbeState, ProviderKind, ProviderRecordDTO } from './types'
import './i18n'

/**
 * ApiKeys — the API keys on Tools (#2552): where a new operator lands when they ask
 * "where do I put my API key". It was a page of its own (`/providers`), which is now a
 * door into Tools.
 *
 * Before it there was no answer: the console had provider PROFILES (which home
 * directory an official CLI runs under) and the server had environment variables, and
 * nothing in between that an operator could name, test or revoke.
 *
 * Three rules this list holds itself to, each of them a measured complaint:
 *  1. The empty state names the next action.
 *  2. A registered provider is NEVER shown as working. Registering is not evidence;
 *     only a connection test is, and an untested one says so in its own column.
 *  3. Nothing ends in a form. Creating offers the test; a tested provider offers
 *     the binding that actually launches something.
 *
 * The list is remounted on the authority boundary: nothing read or half-done under
 * one operator survives into the next.
 */
/** The API keys list. `adding` opens its add form from outside the list, as AI tools'
 * header action does; closing the form reports back through `onAddingChange`. */
export function ApiKeys({
  adding = false,
  onAddingChange,
}: {
  adding?: boolean
  onAddingChange?: (open: boolean) => void
} = {}) {
  const boundary = useProviderBoundary()
  return (
    <Inner key={boundary.key} adding={adding} onAddingChange={onAddingChange} />
  )
}

function probeHintKey(record: ProviderRecordDTO) {
  const state = ['ok', 'refused', 'unreachable'].includes(
    record.probe_state ?? '',
  )
    ? record.probe_state
    : 'never'
  return `probe.${record.kind === 'ollama' ? 'local.' : ''}${state}Hint`
}

/** The provider `?add=<kind>` asks for: the setup wizard's "Add an API key" or "Add a
 * local model" opens the form on that provider. Read once, like the other views'
 * `?tab=`; anything else opens nothing. */
function addFromURL(): ProviderKind | undefined {
  const want = new URLSearchParams(window.location.search).get('add')
  return PROVIDER_KINDS.find((k) => k === want)
}

/** Where the wizard's "Add an API key" came from (`?returnTo=`, a console page on this
 * origin only): the person goes back there once the new key passes its test; a refused key
 * keeps them here, where it is fixed. */
function returnFromURL(): string | null {
  const want = new URLSearchParams(window.location.search).get('returnTo')
  return consoleReturnPath(want, window.location.origin)
}

/** The engine's latest "downloaded …" line of a tool job: how far the download is. The
 * job's other lines (URLs, digests, paths) stay in AI tools. */
function downloadLine(progress: string | undefined): string | null {
  const lines = (progress ?? '').split('\n').map((line) => line.trim())
  return lines.filter((line) => line.startsWith('downloaded ')).at(-1) ?? null
}

const SESSION_TOOL: Partial<Record<ProviderKind, SignInTool>> = {
  anthropic: 'claude',
  openai: 'codex',
  gemini: 'gemini-cli',
  ollama: 'opencode',
}

function Inner({
  adding,
  onAddingChange,
}: {
  adding: boolean
  onAddingChange?: (open: boolean) => void
}) {
  const { t, i18n } = useTranslation(['providers', 'errors'])
  const navigate = useNavigate()
  const { activeTenant, can } = useAuth()
  const phone = useIsPhone()
  const boundary = useProviderBoundary()
  const canRead = can('sessions:provider:read')
  const canWrite = can('sessions:provider:write')
  const canAdmin = can('sessions:provider:admin')

  const addReasonId = useId()
  const headingId = useId()
  const [addKind] = useState(addFromURL)
  const [returnTo] = useState(returnFromURL)
  // The provider this page just created, whose first test decides the return.
  const created = useRef<string | null>(null)
  const [ownOpen, setOwnOpen] = useState(!!addKind)
  const createOpen = ownOpen || adding
  const setCreateOpen = (open: boolean) => {
    setOwnOpen(open)
    onAddingChange?.(open)
  }
  const [binding, setBinding] = useState<ProviderRecordDTO | null>(null)
  const [rotating, setRotating] = useState<ProviderRecordDTO | null>(null)
  const [revoking, setRevoking] = useState<ProviderRecordDTO | null>(null)
  const [testing, setTesting] = useState<string | null>(null)
  const [editingDefault, setEditingDefault] =
    useState<ProviderRecordDTO | null>(null)
  const [defaultModel, setDefaultModel] = useState('')
  // The refusal of the default-model or revoke dialog: the open dialog hides the
  // toast from screen readers, so the dialog says it (one is open at a time).
  const [dialogError, setDialogError] = useState<string>()
  const [preparing, setPreparing] = useState<SignInTool | null>(null)
  // The engine's latest line about the download a preparation is waiting for (#1086).
  const [download, setDownload] = useState<string | null>(null)
  const canPrepare = can('system:admin') && can('sessions:profile:write')
  const prepare = usePrivilegedMutation<SignInTool, void>({
    mutationFn: async (driver, authority) => {
      const status = await signInApi.status(
        driver,
        activeTenant,
        undefined,
        undefined,
        authority,
      )
      authority.dispatchGuard()
      if (!status.installed) {
        let job
        try {
          job = await installLatest(driver, authority, (polled) =>
            setDownload(
              polled.state === 'running' ? downloadLine(polled.progress) : null,
            ),
          )
        } finally {
          // A failed or finished install leaves no download for the next run (Retry).
          setDownload(null)
        }
        authority.dispatchGuard()
        if (job.state !== 'succeeded')
          throw new Error(
            job.error || t('prepare.incomplete', { state: job.state }),
          )
      }
      // The engine's existing rule owns the choice and creates/reuses private homes.
      await agentOpsApi.resolveProfile(driver, authority)
    },
    invalidateKeys: () => [
      firstHourKeys.all(activeTenant),
      agentToolsKeys.all,
      agentOpsKeys.profiles(activeTenant, boundary.epoch),
    ],
    successMessage: t('prepare.ready'),
    onDone: () => {
      if (returnTo)
        void navigate({ to: returnTo.split(/[?#]/)[0] as '/', href: returnTo })
    },
  })

  const listQ = useQuery({
    queryKey: providerKeys.list(activeTenant, boundary.epoch),
    queryFn: ({ signal }) => providersApi.list(undefined, { signal }),
    enabled: canRead,
  })

  const test = usePrivilegedMutation<string, ProviderRecordDTO>({
    mutationFn: (ref) => providersApi.test(ref),
    // The test's verdict decides whether a tool can start on the key (first hour).
    invalidateKeys: () => [
      providerKeys.list(activeTenant, boundary.epoch),
      firstHourKeys.all(activeTenant),
    ],
    // ⛔ THE DIALOGS ARE NOT WRAPPED IN RequireAssurance, and that is deliberate.
    // That wrapper renders the ceremony IN PLACE of its children, so wrapping a
    // dialog would paint a step-up panel on the page whenever the session is below
    // AAL3 — with the dialog closed and nothing asked for. It is the right tool for
    // an always-visible inline form (the onboarding wizard uses it that way); for a
    // dialog, usePrivilegedMutation raises the demand AT DISPATCH, which is the
    // pattern the profile plane beside this one already uses.
    stepUpAction: 'providers',
    // The toast reports the VERDICT, not "the request worked". A probe that reached
    // the provider and was refused is a successful test with a negative answer, and
    // telling the operator "tested" without the answer is the report this screen exists
    // to remove. Only an accepted credential is good news: refused and unreachable
    // are warnings, never a green check beside "Rotate it".
    successMessage: (record) => t(probeHintKey(record)),
    successIntent: (record) =>
      record.probe_state === 'ok' ? 'success' : 'warning',
    onDone: (tested, ref) => {
      setTesting(null)
      if (ref !== created.current || tested.probe_state !== 'ok') return
      const driver = SESSION_TOOL[tested.kind]
      if (driver && canPrepare) {
        if (prepare.isPending || prepare.stepUpRequest) return
        setPreparing(driver)
        prepare.mutate(driver)
      } else if (returnTo) {
        void navigate({ to: returnTo.split(/[?#]/)[0] as '/', href: returnTo })
      }
    },
    onError: () => {
      setTesting(null)
      // Not handled here: an authorization or assurance failure is reported by the
      // hook itself, and suppressing it would hide the only thing worth saying.
      return false
    },
  })

  const revoke = usePrivilegedMutation<string, ProviderRecordDTO>({
    mutationFn: (ref) => providersApi.revoke(ref),
    invalidateKeys: () => [
      providerKeys.list(activeTenant, boundary.epoch),
      // What a tool can start on may change with the keys (the first-hour readiness).
      firstHourKeys.all(activeTenant),
    ],
    stepUpAction: 'providers',
    successMessage: (record) =>
      t(record.kind === 'ollama' ? 'revoke.localSuccess' : 'revoke.success'),
    onDone: () => setRevoking(null),
    onError: (err) => {
      setDialogError(failureText(err) ?? t('errors:generic'))
      return true
    },
  })

  const saveDefault = usePrivilegedMutation<
    { ref: string; model: string },
    ProviderRecordDTO
  >({
    mutationFn: ({ ref, model }) =>
      providersApi.patch(ref, { default_model: model }),
    invalidateKeys: () => [
      providerKeys.list(activeTenant, boundary.epoch),
      firstHourKeys.all(activeTenant),
    ],
    successMessage: t('defaultModel.success'),
    onDone: () => setEditingDefault(null),
    onError: (err) => {
      setDialogError(failureText(err) ?? t('errors:generic'))
      return true
    },
  })

  if (!canRead) {
    return (
      <ForbiddenState
        title={t('forbidden.title')}
        description={t('forbidden.description')}
      />
    )
  }

  const addReason = prepare.stepUpRequest
    ? t('common:privileged.stepUp.title')
    : prepare.isPending && preparing
      ? download
        ? t('prepare.installing', {
            tool: toolName(preparing),
            progress: download,
          })
        : t('prepare.pending', { tool: toolName(preparing) })
      : test.isPending
        ? t('actions.testing')
        : undefined
  const addingBlocked =
    test.isPending || prepare.isPending || !!prepare.stepUpRequest
  const rows = listQ.data?.items ?? []

  const columns: TableColumn<ProviderRecordDTO>[] = [
    {
      id: 'name',
      header: t('columns.name'),
      accessorFn: (r) => r.display_name,
      cell: ({ row }) => (
        <div className="flex flex-col">
          <span className="font-medium text-foreground">
            {row.original.display_name}
          </span>
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.provider_ref}
          </span>
        </div>
      ),
    },
    {
      id: 'kind',
      header: t('columns.kind'),
      accessorFn: (r) => r.kind,
      cell: ({ row }) => (
        <div className="flex flex-col">
          <span>{t(`kinds.${row.original.kind}`)}</span>
          {row.original.base_url ? (
            <span className="font-mono text-xs text-muted-foreground">
              {row.original.base_url}
            </span>
          ) : null}
        </div>
      ),
    },
    {
      id: 'key',
      header: t('columns.key'),
      accessorFn: (r) => r.key_hint ?? '',
      cell: ({ row }) => (
        <span className="font-mono text-xs">
          {row.original.key_hint ?? '—'}
        </span>
      ),
    },
    {
      id: 'state',
      header: t('columns.state'),
      accessorFn: (r) => r.state,
      cell: ({ row }) => (
        <Badge
          variant={row.original.state === 'active' ? 'success' : 'neutral'}
        >
          {t(`states.${row.original.state}`)}
        </Badge>
      ),
    },
    {
      id: 'connection',
      header: t('columns.connection'),
      accessorFn: (r) => r.probe_state ?? '',
      cell: ({ row }) => <ConnectionCell record={row.original} />,
    },
    {
      id: 'defaultModel',
      header: t('defaultModel.title'),
      accessorFn: (r) => r.default_model ?? '',
      cell: ({ row }) => row.original.default_model || t('defaultModel.native'),
    },
    {
      id: 'actions',
      // Named for a screen reader, not shown: an empty header is a column with no name.
      header: () => <span className="sr-only">{t('columns.actions')}</span>,
      cell: ({ row }) => actionsFor(row.original, true),
    },
  ]

  /** A row's actions: Test connection always in view; the rest as buttons on a phone card,
   * and in the row's own menu on the table, where five buttons ran past the right edge at
   * 1280 px. */
  function actionsFor(record: ProviderRecordDTO, inMenu = false) {
    if (record.state !== 'active') return null
    const more = [
      canWrite && {
        key: 'default',
        label: t('defaultModel.title'),
        run: () => {
          setDefaultModel(record.default_model ?? '')
          setDialogError(undefined)
          setEditingDefault(record)
        },
      },
      can('sessions:profile:read') &&
        can('sessions:profile:write') && {
          key: 'bind',
          label: t('bind.title'),
          run: () => setBinding(record),
        },
      canWrite &&
        record.kind !== 'ollama' && {
          key: 'rotate',
          label: t('actions.rotate'),
          icon: <RefreshCw className="size-3.5" />,
          run: () => setRotating(record),
        },
      canAdmin && {
        key: 'revoke',
        label: t('actions.revoke'),
        icon: <Trash2 className="size-3.5" />,
        run: () => {
          setDialogError(undefined)
          setRevoking(record)
        },
      },
    ].filter(Boolean) as {
      key: string
      label: string
      icon?: ReactNode
      run: () => void
    }[]
    const busy = test.isPending && testing === record.provider_ref
    return (
      <div
        className={cn(
          'flex items-center gap-1 sm:justify-end',
          !inMenu && 'flex-wrap',
        )}
      >
        {canWrite && (
          <Button
            variant="ghost"
            size="sm"
            disabled={busy}
            onClick={() => {
              setTesting(record.provider_ref)
              test.mutate(record.provider_ref)
            }}
          >
            {busy ? (
              <Spinner className="size-3.5" />
            ) : (
              <PlugZap className="size-3.5" />
            )}
            {busy ? t('actions.testing') : t('actions.test')}
          </Button>
        )}
        {inMenu && more.length > 0 ? (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={t('rowMenu', {
                  name: record.display_name || record.kind,
                })}
              >
                <MoreHorizontal />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              {more.map((a) => (
                <DropdownMenuItem key={a.key} onSelect={a.run}>
                  {a.icon}
                  {a.label}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        ) : (
          more.map((a) => (
            <Button key={a.key} variant="ghost" size="sm" onClick={a.run}>
              {a.icon}
              {a.label}
            </Button>
          ))
        )}
      </div>
    )
  }

  return (
    <section
      aria-labelledby={headingId}
      className="flex min-w-0 flex-col gap-3"
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="flex min-w-0 flex-col gap-0.5">
          <h3 id={headingId} className="text-body-l font-semibold text-text">
            {t('section.title')}
          </h3>
          <p className="text-caption text-text-2">{t('subtitle')}</p>
        </div>
        {/* Secondary: the one filled control of Tools is the page's own verb. */}
        {canWrite && !(listQ.isSuccess && rows.length === 0) ? (
          <Button
            variant="secondary"
            onClick={() => setCreateOpen(true)}
            disabled={addingBlocked}
            aria-describedby={addingBlocked ? addReasonId : undefined}
          >
            <Plus className="size-4" />
            {t('section.add')}
          </Button>
        ) : null}
      </div>

      {addReason && (
        <p
          id={addReasonId}
          role="status"
          className="flex items-center gap-2 text-body"
        >
          {(prepare.isPending && !download) || test.isPending ? (
            <Spinner className="size-4" aria-hidden />
          ) : null}
          {addReason}
        </p>
      )}
      {preparing && !prepare.isPending && (
        <div className="flex flex-wrap items-center gap-3 rounded-lg border border-border p-3">
          {prepare.isSuccess ? (
            <>
              <p role="status" className="text-body">
                {t('prepare.ready')}
              </p>
              <Button
                onClick={() => useNewSessionDialog.getState().setOpen(true)}
              >
                {t('prepare.start')}
              </Button>
            </>
          ) : prepare.isError ? (
            <>
              <p
                role="alert"
                className="min-w-0 break-words text-body text-danger"
              >
                {t('prepare.failed', {
                  error:
                    prepare.error instanceof Error
                      ? prepare.error.message
                      : t('prepare.retry'),
                })}
              </p>
              {canPrepare && (
                <Button
                  onClick={() => prepare.mutate(preparing)}
                  disabled={!!prepare.stepUpRequest}
                >
                  {t('prepare.retry')}
                </Button>
              )}
            </>
          ) : null}
        </div>
      )}

      {/* The list is ONE PAGE. A screen that shows a page says so — otherwise an
          operator reads "these are my providers" off a list that is missing some,
          and binds the wrong one because the right one was on page two. */}
      <ListTruncationBadge
        query={listQ}
        label={t('truncation.label', { n: rows.length })}
        hint={withInlineCommands(t('truncation.hint'))}
        filas={rows.length}
        className="px-0 pt-0"
      />

      {phone && !listQ.isLoading && !listQ.error && rows.length > 0 ? (
        <ul
          aria-label={t('section.title')}
          className="flex min-w-0 flex-col gap-3"
        >
          {rows.map((record) => (
            <li key={record.provider_ref}>
              <article
                aria-label={record.display_name}
                data-testid="provider-card"
                className="flex min-w-0 flex-col gap-3 rounded-lg border border-border p-3"
              >
                <div className="min-w-0">
                  <h2 className="break-words text-sm font-medium">
                    {record.display_name}
                  </h2>
                  <p className="break-all font-mono text-xs text-muted-foreground">
                    {record.provider_ref}
                  </p>
                </div>
                <dl className="grid min-w-0 grid-cols-2 gap-3 text-xs">
                  <div>
                    <dt className="mb-1 text-muted-foreground">
                      {t('columns.state')}
                    </dt>
                    <dd>
                      <Badge
                        variant={
                          record.state === 'active' ? 'success' : 'neutral'
                        }
                      >
                        {t(`states.${record.state}`)}
                      </Badge>
                    </dd>
                  </div>
                  <div>
                    <dt className="mb-1 text-muted-foreground">
                      {t('columns.connection')}
                    </dt>
                    <dd>
                      <ConnectionCell record={record} />
                    </dd>
                  </div>
                  <div>
                    <dt className="mb-1 text-muted-foreground">
                      {t('columns.kind')}
                    </dt>
                    <dd>{t(`kinds.${record.kind}`)}</dd>
                  </div>
                  <div>
                    <dt className="mb-1 text-muted-foreground">
                      {t('columns.key')}
                    </dt>
                    <dd className="font-mono">{record.key_hint ?? '—'}</dd>
                  </div>
                  <div className="col-span-2 min-w-0">
                    <dt className="mb-1 text-muted-foreground">
                      {t('defaultModel.title')}
                    </dt>
                    <dd className="break-all">
                      {record.default_model || t('defaultModel.native')}
                    </dd>
                  </div>
                  <div className="col-span-2 min-w-0">
                    <dt className="mb-1 text-muted-foreground">
                      {t('detail.endpoint')}
                    </dt>
                    <dd className="break-all">
                      {record.base_url || t('detail.officialEndpoint')}
                    </dd>
                  </div>
                </dl>
                {actionsFor(record)}
              </article>
            </li>
          ))}
        </ul>
      ) : (
        <DataTable
          columns={columns}
          data={rows}
          isLoading={listQ.isLoading}
          error={listQ.error}
          onRetry={() => void listQ.refetch()}
          label={t('section.title')}
          getRowId={(r) => r.provider_ref}
          empty={
            <EmptyState
              icon={<KeyRound />}
              title={t('empty.title')}
              description={t('empty.description')}
              action={
                canWrite ? (
                  <Button
                    variant="secondary"
                    onClick={() => setCreateOpen(true)}
                    disabled={addingBlocked}
                    aria-describedby={addingBlocked ? addReasonId : undefined}
                  >
                    <Plus className="size-4" />
                    {t('empty.action')}
                  </Button>
                ) : undefined
              }
            />
          }
        />
      )}

      {canWrite && (
        <ProviderCreateDialog
          open={createOpen}
          onOpenChange={setCreateOpen}
          initialKind={addKind}
          onCreated={(record) => {
            setPreparing(null)
            prepare.reset()
            // The screen does not stop at "registered". The next question an
            // operator has is whether it works, so the answer is offered as the
            // action rather than left as an exercise.
            setTesting(record.provider_ref)
            created.current = record.provider_ref
            test.mutate(record.provider_ref)
          }}
        />
      )}

      {binding && (
        <ProviderBindDialog
          record={binding}
          open
          onOpenChange={(open) => {
            if (!open) setBinding(null)
          }}
        />
      )}
      {canWrite && editingDefault && (
        <Dialog
          open
          onOpenChange={(o) => {
            if (!o && !saveDefault.isPending) setEditingDefault(null)
          }}
        >
          <DialogContent className="max-w-lg">
            <DialogHeader>
              <DialogTitle>{t('defaultModel.title')}</DialogTitle>
              <DialogDescription>{t('defaultModel.hint')}</DialogDescription>
            </DialogHeader>
            <Field
              label={t('defaultModel.model')}
              description={t('defaultModel.modelsHint')}
              error={dialogError}
            >
              <select
                value={defaultModel}
                onChange={(e) => setDefaultModel(e.target.value)}
                className="h-8 w-full rounded-ctl border border-ctl-border bg-canvas px-3 text-body text-text outline-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus"
              >
                <option value="">{t('defaultModel.native')}</option>
                {Array.from(
                  new Set([
                    ...(editingDefault.models ?? []),
                    ...(editingDefault.default_model
                      ? [editingDefault.default_model]
                      : []),
                  ]),
                ).map((model) => (
                  <option key={model} value={model}>
                    {model}
                  </option>
                ))}
              </select>
            </Field>
            <DialogFooter>
              <Button
                variant="secondary"
                onClick={() => setEditingDefault(null)}
                disabled={saveDefault.isPending}
              >
                {t('create.cancel')}
              </Button>
              <Button
                variant="primary"
                disabled={saveDefault.isPending}
                onClick={() => {
                  setDialogError(undefined)
                  saveDefault.mutate({
                    ref: editingDefault.provider_ref,
                    model: defaultModel,
                  })
                }}
              >
                {t('defaultModel.save')}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
      {canWrite && rotating && (
        <ProviderRotateDialog
          record={rotating}
          open
          onOpenChange={(o) => (o ? undefined : setRotating(null))}
        />
      )}

      {canAdmin && revoking && (
        <ConfirmDialog
          open
          onOpenChange={(o) => (o ? undefined : setRevoking(null))}
          title={t('revoke.title')}
          description={
            revoking.kind === 'ollama'
              ? t('revoke.localDescription')
              : `${t('revoke.description')} ${t('revoke.consequence')}`
          }
          confirmLabel={t('revoke.confirm')}
          tone="danger"
          pending={revoke.isPending}
          onConfirm={() => {
            setDialogError(undefined)
            revoke.mutate(revoking.provider_ref)
          }}
        >
          {dialogError ? <FieldError>{dialogError}</FieldError> : null}
        </ConfirmDialog>
      )}
    </section>
  )

  function ConnectionCell({ record }: { record: ProviderRecordDTO }) {
    const state: ProbeState = record.probe_state ?? ''
    const label =
      state === 'ok'
        ? t('probe.ok')
        : state === 'refused'
          ? t('probe.refused')
          : state === 'unreachable'
            ? t('probe.unreachable')
            : t('probe.never')
    const hint = t(probeHintKey(record))
    // `refused` is a WARNING and not an error: the call worked, the provider
    // answered, and the answer was no. `unreachable` is neutral for the same
    // reason in reverse — it is not a verdict about the credential at all.
    const variant =
      state === 'ok' ? 'success' : state === 'refused' ? 'warning' : 'outline'
    return (
      <div className="flex flex-col gap-0.5">
        <Badge variant={variant} title={hint}>
          {label}
        </Badge>
        {state === 'ok' && record.models?.length ? (
          <span className="text-xs text-muted-foreground">
            {t('probe.models', { n: record.models.length })}
            {record.probe_latency_ms
              ? ` · ${t('probe.latency', { ms: record.probe_latency_ms })}`
              : ''}
          </span>
        ) : null}
        {record.probed_at ? (
          <span className="text-xs text-muted-foreground">
            {t('probe.at', {
              when: formatDateTime(record.probed_at, i18n.language),
            })}
          </span>
        ) : null}
      </div>
    )
  }
}
