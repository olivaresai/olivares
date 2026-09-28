// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Governed Git publication (modules/gitpublish, CONTRACT-J10-S3). The shapes mirror the
// module's DTOs in routes.go field for field. Three records of one intent are kept apart on
// the wire and here: what was REQUESTED, what the host was last OBSERVED to hold, and
// whether the host ACKNOWLEDGED our request. None of them implies the others.

/** GET /targets, GET /targets/{id}. The binding ids are present only for a caller who
 * holds target administration on the target; the list never carries them. */
export interface PublicationTarget {
  id: string
  workspace_id: string
  credential_binding_id?: string
  repository_binding_id?: string
  push_prefix: string
  merge_bases: string[]
  version: number
}

/** POST /targets. The engine decodes strictly: any other field is field_not_accepted. */
export interface CreateTargetInput {
  workspace_id: string
  credential_binding_id: string
  repository_binding_id: string
  push_prefix: string
  merge_bases: string[]
}

/** PUT /targets/{id}. workspace_id is refused; an empty binding id keeps the current one. */
export interface UpdateTargetInput {
  expected_version: number
  credential_binding_id: string
  repository_binding_id: string
  push_prefix: string
  merge_bases: string[]
}

export type PublicationEffect = 'push' | 'pull_request' | 'merge'

/** Intent states (§3.14). `dispatching` is also the ANSWER a replay gets when it does not
 * own the dispatch. */
export type IntentState =
  | 'dispatching'
  | 'applied'
  | 'adopted'
  | 'rejected'
  | 'not_dispatched'
  | 'uncertain'
  | 'abandoned'

/** Whether OUR request caused the effect, or an existing matching effect was adopted. */
export type ReceiptKind = 'caused' | 'existing_effect' | 'none'

export interface IntentRequested {
  ref?: string
  expected_old?: string
  commit?: string
  tree?: string
  head_ref?: string
  base?: string
  number?: number
  expected_head?: string
  method?: string
  title?: string
}

export interface IntentObserved {
  present: boolean
  sha?: string
  head_sha?: string
  number?: number
  merged: boolean
  merge_commit_sha?: string
  merge_tree?: string
  source?: string
  at?: string
}

export interface IntentAcknowledged {
  acknowledged: boolean
  status?: number
  request_id?: string
  at?: string
}

/** One publication effect and its receipt. `answer` is present only when this call's
 * answer differs from the stored state. */
export interface PublicationIntent {
  id: string
  target_id: string
  target_version: number
  effect: PublicationEffect
  operation_id: string
  attempt: number
  state: IntentState
  answer?: IntentState
  reason?: string
  receipt: ReceiptKind
  requested: IntentRequested
  observed: IntentObserved
  /** Only for an applied pull request: whether its head matches the requested commit. */
  content_match?: boolean
  acknowledged: IntentAcknowledged
  authorized_by?: string
  dispatch_deadline?: string
  release_failure?: string
}

/** One appended host read, dispatcher outcome or refusal. */
export interface PublicationObservation {
  attempt: number
  source: string
  result: string
  host_object?: string
  status?: number
  request_id?: string
  at: string
}

export interface PushInput {
  operation_id: string
  ref: string
  expected_old: string
  commit: string
  tree: string
  acknowledge_intent: string
}

export interface PullRequestInput {
  operation_id: string
  head_ref: string
  base: string
  commit: string
  title: string
  body: string
  draft: boolean
  acknowledge_intent: string
}

/** POST /targets/{id}/merges. expected_result_tree and expected_base exist on the wire but
 * are unsupported_requirement, so the console never sends them. */
export interface MergeInput {
  operation_id: string
  number: number
  expected_head: string
  method: 'merge' | 'squash' | 'rebase'
  acknowledge_intent: string
}

/** The module's list envelope: `{ items }`, with no cursor. */
export interface ItemsResponse<T> {
  items: T[]
}
