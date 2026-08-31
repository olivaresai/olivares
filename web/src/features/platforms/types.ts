// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for the multi-surface deploy/compliance matrix (ANT2-01/17) and the
// per-platform model lifecycle (ANT2-03) — LIVE since. These mirror the module
// DTOs of GET /v1/m/models/platforms (modules/models/platforms.go) 1:1; the module
// in turn materializes the credential-less reference accessors of
// connectors/claude-api (surfaces.go Surface/APISupport/ConfirmStatus, lifecycle.go
// retirementSchedule/RejectsSamplingParams) without importing the connector itself
// (the admin-dashboards contract's license boundary). The view PRESENTS the
// response verbatim (ARCHITECTURE.md) and never fabricates a value it does not give
//.
//
// HONESTY MODEL: a fact verified against primary docs is "confirmed"; one
// the authority page did not state (so we model the pattern but not the literal) is
// "to-confirm". Surfaces.go mirrors this with statusConfirmed/statusToConfirm; HIPAA
// carries an explicit ConfirmStatus so a to-confirm posture is NEVER rendered as a
// hard yes/no. The gateway dimension is an OPEN string (sdk/model Gateway): an
// unmodeled surface keeps its key but has no attribute matrix (honest unknown).

/** Honesty marker per surface fact (surfaces.go ConfirmStatus). Open for forward-compat. */
export type ConfirmStatus = 'confirmed' | 'to-confirm' | (string & {})

/** The six MODELED deployment surfaces (sdk/model Gateway values). Widened with
 *  `| string` because Gateway is an open enum — an unmodeled surface is possible. */
export type SurfaceGateway =
  | 'direct'
  | 'claude-platform-aws'
  | 'bedrock-mantle'
  | 'bedrock-legacy'
  | 'vertex'
  | 'foundry'
  | (string & {})

/** Which Anthropic API families apply per surface (surfaces.go APISupport, 1:1). */
export interface ApiSupport {
  /** POST /v1/messages (inference) — every surface. */
  messages: boolean
  /** /v1/organizations/* (usage/cost/keys/workspaces/external_keys/rate_limits). */
  admin: boolean
  /** /v1/compliance/* activity feed. */
  compliance: boolean
  /** GET /v1/models capabilities (ANT2-16 source-of-truth). */
  models: boolean
  /** /v1/messages/batches. */
  batches: boolean
  /** Messages-API mcp_toolset (mirrors MCPConnectorAvailability). */
  mcp_connector: boolean
}

/** The full attribute set of one Claude deployment surface (surfaces.go Surface, 1:1). */
export interface Surface {
  /** The gateway enum key (the value carried on CostSample.Gateway). */
  gateway: SurfaceGateway
  /** Human label ("Claude Platform on AWS"). */
  display_name: string
  /** Who operates the inference plane: Anthropic / AWS / Google / Microsoft. */
  operator: string
  /** The operator's data-access posture, verbatim. */
  operator_data_access: string
  /** Endpoint template, with {region}/{resource} placeholders. */
  base_url_pattern: string
  /** Human description of how the credential is presented. */
  auth_scheme: string
  /** AWS SigV4 signing service name ("" for non-AWS surfaces). */
  sigv4_service: string
  /** Workspace-scoping request header ("" where the surface scopes by key/deployment). */
  workspace_header: string
  /** How a model id is formed on this surface. */
  model_id_form: string
  /** Subset of Anthropic API families that apply here. */
  apis: ApiSupport
  /** Billing channel ("Anthropic invoice", "AWS Marketplace / CCU", "Azure", "GCP"). */
  billing: string
  /** HIPAA posture ("yes" | "no" | "to-confirm" — never fabricated). */
  hipaa: string
  /** Whether HIPAA is confirmed against primary docs or to-confirm. */
  hipaa_status: ConfirmStatus
  /** Zero-Data-Retention posture ("opt-in", "on-request", "yes", "AWS-governed"…). */
  zdr: string
  /** Data-residency model in one line. */
  residency: string
  /** True = observe-only/deprecated surface (never a new build target). */
  deprecated: boolean
  /** When this record was recorded (AsOf, ISO-8601). */
  as_of: string
  /** Surface-specific caveats verbatim from the authority. */
  notes: string
}

/** One per-surface retirement entry (modules/models PlatformRetirement, 1:1). */
export interface ModelRetirement {
  /** The surface this deadline applies to. */
  surface: SurfaceGateway
  /** ISO-8601 retirement date, or "" when the authority did not publish one. */
  retires_on: string
  /** Honesty: "confirmed" or "to-confirm" (Bedrock dates are not published). */
  status: ConfirmStatus
  /** The authority-PUBLISHED recommended successor, carried family-wide. This used
   *  to be empty by design; model-deprecations.md now names one per deprecated
   *  family, so it is no longer an unmarked inference (connectors/claude-api/
   *  lifecycle.go:224-229). Still "" where the authority named none (claude-2.x). */
  replacement_ref: string
  /** ISO-8601 date the family was DEPRECATED (additive); absent where the
   *  verified capture carried none. */
  deprecated_on?: string
  /** When this schedule was recorded (AsOf, ISO-8601). */
  as_of: string
}

/** A declared model-lifecycle row: a model family and its per-surface deadlines. */
export interface ModelLifecycle {
  /** The model id / family this schedule is for. */
  model_id: string
  /** Human label for the family. */
  display_name: string
  /** Per-surface retirement deadlines (divergent — ANT2-03). */
  retirements: ModelRetirement[]
}

/** Param-deprecation pre-advice (lifecycle.go RejectsSamplingParams, ANT2-03).
 *  Anthropic's deprecation, NOT a product bug and NOT a fixable error — informational. */
export interface ParamDeprecation {
  /** The params rejected with a 400 on the affected models. */
  params: string[]
  /** The model generations that reject them ("Opus 4.7+, Fable/Mythos 5"). */
  affected: string
  /** The HTTP status the provider returns for a non-default value. */
  http_status: number
}

/** One Bedrock id / residency honesty note. The notes are WEB-DECLARED reference
 *  (lifecycle.data.ts): they cite modules/models/bedrock.go facts the platforms
 *  endpoint does not serve, keyed into i18n with their own confirm status. The type
 *  lives here so components import types, never a data file. */
export interface LifecycleNote {
  /** A short i18n-key suffix identifying the note. */
  key: 'crisIdFormat' | 'crisOpusId' | 'globalRegionalPremium' | 'usBurndown'
  status: 'confirmed' | 'to-confirm'
}

/** GET /v1/m/models/platforms (LIVE, modules/models). Always 200: when no provider
 *  is wired the module answers `available:false` + `reason` — the view shows an
 *  honest unavailable notice, never a fabricated empty matrix. */
export interface PlatformsReference {
  available: boolean
  /** Why the reference is unavailable; present only when available=false. */
  reason?: string
  surfaces: Surface[]
  /** AsOf + source citation for the surface matrix (surfaces.go surfacesAsOf). */
  surfaces_as_of: string
  surfaces_source: string
  lifecycles: ModelLifecycle[]
  /** AsOf + source citation for the lifecycle schedule (lifecycle.go lifecycleAsOf). */
  lifecycle_as_of: string
  lifecycle_source: string
  param_deprecation: ParamDeprecation
}
