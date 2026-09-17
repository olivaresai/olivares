// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ApiError, NetworkError } from '@/lib/api/errors'

/**
 * What the operator must be told when a K3 call does not succeed — SEVEN answers,
 * because collapsing any two of them makes an operator do the one thing they must
 * not (re-send a stale Ack, retry a refused send, read "could not look" as "empty").
 *
 *  - forbidden        403: the engine refused THIS principal NOW. Not red.
 *  - step_up          403 step_up_required: the role permits it; the session must be elevated.
 *  - not_found        404: absent, or concealed (the engine hides local denial as 404).
 *  - unavailable      503 / verdict UNKNOWN: the engine COULD NOT LOOK. Neither empty nor refused.
 *  - version_mismatch 412 version_mismatch: the row moved since it was read. Re-read, reconfirm.
 *  - version_required 428: no If-Match was sent on a CAS route — the console's own defect.
 *  - plan_changed     412 plan_changed: the publication would not reproduce the bound plan.
 *  - conflict         409 without a code: the row moved or the command no longer applies.
 *                     MEASURED on the served engine (2026-09-06): a stale If-Match on the
 *                     Ack answers 409 {"error":{"message":"conflict"}} — the Ack's
 *                     version_mismatch, already_acknowledged and idempotency_key_reused
 *                     all wrap store.ErrConflict (modules/sessions/communication_ack_service.go)
 *                     and only the inbox cursor's mismatch reaches 412. The reaction is
 *                     the same as a 412: the intent dies, nothing is re-sent, re-read.
 *  - snapshot_changed 409 channel_snapshot_changed: the Channel's version or ACL revision
 *                     moved BETWEEN TWO PAGES of one paginated grant history. Its remedy is
 *                     specific — discard every page collected so far and start again with
 *                     no continuation — and the engine names it apart from `terminal` and
 *                     from the bare store conflict so a listing is never restarted for a
 *                     lost CAS, nor a lost CAS re-sent for a moved listing.
 *  - terminal         409 terminal: the row is in a terminal state; the command cannot apply.
 *  - invalid          400: the body or query was refused as malformed.
 *  - rate_limited     429.
 *  - ambiguous        the transport failed: whether the write happened is UNDETERMINED.
 *  - aborted          the console cancelled its own request (boundary moved, permission lost).
 *  - other            anything else, shown with its code.
 */
export type FailureKind =
  | 'forbidden'
  | 'step_up'
  | 'not_found'
  | 'unavailable'
  | 'version_mismatch'
  | 'version_required'
  | 'plan_changed'
  | 'conflict'
  | 'snapshot_changed'
  | 'terminal'
  | 'invalid'
  | 'rate_limited'
  | 'ambiguous'
  | 'aborted'
  | 'other'

export interface Failure {
  kind: FailureKind
  status?: number
  code?: string
  verdict?: string
  message?: string
  requestId?: string
}

type CommunicationErrorBody = { verdict?: unknown; code?: unknown }

function verdictOf(err: ApiError): string | undefined {
  const v = (err.body as CommunicationErrorBody | undefined)?.verdict
  return typeof v === 'string' ? v : undefined
}

/**
 * The engine's code when it sent one. The module family's bare envelope
 * (`{"error":{"message":"conflict"}}`) carries none, and the shared client then
 * substitutes `internal` as a placeholder; that placeholder is not shown as if the
 * engine had said it, so the operator reads the status and the server's sentence.
 */
function codeOf(err: ApiError): string | undefined {
  const c = (err.body as CommunicationErrorBody | undefined)?.code
  if (typeof c === 'string' && c !== '') return c
  return err.code === 'internal' ? undefined : err.code
}

export function isAbortError(err: unknown): boolean {
  return (
    (typeof DOMException !== 'undefined' &&
      err instanceof DOMException &&
      err.name === 'AbortError') ||
    (err instanceof Error && err.name === 'AbortError')
  )
}

/** An UNKNOWN verdict is "could not look" — never an empty result, never a refusal. */
export function isUnknownVerdict(err: unknown): boolean {
  if (!(err instanceof ApiError)) return false
  const v = verdictOf(err)
  return (
    v === 'UNKNOWN' ||
    v === 'NO_HE_PODIDO_MIRAR' ||
    codeOf(err) === 'evidence_unavailable'
  )
}

export function classifyFailure(err: unknown): Failure {
  if (isAbortError(err)) return { kind: 'aborted' }
  if (err instanceof NetworkError) {
    return { kind: 'ambiguous', message: err.message }
  }
  if (!(err instanceof ApiError)) {
    return {
      kind: 'other',
      message: err instanceof Error ? err.message : String(err),
    }
  }
  const base: Failure = {
    kind: 'other',
    status: err.status,
    code: codeOf(err),
    verdict: verdictOf(err),
    message: err.message,
    requestId: err.requestId,
  }
  // The CODE first: it is the contract. The status only where the code alone would
  // be ambiguous, so a future code sharing a status is not absorbed silently.
  if (err.isStepUpRequired) return { ...base, kind: 'step_up' }
  if (isUnknownVerdict(err) || err.status === 503) {
    return { ...base, kind: 'unavailable' }
  }
  if (base.code === 'plan_changed') return { ...base, kind: 'plan_changed' }
  if (base.code === 'version_mismatch') {
    return { ...base, kind: 'version_mismatch' }
  }
  if (base.code === 'version_required') {
    return { ...base, kind: 'version_required' }
  }
  if (err.status === 403) return { ...base, kind: 'forbidden' }
  if (err.status === 404) return { ...base, kind: 'not_found' }
  if (err.status === 412) return { ...base, kind: 'version_mismatch' }
  if (err.status === 428) return { ...base, kind: 'version_required' }
  if (base.code === 'terminal') return { ...base, kind: 'terminal' }
  if (base.code === 'channel_snapshot_changed') {
    return { ...base, kind: 'snapshot_changed' }
  }
  if (err.status === 409) return { ...base, kind: 'conflict' }
  if (err.status === 400) return { ...base, kind: 'invalid' }
  if (err.status === 429) return { ...base, kind: 'rate_limited' }
  return base
}
