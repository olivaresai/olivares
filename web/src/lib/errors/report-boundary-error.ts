// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ErrorInfo } from 'react'
import { http } from '@/lib/api/client'

// The router's boundary hands the React component stack to onCatch, not to the
// error component; it is kept here until the error component reports it.
const componentStacks = new WeakMap<object, string>()

/** The routes' onCatch: remember the component stack of a caught error. */
export function rememberComponentStack(error: unknown, info?: ErrorInfo): void {
  if (typeof error === 'object' && error !== null && info?.componentStack) {
    componentStacks.set(error, info.componentStack)
  }
}

export function createBoundaryErrorId(): string {
  return crypto.randomUUID().slice(0, 8)
}

function errorText(error: unknown): string {
  if (error instanceof Error)
    return error.stack ?? `${error.name}: ${error.message}`
  try {
    return typeof error === 'string'
      ? error
      : (JSON.stringify(error) ?? String(error))
  } catch {
    return String(error)
  }
}

/** Log a boundary error once: the browser console always, the engine log when
 * the engine has telemetry on (it decides). Only the id is shown on screen; a
 * failed delivery never touches the recovery UI or the session. */
export function reportBoundaryError(errorId: string, error: unknown): void {
  const componentStack =
    typeof error === 'object' && error !== null
      ? (componentStacks.get(error) ?? null)
      : null
  console.error(
    `[olivares boundary error ${errorId}]`,
    error,
    componentStack ? `\nComponent stack:${componentStack}` : '',
  )
  void http
    .post<void>(
      '/v1/console/errors',
      {
        error_id: errorId,
        error: errorText(error),
        component_stack: componentStack,
        route_path: window.location.pathname,
      },
      { sessionEffects: 'none' },
    )
    .catch((reportError: unknown) => {
      console.error('boundary engine report failed', reportError)
    })
}

/** Only a failed dynamic import needs a fresh document and module map: React.lazy
 * keeps the rejected import, so re-mounting throws it again. Ordinary render failures
 * recover by re-mounting the failed view. The messages are each browser's own
 * (Chromium, Firefox, Safari) and Vite's preload helper's. */
export function isChunkLoadError(error: unknown): boolean {
  if (!(error instanceof Error)) return false
  const message = error.message.toLowerCase()
  return (
    error.name === 'ChunkLoadError' ||
    message.includes('loading chunk') ||
    message.includes('dynamically imported module') ||
    message.includes('importing a module script failed') ||
    message.includes('unable to preload css') ||
    message.includes('could not fetch script')
  )
}
