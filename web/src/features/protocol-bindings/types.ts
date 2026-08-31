// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/** Hand-written mirror of modules/sessions/communication_binding_{model,api}.go.
 * The beta module routes are intentionally absent from the stable generated client. */

export type BindingProtocol = 'a2a' | 'mcp'
export type BindingDirection = 'inbound' | 'outbound' | 'bidirectional'
export type BindingLocalKind = 'work_item' | 'agent' | 'model' | 'channel'
export type BindingCurrencyPolicy = 'pinned'
export type ProtocolBindingSpecState =
  'draft' | 'active' | 'disabled' | 'superseded'
export type ProtocolObservationVerdict = 'CLEAN' | 'BROKEN' | 'UNKNOWN'
export type AssessmentVerdict = 'LIMPIO' | 'ROTO' | 'NO_HE_PODIDO_MIRAR'
export type ProtocolSpecMode = 'validate' | 'plan' | 'apply'
export type ProtocolReconcileMode = ProtocolSpecMode | 'test'
export type ProtocolMappingCardinality =
  'one_to_one' | 'one_to_many' | 'many_to_one'
export type ProtocolMappingTransform =
  'identity' | 'text' | 'reference' | 'metadata' | 'status'

export interface ProtocolMappingRule {
  source: string
  target: string
  cardinality: ProtocolMappingCardinality
  transform: ProtocolMappingTransform
}

export interface ProtocolBindingLoss {
  field: string
  reason_code: string
  accepted: boolean
  acceptance_ref?: string
}

export interface ProtocolBindingValidation {
  verdict: ProtocolObservationVerdict
  code: string
  observed_at?: string
}

export interface ProtocolBindingSpecInput {
  workspace_id: string
  binding_key: string
  generation: number
  protocol: BindingProtocol
  protocol_version: string
  direction: BindingDirection
  local_kind: BindingLocalKind
  local_selector: Record<string, unknown>
  peer_authority: string
  remote_resource_kind: string
  remote_resource_ref: string
  mapping_schema: string
  mapping: ProtocolMappingRule[]
  known_losses: ProtocolBindingLoss[]
  rule_refs: string[]
  permission_profile_ref: string
  currency_policy: BindingCurrencyPolicy
  supersedes_id?: string
}

export interface CommunicationEntity {
  id: string
  tenant_id: string
  workspace_id: string
  version: number
  created_at: string
  updated_at: string
}

export interface ProtocolBindingSpec extends CommunicationEntity {
  binding_key: string
  generation: number
  protocol: BindingProtocol
  protocol_version: string
  direction: BindingDirection
  local_kind: BindingLocalKind
  local_selector: Record<string, unknown>
  peer_authority: string
  remote_resource_kind: string
  remote_resource_ref: string
  mapping_schema: string
  mapping: ProtocolMappingRule[]
  mapping_hash: string
  known_losses: ProtocolBindingLoss[]
  losses_hash: string
  rule_refs: string[]
  permission_profile_ref: string
  currency_policy: BindingCurrencyPolicy
  validation: ProtocolBindingValidation
  state: ProtocolBindingSpecState
  supersedes_id?: string
  spec_hash: string
  plan_hash: string
}

export type ProtocolBindingSpecOperation = 'draft' | 'activate' | 'disable'

export interface ProtocolBindingSpecPlan {
  verdict: ProtocolObservationVerdict
  code: string
  /** Server-derived capability witness. It is never accepted from create input. */
  validation: ProtocolBindingValidation
  plan_hash: string
  operation: ProtocolBindingSpecOperation
  workspace_id: string
  spec_id?: string
  generation: number
  prior_active_id?: string
  spec_hash: string
  mapping_hash: string
  losses_hash: string
}

export interface ProtocolBindingSpecResult extends ProtocolBindingSpecPlan {
  spec: ProtocolBindingSpec
  replayed?: boolean
}

export interface ProtocolBinding extends CommunicationEntity {
  binding_spec_id: string
  binding_spec_generation: number
  pinned_spec_hash: string
  pinned_mapping_hash: string
  pinned_losses_hash: string
  work_item_id?: string
  message_id?: string
  delivery_id?: string
  protocol: BindingProtocol
  protocol_version: string
  direction: BindingDirection
  peer_authority: string
  remote_resource_ref: string
  attempt_id: string
  generation: number
  synthetic_sid: string
  owner_kind?: string
  owner_ref?: string
  owner_digest?: string
  owner_epoch?: number
  lease_fence?: number
  external_kind: string
  external_id?: string
  context_id?: string
  external_message_id?: string
  local_state: string
  remote_state: string
  remote_revision?: string
  observation_verdict: ProtocolObservationVerdict
  observation_code: string
  last_observed_at?: string
  detail_hash?: string
  current_ttl_ms?: number
  current_poll_interval_ms?: number
  terminal: boolean
  cancel_requested: boolean
  cancel_requested_at?: string
  cancel_reason_code?: string
  mcp_task?: unknown
  mcp_task_hash?: string
  protocol_metadata_json?: unknown
  last_command_id: string
  last_event_id: string
  last_event_seq: number
}

export interface ProtocolPage<T> {
  items: T[]
  next_cursor?: string
  has_more: boolean
}

export interface WorkCheck {
  name: string
  verdict: AssessmentVerdict
  evidence_ref?: string
}

export interface ProtocolBindingAssessment {
  verdict: AssessmentVerdict
  code: string
  observed_at: string
  checks: WorkCheck[]
  plan_hash: string
  resource?: ProtocolBinding
}

export interface ProtocolBindingReconcilePlan extends ProtocolBindingAssessment {
  command: string
  expected_etag?: string
  row_effects: string[]
  event_type: string
  event_types?: string[]
  audit_action: string
  permission: string
  external_calls: string[]
}

export interface ProtocolWriteMeta {
  etag: string | null
  replayed: boolean
}

export interface ProtocolSpecApplyOutcome extends ProtocolWriteMeta {
  result: ProtocolBindingSpecResult
}

export interface ProtocolReconcileOutcome extends ProtocolWriteMeta {
  assessment: ProtocolBindingAssessment
}

export interface ListProtocolBindingSpecsParams {
  workspace_id: string
  binding_key?: string
  generation?: number
  protocol?: BindingProtocol
  direction?: BindingDirection
  local_kind?: BindingLocalKind
  peer_authority?: string
  state?: ProtocolBindingSpecState
  limit?: number
  cursor?: string
}

export interface ListProtocolBindingsParams {
  workspace_id: string
  binding_spec_id?: string
  work_item_id?: string
  protocol?: BindingProtocol
  peer_authority?: string
  owner_kind?: string
  owner_ref?: string
  external_kind?: string
  external_id?: string
  verdict?: ProtocolObservationVerdict
  terminal?: boolean
  limit?: number
  cursor?: string
}

/** UI-only draft. It deliberately has no validation witness field: the browser is
 * never an authority for CLEAN, BROKEN, or UNKNOWN. The engine derives it. */
export interface ProtocolComposerDraft {
  bindingKey: string
  generation: number
  supersedesId: string
  protocol: BindingProtocol
  protocolVersion: string
  direction: BindingDirection
  peerAuthority: string
  localKind: BindingLocalKind
  localRef: string
  remoteResourceKind: string
  remoteResourceRef: string
  mappingSchema: string
  mapping: ProtocolMappingRule[]
  knownLosses: ProtocolBindingLoss[]
  ruleRefsText: string
  permissionProfileRef: string
}
