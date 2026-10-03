// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { apiFetch } from '@/lib/api/client'
import { ApiError, NetworkError, parseErrorEnvelope } from '@/lib/api/errors'
import { useSessionStore } from '@/stores/session'
import { AuthorityLostError } from './auth-boundary'
import type { CreateRunRequest, RunDTO } from './types'

/** Exchange and launch are one explicit operator intent. The delegated bearer stays
 * in this stack frame, never in React, a store, a query key or mutation variables.
 * Neither request renews/replays; denial never launches with human authority. */
export async function launchRunAsAgent(
  body: CreateRunRequest,
  actor: string,
  tenant: string | null,
  authority: { dispatchGuard: () => void; signal?: AbortSignal },
): Promise<RunDTO> {
  const { csrfToken: token, credentialGeneration } = useSessionStore.getState()
  const dispatchGuard = () => {
    authority.dispatchGuard()
    const current = useSessionStore.getState()
    if (
      !actor ||
      !tenant ||
      !token ||
      current.csrfToken !== token ||
      current.credentialGeneration !== credentialGeneration
    )
      throw new AuthorityLostError()
  }
  dispatchGuard()
  const form = new URLSearchParams({
    grant_type: 'urn:ietf:params:oauth:grant-type:token-exchange',
    subject_token: 'browser-session',
    subject_token_type: 'urn:ietf:params:oauth:token-type:access_token',
    requested_actor: actor,
    resource: window.location.origin,
    scope: 'write',
    name: 'Console agent session launch',
  })
  let exchange: {
    access_token: string
    token_type: string
    issued_token_type: string
    expires_in: number
  }
  try {
    exchange = await apiFetch('/v1/auth/token-exchange', {
      method: 'POST',
      rawBody: form.toString(),
      contentType: 'application/x-www-form-urlencoded',
      tenant,
      sessionEffects: 'none',
      signal: authority.signal,
      dispatchGuard,
    })
  } catch (error) {
    if (
      error instanceof ApiError &&
      error.body &&
      typeof error.body === 'object' &&
      'error_description' in error.body &&
      typeof error.body.error_description === 'string'
    ) {
      throw new ApiError(
        error.status,
        'error' in error.body && typeof error.body.error === 'string'
          ? error.body.error
          : error.code,
        error.body.error_description,
        error.requestId,
      )
    }
    throw error
  }
  dispatchGuard()
  if (
    !exchange.access_token ||
    exchange.token_type !== 'Bearer' ||
    exchange.issued_token_type !==
      'urn:ietf:params:oauth:token-type:access_token' ||
    !(exchange.expires_in > 0)
  )
    throw new Error(
      'Agent token exchange returned an invalid credential response.',
    )
  // The shared client deliberately forbids overriding its operator bearer.
  // This one fixed, same-origin endpoint uses the exchanged credential directly,
  // with no refresh, redirect, retry or global session effects.
  dispatchGuard()
  let response: Response
  try {
    response = await fetch('/v1/m/sessions/runs', {
      method: 'POST',
      body: JSON.stringify(body),
      headers: new Headers({
        Accept: 'application/json',
        'Content-Type': 'application/json',
        Authorization: `Bearer ${exchange.access_token}`,
        'X-Olivares-Tenant': tenant!,
      }),
      signal: authority.signal,
      redirect: 'error',
      cache: 'no-store',
    })
  } catch (error) {
    dispatchGuard()
    throw new NetworkError(
      'Agent session launch could not reach the server.',
      error,
    )
  }
  dispatchGuard()
  const raw = await response.text()
  dispatchGuard()
  let result: unknown
  try {
    result = JSON.parse(raw)
  } catch {
    throw new ApiError(
      response.status,
      'internal',
      'Agent session launch returned an invalid response.',
    )
  }
  if (!response.ok) {
    const { code, message, details } = parseErrorEnvelope(
      result,
      'Agent session launch was refused.',
    )
    throw new ApiError(
      response.status,
      code,
      message,
      response.headers.get('X-Request-ID') ?? undefined,
      details,
    )
  }
  return result as RunDTO
}
