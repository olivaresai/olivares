// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for the identity & NHI admin console. Provenance is tracked per the
// honesty contract — the panel must never present a shape the backend
// does not actually return:
//   • REAL — backed by an endpoint that exists and is wired today (SSO federation
//     start/callback + 501 sso_not_configured, SCIM 2.0 /v1/scim/v2,
//     governance roster /v1/m/governance/identities|groups, security Findings
//     /v1/m/security/findings (iam_posture/governance/mcp_auth), audit ledger).
//   • DECLARED — a contract this session EXPOSES for to serve. These
//     are wired to their declared routes; until the backend lands they 404/501 and
//     the UI renders an honest ContractPendingNotice (never a fake success, never a
//     red error). The External-Keys/residency objects, cert-manager TLS and crypto/PQC
//     inventory, and the WebAuthn/PIV ceremony are DECLARED — verified absent from a
//     queryable REST surface in this repo (they flow on the connector bus, not HTTP). The
//     WIF objects (svac_/fdis_/fdrl_) ARE now LISTED via the org:admin OAuth WIF Admin API
//     and reconciled against the declared rules; the panel VISUALISES + LINTS them
//     and shows declared-vs-actual drift — it never creates/edits them (Console-only writes).

// ---------------------------------------------------------------------------
// REAL — security/governance Findings (mirrors the security module findingDTO,
// the same shape consumes). Carries a redacted detail_hash, never a payload
// (docs/SECURITY-HARDENING.md).
// ---------------------------------------------------------------------------
export interface IdentityFinding {
  id: string
  /** iam_posture | governance | mcp_auth | … */
  kind: string
  severity: string
  status: string
  subject_kind?: string
  subject_ref?: string
  title?: string
  /** Redacted fingerprint — render as a hash, never expand to content. */
  detail_hash?: string
  occurred_at?: string
  metadata?: Record<string, unknown>
}

// ---------------------------------------------------------------------------
// REAL — NHI roster (governance module: GET /v1/m/governance/identities|groups).
// `ref` IS the external_id — the convergence key the access map / R-RW diff joins
// on (PERMITTED vs OBSERVED). It is explicitly "NOT a secret".
// ---------------------------------------------------------------------------

/** A reconciled identity (human or NHI). Anthropic NHI kinds: api_key,
 *  service_account, federation_issuer; estate kinds: secret_store, etc. */
export interface IdentityDTO {
  id: string
  /** external_id — raw provider id (apikey_/svac_/fdis_/user_/wrkspc_…). NOT secret. */
  ref: string
  name?: string
  /** api_key | service_account | federation_issuer | member | invite | secret_store | … */
  kind?: string
  /** anthropic | ldap | okta | entra | vault | spiffe | … */
  source?: string
  /** human | nhi | unknown */
  principal_type?: string
  disabled: boolean
}

/** A directory collection (group/role) or a federation rule projected as a policy. */
export interface IdentityGroupDTO {
  ref: string
  /** group | role | policy */
  kind?: string
  display_name?: string
  source?: string
}

// ---------------------------------------------------------------------------
// REAL — NHI lifecycle (governance module: /v1/m/governance/nhi/*).
// This is deliberately separate from the reconciled identity roster above: the
// roster is inventory, while these records carry rotation, enforcement,
// ownership and offboarding state. A rotated secret is only present on the
// immediate action response and is never part of a lifecycle row or event.
// ---------------------------------------------------------------------------

export interface NhiLifecycleDTO {
  identity_ref: string
  source?: string
  kind?: 'agent' | ''
  criticality: 'low' | 'medium' | 'high' | 'critical' | string
  owner_ref?: string
  sponsor_ref?: string
  rotated_at?: string
  max_age_seconds?: number
  rotation_target?: string
  staleness_status: 'ok' | 'stale' | 'unknown' | string
  enforcement: 'monitor' | 'alert' | 'blocked' | string
  enforcement_reason?: string
  orphaned: boolean
  registry_orphaned?: boolean
  offboard_state: 'none' | 'soft_deleted' | 'finalized' | string
  recovery_until?: string
}

export interface NhiPostureDTO {
  total: number
  rotation_known: number
  rotation_coverage?: number
  stale: number
  blocked: number
  alerting: number
  orphaned: number
  unsponsored?: number
  owned?: number
  soft_deleted: number
  finalized: number
  critical: number
}

export interface NhiEventDTO {
  identity_ref: string
  event: string
  actor: string
  detail?: string
  occurred_at: string
}

export interface NhiOwnershipInput {
  owner_ref: string
  sponsor_ref: string
}

export interface NhiPolicyInput {
  criticality: string
  max_age_seconds: number
  rotation_target: string
  rotated_at: string
}

export interface NhiActionInput {
  target_ref?: string
  reason?: string
}

export interface NhiActionResult {
  status: string
  approval_ref?: string
  detail?: string
  /** Present only in the immediate successful rotate response. */
  new_secret?: string
  new_credential_ref?: string
}

export interface NhiSweepReport {
  scanned: number
  registered: number
  stale: number
  blocked: number
  orphaned: number
  unsponsored: number
}

// ---------------------------------------------------------------------------
// REAL — SSO federation. Config is ENV-ONLY (no write API); the panel
// renders the env keys as documentation, computes + checks the exact redirect
// URI, and surfaces the connection state. Unconfigured (the default AGPL binary)
// → 501 with code "sso_not_configured" (ErrSSONotConfigured).
// ---------------------------------------------------------------------------

/** DECLARED read of the SSO connection state. Maps the REAL provider protocol /
 *  ErrSSONotConfigured signal into the panel. */
/** The third answer, on any posture endpoint that reads an upstream the deployment may
 *  not have wired: `available:false` + a `reason` means WE COULD NOT LOOK, which is not
 *  the same fact as an empty inventory. Rendering them alike is how "no customer-managed
 *  keys" gets shown to an operator whose Admin credential simply is not provisioned. */
export interface PostureAvailability {
  available: boolean
  /** Present only when the posture could NOT be read; human-readable, names the fix. */
  reason?: string
}

export interface SsoStatus {
  /** "oidc" | "saml" | "" (== unconfigured). */
  protocol: string
  configured: boolean
  /** Server-derived exact callback (RFC 9700 exact-match). The panel also computes
   *  this client-side from the origin so it can be shown before the backend lands. */
  redirect_uri?: string
  /** PKCE is ALWAYS S256 in core (never plain) — reflected, not toggled. */
  pkce_method?: 'S256'
  /** Set when the engine could not determine the SSO posture. `configured:false` with
   *  NO reason is the real "SSO is off" state; with a reason it means we did not look. */
  reason?: string
}

/** The env keys that configure SSO (rendered as read-only documentation — there is
 *  NO config-write API; config is read once at boot via FromEnv). */
export interface SsoEnvField {
  key: string
  required: boolean
  protocol: 'oidc' | 'saml' | 'common'
  secret?: boolean
}

// ---------------------------------------------------------------------------
// REAL — SCIM 2.0 (v1/scim/v2). SCIM envelopes are distinct from the core
// ListResponse (capitalised `Resources`, string `status` in errors).
// ---------------------------------------------------------------------------

export interface ScimServiceProviderConfig {
  schemas: string[]
  patch: { supported: boolean }
  bulk: { supported: boolean; maxOperations: number; maxPayloadSize: number }
  filter: { supported: boolean; maxResults: number }
  changePassword: { supported: boolean }
  sort: { supported: boolean }
  etag: { supported: boolean }
  authenticationSchemes: {
    type: string
    name: string
    description: string
    primary?: boolean
  }[]
}

export interface ScimUser {
  schemas: string[]
  id: string
  userName: string
  active: boolean
  externalId?: string
  displayName?: string
  emails?: { value: string; type?: string; primary?: boolean }[]
  meta?: {
    resourceType?: string
    created?: string
    lastModified?: string
    version?: string
  }
}

export interface ScimListResponse<T> {
  schemas: string[]
  totalResults: number
  startIndex: number
  itemsPerPage: number
  Resources: T[]
}

// ---------------------------------------------------------------------------
// REAL — audit ledger entry (leaver evidence: action scim.user.deprovision →
// sessions+tokens revoked). Mirrors the audit DTO (defensive optional fields).
// ---------------------------------------------------------------------------
export interface AuditEntry {
  id?: string
  action: string
  actor?: string
  actor_kind?: string
  target_kind?: string
  target_id?: string
  at?: string
}

// ---------------------------------------------------------------------------
// LIVE — Workload Identity Federation objects (ANT2-08/07), served by
// GET /v1/m/identity/wif (modules/governance/identityconsole.go). The Go
// WIFGraph mirrors this shape 1:1 (connectors/claude-wif/graph.go). When an org:admin
// OAuth token is configured the backend LISTS the live federation via the WIF Admin API
// and reconciles it against the operator-declared rules: each object carries a
// `source` provenance marker (declared|live|both) and the graph carries a
// `reconciliation` status. Writes remain Console-only, so the panel VISUALISES + LINTS +
// shows drift; it never creates/edits. MINIMAL DATA: the wire carries only the boolean
// `ca_cert_configured` (presence) — there is NO `ca_cert_pem`, key, token, or SVID.
// ---------------------------------------------------------------------------

export type WifJwksMode = 'discovery' | 'explicit_url' | 'inline'
export type WifScope =
  | 'workspace:developer'
  | 'workspace:inference'
  | 'org:manage_tunnels'
  | 'org:admin'
  | string

/** Provenance of a reconciled WIF object: operator-declared, observed live via the WIF
 *  Admin API, or both. Absent on the declared-only graph (no org:admin token). */
export type WifSource = 'declared' | 'live' | 'both'

/** Federation Issuer (fdis_). */
export interface WifIssuer {
  id: string
  issuer_url?: string
  jwks_mode?: WifJwksMode
  /** Presence flag only — the PEM is never stored/transmitted. */
  ca_cert_configured?: boolean
  /** Reconciliation provenance (declared|live|both); absent on the declared-only graph. */
  source?: WifSource
}

/** Federation Rule (fdrl_) — the security boundary. */
export interface WifRule {
  rule_id: string
  issuer_id?: string
  service_account_id: string
  service_account_name?: string
  /** "" ⇒ workspace:developer. Live rules may carry space-separated multi-scope. */
  oauth_scope?: WifScope
  /** wrkspc_… | "default" | "". */
  workspace_id?: string
  subject_prefix?: string
  audience?: string
  claims?: Record<string, string>
  /** CEL conditions are security boundaries (carried, not evaluated here). */
  cel_condition?: string
  /** Validated band 60–86400; 0/undefined = undeclared. */
  token_lifetime_seconds?: number
  jwks_mode?: WifJwksMode
  ca_cert_configured?: boolean
  /** A live rule enabled for every workspace (a breadth signal). */
  applies_to_all_workspaces?: boolean
  /** Explicit per-workspace enablement (live rules). */
  workspace_ids?: string[]
  /** Reconciliation provenance (declared|live|both); absent on the declared-only graph. */
  source?: WifSource
}

/** Service Account (svac_) — a first-class NHI principal, distinct from an API key
 *  (a key IS a credential; a svac HAS credentials minted on-demand via WIF). */
export interface WifServiceAccount {
  id: string
  name?: string
  workspace_id?: string
  oauth_scope?: WifScope
  issuer_id?: string
  rule_id?: string
  /** Live org role (developer|admin). An admin-role svac is the only target a rule can
   *  use to mint org:admin tokens — a posture signal. Absent on a declared svac. */
  organization_role?: string
  /** Reconciliation provenance (declared|live|both); absent on the declared-only graph. */
  source?: WifSource
}

/** Declared-vs-actual reconciliation status. When `reconciled` is false and `unavailable`
 *  is set, the live config could not be listed (the panel shows the declared baseline and
 *  says so — never a fabricated "all clear"). Absent when no org:admin token is configured. */
export interface WifReconciliation {
  reconciled: boolean
  observed_at?: string
  unavailable?: string
}

/** The WIF object graph: fdis_ → fdrl_ → svac_ → scopes. */
export interface WifGraphData {
  issuers: WifIssuer[]
  rules: WifRule[]
  service_accounts: WifServiceAccount[]
  /** Whether a static ANTHROPIC_API_KEY / AUTH_TOKEN is present in the estate
   *  (shadows federation). Sourced from the real footgun Finding, not invented. */
  key_shadow?: { present: boolean; var?: string }
  /** Declared-vs-actual reconciliation status; absent on the declared-only graph.*/
  reconciliation?: WifReconciliation
}

// ---------------------------------------------------------------------------
// DECLARED — Anthropic depth-II posture (ingest; flows on the connector bus,
// not yet HTTP-served — verified). Read-only posture, never management.
// ---------------------------------------------------------------------------

/** External Key / CMEK reference (ekey_). Never the key material. */
export interface ExternalKeyRef {
  id: string
  /** aws_kms | gcp_kms | azure_keyvault */
  provider: string
  name?: string
  /** active | validating | invalid | disabled | "" (unknown) */
  state?: string
  /** Last validate round-trip (documented encrypt/decrypt ≤30s). */
  last_validated_at?: string
  /** Referenced by a workspace ⇒ immutable while referenced. */
  in_use: boolean
  created_at?: string
}

export interface WorkspaceResidency {
  id: string
  name?: string
  /** Immutable home region (today only "us"); distinct from inference geo. */
  geo?: string
  /** Write-once CMEK ref (ekey_); empty ⇒ provider-managed ⇒ posture finding. */
  external_key_id?: string
  /** Cloud-KMS compartment the CMEK key-policy is scoped to. */
  compartment_id?: string
  tags?: Record<string, string>
  data_residency?: {
    /** Empty = unrestricted/unreported, never inferred-denied. */
    allowed_inference_geos?: string[]
    default_inference_geo?: string
  }
}

// ---------------------------------------------------------------------------
// DECLARED — panel cert-manager TLS posture + crypto-agility/PQC inventory
//. Has NOT built a posture contract for these (verified
// ABSENT); the views sit behind this documented pending interface and say so.
// ---------------------------------------------------------------------------

export interface TlsPosture {
  /** cert-manager issuer / ClusterIssuer ref. */
  issuer?: string
  serial?: string
  not_before?: string
  not_after?: string
  /** Last automated rotation. */
  rotated_at?: string
  /** Chain subjects, leaf → root (subjects only, no key material). */
  chain?: string[]
  auto_renew?: boolean
}

export interface CryptoInventoryItem {
  id: string
  /** Where the key/algorithm is used (e.g. "panel-tls", "audit-ledger-signer"). */
  usage: string
  algorithm: string
  /** classic | pqc | hybrid */
  family: 'classic' | 'pqc' | 'hybrid' | string
  /** Whether this slot can be rotated to a PQC algorithm today (crypto-agility). */
  pqc_ready?: boolean
}

// ---------------------------------------------------------------------------
// DECLARED — auth-MCP (PRM RFC 9728). The panel documents the audience-bound
// model and surfaces the REAL mcp_auth Findings; it NEVER exposes token
// passthrough (prohibited by design).
// ---------------------------------------------------------------------------

export interface McpAuthServer {
  /** Canonical resource indicator (lowercase scheme+host+path) — the audience. */
  resource: string
  /** token-binding-verified — true only after authorized introspection. */
  token_binding_verified: boolean
  /** Advertised authorization servers (RFC 9728 PRM). */
  authorization_servers?: string[]
  scopes_supported?: string[]
}

// ---------------------------------------------------------------------------
// DECLARED — privileged login ceremony. The browser runs the
// WebAuthn ceremony via navigator.credentials; the backend issues the challenge
// and verifies. PIV/CAC status is read from the mTLS peer cert server-side. All
// DECLARED today (no backend) → the panel orchestrates + fails closed, and makes
// NO NIST/FIPS conformance claim the backend does not guarantee.
// ---------------------------------------------------------------------------

/** A registered WebAuthn credential as returned by the credentials list endpoint. */
export interface WebAuthnCredentialItem {
  id: string
  name: string
  created_at: string
  backup_eligible?: boolean
}

/** Opaque server-issued WebAuthn options (the panel passes them to the browser
 *  API verbatim; it does not invent the challenge). */
export interface WebAuthnChallenge {
  /** base64url challenge + relying-party / user / credential descriptors, exactly
   *  as the backend issues them. Kept opaque on purpose. */
  publicKey: Record<string, unknown>
}

export interface PivStatus {
  /** Whether a client certificate was presented on this mTLS connection. */
  presented: boolean
  subject?: string
  issuer?: string
  /** Mapped role (cert-to-role), if the backend resolved one. */
  mapped_role?: string
  /** OCSP revocation check outcome: good | revoked | unknown. */
  ocsp?: 'good' | 'revoked' | 'unknown'
  not_after?: string
}

// ---------------------------------------------------------------------------
// Client-side WIF lint result (the differential value — pure, unit-tested logic
// over the WIF objects above; see ./wif/wif-lint).
// ---------------------------------------------------------------------------

export type WifLintSeverity = 'error' | 'warning' | 'info'
export type WifLintRule =
  | 'cel-over-broad'
  | 'key-shadow'
  | 'token-lifetime'
  | 'scope-over-broad'
  | 'jwks-insecure'
  | 'drift'

export interface WifLintFinding {
  rule: WifLintRule
  severity: WifLintSeverity
  /** The WIF object the finding is about (fdrl_/svac_/env-var). */
  subjectRef: string
  /** Interpolation values for the localized title/detail/recommendation, keyed by
   *  `rule` in the i18n bundle. The linter stays pure + language-agnostic; the view
   *  renders the copy (and adds the `ant auth status` remediation for key-shadow —
   *  clearly UI guidance, not a backend field: FindingReport has no remediation). */
  meta?: Record<string, string | number>
}
