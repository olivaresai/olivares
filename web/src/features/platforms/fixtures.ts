// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Deterministic fixtures for the platforms tests, shaped EXACTLY like the LIVE
// GET /v1/m/models/platforms response. The data mirrors the Go reference the
// backend serves, verbatim:
//  • surfaces  → connectors/claude-api/surfaces.go AllSurfaces() (AsOf 2026-06-06),
//    sorted by gateway enum string (bedrock-legacy, bedrock-mantle,
//    claude-platform-aws, direct, foundry, vertex).
//  • lifecycles → connectors/claude-api/lifecycle.go retirementSchedule (AsOf
//    2026-06-09), ALL non-exempt families in registry order. Confirmed rows come
//    from the verified per-surface dates; to-confirm rows (empty retires_on) come
//    from the documented "authority says Bedrock/Vertex but published no date"
//    knowledge (sonnet-4 → both bedrock gateways; the opus families → vertex;
//    3-5-haiku → both bedrocks + vertex). replacement_ref is now POPULATED: the
//    deprecations page publishes a recommended successor per family
//    (lifecycle.go:224-229); claude-2.x carries none (verbatim absence).
//  • param_deprecation → RejectsSamplingParams (Opus 4.7+, Fable/Mythos 5 → 400).
// HONESTY: nothing here is invented — every date/status traces to the Go registry;
// rows whose date the authority did not publish carry retires_on "" and are
// rendered "date not published / to-confirm", never "never retires".
import type {
  ModelLifecycle,
  PlatformsReference,
  Surface,
} from './types'

const SURFACES_AS_OF = '2026-06-06'
const LIFECYCLE_AS_OF = '2026-06-09'

/** The six modeled deployment surfaces, verbatim from surfaces.go AllSurfaces(). */
export const surfacesFixture: Surface[] = [
  {
    gateway: 'bedrock-legacy',
    display_name: 'Amazon Bedrock (legacy InvokeModel/Converse — DEPRECATED)',
    operator: 'AWS',
    operator_data_access: 'zero operator access; data governed by AWS',
    base_url_pattern: 'https://bedrock-runtime.{region}.amazonaws.com',
    auth_scheme: 'AWS SigV4 (service bedrock) + IAM (bedrock:*)',
    sigv4_service: 'bedrock',
    workspace_header: '',
    model_id_form:
      '<geo>.anthropic.<model> cross-region inference profile, geo ∈ {us, eu, apac, global}',
    apis: {
      messages: true,
      admin: false,
      compliance: false,
      models: false,
      batches: true,
      mcp_connector: false,
    },
    billing: 'AWS (Bedrock)',
    hipaa: 'yes',
    hipaa_status: 'confirmed',
    zdr: 'AWS-governed',
    residency: 'AWS region / cross-region inference profile geo',
    deprecated: true,
    as_of: SURFACES_AS_OF,
    notes:
      'OBSERVE-ONLY/deprecated — NOT a new build target. The specific opus Global-CRIS inference-profile id (global.anthropic.claude-opus-4-…) is NOT verified against the AWS page (only Sonnet was listed); the FORMAT is confirmed, that concrete id is to-confirm — NOT fabricated.',
  },
  {
    gateway: 'bedrock-mantle',
    display_name: 'Amazon Bedrock (Mantle — current surface)',
    operator: 'AWS',
    operator_data_access: 'zero operator access; data governed by AWS',
    base_url_pattern: 'https://bedrock-mantle.{region}.api.aws',
    auth_scheme: 'AWS SigV4 (service bedrock-mantle) + IAM',
    sigv4_service: 'bedrock-mantle',
    workspace_header: '',
    model_id_form: 'anthropic.<model>, e.g. anthropic.claude-opus-4-8',
    apis: {
      messages: true,
      admin: false,
      compliance: false,
      models: false,
      batches: true,
      mcp_connector: false,
    },
    billing: 'AWS (Bedrock) — no Anthropic Admin cost API',
    hipaa: 'yes',
    hipaa_status: 'confirmed',
    zdr: 'AWS-governed',
    residency: 'AWS region; HIPAA/FedRAMP/IL4-5 eligible (ANT2-17)',
    deprecated: false,
    as_of: SURFACES_AS_OF,
    notes:
      'The CURRENT Bedrock surface and the build target. No Anthropic Admin/Models/Compliance API (AWS-governed) → Anthropic-side ingest degrades to CloudTrail/usage-derived; document, never fake. HIPAA/FedRAMP/IL4-5 live here.',
  },
  {
    gateway: 'claude-platform-aws',
    display_name: 'Claude Platform on AWS (Anthropic-operated)',
    operator: 'Anthropic',
    operator_data_access:
      'Anthropic-operated on AWS (distinct from partner-operated Bedrock)',
    base_url_pattern: 'https://aws-external-anthropic.{region}.api.aws',
    auth_scheme:
      'AWS SigV4 (service aws-external-anthropic) + IAM (65 actions; over-read in Get*)',
    sigv4_service: 'aws-external-anthropic',
    workspace_header: 'anthropic-workspace-id',
    model_id_form:
      'bare model id (Anthropic-native), workspace selected via anthropic-workspace-id header',
    apis: {
      messages: true,
      admin: true,
      compliance: true,
      models: true,
      batches: true,
      mcp_connector: true,
    },
    billing: 'AWS Marketplace / CCU (committed-compute units)',
    hipaa: 'no',
    hipaa_status: 'confirmed',
    zdr: 'opt-in (on-request)',
    residency: 'AWS region of the endpoint; ZDR opt-in',
    deprecated: false,
    as_of: SURFACES_AS_OF,
    notes:
      'Anthropic-operated, so the full Admin/Compliance/Models APIs apply (unlike Bedrock). HIPAA explicitly NOT supported (ANT2-01). IAM grants 65 actions with documented over-read in Get*.',
  },
  {
    gateway: 'direct',
    display_name: 'Anthropic API (first-party)',
    operator: 'Anthropic',
    operator_data_access: 'Anthropic-operated',
    base_url_pattern: 'https://api.anthropic.com',
    auth_scheme:
      'x-api-key (workspace API key) / Admin key for /v1/organizations/*',
    sigv4_service: '',
    workspace_header: '',
    model_id_form: 'bare model id, e.g. claude-opus-4-8',
    apis: {
      messages: true,
      admin: true,
      compliance: true,
      models: true,
      batches: true,
      mcp_connector: true,
    },
    billing: 'Anthropic invoice',
    hipaa: 'to-confirm',
    hipaa_status: 'to-confirm',
    zdr: 'on-request',
    residency:
      'per-request inference_geo (us|global); workspace data_residency policy',
    deprecated: false,
    as_of: SURFACES_AS_OF,
    notes:
      'The only surface exposing the full API set (Admin/Compliance/Models/Batches) and the Models-API source-of-truth (ANT2-16).',
  },
  {
    gateway: 'foundry',
    display_name: 'Microsoft Foundry',
    operator: 'Microsoft',
    operator_data_access: 'Microsoft-governed (Azure)',
    base_url_pattern: 'https://{resource}.services.ai.azure.com/anthropic/v1/*',
    auth_scheme: 'Entra ID (Azure AD OAuth2)',
    sigv4_service: '',
    workspace_header: '',
    model_id_form:
      'deployment-name indirection (the Azure deployment name maps to the model)',
    apis: {
      messages: true,
      admin: false,
      compliance: false,
      models: false,
      batches: false,
      mcp_connector: true,
    },
    billing: 'Azure',
    hipaa: 'to-confirm',
    hipaa_status: 'to-confirm',
    zdr: 'yes',
    residency: 'Azure region of the resource',
    deprecated: false,
    as_of: SURFACES_AS_OF,
    notes:
      'NO Admin/Compliance/Models/Batches API (ANT2-01) — Anthropic-side governance ingest is unavailable here; model access is by deployment-name indirection, not model id. MCP connector IS available (mirrors MCPConnectorAvailability). ZDR supported.',
  },
  {
    gateway: 'vertex',
    display_name: 'Google Vertex AI',
    operator: 'Google',
    operator_data_access: 'Google-governed',
    base_url_pattern: 'https://{region}-aiplatform.googleapis.com',
    auth_scheme: 'Google ADC / OAuth2 access token',
    sigv4_service: '',
    workspace_header: '',
    model_id_form: 'publisher model id (vertex form), per-platform versioning',
    apis: {
      messages: true,
      admin: false,
      compliance: false,
      models: false,
      batches: true,
      mcp_connector: false,
    },
    billing: 'GCP',
    hipaa: 'to-confirm',
    hipaa_status: 'to-confirm',
    zdr: 'Google-governed',
    residency: 'GCP region; Google-governed retention',
    deprecated: false,
    as_of: SURFACES_AS_OF,
    notes:
      'Lifecycle dates differ from first-party (ANT2-03): e.g. Sonnet 4 retires 2026-09-14 on Vertex vs 2026-06-15 first-party. No Anthropic Admin/Models API.',
  },
]

// --- lifecycles (ALL non-exempt families, registry order, rows sorted by surface) --

/** Build the three Anthropic-operated rows every family shares (lifecycle.go
 *  anthropicOperated), already in ascending surface order. `confirmed` means the
 *  authority PUBLISHED the date — an empty date (mythos-preview, claude-2.x) is a
 *  verified announcement whose DATE is to-confirm, matching the backend mapping. */
function anthropicOperated(
  retiresOn: string,
  replacement: string,
  deprecatedOn: string | undefined,
) {
  return ['claude-platform-aws', 'direct', 'foundry'].map((surface) => ({
    surface,
    retires_on: retiresOn,
    status: retiresOn === '' ? ('to-confirm' as const) : ('confirmed' as const),
    replacement_ref: replacement,
    ...(deprecatedOn ? { deprecated_on: deprecatedOn } : {}),
    as_of: LIFECYCLE_AS_OF,
  }))
}

/** A to-confirm row: the authority serves the family on this surface but published
 *  no retirement date — empty date, never fabricated. `deprecated_on` is OMITTED on
 *  these rows (matching the backend): the published deprecation date applies to the
 *  Anthropic-operated surfaces only (lifecycle.go:51-54 — Bedrock/Vertex run their
 *  own schedules), so stamping it here would over-claim. */
function toConfirm(surface: string, replacement: string) {
  return {
    surface,
    retires_on: '',
    status: 'to-confirm' as const,
    replacement_ref: replacement,
    as_of: LIFECYCLE_AS_OF,
  }
}

export const lifecyclesFixture: ModelLifecycle[] = [
  {
    model_id: 'claude-opus-4-1',
    display_name: 'Claude Opus 4.1',
    retirements: [
      ...anthropicOperated('2026-08-05', 'claude-opus-4-8', '2026-06-05'),
      // Vertex lists the family deprecated with NO published date.
      toConfirm('vertex', 'claude-opus-4-8'),
    ],
  },
  {
    model_id: 'claude-opus-4-2025',
    display_name: 'Claude Opus 4',
    retirements: [
      ...anthropicOperated('2026-06-15', 'claude-opus-4-8', '2026-04-14'),
      toConfirm('vertex', 'claude-opus-4-8'),
    ],
  },
  {
    model_id: 'claude-opus-4-0',
    // The humanizer drops a trailing -0 alias marker: claude-opus-4-0 IS
    // claude-opus-4-2025 (alias + dated id), so both render 'Claude Opus 4'.
    display_name: 'Claude Opus 4',
    retirements: [
      ...anthropicOperated('2026-06-15', 'claude-opus-4-8', '2026-04-14'),
      toConfirm('vertex', 'claude-opus-4-8'),
    ],
  },
  {
    model_id: 'claude-sonnet-4',
    display_name: 'Claude Sonnet 4',
    retirements: [
      // Bedrock (both gateways): date NOT published → to-confirm (NOT "never").
      toConfirm('bedrock-legacy', 'claude-sonnet-4-6'),
      toConfirm('bedrock-mantle', 'claude-sonnet-4-6'),
      ...anthropicOperated('2026-06-15', 'claude-sonnet-4-6', '2026-04-14'),
      // Vertex retires later (verified divergence — the whole point of ANT2-03).
      {
        surface: 'vertex',
        retires_on: '2026-09-14',
        status: 'confirmed' as const,
        replacement_ref: 'claude-sonnet-4-6',
        deprecated_on: '2026-04-14',
        as_of: LIFECYCLE_AS_OF,
      },
    ],
  },
  {
    model_id: 'claude-mythos-preview',
    display_name: 'Claude Mythos (preview)',
    // Deprecated 2026-06-09; retirement "after Mythos 5 GA" with NO published
    // date → empty dates (announced, not scheduled).
    retirements: anthropicOperated('', 'claude-mythos-5', '2026-06-09'),
  },
  {
    model_id: 'claude-3-7-sonnet',
    display_name: 'Claude 3.7 Sonnet',
    retirements: anthropicOperated(
      '2026-02-19',
      'claude-sonnet-4-6',
      '2025-10-28',
    ),
  },
  {
    model_id: 'claude-3-5-haiku',
    display_name: 'Claude 3.5 Haiku',
    retirements: [
      // Still served on Bedrock/Vertex; their dates unpublished → to-confirm.
      toConfirm('bedrock-legacy', 'claude-haiku-4-5-20251001'),
      toConfirm('bedrock-mantle', 'claude-haiku-4-5-20251001'),
      ...anthropicOperated(
        '2026-02-19',
        'claude-haiku-4-5-20251001',
        '2025-12-19',
      ),
      toConfirm('vertex', 'claude-haiku-4-5-20251001'),
    ],
  },
  {
    model_id: 'claude-3-haiku',
    display_name: 'Claude 3 Haiku',
    retirements: anthropicOperated(
      '2026-04-20',
      'claude-haiku-4-5-20251001',
      '2026-02-19',
    ),
  },
  {
    model_id: 'claude-3-opus',
    display_name: 'Claude 3 Opus',
    retirements: anthropicOperated(
      '2026-01-05',
      'claude-opus-4-8',
      '2025-06-30',
    ),
  },
  {
    model_id: 'claude-3-5-sonnet',
    display_name: 'Claude 3.5 Sonnet',
    retirements: anthropicOperated(
      '2025-10-28',
      'claude-sonnet-4-6',
      '2025-08-13',
    ),
  },
  {
    model_id: 'claude-3-sonnet',
    display_name: 'Claude 3 Sonnet',
    retirements: anthropicOperated(
      '2025-07-21',
      'claude-sonnet-4-6',
      '2025-01-21',
    ),
  },
  {
    model_id: 'claude-2.',
    display_name: 'Claude 2',
    // Retired per the deprecations page, but dates and replacement were NOT in
    // the verified capture — dateless, successor-less rows (honest absence).
    retirements: anthropicOperated('', '', undefined),
  },
]

/** The full live-response fixture the container tests mock the api with. */
export const platformsReferenceFixture: PlatformsReference = {
  available: true,
  surfaces: surfacesFixture,
  surfaces_as_of: SURFACES_AS_OF,
  surfaces_source: 'connectors/claude-api/surfaces.go',
  lifecycles: lifecyclesFixture,
  lifecycle_as_of: LIFECYCLE_AS_OF,
  lifecycle_source: 'connectors/claude-api/lifecycle.go',
  param_deprecation: {
    params: ['temperature', 'top_p', 'top_k'],
    affected: 'Opus 4.7+, Fable/Mythos 5',
    http_status: 400,
  },
}

/** An unmodeled, third-party gateway id (e.g. a self-hosted LiteLLM proxy). There is
 *  NO attribute matrix for it — the view keeps the value and labels it honestly. */
export const unmodeledGateway = 'litellm-proxy'
