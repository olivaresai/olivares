// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Add a profile to a tool: a name (the next free one is filled in), and how it signs in,
// Account or API key. An account then continues, in the same dialog, to the tool's own
// official sign-in. The engine decides the name and its answer wins.
import { useRef, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Segmented } from '@/components/ui/segmented'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { useProfileProviders } from '@/features/agentops/profile-authentication'
import type {
  CreateAccountRequest,
  ProviderAccountDTO,
} from '@/features/agentops/types'
import { ProviderCreateDialog } from '@/features/providers/provider-create-dialog'
import { PROVIDER_KINDS, recordServesDriver } from '@/features/providers/kinds'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { ADD_KEY, KeySelect } from './key-select'
import { PHONE_SHEET } from './phone-sheet'
import { isProfileName, nextFreeName, profileStem } from './profile-names'
import { ProfileSignInPanel } from './profile-sign-in'
import './i18n'

type Method = 'account' | 'key'

interface Created {
  account: ProviderAccountDTO
  /** The profile exists, but its key could not be set: said in the dialog. */
  bindError?: string
}

const errorText = (err: unknown) =>
  err instanceof Error && err.message ? err.message : undefined

export function AddProfileDialog({
  driver,
  toolName,
  taken,
  onOpenChange,
}: {
  driver: string
  toolName: string
  /** Every name the engine has reserved. */
  taken: ReadonlySet<string>
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation('agentTools')
  const { activeTenant, can } = useAuth()
  // A key is bound with a second call; without it the account would be left unkeyed.
  const canBind = can('sessions:profile:write')
  const boundary = useAuthBoundary()
  const suggested = nextFreeName(profileStem(driver), taken)
  const [name, setName] = useState(suggested)
  const [typed, setTyped] = useState(false)
  const [method, setMethod] = useState<Method>('account')
  const [keyRef, setKeyRef] = useState('')
  const [addingKey, setAddingKey] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [done, setDone] = useState<Created | null>(null)
  // One idempotency key per request body: a retry after a lost answer replays it.
  const keys = useRef(new Map<string, string>())
  const { records, allowed } = useProfileProviders(driver, method === 'key')
  const initialKind =
    PROVIDER_KINDS.find((k) => recordServesDriver(k, undefined, driver)) ??
    PROVIDER_KINDS[0]

  const nameProblem = !name
    ? undefined
    : !isProfileName(name)
      ? t('add.nameInvalid')
      : taken.has(name)
        ? t('add.nameTaken')
        : undefined
  const missing = !name
    ? t('add.needName')
    : nameProblem
      ? nameProblem
      : method === 'key' && !keyRef
        ? t('add.needKey')
        : undefined

  const request = (n: string | undefined): CreateAccountRequest => {
    const body = { driver, ...(n ? { name: n } : {}) }
    const id = JSON.stringify(body)
    if (!keys.current.has(id)) keys.current.set(id, crypto.randomUUID())
    return { ...body, idempotency_key: keys.current.get(id)! }
  }

  const create = usePrivilegedMutation<void, Created>({
    mutationFn: async (_vars, authority) => {
      const scope = {
        tenant: activeTenant,
        dispatchGuard: authority.dispatchGuard,
      }
      let account: ProviderAccountDTO
      try {
        account = await agentOpsApi.createAccount(request(name), scope)
      } catch (err) {
        // The engine is the authority on names: when it refuses one it filled in, it
        // picks the next free one itself.
        if (typed || !(err instanceof ApiError) || err.status !== 409) throw err
        account = await agentOpsApi.createAccount(request(undefined), scope)
      }
      if (method !== 'key') return { account }
      try {
        await agentOpsApi.patchProfile(
          account.account_ref,
          { auth_source: 'managed_injection', provider_record_ref: keyRef },
          authority,
        )
        return { account }
      } catch (err) {
        return { account, bindError: errorText(err) ?? t('add.bindFailed') }
      }
    },
    successMessage: (made) => t('add.created', { name: made.account.name }),
    invalidateKeys: [
      agentOpsKeys.accounts(activeTenant, boundary.epoch),
      agentOpsKeys.profiles(activeTenant, boundary.epoch),
    ],
    onDone: (made) => {
      if (made.bindError) {
        setError(t('add.keyNotSet', { error: made.bindError }))
        setDone(made)
      } else if (method === 'account') setDone(made)
      else onOpenChange(false)
    },
    onError: (err) => {
      setError(errorText(err) ?? t('add.failed'))
      return true
    },
  })
  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (missing || create.isPending) return
    setError(null)
    create.mutate()
  }

  if (done && !done.bindError)
    return (
      <Dialog open onOpenChange={onOpenChange}>
        <DialogContent className={PHONE_SHEET}>
          <DialogHeader>
            <DialogTitle>
              {t('signIn.title', { name: done.account.name })}
            </DialogTitle>
            <DialogDescription>{t('signIn.description')}</DialogDescription>
          </DialogHeader>
          <ProfileSignInPanel
            driver={driver}
            name={done.account.name}
            account={done.account}
            tenant={activeTenant}
            autoStart
            onSignedIn={() => onOpenChange(false)}
          />
          <DialogFooter>
            <Button variant="ghost" onClick={() => onOpenChange(false)}>
              {t('add.notNow')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    )

  // The profile exists but its key was not set: the way on is the profile sheet. Creating
  // again would only make a second profile.
  if (done)
    return (
      <Dialog open onOpenChange={onOpenChange}>
        <DialogContent className={PHONE_SHEET}>
          <DialogHeader>
            <DialogTitle>{t('add.title')}</DialogTitle>
            <DialogDescription>
              {t('add.created', { name: done.account.name })}
            </DialogDescription>
          </DialogHeader>
          {error ? (
            <p role="alert" className="text-body text-danger">
              {error}
            </p>
          ) : null}
          <DialogFooter>
            <Button variant="primary" onClick={() => onOpenChange(false)}>
              {t('add.close')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    )

  return (
    <>
      <Dialog
        open
        onOpenChange={(open) =>
          create.isPending ? undefined : onOpenChange(open)
        }
      >
        <DialogContent className={PHONE_SHEET}>
          <DialogHeader>
            <DialogTitle>{t('add.title')}</DialogTitle>
            <DialogDescription>
              {t('add.description', { name: toolName })}
            </DialogDescription>
          </DialogHeader>
          <form onSubmit={submit} className="flex flex-col gap-4">
            <Field
              label={t('add.name')}
              description={t('add.nameHint')}
              error={nameProblem}
            >
              <Input
                value={name}
                onChange={(e) => {
                  setName(e.target.value)
                  setTyped(true)
                }}
                disabled={create.isPending}
                maxLength={32}
                autoComplete="off"
                spellCheck={false}
                aria-invalid={!!nameProblem}
              />
            </Field>
            <Field label={t('add.method')}>
              <Segmented
                aria-label={t('add.method')}
                value={method}
                onValueChange={setMethod}
                options={[
                  { value: 'account', label: t('add.account') },
                  canBind
                    ? { value: 'key', label: t('add.key') }
                    : {
                        value: 'key',
                        label: t('add.key'),
                        disabled: true,
                        reason: t('add.keyNeedsAccess'),
                      },
                ]}
              />
            </Field>
            {method === 'account' ? (
              <p className="text-body text-text-2">
                {t('add.accountHint', { name: toolName })}
              </p>
            ) : (
              <Field label={t('add.key')}>
                <KeySelect
                  records={records}
                  addKey={allowed}
                  value={keyRef}
                  disabled={create.isPending}
                  onChange={(e) => {
                    if (e.target.value === ADD_KEY) setAddingKey(true)
                    else setKeyRef(e.target.value)
                  }}
                />
              </Field>
            )}
            {error ? (
              <p role="alert" className="text-body text-danger">
                {error}
              </p>
            ) : null}
            <DialogFooter className="items-center">
              {missing && !nameProblem && !create.isPending ? (
                <p
                  role="status"
                  className="text-caption text-text-2 sm:mr-auto"
                >
                  {missing}
                </p>
              ) : null}
              <Button
                type="button"
                variant="ghost"
                disabled={create.isPending}
                onClick={() => onOpenChange(false)}
              >
                {t('cancel')}
              </Button>
              <Button
                type="submit"
                variant="primary"
                disabled={!!missing || create.isPending}
              >
                {create.isPending ? t('add.creating') : t('add.create')}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
      {addingKey ? (
        <ProviderCreateDialog
          open
          onOpenChange={setAddingKey}
          initialKind={initialKind}
          onCreated={(record) => setKeyRef(record.provider_ref)}
        />
      ) : null}
    </>
  )
}
