// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import "github.com/olivaresai/olivares/core/model"

// This file catalogs the engine's authentication/authorization entities,
// in the same descriptor+codec style as catalog.go. They are core entities, so
// the engine generates their tables, injects the base columns, and attaches the
// unconditional tenant/append-only guards — exactly like every other core
// entity. Every auth row lives in the reserved system tenant (its
// BaseFields.TenantID is SystemTenantID); the GRANTED/BOUND tenant is a separate
// column (target_tenant_id / bound_tenant_id), never the isolation row. PEP
// services follow the same pattern: TargetTenantID is their business tenant.
//
// These tables are reached only through the engine's auth partition (AuthScope,
// see authscope.go), which binds SystemTenantID as a normal RLS-enforced scope.
// They are NOT reachable through Scope.Ext (which rejects the core namespace) nor
// through the module-facing typed Scope accessors, so a module can never read a
// credential. Auditing is OFF at the row level on purpose: the API records
// semantic audit events with the real principal as actor (the maybeAudit path
// would hardcode "system"); see core/audit.

// authDescriptors are appended to coreDescriptors() so the engine generates and
// guards their tables. Order is irrelevant (no DB-level foreign keys).
func authDescriptors() []model.EntityDescriptor {
	return []model.EntityDescriptor{
		userDescriptor, membershipDescriptor, userGroupDescriptor, userGroupMemberDescriptor,
		authSessionDescriptor, apiTokenDescriptor, webauthnCredentialDescriptor, userInviteDescriptor,
		federationConfigDescriptor, federationDomainClaimDescriptor, secretEntryDescriptor, sourceDefDescriptor,
		setSeenJTIDescriptor,
		pepServiceDescriptor, pepServiceCredentialDescriptor, delegationHandleDescriptor, pdpDecisionClaimDescriptor,
	}
}

// encTenant encodes a required tenant id to its canonical text.
func encTenant(t model.TenantID) any { return t.String() }

// encOptTenant encodes an optional tenant id, storing nil when zero.
func encOptTenant(t model.TenantID) any {
	if t.IsZero() {
		return nil
	}
	return t.String()
}

// decTenant reads a tenant-id column.
func decTenant(rec model.Record, col string) model.TenantID {
	return model.TenantID(rec.String(col))
}

// --- User --------------------------------------------------------------------

var userDescriptor = model.EntityDescriptor{
	Kind:  "core.user",
	Table: "users",
	Fields: []model.FieldSpec{
		field("email", model.KindText, false),
		field("display_name", model.KindText, true),
		field("status", model.KindText, false),
		field("password_hash", model.KindText, true),
		field("is_superadmin", model.KindBool, false),
		// SCIM externalId (RFC 7643): the provisioning IdP's stable id. Nullable
		// (local users have none); indexed so SCIM can correlate by externalId eq.
		// Added post-v2 via the additive reconcile.
		indexedField("external_id", model.KindText, true),
		// SCIM enterprise User extension attributes (RFC 7643 §4.3). All nullable
		// and appended last so the additive reconcile (schema.go) issues the
		// ALTER TABLE ADD COLUMN on an existing DB and v2 regenerates them on a
		// fresh one — no hand-authored migration. Non-secret directory metadata.
		field("employee_number", model.KindText, true),
		field("department", model.KindText, true),
		field("manager", model.KindText, true),
		// sso_subject is the issuer-qualified SSO login-correlation key (U3),
		// "<issuer>\x1f<subject>". Nullable and appended last for the additive
		// reconcile (ALTER TABLE ADD COLUMN on an existing DB, v2 regenerates it on a
		// fresh one — no hand-authored migration). Distinct from external_id: that is
		// SCIM's unqualified key, this qualifies the subject by its issuing IdP.
		field("sso_subject", model.KindText, true),
	},
	// Email is globally unique among users; the index leads with tenant_id so it
	// obeys the tenant-isolation rule even though every user shares SystemTenantID.
	Indexes: []model.IndexSpec{
		{Name: "users_email_uniq", Columns: []string{"tenant_id", "email"}, Unique: true},
		// sso_subject is UNIQUE per the issuing IdP: two accounts can never claim the
		// same "<issuer>\x1f<subject>", so a federated login resolves to exactly one
		// user. The column is nullable and a UNIQUE index treats NULLs as DISTINCT on
		// both SQLite and Postgres (NULLS DISTINCT is the default), so it behaves as a
		// partial "WHERE sso_subject IS NOT NULL" index — every local/password/SCIM-only
		// account (NULL subject) coexists freely; only issuer-qualified subjects are
		// constrained. Leads with tenant_id for the isolation rule (all users share
		// SystemTenantID), matching users_email_uniq.
		{Name: "users_sso_subject_uniq", Columns: []string{"tenant_id", "sso_subject"}, Unique: true},
	},
}

var userCodec = model.Codec[model.User]{
	Base: func(u *model.User) *model.BaseFields { return &u.BaseFields },
	Encode: func(u model.User) (model.Record, error) {
		// An absent sso_subject is stored as NULL, not "": the unique index must let
		// every local/password/SCIM-only account (no federated subject) coexist, and
		// NULLs never collide in a unique index on either engine — the same discipline
		// as user_groups.external_id. (external_id below is NOT nil-normalized: its
		// users index is non-unique, so an empty "" there is harmless.)
		var ssoSub any
		if u.SsoSubject != "" {
			ssoSub = u.SsoSubject
		}
		return model.Record{
			"email": u.Email, "display_name": u.DisplayName, "status": string(u.Status),
			"password_hash": u.PasswordHash, "is_superadmin": u.IsSuperadmin,
			"external_id":     u.ExternalID,
			"employee_number": u.EmployeeNumber, "department": u.Department, "manager": u.Manager,
			"sso_subject": ssoSub,
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.User, error) {
		return model.User{BaseFields: b, Email: r.String("email"), DisplayName: r.String("display_name"),
			Status: model.LifecycleStatus(r.String("status")), PasswordHash: r.String("password_hash"),
			IsSuperadmin: r.Bool("is_superadmin"), ExternalID: r.String("external_id"),
			EmployeeNumber: r.String("employee_number"), Department: r.String("department"), Manager: r.String("manager"),
			SsoSubject: r.String("sso_subject")}, nil
	},
}

// --- Membership --------------------------------------------------------------

var membershipDescriptor = model.EntityDescriptor{
	Kind:  "core.membership",
	Table: "memberships",
	Fields: []model.FieldSpec{
		indexedField("user_id", model.KindUUID, false),
		field("target_tenant_id", model.KindUUID, false),
		field("role", model.KindText, false),
		// workspace_id OPTIONALLY scopes the membership to one workspace in the
		// granted tenant (FASE X). Nullable and appended last for additive
		// reconcile; NULL is the historical tenant-wide membership. Enforcement is
		// Own the precise index when they query by workspace.
		field("workspace_id", model.KindUUID, true),
	},
	// One membership per (user, granted tenant); enumerated at login by user_id.
	// The unique key is deliberately (user, target_tenant) — NOT including
	// workspace_id — so a user has exactly one membership row per granted tenant;
	// the optional workspace scope narrows that single row, it does not multiply it.
	Indexes: []model.IndexSpec{
		{Name: "memberships_user_target_uniq", Columns: []string{"tenant_id", "user_id", "target_tenant_id"}, Unique: true},
	},
}

var membershipCodec = model.Codec[model.Membership]{
	Base: func(m *model.Membership) *model.BaseFields { return &m.BaseFields },
	Encode: func(m model.Membership) (model.Record, error) {
		return model.Record{
			"user_id": m.UserID.String(), "target_tenant_id": encTenant(m.TargetTenantID), "role": m.Role,
			"workspace_id": encOptID(m.WorkspaceID),
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.Membership, error) {
		return model.Membership{BaseFields: b, UserID: decID(r, "user_id"),
			TargetTenantID: decTenant(r, "target_tenant_id"), Role: r.String("role"),
			WorkspaceID: decID(r, "workspace_id")}, nil
	},
}

// --- UserGroup -----------------------------------------------------------------

var userGroupDescriptor = model.EntityDescriptor{
	Kind:  "core.user_group",
	Table: "user_groups",
	Fields: []model.FieldSpec{
		indexedField("target_tenant_id", model.KindUUID, false),
		// display_name is deliberately NOT unique: Microsoft Entra legally
		// provisions duplicate group names (it correlates by externalId), so its
		// dedupe is application-level in core/auth, never a DB index.
		indexedField("display_name", model.KindText, false),
		indexedField("external_id", model.KindText, true),
		field("mapped_role", model.KindText, true),
		// parent_group_id nests this group under another of the same tenant (S256
		// group hierarchy). Nullable and APPENDED last so a reconciled (pre-S256)
		// table and a freshly-created one agree on column order — the same additive
		// discipline as the assurance columns on auth_sessions.
		field("parent_group_id", model.KindUUID, true),
	},
	// external_id IS unique per granted tenant: it is the IdP's correlation key,
	// and the application-level probe alone is a non-atomic check-then-insert
	// (two concurrent creates both pass the probe on Postgres). Groups without an
	// externalId store NULL (see the codec), and NULLs never collide in a unique
	// index on either engine, so Okta-style no-externalId groups coexist freely.
	Indexes: []model.IndexSpec{
		{Name: "user_groups_external_uniq", Columns: []string{"tenant_id", "target_tenant_id", "external_id"}, Unique: true},
	},
}

var userGroupCodec = model.Codec[model.UserGroup]{
	Base: func(g *model.UserGroup) *model.BaseFields { return &g.BaseFields },
	Encode: func(g model.UserGroup) (model.Record, error) {
		// An absent externalId is stored as NULL, not "": the unique index must
		// let any number of no-externalId groups coexist.
		var ext any
		if g.ExternalID != "" {
			ext = g.ExternalID
		}
		return model.Record{
			"target_tenant_id": encTenant(g.TargetTenantID), "display_name": g.DisplayName,
			"external_id": ext, "mapped_role": g.MappedRole, "parent_group_id": encOptID(g.ParentGroupID),
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.UserGroup, error) {
		return model.UserGroup{BaseFields: b, TargetTenantID: decTenant(r, "target_tenant_id"),
			DisplayName: r.String("display_name"), ExternalID: r.String("external_id"),
			MappedRole: r.String("mapped_role"), ParentGroupID: decID(r, "parent_group_id")}, nil
	},
}

// --- UserGroupMember -----------------------------------------------------------

var userGroupMemberDescriptor = model.EntityDescriptor{
	Kind:  "core.user_group_member",
	Table: "user_group_members",
	Fields: []model.FieldSpec{
		indexedField("group_id", model.KindUUID, false),
		indexedField("user_id", model.KindUUID, false),
	},
	// One row per (group, user); enumerated by group_id (roster) and by user_id
	// (the loadGrants fold).
	Indexes: []model.IndexSpec{
		{Name: "user_group_members_uniq", Columns: []string{"tenant_id", "group_id", "user_id"}, Unique: true},
	},
}

var userGroupMemberCodec = model.Codec[model.UserGroupMember]{
	Base: func(m *model.UserGroupMember) *model.BaseFields { return &m.BaseFields },
	Encode: func(m model.UserGroupMember) (model.Record, error) {
		return model.Record{"group_id": m.GroupID.String(), "user_id": m.UserID.String()}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.UserGroupMember, error) {
		return model.UserGroupMember{BaseFields: b, GroupID: decID(r, "group_id"),
			UserID: decID(r, "user_id")}, nil
	},
}

// --- AuthSession -------------------------------------------------------------

var authSessionDescriptor = model.EntityDescriptor{
	Kind:  "core.auth_session",
	Table: "auth_sessions",
	Fields: []model.FieldSpec{
		indexedField("user_id", model.KindUUID, false),
		field("selector", model.KindText, false),
		field("secret_hash", model.KindBytes, false),
		field("expires_at", model.KindTimestamp, false),
		field("revoked", model.KindBool, false),
		field("created_ip", model.KindText, true),
		// Assurance columns. Nullable (additive reconcile on an existing DB);
		// NULL aal reads as 1 — a pre session is never inflated past AAL1.
		// Appended at the end so fresh and reconciled tables agree on column order.
		field("aal", model.KindInt, true),
		field("amr", model.KindJSON, true),
		field("aal_expires_at", model.KindTimestamp, true),
	},
	Indexes: []model.IndexSpec{
		{Name: "auth_sessions_selector_uniq", Columns: []string{"tenant_id", "selector"}, Unique: true},
	},
}

var authSessionCodec = model.Codec[model.AuthSession]{
	Base: func(s *model.AuthSession) *model.BaseFields { return &s.BaseFields },
	Encode: func(s model.AuthSession) (model.Record, error) {
		amr, err := encStrings(s.AMR)
		if err != nil {
			return nil, err
		}
		return model.Record{
			"user_id": s.UserID.String(), "selector": s.Selector, "secret_hash": encBytes(s.SecretHash),
			"expires_at": encTS(s.ExpiresAt), "revoked": s.Revoked, "created_ip": s.CreatedIP,
			"aal": encOptInt(int64(s.AAL)), "amr": amr, "aal_expires_at": encOptTS(s.AALExpiresAt),
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.AuthSession, error) {
		exp, err := decTS(r, "expires_at")
		if err != nil {
			return model.AuthSession{}, err
		}
		amr, err := decStrings(r, "amr")
		if err != nil {
			return model.AuthSession{}, err
		}
		aalExp, err := decOptTS(r, "aal_expires_at")
		if err != nil {
			return model.AuthSession{}, err
		}
		return model.AuthSession{BaseFields: b, UserID: decID(r, "user_id"), Selector: r.String("selector"),
			SecretHash: r.Bytes("secret_hash"), ExpiresAt: exp, Revoked: r.Bool("revoked"),
			CreatedIP: r.String("created_ip"), AAL: int(r.Int("aal")), AMR: amr, AALExpiresAt: aalExp}, nil
	},
}

// --- WebAuthnCredential --------------------------------------------------------

// webauthnCredentialDescriptor stores a user's registered FIDO2 authenticators
//. Public verifier material only — the credential id and the
// library's full credential record (public key, flags, sign count, attestation);
// never a private key or a challenge. The credential id is unique per tenant
// (WebAuthn requires per-RP uniqueness; every row lives in the system tenant).
var webauthnCredentialDescriptor = model.EntityDescriptor{
	Kind:  "core.webauthn_credential",
	Table: "webauthn_credentials",
	Fields: []model.FieldSpec{
		indexedField("user_id", model.KindUUID, false),
		field("name", model.KindText, true),
		field("credential_id", model.KindText, false),
		field("credential", model.KindJSON, false),
	},
	Indexes: []model.IndexSpec{
		{Name: "webauthn_credentials_id_uniq", Columns: []string{"tenant_id", "credential_id"}, Unique: true},
	},
}

var webauthnCredentialCodec = model.Codec[model.WebAuthnCredential]{
	Base: func(c *model.WebAuthnCredential) *model.BaseFields { return &c.BaseFields },
	Encode: func(c model.WebAuthnCredential) (model.Record, error) {
		return model.Record{
			"user_id": c.UserID.String(), "name": c.Name,
			"credential_id": c.CredentialID, "credential": string(c.Credential),
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.WebAuthnCredential, error) {
		return model.WebAuthnCredential{BaseFields: b, UserID: decID(r, "user_id"), Name: r.String("name"),
			CredentialID: r.String("credential_id"), Credential: []byte(r.String("credential"))}, nil
	},
}

// --- APIToken ----------------------------------------------------------------

var apiTokenDescriptor = model.EntityDescriptor{
	Kind:  "core.api_token",
	Table: "api_tokens",
	Fields: []model.FieldSpec{
		field("name", model.KindText, false),
		// user_id is indexed: the leaver/deprovision path lists a user's tokens to
		// revoke them, and the delegation cascade lists by parent_token_id.
		indexedField("user_id", model.KindUUID, true),
		field("selector", model.KindText, false),
		field("secret_hash", model.KindBytes, false),
		indexedField("bound_tenant_id", model.KindUUID, true),
		field("role", model.KindText, true),
		field("is_superadmin", model.KindBool, false),
		field("expires_at", model.KindTimestamp, true),
		field("revoked", model.KindBool, false),
		field("last_used_at", model.KindTimestamp, true),
		// Delegation columns (RFC 8693/8707); all nullable, populated only on a
		// token-exchange-minted token. Added post-v2 via the additive reconcile.
		field("audience", model.KindText, true),
		field("act_as_user_id", model.KindUUID, true),
		indexedField("parent_token_id", model.KindUUID, true),
		field("scope", model.KindText, true),
		// agent_ref is the external_id of the agent identity this token is delegated
		// to (agent-OBO). Nullable; non-empty only on an agent-OBO exchange.
		field("agent_ref", model.KindText, true),
		// purpose is nullable for additive reconciliation and reads as empty on
		// pre-existing rows. Non-empty values mark credentials reserved for a
		// specialized protocol rather than ordinary API authentication.
		field("purpose", model.KindText, true),
		// session_ref is a server-authored canonical work-session SID. It is
		// nullable so existing API tokens remain agent/user scoped and cannot be
		// mistaken for a session credential after additive reconciliation.
		field("session_ref", model.KindText, true),
		// communication-session stores its complete server-authored binding in
		// dedicated nullable columns. They remain NULL on ordinary and legacy
		// work-session tokens; no binding is encoded into a display name.
		field("workspace_id", model.KindUUID, true),
		field("session_run_ref", model.KindText, true),
		field("session_fence", model.KindInt, true),
	},
	Indexes: []model.IndexSpec{
		{Name: "api_tokens_selector_uniq", Columns: []string{"tenant_id", "selector"}, Unique: true},
	},
}

var apiTokenCodec = model.Codec[model.APIToken]{
	Base: func(t *model.APIToken) *model.BaseFields { return &t.BaseFields },
	Encode: func(t model.APIToken) (model.Record, error) {
		return model.Record{
			"name": t.Name, "user_id": encOptID(t.UserID), "selector": t.Selector,
			"secret_hash": encBytes(t.SecretHash), "bound_tenant_id": encOptTenant(t.BoundTenantID),
			"role": t.Role, "is_superadmin": t.IsSuperadmin, "expires_at": encOptTS(t.ExpiresAt),
			"revoked": t.Revoked, "last_used_at": encOptTS(t.LastUsedAt),
			"audience": t.Audience, "act_as_user_id": encOptID(t.ActAsUserID),
			"parent_token_id": encOptID(t.ParentTokenID), "scope": t.Scope,
			"agent_ref": t.AgentRef, "purpose": encOptStr(t.Purpose),
			"session_ref": encOptStr(t.SessionRef), "workspace_id": encOptID(t.WorkspaceID),
			"session_run_ref": encOptStr(t.SessionRunRef), "session_fence": encOptInt(t.SessionFence),
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.APIToken, error) {
		exp, err := decOptTS(r, "expires_at")
		if err != nil {
			return model.APIToken{}, err
		}
		used, err := decOptTS(r, "last_used_at")
		if err != nil {
			return model.APIToken{}, err
		}
		return model.APIToken{BaseFields: b, Name: r.String("name"), UserID: decID(r, "user_id"),
			Selector: r.String("selector"), SecretHash: r.Bytes("secret_hash"),
			BoundTenantID: decTenant(r, "bound_tenant_id"), Role: r.String("role"),
			IsSuperadmin: r.Bool("is_superadmin"), ExpiresAt: exp, Revoked: r.Bool("revoked"),
			LastUsedAt: used, Audience: r.String("audience"), ActAsUserID: decID(r, "act_as_user_id"),
			ParentTokenID: decID(r, "parent_token_id"), Scope: r.String("scope"),
			AgentRef: r.String("agent_ref"), Purpose: r.String("purpose"),
			SessionRef: r.String("session_ref"), WorkspaceID: decID(r, "workspace_id"),
			SessionRunRef: r.String("session_run_ref"), SessionFence: r.Int("session_fence")}, nil
	},
}

// --- UserInvite --------------------------------------------------------------

// userInviteDescriptor stores pending, single-use onboarding invitations.
// Like every auth row it lives in the system tenant; the invited tenant is the
// target_tenant_id column. Only SHA-256(secret) is stored (secret_hash), never
// the invite token; the selector is the public, indexed lookup key.
var userInviteDescriptor = model.EntityDescriptor{
	Kind:  "core.user_invite",
	Table: "user_invites",
	Fields: []model.FieldSpec{
		field("email", model.KindText, false),
		indexedField("target_tenant_id", model.KindUUID, false),
		field("role", model.KindText, false),
		field("selector", model.KindText, false),
		field("secret_hash", model.KindBytes, false),
		field("expires_at", model.KindTimestamp, false),
		field("accepted_at", model.KindTimestamp, true),
		field("created_by", model.KindText, true),
	},
	// The selector is the unique, indexed lookup key the accept leg resolves.
	Indexes: []model.IndexSpec{
		{Name: "user_invites_selector_uniq", Columns: []string{"tenant_id", "selector"}, Unique: true},
	},
}

var userInviteCodec = model.Codec[model.UserInvite]{
	Base: func(i *model.UserInvite) *model.BaseFields { return &i.BaseFields },
	Encode: func(i model.UserInvite) (model.Record, error) {
		return model.Record{
			"email": i.Email, "target_tenant_id": encTenant(i.TargetTenantID), "role": i.Role,
			"selector": i.Selector, "secret_hash": encBytes(i.SecretHash), "expires_at": encTS(i.ExpiresAt),
			"accepted_at": encOptTS(i.AcceptedAt), "created_by": i.CreatedBy,
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.UserInvite, error) {
		exp, err := decTS(r, "expires_at")
		if err != nil {
			return model.UserInvite{}, err
		}
		acc, err := decOptTS(r, "accepted_at")
		if err != nil {
			return model.UserInvite{}, err
		}
		return model.UserInvite{BaseFields: b, Email: r.String("email"),
			TargetTenantID: decTenant(r, "target_tenant_id"), Role: r.String("role"),
			Selector: r.String("selector"), SecretHash: r.Bytes("secret_hash"),
			ExpiresAt: exp, AcceptedAt: acc, CreatedBy: r.String("created_by")}, nil
	},
}

// --- FederationConfig --------------------------------------------------------

// federationConfigDescriptor stores the managed SSO/IdP configuration.
// One row per scope (target_tenant_id); SystemTenantID is the global config. The
// secret-bearing columns hold SEALED values (never cleartext, never a one-way
// hash); the *_hint columns are non-secret fingerprints for display.
var federationConfigDescriptor = model.EntityDescriptor{
	Kind:  "core.federation_config",
	Table: "federation_configs",
	Fields: []model.FieldSpec{
		indexedField("target_tenant_id", model.KindUUID, false),
		field("protocol", model.KindText, true),
		field("status", model.KindText, false),
		field("oidc_issuer", model.KindText, true),
		field("oidc_client_id", model.KindText, true),
		field("oidc_client_secret_sealed", model.KindText, true),
		field("oidc_client_secret_hint", model.KindText, true),
		field("saml_metadata_url", model.KindText, true),
		field("saml_entity_id", model.KindText, true),
		field("saml_acs_url", model.KindText, true),
		field("saml_idp_sso_url", model.KindText, true),
		field("saml_email_attr", model.KindText, true),
		field("saml_sp_cert_pem", model.KindText, true),
		field("saml_sp_key_sealed", model.KindText, true),
		field("saml_sp_key_hint", model.KindText, true),
		field("saml_sp_sign_cert_pem", model.KindText, true),
		field("saml_sp_sign_key_sealed", model.KindText, true),
		field("saml_sp_sign_key_hint", model.KindText, true),
		// Login-enforcement posture (non-secret operator intent). Nullable and
		// appended LAST so the additive reconcile (schema.go) ALTERs an existing DB and
		// v2 regenerates them on a fresh one — no hand-authored migration. A NULL
		// require_sso reads as false and a NULL list reads as empty, so a pre row
		// decodes to "no enforcement" (the open build never enforced anyway).
		field("require_sso", model.KindBool, true),
		field("network_allow_cidrs", model.KindJSON, true),
		// Group-mapping + JIT coherence (appended LAST — additive reconcile).
		field("oidc_groups_claim", model.KindText, true),
		field("saml_groups_attr", model.KindText, true),
		field("scim_authoritative", model.KindBool, true),
		// U4 first-class IdP entity key (appended LAST — additive reconcile ALTERs
		// an existing DB, v2 regenerates it on a fresh one). Nullable in storage; the
		// codec normalizes NULL/empty → "default", and the boot-time backfill
		// (reconcileCoreData, schema.go) rewrites legacy NULLs so the unique index below
		// enforces one "default" per scope IDENTICALLY on upgraded and fresh databases.
		field("alias", model.KindText, true),
		// U5 home-realm routing: the email domains this IdP claims (globally unique;
		// enforced in the service). Non-secret JSON list, appended LAST — additive reconcile.
		field("claimed_domains", model.KindJSON, true),
	},
	// U4: one config per (scope, alias) — the first-class IdP entity key that lets
	// multiple IdPs coexist under a TargetTenantID. RELAXED from the pre-U4
	// (tenant_id, target_tenant_id) scope-unique index, which the v4 core migration
	// DROPs on an existing DB (schema.go). A NULL alias is DISTINCT on both engines, so
	// the legacy-NULL→"default" backfill is what makes this enforce single-default-per-
	// scope; the service additionally load-then-updates the default so no duplicate can
	// be created even before the backfill runs (federation_config.go).
	Indexes: []model.IndexSpec{
		{Name: "federation_configs_idp_uniq", Columns: []string{"tenant_id", "target_tenant_id", "alias"}, Unique: true},
	},
}

var federationConfigCodec = model.Codec[model.FederationConfig]{
	Base: func(c *model.FederationConfig) *model.BaseFields { return &c.BaseFields },
	Encode: func(c model.FederationConfig) (model.Record, error) {
		cidrs, err := encStrings(c.NetworkAllowCIDRs)
		if err != nil {
			return nil, err
		}
		// Canonicalize domains at the store boundary too (belt-and-suspenders, like alias),
		// so the global-uniqueness comparison holds regardless of how a row was written.
		normDomains := make([]string, 0, len(c.ClaimedDomains))
		for _, d := range c.ClaimedDomains {
			if nd := model.NormalizeFederationDomain(d); nd != "" {
				normDomains = append(normDomains, nd)
			}
		}
		domains, err := encStrings(normDomains)
		if err != nil {
			return nil, err
		}
		return model.Record{
			"target_tenant_id": encTenant(c.TargetTenantID), "alias": model.NormalizeFederationAlias(c.Alias),
			"protocol": c.Protocol, "status": string(c.Status),
			"oidc_issuer": c.OIDCIssuer, "oidc_client_id": c.OIDCClientID,
			"oidc_client_secret_sealed": c.OIDCClientSecretSealed, "oidc_client_secret_hint": c.OIDCClientSecretHint,
			"saml_metadata_url": c.SAMLMetadataURL, "saml_entity_id": c.SAMLEntityID,
			"saml_acs_url": c.SAMLACSURL, "saml_idp_sso_url": c.SAMLIDPSSOURL, "saml_email_attr": c.SAMLEmailAttr,
			"saml_sp_cert_pem": c.SAMLSPCertPEM, "saml_sp_key_sealed": c.SAMLSPKeySealed, "saml_sp_key_hint": c.SAMLSPKeyHint,
			"saml_sp_sign_cert_pem": c.SAMLSPSignCertPEM, "saml_sp_sign_key_sealed": c.SAMLSPSignKeySealed,
			"saml_sp_sign_key_hint": c.SAMLSPSignKeyHint,
			"require_sso":           c.RequireSSO, "network_allow_cidrs": cidrs,
			"oidc_groups_claim": c.OIDCGroupsClaim, "saml_groups_attr": c.SAMLGroupsAttr,
			"scim_authoritative": c.SCIMAuthoritative, "claimed_domains": domains,
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.FederationConfig, error) {
		cidrs, err := decStrings(r, "network_allow_cidrs")
		if err != nil {
			return model.FederationConfig{}, err
		}
		domains, err := decStrings(r, "claimed_domains")
		if err != nil {
			return model.FederationConfig{}, err
		}
		return model.FederationConfig{BaseFields: b, TargetTenantID: decTenant(r, "target_tenant_id"),
			Alias:    model.NormalizeFederationAlias(r.String("alias")),
			Protocol: r.String("protocol"), Status: model.LifecycleStatus(r.String("status")),
			OIDCIssuer: r.String("oidc_issuer"), OIDCClientID: r.String("oidc_client_id"),
			OIDCClientSecretSealed: r.String("oidc_client_secret_sealed"), OIDCClientSecretHint: r.String("oidc_client_secret_hint"),
			SAMLMetadataURL: r.String("saml_metadata_url"), SAMLEntityID: r.String("saml_entity_id"),
			SAMLACSURL: r.String("saml_acs_url"), SAMLIDPSSOURL: r.String("saml_idp_sso_url"),
			SAMLEmailAttr: r.String("saml_email_attr"), SAMLSPCertPEM: r.String("saml_sp_cert_pem"),
			SAMLSPKeySealed: r.String("saml_sp_key_sealed"), SAMLSPKeyHint: r.String("saml_sp_key_hint"),
			SAMLSPSignCertPEM: r.String("saml_sp_sign_cert_pem"), SAMLSPSignKeySealed: r.String("saml_sp_sign_key_sealed"),
			SAMLSPSignKeyHint: r.String("saml_sp_sign_key_hint"),
			RequireSSO:        r.Bool("require_sso"), NetworkAllowCIDRs: cidrs,
			OIDCGroupsClaim: r.String("oidc_groups_claim"), SAMLGroupsAttr: r.String("saml_groups_attr"),
			SCIMAuthoritative: r.Bool("scim_authoritative"), ClaimedDomains: domains}, nil
	},
}

// federationDomainClaimDescriptor is the DERIVED home-realm routing index (U8): one row
// per (config, claimed domain), with a UNIQUE index on the domain that makes a claimed domain
// GLOBALLY unique at the storage layer — every auth row shares SystemTenantID as tenant_id, so
// (tenant_id, domain) unique ⇒ one domain → at most one IdP across every scope, enforced at
// COMMIT regardless of isolation level (hardening U5's app-level scan). It is a projection of
// federation_configs.claimed_domains, maintained transactionally with the config write
// (federation_config.go) and converged at boot (FederationService.ReconcileDomainClaims). A
// NEW table: the additive reconcile creates it — createTableTx WITH this index on an existing
// DB, v2 regen on a fresh one (schema.go) — so no hand-authored migration is needed.
var federationDomainClaimDescriptor = model.EntityDescriptor{
	Kind:  "core.federation_domain_claim",
	Table: "federation_domain_claims",
	Fields: []model.FieldSpec{
		indexedField("target_tenant_id", model.KindUUID, false),
		indexedField("config_id", model.KindUUID, false),
		field("domain", model.KindText, false),
	},
	Indexes: []model.IndexSpec{
		{Name: "federation_domain_claims_domain_uniq", Columns: []string{"tenant_id", "domain"}, Unique: true},
	},
}

var federationDomainClaimCodec = model.Codec[model.FederationDomainClaim]{
	Base: func(c *model.FederationDomainClaim) *model.BaseFields { return &c.BaseFields },
	Encode: func(c model.FederationDomainClaim) (model.Record, error) {
		return model.Record{
			"target_tenant_id": encTenant(c.TargetTenantID),
			"config_id":        c.ConfigID.String(),
			// Canonicalize at the store boundary too (like the config codec), so the unique
			// index compares the same key regardless of how a row was written.
			"domain": model.NormalizeFederationDomain(c.Domain),
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.FederationDomainClaim, error) {
		return model.FederationDomainClaim{BaseFields: b,
			TargetTenantID: decTenant(r, "target_tenant_id"),
			ConfigID:       decID(r, "config_id"),
			Domain:         r.String("domain")}, nil
	},
}
