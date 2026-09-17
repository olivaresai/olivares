// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/**
 * The provider plane's wire shapes (D19).
 *
 * There is no field here for a credential, and there is no field for the vault
 * locator either. The first omission is obvious. The second is the subtle one: the
 * locator is not secret, but it is the name a caller would need in order to ask the
 * vault for the value, and a browser has no reason to hold it.
 */

/** The four credential kinds the engine accepts. Closed on the engine, and closed
 * here: a kind decides which environment variables a child process receives, so an
 * open set would be an open injection set. */
export type ProviderKind = 'anthropic' | 'openai' | 'xai' | 'openai_compatible'

/** Two states. Revoking is final for that reference: the record is kept so the
 * sessions it authorised still read truthfully, not so it can come back. */
export type ProviderState = 'active' | 'revoked'

/**
 * What the last connection test found. Three values, and they are three answers to
 * different questions:
 *  - `''`            nobody has tested it. It is never green for being registered.
 *  - `ok`            the provider answered and accepted the credential.
 *  - `refused`       the provider answered and REJECTED the credential. Replace it.
 *  - `unreachable`   no answer was obtained. This says nothing about the credential,
 *                    and reporting it as a refusal sends an operator to regenerate a
 *                    key that was never the problem.
 */
export type ProbeState = '' | 'ok' | 'refused' | 'unreachable'

export interface ProviderRecordDTO {
  provider_ref: string
  kind: ProviderKind
  display_name: string
  base_url?: string
  /** Four characters and a marker. Enough to tell two credentials apart in a
   * picker, useless for anything else. */
  key_hint?: string
  state: ProviderState
  /** What the last SUCCESSFUL probe listed. An observation with a timestamp beside
   * it, not a catalogue: a provider can add or retire a model without this moving. */
  models?: string[]
  probe_state?: ProbeState
  probe_detail?: string
  probe_latency_ms?: number
  probed_at?: string
  created_at?: string
  updated_at?: string
  revoked_at?: string
}

/** The credential travels in `api_key` on the way IN and in nothing on the way out. */
export interface CreateProviderRequest {
  kind: ProviderKind
  display_name: string
  base_url?: string
  api_key: string
}

/** Rename, re-endpoint and/or rotate. `kind` is absent because changing it would
 * redefine every launch the record's bindings already authorise. */
export interface PatchProviderRequest {
  display_name?: string
  base_url?: string
  api_key?: string
}
