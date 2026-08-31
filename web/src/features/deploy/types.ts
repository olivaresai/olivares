// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for the deployment & integration module (VII) — mirror the Go DTOs in
// modules/deploy/*.go 1:1 (snake_case JSON tags). The web is a thin client
// (ARCHITECTURE.md): these are the exact shapes the engine returns/accepts at
// /v1/m/deploy. CRITICAL invariants encoded here: env_refs/wirings carry secret
// REFERENCES (<scheme>:<locator>), never values; gate_status is a REPORTED decision
// to DISPLAY (never inferred from client-side); wiring attribution 'degraded' is an
// honesty/provenance signal, never an enforcement input.

/** The subject of a deployment — an agent or an MCP server. */
export type SubjectKind = 'agent' | 'mcp_server' | (string & {})

/** Desired lifecycle of a definition. */
export type DesiredStatus = 'active' | 'retired' | (string & {})

/** Status of a real-state (core Deployment) snapshot. */
export type RealStatus = 'active' | 'drifted' | 'retired' | (string & {})

/** Mode an agent uses to reach a resource. */
export type WiringMode = 'read' | 'write' | 'readwrite' | (string & {})

/** Lifecycle status of a declared wiring. */
export type WiringStatus = 'declared' | 'applied' | 'revoked' | (string & {})

/**
 * Per-agent attribution provenance of a wiring. `firm` = NHI identity firmly bound;
 * `degraded` = per-agent identity could NOT be firmly bound, so attribution is
 * approximate. This is an HONESTY signal, never a failure and never an enforcement
 * input.
 */
export type Attribution = 'firm' | 'degraded' | (string & {})

/** The reconciliation op recorded in the ledger. */
export type OpKind = 'plan' | 'apply' | 'verify' | 'retire' | (string & {})

/** Outcome of a ledger operation. */
export type OpStatus =
  | 'planned'
  | 'requested'
  | 'blocked'
  | 'applied'
  | 'verified'
  | 'retired'
  | 'failed'
  | 'noop'
  | (string & {})

/**
 * A REPORTED governance gate decision — DISPLAY ONLY. The UI must never infer
 * authorization from this; enforcement is server-side deny-by-default (403 blocked).
 */
export type GateStatus =
  | 'approved'
  | 'pending'
  | 'rejected'
  | 'expired'
  | 'no_gate'
  | 'not_required'
  | (string & {})

/** The kind of change a plan/verify diff item represents. */
export type ChangeKind = 'create' | 'update' | 'delete' | 'noop' | (string & {})

/** An allow-listed secret-store scheme for a <scheme>:<locator> reference. */
export type SecretScheme =
  | 'vault'
  | 'infisical'
  | 'aws-secretsmanager'
  | 'gcp-secretmanager'
  | 'azure-keyvault'
  | 'k8s-secret'
  | 'env'
  | 'file'
  | (string & {})

export const SUBJECT_KINDS: SubjectKind[] = ['agent', 'mcp_server']
export const WIRING_MODES: WiringMode[] = ['read', 'write', 'readwrite']
export const SECRET_SCHEMES: SecretScheme[] = [
  'vault',
  'infisical',
  'aws-secretsmanager',
  'gcp-secretmanager',
  'azure-keyvault',
  'k8s-secret',
  'env',
  'file',
]

/** An env value provided BY a secret-store reference — never the value. */
export interface EnvRef {
  name: string
  /** SECRET REFERENCE (<scheme>:<locator>), never the credential value. */
  secret_ref: string
}

/** A declared PERMITTED agent→resource connection inside a spec. */
export interface WiringSpec {
  resource_kind: string
  resource_ref: string
  mode: WiringMode
  /** SECRET REFERENCE the agent uses to reach the resource, never the value. */
  secret_ref?: string
}

/** Per-agent NHI identity intent. */
export interface IdentitySpec {
  /** Directory/NHI reference the agent runs as. */
  identity_ref?: string
  /** Provision a fresh per-agent NHI instead of binding an existing one. */
  mint?: boolean
}

/** The typed desired-state spec. Carries references only — never secret values. */
export interface DeploySpec {
  /** Container image / artifact ref (non-sensitive). */
  image?: string
  /** Entrypoint override (non-sensitive, no secrets). */
  command?: string
  /** Desired replica count (0 = single/default). */
  replicas?: number
  /** Non-sensitive compute requests, e.g. {cpu, mem}. */
  resources?: Record<string, string>
  /** Env values BY secret-store reference. */
  env_refs?: EnvRef[]
  /** Declared PERMITTED agent→resource connections. */
  wirings?: WiringSpec[]
  /** Per-agent NHI identity intent. */
  identity?: IdentitySpec | null
}

/** The applied real-state snapshot — present only when a core Deployment is linked. */
export interface RealStateDTO {
  status: RealStatus
  /** Applied version, as a string. */
  version?: string
  deployed_at?: string
}

/** A deployment definition (desired-state record). */
export interface DefinitionDTO {
  id: string
  subject_kind: SubjectKind
  subject_ref: string
  name: string
  environment: string
  target: string
  runtime: string
  desired_status: DesiredStatus
  current_version: number
  /** Reconciled-to-infra revision (0 = never applied). */
  applied_version: number
  /** Hex SHA-256 of the canonical spec. */
  spec_hash?: string
  /** GitOps provenance, e.g. git:repo#commit. */
  source_ref?: string
  /** applied_version == current_version (always present). */
  up_to_date: boolean
  /** Applied-state snapshot — single-get only, when a core Deployment is linked. */
  real?: RealStateDTO | null
  /** Current desired spec — single-get / create / update responses only. */
  spec?: DeploySpec | null
}

/** The write payload for declaring a NEW definition (POST /definitions). */
export interface DefinitionCreateInput {
  subject_kind: SubjectKind
  subject_ref: string
  name: string
  environment: string
  target: string
  runtime?: string
  source_ref?: string
  spec: DeploySpec
}

/** The write payload for re-declaring desired state (PUT /definitions/{id}). */
export interface DefinitionUpdateInput {
  target?: string
  source_ref?: string
  note?: string
  spec: DeploySpec
}

/** One immutable revision (a rollback target). */
export interface RevisionDTO {
  version: number
  spec_hash: string
  source_ref?: string
  note?: string
  created_by?: string
  created_at?: string
  /** NOT populated in the revisions list endpoint (withSpec=false). */
  spec?: DeploySpec | null
}

/** A materialized PERMITTED agent→resource connection. */
export interface WiringDTO {
  definition_id: string
  agent_ref: string
  /** Bound NHI identity (firm) or fallback ref. */
  identity_ref?: string
  resource_kind: string
  resource_ref: string
  mode: WiringMode
  /** SECRET REFERENCE only, never the value. */
  secret_ref?: string
  status: WiringStatus
  /** firm | degraded — provenance honesty signal, never an enforcement input. */
  attribution: Attribution
  /** Revision number the wiring was materialized at. */
  version: number
}

/** One row of the append-only change-management ledger. */
export interface OperationDTO {
  definition_id: string
  op: OpKind
  from_version: number
  to_version: number
  plan_hash?: string
  /** Governance approval reference (who approved). */
  approval_ref?: string
  gate_status?: GateStatus
  status: OpStatus
  /** Principal that ran the op. */
  actor?: string
  /** Short non-sensitive outcome summary. */
  result?: string
  occurred_at?: string
}

/** A single diff item from a plan / verify. */
export interface Change {
  kind: ChangeKind
  /** What changes, e.g. container | deployment | wiring. */
  resource: string
  detail?: string
}

/** The dry-run plan response (POST /plan). */
export interface PlanResponse {
  /** Anti-TOCTOU hash bound to (definition|from|to|spec_hash). */
  plan_hash: string
  from_version: number
  to_version: number
  /** true when changes is empty. */
  up_to_date: boolean
  changes: Change[]
}

/** The verify response — an INLINE map, NOT a named DTO server-side. */
export interface VerifyResponse {
  in_sync: boolean
  drift: Change[]
}

/** The apply / retire response (two-phase HITL). */
export interface MutationResponse {
  op: 'apply' | 'retire' | (string & {})
  plan_hash: string
  /** Target version (0 for retire). */
  version: number
  status: OpStatus
  /** true when a HITL approval is needed/awaited. */
  requires_approval: boolean
  /** Approval handle to present on phase 2. */
  approval_ref?: string
  gate_status?: GateStatus
  /** Diff returned on a phase-1 apply. */
  changes?: Change[]
  /** Count of materialized wirings (on applied). */
  wirings?: number
  /** Short non-sensitive detail / denial reason. */
  detail?: string
}

/** The apply / retire request body (two-phase). Phase 1 omits approval_ref. */
export interface MutationInput {
  /** Absent/empty = phase 1 (request); present = phase 2 (execute). */
  approval_ref?: string
}

/** The rollback request body (POST /rollback). */
export interface RollbackInput {
  to_version: number
  note?: string
}
