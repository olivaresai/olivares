// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// TOTP second factor — the account's own factor . Enrolment shows the QR
// and the base32 secret exactly once, activation proves possession with the
// app's code, and the recovery codes are revealed once with an explicit "I
// saved them" gate: after that nobody can read them again, by design. TOTP is
// an additional factor, never a replacement — passkeys/PIV remain the AAL3
// paths, and removing this factor demands one.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionCard } from '@/features/_intel'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { KvList, KvRow } from '@/components/ui/kv'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { identityKeys, totpApi, type TOTPEnrolmentDTO } from './api'

export function TOTPTab() {
  return (
    <div className="flex flex-col gap-6">
      <TOTPStatusSection />
      <TOTPPolicySection />
    </div>
  )
}

function TOTPStatusSection() {
  const { t } = useTranslation(['identity', 'common'])
  const qc = useQueryClient()
  const { activeTenant } = useAuth()
  const status = useQuery({
    queryKey: identityKeys.totp(activeTenant),
    queryFn: () => totpApi.status(),
  })

  const [enrolment, setEnrolment] = useState<TOTPEnrolmentDTO | null>(null)
  const [code, setCode] = useState('')
  const [recoveryCodes, setRecoveryCodes] = useState<string[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [confirmRemove, setConfirmRemove] = useState(false)

  const beginMutation = useMutation({
    mutationFn: () => totpApi.enrol({}),
    onSuccess: (dto) => {
      setError(null)
      setCode('')
      setEnrolment(dto)
    },
    onError: (err) => setError(errorMessage(err, t)),
  })

  const activateMutation = useMutation({
    mutationFn: () => totpApi.activate({ code }),
    onSuccess: (res) => {
      setEnrolment(null)
      setCode('')
      setRecoveryCodes(res.recovery_codes ?? [])
      void qc.invalidateQueries({ queryKey: identityKeys.totp(activeTenant) })
    },
    onError: (err) => setError(errorMessage(err, t)),
  })

  const removeMutation = useMutation({
    mutationFn: () => totpApi.remove(),
    onSuccess: () => {
      setConfirmRemove(false)
      setError(null)
      void qc.invalidateQueries({ queryKey: identityKeys.totp(activeTenant) })
    },
    onError: (err) => setError(errorMessage(err, t)),
  })

  const enrolled = status.data?.enrolled ?? false

  return (
    <SectionCard
      title={t('identity:totp.title')}
      description={t('identity:totp.description')}
    >
      {status.isLoading ? (
        <p className="text-body text-muted-foreground">
          {t('common:states.loading')}
        </p>
      ) : status.isError ? (
        <p className="text-body text-danger" role="alert">
          {t('identity:totp.loadFailed')}
        </p>
      ) : (
        <>
          <KvList>
            <KvRow label={t('identity:totp.state')}>
              <Badge variant={enrolled ? 'success' : 'neutral'}>
                {enrolled
                  ? t('identity:totp.enrolled')
                  : t('identity:totp.notEnrolled')}
              </Badge>
            </KvRow>
            {enrolled && status.data ? (
              <>
                <KvRow label={t('identity:totp.algorithm')}>
                  {status.data.algorithm} · {status.data.digits} ·{' '}
                  {t('identity:totp.everySeconds', {
                    period: status.data.period ?? 30,
                  })}
                </KvRow>
                <KvRow label={t('identity:totp.activatedAt')}>
                  {status.data.activated_at
                    ? new Date(status.data.activated_at).toLocaleString()
                    : '—'}
                </KvRow>
                <KvRow label={t('identity:totp.recoveryRemaining')}>
                  {t('identity:totp.recoveryCount', {
                    count: status.data.recovery_codes_remaining,
                  })}
                </KvRow>
              </>
            ) : null}
          </KvList>

          {enrolment ? (
            <div className="mt-4 flex flex-col gap-3">
              <p className="text-body text-muted-foreground">
                {t('identity:totp.scanPrompt')}
              </p>
              <div className="flex flex-col items-start gap-4 sm:flex-row">
                <img
                  src={`data:image/png;base64,${enrolment.qr_png_base64}`}
                  alt={t('identity:totp.qrAlt')}
                  width={192}
                  height={192}
                  className="rounded border border-border bg-white p-2"
                />
                <div className="flex flex-col gap-2 break-all">
                  <p className="text-caption text-muted-foreground">
                    {t('identity:totp.manualEntry')}
                  </p>
                  <code className="rounded bg-muted px-2 py-1 font-mono text-body">
                    {enrolment.secret}
                  </code>
                </div>
              </div>
              <form
                className="flex flex-col gap-2"
                onSubmit={(e) => {
                  e.preventDefault()
                  activateMutation.mutate()
                }}
              >
                <Field
                  label={t('identity:totp.codeLabel')}
                  htmlFor="totp-activate-code"
                >
                  <Input
                    id="totp-activate-code"
                    inputMode="numeric"
                    autoComplete="one-time-code"
                    value={code}
                    onChange={(e) => setCode(e.target.value)}
                    placeholder="000000"
                    className="w-32"
                  />
                </Field>
                <div className="flex gap-2">
                  <Button
                    type="submit"
                    variant="primary"
                    disabled={
                      activateMutation.isPending || code.trim().length < 6
                    }
                  >
                    {activateMutation.isPending
                      ? t('common:privileged.working')
                      : t('identity:totp.activate')}
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    onClick={() => setEnrolment(null)}
                  >
                    {t('common:actions.cancel')}
                  </Button>
                </div>
              </form>
            </div>
          ) : recoveryCodes ? (
            <div className="mt-4 flex flex-col gap-3">
              <p className="text-body text-danger" role="alert">
                {t('identity:totp.recoveryWarning')}
              </p>
              <ul className="grid grid-cols-1 gap-1 font-mono text-body sm:grid-cols-2">
                {recoveryCodes.map((c) => (
                  <li key={c} className="rounded bg-muted px-2 py-1">
                    {c}
                  </li>
                ))}
              </ul>
              <div>
                <Button
                  variant="primary"
                  onClick={() => setRecoveryCodes(null)}
                >
                  {t('identity:totp.savedCodes')}
                </Button>
              </div>
            </div>
          ) : (
            <div className="mt-3 flex flex-wrap gap-2">
              <Button
                variant="primary"
                onClick={() => beginMutation.mutate()}
                disabled={beginMutation.isPending}
              >
                {enrolled
                  ? t('identity:totp.replace')
                  : t('identity:totp.enrol')}
              </Button>
              {enrolled ? (
                confirmRemove ? (
                  <Button
                    variant="destructive"
                    onClick={() => removeMutation.mutate()}
                    disabled={removeMutation.isPending}
                  >
                    {t('identity:totp.confirmRemove')}
                  </Button>
                ) : (
                  <Button
                    variant="outline"
                    onClick={() => setConfirmRemove(true)}
                  >
                    {t('identity:totp.remove')}
                  </Button>
                )
              ) : null}
            </div>
          )}

          {error ? (
            <p className="mt-3 text-body text-danger" role="alert">
              {error}
            </p>
          ) : null}
        </>
      )}
    </SectionCard>
  )
}

function TOTPPolicySection() {
  const { t } = useTranslation(['identity', 'common'])
  const { can } = useAuth()
  const qc = useQueryClient()
  const { activeTenant } = useAuth()
  const allowed = can('system:admin')

  const policy = useQuery({
    queryKey: identityKeys.totpPolicy(activeTenant),
    queryFn: () => totpApi.policy(),
    enabled: allowed,
  })

  const [confirmOn, setConfirmOn] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const setMutation = useMutation({
    mutationFn: (on: boolean) => totpApi.setPolicy(on),
    onSuccess: () => {
      setConfirmOn(false)
      setError(null)
      void qc.invalidateQueries({
        queryKey: identityKeys.totpPolicy(activeTenant),
      })
    },
    onError: (err) => setError(errorMessage(err, t)),
  })

  if (!allowed) return null
  const on = policy.data?.require_for_admins ?? false

  return (
    <SectionCard
      title={t('identity:totp.policyTitle')}
      description={t('identity:totp.policyDescription')}
    >
      {policy.isLoading ? (
        <p className="text-body text-muted-foreground">
          {t('common:states.loading')}
        </p>
      ) : policy.isError ? (
        <p className="text-body text-danger" role="alert">
          {t('identity:totp.loadFailed')}
        </p>
      ) : (
        <>
          <KvList>
            <KvRow label={t('identity:totp.policyState')}>
              <Badge variant={on ? 'success' : 'neutral'}>
                {on
                  ? t('identity:totp.policyOn')
                  : t('identity:totp.policyOff')}
              </Badge>
            </KvRow>
          </KvList>
          <div className="mt-3 flex flex-wrap gap-2">
            {on ? (
              <Button
                variant="outline"
                onClick={() => setMutation.mutate(false)}
                disabled={setMutation.isPending}
              >
                {t('identity:totp.policyDisable')}
              </Button>
            ) : confirmOn ? (
              <Button
                variant="primary"
                onClick={() => setMutation.mutate(true)}
                disabled={setMutation.isPending}
              >
                {t('identity:totp.policyConfirmOn')}
              </Button>
            ) : (
              <Button
                variant="primary"
                onClick={() => setConfirmOn(true)}
                disabled={setMutation.isPending}
              >
                {t('identity:totp.policyEnable')}
              </Button>
            )}
          </div>
          <p className="mt-2 text-caption text-muted-foreground">
            {t('identity:totp.policyAal3Note')}
          </p>
          {error ? (
            <p className="mt-2 text-body text-danger" role="alert">
              {error}
            </p>
          ) : null}
        </>
      )}
    </SectionCard>
  )
}

/** The honest failure line: a 403 names the deployment's extra check for
 *  administrative actions, a 503 names the unwired sealer; anything else is the
 *  engine's own message, never invented. */
function errorMessage(err: unknown, t: (k: string) => string): string {
  if (err instanceof ApiError && err.status === 403)
    return t('identity:totp.stepUpRequired')
  if (err instanceof ApiError && err.status === 503)
    return t('identity:totp.sealerUnavailable')
  if (err instanceof Error && err.message) return err.message
  return t('common:status.error')
}
