// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { SessionLaunchReadiness } from './launch-readiness'

export function fixtureReadiness(
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
