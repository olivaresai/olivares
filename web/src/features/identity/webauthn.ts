// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WebAuthn (FIDO2) ceremony helpers. Pure, testable codecs + option
// decoders. The browser runs the ceremony via navigator.credentials; the backend
// (first-party auth seam) issues the challenge and VERIFIES the assertion — the
// panel only orchestrates and NEVER asserts an AAL the backend did not grant
//. No NIST/FIPS conformance is claimed here.

/** Is the platform capable of a WebAuthn ceremony at all? (Fail-closed if not.) */
export function isWebAuthnSupported(): boolean {
  return (
    typeof window !== 'undefined' &&
    typeof window.PublicKeyCredential === 'function' &&
    typeof navigator !== 'undefined' &&
    !!navigator.credentials &&
    typeof navigator.credentials.get === 'function'
  )
}

/** base64url (no padding) → ArrayBuffer. */
export function base64urlToBuffer(value: string): ArrayBuffer {
  const pad = value.length % 4 === 0 ? '' : '='.repeat(4 - (value.length % 4))
  const base64 = value.replace(/-/g, '+').replace(/_/g, '/') + pad
  const binary = atob(base64)
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i)
  return bytes.buffer
}

/** ArrayBuffer → base64url (no padding). */
export function bufferToBase64url(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer)
  let binary = ''
  for (let i = 0; i < bytes.length; i += 1)
    binary += String.fromCharCode(bytes[i])
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

interface ServerDescriptor {
  id: string
  type?: string
  transports?: AuthenticatorTransport[]
}

/** Decode the backend's JSON request options (base64url fields) into the
 *  BufferSource shape navigator.credentials.get expects. */
export function decodeRequestOptions(
  publicKey: Record<string, unknown>,
): PublicKeyCredentialRequestOptions {
  const src = publicKey as {
    challenge: string
    timeout?: number
    rpId?: string
    userVerification?: UserVerificationRequirement
    allowCredentials?: ServerDescriptor[]
  }
  return {
    challenge: base64urlToBuffer(src.challenge),
    timeout: src.timeout,
    rpId: src.rpId,
    // Privileged login REQUIRES user verification (hardware authenticator → AAL3).
    userVerification: src.userVerification ?? 'required',
    allowCredentials: (src.allowCredentials ?? []).map((d) => ({
      id: base64urlToBuffer(d.id),
      type: 'public-key',
      transports: d.transports,
    })),
  }
}

/** Decode the backend's JSON creation options (registration) into the BufferSource
 *  shape navigator.credentials.create expects. */
export function decodeCreationOptions(
  publicKey: Record<string, unknown>,
): PublicKeyCredentialCreationOptions {
  // The server sends every binary field as a base64url STRING; type the source
  // loosely (not as the BufferSource-typed DOM options) and convert.
  const src = publicKey as {
    challenge: string
    user: { id: string; name: string; displayName: string }
    excludeCredentials?: ServerDescriptor[]
  }
  return {
    ...(publicKey as unknown as PublicKeyCredentialCreationOptions),
    challenge: base64urlToBuffer(src.challenge),
    user: {
      ...(src.user as unknown as PublicKeyCredentialUserEntity),
      id: base64urlToBuffer(src.user.id),
    },
    excludeCredentials: (src.excludeCredentials ?? []).map((d) => ({
      id: base64urlToBuffer(d.id),
      type: 'public-key' as const,
      transports: d.transports,
    })),
  }
}

/** Serialize the browser's attestation (registration) back to base64url JSON. */
export function encodeAttestation(credential: PublicKeyCredential): unknown {
  const response = credential.response as AuthenticatorAttestationResponse
  return {
    id: credential.id,
    rawId: bufferToBase64url(credential.rawId),
    type: credential.type,
    response: {
      clientDataJSON: bufferToBase64url(response.clientDataJSON),
      attestationObject: bufferToBase64url(response.attestationObject),
    },
  }
}

/** Serialize the browser's assertion back to base64url JSON for backend verify. */
export function encodeAssertion(credential: PublicKeyCredential): unknown {
  const response = credential.response as AuthenticatorAssertionResponse
  return {
    id: credential.id,
    rawId: bufferToBase64url(credential.rawId),
    type: credential.type,
    response: {
      clientDataJSON: bufferToBase64url(response.clientDataJSON),
      authenticatorData: bufferToBase64url(response.authenticatorData),
      signature: bufferToBase64url(response.signature),
      userHandle: response.userHandle
        ? bufferToBase64url(response.userHandle)
        : null,
    },
  }
}
