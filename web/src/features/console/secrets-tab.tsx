// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryErrorState } from '@/components/layout/query-error-state'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Folder,
  KeyRound,
  Lock,
  Pencil,
  Plus,
  ShieldAlert,
  Trash2,
} from 'lucide-react'
import { useState, useEffect, useId, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
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
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { agentOpsApi } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { AAL, RequireAssurance } from '@/features/identity/assurance'
import { RefChip } from '@/features/sessions/ref-chip'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import { formatDateTime, formatRelativeTime } from '@/lib/format'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import {
  consoleApi,
  consoleKeys,
  type SecretDTO,
  type SecretInput,
} from './api'
import { mcpGatewayApi } from './mcp-gateway-api'
import {
  collectSecretUsage,
  groupByFolder,
  type SecretUse,
  type SecretUsage,
} from './secret-references'
import { StaticTable } from '@/components/data/static-table'
import './i18n'

// The session list pages by recency only (modules/sessions runtime_api.go
// handleListRuns), so "used by" names sessions among the most recent this many.
const RUNS_SCANNED = 500

// A secret name/handle: letters, digits and the separators `. _ - /`. The store
// rejects anything else; we mirror the rule so the create button explains itself.
const NAME_RE = /^[A-Za-z0-9._/-]{1,128}$/

/**
 * SecretsTab is the FASE X console panel over the SEALED RUNTIME SECRET
 * STORE — the single place an operator manages the named secrets that connector
 * configs reference as `store:<name>` and that resolve at Open WITHOUT a restart-
 * to-reconfigure of the file. The store NEVER returns a value: each secret surfaces
 * only a non-secret `hint` (a short fingerprint) so an admin can tell a secret is
 * set / changed without ever seeing it. By construction the value input is blank on
 * edit (blank = keep the stored value). The default is deployment-wide and
 * superadmin-only; explicit tenant mode is restricted to tenant admins and to ONE
 * namespace of that tenant: MCP credential handles (mcp/, the default) or the
 * secrets sessions receive as environment variables (env/, design FH 016). Every
 * write requires AAL3 and is audited.
 */
export function SecretsTab({
  scope,
  namespace = 'mcp/',
}: { scope?: 'tenant'; namespace?: TenantSecretNamespace } = {}) {
  const boundary = useAuthBoundary()
  return (
    <SecretsBody
      key={scope ? boundary.key : 'global'}
      scope={scope}
      namespace={namespace}
    />
  )
}

/** The tenant scope's namespaces: MCP credential handles and session secrets. */
export type TenantSecretNamespace = 'mcp/' | 'env/'

function SecretsBody({
  scope,
  namespace,
}: {
  scope?: 'tenant'
  namespace: TenantSecretNamespace
}) {
  const { t } = useTranslation(['console', 'common'])
  const { isSuperadmin, can } = useAuth()
  const boundary = useAuthBoundary()
  const qc = useQueryClient()
  const queryKey = useMemo(
    () =>
      scope
        ? [...consoleKeys.secrets(scope), boundary.tenant, boundary.epoch]
        : consoleKeys.secrets(),
    [scope, boundary.tenant, boundary.epoch],
  )
  useEffect(
    () => () => {
      if (scope) {
        void qc.cancelQueries({ queryKey })
        qc.removeQueries({ queryKey })
      }
    },
    [qc, scope, queryKey],
  )
  const admitted =
    scope === 'tenant'
      ? !!boundary.tenant && can?.('tenant:admin')
      : isSuperadmin
  const folderId = useId()
  const usedById = useId()
  const [editing, setEditing] = useState<SecretDTO | null>(null)
  const [createOpen, setCreateOpen] = useState(false)
  const [del, setDel] = useState<SecretDTO | null>(null)

  const query = useQuery({
    queryKey,
    queryFn: ({ signal }) =>
      scope
        ? consoleApi.listSecrets(scope, {
            signal,
            tenant: boundary.tenant ?? undefined,
          })
        : consoleApi.listSecrets(),
    enabled: admitted,
  })

  const usage = useSecretUsage({
    scope,
    namespace,
    queryKey,
    tenant: boundary.tenant,
    enabled: admitted,
  })

  const deleteMutation = usePrivilegedMutation<string, void>({
    mutationKey: queryKey,
    mutationFn: (name, authority) =>
      scope
        ? consoleApi.deleteSecret(name, scope, {
            ...authority,
            tenant: boundary.tenant ?? undefined,
          })
        : consoleApi.deleteSecret(name),
    invalidateKeys: () => [queryKey],
    successMessage: t('console:secrets.deleted'),
    onDone: () => setDel(null),
  })

  if (!admitted) {
    return (
      <div className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/5 px-4 py-3 text-body text-muted-foreground">
        <ShieldAlert
          className="mt-0.5 size-4 shrink-0 text-warning"
          aria-hidden
        />
        {t('console:secrets.superadminOnly')}
      </div>
    )
  }

  // One tenant list serves both namespaces; each panel shows its own.
  const secrets = (query.data?.secrets ?? []).filter(
    (s) => !scope || s.name.startsWith(namespace),
  )
  const caption = !scope
    ? t('console:secrets.caption')
    : namespace === 'env/'
      ? t('console:secrets.sessionCaption')
      : t('console:mcpGateway.credentialCaption')
  const sealerAvailable = query.data?.sealer_available ?? true

  return (
    <div className="flex flex-col gap-4 pt-4">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h2 className="text-heading text-foreground">
            {t('console:secrets.title')}
          </h2>
          <p className="max-w-2xl text-body text-muted-foreground">{caption}</p>
        </div>
        <Button onClick={() => setCreateOpen(true)}>
          <Plus />
          {t('console:secrets.create')}
        </Button>
      </div>

      {query.data && !sealerAvailable && (
        <p className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/5 px-4 py-3 text-body text-warning">
          <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
          {t('console:secrets.sealerUnavailable')}
        </p>
      )}

      {query.isLoading ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : query.isError ? (
        <QueryErrorState
          error={query.error}
          retry={() => void query.refetch()}
        />
      ) : secrets.length === 0 ? (
        <EmptyState
          title={t('console:secrets.none')}
          description={t(`console:secrets.noneHint.${usage.roster}`)}
          icon={<KeyRound />}
        />
      ) : (
        <div className="overflow-x-auto rounded-lg border border-border">
          <StaticTable>
            <thead>
              <tr>
                <th>{t('console:secrets.colName')}</th>
                <th>{t('console:secrets.colValue')}</th>
                <th>{t('console:secrets.colChanged')}</th>
                <th>{t('console:secrets.colReference')}</th>
                <th>{t('console:secrets.colUsedBy')}</th>
                <th />
              </tr>
            </thead>
            {groupByFolder(secrets).map(({ folder, items }, i) => (
              <tbody
                key={folder}
                aria-label={
                  folder ? undefined : t('console:secrets.rootFolder')
                }
                aria-labelledby={folder ? `${folderId}-${i}` : undefined}
              >
                {folder && (
                  <tr>
                    <th
                      id={`${folderId}-${i}`}
                      colSpan={6}
                      scope="rowgroup"
                      className="bg-muted/40 text-left"
                    >
                      <span className="inline-flex items-center gap-1.5 font-mono text-caption text-foreground">
                        <Folder className="size-3.5 shrink-0" aria-hidden />
                        {folder}/
                      </span>
                    </th>
                  </tr>
                )}
                {items.map((s) => (
                  <tr key={s.name} className="align-top">
                    <td className={folder ? 'pl-8' : undefined}>
                      <span className="font-mono text-caption text-foreground">
                        {(folder && s.name.slice(folder.length + 1)) || s.name}
                      </span>
                      {s.description && (
                        <p className="text-caption text-muted-foreground">
                          {s.description}
                        </p>
                      )}
                    </td>
                    <td>
                      {/* Never the value: the store does not return it. The hint is
                          a NON-secret fingerprint that changes when the value does. */}
                      <span className="inline-flex items-center gap-1.5">
                        <span
                          aria-hidden
                          className="font-mono text-muted-foreground"
                        >
                          ••••••••
                        </span>
                        <span className="sr-only">
                          {t('console:secrets.valueMasked')}
                        </span>
                        <Badge variant="neutral">
                          <KeyRound
                            className="size-3 shrink-0 text-accent-text"
                            aria-hidden
                          />
                          <span className="font-mono">{s.hint}</span>
                        </Badge>
                      </span>
                    </td>
                    <td
                      className="whitespace-nowrap text-muted-foreground"
                      title={
                        s.updated_at ? formatDateTime(s.updated_at) : undefined
                      }
                    >
                      {formatRelativeTime(s.updated_at)}
                    </td>
                    <td>
                      <RefChip value={`store:${s.name}`} absent="—" />
                    </td>
                    <td>
                      <UsedBy
                        state={usage}
                        uses={usage.data?.store.get(s.name)}
                      />
                    </td>
                    <td className="text-right">
                      <div className="flex justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setEditing(s)}
                        >
                          <Pencil />
                          {t('console:secrets.rotate')}
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setDel(s)}
                        >
                          <Trash2 />
                          {t('console:secrets.delete')}
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            ))}
          </StaticTable>
        </div>
      )}

      {!!usage.data?.external.length && (
        <section
          aria-labelledby="secrets-external"
          className="flex flex-col gap-2"
        >
          <h3 id="secrets-external" className="text-label text-foreground">
            {t('console:secrets.externalTitle')}
          </h3>
          <p className="max-w-2xl text-caption text-muted-foreground">
            {t('console:secrets.externalCaption')}
          </p>
          <div className="overflow-x-auto rounded-lg border border-border">
            <StaticTable>
              <thead>
                <tr>
                  <th>{t('console:secrets.colReference')}</th>
                  <th>{t('console:secrets.colUsedBy')}</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {usage.data.external.map((ref) => (
                  <tr key={ref.reference} className="align-top">
                    <td>
                      {/* The chip paints the locator; the scheme says which backend. */}
                      <span className="inline-flex items-center gap-1.5">
                        <span className="font-mono text-caption text-muted-foreground">
                          {ref.reference.slice(
                            0,
                            ref.reference.indexOf(':') + 1,
                          )}
                        </span>
                        <RefChip value={ref.reference} absent="—" />
                      </span>
                    </td>
                    <td>
                      <UsedBy state={usage} uses={ref.usedBy} />
                    </td>
                    <td className="text-right">
                      <Badge variant="neutral">
                        <Lock className="size-3 shrink-0" aria-hidden />
                        {t('console:secrets.readOnly')}
                      </Badge>
                    </td>
                  </tr>
                ))}
              </tbody>
            </StaticTable>
          </div>
        </section>
      )}

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
          {createOpen && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <SecretForm
                queryKey={queryKey}
                scope={scope}
                namespace={namespace}
                onClose={() => setCreateOpen(false)}
              />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      <Dialog
        open={editing !== null}
        onOpenChange={(o) => !o && setEditing(null)}
      >
        <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
          {editing && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <SecretForm
                queryKey={queryKey}
                scope={scope}
                namespace={namespace}
                existing={editing}
                onClose={() => setEditing(null)}
              />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      {/* The consequence depends on who holds the secret; the confirm names them
          from the same roster as the row's "Used by". */}
      <ConfirmDialog
        open={del !== null}
        onOpenChange={(o) => !o && setDel(null)}
        title={t('console:secrets.deleteTitle')}
        description={t(`console:secrets.deleteBody.${usage.roster}`)}
        confirmLabel={t('console:secrets.delete')}
        tone="danger"
        pending={deleteMutation.isPending}
        onConfirm={() => del && deleteMutation.mutate(del.name)}
      >
        {del && (
          <div
            role="group"
            aria-labelledby={usedById}
            className="flex flex-col gap-1"
          >
            <span id={usedById} className="text-label text-foreground">
              {t('console:secrets.colUsedBy')}
            </span>
            <UsedBy state={usage} uses={usage.data?.store.get(del.name)} />
          </div>
        )}
      </ConfirmDialog>
    </div>
  )
}

/** Which roster "used by" was read from: an empty answer names it, because a holder
 * outside it (a workspace connector, a template) is not seen. */
type UsageRoster = 'sources' | 'mcp' | 'sessions'

interface UsageState {
  roster: UsageRoster
  data?: SecretUsage
  isError: boolean
  /** The engine does not serve that roster (501). */
  unwired: boolean
  /** The session list was cut at RUNS_SCANNED: older sessions are not named. */
  partial: boolean
}

/** What references each secret, read from the rosters that hold the references:
 * the source roster (global), the MCP gateway (mcp/) or the session runs (env/). */
function useSecretUsage({
  scope,
  namespace,
  queryKey,
  tenant,
  enabled,
}: {
  scope?: 'tenant'
  namespace: TenantSecretNamespace
  queryKey: readonly unknown[]
  tenant: string | null
  enabled: boolean
}): UsageState {
  const roster: UsageRoster = !scope
    ? 'sources'
    : namespace === 'mcp/'
      ? 'mcp'
      : 'sessions'
  const query = useQuery({
    // Under the list's own key, so the tenant panel's unmount cleanup and every
    // write's invalidation reach it too. The tenant is named again — the base
    // already carries it in tenant mode — so the key states its own scope.
    queryKey: [...queryKey, 'used-by', roster, tenant],
    enabled,
    queryFn: async ({ signal }) => {
      if (roster === 'sources') {
        const { sources } = await consoleApi.listSources({ signal })
        return { usage: collectSecretUsage({ sources }), partial: false }
      }
      if (roster === 'mcp') {
        const { servers } = await mcpGatewayApi.get({
          signal,
          tenant: tenant ?? undefined,
        })
        return {
          usage: collectSecretUsage({ mcpServers: servers }),
          partial: false,
        }
      }
      const runs = await agentOpsApi.listRuns(
        { limit: RUNS_SCANNED },
        { signal, tenant },
      )
      return {
        usage: collectSecretUsage({ runs: runs.items }),
        partial: runs.has_more,
      }
    },
  })
  return {
    roster,
    data: query.data?.usage,
    isError: query.isError,
    unwired: query.error instanceof ApiError && query.error.status === 501,
    partial: !!query.data?.partial,
  }
}

// One cell names this many users; the rest are counted.
const USES_SHOWN = 5

/** The connections and sessions that reference a secret. Only an answer read from the
 * roster says "none", and it names the roster: a failed, paused or pending read never
 * reads as "not used". */
function UsedBy({ state, uses }: { state: UsageState; uses?: SecretUse[] }) {
  const { t } = useTranslation('console')
  if (state.isError)
    return (
      <span className="text-caption text-warning">
        {state.unwired
          ? t('secrets.usedByUnwired')
          : t('secrets.usedByUnavailable')}
      </span>
    )
  if (!state.data)
    return (
      <span className="text-caption text-muted-foreground">
        {t('secrets.usedByLoading')}
      </span>
    )
  if (!uses?.length)
    return (
      <span className="text-caption text-muted-foreground">
        {state.partial
          ? t('secrets.usedByNoneRecent', { limit: RUNS_SCANNED })
          : t(`secrets.usedByNone.${state.roster}`)}
      </span>
    )
  return (
    <ul className="flex flex-col gap-0.5">
      {uses.slice(0, USES_SHOWN).map((u) => (
        <li key={`${u.kind}:${u.id}`} className="text-caption">
          <span className="text-muted-foreground">
            {t(`secrets.useKind.${u.kind}`)}
          </span>{' '}
          <span className="text-foreground">{u.label}</span>
        </li>
      ))}
      {uses.length > USES_SHOWN && (
        <li className="text-caption text-muted-foreground">
          {t('secrets.usedByMore', { more: uses.length - USES_SHOWN })}
        </li>
      )}
    </ul>
  )
}

function SecretForm({
  queryKey,
  scope,
  namespace,
  existing,
  onClose,
}: {
  existing?: SecretDTO
  scope?: 'tenant'
  namespace: TenantSecretNamespace
  queryKey: readonly unknown[]
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const boundary = useAuthBoundary()
  const isEdit = !!existing
  const [name, setName] = useState(existing?.name ?? (scope ? namespace : ''))
  // The value input is ALWAYS blank on open — we never receive the stored value, and
  // on edit a blank value means "keep the stored secret" (description-only edit).
  const [value, setValue] = useState('')
  const [description, setDescription] = useState(existing?.description ?? '')

  const qc = useQueryClient()
  useEffect(
    () => () => {
      const cache = qc.getMutationCache()
      for (const m of cache.findAll({ mutationKey: queryKey })) cache.remove(m)
    },
    [qc, queryKey],
  )
  const mutation = usePrivilegedMutation<SecretInput, SecretDTO>({
    mutationKey: queryKey,
    mutationFn: (body, authority) =>
      scope
        ? consoleApi.putSecret(body, scope, {
            ...authority,
            tenant: boundary.tenant ?? undefined,
          })
        : consoleApi.putSecret(body),
    invalidateKeys: () => [queryKey],
    successMessage: isEdit
      ? t('console:secrets.rotated')
      : t('console:secrets.created'),
    onDone: onClose,
  })

  const nameValid =
    isEdit ||
    (NAME_RE.test(name.trim()) && (!scope || name.trim().startsWith(namespace)))
  // A new secret requires a value; an existing one may be edited with a blank value
  // (keeps the stored secret).
  const valid = nameValid && (isEdit || value !== '')

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {isEdit
            ? t('console:secrets.editTitle')
            : t('console:secrets.createTitle')}
        </DialogTitle>
        <DialogDescription>
          {!scope
            ? t('console:secrets.caption')
            : namespace === 'env/'
              ? t('console:secrets.sessionCaption')
              : t('console:mcpGateway.credentialCaption')}
        </DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <Field
          label={t('console:secrets.name')}
          htmlFor="secret-name"
          description={t('console:secrets.nameHint')}
          required
        >
          <Input
            id="secret-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            mono
            disabled={isEdit}
            placeholder="gdrive/token"
          />
        </Field>
        <Field
          label={t('console:secrets.value')}
          htmlFor="secret-value"
          description={
            isEdit
              ? t('console:secrets.valueSet', { hint: existing.hint })
              : t('console:secrets.valueHint')
          }
          required={!isEdit}
        >
          {/* type="password" + never prefilled: the value is write-only by design. */}
          <Input
            id="secret-value"
            type="password"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            autoComplete="new-password"
          />
        </Field>
        <Field label={t('console:secrets.description')} htmlFor="secret-desc">
          <Textarea
            id="secret-desc"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            rows={2}
          />
        </Field>
      </div>

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={mutation.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          onClick={() =>
            mutation.mutate({
              name: name.trim(),
              value,
              description: description.trim(),
            })
          }
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {isEdit ? t('console:secrets.rotate') : t('console:secrets.save')}
        </Button>
      </DialogFooter>
    </>
  )
}
