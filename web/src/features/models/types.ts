// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for module X (models/providers), mirroring the UI data contract. Pricing
// in the catalog is DECLARED reference (list defaults, dated `as_of`) — editable,
// not immutable truth. Key references carry only a masked `hint` — NEVER a usable
// credential (docs/SECURITY-HARDENING.md): there is no field that can hold the secret.

export type ModelCapability =
  | 'streaming'
  | 'tool_use'
  | 'vision'
  | 'pdf'
  | 'structured_outputs'
  | 'prompt_caching'
  | 'batch'
  | 'files'
  | 'extended_thinking'
  | 'computer_use'
  | 'memory_tool'
  | 'context_management'
  | 'citations'
  | (string & {})

export interface CatalogPricing {
  input_per_mtok_usd: number
  output_per_mtok_usd: number
  /** Standard (5-minute TTL) cache-write rate (~1.25× base input). */
  cache_write_per_mtok_usd?: number
  /** Distinct 1-hour TTL cache-write rate (~2× base input) — modules/models/dto.go
   *  `cache_write_1h_per_mtok_usd`. A separate tier, never the same as the 5m rate. */
  cache_write_1h_per_mtok_usd?: number
  cache_read_per_mtok_usd?: number
  currency: string
  as_of: string
  source: 'list' | 'api' | 'operator' | string
}

export interface CatalogModel {
  family: string
  provider_ref: string
  capabilities: ModelCapability[]
  /** true = the capability set is NOT yet verified against a model card (e.g. a
   *  freshly-announced preview). The catalog lists the model but flags its caps as
   *  unconfirmed rather than inventing them — modules/models/dto.go `caps_to_confirm`. */
  caps_to_confirm?: boolean
  context_window: number
  max_output_tokens: number
  modality: string
  /** null when the family has no declared price. */
  pricing: CatalogPricing | null
  /** Billing tiers the family can run on (e.g. standard, batch, priority,
   *  priority_on_demand, flex, flex_discount). Empty/absent = not declared.
   *  modules/models/dto.go `service_tier_eligibility`. */
  service_tier_eligibility?: string[]
  /** inference_geo regions the family supports (global, us). Empty/absent = the
   *  dimension is not applicable (models before Feb 2026 report not_available).
   *  modules/models/dto.go `data_residency`. */
  data_residency?: string[]
  /** Price multiplier for inference_geo="us" (1.1 on Opus 4.6 / Sonnet 4.6+; absent
   *  or 0 = no US-residency premium / not applicable). modules/models/dto.go
   *  `us_inference_burndown_mult`. */
  us_inference_burndown_mult?: number
}

export interface CatalogResponse {
  models: CatalogModel[]
  /** The full capability vocabulary, in UI order. */
  capabilities: ModelCapability[]
  pricing_as_of: string
  pricing_note: string
}

/** A model in the governed estate (enriched from the catalog). */
export interface GovernedModel {
  id: string
  name: string
  provider_id: string
  provider: string
  family: string
  context_window: number
  /** µUSD per token (integer, coarse — the authoritative cost is the FinOps ledger). */
  input_cost_micro_usd: number
  output_cost_micro_usd: number
  modality: string
  status: string
  capabilities: ModelCapability[]
  /** false = no declared family yet (no price). */
  enriched: boolean
}

export type RoutingStrategy = 'cost' | 'latency' | 'capability' | 'pinned'

export interface RoutingPolicy {
  id: string
  name: string
  enabled: boolean
  strategy: RoutingStrategy
  required_capabilities: ModelCapability[]
  preferred_providers: string[]
  min_context_window: number
  /** Only under strategy=pinned. */
  pinned_model: string
  allow_deprecated: boolean
  /** If set, resolved targets are marked via_gateway. */
  gateway_endpoint: string
}

export interface RoutingPolicyInput {
  name: string
  enabled?: boolean
  strategy: RoutingStrategy
  required_capabilities?: ModelCapability[]
  preferred_providers?: string[]
  min_context_window?: number
  pinned_model?: string
  allow_deprecated?: boolean
  gateway_endpoint?: string
}

export interface DecisionTarget {
  provider_ref: string
  model_ref: string
  via_gateway?: boolean
  endpoint?: string
}

/** Result of POST /routing-policies/{id}/resolve. */
export interface Decision {
  resolved: boolean
  policy: string
  primary?: DecisionTarget
  /** Always arrays (never null). */
  fallbacks: DecisionTarget[]
  chain: DecisionTarget[]
  reason: string
}

/** A governed reference to a provider API key / workspace — MINIMAL DATA. */
export interface KeyRef {
  id: string
  ref_kind: 'api_key' | 'workspace' | string
  provider_ref: string
  ext_id: string
  name: string
  workspace_ref: string
  status: string
  /** ONLY the masked partial — NEVER the secret (docs/SECURITY-HARDENING.md).*/
  hint: string
  owner_ref: string
  created_at: string
}

export interface KeyRefInput {
  ref_kind: string
  provider_ref: string
  ext_id: string
  name: string
  workspace_ref?: string
  owner_ref?: string
}

// --- GPAI supplier posture -------------------------------------------------

/** One operator-attested GPAI posture per provider. Every claim boolean describes
 *  what the operator recorded; it is not a compliance determination by Olivares. */
export interface GpaiPosture {
  id: string
  provider_ref: string
  cop_signatory: boolean
  technical_docs: boolean
  training_data_summary: boolean
  copyright_policy: boolean
  downstream_info: boolean
  systemic_risk: boolean
  safety_report: boolean
  verified: boolean
  verification_method?: string
  attested_by: string
  attested_at: string
  note?: string
}

/** Writable GPAI posture fields. Attestation identity/time are always server-set. */
export interface GpaiPostureInput {
  provider_ref: string
  cop_signatory: boolean
  technical_docs: boolean
  training_data_summary: boolean
  copyright_policy: boolean
  downstream_info: boolean
  systemic_risk: boolean
  safety_report: boolean
  verified: boolean
  verification_method?: string
  note?: string
}

// --- Claude model-access governance ----------------------------------
// "Which users/groups/agent-groups may use which Claude models (or model-groups)
// in which workspaces, on which surface, under which budget." model-groups are
// admin-defined named sets (hybrid: explicit refs + catalog selectors); grants are
// positive allow-list rows (subject-scoped, deny-closed). See
// the Claude model-governance contract.

/** A tenant-named set of models. Membership is HYBRID: a model belongs to the group
 *  if it is an explicit member ref (exact/prefix), or its declared family is in
 *  `family_selectors`, or its access tier is in `tier_selectors`. */
export interface ModelGroup {
  id: string
  /** Immutable on update (it is the reference grants resolve). */
  name: string
  member_refs: string[]
  family_selectors: string[]
  tier_selectors: string[]
  description?: string
}

export interface ModelGroupInput {
  name: string
  member_refs?: string[]
  family_selectors?: string[]
  tier_selectors?: string[]
  description?: string
}

/** The deployment surface a grant may restrict to (model.Gateway, open string). */
export type ModelSurface =
  | 'direct'
  | 'bedrock-mantle'
  | 'bedrock-legacy'
  | 'vertex'
  | 'foundry'
  | 'claude-platform-aws'
  | (string & {})

export type ModelAccessSubjectKind = 'user' | 'role' | 'agent_group'
export type ModelAccessTargetKind = 'model' | 'model_group'

/** A positive model-access grant: SUBJECT may use TARGET in WORKSPACE on SURFACES,
 *  optionally referencing a budget. A subject named by no grant is unrestricted; one
 *  named by any grant is confined to its grants (deny-closed). */
export interface ModelAccessGrant {
  id: string
  subject_kind: ModelAccessSubjectKind
  subject_ref: string
  target_kind: ModelAccessTargetKind
  target_ref: string
  /** "" / omitted = tenant-wide. */
  workspace_ref?: string
  /** [] / omitted = all surfaces. */
  surfaces: ModelSurface[]
  /**
   * ⛔ `allow` (defecto) | `forbid`, y el vacío ES un `allow` («empty ⇒ allow», back-compat,
   * `modules/models/modelgovernance.go:415`). No estaba en este tipo, así que la consola no podía
   * ni leer ni crear la regla que RESTA: un `forbid` **anula cualquier allow de los sujetos
   * que nombra** — forbid-overrides-allow, deny-closed (`:418-419`).
   */
  effect?: 'allow' | 'forbid'
  /** A finops budget id referenced for defense-in-depth (metadata). */
  budget_ref?: string
  description?: string
}

export interface ModelAccessInput {
  subject_kind: ModelAccessSubjectKind
  subject_ref: string
  target_kind: ModelAccessTargetKind
  target_ref: string
  workspace_ref?: string
  surfaces?: ModelSurface[]
  effect?: 'allow' | 'forbid'
  budget_ref?: string
  description?: string
}

// --- model operations (module XXIII — own-model registry, versions, local
// inference deployments, signed-model admission, AIBOM). RESPONSE and `…Input`
// (command) types are kept SEPARATE on purpose: the engine decodes writes with
// DisallowUnknownFields, so an Input carries ONLY the fields the handler accepts —
// never id/attested_by/attested_at/configured or any response-only field. Fields the
// Go DTO tags `omitempty` are optional here. Mirrors modules/models/{owned,admission,
// dataset,aibom}.go; there is no generated schema — this file is the hand-authored
// source of truth (the DTOs are NOT in the OpenAPI surface).

export type OwnedModelKind =
  'hosted' | 'fine_tuned' | 'imported' | (string & {})
export type OwnedModelStatus = 'active' | 'deprecated' | 'draft' | (string & {})
export type OwnedModelVisibility = 'private' | 'internal' | (string & {})

/** A governed own-model registry entry (owned.go `ownedModelDTO`). The aggregate
 *  root: versions, deployments and datasets reference it by `owned_ref`. */
export interface OwnedModel {
  id: string
  name: string
  kind: OwnedModelKind
  base_ref?: string
  provider_ref?: string
  visibility: OwnedModelVisibility
  status: OwnedModelStatus
  owner_ref?: string
  note?: string
}

export interface OwnedModelInput {
  name: string
  kind: OwnedModelKind
  base_ref?: string
  provider_ref?: string
  visibility?: OwnedModelVisibility
  status?: OwnedModelStatus
  owner_ref?: string
  note?: string
}

export type ModelVersionStatus =
  'draft' | 'active' | 'deprecated' | (string & {})

/** A model version (owned.go `modelVersionDTO`). IMMUTABLE once created — there is no
 *  update route; only create, delete and the admit verdict. */
export interface ModelVersion {
  id: string
  owned_ref: string
  version: string
  artifact_ref?: string
  status: ModelVersionStatus
  parent_ref?: string
  source_ref?: string
  note?: string
}

export interface ModelVersionInput {
  owned_ref: string
  version: string
  artifact_ref?: string
  status?: ModelVersionStatus
  parent_ref?: string
  source_ref?: string
  note?: string
}

export type DeploymentRuntime =
  'vllm' | 'ollama' | 'llamacpp' | 'other' | (string & {})
export type DeploymentStatus = 'active' | 'stopped' | (string & {})
/** The admission-gate discriminator (owned.go D-08). `local` serves a self-hosted
 *  owned model version and is admission-gated (requires owned_ref + version_ref); `brokered`
 *  calls a hosted provider (endpoint_ref) and is never gated; `unclassified` is the migration
 *  state for pre rows — read-only, deny-closed under require_signed. The console only
 *  ever WRITES local or brokered. */
export type DeploymentType =
  'local' | 'brokered' | 'unclassified' | (string & {})

/** A local inference deployment (owned.go `inferenceDeploymentDTO`). `governed` is a
 *  declared posture flag — NOT a synonym for admitted, and never an admission bypass.
 *  Create/update are subject to the deny-closed admission gate (HTTP 422). */
export interface InferenceDeployment {
  id: string
  name: string
  runtime: DeploymentRuntime
  deployment_type?: DeploymentType
  endpoint_ref?: string
  owned_ref?: string
  version_ref?: string
  status: DeploymentStatus
  governed: boolean
  note?: string
}

export interface InferenceDeploymentInput {
  name: string
  runtime: DeploymentRuntime
  deployment_type: DeploymentType
  endpoint_ref?: string
  owned_ref?: string
  version_ref?: string
  status?: DeploymentStatus
  governed?: boolean
  note?: string
}

/** The tenant signed-model admission policy (admission.go `admissionPolicyDTO`).
 *  The signing METHOD is derived from field presence, never authored: roots + identity/
 *  issuer → sigstore-keyless; roots only → certificate-pki; keys → bare-key; empty →
 *  admits nothing (deny-closed). `configured` appears ONLY on the unconfigured stub. */
export interface AdmissionPolicy {
  require_signed: boolean
  require_artifact_digests: boolean
  allowed_identities?: string[]
  allowed_issuers?: string[]
  trusted_keys?: string[]
  trusted_roots?: string[]
  note?: string
  attested_by?: string
  attested_at?: string
  /** Present ONLY on the unconfigured-default response (observe mode). Absent once a
   *  policy has been saved. Never send this back on PUT. */
  configured?: boolean
}

/** The write body for PUT /admission-policy. A DEDICATED command type — NEVER built by
 *  spreading the GET response, or `configured`/synthetic `note` would reach the
 *  fail-closed decoder and 400. */
export interface AdmissionPolicyInput {
  require_signed: boolean
  require_artifact_digests: boolean
  allowed_identities?: string[]
  allowed_issuers?: string[]
  trusted_keys?: string[]
  trusted_roots?: string[]
  note?: string
}

/** The derived trust mode of a policy. Canonical definition in `@/lib/admission/policy`,
 *  re-exported here so every existing import keeps working. */
export type { AdmissionMode } from '@/lib/admission/policy'

/** A recorded admission verdict (admission.go `modelAdmissionDTO`). `signature_verified`
 *  is the honest core result; `signer_roots` are the anchoring-root markers. Note
 *  `tlog_verified` is a documented seam (always false) — never fold it into
 *  `tlog_present`. `reason`/`coverage_note` are verbatim engine text. */
export interface ModelAdmission {
  id: string
  version_ref: string
  model_ref?: string
  subject_name?: string
  subject_digest?: string
  predicate_type?: string
  method?: string
  signer_identity?: string
  signer_issuer?: string
  /** — "root:<sha256>" markers of the anchoring CA root(s). Render verbatim and
   *  copyable; never normalize or label "currently trusted". */
  signer_roots?: string[]
  signature_verified: boolean
  artifact_verified: boolean
  tlog_present: boolean
  tlog_verified: boolean
  resource_count: number
  coverage_note?: string
  aibom_ref?: string
  reason?: string
  note?: string
  attested_by?: string
  attested_at?: string
}

/** The write body for POST /model-versions/{id}/admit (admission.go `admitRequestDTO`).
 *  `bundle` is the raw OMS/Sigstore signature JSON (an object). */
export interface AdmitInput {
  bundle: unknown
  resolved_digests?: Record<string, string>
  model_ref?: string
  aibom_ref?: string
  note?: string
}

/** POST /admit response (admission.go `admitResponseDTO`). A 200 with `admitted:false`
 *  is a recorded deny verdict (evidence), NOT an error — surface as a warning. */
export interface AdmitResponse {
  admitted: boolean
  /** Whether require_signed was on (deny-closed) vs observe. */
  enforced: boolean
  admission: ModelAdmission
}

/** A sealed AIBOM row (aibom.go `aibomSealDTO`) — tamper-evidence anchored to the audit
 *  ledger. The seal is the durable action; GET .../aibom re-generates the live BOM. */
export interface AibomSeal {
  id: string
  owned_ref: string
  serial_number: string
  content_hash: string
  spec_version: string
  component_count: number
  ledger_seq: number
  ledger_hash?: string
  scope_note?: string
  generated_by?: string
  generated_at?: string
}

/** The seal receipt (POST /owned-models/{id}/aibom): the seal row plus the CycloneDX
 *  document it anchored. The BOM body is preserved as opaque JSON — never rebuilt in the
 *  browser. */
export interface AibomSealReceipt {
  seal: AibomSeal
  aibom: unknown
}

// --- slice 2: lineage & evidence (datasets, fine-tune job records, AIBOM
// generate/seal, model card). Same RESPONSE / `…Input` (command) split as slice 1: the
// engine decodes writes with DisallowUnknownFields, so an Input carries ONLY the fields
// the handler reads — never id/attested_by/attested_at (server-set). These DTOs are NOT
// in the OpenAPI surface; this file is the hand-authored source of truth.

/** The closed dataset classification set (dataset.go). */
export type DatasetClassification =
  'public' | 'internal' | 'confidential' | 'restricted' | 'pii' | 'other'

/** A governed dataset lineage component (dataset.go `datasetDTO`). Minimal-data: records
 *  only a NAME + content reference + hash, NEVER the dataset contents. `verified` is an
 *  OPERATOR CLAIM (provenance asserted by the operator), not a cryptographic result;
 *  `owned_ref` is an optional lineage pointer to an owned model (a dataset is tenant-wide,
 *  not owned-scoped). */
export interface Dataset {
  id: string
  name: string
  owned_ref?: string
  classification: DatasetClassification | (string & {})
  governance?: string
  source_ref?: string
  content_hash?: string
  content_alg?: string
  verified: boolean
  attested_by?: string
  attested_at?: string
  note?: string
}

export interface DatasetInput {
  name: string
  owned_ref?: string
  classification?: DatasetClassification
  governance?: string
  source_ref?: string
  content_hash?: string
  content_alg?: string
  /** The operator's provenance claim; defaults to false server-side. */
  verified?: boolean
  note?: string
}

export type FinetuneRuntime =
  'vllm' | 'ollama' | 'llamacpp' | 'other' | (string & {})
export type FinetuneStatus =
  'queued' | 'running' | 'succeeded' | 'failed' | 'canceled' | (string & {})

/** A fine-tune job RECORD (owned.go `finetuneJobDTO`). The control plane records a job's
 *  STATE and the version it produced — it NEVER trains a model or holds weights.
 *  `result_version_ref` is the validated reference to the produced model VERSION (not an
 *  owned model); `base_ref`/`dataset_ref` are free-text references, never resolved and
 *  never the payload. It HAS an update route (status/lineage transitions) but NO delete. */
export interface FinetuneJob {
  id: string
  name: string
  base_ref?: string
  dataset_ref?: string
  runtime?: FinetuneRuntime
  status: FinetuneStatus
  result_version_ref?: string
  started_at?: string
  ended_at?: string
  note?: string
}

export interface FinetuneJobInput {
  name: string
  base_ref?: string
  dataset_ref?: string
  runtime?: FinetuneRuntime
  status?: FinetuneStatus
  result_version_ref?: string
  /** RFC3339. An unparseable value is SILENTLY dropped by the engine (no 400) — the form
   *  validates the format client-side so a bad datetime is never a silent no-op. */
  started_at?: string
  ended_at?: string
  note?: string
}

/** A generated model card (modelcard.go `modelCardDoc`) — a live, read-only export from
 *  the governed inventory; NEVER sealed. `evaluation`/`ethical_considerations` are always
 *  the literal "not_recorded" (the plane never benchmarks); `intended_use` is the
 *  operator's recorded note or "not_recorded"; `training_data` is the lineage datasets or
 *  "not_recorded". The per-version signature/artifact flags are VERIFIED results from the
 *  admission verdict, not claims. */
export interface ModelCardVersion {
  version: string
  status: string
  artifact_ref?: string
  source_ref?: string
  parent_ref?: string
  admission_recorded: boolean
  signature_verified: boolean
  artifact_verified: boolean
  subject_digest?: string
}

export interface ModelCardDetails {
  name: string
  kind: string
  base_ref?: string
  provider_ref?: string
  status: string
  owner_ref?: string
  versions: ModelCardVersion[]
}

export interface ModelCardDataset {
  name: string
  classification?: string
  governance?: string
  source_ref?: string
  provenance_verified: boolean
  content_hash?: string
}

export interface ModelCardProvenance {
  signed_admissions_recorded: number
  signed_admissions_verified: number
  supplier_gpai_posture_recorded: boolean
  supplier_gpai_posture_verified: boolean
  note: string
}

export interface ModelCardDoc {
  schema: string
  generated_at: string
  model_details: ModelCardDetails
  /** The operator's recorded note, or the literal "not_recorded". */
  intended_use: string
  limitations: string[]
  /** The lineage datasets, or the literal "not_recorded". */
  training_data: ModelCardDataset[] | string
  /** Structurally always "not_recorded" — the plane does not benchmark. */
  evaluation: string
  /** Structurally always "not_recorded". */
  ethical_considerations: string
  provenance_and_admission: ModelCardProvenance
  format_references: { title: string; url: string }[]
  disclaimer: string
}
