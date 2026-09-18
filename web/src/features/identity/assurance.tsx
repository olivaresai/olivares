// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Fingerprint, IdCard, ShieldAlert } from 'lucide-react'
import { useState, type ReactNode, useId, useRef, useLayoutEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { SelfAuditNotice } from '@/features/_intel'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { queryKeys } from '@/lib/api'
import { authApi } from '@/lib/api/endpoints'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { useSessionStore } from '@/stores/session'
import { cn } from '@/lib/utils'
import {
  stepUpPrincipal,
  useStepUpOwner,
  useStepUpStore,
  type StepUpAttempt,
  type StepUpOwner,
} from '@/stores/step-up'
import {
  identityApi,
  isContractPending,
  isNoWebAuthnCredential,
  isRelyingPartyUnusable,
} from './api'
import { isPivKnownUnconfigured, pivStatusQueryKey } from './piv-configuration'
import { ContractPendingNotice } from './components'
import {
  addressCannotBeRelyingParty,
  PasskeyAddressNotice,
} from './passkey-address'
import {
  canEnrollPasskey,
  enrollPasskey,
  RegistrationOutcomeUnknown,
} from './enroll-passkey'
import {
  decodeRequestOptions,
  encodeAssertion,
  isWebAuthnSupported,
} from './webauthn'
// Foreign lazy chunks use this gate, so it owns registration of its namespace.
import './i18n'

export const AAL = { PASSWORD: 1, MFA: 2, HARDWARE: 3 } as const
export function useAssurance(): { aal: number; amr: string[] } {
  const { principal } = useAuth()
  return {
    aal: typeof principal?.aal === 'number' ? principal.aal : AAL.PASSWORD,
    amr: principal?.amr ?? [],
  }
}

interface GateProps {
  minAal: number
  action: string
  children: ReactNode
  allowEnrollment?: boolean
}
export function RequireAssurance(props: GateProps) {
  const { principal } = useAuth()
  const revision = useStepUpStore((s) => s.contextRevision)
  return (
    <AssuranceGate
      key={`${revision}:${stepUpPrincipal(principal)}`}
      {...props}
    />
  )
}
function AssuranceGate({
  minAal,
  action,
  children,
  allowEnrollment,
}: GateProps) {
  const { aal } = useAssurance()
  // Proof belongs to one challenge episode. A new low-AAL observation retires
  // the previous proof, even when identity and credential generation are unchanged.
  const sufficient = aal >= minAal
  const [wasSufficient, setWasSufficient] = useState(sufficient)
  const [episode, setEpisode] = useState({ verified: sufficient })
  if (sufficient !== wasSufficient) {
    setWasSufficient(sufficient)
    if (!sufficient) setEpisode({ verified: false })
  }
  if (sufficient && episode.verified) return <>{children}</>
  return (
    <StepUpPanel
      minAal={minAal}
      currentAal={aal}
      action={action}
      allowEnrollment={allowEnrollment}
      onElevated={() =>
        setEpisode((current) =>
          current === episode ? { verified: true } : current,
        )
      }
    />
  )
}

interface PanelProps {
  minAal: number
  currentAal: number
  action: string
  className?: string
  allowEnrollment?: boolean
  onElevated?: () => void
  onUnenrolled?: () => void
  isCurrentRequest?: () => boolean
}
export function StepUpPanel(props: PanelProps) {
  const { principal } = useAuth()
  const revision = useStepUpStore((s) => s.contextRevision)
  return (
    <CeremonyPanel
      key={`${revision}:${stepUpPrincipal(principal)}`}
      {...props}
    />
  )
}

type Status =
  | 'idle'
  | 'authenticating'
  | 'registering'
  | 'checking'
  | 'unenrolled'
  | 'registered'
  | 'registrationUnknown'
  | 'authenticationUnknown'
  | 'needsAuthentication'
  | 'pending'
  | 'unsupported'
  | 'failed'
  | 'relyingPartyUnusable'
  | 'sessionExpired'
  | 'verificationFailed'
  | 'cancelled'

function CeremonyPanel({
  minAal,
  currentAal,
  action,
  className,
  allowEnrollment = false,
  onElevated,
  onUnenrolled,
  isCurrentRequest,
}: PanelProps) {
  const { t } = useTranslation(['identity', 'common'])
  const { principal } = useAuth()
  const credentialGeneration = useSessionStore((s) => s.credentialGeneration)
  const queryClient = useQueryClient()
  const captureOwner = useStepUpOwner()
  const [status, setStatus] = useState<Status>('idle')
  const [enrollmentAvailable, setEnrollmentAvailable] = useState(false)
  const [name, setName] = useState('')
  const authenticateButton = useRef<HTMLButtonElement>(null)
  // THE NOTICE'S OWN POLICY, APPLIED TO THE ACTION IT EXPLAINS. The notice below
  // has always said "passkeys are not available at this address" while the
  // button above it still offered to start one: at http://127.0.0.1:18761 the
  // operator was handed a correct explanation and a contradictory action on one
  // card, and the click it invited is refused by the BROWSER — nothing on the
  // wire, nothing in any log, and a panel reporting a failure that reads like
  // their own doing. Asking the address module — rather than deciding here what
  // a usable address is — keeps ONE policy behind the sentence and the button,
  // and that policy is the host classification the engine's verifier mirrors.
  const addressUnusable = addressCannotBeRelyingParty()
  const addressNoticeId = useId()
  const focusOwner = useRef<StepUpOwner | null>(null)
  useLayoutEffect(() => {
    if (
      ['registered', 'registrationUnknown', 'needsAuthentication'].includes(
        status,
      ) &&
      focusOwner.current?.current() &&
      (!isCurrentRequest || isCurrentRequest())
    ) {
      authenticateButton.current?.focus()
    }
  }, [status, isCurrentRequest])
  const busy =
    status === 'authenticating' ||
    status === 'registering' ||
    status === 'checking'
  const terminal = status === 'sessionExpired'
  const knownUnconfigured = isPivKnownUnconfigured(principal)
  const piv = useQuery({
    queryKey: pivStatusQueryKey(null, principal, credentialGeneration),
    queryFn: ({ signal }) =>
      identityApi.pivStatus({ signal, sessionEffects: 'none' }),
    retry: false,
    enabled: !knownUnconfigured,
  })

  async function verify(owner: StepUpOwner, attempt: StepUpAttempt) {
    // Cancel earlier readers; never join a cached/prior in-flight query as evidence.
    await queryClient.cancelQueries({ queryKey: queryKeys.whoami, exact: true })
    attempt.dispatchGuard()
    const principal = await authApi.whoami(attempt)
    attempt.dispatchGuard()
    if (
      !owner.principal ||
      stepUpPrincipal(principal) !== owner.principal ||
      !Number.isFinite(principal?.aal) ||
      (principal.aal ?? 1) < minAal
    ) {
      setStatus('verificationFailed')
      return
    }
    // A reader might have started during the direct request. Cancel it before publication.
    await queryClient.cancelQueries({ queryKey: queryKeys.whoami, exact: true })
    attempt.dispatchGuard()
    queryClient.setQueryData(queryKeys.whoami, principal)
    attempt.dispatchGuard()
    setStatus('idle')
    onElevated?.()
  }

  function fail(
    err: unknown,
    attempt: StepUpAttempt,
    finalPost: boolean,
    registering: boolean,
  ) {
    if (!attempt.current()) return
    if (err instanceof ApiError && err.isUnauthenticated) {
      setStatus('sessionExpired')
    } else if (isRelyingPartyUnusable(err)) {
      // BEFORE the generic branches, and before the finish-leg "unknown" branch
      // that a 503 would otherwise fall into. This is a deployment state, not an
      // outcome of the ceremony: no attempt at this address can succeed, so
      // "did not complete" and "check the session" are both the wrong advice.
      setStatus('relyingPartyUnusable')
    } else if (isNoWebAuthnCredential(err)) {
      // This is permanent for the owned demand, including any later PIV success.
      onUnenrolled?.()
      setEnrollmentAvailable(true)
      setStatus('unenrolled')
    } else if (registering && err instanceof ApiError && err.isStepUpRequired) {
      // Another first registration may have won. This is not permission to enroll again.
      setEnrollmentAvailable(false)
      setStatus('needsAuthentication')
    } else if (err instanceof RegistrationOutcomeUnknown) {
      setEnrollmentAvailable(false)
      setStatus('registrationUnknown')
    } else if (isContractPending(err)) {
      setStatus('pending')
    } else if (finalPost && !(err instanceof ApiError && err.status < 500)) {
      setStatus('authenticationUnknown')
    } else if (
      err instanceof DOMException &&
      (err.name === 'NotAllowedError' || err.name === 'AbortError')
    )
      setStatus('cancelled')
    else setStatus('failed')
  }

  async function run(method: 'webauthn' | 'piv' | 'register' | 'check') {
    if (busy || terminal) return
    // The withdrawn action, guarded at the callback and not only at the control:
    // an activation that arrives anyway — programmatically, from a control an
    // assistive technology can still reach, or from a handler captured before
    // the address was read — must not open a ceremony the browser will refuse.
    // Re-read rather than close over the render's value, so this answers for the
    // address the console is on NOW. It withdraws nothing else: PIV, the session
    // check and every gate below are untouched, and the engine still verifies
    // the origin, the relying party and the step-up on whatever does reach it.
    if (
      (method === 'webauthn' || method === 'register') &&
      addressCannotBeRelyingParty()
    )
      return
    if (method === 'webauthn' && !isWebAuthnSupported()) {
      setStatus('unsupported')
      return
    }
    if (
      method === 'register' &&
      (!enrollmentAvailable ||
        !allowEnrollment ||
        !name.trim() ||
        !canEnrollPasskey())
    )
      return
    const owner = captureOwner()
    focusOwner.current = owner
    const attempt = owner.begin(isCurrentRequest)
    let finalPost = false
    let verifying = false
    setStatus(
      method === 'register'
        ? 'registering'
        : method === 'check'
          ? 'checking'
          : 'authenticating',
    )
    try {
      attempt.dispatchGuard()
      if (method === 'register') {
        await enrollPasskey(name, attempt)
        attempt.dispatchGuard()
        setEnrollmentAvailable(false)
        setStatus('registered')
        return
      }
      if (method === 'webauthn') {
        const options = await identityApi.webauthnAuthOptions(attempt)
        attempt.dispatchGuard()
        const credential = (await navigator.credentials.get({
          publicKey: decodeRequestOptions(options.publicKey),
          signal: attempt.signal,
        })) as PublicKeyCredential | null
        attempt.dispatchGuard()
        if (!credential)
          throw new DOMException('Authentication dismissed', 'NotAllowedError')
        const assertion = encodeAssertion(credential)
        finalPost = true
        const result = await identityApi.webauthnAuthenticate(
          assertion,
          attempt,
        )
        attempt.dispatchGuard()
        if (result?.ok !== true)
          throw new Error('Unknown authentication result')
      } else if (method === 'piv') {
        finalPost = true
        const result = await identityApi.pivElevate(attempt)
        attempt.dispatchGuard()
        if (result?.ok !== true)
          throw new Error('Unknown authentication result')
      }
      finalPost = false
      verifying = true
      await verify(owner, attempt)
      attempt.dispatchGuard()
    } catch (err) {
      if (
        verifying &&
        attempt.current() &&
        !(err instanceof ApiError && err.isUnauthenticated)
      )
        setStatus('verificationFailed')
      else fail(err, attempt, finalPost, method === 'register')
    } finally {
      attempt.retire()
    }
  }

  return (
    <Card className={cn('border-warning/40', className)} aria-busy={busy}>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <ShieldAlert className="size-4 text-warning" aria-hidden />
          {t('assurance.stepUpTitle')}
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <p className="text-body text-muted-foreground">
          {t('assurance.stepUpBody', {
            action: t(`assurance.actions.${action}`, {
              defaultValue: t('assurance.actions.generic'),
            }),
            required: aalLabel(minAal, t),
            current: aalLabel(currentAal, t),
          })}
        </p>
        <SelfAuditNotice />
        {/* Mounted unconditionally and renders nothing at an address where a
         *  passkey ceremony can run. It sits ABOVE the buttons because the
         *  refusals it names happen in the BROWSER — the request never leaves,
         *  so pressing the button produces a failure with no server-side
         *  explanation anywhere. It changes no gate: the AAL3 rule, the
         *  enrolment ownership and every button below are untouched. */}
        <PasskeyAddressNotice id={addressNoticeId} />
        <div>
          {/* aria-disabled, not `disabled`: the control keeps its place in the
           *  tab order so the explanation it points at is announced to whoever
           *  arrives at it, instead of the action disappearing silently from
           *  under a screen reader. `run` refuses the activation. */}
          <Button
            type="button"
            ref={authenticateButton}
            onClick={() => void run('webauthn')}
            disabled={busy || terminal}
            aria-disabled={addressUnusable || undefined}
            aria-describedby={addressUnusable ? addressNoticeId : undefined}
            className={cn(addressUnusable && 'opacity-50')}
          >
            <Fingerprint className="size-4" aria-hidden />
            {status === 'authenticating'
              ? t('assurance.authenticating')
              : t('assurance.authenticate')}
          </Button>
        </div>
        {!knownUnconfigured && piv.data?.presented === true && (
          <div>
            <Button
              type="button"
              variant="outline"
              onClick={() => void run('piv')}
              disabled={busy || terminal}
            >
              <IdCard className="size-4" aria-hidden />
              {t('assurance.authenticatePiv')}
            </Button>
          </div>
        )}
        {allowEnrollment && enrollmentAvailable && (
          <div className="flex flex-col gap-3 rounded-md border p-3">
            <p className="text-body text-muted-foreground">
              {t('assurance.enrollHint')}
            </p>
            {canEnrollPasskey() ? (
              <>
                <label className="flex flex-col gap-1.5 text-body font-medium">
                  {t('assurance.passkeyName')}
                  <Input
                    value={name}
                    onChange={(event) => setName(event.target.value)}
                    disabled={busy || terminal}
                  />
                </label>
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => void run('register')}
                  disabled={busy || terminal || !name.trim()}
                  aria-disabled={addressUnusable || undefined}
                  aria-describedby={
                    addressUnusable ? addressNoticeId : undefined
                  }
                  className={cn(addressUnusable && 'opacity-50')}
                >
                  {status === 'registering'
                    ? t('login.passkeys.registering')
                    : t('login.passkeys.register')}
                </Button>
              </>
            ) : (
              <p role="alert" className="text-body text-warning">
                {t('assurance.enrollUnsupported')}
              </p>
            )}
          </div>
        )}
        {status === 'pending' && (
          <ContractPendingNotice what={t('assurance.seamWhat')} />
        )}
        {status !== 'idle' && status !== 'pending' && !busy && (
          <p
            role={status === 'registered' ? 'status' : 'alert'}
            className="text-body text-muted-foreground"
          >
            {t(
              `assurance.${status === 'unenrolled' && allowEnrollment ? 'unenrolledInline' : status}`,
            )}
          </p>
        )}
        {(status === 'authenticationUnknown' ||
          status === 'verificationFailed') && (
          <Button
            type="button"
            variant="outline"
            onClick={() => void run('check')}
          >
            {t('assurance.checkSession')}
          </Button>
        )}
        {allowEnrollment && (
          <p className="text-caption text-muted-foreground">
            {t('assurance.cancelHint')}
          </p>
        )}
      </CardContent>
    </Card>
  )
}

export function aalLabel(
  level: number,
  t: (k: string, o?: Record<string, unknown>) => string,
): string {
  if (level >= AAL.HARDWARE) return t('assurance.aal.aal3')
  if (level >= AAL.MFA) return t('assurance.aal.aal2')
  return t('assurance.aal.aal1')
}
