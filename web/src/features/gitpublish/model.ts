// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The decisions the Git publication screens make, kept out of the components so they are
// tested once: what an answer MEANS, which follow-up an intent offers, and which authority
// each action is mounted with.
//
// ⛔ THERE IS NO RETRY HERE, ON PURPOSE. An uncertain remote outcome is not permission to send
//    again (docs-site reference/modules/gitpublish.md): the effect may have landed, and a
//    second dispatch can race it. The follow-ups are reconcile, which reads the host and
//    never dispatches (modules/gitpublish/reconcile.go), and abandon, which records that an
//    administrator takes responsibility and proves nothing about the host.
import { ApiError, NetworkError } from '@/lib/api/errors'
import type { IntentState, PublicationEffect, PublicationIntent } from './types'

/** The closed refusal vocabulary the module answers with (errors.go, targets.go,
 * publish.go, reconcile.go, routes.go `decode`). Each code has copy under `refusals.<code>`. */
export const REFUSAL_CODES = [
  'authority_expired',
  'authority_unavailable',
  'base_not_allowed',
  'binding_not_approved',
  'content_mismatch',
  'default_branch_refused',
  'dispatch_deadline_passed',
  'field_not_accepted',
  'forbidden',
  'head_mismatch',
  'host_credential_refused',
  'host_unavailable',
  'intent_settled',
  'invalid_request',
  'lookup_incomplete',
  'not_found',
  'not_mergeable',
  'operation_conflict',
  'protected_branch_refused',
  'push_prefix_overlaps_base',
  'ref_not_allowed',
  'repository_outside_binding',
  'runtime_credential_refused',
  'scoped_grant_required',
  'stale_lease',
  'step_up_required',
  'subject_mismatch',
  'target_changed',
  'target_in_use',
  'unresolved_intent',
  'unsupported_requirement',
  'version_conflict',
  'workflow_change_refused',
] as const

export type RefusalCode = (typeof REFUSAL_CODES)[number]

export function isKnownRefusal(code: string): code is RefusalCode {
  return (REFUSAL_CODES as readonly string[]).includes(code)
}

/** The `gitpublish` key of the copy for a refusal or reason code; the caller shows the raw
 * code beside it. An unknown code gets the generic line, never a guessed meaning. */
export function refusalKey(code: string): string {
  return isKnownRefusal(code) ? `refusals.${code}` : 'refusals.unknown'
}

/** What one answer to a publication request, reconcile or abandon means. */
export type PublicationOutcome =
  /** The intent reached a state that needs nothing more from this request. */
  | { kind: 'settled'; intent: PublicationIntent }
  /** Dispatched, and the host's result is not known. Reconcile, never resend. */
  | { kind: 'uncertain'; intent: PublicationIntent }
  /** The host refused the effect; the 409 body is the intent with its reason. */
  | { kind: 'rejected'; intent: PublicationIntent }
  /** The engine refused before dispatch, with a code and sometimes the intent it names. */
  | { kind: 'refused'; code: string; status: number; intentId?: string }
  /** No answer arrived. The request may have been recorded: look, do not resend. */
  | { kind: 'unknown' }

const UNCERTAIN: readonly IntentState[] = ['uncertain', 'dispatching']

function isIntent(body: unknown): body is PublicationIntent {
  return (
    typeof body === 'object' &&
    body !== null &&
    typeof (body as PublicationIntent).id === 'string' &&
    typeof (body as PublicationIntent).state === 'string'
  )
}

/** outcomeOf reads a settled mutation: its data, or the error it failed with. */
export function outcomeOf(
  settled: { data: PublicationIntent } | { error: unknown },
): PublicationOutcome {
  if ('data' in settled) {
    const intent = settled.data
    const answer = intent.answer ?? intent.state
    if (UNCERTAIN.includes(answer)) return { kind: 'uncertain', intent }
    if (answer === 'rejected') return { kind: 'rejected', intent }
    return { kind: 'settled', intent }
  }
  const error = settled.error
  if (error instanceof ApiError) {
    const body = error.body
    if (isIntent(body) && body.state === 'rejected') {
      return { kind: 'rejected', intent: body }
    }
    const flat =
      typeof body === 'object' && body !== null
        ? (body as { error?: unknown; intent_id?: unknown })
        : {}
    const code = typeof flat.error === 'string' ? flat.error : error.code
    return typeof flat.intent_id === 'string' && flat.intent_id !== ''
      ? {
          kind: 'refused',
          code,
          status: error.status,
          intentId: flat.intent_id,
        }
      : { kind: 'refused', code, status: error.status }
  }
  if (error instanceof NetworkError) return { kind: 'unknown' }
  // Anything else never reached the engine as far as this console can tell; saying
  // "unknown" is the claim that stays true either way.
  return { kind: 'unknown' }
}

/** The actions an engine route requires, as it mounts them (routes.go APIRoutes and the
 * in-handler admissions). `aal3` is the step-up floor the sealed door enforces. */
export type GovernedAction =
  | PublicationEffect
  | 'target'
  | 'reconcile_push'
  | 'reconcile_pull_request'
  | 'reconcile_merge'
  | 'abandon'

export interface ActionAuthority {
  permission: string
  aal3: boolean
}

const EFFECT_AUTHORITY: Record<PublicationEffect, ActionAuthority> = {
  push: { permission: 'gitpublish:push:write', aal3: false },
  pull_request: { permission: 'gitpublish:pull_request:write', aal3: false },
  merge: { permission: 'gitpublish:merge:admin', aal3: true },
}

const TARGET_ADMIN: ActionAuthority = {
  permission: 'gitpublish:target:admin',
  aal3: true,
}

export function actionAuthority(action: GovernedAction): ActionAuthority {
  switch (action) {
    case 'target':
    case 'abandon':
      return TARGET_ADMIN
    // Reconcile is authorized with the intent's OWN effect permission (admitIntent).
    case 'reconcile_push':
      return { ...EFFECT_AUTHORITY.push, aal3: false }
    case 'reconcile_pull_request':
      return { ...EFFECT_AUTHORITY.pull_request, aal3: false }
    case 'reconcile_merge':
      return { ...EFFECT_AUTHORITY.merge, aal3: false }
    default:
      return EFFECT_AUTHORITY[action]
  }
}

export function reconcileAction(effect: PublicationEffect): GovernedAction {
  return `reconcile_${effect}` as GovernedAction
}

/** The follow-ups an intent offers. Reconcile only while uncertain (on any other state the
 * engine answers the stored intent unchanged); abandon while the intent still holds its
 * scope unresolved (Abandon in reconcile.go). No state offers a resend. */
export function intentActions(
  intent: PublicationIntent,
  can: (permission: string) => boolean,
): { reconcile: boolean; abandon: boolean } {
  const reconcile =
    intent.state === 'uncertain' &&
    can(actionAuthority(reconcileAction(intent.effect)).permission)
  const abandon =
    (['uncertain', 'dispatching', 'not_dispatched'] as IntentState[]).includes(
      intent.state,
    ) && can(actionAuthority('abandon').permission)
  return { reconcile, abandon }
}

/** A fresh operation id for one publication decision. The engine replays a repeated
 * operation id with the same request instead of dispatching again (A2 in publish.go), so
 * the id is minted per decision and never per click. Pattern: ^[A-Za-z0-9._:-]{1,128}$. */
export function newOperationId(): string {
  const random =
    typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
      ? crypto.randomUUID()
      : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
  return `console:${random}`
}

/** Badge tone for an intent state. `uncertain` is a warning, never a failure. */
export function stateTone(
  state: IntentState,
): 'success' | 'warning' | 'danger' | 'neutral' {
  switch (state) {
    case 'applied':
    case 'adopted':
      return 'success'
    case 'uncertain':
    case 'dispatching':
      return 'warning'
    case 'rejected':
      return 'danger'
    default:
      return 'neutral'
  }
}

/** Merge bases are entered one per line or comma-separated; empties are dropped. */
export function parseMergeBases(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean)
}
