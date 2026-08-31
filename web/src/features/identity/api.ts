// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// API for the identity & NHI admin console. ARCHITECTURE.md — presentation over
// the SAME API; no server logic here.
//
// CONTRACT PROVENANCE (honest):
//  • REAL today:
//      - SSO federation start/callback (501 sso_not_configured when unconfigured) —
//      - SCIM 2.0 ServiceProviderConfig + Users — (/v1/scim/v2)
//      - NHI roster identities + groups — (/v1/m/governance)
//      - security/governance Findings (iam_posture, governance/anthropic.federation
//        footgun, mcp_auth) — (/v1/m/security/findings)
//      - audit ledger (leaver evidence) — core (/v1/audit)
//  • LIVE (flipped from a declared seam in):
//      - WIF object graph — GET /v1/m/identity/wif, served by modules/governance
//        (identityconsole.go). Read-only + self-audited; always 200 (an empty
//        federation returns empty arrays, never a seam). The Go WIFGraph mirrors the
//        WifGraphData shape 1:1.
//  • LIVE (flipped from a declared seam in after a browser walk caught the panel
//    calling routes the engine never mounted):
//      - SSO connection-state summary — GET /v1/m/identity/sso
//      - External Keys / CMEK inventory — GET /v1/m/identity/external-keys
//      - Workspace data-residency posture — GET /v1/m/identity/residency
//    All three are served by modules/governance (identitysso.go, identityposture.go),
//    read-only and self-audited, and answer 200 with `available`/`reason` rather than
//    404 when the upstream Admin credential is not provisioned — a mounted route that
//    says "not wired" is an answer; a 404 is a defect.
//  • DECLARED (wired to a route that today 404/501s → honest pending seam):
//      - cert-manager TLS + crypto/PQC inventory (did NOT build these) and the
//        WebAuthn/PIV privileged-login ceremony (no backend — Declared seam).
//        Both are registered in scripts/console-route-seams.json with their reason,
//        which is what keeps the class check green without hiding them.
import { http } from '@/lib/api'
import { ApiError } from '@/lib/api/errors'
import type { ListResponse } from '@/lib/api/types'
import type {
  AuditEntry,
  CryptoInventoryItem,
  ExternalKeyRef,
  IdentityDTO,
  IdentityFinding,
  IdentityGroupDTO,
  NhiActionInput,
  NhiActionResult,
  NhiEventDTO,
  NhiLifecycleDTO,
  NhiOwnershipInput,
  NhiPolicyInput,
  NhiPostureDTO,
  NhiSweepReport,
  PivStatus,
  PostureAvailability,
  ScimListResponse,
  ScimServiceProviderConfig,
  ScimUser,
  SsoStatus,
  TlsPosture,
  WebAuthnChallenge,
  WebAuthnCredentialItem,
  WifGraphData,
  WorkspaceResidency,
} from './types'

/** REAL — SCIM 2.0 service provider (first-party, NOT a /v1/m module). */
const SCIM = '/v1/scim/v2'
/** REAL — NHI roster (governance module). */
const GOVERNANCE = '/v1/m/governance'
/** REAL — security/governance Findings. */
const SECURITY = '/v1/m/security'
/** REAL — tamper-evident audit ledger. */
const AUDIT = '/v1/audit'
/** DECLARED — identity-console posture/graph the panel EXPOSES for the backend. */
const IDENTITY = '/v1/m/identity'
/** DECLARED — privileged-login ceremony (first-party auth seam). */
const AUTH = '/v1/auth'

/** SCIM error code from the unconfigured federation provider. */
export const SSO_NOT_CONFIGURED = 'sso_not_configured'

export interface FindingParams {
  kind?: string
  subject_kind?: string
  status?: string
  cursor?: string
  limit?: number
}

export interface IdentityParams {
  source?: string
  kind?: string
  cursor?: string
  limit?: number
}

export interface NhiLifecycleParams {
  enforcement?: string
  offboard_state?: string
  cursor?: string
  limit?: number
}

function nhiPath(ref: string): string {
  return `${GOVERNANCE}/nhi/${encodeURIComponent(ref)}`
}

export interface ScimUsersParams {
  startIndex?: number
  count?: number
  filter?: string
}

export const identityApi = {
  // --- REAL: SSO / SCIM ------------------------------------------------
  /** DECLARED summary of the SSO connection state. On 501 sso_not_configured the
   *  view renders the explicit ErrSSONotConfigured state; on 404/other-501 → seam. */
  ssoStatus: () => http.get<SsoStatus>(`${IDENTITY}/sso`),
  /** REAL: SCIM service-provider config (read-only discovery). */
  scimServiceProviderConfig: () =>
    http.get<ScimServiceProviderConfig>(`${SCIM}/ServiceProviderConfig`),
  /** REAL: SCIM Users (provisioned roster). SCIM envelope, not core ListResponse. */
  scimUsers: (params?: ScimUsersParams) =>
    http.get<ScimListResponse<ScimUser>>(`${SCIM}/Users`, {
      query: { ...params },
    }),

  // --- REAL: NHI roster ------------------------------------------------
  identities: (params?: IdentityParams) =>
    http.get<ListResponse<IdentityDTO>>(`${GOVERNANCE}/identities`, {
      query: { ...params },
    }),
  groups: (params?: { cursor?: string; limit?: number }) =>
    http.get<ListResponse<IdentityGroupDTO>>(`${GOVERNANCE}/groups`, {
      query: { ...params },
    }),

  // --- REAL: NHI lifecycle ---------------------------------------------------
  nhiLifecycle: (params?: NhiLifecycleParams) =>
    http.get<ListResponse<NhiLifecycleDTO>>(`${GOVERNANCE}/nhi`, {
      query: { ...params },
    }),
  nhiPosture: () => http.get<NhiPostureDTO>(`${GOVERNANCE}/nhi/posture`),
  nhiDetail: (ref: string) => http.get<NhiLifecycleDTO>(nhiPath(ref)),
  nhiEvents: (ref: string) =>
    http.get<{ items: NhiEventDTO[] }>(`${nhiPath(ref)}/events`),
  setNhiOwnership: (ref: string, input: NhiOwnershipInput) =>
    http.put<void>(`${nhiPath(ref)}/ownership`, input),
  setNhiPolicy: (ref: string, input: NhiPolicyInput) =>
    http.put<void>(`${nhiPath(ref)}/policy`, input),
  rotateNhi: (ref: string, input?: NhiActionInput) =>
    http.post<NhiActionResult>(`${nhiPath(ref)}/rotate`, input),
  offboardNhi: (ref: string, input?: NhiActionInput) =>
    http.post<NhiActionResult>(`${nhiPath(ref)}/offboard`, input),
  finalizeNhi: (ref: string, input?: NhiActionInput) =>
    http.post<NhiActionResult>(`${nhiPath(ref)}/offboard/finalize`, input),
  restoreNhi: (ref: string) =>
    http.post<NhiActionResult>(`${nhiPath(ref)}/restore`),
  sweepNhi: () => http.post<NhiSweepReport>(`${GOVERNANCE}/nhi/sweep`),

  // --- REAL: Findings ------------------------------------------
  /** iam_posture (claude-console blind-spot), governance (WIF key-shadow footgun),
   *  mcp_auth (token-binding) — all REAL on the security findings stream. */
  findings: (params?: FindingParams) =>
    http.get<ListResponse<IdentityFinding>>(`${SECURITY}/findings`, {
      query: { ...params },
    }),

  // --- REAL: audit ledger (leaver evidence) ----------------------------------
  audit: (params?: { action?: string; cursor?: string; limit?: number }) =>
    http.get<ListResponse<AuditEntry>>(AUDIT, { query: { ...params } }),

  // --- LIVE: WIF object graph (ANT2-08/07; flipped in) ------------------
  /** The rich fdis_/fdrl_/svac_ objects (CEL/lifetime/scope/jwks/subject) the linter
   *  renders over. Served by GET /v1/m/identity/wif (modules/governance) — always
   *  200, self-audited; an empty federation returns empty arrays, never a seam. */
  wifGraph: () => http.get<WifGraphData>(`${IDENTITY}/wif`),

  // --- DECLARED: posture (ANT2-04/06 via via) ---------
  externalKeys: () =>
    http.get<ListResponse<ExternalKeyRef> & PostureAvailability>(
      `${IDENTITY}/external-keys`,
    ),
  workspaceResidency: () =>
    http.get<ListResponse<WorkspaceResidency> & PostureAvailability>(
      `${IDENTITY}/residency`,
    ),
  tlsPosture: () => http.get<TlsPosture>(`${IDENTITY}/tls`),
  cryptoInventory: () =>
    http.get<ListResponse<CryptoInventoryItem>>(`${IDENTITY}/crypto-inventory`),

  // --- DECLARED: privileged-login ceremony --------------------
  /** List the operator's registered WebAuthn credentials. */
  webauthnCredentials: () =>
    http.get<{ items: WebAuthnCredentialItem[] }>(
      `${AUTH}/webauthn/credentials`,
    ),
  /** Server-issued WebAuthn registration options (the browser consumes them). */
  webauthnRegisterOptions: () =>
    http.post<WebAuthnChallenge>(`${AUTH}/webauthn/register/options`),
  /** Submit the browser's attestation for verification (backend verifies). */
  webauthnRegister: (credential: unknown, name: string) =>
    http.post<{ ok: boolean }>(`${AUTH}/webauthn/register`, {
      credential,
      name,
    }),
  /** Rename a registered WebAuthn credential. */
  webauthnRename: (id: string, name: string) =>
    http.patch<{ ok: boolean }>(
      `${AUTH}/webauthn/credentials/${encodeURIComponent(id)}`,
      { name },
    ),
  /** Delete a registered WebAuthn credential (requires AAL3). */
  webauthnDelete: (id: string) =>
    http.delete<void>(`${AUTH}/webauthn/credentials/${encodeURIComponent(id)}`),
  /** Server-issued assertion options for a step-up. */
  webauthnAuthOptions: () =>
    http.post<WebAuthnChallenge>(`${AUTH}/webauthn/authenticate/options`),
  /** Submit the browser's assertion to elevate the session AAL (backend verifies). */
  webauthnAuthenticate: (credential: unknown) =>
    http.post<{ ok: boolean; aal?: number }>(`${AUTH}/webauthn/authenticate`, {
      credential,
    }),
  /** PIV/CAC client-certificate status (read from the mTLS peer cert server-side). */
  pivStatus: () => http.get<PivStatus>(`${AUTH}/piv/status`),
  /** Elevate the CURRENT session with the client certificate already presented on this
   *  connection. No body: the engine reads the TLS peer certificate
   *  (core/api/handlers_piv.go:71 → ElevatePIVSession), so the browser must have attached
   *  one during the handshake — which is why the caller gates on `PivStatus.presented`
   *  rather than on "PIV is configured". A certificate cannot be attached after the fact. */
  pivElevate: () =>
    http.post<{ ok: boolean; aal: number }>(`${AUTH}/piv/elevate`, {}),
}

/**
 * True when a DECLARED endpoint is not implemented by the backend yet (404 / 501 /
 * 405) — render the honest pending seam instead of a red error. Identical contract
 * to the governance console.
 */
export function isContractPending(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false
  return error.status === 404 || error.status === 501 || error.status === 405
}

/** True for the specific "SSO not configured" signal (ErrSSONotConfigured → 501
 *  sso_not_configured) — distinct from a backend-pending route, it is a real, known
 *  state the view renders explicitly. */
export function isSsoNotConfigured(error: unknown): boolean {
  return error instanceof ApiError && error.code === SSO_NOT_CONFIGURED
}

/** True for the backend's "PIV/CAC not configured on this deployment" signal
 *  (501 piv_not_configured) — a real, known state (set OLIVARES_PIV_CONFIG),
 *  NOT the backend-pending seam (the route is live). Same pattern as
 *  isSsoNotConfigured. */
export function isPivNotConfigured(error: unknown): boolean {
  return error instanceof ApiError && error.code === 'piv_not_configured'
}

/** True when a step-up cannot start because the user has no registered passkey
 *  yet (400 no_webauthn_credential) — the operator must register one in
 *  the Privileged-login tab first; rendered as guidance, never a red failure. */
export function isNoWebAuthnCredential(error: unknown): boolean {
  return error instanceof ApiError && error.code === 'no_webauthn_credential'
}

/** Tenant-scoped query keys (same convention as queryKeys / claudePolicyKeys). */
export const identityKeys = {
  all: (t: string | null) => ['identity', t] as const,
  sso: (t: string | null) => ['identity', t, 'sso'] as const,
  scimConfig: (t: string | null) => ['identity', t, 'scim', 'config'] as const,
  scimUsers: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['identity', t, 'scim', 'users'] as const)
      : (['identity', t, 'scim', 'users', params] as const),
  identities: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['identity', t, 'identities'] as const)
      : (['identity', t, 'identities', params] as const),
  groups: (t: string | null) => ['identity', t, 'groups'] as const,
  nhiAll: (t: string | null) => ['identity', t, 'nhi-lifecycle'] as const,
  nhi: (t: string | null, params?: unknown) =>
    params === undefined
      ? ([...identityKeys.nhiAll(t)] as const)
      : ([...identityKeys.nhiAll(t), params] as const),
  nhiPosture: (t: string | null) =>
    [...identityKeys.nhiAll(t), 'posture'] as const,
  nhiDetail: (t: string | null, ref: string) =>
    [...identityKeys.nhiAll(t), 'detail', ref] as const,
  nhiEvents: (t: string | null, ref: string) =>
    [...identityKeys.nhiAll(t), 'events', ref] as const,
  findings: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['identity', t, 'findings'] as const)
      : (['identity', t, 'findings', params] as const),
  audit: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['identity', t, 'audit'] as const)
      : (['identity', t, 'audit', params] as const),
  wif: (t: string | null) => ['identity', t, 'wif'] as const,
  externalKeys: (t: string | null) => ['identity', t, 'external-keys'] as const,
  residency: (t: string | null) => ['identity', t, 'residency'] as const,
  tls: (t: string | null) => ['identity', t, 'tls'] as const,
  crypto: (t: string | null) => ['identity', t, 'crypto'] as const,
  piv: (t: string | null) => ['identity', t, 'piv'] as const,
  webauthnCredentials: (t: string | null) =>
    ['identity', t, 'webauthn', 'credentials'] as const,
}
