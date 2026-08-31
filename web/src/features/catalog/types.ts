// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for the catalog module (XIV) — mirror the Go DTOs in modules/catalog/dto.go
// 1:1 (snake_case JSON tags). The web is a thin client (ARCHITECTURE.md): these are the
// exact shapes the engine returns at /v1/m/catalog. CRITICAL invariants encoded here:
//   - secrets live INSIDE entry.spec by name/locator only — there is NO structured
//     secret_refs field, and the spec viewer is read-only (never a raw-value input);
//   - all integrity/lifecycle fields (status/content_hash/signed/sig_alg/signed_by/
//     approved_by/approved_at) are SERVER-MANAGED — they are NEVER sent on a write;
//   - signed_by is the signing-key FINGERPRINT (hex16), never the raw public key;
//   - the verification posture (verifyDTO.verified) is server-computed TRUTH, so it
//     is rendered honestly — an approved-but-unsigned entry is "hash-pinned, unsigned",
//     a hash/signature mismatch is NEVER shown as safe.

/** Kind of catalog entry. */
export type EntryKind =
  'agent' | 'mcp' | 'skill' | 'template' | 'model' | 'connector' | (string & {})

/** Lifecycle status of a catalog entry. */
export type EntryStatus =
  'draft' | 'pending' | 'approved' | 'deprecated' | (string & {})

/** Governance status of an instantiation request. */
export type InstanceStatus =
  'requested' | 'approved' | 'rejected' | 'active' | (string & {})

/** The decisions an admin can apply to an instance via /transition. */
export type InstanceDecision = 'approved' | 'rejected' | 'active'

export const ENTRY_KINDS: EntryKind[] = [
  'agent',
  'mcp',
  'skill',
  'template',
  'model',
  'connector',
]
export const ENTRY_STATUSES: EntryStatus[] = [
  'draft',
  'pending',
  'approved',
  'deprecated',
]
export const INSTANCE_STATUSES: InstanceStatus[] = [
  'requested',
  'approved',
  'rejected',
  'active',
]

/** A curated catalog entry (one version). Mirrors entryDTO. */
export interface EntryDTO {
  id?: string
  kind: EntryKind
  name: string
  slug: string
  version: string
  status?: EntryStatus
  summary?: string
  /** Reusable definition JSON; NO secret values, secrets referenced by name/locator. */
  spec?: Record<string, unknown>
  owner_ref?: string
  /** SHA-256 hex of the canonical preimage, set only after approval. */
  content_hash?: string
  /** True if an Ed25519 signature is present (always emitted). */
  signed: boolean
  /** 'ed25519' when signed. */
  sig_alg?: string
  /** Short hex16 key FINGERPRINT (display only; never the raw public key). */
  signed_by?: string
  /** Principal actor string of the approver (e.g. 'user:uuid'). */
  approved_by?: string
  /** RFC3339 timestamp of approval. */
  approved_at?: string
}

/**
 * The write payload for create/update — EXACTLY the fields the backend accepts
 * (DisallowUnknownFields → any extra field 400s). All integrity/lifecycle fields are
 * server-managed and MUST NOT be sent; status is forced to 'draft' on create.
 */
export interface EntryInput {
  kind: EntryKind
  name: string
  slug: string
  version: string
  summary?: string
  spec?: Record<string, unknown>
  owner_ref?: string
}

/** The honest verification posture of an entry. Mirrors verifyDTO. */
export interface VerifyDTO {
  /** Entry status at verify time (approved|deprecated|other). */
  status: string
  /** Recomputed content hash matches the stored pin. */
  hash_ok: boolean
  /** A signature is present. */
  signed: boolean
  /** Ed25519 signature valid over the stored hash. */
  signature_ok: boolean
  /** Overall: hash_ok && (unsigned || signature_ok), pinned-posture aware. */
  verified: boolean
  /** Signer key fingerprint (hex16) when signed. */
  signed_by?: string
  /** Stored pinned hash (hex). */
  content_hash?: string
  /** Freshly recomputed hash (hex). */
  recomputed_hash?: string
  /** Human-readable explanation (always present). */
  reason: string
}

/**
 * The catalog signing posture from GET /pubkey — an UNTYPED JSON map with two shapes
 * (NOT a struct). When configured: {signing_enabled, algorithm, public_key,
 * fingerprint}. When not configured: {signing_enabled:false, note}.
 */
export interface PubkeyDTO {
  signing_enabled: boolean
  algorithm?: string
  /** base64 Ed25519 public key — intentionally public, only present when configured. */
  public_key?: string
  /** hex16 fingerprint, only present when configured. */
  fingerprint?: string
  /** Explanatory note, only present when signing is not configured. */
  note?: string
}

/** A self-service instantiation request. Mirrors instanceDTO. */
export interface InstanceDTO {
  id?: string
  /** Source entry id (provenance; always emitted). */
  entry_id: string
  entry_kind?: EntryKind
  entry_slug?: string
  entry_version?: string
  /** Instance name (always emitted). */
  name: string
  /** Deployment target reference (e.g. 'env:prod'). */
  target_ref?: string
  status?: InstanceStatus
  /** Principal actor who requested. */
  requested_by?: string
  /** Principal actor who last transitioned. */
  decided_by?: string
  note?: string
}

/** Request body for POST /entries/{id}/instantiate. */
export interface InstantiateInput {
  name: string
  target_ref?: string
  note?: string
}

/** Request body for POST /instances/{id}/transition. */
export interface TransitionInput {
  status: InstanceDecision
  note?: string
}

/** A non-terminal instance is still moving through governance (poll while present). */
export function isInstanceNonTerminal(status?: InstanceStatus): boolean {
  return status === 'requested' || status === 'approved'
}

/** The catalog-native entry kinds that carry an attestation-admission gate. The
 * model kind is ALSO gated at approve, but through the models module (decoupled) —
 * it has no catalog /…-admissions route, so it is not surfaced here. */
export type AdmissionKind = 'mcp' | 'connector'

/** La política de admisión por tenant y por CLASE de entrada — el "trust root" que decide si una
 *  entrada firmada se admite. Los DTO de `mcp` y `connector` son IDÉNTICOS en el motor
 *  (`modules/catalog/mcpadmission.go:161` y `connectoradmission.go:178`: los mismos diez campos con
 *  los mismos tags), así que aquí es UN tipo y no dos.
 *
 *  ⛔ `configured` LLEGA SÓLO CUANDO ES `false`. El motor devuelve el DTO cuando hay política, y un
 *  objeto aparte con `configured: false` cuando no la hay (`mcpadmission.go:260-265`). Es decir:
 *  con la política PUESTA el campo viene `undefined`, y `undefined` es falsy — un `if
 *  (!policy.configured)` diría «sin configurar» sobre una política configurada. Se lee con
 *  `policyConfigured()`, más abajo, y no a mano. */
export interface AdmissionPolicy {
  require_signed: boolean
  require_subject_digest: boolean
  allowed_identities?: string[]
  allowed_issuers?: string[]
  trusted_keys?: string[]
  trusted_roots?: string[]
  allowed_predicates?: string[]
  note?: string
  attested_by?: string
  attested_at?: string
  /** Presente y `false` SÓLO cuando no hay política. Ausente cuando sí la hay. */
  configured?: boolean
}

/** El cuerpo de escritura del PUT. Tipo DEDICADO, NUNCA construido esparciendo la respuesta
 *  del GET: `decodeJSON` es `DisallowUnknownFields` (`modules/catalog/dto.go:89`) y el DTO no
 *  tiene `configured`, así que devolver el vacío tal cual da 400. Y aunque no lo diera, la
 *  `note` sintética del vacío («no MCP-entry admission policy configured; …») se guardaría
 *  como si la hubiera escrito el operador.
 *
 *  `attested_by`/`attested_at` tampoco viajan: los impone el motor desde el principal y su
 *  reloj (`toRecord(actor, at)`), así que enviarlos sólo puede confundir a quien lea esto. */
export interface AdmissionPolicyInput {
  require_signed: boolean
  require_subject_digest: boolean
  allowed_identities?: string[]
  allowed_issuers?: string[]
  trusted_keys?: string[]
  trusted_roots?: string[]
  allowed_predicates?: string[]
  note?: string
}

/** La única lectura correcta de `configured`: ausente significa CONFIGURADA. */
export function policyConfigured(p: AdmissionPolicy | undefined): boolean {
  return !!p && p.configured !== false
}

/** The admission-gated kind for an entry kind, or null when it has no catalog gate. */
export function admissionKind(kind: EntryKind): AdmissionKind | null {
  if (kind === 'mcp') return 'mcp'
  if (kind === 'connector') return 'connector'
  return null
}

/**
 * A recorded attestation-admission verdict for an mcp or connector entry — mirrors
 * mcpAdmissionDTO / connectorAdmissionDTO in modules/catalog (identical shapes). This
 * is the DURABLE answer to "why was this entry admitted or refused into the served
 * catalog?": the booleans are the verdict, and `reason` is the verifier's verbatim
 * explanation. subject_digest is the artifact the attestation actually covered.
 */
export interface AdmissionDTO {
  id?: string
  entry_ref: string
  subject_name?: string
  subject_digest?: string
  predicate_type?: string
  method?: string
  signer_identity?: string
  signer_issuer?: string
  /** The DSSE signature verified against a trusted root. */
  signature_verified: boolean
  /** The attested subject was bound to the expected artifact digest. */
  artifact_verified: boolean
  tlog_present: boolean
  tlog_verified: boolean
  coverage_note?: string
  /** Verbatim verifier explanation — rendered as-is, never reworded. */
  reason?: string
  note?: string
  attested_by?: string
  attested_at?: string
}

/** Request body for POST /entries/{id}/admit (a Sigstore attestation bundle). */
export interface AdmitInput {
  /** The Sigstore attestation bundle JSON (cosign download attestation / gh attestation download). */
  bundle: unknown
  predicate_types?: string[]
  expected_digest?: string
  note?: string
}

/** Response from POST /entries/{id}/admit. Mirrors {mcp,connector}AdmitResponseDTO. */
export interface AdmitResponse {
  /** Whether the attestation satisfied the enforcing policy (verdict, not HTTP status). */
  admitted: boolean
  /** Whether the tenant policy enforces (require_signed) vs observe-only. */
  enforced: boolean
  admission: AdmissionDTO
}
