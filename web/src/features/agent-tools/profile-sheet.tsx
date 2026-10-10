// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The profile sheet (the arrow of a row): what a profile is and the actions on it. Its
// name can be changed (the home path never moves), and it can sign in with its own account
// or an API key. Paths and references stay under Details.
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { KvList, KvRow } from '@/components/ui/kv'
import { Segmented } from '@/components/ui/segmented'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { useProfileProviders } from '@/features/agentops/profile-authentication'
import type { ProviderAccountDTO } from '@/features/agentops/types'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { formatDateTime } from '@/lib/format'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { KeySelect } from './key-select'
import { isProfileName } from './profile-names'
import {
  StateChip,
  useProfileDetail,
  type ProfileRowModel,
} from './profile-row'
import './i18n'

type Method = 'account' | 'key'

/** Name, with its own Save: the engine renames (PATCH name). An engine that does not know
 * the field ignores it: the old name stays and the sheet says it needs a newer engine. A
 * refusal (400, 422) says its own reason, and the old name stays too. */
function RenameField({ account }: { account: ProviderAccountDTO }) {
  const { t } = useTranslation('agentTools')
  const { activeTenant } = useAuth()
  const boundary = useAuthBoundary()
  const [name, setName] = useState(account.name)
  const [problem, setProblem] = useState<string | null>(null)
  const shapeProblem =
    name !== account.name && !isProfileName(name)
      ? t('add.nameInvalid')
      : undefined
  const rename = usePrivilegedMutation<string, ProviderAccountDTO>({
    mutationFn: async (next, authority) => {
      const answer = await agentOpsApi.patchAccountMetadata(
        account.account_ref,
        { name: next },
        { tenant: activeTenant, dispatchGuard: authority.dispatchGuard },
      )
      // An engine that does not know the field may answer 200 and change nothing.
      if (answer.name !== next) throw new RenameUnsupported()
      return answer
    },
    successMessage: (done) => t('profiles.renamed', { name: done.name }),
    invalidateKeys: [
      agentOpsKeys.accounts(activeTenant, boundary.epoch),
      agentOpsKeys.profiles(activeTenant, boundary.epoch),
    ],
    onDone: () => setProblem(null),
    onError: (err) => {
      setName(account.name)
      if (err instanceof ApiError && err.status === 409) {
        setProblem(t('add.nameTaken'))
        return true
      }
      // The engine ignored the field: it does not know rename yet.
      if (err instanceof RenameUnsupported) {
        setProblem(t('profiles.renameNeedsNewerEngine'))
        return true
      }
      // A refusal carries its reason (a name it does not allow, for one): that is said.
      if (
        err instanceof ApiError &&
        (err.status === 400 || err.status === 422)
      ) {
        setProblem(err.message || t('add.failed'))
        return true
      }
      return false
    },
  })
  const changed = name !== account.name
  return (
    <form
      className="flex flex-col gap-2"
      onSubmit={(event) => {
        event.preventDefault()
        if (changed && !shapeProblem && !rename.isPending) {
          setProblem(null)
          rename.mutate(name)
        }
      }}
    >
      <Field
        label={t('add.name')}
        description={t('add.nameHint')}
        error={shapeProblem ?? problem ?? undefined}
      >
        <Input
          value={name}
          onChange={(event) => {
            setName(event.target.value)
            setProblem(null)
          }}
          maxLength={32}
          autoComplete="off"
          spellCheck={false}
          aria-invalid={!!shapeProblem}
        />
      </Field>
      {changed ? (
        <Button
          type="submit"
          className="self-start"
          disabled={!!shapeProblem || rename.isPending}
        >
          {t('profiles.saveName')}
        </Button>
      ) : null}
    </form>
  )
}

class RenameUnsupported extends Error {}

/** Account or API key, with the existing profile update (auth_source and the key). */
function SignsInWith({ account }: { account: ProviderAccountDTO }) {
  const { t } = useTranslation('agentTools')
  const { activeTenant } = useAuth()
  const boundary = useAuthBoundary()
  const current: Method =
    account.auth_source === 'managed_injection' ? 'key' : 'account'
  const [method, setMethod] = useState<Method>(current)
  const [keyRef, setKeyRef] = useState(account.provider_record_ref ?? '')
  const { records } = useProfileProviders(account.driver, method === 'key')
  const unchanged =
    method === current &&
    (method === 'account' || keyRef === (account.provider_record_ref ?? ''))
  const valid = method === 'account' || !!keyRef
  const save = usePrivilegedMutation<void, unknown>({
    mutationFn: (_vars, authority) =>
      agentOpsApi.patchProfile(
        account.account_ref,
        method === 'key'
          ? { auth_source: 'managed_injection', provider_record_ref: keyRef }
          : { auth_source: 'provider_account_home', provider_record_ref: '' },
        authority,
      ),
    successMessage: t('profiles.signsInSaved'),
    invalidateKeys: [
      agentOpsKeys.accounts(activeTenant, boundary.epoch),
      agentOpsKeys.profiles(activeTenant, boundary.epoch),
    ],
  })
  return (
    <div className="flex flex-col gap-3">
      <Field label={t('add.method')}>
        <Segmented
          aria-label={t('add.method')}
          value={method}
          onValueChange={setMethod}
          options={[
            { value: 'account', label: t('add.account') },
            { value: 'key', label: t('add.key') },
          ]}
        />
      </Field>
      {method === 'key' ? (
        <Field label={t('add.key')}>
          <KeySelect
            records={records}
            value={records.some((r) => r.provider_ref === keyRef) ? keyRef : ''}
            onChange={(event) => setKeyRef(event.target.value)}
          />
        </Field>
      ) : null}
      {!unchanged ? (
        <Button
          className="self-start"
          disabled={!valid || save.isPending}
          onClick={() => save.mutate()}
        >
          {t('profiles.save')}
        </Button>
      ) : null}
    </div>
  )
}

/** Remove (retire) the profile: irreversible, so it asks first. */
function RemoveProfile({
  account,
  onRemoved,
}: {
  account: ProviderAccountDTO
  onRemoved: () => void
}) {
  const { t } = useTranslation('agentTools')
  const { activeTenant } = useAuth()
  const boundary = useAuthBoundary()
  const [open, setOpen] = useState(false)
  const retire = usePrivilegedMutation<void, unknown>({
    mutationFn: () => agentOpsApi.retireProfile(account.account_ref),
    successMessage: t('profiles.removed', { name: account.name }),
    invalidateKeys: [
      agentOpsKeys.accounts(activeTenant, boundary.epoch),
      agentOpsKeys.profiles(activeTenant, boundary.epoch),
    ],
    onDone: () => {
      setOpen(false)
      onRemoved()
    },
  })
  return (
    <>
      <Button
        variant="destructive"
        className="self-start"
        onClick={() => setOpen(true)}
      >
        {t('profiles.remove')}
      </Button>
      <ConfirmDialog
        open={open}
        onOpenChange={setOpen}
        title={t('profiles.removeTitle', { name: account.name })}
        description={t('profiles.removeBody')}
        confirmLabel={t('profiles.remove')}
        tone="danger"
        pending={retire.isPending}
        onConfirm={() => retire.mutate()}
      />
    </>
  )
}

export function ProfileSheet({
  toolName,
  model,
  account,
  onOpenChange,
  onSignIn,
}: {
  toolName: string
  model: ProfileRowModel
  /** The account behind the row; absent for the tenant's default login. */
  account?: ProviderAccountDTO
  onOpenChange: (open: boolean) => void
  onSignIn: () => void
}) {
  const { t } = useTranslation('agentTools')
  const { can } = useAuth()
  const detail = useProfileDetail(model.state)
  const canSignIn =
    model.state.kind === 'account' && model.state.signedIn !== undefined
  const editable = !!account && account.state === 'active'
  return (
    <Sheet open onOpenChange={onOpenChange}>
      <SheetContent className="w-full max-w-md overflow-y-auto">
        <SheetHeader>
          <SheetTitle>{model.name}</SheetTitle>
          <SheetDescription>{toolName}</SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-5">
          {editable && can('sessions:account:write') ? (
            <RenameField account={account} />
          ) : null}
          {editable && can('sessions:profile:write') ? (
            <SignsInWith account={account} />
          ) : null}
          <div className="flex flex-col gap-1">
            <p className="text-body text-text">{detail}</p>
            {model.off ? (
              <StateChip tone="off">{t('profiles.off')}</StateChip>
            ) : model.ready ? (
              <StateChip tone="ready">{t('profiles.ready')}</StateChip>
            ) : null}
            {account?.last_login_at ? (
              <p className="text-caption text-text-2">
                {t('profiles.lastSignIn', {
                  when: formatDateTime(account.last_login_at),
                })}
              </p>
            ) : null}
          </div>
          {canSignIn ? (
            <Button className="self-start" onClick={onSignIn}>
              {t(
                model.state.kind === 'account' && model.state.signedIn
                  ? 'profiles.signInAgain'
                  : 'profiles.signIn',
              )}
            </Button>
          ) : null}
          {editable && can('sessions:profile:admin') ? (
            <RemoveProfile
              account={account}
              onRemoved={() => onOpenChange(false)}
            />
          ) : null}
          <details className="border-t border-line pt-3">
            <summary className="cursor-pointer text-body text-text-2">
              {t('profiles.details')}
            </summary>
            <KvList className="mt-2">
              {account ? (
                <>
                  <KvRow label={t('profiles.ref')} mono>
                    {account.account_ref}
                  </KvRow>
                  <KvRow label={t('profiles.environment')} mono>
                    {account.environment_ref}
                  </KvRow>
                  <KvRow label={t('profiles.home')}>
                    {t(
                      account.home_mode === 'adopted'
                        ? 'profiles.homeAdopted'
                        : 'profiles.homeManaged',
                    )}
                  </KvRow>
                </>
              ) : (
                <KvRow label={t('profiles.home')}>
                  {t('profiles.homeDefault')}
                </KvRow>
              )}
            </KvList>
          </details>
        </div>
      </SheetContent>
    </Sheet>
  )
}
