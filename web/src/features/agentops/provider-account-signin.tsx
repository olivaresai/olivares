// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { CodeLine } from '@/components/ui/code-line'
import { SignIn } from '@/features/first-hour/first-hour'
import { signInApi, type SignInTool } from '@/features/first-hour/api'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { useStepUpOwner } from '@/stores/step-up'
import { agentOpsKeys } from './api'
import { useAuthBoundary } from './auth-boundary'
import type { ProviderAccountDTO } from './types'

/** Native login is a system administrator action, just like the AI tools page.
 * Account bytes and pending gestures end with the provider room's auth boundary.
 * No list of tools lives here: the sign-in route is the engine's answer, and it refuses a
 * tool that has no official sign-in (400), which this renders as nothing. */
export function ProviderAccountSignIn({
  account,
}: {
  account: ProviderAccountDTO
}) {
  const { isSuperadmin, activeTenant } = useAuth()
  if (
    !isSuperadmin ||
    !activeTenant ||
    account.state !== 'active' ||
    account.auth_source !== 'provider_account_home' ||
    account.provider_record_ref
  )
    return null
  return <AccountSignIn account={account} tenant={activeTenant} />
}

/** What the tool itself says of this account's login, read in the account's own home. The
 * list rows and the sign-in panel read the same answer (one key). */
export function useAccountSignInStatus(
  account: ProviderAccountDTO,
  tenant: string,
  enabled = true,
) {
  const boundary = useAuthBoundary()
  // Refetching status must not retire the active login's owner.
  const captureStatusOwner = useStepUpOwner()
  const driver = account.driver as SignInTool
  return useQuery({
    queryKey: [
      ...agentOpsKeys.boundaryScope(tenant, boundary.epoch),
      'account-sign-in',
      account.account_ref,
    ],
    queryFn: async ({ signal }) => {
      const owner = captureStatusOwner()
      const options = owner.begin()
      const retire = () => owner.retire()
      signal.addEventListener('abort', retire, { once: true })
      if (signal.aborted) retire()
      try {
        return await signInApi.status(
          driver,
          tenant,
          options.signal,
          account.account_ref,
          options,
        )
      } finally {
        signal.removeEventListener('abort', retire)
        owner.retire()
      }
    },
    enabled,
    gcTime: 0,
    retry: false,
  })
}

export function AccountSignIn({
  account,
  tenant,
  autoStart = false,
  onSignedIn,
  quiet = false,
  explainRefusal = false,
}: {
  account: ProviderAccountDTO
  tenant: string
  /** Start the login at once (Add profile continues here). */
  autoStart?: boolean
  /** Runs once the tool says the account is signed in. */
  onSignedIn?: () => void
  /** No status sentence above the flow: the dialog around it already says what it is. */
  quiet?: boolean
  /** Say, in one sentence, that this tool has no official sign-in here (the route's 400)
   * instead of showing nothing: the person asked for one. */
  explainRefusal?: boolean
}) {
  const { t } = useTranslation('firstHour')
  const boundary = useAuthBoundary()
  const qc = useQueryClient()
  const captureOwner = useStepUpOwner()
  const requestOptions = useCallback(
    () => captureOwner().begin(),
    [captureOwner],
  )
  const driver = account.driver as SignInTool
  const queryKey = [
    ...agentOpsKeys.boundaryScope(tenant, boundary.epoch),
    'account-sign-in',
    account.account_ref,
  ]
  const status = useAccountSignInStatus(account, tenant)
  const refresh = useCallback(() => {
    void qc.invalidateQueries({
      queryKey: [
        ...agentOpsKeys.boundaryScope(tenant, boundary.epoch),
        'account-sign-in',
        account.account_ref,
      ],
    })
    onSignedIn?.()
  }, [qc, tenant, boundary.epoch, account.account_ref, onSignedIn])

  // The route refuses a tool with no sign-in; that is not a failed read.
  if (
    status.error instanceof ApiError &&
    status.error.status === 400 &&
    !status.data
  )
    return explainRefusal ? (
      <p role="status" className="text-body text-text-2">
        {t('status.noSignIn')}
      </p>
    ) : null

  return (
    <div className="flex flex-col gap-3">
      {quiet && status.data?.installed && !status.isError ? null : (
        <p role="status" className="text-body">
          {status.isPending
            ? t('common:states.loading')
            : status.isError
              ? t('errors.status')
              : !status.data.installed
                ? t('status.notInstalled')
                : status.data.signed_in
                  ? status.data.account
                    ? t('status.signedInAs', { account: status.data.account })
                    : t('status.signedIn')
                  : t('status.notSignedIn')}
        </p>
      )}
      {status.data && !status.data.installed && !status.isError ? (
        <CodeLine
          inline
          className="self-start"
          command={`olivares tool install ${driver}`}
        />
      ) : null}
      {status.isError ? (
        <Button
          variant="secondary"
          className="self-start"
          onClick={() => void status.refetch()}
        >
          {t('actions.retry')}
        </Button>
      ) : null}
      {status.data?.installed && !status.data.signed_in && !status.isError ? (
        <SignIn
          driver={driver}
          accountRef={account.account_ref}
          tenantId={tenant}
          onSignedIn={refresh}
          mutationScope={queryKey}
          requestOptions={requestOptions}
          autoStart={autoStart}
        />
      ) : null}
    </div>
  )
}
