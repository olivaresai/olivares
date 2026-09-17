// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ApiError } from '@/lib/api/errors'
import type { StepUpAttempt } from '@/stores/step-up'
import { identityApi, isContractPending } from './api'
import { decodeCreationOptions, encodeAttestation } from './webauthn'

export class RegistrationOutcomeUnknown extends Error {}
export function canEnrollPasskey(): boolean {
  return (
    typeof window.PublicKeyCredential === 'function' &&
    typeof navigator.credentials?.create === 'function'
  )
}

/** Mechanism shared with additional-key registration. The server decides whether
 * this is a first credential or requires AAL3. Success never authenticates.
 * An unknown finish result must not resend the attestation or begin automatically.
 */
export async function enrollPasskey(
  name: string,
  attempt: StepUpAttempt,
): Promise<void> {
  attempt.dispatchGuard()
  const options = await identityApi.webauthnRegisterOptions(attempt)
  attempt.dispatchGuard()
  const credential = (await navigator.credentials.create({
    publicKey: decodeCreationOptions(options.publicKey),
    signal: attempt.signal,
  })) as PublicKeyCredential | null
  attempt.dispatchGuard()
  if (!credential)
    throw new DOMException('Registration dismissed', 'NotAllowedError')
  const attestation = encodeAttestation(credential)
  try {
    const result = await identityApi.webauthnRegister(
      attestation,
      name.trim(),
      attempt,
    )
    attempt.dispatchGuard()
    if (result?.ok !== true) throw new RegistrationOutcomeUnknown()
  } catch (err) {
    attempt.dispatchGuard()
    // A definite client/verification refusal is recoverable with a new challenge.
    // Server/network/empty finish responses cannot establish non-persistence.
    if (
      isContractPending(err) ||
      (err instanceof ApiError && err.status >= 400 && err.status < 500)
    )
      throw err
    throw new RegistrationOutcomeUnknown()
  }
}
