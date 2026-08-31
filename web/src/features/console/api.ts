// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Endpoint helpers + query keys for the control console: user onboarding +
// invites, workspace / agent-group CRUD, and the managed SSO/IdP config. Thin
// wrappers over the core HTTP client (ARCHITECTURE.md — no logic here); the active
// tenant header is attached automatically. The SSO config is GLOBAL (deployment-
// wide), so its key carries no tenant.
import { http } from '@/lib/api'
import {
  ensureFreshSession,
  notifyUnauthorized,
  type TenantListOptions,
  type TenantRequestOptions,
} from '@/lib/api/client'
import { ApiError, NetworkError, parseErrorEnvelope } from '@/lib/api/errors'
// El techo de las listas de /v1/m/models es UNO, y vive donde nacio. Definir aqui una
// segunda constante con el mismo 1000 fabrica dos copias de un control, que envejecen
// aparte: la primera vez que alguien suba una y no la otra, estas dos listas y el resto
// de /v1/m/models dejan de pedir lo mismo sin que nada lo diga.
import { EVIDENCE_PAGE } from '@/features/models/api'
import type { AgentDTO, ListResponse } from '@/lib/api/types'
import type { SourceMode } from '@/features/shared'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'

// --- types -------------------------------------------------------------------

export interface MembershipRef {
  id: string
  user_id: string
  tenant: string
  role: string
}

export interface OnboardedUser {
  id: string
  email: string
  display_name?: string
  status: string
  is_superadmin: boolean
  created_at: string
}

export interface InviteHandle {
  id: string
  token: string
  accept_url: string
  expires_at: string
}

export interface OnboardResult {
  user: OnboardedUser
  created: boolean
  membership: MembershipRef
  invite?: InviteHandle
}

export interface OnboardInput {
  email: string
  display_name?: string
  role: string
  mode: 'password' | 'invite'
  password?: string
}

export interface InviteDTO {
  id: string
  email: string
  tenant: string
  role: string
  expires_at: string
  created_at: string
}

export interface RosterMemberDTO {
  user_id: string
  email: string
  display_name?: string
  status: 'active' | 'inactive' | 'error'
  external_id?: string
  sso_only: boolean
  role: 'viewer' | 'editor' | 'admin' | 'owner'
  workspace_ids?: string[]
  groups?: string[]
}

export interface WorkspaceDTO {
  id: string
  tenant_id: string
  name: string
  slug: string
  status: string
  is_default: boolean
  created_at: string
  updated_at: string
  version: number
}

export interface AgentGroupDTO {
  id: string
  tenant_id: string
  workspace_id?: string
  name: string
  slug: string
  description?: string
  status: string
  metadata?: Record<string, unknown>
  created_at: string
  updated_at: string
  version: number
}

export type AgentStatus = 'active' | 'inactive' | 'archived'

export interface AgentInput {
  name: string
  kind: string
  external_id?: string
  status?: AgentStatus
  identity_id?: string
  workspace_id?: string
  labels?: Record<string, unknown>
  metadata?: Record<string, unknown>
}

export interface AgentGroupUpdateInput {
  name?: string
  description?: string
  status?: 'active' | 'inactive'
  metadata?: Record<string, unknown>
  // A workspace id re-scopes the group; "" clears the scope back to tenant-wide.
  // Omit the field to leave the current scope untouched (backend partial update).
  workspace_id?: string
}

export interface AgentGroupMemberDTO {
  id: string
  group_id: string
  agent_id: string
}

// ssoConfigPath targets one IdP: the global or a tenant-scoped surface (U6), and
// within it the scope's PRIMARY ("default") IdP on the base path or a named additional
// IdP under /idps/{alias} (U4). "default"/absent alias => the base path.
function ssoConfigPath(scope?: string, alias?: string): string {
  const base = scope
    ? `/v1/console/sso/tenants/${encodeURIComponent(scope)}`
    : '/v1/console/sso'
  return alias && alias !== 'default'
    ? `${base}/idps/${encodeURIComponent(alias)}`
    : base
}

export interface SSOConfigDTO {
  configured: boolean
  provider_available: boolean
  protocol?: string
  status?: string
  redirect_uri: string
  // The scope this config governs (U6): absent for the deployment-wide global
  // config, else the tenant id whose IdP this is. A per-tenant IdP only RESOLVES at
  // login in an enterprise build (the MultiIDP cap-lift); the open build stores it
  // but the single-active cap refuses activating a second IdP.
  target_tenant?: string
  // The IdP's scope-unique identifier (U4); "default" is the scope's primary IdP.
  // A scope may configure additional IdPs, each with its own alias.
  alias?: string
  oidc_issuer?: string
  oidc_client_id?: string
  oidc_client_secret_hint?: string
  saml_metadata_url?: string
  saml_entity_id?: string
  saml_acs_url?: string
  saml_idp_sso_url?: string
  saml_email_attr?: string
  saml_sp_cert_pem?: string
  saml_sp_key_hint?: string
  // SP SIGNING keypair, independent of the encryption pair above: it signs
  // AuthnRequests and is published in the SP metadata as the use="signing" descriptor.
  // The cert is public and round-trips; only a hint of the sealed key is ever returned.
  saml_sp_sign_cert_pem?: string
  saml_sp_sign_key_hint?: string
  // Login-enforcement posture (protocol-independent). `require_sso` is the
  // operator intent to block password login; `network_allowlist` is the IP CIDR
  // allow-list (always present, may be empty). `enforced_by` has THREE values, not two:
  // "enterprise" (this build enforces THIS row's posture), "unavailable" (it never
  // enforces — the open build), and "out_of_scope" (it enforces, but not over this row —
  // the posture it reads is the deployment-wide primary's). Test for "enterprise"
  // explicitly: treating anything that is not "unavailable" as enforced is the false green
  // badge this replaced.
  require_sso: boolean
  network_allowlist: string[]
  enforced_by: string
  // Group mapping + JIT coherence. `oidc_groups_claim` / `saml_groups_attr`
  // name where the provider reads the subject's directory groups; `scim_authoritative`
  // makes SCIM the sole identity authority (SSO login never JIT-creates). `groups_mapped_by`
  // is "enterprise" when this build turns an asserted group into a grant at login, or
  // "unavailable" when it extracts groups but never maps them (the open build) — symmetric
  // with `enforced_by`.
  oidc_groups_claim?: string
  saml_groups_attr?: string
  scim_authoritative: boolean
  groups_mapped_by: string
  //U5 home-realm routing: the email domains this IdP claims (always an array). An
  // email-first login for user@<domain> routes to the IdP that claims <domain>.
  // `routed_by` is "enterprise" when THIS build routes by domain, or "unavailable" when it
  // stores the domains but resolves the single global IdP (the open build) — symmetric
  // with `enforced_by` / `groups_mapped_by`.
  claimed_domains: string[]
  routed_by: string
  updated_at?: string
}

export interface SSOConfigInput {
  protocol: string
  enabled: boolean
  oidc_issuer?: string
  oidc_client_id?: string
  oidc_client_secret?: string
  saml_metadata_url?: string
  saml_entity_id?: string
  saml_acs_url?: string
  saml_idp_sso_url?: string
  saml_email_attr?: string
  saml_sp_cert_pem?: string
  saml_sp_key_pem?: string
  // SP SIGNING keypair. Both halves obey "blank = keep the stored value", so an
  // edit never has to re-enter them — but the console MUST still declare and send the
  // cert: a payload that omits one half of a keypair the backend stores is exactly how
  // request signing broke (core/auth/federation_config.go, PutConfigIdP).
  saml_sp_sign_cert_pem?: string
  saml_sp_sign_key_pem?: string
  // Login-enforcement posture (protocol-independent). Malformed CIDRs are
  // rejected by the backend (HTTP 400, code "bad_request").
  require_sso?: boolean
  network_allowlist?: string[]
  // Group mapping + JIT coherence (replaced verbatim on every PUT).
  oidc_groups_claim?: string
  saml_groups_attr?: string
  scim_authoritative?: boolean
  //U5 home-realm domains. Malformed (400) or already-claimed (409) domains are
  // rejected by the backend.
  claimed_domains?: string[]
}

// --- sealed runtime secret store ----------------------------------------
// A named secret in the deployment-wide sealed store. The control plane NEVER
// returns the value — only a non-secret `hint` (a short fingerprint) so an admin
// can tell a secret is set / changed without seeing it. Connector configs refer to
// a secret by `store:<name>` and the value is resolved at Open (no restart-to-
// reconfigure). Shapes mirror the /v1/console/secrets handler exactly.

/** One named secret as the store surfaces it — never the value. */
export interface SecretDTO {
  name: string
  hint: string
  description: string
  created_at: string
  updated_at: string
}

/** The list response: the secrets plus whether a sealer backend is wired. */
export interface SecretsListDTO {
  secrets: SecretDTO[]
  sealer_available: boolean
}

/** Create / rotate input. An empty `value` on an existing secret keeps the stored
 * value (edits the description only); a new secret requires a value. */
export interface SecretInput {
  name: string
  value: string
  description: string
}

// --- console connector onboarding ---------------------------------------
// The descriptor-driven authoring surface for connectors AND their credentials:
// add/configure/test/remove a connector from the console, sealed and persisted in
// the database. A SECRET field's value is entered inline and the engine seals it
// into the store (the source row keeps only a `store:<name>` reference); a
// blank secret field on edit keeps the stored value. Shapes mirror the
// /v1/console/connectors handlers + the v1/console/sources list exactly.

/** One configuration field a connector declares, for descriptor-driven rendering. */
export interface ConnectorField {
  key: string
  type: string // "string" | "int" | "bool" | "duration"
  required: boolean
  secret: boolean
  default?: string
  description?: string
}

/** Where the observed system runs, as the ENGINE derives it from the connector's own
 *  declared endpoint defaults (never recomputed here). `unknown` is a real third
 *  answer — the connector declares no endpoint, or it is a plugin the host cannot
 *  introspect — and must never be rendered as if it meant vendor-hosted. The union is
 *  OPEN so an engine newer than this console degrades to "unknown" instead of
 *  crashing. */
export type ConnectorHosting =
  'self_hosted' | 'vendor_hosted' | 'unknown' | (string & {})

/** One available connector kind the catalog offers. `fields_known` is false for an
 * out-of-process (plugin) kind whose schema the host cannot introspect. */
export interface ConnectorInfo {
  kind: string
  title?: string
  description?: string
  transport: string // "in_process" | "plugin"
  fields_known: boolean
  /** Optional so an OLDER engine (which omits it) reads as unknown, not as a lie. */
  hosting?: ConnectorHosting
  fields?: ConnectorField[]
}

/** A configured source as the live roster surfaces it. Config carries secret
 * REFERENCES, never values; `status` is the live state. */
export interface SourceRosterEntry {
  name: string
  kind?: string
  tenant: string
  poll_seconds?: number
  enabled: boolean
  config?: Record<string, string>
  status: string
  source_mode?: SourceMode
}

/** Create/update/test payload. Non-secret settings in `config`; secret-declared
 * fields in `secrets` (blank = keep stored; a `<scheme>:<locator>` = used verbatim;
 * any other value = a literal the engine seals and references). */
export interface ConnectorOnboardInput {
  name: string
  kind: string
  tenant: string
  poll_seconds?: number
  enabled: boolean
  config?: Record<string, string>
  secrets?: Record<string, string>
}

/** The outcome of applying one connector change to the live engine.*/
export interface SourceApplyResult {
  name: string
  action: string
  persisted: boolean
  applied: boolean
  note?: string
}

/** One source the reconciler could not apply, with the honest reason. Mirrors
 * api.SourceRejection (core/api/sources.go). */
export interface SourceRejection {
  name: string
  reason: string
}

/**
 * The result of a full runtime source-roster reconcile. Mirrors
 * api.SourceReloadReport (core/api/sources.go) field-for-field, including the JSON
 * tags. `requires_restart` names the configuration DOMAINS this live reload does NOT
 * cover — read once at boot, they need a restart. The backend states them on EVERY
 * reload, so the UI must surface them every time (never hide them behind "all clear").
 */
export interface SourceReloadReport {
  added?: string[]
  removed?: string[]
  rotated?: string[]
  unchanged: number
  rejected?: SourceRejection[]
  requires_restart?: string[]
}

// --- workspace connector scoping ----------------------------------------
// Connector→workspace assignments and workspace-scoped connector definitions.
// Shapes mirror modules/sourcescope/assignment.go and wsconnector.go.

/** A connector→workspace assignment: "connector X is available in workspace W". */
export interface ConnectorAssignmentDTO {
  id?: string
  connector_name: string
  workspace_ref: string
  mode?: 'r' | 'rw' | (string & {})
  enabled: boolean
  note?: string
}

/** A workspace-scoped connector definition created by a workspace admin. */
export interface WorkspaceConnectorDTO {
  id?: string
  name: string
  kind: string
  workspace_ref: string
  config?: Record<string, string>
  secrets?: Record<string, string>
  poll_seconds?: number
  enabled: boolean
  note?: string
  status?: string
}

// --- source-scope bindings ----------------------------------------
// Shapes mirror modules/sourcescope/binding.go, posture.go and resources.go.

export type SourceScopeSourceType =
  'mcp' | 'model' | 'provider' | 'knowledge' | 'data'

export type SourceScopeTree =
  | 'workspace'
  | 'agent_group'
  | 'folder'
  | 'session'
  | 'agent'
  | 'user'
  | 'user_group'
  | 'role'

/** El actor de una previsualización de enlace. El motor acepta EXACTAMENTE estos dos
 *  (`modules/sourcescope/preview.go`: «actor_kind must be session or agent»), así que la
 *  unión se escribe entera aquí — ofrecer uno de más le manda un 400 al operador y uno de
 *  menos le esconde media función. */
export type SourceScopeActorKind = 'session' | 'agent'

/** El veredicto BASELINE del resolvedor para un par (actor, fuente).
 *
 *  ⛔ `baseline` NO es un adorno y la pantalla NO puede callarlo. El motor corre el
 *  resolvedor REAL con un principal a cero A PROPÓSITO: informa de la línea base
 *  deny-closed (contención, efectos de fila, asignaciones) y **no simula** lo que el
 *  principal añadiría por sus propias concesiones o su RBAC. Pintarlo como «lo que este
 *  actor obtiene» sería afirmar de más justo en una pantalla de autorización.
 *
 *  Y la credencial llega **sólo** como nombre + pista enmascarada: el localizador se queda
 *  en los endpoints de enlace, donde ya vive para quien tiene `binding:read`. */
export interface SourceScopeResolvePreview {
  allowed: boolean
  reason: string
  bound: boolean
  baseline: boolean
  cred_name?: string
  cred_hint?: string
}

export type BindingEffect = 'allow' | 'forbid'
export type CredentialRefKind =
  'env' | 'vault' | 'secret_manager' | 'file' | 'other'

/** A source→scope binding. Credential fields are value-free references only. */
export interface SourceScopeBindingDTO {
  id?: string
  source_type: SourceScopeSourceType
  source_ref: string
  scope_tree: SourceScopeTree
  scope_ref?: string
  effect?: BindingEffect
  folder_path?: string
  cred_name?: string
  cred_ref_kind?: CredentialRefKind
  cred_ref?: string
  cred_hint?: string
  enabled: boolean
  note?: string
}

/** One navigable Resource tree node for folder/subtree bindings. */
export interface ResourceNodeDTO {
  id: string
  name: string
  kind: string
  path?: string
  parent_id?: string
  workspace_id?: string
  sensitivity?: string
}

export interface SourceScopeResourceListParams {
  parent?: string
  subtree?: string
  kind?: string
  workspace_id?: string
  limit?: number
  cursor?: string
}

export interface SourceScopeBindingListParams {
  source_type?: SourceScopeSourceType
  source_ref?: string
  scope_tree?: SourceScopeTree
  limit?: number
  cursor?: string
}

/** A dual-control posture request for a relaxing source-scope change. */
export interface PostureRequestDTO {
  id?: string
  source_type: SourceScopeSourceType
  source_ref: string
  op: string
  target_id?: string
  proposed?: SourceScopeBindingDTO
  reason?: string
  proposer?: string
  status: 'pending' | 'approved' | 'rejected' | (string & {})
  decided_by?: string
  note?: string
  guard_profile?: string
}

/**
 * The CURRENT guard posture of a source, which is a different axis from
 * source-scope: `acl_aware` tightens and applies immediately, `public_only` RELAXES and
 * is dual-controlled (`modules/sourcescope/api.go:105-109`).
 *
 * ⛔ WHY THE CONSOLE NEEDS THIS AND DID NOT HAVE IT. The relaxation review queue lets an
 * operator APPROVE or REJECT a posture change -- four call sites below -- while
 * `GET /guard-postures` had zero callers anywhere in web/src. Measured 2026-08-20: the queue
 * renders Target, Reason and Proposer, and `guard_profile` appeared in web/src ONLY as a
 * type, never in a .tsx. So the approver saw WHICH source and WHY the proposer said so, and
 * neither the proposed profile nor the one it would replace.
 *
 * A dual control whose approver cannot see the state it governs is its weakest form, and the
 * direction matters here: approving `public_only` without knowing it came from `acl_aware`
 * is approving blind in exactly the direction that opens.
 */
export interface GuardPostureDTO {
  source_type: SourceScopeSourceType
  source_ref: string
  /** `acl_aware` | `public_only`. The server normalizes anything else to `acl_aware`. */
  profile: string
  reason?: string
  updated_by?: string
}

export interface PostureRequestListParams {
  status?: string
  source_type?: SourceScopeSourceType
  source_ref?: string
  limit?: number
  cursor?: string
}

export type BindingApplyResult =
  | {
      kind: 'binding'
      status: 200 | 201
      binding: SourceScopeBindingDTO
    }
  | {
      kind: 'posture_request'
      status: 202
      posture_request: PostureRequestDTO
    }

export type BindingDeleteResult =
  | { kind: 'deleted'; status: 204 }
  | {
      kind: 'posture_request'
      status: 202
      posture_request: PostureRequestDTO
    }

// --- scoped administration (Roles & delegation) -------------------
// The structured RBAC surface the governance module projects to the Cedar engine:
// custom roles, permission-groups and scoped grants, plus the delegation ceiling.
// All shapes mirror modules/governance/scopedadmin_handlers.go exactly.

/**
 * The scopeable catalog that feeds the role/grant editors.
 *
 * split what used to be one list, because a MODULE permission kind
 * ("<ns>:<res>") is grantable without being a node of the scope tree:
 *  - `kinds`       — every grantable kind (tree kinds first, then module kinds);
 *  - `tree_kinds`  — the subset a workspace/agent-group/folder scope can filter on.
 *    A module kind on a tree scope is rejected by the backend (it would project a
 *    permit no module route can satisfy), so a scope-class picker MUST use this list;
 *  - `permissions` — the exact module permissions a mounted module declared. A module
 *    kind does NOT imply all three verbs, so the kind × verb grid must consult this
 *    before offering a checkbox, or it offers permissions the backend will reject.
 */
export interface RBACCatalogDTO {
  kinds: string[]
  tree_kinds: string[]
  permissions: string[]
  verbs: string[]
  builtin_roles: string[]
  scope_trees: string[]
}

/** A tenant-wide reusable permission bundle (verb+kind perms ∪ its groups). */
export interface CustomRoleDTO {
  name: string
  display_name?: string
  description?: string
  /**
   * A BUILT-IN role this role starts from, resolved LIVE on every projection. It is what
   * lets "editor except models:keys:write" stay true as the catalog grows, instead of an
   * enumerated copy that silently drifts from the built-in role it was copied from.
   */
  base_role?: string
  permissions: string[]
  groups?: string[]
  /**
   * Subtracted AFTER the base, the direct permissions and every included group, so
   * nothing an include adds can put one back. An exclusion that removes nothing today is
   * valid on purpose — the base grows, and the exclusion holds.
   */
  excludes?: string[]
  created_by?: string
}

/** A tenant-wide reusable permission bundle a custom role can include. */
export interface PermGroupDTO {
  name: string
  display_name?: string
  description?: string
  permissions: string[]
  created_by?: string
}

/** A scoped grant: a subject receives a role within a scope. */
export interface ScopedGrantDTO {
  id?: string
  subject_kind: 'user' | 'role' | 'group' // S256: added 'group'
  subject_ref: string
  role: string
  role_custom?: boolean
  scope_tree: 'tenant' | 'workspace' | 'agent_group'
  scope_ref?: string
  scope_class?: string
  note?: string
  created_by?: string
}

// --- S256 provisioned groups (membership-scoped, operator-managed) --------

export interface GroupDTO {
  id: string
  display_name: string
  external_id: string
  mapped_role: string
  parent_group_id: string
  members: number
}

/** One (scope, grantable-perms) pair the actor may delegate within. */
export interface DelegationDomainDTO {
  scope_tree: string
  scope_ref?: string
  scope_class?: string
  permissions: string[]
}

/** The actor's delegation ceiling: the unbounded-root flag plus its domains. */
export interface DelegationAuthorityDTO {
  superadmin: boolean
  domains: DelegationDomainDTO[]
}

const RBAC = '/v1/m/governance/rbac'
const SOURCESCOPE = '/v1/m/sourcescope'

// --- model-groups + model-access rules -------------------------

const MODELS = '/v1/m/models'
/** Techo de las listas de modelos de consola. Va en la CAPA API y no en el llamante: asi
 *  ninguna vista hereda el recorte por olvidarse de pedirlo. El llamante puede subirlo. */

export interface ModelGroupDTO {
  id?: string
  name: string
  member_refs: string[]
  family_selectors: string[]
  tier_selectors: string[]
  description?: string
}

export interface ModelAccessDTO {
  id?: string
  subject_kind: string
  subject_ref: string
  target_kind: string
  target_ref: string
  workspace_ref?: string
  surfaces: string[]
  budget_ref?: string
  effect?: string
  description?: string
}

// --- AuthZEN search + access-review export --------------------------

export interface AuthZenSubject {
  type: string
  id?: string
}

export interface AuthZenAction {
  name: string
}

export interface AuthZenResource {
  type: string
  id?: string
}

export interface AuthZenSearchRequest {
  subject?: AuthZenSubject
  action?: AuthZenAction
  resource?: AuthZenResource
  page?: { token?: string; limit?: number }
}

export interface AuthZenEntityResult {
  type?: string
  id?: string
  name?: string
}

export interface AuthZenSearchResponse {
  results: AuthZenEntityResult[]
  page: { next_token: string; count?: number }
  context?: Record<string, unknown>
}

export interface AccessReviewRequest {
  resource: { type: string; id: string }
  permissions?: string[]
  subject_type?: string
  assurance?: number
}

export interface AccessReviewEntry {
  subject: { type: string; id: string; display?: string }
  permission: string
  via: string
  reason: string
}

export interface AccessReviewPack {
  resource: { type: string; id: string; name?: string; sensitivity?: string }
  tenant: string
  generated_at: string
  assurance: number
  permissions: string[]
  population: Record<string, unknown>
  entries: AccessReviewEntry[]
  integrity: { pack_sha256: string; sealed: boolean; audit_seq?: number }
}

// --- live edition / license ---------------------------------------------
// The deployment-wide edition surface: install / observe / HOT-APPLY a commercial
// license without a restart (the Grafana/Elastic in-place model). It is pure edition
// plumbing — the open binary persists/displays/hot-applies the attestation but NEVER
// gates a feature on it (LICENSING.md); only the enterprise build's seat policy consumes
// the attested MaxUsers. Shapes mirror core/api.LicenseStatus exactly.

/** The live edition/license view: status + attested claims + live seat usage. */
export interface LicenseStatusDTO {
  /** Build edition — f(build tag), independent of the license: community | enterprise. */
  edition: string
  /** Whether this engine applies a license change without a restart (always true). */
  hot_apply: boolean
  /** License lifecycle: none | invalid | valid | expired | perpetual. */
  status: string
  /** Provenance: none | flag | env-path | env-inline | data-dir. */
  source: string
  source_path?: string
  /** A boot override (--license / OLIVARES_LICENSE[_PATH]) outranks the data-dir file,
   * so install/uninstall are refused (the license is managed out-of-band). */
  managed_externally: boolean
  licensee?: string
  plan?: string
  /** Attested support relationship (e.g. standard | enterprise); display-only, never gates. */
  support_tier?: string
  features?: string[]
  /** @deprecated Attested seat figure (0 = unlimited). IGNORED since B10 — no build
   * caps users on it and the panel does not render it. Kept on the wire only so
   * older clients keep parsing. */
  max_users: number
  issued_at?: string
  expires_at?: string
  /** @deprecated Retained for wire compatibility and IGNORED by this console. Nothing
   * enforces a seat limit any more; the engine normalizes any non-positive figure to
   * (0, false) = unlimited, so these must never be rendered as a quota. */
  seat_limit: number
  /** @deprecated See seat_limit. */
  seat_limited: boolean
  /** Live ACTIVE-account usage — a usage figure, never a quota numerator. */
  active_users: number
  /** The active count hit the display bound (render "N+"). */
  active_users_capped?: boolean
}

// --- enterprise activation ----------------------------------------------
// The deployment-wide activation surface: read per-add-on state and enable/disable
// a preset (or promote a staged add-on). ENTERPRISE-ONLY — the community build 501s
// these routes, so the console shows an "available in the business edition" note.
// Changes stage the governed activation manifest and take effect at the next engine
// RESTART (add-ons are wired at boot), which `restart_required` communicates.

/** One add-on's activation row. `state`: active | pending | available | console. */
export interface ActivationAddonDTO {
  key: string
  title: string
  summary: string
  env: string
  preset: string // the tier that introduces it: starter | regulated | full
  state: string
  reason?: string
  needs_secret?: boolean
}

/** A preset and its add-on keys, for the enable chooser. */
export interface ActivationPresetDTO {
  name: string
  addons: string[]
}

/** The rendered activation status for the console table. */
export interface ActivationStatusDTO {
  edition: string
  preset?: string
  restart_required: boolean
  addons: ActivationAddonDTO[]
  presets: ActivationPresetDTO[]
}

/** One line in a preview diff. `action`: activate | stage | unchanged | console. */
export interface ActivationPlanEntryDTO {
  addon: string
  action: string
  state?: string
  reason?: string
}

/** The diff a preview returns (shown before the operator confirms). */
export interface ActivationPlanDTO {
  preset: string
  changes: boolean
  entries: ActivationPlanEntryDTO[]
}

/** An enable/disable/promote instruction. */
export interface ActivationApplyInput {
  action: 'enable' | 'disable' | 'promote'
  preset?: string
  addon?: string
}

// --- API tokens (programmatic credentials) ----------------------------

export interface TokenDTO {
  id: string
  name: string
  user_id?: string
  bound_tenant_id?: string
  role?: string
  is_superadmin?: boolean
  expires_at?: string
  revoked: boolean
  last_used_at?: string
  created_at: string
}

export interface IssueTokenInput {
  name: string
  tenant: string
  role: string
  superadmin?: boolean
}

export interface IssueTokenResult {
  token: string
  id: string
  name: string
}

export interface RotateTokenResult {
  token: string
  id: string
  name: string
  revoked_id: string
}

// --- setup status + health summary ------------------------------------

export interface SetupStep {
  id: string
  completed: boolean
}

export interface SetupStatusDTO {
  completed: boolean
  steps: SetupStep[]
}

// UpdateStatusDTO is the OTA update-availability indicator. It is present
// only when update checking is configured; on air-gapped deployments it is absent
// (the console shows no indicator — silence, never an error).
export interface UpdateStatusDTO {
  enabled: boolean
  available: boolean
  up_to_date: boolean
  channel: string
  current_version: string
  latest_version?: string
  security?: boolean
  advisories?: string[]
  checked_at?: string
  error?: string
}

// AuditSpoolDTO is the audit-spool budget indicator (ADR-0024 Q2). Present
// only when an audit-ledger budget is declared; absent otherwise — silence, not
// error (mirrors core/api/dto.go auditSpoolDTO).
export interface AuditSpoolDTO {
  max_bytes: number
  used_bytes: number
  /** Exhaustion policy: 'block' (refuse governed writes) | 'degrade' (drop with
   *  declared-gap markers). An unset server mode reports 'block'. */
  mode: string
  /** True when the budget is met/exceeded — governed writes are being refused
   *  (block) or dropped (degrade) RIGHT NOW. */
  engaged: boolean
  pending_drop_tenants?: number
  pending_drops?: number
}

export interface HealthSummaryDTO {
  healthy: boolean
  ready: boolean
  store_engine: string
  /** Connector KINDS this build can wire (the catalog). A capability of the
   *  binary, NOT a live fleet: it is non-zero on a clean install where nothing
   *  is configured, so it must never be rendered as "active". */
  connectors_available: number
  /** Connector INSTANCES in the durable roster, enabled or not. */
  connectors_configured: number
  /** Roster entries whose live status is running — what ingests right now. */
  connectors_running: number
  /** ENABLED roster entries whose live status is failed. */
  connectors_error: number
  users: number
  sso_configured: boolean
  version: string
  update?: UpdateStatusDTO
  /** Audit-spool budget status; absent when no budget is declared.*/
  audit_spool?: AuditSpoolDTO
  /** Live listener certificate expiry; absent when TLS state is unavailable. */
  tls_not_after?: string
  /** Whole days remaining. Zero is a real value, so only undefined means absent. */
  tls_days_left?: number
}

/** Non-secret key/sealer custody metadata from /v1/console/keys. */
export interface KeyInfoDTO {
  purpose: string
  algorithm?: string
  custody_mode?: string
  kek?: string
  created?: string
  public_key?: string
  fingerprint?: string
  prior_count?: number
  origin?: string
  source?: string
  present?: boolean
}

export interface KeyCustodyDTO {
  keys: KeyInfoDTO[]
}

export interface EffectiveConfigEntryDTO {
  key: string
  value: string
  redacted: boolean
  source: 'env' | 'activation' | (string & {})
}

export interface EffectiveConfigDTO {
  entries: EffectiveConfigEntryDTO[]
  strict_violations: string[]
}

export interface BusSubscriberDTO {
  name: string
  class: string
  depth: number
  capacity: number
}

export interface BusBridgeDTO {
  connected: boolean
  pending_msgs: number
  pending_bytes: number
  dropped: number
  publish_errors: number
  decode_errors: number
  gate_skipped: number
  invalid_subject: number
}

export interface BusSnapshotDTO {
  subscribers: BusSubscriberDTO[]
  publish_blocked: number
  dropped: number
  dropped_telemetry: number
  dropped_notify: number
  handler_errors: number
  enqueued: number
  handled: number
  bridge?: BusBridgeDTO
}

export interface FetchedSupportBundle {
  blob: Blob
  filename: string
}

/** True only for the honest air-gap/unconfigured response from update-check. */
export function isUpdateCheckingUnavailable(err: unknown): boolean {
  return err instanceof ApiError && err.status === 501
}

/** Fetch the AAL3-gated support archive with the same auth/tenant headers as the
 * shared JSON client. The response is binary, so it cannot use the shared
 * JSON http client. (No backticks in this comment: the export scrubber's
 * tokenizer treats a backtick in a comment as a template-literal opener and
 * stops rewriting the rest of the file.) */
export async function fetchSupportBundle(): Promise<FetchedSupportBundle> {
  // La renovación va ANTES de la petición: este camino rodea `apiFetch`, así que sin esto
  // sería el único del console que sigue muriendo por caducidad. Comparte el vuelo único.
  // ⛔ EL INQUILINO SE LEE ANTES DE LA ESPERA. Este camino rodea `apiFetch`, así que no
  //    hereda la fijación de `apiFetchWithMeta`: si se leyera después del refresco, un
  //    cambio de inquilino durante la renovación mandaría la petición al inquilino nuevo.
  const tenant = useTenantStore.getState().activeTenant
  await ensureFreshSession()
  const headers = new Headers({ Accept: 'application/octet-stream' })
  const token = useSessionStore.getState().token
  if (token) headers.set('Authorization', `Bearer ${token}`)
  if (tenant) headers.set('X-Olivares-Tenant', tenant)

  let response: Response
  try {
    response = await fetch('/v1/console/support-bundle', {
      method: 'POST',
      headers,
      credentials: 'same-origin',
    })
  } catch (cause) {
    throw new NetworkError('The control plane is unreachable.', cause)
  }

  if (!response.ok) {
    if (response.status === 401) notifyUnauthorized()
    // ⛔ EL CÓDIGO DEL MOTOR SE CONSERVA. Esto fijaba `code: 'support_bundle_failed'` para
    // CUALQUIER fallo y sólo leía el mensaje del sobre, de modo que toda decisión por código era
    // imposible en esta ruta — y hay una que importa: el handler responde 403 con
    // `step_up_required` (core/api/handlers_support_bundle.go:28-37, con prueba de wire en
    // core/api/handlers_console_wave2_test.go:160-168). Con el código aplastado,
    // `ApiError.isStepUpRequired` —que compara el CÓDIGO (lib/api/errors.ts:71-79)— era falso
    // por construcción, y la consola trataba una ceremonia pendiente como una negativa de rol.
    //
    // Se reusa `parseErrorEnvelope`, que es el parser canónico del cliente compartido
    // (lib/api/client.ts:168): que esta ruta devuelva BINARIO obliga a no usar el cliente JSON,
    // no a re-implementar la lectura del sobre de error. Lo levantó el contraste Codex sol max.
    let parsed: unknown
    try {
      parsed = await response.json()
    } catch {
      // Cuerpo no-JSON: `parseErrorEnvelope` cae al mensaje de reserva.
      parsed = undefined
    }
    const { code, message, details } = parseErrorEnvelope(
      parsed,
      response.statusText || 'Support bundle generation failed',
    )
    throw new ApiError(
      response.status,
      code,
      message,
      response.headers.get('X-Request-ID') ?? undefined,
      details,
      parsed,
    )
  }

  const disposition = response.headers.get('Content-Disposition') ?? ''
  // Built via the RegExp constructor, not a regex literal: the export
  // scrubber's lexer has no regex concept, and a literal containing an odd
  // number of double quotes desynchronizes its string tracking for the whole
  // rest of the file (which un-scrubs later comments).
  const filenameMatch = new RegExp('filename="([^"]+)"', 'i').exec(disposition)
  return {
    blob: await response.blob(),
    filename:
      filenameMatch?.[1] ??
      `olivares-support-${new Date().toISOString().slice(0, 10)}.tar.gz`,
  }
}

/** Save a fetched support bundle using the browser's native download affordance. */
export function downloadSupportBundle(bundle: FetchedSupportBundle): void {
  const url = URL.createObjectURL(bundle.blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = bundle.filename
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
  URL.revokeObjectURL(url)
}

function bindingApplyResult(
  status: number,
  data: SourceScopeBindingDTO | PostureRequestDTO,
): BindingApplyResult {
  if (status === 202) {
    return {
      kind: 'posture_request',
      status: 202,
      posture_request: data as PostureRequestDTO,
    }
  }
  return {
    kind: 'binding',
    status: status === 201 ? 201 : 200,
    binding: data as SourceScopeBindingDTO,
  }
}

function bindingDeleteResult(
  status: number,
  data: PostureRequestDTO | undefined,
): BindingDeleteResult {
  if (status === 202) {
    return {
      kind: 'posture_request',
      status: 202,
      posture_request: data as PostureRequestDTO,
    }
  }
  return { kind: 'deleted', status: 204 }
}

// --- endpoints ---------------------------------------------------------------

export const consoleApi = {
  // People: onboarding + invites.
  onboard: (input: OnboardInput) =>
    http.post<OnboardResult>('/v1/onboard', input),
  listInvites: () =>
    http.get<ListResponse<InviteDTO>>('/v1/invites', {
      query: { limit: EVIDENCE_PAGE },
    }),
  revokeInvite: (id: string) =>
    http.delete<void>(`/v1/invites/${encodeURIComponent(id)}`),
  listMembers: () =>
    http.get<ListResponse<RosterMemberDTO>>('/v1/members', {
      query: { limit: EVIDENCE_PAGE },
    }),
  setMemberActive: (userId: string, active: boolean) =>
    http.patch<unknown>(`/v1/scim/v2/Users/${encodeURIComponent(userId)}`, {
      schemas: ['urn:ietf:params:scim:api:messages:2.0:PatchOp'],
      Operations: [{ op: 'replace', path: 'active', value: active }],
    }),

  // Internal-superadmin lifecycle (global, superadmin-gated; writes need an
  // AAL3 step-up). List the superadmin accounts (with active/inactive status) and
  // enable/disable one. Non-destructive: disabling marks the account inactive and
  // revokes its credentials; the backend is deny-closed against disabling the last
  // ACTIVE superadmin (`last_superadmin`, 409).
  listSuperadmins: () =>
    http.get<ListResponse<OnboardedUser>>('/v1/users/superadmins', {
      query: { limit: EVIDENCE_PAGE },
    }),
  setSuperadminActive: (id: string, active: boolean) =>
    http.post<OnboardedUser>(
      `/v1/users/${encodeURIComponent(id)}/${active ? 'enable' : 'disable'}`,
    ),

  // Scopes: workspaces + agent-groups.
  listWorkspaces: (opts?: { limit?: number }) =>
    http.get<ListResponse<WorkspaceDTO>>('/v1/workspaces', {
      query: { ...opts, limit: opts?.limit ?? EVIDENCE_PAGE },
    }),
  /** UN workspace, PREGUNTADO por su ID. ⛔ El nombre dice `ByID` y no `get` a secas
   *  porque la ruta es `GET /v1/workspaces/{id}` y el handler hace
   *  `Workspaces().Get(ctx, id)`: **no hay resolución por slug en ningún punto**. Un
   *  comentario anterior decía «acepta id o slug» y era una afirmación sin comprobar —
   *  pasarle un slug devuelve 404, no el workspace. Existe porque buscar el id dentro de
   *  `listWorkspaces()` —que sirve UNA pagina— devolvia `undefined` en cuanto el
   *  workspace no cabia, y el llamante caia al id crudo como filtro: la consulta
   *  siguiente no casaba nada y la pantalla afirmaba «no hay conectores». */
  getWorkspaceByID: (id: string) =>
    http.get<WorkspaceDTO>(`/v1/workspaces/${encodeURIComponent(id)}`),
  createWorkspace: (input: { name: string; slug: string }) =>
    http.post<WorkspaceDTO>('/v1/workspaces', input),
  updateWorkspace: (id: string, input: { name?: string; status?: string }) =>
    http.patch<WorkspaceDTO>(`/v1/workspaces/${encodeURIComponent(id)}`, input),

  listAgentGroups: (opts?: { workspace_id?: string; limit?: number }) =>
    http.get<ListResponse<AgentGroupDTO>>('/v1/agent-groups', {
      query: { ...opts, limit: opts?.limit ?? EVIDENCE_PAGE },
    }),
  createAgentGroup: (input: {
    name: string
    slug: string
    workspace_id?: string
    description?: string
    status?: 'active' | 'inactive'
  }) => http.post<AgentGroupDTO>('/v1/agent-groups', input),
  updateAgentGroup: (id: string, input: AgentGroupUpdateInput) =>
    http.patch<AgentGroupDTO>(
      `/v1/agent-groups/${encodeURIComponent(id)}`,
      input,
    ),
  deleteAgentGroup: (id: string) =>
    http.delete<void>(`/v1/agent-groups/${encodeURIComponent(id)}`),
  listAgentGroupMembers: (id: string) =>
    http.get<ListResponse<AgentGroupMemberDTO>>(
      `/v1/agent-groups/${encodeURIComponent(id)}/members`,
      { query: { limit: EVIDENCE_PAGE } },
    ),
  addAgentGroupMember: (id: string, agentID: string) =>
    http.put<AgentGroupMemberDTO>(
      `/v1/agent-groups/${encodeURIComponent(id)}/members/${encodeURIComponent(
        agentID,
      )}`,
    ),
  removeAgentGroupMember: (id: string, agentID: string) =>
    http.delete<void>(
      `/v1/agent-groups/${encodeURIComponent(id)}/members/${encodeURIComponent(
        agentID,
      )}`,
    ),

  // SSO/IdP managed config, superadmin-gated. `scope` = a tenant id targets that
  // tenant's IdP (the per-tenant surface U6); omitted = the deployment-wide
  // global config. `alias` selects one IdP within the scope (U4); omitted /
  // "default" = the scope's primary IdP (the base surface).
  listIdPs: (scope?: string) =>
    http.get<{ idps: SSOConfigDTO[] }>(`${ssoConfigPath(scope)}/idps`),
  getSSO: (scope?: string, alias?: string) =>
    http.get<SSOConfigDTO>(ssoConfigPath(scope, alias)),
  putSSO: (input: SSOConfigInput, scope?: string, alias?: string) =>
    http.put<SSOConfigDTO>(ssoConfigPath(scope, alias), input),
  deleteSSO: (scope?: string, alias?: string) =>
    http.delete<void>(ssoConfigPath(scope, alias)),
  testSSO: (input: SSOConfigInput, scope?: string, alias?: string) =>
    http.post<{ ok: boolean }>(`${ssoConfigPath(scope, alias)}/test`, input),

  // Sealed runtime secret store (global, superadmin-gated). Values are never
  // returned; PUT is used for both create and rotate (rotate = set a new value).
  listSecrets: () => http.get<SecretsListDTO>('/v1/console/secrets'),
  putSecret: (input: SecretInput) =>
    http.put<SecretDTO>('/v1/console/secrets', input),
  deleteSecret: (name: string) =>
    http.delete<void>('/v1/console/secrets', { name }),

  // Connector onboarding (global, superadmin-gated). The catalog feeds the
  // descriptor-driven form; putConnector seals inline credentials + applies live.
  listConnectors: () =>
    http.get<{ connectors: ConnectorInfo[] }>('/v1/console/connectors'),
  listSources: () =>
    http.get<{ sources: SourceRosterEntry[] }>('/v1/console/sources'),
  putConnector: (input: ConnectorOnboardInput) =>
    http.put<SourceApplyResult>('/v1/console/connectors', input),
  testConnector: (input: ConnectorOnboardInput) =>
    http.post<{ ok: boolean }>('/v1/console/connectors/test', input),
  deleteConnector: (name: string) =>
    http.delete<SourceApplyResult>('/v1/console/connectors', { name }),
  // Reconcile the LIVE source roster against the durable store (and best-effort
  // re-resolve the license). Disruptive: removed/rotated sources are torn down live.
  reloadRuntime: () =>
    http.post<SourceReloadReport>('/v1/console/runtime/reload', {}),

  //connector→workspace assignments (tenant-resident, module route).
  // ⛔ `limit` NO ES OPCIONAL DE HECHO, y es la MISMA trampa que dejó documentada para
  //    `guard-postures`: el handler devuelve `has_more` sin drenar el cursor
  //    (`modules/sourcescope/assignment.go:109`) y el repositorio genérico pagina a 100
  //    (`core/internal/store/sqlstore/generic.go:28`). Pedir el máximo reduce la ventana; NO la
  //    cierra, y por eso quien consuma esto tiene que mirar `has_more` además.
  listAssignments: (params?: {
    connector_name?: string
    workspace_ref?: string
    limit?: number
  }) =>
    http.get<ListResponse<ConnectorAssignmentDTO>>(
      `${SOURCESCOPE}/assignments`,
      { query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE } },
    ),
  createAssignment: (input: ConnectorAssignmentDTO) =>
    http.post<ConnectorAssignmentDTO>(`${SOURCESCOPE}/assignments`, input),
  updateAssignment: (id: string, input: Partial<ConnectorAssignmentDTO>) =>
    http.put<ConnectorAssignmentDTO>(
      `${SOURCESCOPE}/assignments/${encodeURIComponent(id)}`,
      input,
    ),
  deleteAssignment: (id: string) =>
    http.delete<void>(`${SOURCESCOPE}/assignments/${encodeURIComponent(id)}`),

  /** Previsualización del efecto de enlace: qué decidiría el resolvedor para este par.
   *  El motor la describe como «the console's binding-effect preview» — se construyó para
   *  esta pantalla y hasta hoy no la llamaba nadie. */
  resolvePreview: (params: {
    source_type: SourceScopeSourceType
    source_ref: string
    actor_kind: SourceScopeActorKind
    actor_ref: string
  }) =>
    http.get<SourceScopeResolvePreview>(`${SOURCESCOPE}/resolve`, {
      query: params,
    }),

  //workspace-scoped connectors (tenant-resident, module route).
  // ⛔ `limit?: number` DECLARADO. La reconstruccion de F-03 hace que esta ruta LEA
  //    `params?.limit`, y la firma no lo tenia: TS2339, que `tsc --noEmit -p tsconfig.json` NO
  //    ve —el tsconfig raiz es una SOLUCION con `references` y sin ficheros propios, asi que sale
  //    rc 0 sin mirar nada. Lo que compila de verdad es `tsc -b`. Lo cazo the reviewer al re-verificar.
  listWsConnectors: (params?: {
    workspace_ref?: string
    kind?: string
    limit?: number
  }) =>
    http.get<ListResponse<WorkspaceConnectorDTO>>(
      `${SOURCESCOPE}/workspace-connectors`,
      { query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE } },
    ),
  createWsConnector: (input: WorkspaceConnectorDTO) =>
    http.post<WorkspaceConnectorDTO>(
      `${SOURCESCOPE}/workspace-connectors`,
      input,
    ),
  getWsConnector: (id: string) =>
    http.get<WorkspaceConnectorDTO>(
      `${SOURCESCOPE}/workspace-connectors/${encodeURIComponent(id)}`,
    ),
  updateWsConnector: (id: string, input: Partial<WorkspaceConnectorDTO>) =>
    http.put<WorkspaceConnectorDTO>(
      `${SOURCESCOPE}/workspace-connectors/${encodeURIComponent(id)}`,
      input,
    ),
  deleteWsConnector: (id: string) =>
    http.delete<void>(
      `${SOURCESCOPE}/workspace-connectors/${encodeURIComponent(id)}`,
    ),

  //source-scope bindings, resources and dual-control posture queue.
  listResources: (params?: SourceScopeResourceListParams) =>
    http.get<ListResponse<ResourceNodeDTO>>(`${SOURCESCOPE}/resources`, {
      query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE },
    }),
  listBindings: (params?: SourceScopeBindingListParams) =>
    http.get<ListResponse<SourceScopeBindingDTO>>(`${SOURCESCOPE}/bindings`, {
      query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE },
    }),
  getBinding: (id: string) =>
    http.get<SourceScopeBindingDTO>(
      `${SOURCESCOPE}/bindings/${encodeURIComponent(id)}`,
    ),
  createBinding: async (input: SourceScopeBindingDTO) => {
    const { status, data } = await http.postWithMeta<
      SourceScopeBindingDTO | PostureRequestDTO
    >(`${SOURCESCOPE}/bindings`, input)
    return bindingApplyResult(status, data)
  },
  updateBinding: async (id: string, input: SourceScopeBindingDTO) => {
    const { status, data } = await http.putWithMeta<
      SourceScopeBindingDTO | PostureRequestDTO
    >(`${SOURCESCOPE}/bindings/${encodeURIComponent(id)}`, input)
    return bindingApplyResult(status, data)
  },
  deleteBinding: async (id: string) => {
    const { status, data } = await http.deleteWithMeta<
      PostureRequestDTO | undefined
    >(`${SOURCESCOPE}/bindings/${encodeURIComponent(id)}`)
    return bindingDeleteResult(status, data)
  },
  disableScoping: (input: {
    source_type: SourceScopeSourceType
    source_ref: string
  }) =>
    http.post<PostureRequestDTO>(
      `${SOURCESCOPE}/sources/disable-scoping`,
      input,
    ),
  // ⛔ `limit` NO ES OPCIONAL DE HECHO. Sin él el repositorio genérico pagina a 100
  //    (`core/internal/store/sqlstore/generic.go:28`) y el handler hace UNA sola llamada a
  //    `repo.List` sin drenar el cursor (`modules/sourcescope/guardposture.go:112-121`). Pedir el
  //    máximo (`maxLimit`, :29) reduce la ventana; NO la cierra, y por eso quien lea esto tiene
  //    que mirar tambien `has_more` en el consumidor.
  listGuardPostures: (params?: {
    source_type?: SourceScopeSourceType
    source_ref?: string
    profile?: string
    limit?: number
  }) =>
    http.get<ListResponse<GuardPostureDTO>>(`${SOURCESCOPE}/guard-postures`, {
      query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE },
    }),
  listPostureRequests: (params?: PostureRequestListParams) =>
    http.get<ListResponse<PostureRequestDTO>>(
      `${SOURCESCOPE}/posture-requests`,
      { query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE } },
    ),
  getPostureRequest: (id: string) =>
    http.get<PostureRequestDTO>(
      `${SOURCESCOPE}/posture-requests/${encodeURIComponent(id)}`,
    ),
  approvePostureRequest: (id: string) =>
    http.post<PostureRequestDTO>(
      `${SOURCESCOPE}/posture-requests/${encodeURIComponent(id)}/approve`,
    ),
  rejectPostureRequest: (id: string) =>
    http.post<PostureRequestDTO>(
      `${SOURCESCOPE}/posture-requests/${encodeURIComponent(id)}/reject`,
    ),

  // Roles & delegation (scoped administration).
  rbacCatalog: () => http.get<RBACCatalogDTO>(`${RBAC}/catalog`),
  delegationAuthority: () =>
    http.get<DelegationAuthorityDTO>(`${RBAC}/delegation-authority`),

  listRoles: () =>
    http.get<ListResponse<CustomRoleDTO>>(`${RBAC}/roles`, {
      query: { limit: EVIDENCE_PAGE },
    }),
  createRole: (input: CustomRoleDTO) =>
    http.post<CustomRoleDTO>(`${RBAC}/roles`, input),
  updateRole: (name: string, input: CustomRoleDTO) =>
    http.put<CustomRoleDTO>(`${RBAC}/roles/${encodeURIComponent(name)}`, input),
  deleteRole: (name: string) =>
    http.delete<void>(`${RBAC}/roles/${encodeURIComponent(name)}`),

  listPermGroups: () =>
    http.get<ListResponse<PermGroupDTO>>(`${RBAC}/permission-groups`, {
      query: { limit: EVIDENCE_PAGE },
    }),
  createPermGroup: (input: PermGroupDTO) =>
    http.post<PermGroupDTO>(`${RBAC}/permission-groups`, input),
  updatePermGroup: (name: string, input: PermGroupDTO) =>
    http.put<PermGroupDTO>(
      `${RBAC}/permission-groups/${encodeURIComponent(name)}`,
      input,
    ),
  deletePermGroup: (name: string) =>
    http.delete<void>(`${RBAC}/permission-groups/${encodeURIComponent(name)}`),

  listGrants: () =>
    http.get<ListResponse<ScopedGrantDTO>>(`${RBAC}/grants`, {
      query: { limit: EVIDENCE_PAGE },
    }),
  createGrant: (input: ScopedGrantDTO) =>
    http.post<ScopedGrantDTO>(`${RBAC}/grants`, input),
  revokeGrant: (id: string) =>
    http.delete<void>(`${RBAC}/grants/${encodeURIComponent(id)}`),

  // Live edition / license (global, superadmin-gated; writes need an AAL3
  // step-up). getLicense reads the live status + seat usage; installLicense verifies,
  // persists and hot-applies (acknowledge confirms a seat downgrade); uninstallLicense
  // removes it and reverts to community.
  getLicense: () => http.get<LicenseStatusDTO>('/v1/console/license'),
  installLicense: (input: { license: string; acknowledge?: boolean }) =>
    http.post<LicenseStatusDTO>('/v1/console/license', input),
  uninstallLicense: (acknowledge: boolean) =>
    http.delete<LicenseStatusDTO>(
      `/v1/console/license${acknowledge ? '?acknowledge=true' : ''}`,
    ),

  // Enterprise activation (global, superadmin-gated; apply needs an AAL3
  // step-up). getActivation reads per-add-on state; previewActivation returns the
  // enable diff (no writes); applyActivation enables/disables a preset or promotes
  // a staged add-on. Enterprise-only — 501 in the community build.
  getActivation: () => http.get<ActivationStatusDTO>('/v1/console/activation'),
  previewActivation: (preset: string) =>
    http.post<ActivationPlanDTO>('/v1/console/activation/preview', { preset }),
  applyActivation: (input: ActivationApplyInput) =>
    http.post<ActivationStatusDTO>('/v1/console/activation/apply', input),

  // S256: provisioned groups (the IdP-pushed groups and their hierarchy).
  listGroups: () => http.get<{ groups: GroupDTO[] }>('/v1/groups'),
  // The verb that turns a directory group into real authority. The console
  // rendered mapped_role read-only, so an operator could SEE the mapping but
  // never create or clear it — the one write that matters most on this screen.
  // An empty role clears the mapping (the server treats clearing as narrowing,
  // so it needs no role ceiling).
  setGroupRole: (id: string, role: string) =>
    http.put<{ id: string; display_name: string; mapped_role: string }>(
      `/v1/groups/${encodeURIComponent(id)}/role`,
      { role },
    ),
  setGroupParent: (id: string, parentId: string) =>
    http.put<{ id: string; display_name: string; parent_group_id: string }>(
      `/v1/groups/${encodeURIComponent(id)}/parent`,
      { parent_id: parentId },
    ),

  // Agent-axis picker: read-only roster for binding authoring.
  listAgents: (
    params?: {
      tenant?: string
      workspace_id?: string
      limit?: number
      cursor?: string
    },
    options?: TenantRequestOptions,
  ) =>
    http.get<ListResponse<AgentDTO>>('/v1/agents', {
      ...options,
      query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE },
    }),
  getAgent: (id: string) =>
    http.get<AgentDTO>(`/v1/agents/${encodeURIComponent(id)}`),
  createAgent: (input: AgentInput) => http.post<AgentDTO>('/v1/agents', input),
  updateAgent: (id: string, input: AgentInput) =>
    http.patch<AgentDTO>(`/v1/agents/${encodeURIComponent(id)}`, input),
  deleteAgent: (id: string) =>
    http.delete<void>(`/v1/agents/${encodeURIComponent(id)}`),

  //model-groups + model-access rules (under /v1/m/models/).
  listModelGroups: ({ tenant, signal, query }: TenantListOptions) =>
    http.get<ListResponse<ModelGroupDTO>>(`${MODELS}/model-groups`, {
      tenant,
      signal,
      query: { ...query, limit: EVIDENCE_PAGE },
    }),
  createModelGroup: (input: ModelGroupDTO, options: TenantRequestOptions) =>
    http.post<ModelGroupDTO>(`${MODELS}/model-groups`, input, options),
  updateModelGroup: (
    id: string,
    input: ModelGroupDTO,
    options: TenantRequestOptions,
  ) =>
    http.put<ModelGroupDTO>(
      `${MODELS}/model-groups/${encodeURIComponent(id)}`,
      input,
      options,
    ),
  deleteModelGroup: (id: string, options: TenantRequestOptions) =>
    http.delete<void>(
      `${MODELS}/model-groups/${encodeURIComponent(id)}`,
      undefined,
      options,
    ),
  listModelAccess: ({ tenant, signal, query }: TenantListOptions) =>
    http.get<ListResponse<ModelAccessDTO>>(`${MODELS}/model-access`, {
      tenant,
      signal,
      query: { ...query, limit: EVIDENCE_PAGE },
    }),
  createModelAccess: (input: ModelAccessDTO, options: TenantRequestOptions) =>
    http.post<ModelAccessDTO>(`${MODELS}/model-access`, input, options),
  updateModelAccess: (
    id: string,
    input: ModelAccessDTO,
    options: TenantRequestOptions,
  ) =>
    http.put<ModelAccessDTO>(
      `${MODELS}/model-access/${encodeURIComponent(id)}`,
      input,
      options,
    ),
  deleteModelAccess: (id: string, options: TenantRequestOptions) =>
    http.delete<void>(
      `${MODELS}/model-access/${encodeURIComponent(id)}`,
      undefined,
      options,
    ),

  //AuthZEN search (reverse queries) + access-review export.
  searchSubjects: (input: AuthZenSearchRequest) =>
    http.post<AuthZenSearchResponse>('/access/v1/search/subject', input),
  searchResources: (input: AuthZenSearchRequest) =>
    http.post<AuthZenSearchResponse>('/access/v1/search/resource', input),
  accessReviewExport: (input: AccessReviewRequest) =>
    http.post<AccessReviewPack>('/access/v1/access-review/export', input),

  //API token management (programmatic credentials).
  /** ⛔ EL CUERPO TIRABA TODO SALVO `include_revoked`, así que añadir `limit?` al TIPO no habría
   *  bastado: el techo se habría quedado en la firma sin llegar nunca a la URL. Es la misma
   *  trampa que la clave que dice una cosa y la llamada manda otra, pero dentro del cliente. */
  listTokens: (params?: { include_revoked?: boolean; limit?: number }) =>
    http.get<ListResponse<TokenDTO>>('/v1/tokens', {
      query: {
        limit: EVIDENCE_PAGE,
        ...(params?.include_revoked ? { include_revoked: 'true' } : {}),
        ...(params?.limit ? { limit: params.limit } : {}),
      },
    }),
  issueToken: (input: IssueTokenInput) =>
    http.post<IssueTokenResult>('/v1/tokens', input),
  revokeToken: (id: string) =>
    http.delete<void>(`/v1/tokens/${encodeURIComponent(id)}`),
  rotateToken: (id: string) =>
    http.post<RotateTokenResult>(`/v1/tokens/${encodeURIComponent(id)}/rotate`),

  //operational console (setup wizard + health dashboard).
  setupStatus: () => http.get<SetupStatusDTO>('/v1/console/setup-status'),
  healthSummary: () => http.get<HealthSummaryDTO>('/v1/console/health-summary'),
  keyCustody: () => http.get<KeyCustodyDTO>('/v1/console/keys'),
  effectiveConfig: () =>
    http.get<EffectiveConfigDTO>('/v1/console/config/effective'),
  busSnapshot: () => http.get<BusSnapshotDTO>('/v1/console/bus'),
  updateCheck: () => http.post<UpdateStatusDTO>('/v1/console/update-check'),
  supportBundle: () => fetchSupportBundle(),
}

// --- query keys (tenant-scoped where the data is tenant-resident) ------------

export const consoleKeys = {
  invites: (t: string | null) => ['console', t, 'invites'] as const,
  members: (t: string | null) => ['console', t, 'members'] as const,
  // Superadmins are GLOBAL principals (not tenant-scoped), so the key carries no tenant.
  superadmins: () => ['console', 'superadmins'] as const,
  workspace: (t: string | null, ref: string) =>
    ['console', t, 'workspace', ref] as const,
  workspaces: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['console', t, 'workspaces'] as const)
      : (['console', t, 'workspaces', params] as const),
  agentGroups: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['console', t, 'agentGroups'] as const)
      : (['console', t, 'agentGroups', params] as const),
  agentGroupMembers: (t: string | null, groupId: string) =>
    ['console', t, 'agentGroups', groupId, 'members'] as const,
  // SSO config: the deployment-wide global config, or a specific tenant's IdP
  // (U6) — the scope keys the cache so switching scope refetches; the alias
  // (U4) keys the specific IdP within the scope.
  sso: (scope?: string, alias?: string) =>
    ['console', 'sso', scope ?? 'global', alias ?? 'default'] as const,
  // The list of IdPs configured under a scope (U4).
  ssoIdps: (scope?: string) =>
    ['console', 'sso-idps', scope ?? 'global'] as const,
  // The sealed secret store is deployment-global, not tenant-scoped.
  secrets: () => ['console', 'secrets'] as const,
  // Connector onboarding: catalog + configured sources, deployment-global.
  connectors: () => ['console', 'connectors'] as const,
  sources: () => ['console', 'sources'] as const,
  //workspace connector scoping (tenant-resident).
  connectorAssignments: (t: string | null) =>
    ['console', t, 'connectorAssignments'] as const,
  workspaceConnectors: (t: string | null) =>
    ['console', t, 'workspaceConnectors'] as const,
  //source-scope bindings/resources/posture (tenant-resident).
  resources: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['console', t, 'sourcescope', 'resources'] as const)
      : (['console', t, 'sourcescope', 'resources', params] as const),
  bindings: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['console', t, 'sourcescope', 'bindings'] as const)
      : (['console', t, 'sourcescope', 'bindings', params] as const),
  sourceScopeResolve: (
    t: string | null,
    params: {
      source_type: string
      source_ref: string
      actor_kind: string
      actor_ref: string
    },
  ) => ['console', t, 'sourcescope', 'resolve', params] as const,
  binding: (t: string | null, id: string) =>
    ['console', t, 'sourcescope', 'binding', id] as const,
  guardPostures: (t: string | null) =>
    ['console', t, 'sourcescope', 'guardPostures'] as const,
  assignments: (t: string | null) =>
    ['console', t, 'sourcescope', 'assignments'] as const,
  postureRequests: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['console', t, 'sourcescope', 'postureRequests'] as const)
      : (['console', t, 'sourcescope', 'postureRequests', params] as const),
  postureRequest: (t: string | null, id: string) =>
    ['console', t, 'sourcescope', 'postureRequest', id] as const,
  agents: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['console', t, 'agents'] as const)
      : (['console', t, 'agents', params] as const),
  agent: (t: string | null, id: string) =>
    ['console', t, 'agents', id] as const,
  // Roles & delegation (tenant-resident; the catalog is global enum data).
  rbacCatalog: () => ['console', 'rbac', 'catalog'] as const,
  delegationAuthority: (t: string | null) =>
    ['console', t, 'delegationAuthority'] as const,
  roles: (t: string | null) => ['console', t, 'roles'] as const,
  permGroups: (t: string | null) => ['console', t, 'permGroups'] as const,
  grants: (t: string | null) => ['console', t, 'grants'] as const,
  // The edition/license is deployment-global, not tenant-scoped.
  license: () => ['console', 'license'] as const,
  //enterprise activation is deployment-global.
  activation: () => ['console', 'activation'] as const,
  // S256: provisioned groups (tenant-resident).
  groups: (t: string | null) => ['console', t, 'groups'] as const,
  //model-groups + model-access (tenant-resident).
  modelGroups: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['console', t, 'modelGroups'] as const)
      : (['console', t, 'modelGroups', params] as const),
  modelAccess: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['console', t, 'modelAccess'] as const)
      : (['console', t, 'modelAccess', params] as const),
  //API tokens (tenant-scoped for bound tokens; global for superadmin).
  tokens: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['console', t, 'tokens'] as const)
      : (['console', t, 'tokens', params] as const),
  //operational endpoints (deployment-global).
  setupStatus: () => ['console', 'setupStatus'] as const,
  healthSummary: () => ['console', 'healthSummary'] as const,
  keyCustody: () => ['console', 'keyCustody'] as const,
  effectiveConfig: () => ['console', 'effectiveConfig'] as const,
  busSnapshot: () => ['console', 'busSnapshot'] as const,
}
