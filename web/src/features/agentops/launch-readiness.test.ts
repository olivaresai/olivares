// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import {
  currentLaunchReadinessObservation,
  effectiveLaunchTransport,
  isProfileChangedError,
  launchRequestPermission,
  observationMatchesSelection,
  PROFILE_CHANGED_CODE,
  readinessObservationIsUnusable,
  type LaunchReadinessObserverSnapshot,
  type SessionLaunchReadiness,
} from './launch-readiness'

function sample(
  over: Partial<SessionLaunchReadiness> = {},
): SessionLaunchReadiness {
  return {
    profile_ref: 'ppf_a',
    profile_version: 1,
    driver: 'claude',
    profile_state: 'active',
    environment_ref: 'xenv_1',
    observed_at: '2026-09-06T22:00:00Z',
    selection: { transport: 'stream-json', isolation: 'native' },
    configuration_state: 'ready',
    checks: [
      { check: 'profile', state: 'ready', code: 'profile_active' },
      { check: 'local_environment', state: 'ready', code: 'environment_local' },
      { check: 'driver', state: 'ready', code: 'driver_operable' },
      { check: 'runner', state: 'ready', code: 'runner_ready' },
      { check: 'program', state: 'ready', code: 'program_present' },
      { check: 'homes', state: 'ready', code: 'homes_resolved' },
      {
        check: 'auth_source',
        state: 'ready',
        code: 'auth_source_account_home',
      },
      {
        check: 'credential_source',
        state: 'not_applicable',
        code: 'credential_source_not_injected',
      },
      {
        check: 'runtime_credentials',
        state: 'not_applicable',
        code: 'runtime_credentials_not_requested',
      },
    ],
    transport_capabilities: {
      protocol: 'claude_stream_json',
      io: 'bidirectional',
      input: 'line',
    },
    provider_authentication: {
      state: 'unknown',
      code: 'not_observed_for_this_launch',
    },
    launch_authorization: {
      state: 'unknown',
      code: 'evaluated_on_submit',
      required_permission: 'sessions:run:write',
    },
    remaining_checks: [
      'workspace_and_template_validation',
      'current_launch_authorization',
      'claim_admission',
      'credential_mint_when_required',
      'process_start',
      'provider_authentication_and_protocol',
    ],
    ...over,
  }
}

describe('launch-readiness helpers', () => {
  it('permits a request only for ready, and treats unknown as uncertain not ready', () => {
    expect(launchRequestPermission(sample())).toBe('permit')
    expect(
      launchRequestPermission(sample({ configuration_state: 'unknown' })),
    ).toBe('uncertain')
    expect(
      launchRequestPermission(
        sample({ configuration_state: 'not_configured' }),
      ),
    ).toBe('block')
    expect(
      launchRequestPermission(sample({ configuration_state: 'unsupported' })),
    ).toBe('block')
    expect(launchRequestPermission(undefined)).toBe('none')
  })

  it('detects the typed 409 by code, never by message text', () => {
    expect(
      isProfileChangedError(
        new ApiError(409, PROFILE_CHANGED_CODE, 'anything at all'),
      ),
    ).toBe(true)
    expect(
      isProfileChangedError(new ApiError(409, 'conflict', 'profile_changed')),
    ).toBe(false)
    expect(
      isProfileChangedError(new ApiError(409, PROFILE_CHANGED_CODE, 'x')),
    ).toBe(true)
    expect(
      readinessObservationIsUnusable(new ApiError(401, 'unauthenticated', 'x')),
    ).toBe(true)
    expect(
      readinessObservationIsUnusable(new ApiError(403, 'forbidden', 'x')),
    ).toBe(true)
    expect(
      readinessObservationIsUnusable(new ApiError(404, 'not_found', 'x')),
    ).toBe(true)
    expect(
      readinessObservationIsUnusable(new ApiError(503, 'unavailable', 'x')),
    ).toBe(false)
  })

  it('rejects another profile or transport as a current observation', () => {
    const data = sample()
    expect(
      observationMatchesSelection(data, {
        profileRef: 'ppf_a',
        transport: 'stream-json',
        isolation: 'native',
      }),
    ).toBe(true)
    expect(
      observationMatchesSelection(data, {
        profileRef: 'ppf_b',
        transport: 'stream-json',
        isolation: 'native',
      }),
    ).toBe(false)
    expect(
      observationMatchesSelection(data, {
        profileRef: 'ppf_a',
        transport: 'remote-control',
        isolation: 'native',
      }),
    ).toBe(false)
  })

  it('takes the preview transport after template merge, not the stale form field', () => {
    expect(effectiveLaunchTransport('stream-json', 'remote-control')).toBe(
      'remote-control',
    )
    expect(effectiveLaunchTransport('remote-control', undefined)).toBe(
      'remote-control',
    )
  })

  it('treats only an enabled, settled, matching success as a current observation', () => {
    const sel = {
      profileRef: 'ppf_a',
      transport: 'stream-json' as const,
      isolation: 'native' as const,
    }
    const ready = sample()
    const unknown = sample({ configuration_state: 'unknown' })
    const snap = (
      over: Partial<LaunchReadinessObserverSnapshot>,
    ): LaunchReadinessObserverSnapshot => ({
      data: ready,
      status: 'success',
      isError: false,
      isFetching: false,
      observationEnabled: true,
      ...over,
    })
    expect(currentLaunchReadinessObservation(snap({}), sel)).toBe(ready)
    expect(
      currentLaunchReadinessObservation(snap({ data: unknown }), sel),
    ).toBe(unknown)
    expect(
      currentLaunchReadinessObservation(snap({ isFetching: true }), sel),
    ).toBeUndefined()
    expect(
      currentLaunchReadinessObservation(
        snap({ isError: true, status: 'error' }),
        sel,
      ),
    ).toBeUndefined()
    expect(
      currentLaunchReadinessObservation(
        snap({ observationEnabled: false }),
        sel,
      ),
    ).toBeUndefined()
    expect(
      currentLaunchReadinessObservation(
        snap({ status: 'pending', data: undefined }),
        sel,
      ),
    ).toBeUndefined()
    expect(
      currentLaunchReadinessObservation(
        snap({
          data: sample({
            profile_ref: 'ppf_b',
          }),
        }),
        sel,
      ),
    ).toBeUndefined()
    expect(launchRequestPermission(unknown)).toBe('uncertain')
    expect(
      launchRequestPermission(
        currentLaunchReadinessObservation(
          snap({ isError: true, status: 'error', data: ready }),
          sel,
        ),
      ),
    ).toBe('none')
  })
})
