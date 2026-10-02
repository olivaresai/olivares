// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The login screen's second-factor leg . A password that verified answers
// with a pending challenge instead of a session; this panel walks it home:
// enter the app's code (or a recovery code), and when the policy demands the
// account's FIRST factor it runs the enrolment — QR, activation, and the
// one-time recovery codes — before handing the session over.
import { useMutation } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { ApiError } from '@/lib/api/errors'
import type { LoginChallenge, LoginResponse } from '@/lib/api/types'
import { totpApi, type TOTPEnrolmentDTO } from './api'

type Props = {
  challenge: LoginChallenge
  onDone: (session: LoginResponse) => void
  /** Back to the plain password form (a challenge that expired, or a restart). */
  onRestart: () => void
}

export function SecondFactorPanel({ challenge, onDone, onRestart }: Props) {
  const { t } = useTranslation(['auth', 'common'])
  const [useRecovery, setUseRecovery] = useState(false)
  const [code, setCode] = useState('')
  const [error, setError] = useState<string | null>(null)

  const [enrolment, setEnrolment] = useState<TOTPEnrolmentDTO | null>(null)
  const [recoveryCodes, setRecoveryCodes] = useState<string[] | null>(null)
  const [completed, setCompleted] = useState<LoginResponse | null>(null)
  const completionNotified = useRef(false)

  // Enrol once per mounted challenge, including effect replays. Activation
  // consumes the credential, so a state change must not start enrolment again.
  const enrolStarted = useRef(false)
  useEffect(() => {
    if (!challenge.enrolment_required || enrolStarted.current) return
    enrolStarted.current = true
    void totpApi
      .enrol({ mfa_token: challenge.mfa_token })
      .then(setEnrolment)
      .catch((err) => setError(failureMessage(err, t)))
  }, [challenge, t])

  const challengeMutation = useMutation({
    mutationFn: () =>
      useRecovery
        ? totpApi.challenge({
            mfa_token: challenge.mfa_token,
            recovery_code: code,
          })
        : totpApi.challenge({ mfa_token: challenge.mfa_token, code }),
    onSuccess: (res) => onDone(res),
    onError: (err) => setError(failureMessage(err, t)),
  })

  const activateMutation = useMutation({
    mutationFn: () =>
      totpApi.activate({ mfa_token: challenge.mfa_token, code }),
    onSuccess: (res) => {
      setCode('')
      setRecoveryCodes(res.recovery_codes ?? [])
      if (res.csrf_token) setCompleted(res)
    },
    onError: (err) => setError(failureMessage(err, t)),
  })

  // Recovery codes revealed and acknowledged: hand the (already minted)
  // session over.
  useEffect(() => {
    if (completed && recoveryCodes === null && !completionNotified.current) {
      completionNotified.current = true
      onDone(completed)
    }
  }, [completed, recoveryCodes, onDone])

  if (challenge.enrolment_required) {
    return (
      <div className="flex flex-col gap-4">
        <p className="text-body text-muted-foreground">
          {t('auth:secondFactor.enrolmentRequired')}
        </p>
        {error ? (
          <p className="text-body text-danger" role="alert">
            {error}
          </p>
        ) : null}
        {recoveryCodes ? (
          <div className="flex flex-col gap-3">
            <p className="text-body text-danger" role="alert">
              {t('auth:secondFactor.recoveryWarning')}
            </p>
            <ul className="grid grid-cols-1 gap-1 font-mono text-body sm:grid-cols-2">
              {recoveryCodes.map((c) => (
                <li key={c} className="rounded bg-muted px-2 py-1">
                  {c}
                </li>
              ))}
            </ul>
            <Button variant="primary" onClick={() => setRecoveryCodes(null)}>
              {t('auth:secondFactor.savedCodes')}
            </Button>
          </div>
        ) : completed || !enrolment ? (
          <p className="text-body text-muted-foreground">
            {t('common:states.loading')}
          </p>
        ) : (
          <div className="flex flex-col gap-3">
            <div className="flex flex-col items-start gap-4 sm:flex-row">
              <img
                src={`data:image/png;base64,${enrolment.qr_png_base64}`}
                alt={t('auth:secondFactor.qrAlt')}
                width={192}
                height={192}
                className="rounded border border-border bg-white p-2"
              />
              <div className="flex flex-col gap-2 break-all">
                <p className="text-caption text-muted-foreground">
                  {t('auth:secondFactor.manualEntry')}
                </p>
                <code className="rounded bg-muted px-2 py-1 font-mono text-body">
                  {enrolment.secret}
                </code>
              </div>
            </div>
            {!recoveryCodes && (
              <form
                className="flex flex-col gap-2"
                onSubmit={(e) => {
                  e.preventDefault()
                  activateMutation.mutate()
                }}
              >
                <Field
                  label={t('auth:secondFactor.codeLabel')}
                  htmlFor="totp-login-code"
                >
                  <Input
                    id="totp-login-code"
                    inputMode="numeric"
                    autoComplete="one-time-code"
                    value={code}
                    onChange={(e) => setCode(e.target.value)}
                    placeholder="000000"
                    className="w-32"
                    autoFocus
                  />
                </Field>
                <Button
                  type="submit"
                  variant="primary"
                  disabled={
                    activateMutation.isPending || code.trim().length < 6
                  }
                >
                  {activateMutation.isPending
                    ? t('common:privileged.working')
                    : t('auth:secondFactor.activate')}
                </Button>
              </form>
            )}
          </div>
        )}
        <button
          type="button"
          className="self-start py-1 text-caption text-muted-foreground underline-offset-2 hover:underline"
          onClick={onRestart}
        >
          {t('auth:secondFactor.backToLogin')}
        </button>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-4">
      <p className="text-body text-muted-foreground">
        {t('auth:secondFactor.codePrompt')}
      </p>
      {error ? (
        <p className="text-body text-danger" role="alert">
          {error}
        </p>
      ) : null}
      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault()
          challengeMutation.mutate()
        }}
      >
        <Field
          label={
            useRecovery
              ? t('auth:secondFactor.recoveryLabel')
              : t('auth:secondFactor.codeLabel')
          }
          htmlFor="totp-challenge-code"
        >
          <Input
            id="totp-challenge-code"
            inputMode="numeric"
            autoComplete="one-time-code"
            value={code}
            onChange={(e) => setCode(e.target.value)}
            placeholder={useRecovery ? 'XXXXXX-XXXXXX-…' : '000000'}
            className={useRecovery ? 'w-64' : 'w-32'}
            autoFocus
          />
        </Field>
        <Button
          type="submit"
          variant="primary"
          disabled={challengeMutation.isPending || code.trim().length < 6}
        >
          {challengeMutation.isPending
            ? t('common:privileged.working')
            : t('auth:secondFactor.submit')}
        </Button>
      </form>
      <div className="flex flex-col gap-2">
        <button
          type="button"
          className="self-start py-1 text-caption text-muted-foreground underline-offset-2 hover:underline"
          onClick={() => {
            setUseRecovery((v) => !v)
            setCode('')
            setError(null)
          }}
        >
          {useRecovery
            ? t('auth:secondFactor.useCode')
            : t('auth:secondFactor.useRecovery')}
        </button>
        <button
          type="button"
          className="self-start py-1 text-caption text-muted-foreground underline-offset-2 hover:underline"
          onClick={onRestart}
        >
          {t('auth:secondFactor.backToLogin')}
        </button>
      </div>
    </div>
  )
}

/** A wrong code and an expired challenge read the same on purpose; a 503 names
 *  the unwired sealer; anything else is the engine's own message. */
function failureMessage(err: unknown, t: (k: string) => string): string {
  if (err instanceof ApiError && err.status === 503)
    return t('auth:secondFactor.unavailable')
  if (err instanceof ApiError && err.status === 401)
    return t('auth:secondFactor.invalid')
  if (err instanceof ApiError && err.status === 429)
    return t('auth:login.lockedOut')
  if (err instanceof ApiError && err.message) return err.message
  return t('common:status.error')
}
