// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { agentOpsApi, agentOpsKeys } from './api'
import { AuthorityLostError, useAuthBoundary } from './auth-boundary'
import type { ProviderAccountDTO, ProviderAccountMetadataPatch } from './types'
import { ProviderAccent } from './provider-accent'
import { useFreshRead } from './use-fresh-read'
import {
  providerAccentNames,
  isProviderAccent,
} from './provider-accent-palette'

/** Metadata uses the account's existing authority. The parent unmounts this editor
 * when write permission leaves; an authority change retires its mutation owner. */
export function ProviderAccountMetadata({
  account,
}: {
  account: ProviderAccountDTO
}) {
  const { t } = useTranslation(['agentops', 'common'])
  const { can } = useAuth()
  const boundary = useAuthBoundary()
  // Opening is a new read cycle, including after cancellation or permission
  // regrant. A possibly-applied write must not become an old cached baseline.
  const read = useFreshRead({
    read: (signal) => agentOpsApi.getAccount(account.account_ref, { signal }),
    allowed: can('sessions:account:read') && can('sessions:account:write'),
    boundary: boundary.key,
  })
  const current =
    read.current && read.state.status === 'ready' ? read.state.data : null
  const editing = current !== null && current.state !== 'retired'
  const editButton = useRef<HTMLButtonElement>(null)
  const wasEditing = useRef(false)
  useEffect(() => {
    if (!editing && wasEditing.current) editButton.current?.focus()
    wasEditing.current = editing
  }, [editing])
  if (editing) return <MetadataForm account={current} onClose={read.stop} />
  return (
    <div className="flex flex-col items-start gap-2">
      <Button
        ref={editButton}
        variant="outline"
        size="sm"
        disabled={read.state.status === 'loading'}
        aria-busy={read.state.status === 'loading'}
        onClick={read.start}
      >
        {t('accounts.metadata.edit')}
      </Button>
      {read.state.status === 'loading' && (
        <p role="status" className="text-caption text-muted-foreground">
          {t('accounts.details.loading')}
        </p>
      )}
      {read.state.status === 'error' && (
        <p role="alert" className="text-caption text-danger">
          {t('common:errors.generic')}
        </p>
      )}
      {read.state.status === 'forbidden' && (
        <p role="status" className="text-caption text-warning">
          {t('common:privileged.notAuthorized')}
        </p>
      )}
      {current?.state === 'retired' && (
        <p role="status" className="text-caption text-muted-foreground">
          {t('common:status.retired')}
        </p>
      )}
    </div>
  )
}

function MetadataForm({
  account,
  onClose,
}: {
  account: ProviderAccountDTO
  onClose: () => void
}) {
  const { t } = useTranslation(['agentops', 'common'])
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const [label, setLabel] = useState(account.display_name ?? '')
  const [accent, setAccent] = useState<string>(account.accent ?? '')
  const [error, setError] = useState<string | null>(null)
  // No status-only refusal proves that the server did not write. Keep only the
  // submitted fields uncertain until success or the next explicit read cycle.
  const [uncertain, setUncertain] = useState({
    display_name: false,
    accent: false,
  })
  const save = usePrivilegedMutation<
    ProviderAccountMetadataPatch,
    ProviderAccountDTO
  >({
    mutationKey: [
      ...agentOpsKeys.account(
        activeTenant,
        boundary.epoch,
        account.account_ref,
      ),
      'metadata',
    ],
    mutationFn: (body, authority) => {
      if (!can('sessions:account:write')) throw new AuthorityLostError()
      return agentOpsApi.patchAccountMetadata(account.account_ref, body, {
        tenant: activeTenant,
        dispatchGuard: authority.dispatchGuard,
      })
    },
    invalidateKeys: [
      agentOpsKeys.accounts(activeTenant, boundary.epoch),
      agentOpsKeys.account(activeTenant, boundary.epoch, account.account_ref),
      agentOpsKeys.profiles(activeTenant, boundary.epoch),
      agentOpsKeys.profile(activeTenant, boundary.epoch, account.account_ref),
    ],
    successMessage: t('accounts.metadata.saved'),
    onDone: onClose,
    onError: (err) => {
      if (err instanceof AuthorityLostError) return true
      setError(
        err instanceof ApiError && !err.isServerError
          ? err.message
          : t('accounts.metadata.uncertain'),
      )
      return true
    },
  })
  const body: ProviderAccountMetadataPatch = {}
  if (uncertain.display_name || label !== (account.display_name ?? ''))
    body.display_name = label
  if (
    (uncertain.accent || accent !== (account.accent ?? '')) &&
    (accent === '' || isProviderAccent(accent))
  )
    body.accent = accent
  const hasChanges = Object.keys(body).length > 0
  function submit(event: FormEvent) {
    event.preventDefault()
    if (save.isPending || !hasChanges) return
    setError(null)
    setUncertain((fields) => ({
      display_name: fields.display_name || body.display_name !== undefined,
      accent: fields.accent || body.accent !== undefined,
    }))
    save.mutate(body)
  }
  return (
    <form
      onSubmit={submit}
      className="flex min-w-0 flex-col gap-3 rounded-lg border border-border p-3"
    >
      <Field
        label={t('accounts.metadata.label')}
        description={t('accounts.metadata.hint')}
      >
        <Input
          autoFocus
          value={label}
          onChange={(event) => setLabel(event.target.value)}
          disabled={save.isPending}
        />
      </Field>
      <Field
        label={t('accounts.metadata.color')}
        description={t('accounts.metadata.colorHint')}
      >
        <select
          value={accent}
          onChange={(event) => setAccent(event.target.value)}
          disabled={save.isPending}
          className="h-8 w-full rounded-ctl border border-ctl-border bg-canvas px-2 text-body text-text focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus"
        >
          <option value="">{t('accounts.metadata.colors.default')}</option>
          {accent && !isProviderAccent(accent) && (
            <option value={accent} disabled>
              {t('accounts.metadata.colors.unknown')}
            </option>
          )}
          {providerAccentNames.map((name) => (
            <option key={name} value={name}>
              {t(`accounts.metadata.colors.${name}`)}
            </option>
          ))}
        </select>
      </Field>
      <span className="inline-flex items-center gap-2 text-caption text-muted-foreground">
        <ProviderAccent accent={accent} />
        {t('accounts.metadata.preview')}
      </span>
      {error && (
        <p role="alert" className="break-words text-caption text-danger">
          {error}
        </p>
      )}
      <div className="flex flex-wrap gap-2">
        <Button
          type="submit"
          size="sm"
          disabled={save.isPending || !hasChanges}
          aria-busy={save.isPending}
        >
          {t('common:actions.save')}
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          disabled={save.isPending}
          onClick={onClose}
        >
          {t('common:actions.cancel')}
        </Button>
      </div>
    </form>
  )
}
