// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import type { LoginResponse } from '@/lib/api/types'
import { configureApiClient } from '@/lib/api/client'
import { ApiError, NetworkError, parseErrorEnvelope } from '@/lib/api/errors'
import { useSessionStore } from '@/stores/session'

let restoring: Promise<void> | null = null
// The old-storage bearer goes to the engine once per page: a retry must not resend it.
let migrationSent = false

// Every cookie-mode sign-in, including module sign-in, goes through the shared
// client. Recovery owns the existing flight; the client owns dispatch ordering.
// A failed recovery still allows the new sign-in's independent credential proof.
configureApiClient({
  beforeBrowserSignIn: () => restoreBrowserSession().catch(() => {}),
})

/** One restore/migration per page, including React's development effect replay.
 * Retain legacy storage only on transient failure so a reload can retry. */
export function restoreBrowserSession(retry = false): Promise<void> {
  if (!restoring)
    restoring = restore(retry).finally(() => {
      restoring = null
    })
  return restoring
}

/** Page load. A clean install has no administrator yet, so no browser session can
 * exist, and the setup gate answers every gated route — this restore included — 409
 * setup_required: the one red request of every first hour (Root on first-hour check). Server-info
 * (read on load anyway, and cached for the shell) says so first; the restore runs only
 * once setup is done, or when server-info cannot tell. */
export async function restoreOnPageLoad(
  serverInfo: () => Promise<{ setup_required?: boolean }>,
): Promise<void> {
  let setupRequired = false
  try {
    setupRequired = (await serverInfo()).setup_required === true
  } catch {
    // Unknown: recover as before; the restore handles a 409 itself.
  }
  if (setupRequired) {
    // What a 409 from the restore would have done: no session, and ready.
    if (!useSessionStore.getState().ready) useSessionStore.getState().clear()
    return
  }
  return restoreBrowserSession()
}

/** The longest wait between two attempts while the engine is unreachable (1, 2, 4, 8 s, then this). */
const RETRY_CAP_MS = 10_000
/** How long page load waits for the engine, and how long a retry does: an engine that always
 * answers in 9 s is slow, not down, so a retry waits longer. Bounded, because a sign-in waits
 * for the recovery in flight (`beforeBrowserSignIn`). */
const FIRST_DEADLINE_MS = 8_000
const RETRY_DEADLINE_MS = 30_000

/** While page-load recovery ended with the engine unreachable, ask again with a capped
 * backoff until it answers (a session, or 401/409), or `signal` aborts. The person stays on
 * the page they asked for: nothing here navigates. */
export async function retryWhileUnreachable(
  signal: AbortSignal,
): Promise<void> {
  for (
    let attempt = 0;
    useSessionStore.getState().engineUnreachable && !signal.aborted;
    attempt++
  ) {
    await new Promise<void>((resolve) => {
      const onAbort = () => {
        clearTimeout(timer)
        resolve()
      }
      const timer = setTimeout(
        () => {
          signal.removeEventListener('abort', onAbort)
          resolve()
        },
        Math.min(1_000 * 2 ** attempt, RETRY_CAP_MS),
      )
      signal.addEventListener('abort', onAbort, { once: true })
    })
    if (signal.aborted) return
    await restoreBrowserSession(true).catch(() => {})
  }
}

async function restore(retry: boolean): Promise<void> {
  const initial = useSessionStore.getState()
  // A retry reopens only a recovery that ended with the engine unreachable.
  const open = (s: typeof initial) => (retry ? s.engineUnreachable : !s.ready)
  if (!open(initial)) return
  if (!retry) migrationSent = false
  // Login or sign-out owns the newer generation. A response from page-load
  // recovery must never install, clear or migrate credentials after it.
  const isCurrent = () => {
    const current = useSessionStore.getState()
    return (
      current.credentialGeneration === initial.credentialGeneration &&
      open(current)
    )
  }
  let legacy: string | null = null
  try {
    const stored = JSON.parse(
      localStorage.getItem('olivares.session') ?? 'null',
    )
    if (
      typeof stored?.state?.token === 'string' &&
      (stored.state.token.startsWith('olvs_') ||
        stored.state.token.startsWith('olvt_'))
    )
      legacy = stored.state.token
  } catch {
    // Malformed or unavailable legacy storage does not prevent cookie recovery.
  }
  const headers = new Headers({ Accept: 'application/json' })
  // Bound the whole recovery flight, including migration and streamed JSON.
  // Abort the request itself so it cannot deliver an old cookie after sign-in.
  const controller = new AbortController()
  const deadline = setTimeout(
    () => controller.abort(),
    retry ? RETRY_DEADLINE_MS : FIRST_DEADLINE_MS,
  )
  // No answer in time, a 5xx or a 429: the engine is slow, restarting or busy, which says
  // nothing about the session. A 401/409 is the engine's own answer and ends it.
  let unreachable = false
  try {
    let response: Response
    try {
      // A previous migration may have set the cookie even if its response was
      // interrupted. Recover that session before retrying its now-rotated bearer.
      response = await fetch('/v1/auth/browser-session', {
        method: 'GET',
        headers,
        credentials: 'same-origin',
        signal: controller.signal,
      })
      if (!isCurrent()) return
      if (response.status === 401 && legacy && !migrationSent) {
        migrationSent = true
        headers.set('Authorization', `Bearer ${legacy}`)
        headers.set('X-Olivares-Session', 'cookie')
        response = await fetch('/v1/auth/browser-session', {
          method: 'POST',
          headers,
          credentials: 'same-origin',
          signal: controller.signal,
        })
      }
    } catch (cause) {
      if (!isCurrent()) return
      unreachable = true
      throw new NetworkError('The control plane is unreachable.', cause)
    }
    if (!isCurrent()) return
    if (response.status === 401 || response.status === 409) {
      useSessionStore.getState().clear()
      return
    }
    if (response.status >= 500 || response.status === 429) {
      unreachable = true
      throw new ApiError(
        response.status,
        'engine_unavailable',
        'The engine is not available.',
      )
    }
    const body = await response.json()
    controller.signal.throwIfAborted()
    if (!isCurrent()) return
    if (!response.ok) {
      const error = parseErrorEnvelope(body, 'Session recovery failed.')
      throw new ApiError(response.status, error.code, error.message)
    }
    const session = body as LoginResponse
    if (!session.csrf_token || !session.session_id || !session.expires_at)
      throw new Error('Invalid browser session response.')
    useSessionStore.getState().setSession({
      csrfToken: session.csrf_token,
      sessionId: session.session_id,
      expiresAt: session.expires_at,
    })
  } finally {
    clearTimeout(deadline)
    unreachable ||= controller.signal.aborted
    // Recovery owns readiness on every outcome, including a separate caller's
    // caught failure. Keep legacy storage on transient failure for reload only;
    // a later provider or sign-in must not start a second flight on this page.
    if (isCurrent())
      useSessionStore.setState({ ready: true, engineUnreachable: unreachable })
  }
}
