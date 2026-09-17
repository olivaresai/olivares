// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ApiError, isApiError } from '@/lib/api/errors'
import type { paths } from '@/lib/api/openapi.gen'
import type { Transport } from './types'

/**
 * The launch-readiness contract AS THE ENGINE SERVES IT. Shapes are derived from
 * the generated beta path so a new closed enum fails the compiler instead of
 * being re-derived in the console.
 */
type JsonOf<R> = R extends { content: { 'application/json': infer B } }
  ? B
  : never

type LaunchReadinessPath =
  paths['/v1/m/sessions/provider-profiles/{ref}/launch-readiness']
type LaunchReadinessGet = LaunchReadinessPath['get']

export type SessionLaunchReadiness = JsonOf<
  LaunchReadinessGet['responses'][200]
>
export type LaunchReadinessCheck = SessionLaunchReadiness['checks'][number]
export type LaunchReadinessSelection = SessionLaunchReadiness['selection']
export type LaunchReadinessAggregate =
  SessionLaunchReadiness['configuration_state']
export type LaunchReadinessTransport = LaunchReadinessSelection['transport']
export type LaunchReadinessIsolation = LaunchReadinessSelection['isolation']
export type LaunchReadinessConflict = JsonOf<
  LaunchReadinessGet['responses'][409]
>

/** Closed identifier of this route's 409. Detect by code, never by message text. */
export const PROFILE_CHANGED_CODE: LaunchReadinessConflict['error']['code'] =
  'profile_changed'

export const DEFAULT_READINESS_TRANSPORT: LaunchReadinessTransport =
  'stream-json'
export const DEFAULT_READINESS_ISOLATION: LaunchReadinessIsolation = 'native'

export function isProfileChangedError(err: unknown): boolean {
  return (
    isApiError(err) && err.status === 409 && err.code === PROFILE_CHANGED_CODE
  )
}

/** 401/403/404 must never keep a previous observation on screen or on submit. */
export function readinessObservationIsUnusable(err: unknown): boolean {
  if (!isApiError(err)) return false
  return err.status === 401 || err.status === 403 || err.status === 404
}

/**
 * Snapshot of the TanStack observer that both the panel and the launch gate
 * consult. `observationEnabled` is the hook's live `enabled` (input, permission
 * and ref): a disabled observer still retains `data`, so flags alone are not
 * enough to drop a previous ready body after authority loss.
 */
export type LaunchReadinessObserverSnapshot = {
  data: SessionLaunchReadiness | undefined
  status: 'pending' | 'error' | 'success'
  isError: boolean
  isFetching: boolean
  observationEnabled: boolean
}

/**
 * The only observation that may be painted or used to permit a request.
 * Pending, refetch, error, disabled/permission-loss, tuple mismatch and a
 * non-success status all yield undefined. A successful `unknown` body is a
 * current observation; a transport/5xx `isError` is not.
 */
export function currentLaunchReadinessObservation(
  query: LaunchReadinessObserverSnapshot,
  sel: {
    profileRef: string
    transport: LaunchReadinessTransport
    isolation: LaunchReadinessIsolation
  },
): SessionLaunchReadiness | undefined {
  if (!query.observationEnabled) return undefined
  if (query.isError || query.isFetching) return undefined
  if (query.status !== 'success') return undefined
  return observationMatchesSelection(query.data, sel) ? query.data : undefined
}

/**
 * Whether THIS observation may enable a launch REQUEST. unknown is not ready:
 * it preserves a valid runner's request path with visible uncertainty.
 * not_configured and unsupported are known-inviable and must not POST.
 */
export type LaunchRequestPermission = 'permit' | 'uncertain' | 'block' | 'none'

export function launchRequestPermission(
  data: SessionLaunchReadiness | undefined,
): LaunchRequestPermission {
  if (!data) return 'none'
  switch (data.configuration_state) {
    case 'ready':
      return 'permit'
    case 'unknown':
      return 'uncertain'
    case 'not_configured':
    case 'unsupported':
      return 'block'
    default:
      return 'none'
  }
}

export function observationMatchesSelection(
  data: SessionLaunchReadiness | undefined,
  sel: {
    profileRef: string
    transport: LaunchReadinessTransport
    isolation: LaunchReadinessIsolation
  },
): data is SessionLaunchReadiness {
  return (
    !!data &&
    data.profile_ref === sel.profileRef &&
    data.selection.transport === sel.transport &&
    data.selection.isolation === sel.isolation
  )
}

export function asLaunchTransport(
  value: string | undefined,
): LaunchReadinessTransport {
  return value === 'remote-control' ? 'remote-control' : 'stream-json'
}

export function asLaunchIsolation(
  value: string | undefined,
): LaunchReadinessIsolation {
  if (value === 'container' || value === 'sandbox') return value
  return 'native'
}

export function effectiveLaunchTransport(
  formTransport: Transport,
  previewTransport: string | undefined,
): LaunchReadinessTransport {
  return asLaunchTransport(previewTransport ?? formTransport)
}

export function checkHasCause(check: LaunchReadinessCheck): boolean {
  return check.state !== 'ready' && check.state !== 'not_applicable'
}

export function hasManagedInjectionLimitation(
  data: SessionLaunchReadiness,
): boolean {
  return data.checks.some(
    (c) => c.code === 'managed_injection_not_applied_for_transport',
  )
}

export function launchFailureMessage(err: unknown, fallback: string): string {
  if (err instanceof ApiError) return err.message
  if (err instanceof Error) return err.message
  return fallback
}
