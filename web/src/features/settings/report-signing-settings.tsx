// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Settings > Report signing: the system administrator turns the signing of downloaded
// evidence bundles on and off, and reads the key that signs them (S's seam, GET/PUT
// /v1/m/reporting/signing). Reports links here from "Downloaded, not signed".
import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { KvList, KvRow } from '@/components/ui/kv'
import { Spinner } from '@/components/ui/spinner'
import {
  ModuleGate,
  QueryErrorState,
} from '@/components/layout/query-error-state'
import {
  reportingApi,
  reportingKeys,
  type ReportSigning,
} from '@/features/reporting/api'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'

/** Mounted for a system administrator only; the engine checks system:admin itself. A page
 * that titles the section already passes `heading={false}`. */
export function ReportSigningSettings({
  heading = true,
}: {
  heading?: boolean
}) {
  // Signing is the reporting module's: off here, one line and the enable action (EU18).
  return (
    <ModuleGate module="reporting">
      <ReportSigningControl heading={heading} />
    </ModuleGate>
  )
}

/** The engine refused the change (not a step-up): its own sentence is shown. */
class SigningRefused extends Error {}

/** The one-line state: on and signing, on but not able to sign (and why), or off. */
export function signingState(s: ReportSigning): 'signing' | 'notReady' | 'off' {
  if (!s.enabled) return 'off'
  return s.ready ? 'signing' : 'notReady'
}

function ReportSigningControl({ heading }: { heading: boolean }) {
  const { t } = useTranslation('settings')
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()
  const key = reportingKeys.signing(activeTenant)
  const query = useQuery({
    queryKey: key,
    queryFn: reportingApi.signing,
    retry: false,
  })
  // The engine's own sentence for a refusal that is not a step-up. On 09 Business a missing
  // license comes back 403 "Forbidden" with a license sentence; the shared toast would say
  // the role is missing, which is false.
  // The shared hook reports every 403 as a missing role before a feature can answer, so the
  // refusal is turned into this control's own error here (a step-up still goes to the hook).
  const [refused, setRefused] = useState('')
  const set = usePrivilegedMutation<boolean, ReportSigning>({
    mutationFn: async (enabled) => {
      setRefused('')
      try {
        return await reportingApi.setSigning(enabled)
      } catch (err) {
        if (
          err instanceof ApiError &&
          err.isForbidden &&
          !err.isStepUpRequired &&
          err.message
        )
          throw new SigningRefused(err.message)
        throw err
      }
    },
    successMessage: t('signing.saved'),
    stepUpAction: 'signing',
    onError: (err) => {
      if (!(err instanceof SigningRefused)) return false
      setRefused(err.message)
      return true
    },
    onDone: (status) => {
      queryClient.setQueryData(key, status)
      // Reports reads bundle_signing from its catalog: it says what the next download does.
      void queryClient.invalidateQueries({ queryKey: reportingKeys.reports() })
    },
  })

  const s = query.data
  const state = s ? signingState(s) : undefined
  return (
    <section
      className="mt-2 flex flex-col gap-3"
      aria-label={heading ? undefined : t('signing.title')}
      aria-labelledby={heading ? 'signing-title' : undefined}
      data-slot="report-signing"
    >
      {heading ? (
        <div>
          <h2 id="signing-title" className="text-body font-semibold text-text">
            {t('signing.title')}
          </h2>
          <p className="text-caption text-text-2">{t('signing.description')}</p>
        </div>
      ) : null}
      {query.isLoading ? (
        <div className="flex justify-center py-6">
          <Spinner />
        </div>
      ) : query.isError || !s || !state ? (
        <QueryErrorState
          error={query.error}
          subject={t('signing.subject')}
          retry={() => void query.refetch()}
        />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <Badge
              variant={
                state === 'signing'
                  ? 'success'
                  : state === 'notReady'
                    ? 'warning'
                    : 'neutral'
              }
            >
              {t(`signing.badge.${state}`)}
            </Badge>
            <span className="text-body text-text" data-slot="signing-state">
              {state === 'notReady'
                ? t('signing.state.notReady', {
                    reason: s.reason ?? t('signing.noReason'),
                  })
                : t(`signing.state.${state}`)}
            </span>
          </div>
          <KvList>
            <KvRow label={t('signing.source.label')}>
              {t(`signing.source.${s.source}`, {
                defaultValue: s.source,
              })}
            </KvRow>
            {s.key_id ? (
              <KvRow label={t('signing.keyId')} mono>
                {s.key_id}
              </KvRow>
            ) : null}
            {s.public_key ? (
              <KvRow label={t('signing.publicKey')} mono>
                {s.public_key}
              </KvRow>
            ) : null}
          </KvList>
          <div className="flex flex-wrap items-center gap-3">
            <Button
              variant={s.enabled ? 'secondary' : 'primary'}
              size="sm"
              disabled={set.isPending}
              onClick={() => set.mutate(!s.enabled)}
            >
              {set.isPending ? <Spinner size="sm" aria-hidden /> : null}
              {s.enabled ? t('signing.turnOff') : t('signing.turnOn')}
            </Button>
            {s.enabled ? (
              <span className="text-caption text-text-2">
                {t('signing.offKeepsKey')}
              </span>
            ) : null}
          </div>
          {refused ? (
            <p role="alert" className="text-body text-warning">
              {refused}
            </p>
          ) : null}
        </>
      )}
    </section>
  )
}
