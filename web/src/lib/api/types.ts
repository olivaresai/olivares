// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Hand-authored DTO types — the stable JSON shapes the engine's REST API returns.
//
// WHY hand-authored and not fully generated: the engine publishes an OpenAPI 3.1
// document (consumed by `openapi-typescript` → openapi.gen.ts for path/method/param
// typing), but that document intentionally describes response BODIES as a generic
// `{type: object}` (see core/api/openapi.go) — the concrete shapes live in the Go
// DTOs (core/api/dto.go). These interfaces mirror those DTOs 1:1 so the client and
// every view get precise, reviewable types. Keep them in sync with
// core/api/dto.go (field names are the snake_case JSON tags).

/** Built-in tenant role, in ascending privilege (core/auth RoleGrants). */
export type Role = 'viewer' | 'editor' | 'admin' | 'owner'

/** Principal kind returned by /v1/auth/whoami. */
export type PrincipalKind = 'user' | 'token'

/** Stable, non-leaking error codes from the API error envelope (core/api/errors.go). */
export type ApiErrorCode =
  | 'unauthenticated'
  | 'forbidden'
  // The actor may administer the thing but cannot grant a rank above their own
  // — distinct from 'forbidden' so the console says which of the two it is.
  | 'role_ceiling'
  // The actor MAY do this; the SESSION is not assured enough. The engine raises
  // it from requireAAL3 (core/api/middleware.go) and core/auth's credential
  // lifecycle, and it always means AAL3 (core/auth/assurance.go:25-28) — the
  // envelope carries no required_aal, so the console must not invent one.
  // Distinct from 'forbidden' for the same reason as role_ceiling, and the
  // stakes are higher: reported as 'forbidden' it accuses the operator of
  // lacking a role they already hold. It is REACHABLE on a correct console —
  // assurance expires after StepUpTTL (15 min, assurance.go:35) while the
  // render-time gate reads the AAL cached in the principal, so a button painted
  // by RequireAssurance can still take a 403 when it is finally pressed.
  | 'step_up_required'
  | 'setup_required'
  | 'setup_complete'
  | 'not_found'
  | 'conflict'
  | 'bad_request'
  | 'weak_password'
  | 'locked_out'
  | 'internal'

/** The single JSON error envelope for the whole API. A handler MAY attach extra
 * structured fields alongside code/message — e.g. the workflow graph validator's
 * `step_ref`, which anchors a failure to one node of the DAG. Those extras ride
 * through to ApiError.details. */
export interface ErrorEnvelope {
  error: { code: string; message: string } & Record<string, unknown>
}

/** Envelope for a paged collection (core/api/dto.go listResponse). */
export interface ListResponse<T> {
  items: T[]
  cursor?: string
  has_more: boolean
}

/** GET /v1/server-info — unauthenticated; drives the setup gate and footer. */
export interface ServerInfo {
  version: string
  engine: string
  setup_required: boolean
  license: { status: string; licensee: string }
}

/** POST /v1/auth/login response. `token` is an opaque session token (olvs_…). */
export interface LoginResponse {
  token: string
  session_id: string
  expires_at: string
}

/** One tenant grant in the calling principal's identity. */
export interface Grant {
  tenant: string
  role: Role | string
  /**
   * The principal's EFFECTIVE permission set in THIS tenant, sorted. `can()` is
   * membership of this set — the console does not re-derive the RBAC rule.
   *
   * It is the tenant-wide RBAC floor over the permissions this binary serves, minus the
   * workspace-confinement forbids that hold whatever the action targets. It does NOT
   * carry authored scoped grants/forbids or the ABAC deny-overlay: those are decided per
   * RESOURCE at request time, so the engine may still refuse an action in this set. See
   * core/auth/effective.go for the authoritative statement.
   *
   * Per GRANT, not per principal: the role — hence the set — differs per tenant, and
   * `can(permission, { tenant })` accepts a tenant other than the active one.
   */
  permissions: string[]
  /**
   * Present only when this membership is confined to a workspace. The principal
   * may act only within it; the enforcement is server-side and per request, and only the
   * target-independent part of it is reflected in `permissions`.
   */
  confined_workspace?: string
}

/** GET /v1/auth/whoami — the calling principal and its grants. */
export interface Whoami {
  kind: PrincipalKind
  user_id: string
  actor: string
  display_name: string
  superadmin: boolean
  grants: Grant[]
  /**
   * NIST SP 800-63B-4 Authenticator Assurance Level of the CURRENT session
   * (1=password, 2=MFA, 3=hardware/phishing-resistant). DECLARED, forward-
   * compatible: the privileged-login backend (Seam) does not
   * emit this yet, so it is `undefined` today and the AAL gate treats an absent
   * value as AAL1 — i.e. sensitive actions are fail-closed until a real
   * WebAuthn/PIV session sets it. The panel NEVER fabricates an AAL the backend
   * did not assert.*/
  aal?: number
  /** Authentication methods of the current session (e.g. "pwd", "webauthn",
   *  "piv"). DECLARED alongside `aal`; absent today. */
  amr?: string[]
}

/** A user (never includes the password hash). */
export interface UserDTO {
  id: string
  email: string
  display_name?: string
  status: string
  is_superadmin: boolean
  created_at: string
}

/** An agent — the representative tenant-scoped resource. */
export interface AgentDTO {
  id: string
  tenant_id: string
  workspace_id?: string
  name: string
  kind: string
  external_id?: string
  status: string
  identity_id?: string
  labels?: Record<string, unknown>
  metadata?: Record<string, unknown>
  created_at: string
  updated_at: string
  version: number
}

export interface AgentInput {
  name: string
  kind: string
  external_id?: string
  status?: string
  identity_id?: string
  labels?: Record<string, unknown>
  metadata?: Record<string, unknown>
}

/** Access-graph confidence (UI-CONTRACT-ACCESS-MAP): attributed = firm
 * (solid edge), approximate = uncertain (dotted edge). Never render approximate
 * as if it were firm. The core model also carries broader confidence strings, so
 * this is widened to string but the two canonical values are named. */
export type AccessConfidence = 'attributed' | 'approximate' | (string & {})

/** Access mode on an edge of the R/RW map. */
export type AccessMode = 'read' | 'readwrite' | (string & {})

// AccessEdgeDTO and DriftDTO were REMOVED in (C2): they backed the dead
// accessApi client (no importers) whose drift call hit the raw, unreconciled core
// route (since removed). The live access-map view has its own typed model under
// src/features/access-map/ (DiffResponse), consuming the reconciled module III path.

/** One tamper-evident ledger event — hashes hex, signature base64. */
export interface AuditEventDTO {
  id: string
  seq: number
  occurred_at: string
  actor: string
  actor_kind: string
  action: string
  target_kind?: string
  target_id?: string
  prev_hash: string
  hash: string
  sig?: string
}

/**
 * The checkpoint verdict as its three real answers — mirrors core/audit
 * CheckpointStatus. `pending` means nothing has been attested YET (a young ledger
 * whose checkpoint scheduler has not fired); it is not a failure and must never be
 * rendered as one. Anything the engine cannot name arrives as `failed`.
 */
export type CheckpointStatus = 'ok' | 'failed' | 'pending'

/**
 * GET /v1/audit/verify — the engine's verdict on the tenant chain. The web renders
 * this verdict; it NEVER recomputes integrity client-side (ARCHITECTURE.md, docs/SECURITY-HARDENING.md).
 * `ok` is the AND of the structural chain and every signed checkpoint, with a
 * `pending` checkpoint verdict counting as trustworthy. Mirrors the map the
 * handler writes (core/api/handlers_audit.go handleAuditVerify) 1:1.
 */
export interface AuditVerifyResponse {
  ok: boolean
  chain: {
    ok: boolean
    /** Number of links walked. */
    checked: number
    /** Seq of the first broken link (0 when the chain is intact). */
    break_at: number
    reason: string
  }
  checkpoints: {
    /** Strict: true only once something has actually been attested AND verified.
     *  It is false both for a virgin ledger and for a tampered one — read
     *  `status`, never this, to decide what to paint. */
    ok: boolean
    /** The three answers the boolean cannot carry (core/audit CheckpointStatus):
     *  verified, verified BAD, or nothing attested yet. `pending` is NOT a
     *  failure — the structural chain verdict already proves the chain. */
    status: CheckpointStatus
    /** Number of signed checkpoints verified. */
    count: number
    /** Highest seq covered by a valid signed checkpoint. */
    latest_attested_seq: number
    /** Seq of the first checkpoint that failed signature verification (0 when none). */
    first_bad_seq: number
    reason: string
  }
}

/** GET /v1/audit/pubkey — the Ed25519 checkpoint key, so an external WORM/SIEM can
 * verify exported checkpoints offline (core/api/handlers_audit.go handleAuditPubkey). */
export interface AuditPubkeyResponse {
  algorithm: string
  /** Base64-encoded Ed25519 public key. */
  public_key: string
}

/** A tenant org. */
export interface OrgDTO {
  id: string
  tenant_id: string
  name: string
  slug: string
  status: string
  created_at: string
}

// --- request inputs ----------------------------------------------------------

export interface LoginRequest {
  email: string
  password: string
}

export interface SetupRequest {
  token: string
  email: string
  password: string
  /** OPTIONAL name for the first organization. Empty/absent lets the engine fall
   * back to its neutral default (core/api/handlers_auth.go firstOrgDefaultName). */
  organization?: string
}

/** POST /v1/setup response (core/api/dto.go setupResponse): the created superadmin,
 * flat as before, PLUS the organization created with it. First-boot setup provisions
 * both in one operation — the superadmin owns the organization — so the console can
 * select the tenant straight away instead of guessing it from a follow-up listing. */
export interface SetupResponse extends UserDTO {
  organization: OrgDTO
}

/** POST /v1/invites/accept payload — unauthenticated; the single-use invite
 * token is the gate (core/api/handlers_onboarding.go). */
export interface AcceptInviteRequest {
  token: string
  password: string
}

/** POST /v1/invites/accept response: the account is active and a session was
 * minted. The console deliberately discards it and routes to /login (the page
 * never stores a token that arrived outside the normal login flow). */
export interface AcceptInviteResponse {
  token: string
  session_id: string
  expires_at: string
}

export interface CreateUserRequest {
  email: string
  display_name?: string
  password: string
  superadmin?: boolean
}

export interface IssueTokenRequest {
  name: string
  tenant?: string
  role?: string
  superadmin?: boolean
}

export interface IssueTokenResponse {
  token: string
  id: string
  name: string
}

export interface GrantMembershipRequest {
  user_id: string
  tenant: string
  role: string
}

export interface CreateOrgRequest {
  name: string
  slug: string
}
