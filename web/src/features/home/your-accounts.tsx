// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// YOUR ACCOUNTS: the provider accounts that are the person's own (a tool's login only
// they can use), each with its state and the limits its tool reported, never money.
// A person who has none, in any state, meets one notice with one next step, Create my
// account: the engine makes it (POST /provider-accounts/mine, answering the one they
// have when it exists, and refusing one retired). Signing it in to the vendor is a
// later engine step, so it is said as waiting, never offered as a control that fails.
import { useId } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { UserRound } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import {
  EditionRefusalNote,
  QueryErrorState,
} from '@/components/layout/query-error-state'
import { BoxedList, BoxedListRow } from '@/components/ui/boxed-list'
import { Button } from '@/components/ui/button'
import { editionRefusal } from '@/components/ui/edition-note'
import { Notice } from '@/components/ui/notice'
import { Skeleton } from '@/components/ui/skeleton'
import { useProfileList } from '@/features/agent-tools/use-profiles'
import { agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { sessionTools, toolName } from '@/features/agentops/engine-tools'
import type { ProviderAccountDTO } from '@/features/agentops/types'
import { useClientSettings } from '@/features/settings/preferences'
import { http } from '@/lib/api'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import './i18n'

/** An account as an engine with own accounts answers it: `owner_id` names the person
 * whose own login it is; absent for an account nobody owns, and on an older engine. */
type Account = ProviderAccountDTO & { owner_id?: string }

const MINE = '/v1/m/sessions/provider-accounts/mine'

/** The page's own sentence for the engine's answers a refused Create my account names by
 * its status; a 403 is read by what refused it, and any other failure is
 * accounts.createFailed. */
const CREATE_REFUSED: Record<number, string> = {
  // No such route on an older engine, or the sessions module switched off.
  404: 'accounts.createUnavailable',
  409: 'accounts.createExists',
}

function AccountRow({ account }: { account: Account }) {
  const { t } = useTranslation('home')
  const state =
    account.state === 'disabled'
      ? t('accounts.disabled')
      : account.state === 'retired'
        ? t('accounts.retired')
        : account.identity
          ? t('accounts.signedIn', { identity: account.identity })
          : t('accounts.signInWaits')
  // The limits the tool reported, as the share of each window used; a credit
  // balance is money, and a member is shown none.
  const limits = (account.usage ?? []).flatMap((u) =>
    u.utilization === undefined
      ? []
      : [
          t('accounts.limit', {
            label: u.label,
            percent: Math.round(u.utilization),
          }),
        ],
  )
  return (
    <BoxedListRow role="listitem" className="flex-wrap">
      <span className="font-medium text-text">{toolName(account.driver)}</span>
      <span className="min-w-0 flex-1 text-caption text-text-2">{state}</span>
      {limits.length > 0 ? (
        <span className="text-caption text-text-2">{limits.join(' · ')}</span>
      ) : null}
    </BoxedListRow>
  )
}

export function YourAccounts() {
  const { t } = useTranslation(['home', 'errors'])
  const headingId = useId()
  const { activeTenant, principal } = useAuth()
  const { epoch } = useAuthBoundary()
  const qc = useQueryClient()
  const defaultTool = useClientSettings((s) => s.sessionDefaults.tool)
  const list = useProfileList()
  const tool =
    sessionTools().find((d) => d === defaultTool) ?? sessionTools()[0]
  const report = useFailedActionReporter()
  const create = useMutation({
    mutationFn: () => http.post<Account>(MINE, { driver: tool }),
    onSuccess: () =>
      qc.invalidateQueries({
        queryKey: agentOpsKeys.accounts(activeTenant, epoch),
      }),
  })
  // A step-up is the shared ceremony, which asks again once it is done; every other
  // refusal is said under the button.
  const makeAccount = (): void =>
    create.mutate(undefined, {
      onError: (err) => {
        if (err instanceof ApiError && err.isStepUpRequired)
          report(err, makeAccount)
      },
    })
  // An engine without the accounts read leaves the page as it was.
  if (list.error instanceof ApiError && list.error.status === 404) return null
  // An account of one's own belongs to a person: a sign-in with no person behind it (a
  // token) has none and is offered none, as the engine refuses it one.
  if (!principal?.user_id) return null
  // Retired ones too: an own account in any state is theirs, and no other is made.
  const all: Account[] = [
    ...(list.data?.accounts ?? []),
    ...(list.data?.disabled ?? []),
    ...(list.data?.retired ?? []),
  ]
  const own = all.filter(
    (a) => !!a.owner_id && a.owner_id === principal?.user_id,
  )
  // "None of your own" is said only of a whole list.
  const none = !!list.data && !list.data.truncated && own.length === 0
  if (list.isSuccess && own.length === 0 && (!none || !tool)) return null
  // A 403 is not one thing: a step-up, an edition answer, a tenant that has not
  // admitted the person and a policy each say what they are, and only a refusal by
  // role reads as the role's.
  const failure = create.error instanceof ApiError ? create.error : undefined
  const edition = editionRefusal(failure)
  const admission =
    failure?.isForbidden && failure.code === 'tenant_admission_required'
  // The sessions module's own 403 (`module_error`) on this route: the tool is not
  // assigned to this person. The role's is the core's permission gate (`forbidden`).
  const notAssigned = failure?.isForbidden && failure.code === 'module_error'
  const policy = failure?.isForbidden
    ? (failure.detailString('policy_name') ?? failure.detailString('policy'))
    : undefined
  const refused = notAssigned
    ? 'accounts.createNotAssigned'
    : failure?.isForbidden
      ? 'accounts.createForbidden'
      : failure
        ? CREATE_REFUSED[failure.status]
        : ''

  return (
    <section aria-labelledby={headingId}>
      <h2
        id={headingId}
        className="mb-1.5 text-heading font-semibold text-text"
      >
        {t('accounts.title')}
      </h2>
      {list.isError ? (
        // A failed read is not a missing account, and the two never look alike. A
        // refusal is not a failure either: it says who can grant it, no Retry.
        <QueryErrorState
          error={list.error}
          title={t('accounts.unavailableTitle')}
          description={t('accounts.unavailableDescription')}
          retry={() => void list.refetch()}
        />
      ) : list.isPending ? (
        <div role="status">
          <span className="sr-only">{t('accounts.loading')}</span>
          <Skeleton className="h-10 w-full" />
        </div>
      ) : none ? (
        <Notice tone="warn" icon={<UserRound />}>
          <p>{t('accounts.none', { tool: toolName(tool!) })}</p>
          <Button
            type="button"
            variant="primary"
            size="sm"
            className="mt-2"
            disabled={create.isPending}
            onClick={makeAccount}
          >
            {t('accounts.create')}
          </Button>
          {!create.isError || failure?.isStepUpRequired ? null : edition ? (
            <EditionRefusalNote refusal={edition} className="mt-2" />
          ) : (
            <p role="alert" className="mt-2 text-danger">
              {admission ? (
                <>
                  <span className="block">
                    {t('errors:tenantAdmission.title')}
                  </span>
                  <span className="block">
                    {t('errors:tenantAdmission.description')}
                  </span>
                </>
              ) : policy ? (
                t('errors:forbidden.deniedByPolicy', { policy })
              ) : (
                t(refused || 'accounts.createFailed', {
                  tool: toolName(tool!),
                })
              )}
            </p>
          )}
        </Notice>
      ) : (
        <BoxedList role="list" aria-label={t('accounts.title')}>
          {own.map((a) => (
            <AccountRow key={a.account_ref} account={a} />
          ))}
        </BoxedList>
      )}
    </section>
  )
}
