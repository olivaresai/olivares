// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { SessionLaunchReadiness } from './launch-readiness'

/**
 * The profile-scoped official CLI observation (HC1): GET
 * /v1/m/sessions/provider-profiles/{ref}/host-tools, as Root ratified it. The route is
 * not in the generated OpenAPI yet, so the shape is declared here and every response is
 * PARSED: closed codes only, unknown future group codes weaken to `unknown`, a malformed
 * body is refused instead of painted. The observation is advisory. Nothing here changes
 * readiness, launch permission, installation, pinning or registration.
 */
export const HOST_TOOL_STATES = [
  'observed',
  'none_observed',
  'unsupported_driver',
  'unknown',
  'not_checked_in_this_environment',
] as const
export type HostToolState = (typeof HOST_TOOL_STATES)[number]

export const HOST_TOOL_ORIGINS = [
  'managed',
  'vendor-default',
  'path',
  'named',
  'unknown',
] as const
export type HostToolOrigin = (typeof HOST_TOOL_ORIGINS)[number]

export const HOST_TOOL_MATCHES = [
  'registered',
  'manifest-corroborated',
  'unregistered-observed',
  'unverified',
  'damaged',
  'unknown',
] as const
export type HostToolMatch = (typeof HOST_TOOL_MATCHES)[number]

export const HOST_TOOL_CONFIGURED = ['same', 'different', 'unknown'] as const
export type HostToolConfigured = (typeof HOST_TOOL_CONFIGURED)[number]

export interface HostToolGroup {
  origin: HostToolOrigin
  match: HostToolMatch
  executable: boolean
  configured: HostToolConfigured
  count: number
}

export interface HostToolObservation {
  profile_ref: string
  profile_version: number
  driver: string
  environment_ref: string
  evaluated_environment_ref: string
  observed_at: string
  state: HostToolState
  groups: HostToolGroup[]
}

/** The readiness facts an observation must belong to before it may be shown. */
export interface HostToolIdentity {
  profileRef: string
  profileVersion: number
  driver: string
  environmentRef: string
  evaluatedEnvironmentRef: string
}

/** A body that is not the ratified shape. Shown as unavailable, never painted. */
export class HostToolsResponseError extends Error {
  constructor(detail: string) {
    super(`host-tools response refused: ${detail}`)
    this.name = 'HostToolsResponseError'
  }
}

/** A well-formed answer for another profile, version, driver or environment. */
export class HostToolsIdentityMismatchError extends Error {
  constructor() {
    super('host-tools response belongs to another profile snapshot')
    this.name = 'HostToolsIdentityMismatchError'
  }
}

export function hostToolIdentityOf(
  readiness: SessionLaunchReadiness,
): HostToolIdentity {
  return {
    profileRef: readiness.profile_ref,
    profileVersion: readiness.profile_version,
    driver: readiness.driver,
    environmentRef: readiness.environment_ref,
    evaluatedEnvironmentRef: readiness.evaluated_environment_ref ?? '',
  }
}

/** Observation is offered only while the current program check is unresolved. */
export function hostToolObservationApplies(
  readiness: SessionLaunchReadiness,
): boolean {
  const program = readiness.checks.find((c) => c.check === 'program')
  return program?.state === 'not_configured' || program?.state === 'unknown'
}

export function hostToolObservationMatches(
  obs: HostToolObservation,
  id: HostToolIdentity,
): boolean {
  return (
    obs.profile_ref === id.profileRef &&
    obs.profile_version === id.profileVersion &&
    obs.driver === id.driver &&
    obs.environment_ref === id.environmentRef &&
    obs.evaluated_environment_ref === id.evaluatedEnvironmentRef
  )
}

function closed<T extends string>(
  allowed: readonly T[],
  value: unknown,
  field: string,
): T | 'unknown' {
  if (typeof value !== 'string') throw new HostToolsResponseError(field)
  return (allowed as readonly string[]).includes(value)
    ? (value as T)
    : 'unknown'
}

function text(value: unknown, field: string): string {
  if (typeof value !== 'string') throw new HostToolsResponseError(field)
  return value
}

const tupleOrder = (g: HostToolGroup) =>
  [
    HOST_TOOL_ORIGINS.indexOf(g.origin),
    HOST_TOOL_MATCHES.indexOf(g.match),
    g.executable ? 0 : 1,
    HOST_TOOL_CONFIGURED.indexOf(g.configured),
  ] as const

/**
 * Parse one response. Groups are merged by their closed tuple AFTER unknown codes are
 * weakened, so every candidate stays counted and no tuple appears twice; they are
 * ordered deterministically. Only `observed` may carry groups, and it must carry some.
 */
export function parseHostToolObservation(raw: unknown): HostToolObservation {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) {
    throw new HostToolsResponseError('body')
  }
  const body = raw as Record<string, unknown>
  const version = body.profile_version
  if (typeof version !== 'number' || !Number.isInteger(version)) {
    throw new HostToolsResponseError('profile_version')
  }
  const stateText = text(body.state, 'state')
  const state: HostToolState = (HOST_TOOL_STATES as readonly string[]).includes(
    stateText,
  )
    ? (stateText as HostToolState)
    : 'unknown'
  if (!Array.isArray(body.groups)) throw new HostToolsResponseError('groups')

  const merged = new Map<string, HostToolGroup>()
  for (const item of body.groups as unknown[]) {
    if (!item || typeof item !== 'object') {
      throw new HostToolsResponseError('group')
    }
    const g = item as Record<string, unknown>
    const count = g.count
    if (typeof count !== 'number' || !Number.isInteger(count) || count < 1) {
      throw new HostToolsResponseError('group.count')
    }
    if (typeof g.executable !== 'boolean') {
      throw new HostToolsResponseError('group.executable')
    }
    const group: HostToolGroup = {
      origin: closed(HOST_TOOL_ORIGINS, g.origin, 'group.origin'),
      match: closed(HOST_TOOL_MATCHES, g.match, 'group.match'),
      executable: g.executable,
      configured: closed(
        HOST_TOOL_CONFIGURED,
        g.configured,
        'group.configured',
      ),
      count,
    }
    const key = `${group.origin}|${group.match}|${group.executable}|${group.configured}`
    const prev = merged.get(key)
    merged.set(key, prev ? { ...prev, count: prev.count + count } : group)
  }
  const groups = [...merged.values()].sort((a, b) => {
    const x = tupleOrder(a)
    const y = tupleOrder(b)
    for (let i = 0; i < x.length; i++) if (x[i] !== y[i]) return x[i] - y[i]
    return 0
  })

  if (stateText === 'observed' && groups.length === 0) {
    throw new HostToolsResponseError('observed without groups')
  }
  if (stateText !== 'observed' && state !== 'unknown' && groups.length > 0) {
    throw new HostToolsResponseError(`${state} with groups`)
  }

  return {
    profile_ref: text(body.profile_ref, 'profile_ref'),
    profile_version: version,
    driver: text(body.driver, 'driver'),
    environment_ref: text(body.environment_ref, 'environment_ref'),
    evaluated_environment_ref: text(
      body.evaluated_environment_ref,
      'evaluated_environment_ref',
    ),
    observed_at: text(body.observed_at, 'observed_at'),
    state,
    // A future state weakened to unknown shows no groups.
    groups: state === 'observed' ? groups : [],
  }
}
