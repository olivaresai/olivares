// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation } from '@tanstack/react-query'
import type { TFunction } from 'i18next'
import { CircleAlert, KeyRound } from 'lucide-react'
import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Notice } from '@/components/ui/notice'
import { http } from '@/lib/api/client'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { networkErrorText } from '@/lib/api/network-error-text'
import type { LoginResponse, ServerInfo } from '@/lib/api/types'
import type { WebAuthnChallenge } from '@/features/identity/types'
import {
  decodeRequestOptions,
  encodeAssertion,
  isWebAuthnSupported,
} from '@/features/identity/webauthn'

/** server-info as this page reads it: `passkey_sign_in` is absent when the
 * build has no passkey sign-in, and true when this address allows it. */
type PasskeyServerInfo = ServerInfo & { passkey_sign_in?: boolean }

/** «Use a passkey» exists only where the engine allows it at this address and
 * the browser can run the ceremony. Anywhere else the page draws nothing. */
export function offersPasskey(info: PasskeyServerInfo | undefined): boolean {
  return info?.passkey_sign_in === true && isWebAuthnSupported()
}

/** The page holds the password form while a mutation with this key runs. */
export const PASSKEY_SIGN_IN = ['passkey-sign-in']

const UNUSABLE = 'webauthn_relying_party_unusable'

/** The engine refuses a passkey at this address. */
const isUnusable = (error: unknown) =>
  error instanceof ApiError && error.code === UNUSABLE

/** Fresh options, the browser's passkey sheet, then the assertion: the
 * session, or null when the person closed the sheet. Nothing is kept. */
async function signInWithPasskey(
  signal: AbortSignal,
): Promise<LoginResponse | null> {
  const options = await http.post<WebAuthnChallenge>(
    '/v1/auth/webauthn/sign-in/options',
    undefined,
    { anonymous: true, signal },
  )
  let credential: PublicKeyCredential | null
  try {
    credential = (await navigator.credentials.get({
      publicKey: decodeRequestOptions(options.publicKey),
      signal,
    })) as PublicKeyCredential | null
  } catch (err) {
    const closed =
      err instanceof DOMException &&
      (err.name === 'NotAllowedError' || err.name === 'AbortError')
    if (closed) return null
    throw err
  }
  if (!credential) return null
  return http.post<LoginResponse>(
    '/v1/auth/webauthn/sign-in',
    { credential: encodeAssertion(credential) },
    {
      anonymous: true,
      signal,
      headers: { 'X-Olivares-Session': 'cookie' },
    },
  )
}

/** The page's own sentence for a refusal, never the engine's words. A refused
 * passkey says only that it did not sign in: the assertion names no account,
 * so the sentence names no email or password either. A browser that refuses
 * the ceremony (a relying party it will not use at this address, options it
 * cannot read) is a passkey that did not sign in too: the engine answered. */
function refusal(error: Error, t: TFunction): string {
  if (error instanceof ApiError) {
    if (error.code === UNUSABLE) return t('login.passkeyUnusable')
    if (error.isLockedOut) return t('login.lockedOut')
    if (error.code === 'sso_required') return t('login.passkeySsoRequired')
    return t('login.passkeyRefused')
  }
  return error instanceof NetworkError
    ? networkErrorText(t).line
    : t('login.passkeyRefused')
}

/** The quiet way in under the password form. A success hands the session to
 * the page's own completion, so it ends as a password does: the same session,
 * the same destination. */
export function PasskeySignIn({
  disabled,
  onSession,
  onUnusable,
}: {
  /** The password sign-in is running: one sign-in at a time. */
  disabled: boolean
  onSession: (session: LoginResponse) => Promise<void>
  /** The button goes at this address and takes the keyboard focus with it:
   * the page puts the focus back on its form. */
  onUnusable: () => void
}) {
  const { t } = useTranslation(['auth', 'errors'])
  const abort = useRef<AbortController | null>(null)
  useEffect(() => () => abort.current?.abort(), [])
  const run = useMutation({
    mutationKey: PASSKEY_SIGN_IN,
    mutationFn: () => {
      abort.current = new AbortController()
      return signInWithPasskey(abort.current.signal)
    },
    onSuccess: async (session) => {
      if (session) await onSession(session)
    },
    onError: (error) => {
      if (isUnusable(error)) onUnusable()
    },
  })
  const unusable = isUnusable(run.error)
  const held = disabled || run.isPending
  return (
    <>
      {run.error && (
        <Notice
          tone={unusable ? 'warn' : 'bad'}
          icon={<CircleAlert />}
          role="alert"
        >
          {refusal(run.error, t)}
        </Notice>
      )}
      {/* At an address where the engine refuses a passkey, the button goes
          until the page is opened again. */}
      {!unusable && (
        <Button
          type="button"
          variant="secondary"
          aria-disabled={held || undefined}
          aria-busy={run.isPending || undefined}
          onClick={() => {
            if (!held) run.mutate()
          }}
          className="h-auto min-h-10 w-full whitespace-normal"
        >
          <KeyRound aria-hidden />
          {run.isPending ? t('login.signingIn') : t('login.passkey')}
        </Button>
      )}
    </>
  )
}
