// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Settings > Sign-in: what administrative actions demand beyond the sign-in.
// Off by default. The engine refuses to raise it unless this session already
// meets the new level (a passkey ceremony at this address, or a TOTP sign-in),
// so nobody can lock themselves out. It refuses to lower or turn it off unless
// the session meets the current level; that step_up_required is handled like
// any other administrative action's.
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Segmented } from '@/components/ui/segmented'
import { ApiError } from '@/lib/api/errors'
import { http, queryKeys, type RequestOptions } from '@/lib/api'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import './i18n'

export type StepUpPolicy = 'none' | 'totp' | 'passkey'

export const stepUpPolicyApi = {
  get: (signal?: AbortSignal) =>
    http.get<{ admin_step_up: StepUpPolicy }>('/v1/auth/step-up-policy', {
      signal,
    }),
  set: (
    policy: StepUpPolicy,
    opts?: Pick<RequestOptions, 'signal' | 'dispatchGuard'>,
  ) =>
    http.put<{ admin_step_up: StepUpPolicy }>(
      '/v1/auth/step-up-policy',
      {
        admin_step_up: policy,
      },
      opts,
    ),
}

const policyKey = ['identity', 'step-up-policy'] as const

export function StepUpPolicySetting() {
  const { t } = useTranslation('identity')
  const { can } = useAuth()
  const allowed = can('system:admin')
  const [refusal, setRefusal] = useState<string | null>(null)
  const policy = useQuery({
    queryKey: policyKey,
    queryFn: ({ signal }) => stepUpPolicyApi.get(signal),
    enabled: allowed,
  })
  const save = usePrivilegedMutation({
    mutationFn: (next: StepUpPolicy, authority) =>
      stepUpPolicyApi.set(next, authority),
    invalidateKeys: [policyKey, queryKeys.whoami],
    successMessage: t('stepUpPolicy.saved'),
    stepUpAction: 'stepUpPolicy',
    onDone: () => setRefusal(null),
    onError: (err) => {
      if (
        err instanceof ApiError &&
        (err.code === 'passkey_not_enrolled' ||
          err.code === 'totp_sign_in_required')
      ) {
        setRefusal(t(`stepUpPolicy.refused.${err.code}`))
        return true
      }
      return false
    },
  })

  if (!allowed) return null
  if (policy.isError)
    return (
      <p className="text-body text-danger" role="alert">
        {t('stepUpPolicy.loadFailed')}
      </p>
    )
  if (!policy.data) return <p role="status">{t('common:states.loading')}</p>
  const current = policy.data.admin_step_up
  return (
    <section
      className="flex flex-col gap-3 py-2"
      aria-labelledby="step-up-policy-title"
    >
      <div>
        <h2
          id="step-up-policy-title"
          className="text-body font-semibold text-text"
        >
          {t('stepUpPolicy.title')}
        </h2>
        <p className="text-caption text-text-2">
          {t('stepUpPolicy.description')}
        </p>
      </div>
      <Segmented
        aria-label={t('stepUpPolicy.title')}
        value={current}
        onValueChange={(next) => {
          setRefusal(null)
          if (next !== current && !save.isPending) save.mutate(next)
        }}
        options={(['none', 'totp', 'passkey'] as const).map((value) => ({
          value,
          label: t(`stepUpPolicy.options.${value}`),
        }))}
      />
      {refusal ? (
        <p className="text-body text-danger" role="alert">
          {refusal}
        </p>
      ) : null}
      <p className="text-caption text-text-2">{t('stepUpPolicy.note')}</p>
    </section>
  )
}
