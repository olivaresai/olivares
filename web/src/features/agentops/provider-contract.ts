// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The two SERVER-OWNED launch facts the console has to read rather than guess: which
// input contract a run answers to, and which environment variables a provider profile
// owns on its child. Both are decided by the engine; this file only mirrors them so the
// console can be accurate BEFORE the request, and neither one is a second authority.
import type { RunDTO } from './types'

/**
 * The driver key of the historical Claude runner. It is not a registered protocol
 * driver: that path is frame-driven, the child reads NDJSON on stdin, and its runs
 * are the ones (together with legacy runs that name no profile at all) that take a
 * raw line.
 */
export const CLAUDE_DRIVER = 'claude'

/** How one run accepts operator input. */
export type RunInputMode = 'text' | 'line'

/**
 * runInputMode reports the contract this run's `POST /runs/{ref}/input` answers to,
 * from the driver the SERVER persisted on the run at launch (`provider_driver`).
 *
 * ⛔ IT IS DECIDED BY THE RUN, NEVER BY WHAT THE OPERATOR TYPED. A run driven by an
 * owned provider protocol has a JSON-RPC peer for a child, so a raw line on its stdin
 * is not "input": it is that peer's whole method surface, approvals included. The
 * engine refuses it before any child effect
 * (`modules/sessions/runtime.go` sendInputLoaded, 400) and refuses the mirror mistake
 * too — text to a non-driver run is a 400 as well. This function exists so the console
 * asks for the right one, not so the guard can be relaxed.
 *
 * The rule is deny-closed in the direction that matters: only the historical Claude
 * path (and a legacy run with no profile at all) is sent a raw frame. Any other
 * driver — today `codex` — is spoken to as a TURN. A profile whose driver has no
 * registered runner on the node is not operable and therefore cannot be running here.
 */
export function runInputMode(
  run: Pick<RunDTO, 'provider_driver'>,
): RunInputMode {
  const driver = run.provider_driver?.trim()
  return driver && driver !== CLAUDE_DRIVER ? 'text' : 'line'
}

/**
 * The environment variables a provider profile OWNS on its launched child. The server
 * resolves them from the profile and refuses a launch whose `env_allow` (or whose
 * launch gate) names any of them, for every driver rather than only the current one:
 * a Claude launch that accepted `CODEX_HOME` would be handing the child a home nobody
 * authorized for a provider nobody selected.
 *
 * Mirrors `providerHomeEnvName` in `modules/sessions/runtime_profile.go`. The server
 * stays the authority — this list only lets the dialog say so before the 400.
 */
export const PROFILE_OWNED_ENV = [
  'HOME',
  'CLAUDE_CONFIG_DIR',
  'CODEX_HOME',
  'GROK_HOME',
] as const

export type ProfileOwnedEnv = (typeof PROFILE_OWNED_ENV)[number]

/**
 * profileOwnedEnvNames returns, in the order the operator wrote them, the names in a
 * comma-separated `env_allow` that the profile owns. Empty means nothing conflicts —
 * an ORDINARY variable (`PATH`, `TERM`, an application's own) is forwarded normally
 * and is not this list's business.
 */
export function profileOwnedEnvNames(envAllow: string): ProfileOwnedEnv[] {
  const owned = new Set<string>(PROFILE_OWNED_ENV)
  return envAllow
    .split(',')
    .map((s) => s.trim())
    .filter((name): name is ProfileOwnedEnv => owned.has(name))
}
