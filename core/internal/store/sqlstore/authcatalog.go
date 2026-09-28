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
		tenantExclusionDescriptor, accountOfferDescriptor, credentialBindingDescriptor,
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

// pdeclNoneAuth*, pdeclNoneFed* and pdeclActorEvidence are the declarations the
// auth partition's columns share. pdeclActorEvidence is an audit-actor string
// ("user:<id>", "token:<id>", ...) that records who wrote the row.
var (
	pdeclNoneAuthSelector   = model.None("the public lookup key of a hashed secret: core/model/auth.go:146, core/model/auth.go:228, core/model/auth.go:293, core/model/auth.go:457")
	pdeclNoneAuthSecretHash = model.None("SHA-256 of a secret, never the secret: core/model/auth.go:148, core/model/auth.go:230, core/model/auth.go:295, core/model/auth.go:459")
	pdeclNoneAuthRole       = model.None("a built-in role name: core/auth/permission.go:42")
	pdeclActorEvidence      = model.Ref(model.EncodeUserRef, model.ClassEvidence)

	pdeclNoneFedEndpoint   = model.None("an IdP issuer, metadata, entity or endpoint URL: core/model/federation.go:79, core/model/federation.go:85")
	pdeclNoneFedSealed     = model.None("sealed secret material, opened only to replay it to the IdP: core/model/federation.go:51")
	pdeclNoneFedHint       = model.None("a fingerprint prefix of a sealed secret, for display: core/model/federation.go:54")
	pdeclNoneFedPublicCert = model.None("public SP certificate material: core/model/federation.go:93, core/model/federation.go:99")
	pdeclNoneFedGroupsName = model.None("the name of the claim or attribute groups are read from, not a value: core/model/federation.go:124")
)

var userDescriptor = model.EntityDescriptor{
	Kind:  "core.user",
	Table: "users",
	Fields: []model.FieldSpec{
		pdecl(field("email", model.KindText, false),
			model.Ref(model.EncodeEmail, model.ClassEvidence)),
		pdecl(field("display_name", model.KindText, true),
			model.None("a human label safe to show: core/model/auth.go:31, core/api/dto.go:161")),
		pdecl(field("status", model.KindText, false), pdeclNoneLifecycle),
		pdecl(field("password_hash", model.KindText, true),
			model.None("an argon2id encoded password hash, never an identifier: core/model/auth.go:35")),
		field("is_superadmin", model.KindBool, false),
		// SCIM externalId (RFC 7643): the provisioning IdP's stable id. Nullable
		// (local users have none); indexed so SCIM can correlate by externalId eq.
		// Added post-v2 via the additive reconcile.
		pdecl(indexedField("external_id", model.KindText, true),
			model.Ref(model.EncodeExternalID, model.ClassEvidence)),
		// SCIM enterprise User extension attributes (RFC 7643 §4.3). All nullable
		// and appended last so the additive reconcile (schema.go) issues the
		// ALTER TABLE ADD COLUMN on an existing DB and v2 regenerates them on a
		// fresh one — no hand-authored migration. Non-secret directory metadata.
		pdecl(field("employee_number", model.KindText, true),
			model.None("the SCIM enterprise employeeNumber, rendered only: core/model/auth.go:55, core/api/scim/user.go:192")),
		pdecl(field("department", model.KindText, true),
			model.None("the SCIM enterprise department, rendered only: core/model/auth.go:57, core/api/scim/user.go:195")),
		// manager is the IdP's manager.value, kept verbatim and never resolved; it
		// may spell another account's id or alias.
		pdecl(field("manager", model.KindText, true), model.Scan(model.ClassEvidence)),
		// sso_subject is the issuer-qualified SSO login-correlation key (U3),
		// "<issuer>\x1f<subject>". Nullable and appended last for the additive
		// reconcile (ALTER TABLE ADD COLUMN on an existing DB, v2 regenerates it on a
		// fresh one — no hand-authored migration). Distinct from external_id: that is
		// SCIM's unqualified key, this qualifies the subject by its issuing IdP.
		// The value is the account's own issuer-qualified alias, matched as text.
		pdecl(field("sso_subject", model.KindText, true), model.Scan(model.ClassEvidence)),
		// credential_custody names who established the account's account-wide
		// credentials (tenant, deployment or holder); NULL is an account of unknown
		// origin recorded before custody was. custody_tenant_id is the creating
		// tenant for tenant custody. Both nullable and appended last for the
		// additive reconcile; the engine writes them and no tenant API does.
		pdecl(field("credential_custody", model.KindText, true),
			model.None("a custody label from the closed tenant/deployment/holder vocabulary: core/model/auth.go:86")),
		pdecl(field("custody_tenant_id", model.KindUUID, true),
			model.None("the tenant whose authority created the account: core/model/auth.go:77")),
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
			"sso_subject":        ssoSub,
			"credential_custody": encOptStr(string(u.CredentialCustody)),
			"custody_tenant_id":  encOptTenant(u.CustodyTenantID),
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.User, error) {
		return model.User{BaseFields: b, Email: r.String("email"), DisplayName: r.String("display_name"),
			Status: model.LifecycleStatus(r.String("status")), PasswordHash: r.String("password_hash"),
			IsSuperadmin: r.Bool("is_superadmin"), ExternalID: r.String("external_id"),
			EmployeeNumber: r.String("employee_number"), Department: r.String("department"), Manager: r.String("manager"),
			SsoSubject:        r.String("sso_subject"),
			CredentialCustody: model.CredentialCustody(r.String("credential_custody")),
			CustodyTenantID:   decTenant(r, "custody_tenant_id")}, nil
	},
}

// --- Membership --------------------------------------------------------------

var membershipDescriptor = model.EntityDescriptor{
	Kind:  "core.membership",
	Table: "memberships",
	Fields: []model.FieldSpec{
		pdecl(indexedField("user_id", model.KindUUID, false),
			model.Ref(model.EncodeUserID, model.ClassAuthority)),
		pdecl(field("target_tenant_id", model.KindUUID, false),
			model.None("the business tenant the membership grants access to: core/model/auth.go:116")),
		pdecl(field("role", model.KindText, false), pdeclNoneAuthRole),
		// workspace_id OPTIONALLY scopes the membership to one workspace in the
		// granted tenant (FASE X). Nullable and appended last for additive
		// reconcile; NULL is the historical tenant-wide membership. Enforcement is
		// Own the precise index when they query by workspace.
		pdecl(field("workspace_id", model.KindUUID, true),
			model.None("the workspace the membership optionally narrows to: core/model/auth.go:120")),
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
		pdecl(indexedField("target_tenant_id", model.KindUUID, false),
			model.None("the business tenant the group is provisioned into: core/model/auth.go:168")),
		// display_name is deliberately NOT unique: Microsoft Entra legally
		// provisions duplicate group names (it correlates by externalId), so its
		// dedupe is application-level in core/auth, never a DB index.
		pdecl(indexedField("display_name", model.KindText, false),
			model.None("the IdP's group name: core/model/auth.go:172")),
		pdecl(indexedField("external_id", model.KindText, true),
			model.None("the provisioning IdP's stable id for the group, never an account: core/model/auth.go:176")),
		pdecl(field("mapped_role", model.KindText, true), pdeclNoneAuthRole),
		// parent_group_id nests this group under another of the same tenant (S256
		// group hierarchy). Nullable and APPENDED last so a reconciled (pre-S256)
		// table and a freshly-created one agree on column order — the same additive
		// discipline as the assurance columns on auth_sessions.
		pdecl(field("parent_group_id", model.KindUUID, true),
			model.None("the parent group row of the same tenant: core/model/auth.go:187")),
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
		pdecl(indexedField("group_id", model.KindUUID, false),
			model.None("the group row the membership belongs to: core/model/auth.go:208")),
		pdecl(indexedField("user_id", model.KindUUID, false),
			model.Ref(model.EncodeUserID, model.ClassAuthority)),
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
		pdecl(indexedField("user_id", model.KindUUID, false),
			model.Ref(model.EncodeUserID, model.ClassAuthority)),
		pdecl(field("selector", model.KindText, false), pdeclNoneAuthSelector),
		pdecl(field("secret_hash", model.KindBytes, false), pdeclNoneAuthSecretHash),
		field("expires_at", model.KindTimestamp, false),
		field("revoked", model.KindBool, false),
		pdecl(field("created_ip", model.KindText, true),
			model.None("the client IP at issue time: core/model/auth.go:236")),
		// Assurance columns. Nullable (additive reconcile on an existing DB);
		// NULL aal reads as 1 — a pre session is never inflated past AAL1.
		// Appended at the end so fresh and reconciled tables agree on column order.
		field("aal", model.KindInt, true),
		pdecl(field("amr", model.KindJSON, true),
			pdeclStrings("authentication method names: core/model/auth.go:246")),
		field("aal_expires_at", model.KindTimestamp, true),
		// tenant_scope confines the session to one tenant; NULL is account scope.
		// Nullable and appended last for the additive reconcile.
		pdecl(field("tenant_scope", model.KindUUID, true),
			model.None("the one tenant the session is confined to: core/model/auth.go:254")),
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
			"tenant_scope": encOptTenant(s.TenantScope),
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
			CreatedIP: r.String("created_ip"), AAL: int(r.Int("aal")), AMR: amr, AALExpiresAt: aalExp,
			TenantScope: decTenant(r, "tenant_scope")}, nil
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
		pdecl(indexedField("user_id", model.KindUUID, false),
			model.Ref(model.EncodeUserID, model.ClassAuthority)),
		pdecl(field("name", model.KindText, true),
			model.None("an optional operator-facing label: core/model/auth.go:269")),
		pdecl(field("credential_id", model.KindText, false),
			model.None("the authenticator's credential id, the lookup key: core/model/auth.go:271")),
		pdecl(field("credential", model.KindJSON, false),
			model.None("the library's public credential record (public key, flags, sign count, attestation), no user handle: core/model/auth.go:274")),
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
		pdecl(field("name", model.KindText, false),
			model.None("a human label for the token: core/model/auth.go:288, core/api/dto.go:257")),
		// user_id is indexed: the leaver/deprovision path lists a user's tokens to
		// revoke them, and the delegation cascade lists by parent_token_id.
		pdecl(indexedField("user_id", model.KindUUID, true),
			model.Ref(model.EncodeUserID, model.ClassAuthority)),
		pdecl(field("selector", model.KindText, false), pdeclNoneAuthSelector),
		pdecl(field("secret_hash", model.KindBytes, false), pdeclNoneAuthSecretHash),
		pdecl(indexedField("bound_tenant_id", model.KindUUID, true),
			model.None("the only tenant the token may act in: core/model/auth.go:297")),
		pdecl(field("role", model.KindText, true),
			model.None("the role the token acts with in its bound tenant: core/model/auth.go:300")),
		field("is_superadmin", model.KindBool, false),
		field("expires_at", model.KindTimestamp, true),
		field("revoked", model.KindBool, false),
		field("last_used_at", model.KindTimestamp, true),
		// Delegation columns (RFC 8693/8707); all nullable, populated only on a
		// token-exchange-minted token. Added post-v2 via the additive reconcile.
		pdecl(field("audience", model.KindText, true),
			model.None("resource indicators and logical audiences the token is bound to: core/model/auth.go:321")),
		pdecl(field("act_as_user_id", model.KindUUID, true),
			model.Ref(model.EncodeUserID, model.ClassAuthority)),
		pdecl(indexedField("parent_token_id", model.KindUUID, true),
			model.None("the API token this token was exchanged from: core/model/auth.go:331")),
		pdecl(field("scope", model.KindText, true),
			model.None("space-delimited permission verbs: core/model/auth.go:337")),
		// agent_ref is the external_id of the agent identity this token is delegated
		// to (agent-OBO). Nullable; non-empty only on an agent-OBO exchange.
		pdecl(field("agent_ref", model.KindText, true),
			model.None("the external id of an agent identity the token is delegated to, never an account: core/model/auth.go:342")),
		// purpose is nullable for additive reconciliation and reads as empty on
		// pre-existing rows. Non-empty values mark credentials reserved for a
		// specialized protocol rather than ordinary API authentication.
		pdecl(field("purpose", model.KindText, true),
			model.None("a purpose label that restricts the credential: core/model/auth.go:310")),
		// session_ref is a server-authored canonical work-session SID. It is
		// nullable so existing API tokens remain agent/user scoped and cannot be
		// mistaken for a session credential after additive reconciliation.
		pdecl(field("session_ref", model.KindText, true),
			model.None("the canonical session identity the credential is confined to: core/model/auth.go:346")),
		// communication-session stores its complete server-authored binding in
		// dedicated nullable columns. They remain NULL on ordinary and legacy
		// work-session tokens; no binding is encoded into a display name.
		pdecl(field("workspace_id", model.KindUUID, true),
			model.None("the workspace of a purpose-restricted communication credential: core/model/auth.go:352")),
		pdecl(field("session_run_ref", model.KindText, true),
			model.None("the exact supervised runtime generation: core/model/auth.go:356")),
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
		pdecl(field("email", model.KindText, false),
			model.Ref(model.EncodeEmail, model.ClassObligation)),
		pdecl(indexedField("target_tenant_id", model.KindUUID, false),
			model.None("the tenant the invite grants membership in: core/model/auth.go:142")),
		pdecl(field("role", model.KindText, false), pdeclNoneAuthRole),
		pdecl(field("selector", model.KindText, false), pdeclNoneAuthSelector),
		pdecl(field("secret_hash", model.KindBytes, false), pdeclNoneAuthSecretHash),
		field("expires_at", model.KindTimestamp, false),
		field("accepted_at", model.KindTimestamp, true),
		pdecl(field("created_by", model.KindText, true), pdeclActorEvidence),
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
		pdecl(indexedField("target_tenant_id", model.KindUUID, false),
			model.None("the scope the IdP configuration governs: core/model/federation.go:58")),
		pdecl(field("protocol", model.KindText, true),
			model.None("oidc or saml: core/model/federation.go:73")),
		pdecl(field("status", model.KindText, false), pdeclNoneLifecycle),
		pdecl(field("oidc_issuer", model.KindText, true), pdeclNoneFedEndpoint),
		pdecl(field("oidc_client_id", model.KindText, true),
			model.None("the OIDC client id registered at the IdP: core/model/federation.go:81")),
		pdecl(field("oidc_client_secret_sealed", model.KindText, true), pdeclNoneFedSealed),
		pdecl(field("oidc_client_secret_hint", model.KindText, true), pdeclNoneFedHint),
		pdecl(field("saml_metadata_url", model.KindText, true), pdeclNoneFedEndpoint),
		pdecl(field("saml_entity_id", model.KindText, true), pdeclNoneFedEndpoint),
		pdecl(field("saml_acs_url", model.KindText, true), pdeclNoneFedEndpoint),
		pdecl(field("saml_idp_sso_url", model.KindText, true), pdeclNoneFedEndpoint),
		pdecl(field("saml_email_attr", model.KindText, true),
			model.None("the name of the SAML attribute an email is read from, not a value: core/model/federation.go:90")),
		pdecl(field("saml_sp_cert_pem", model.KindText, true), pdeclNoneFedPublicCert),
		pdecl(field("saml_sp_key_sealed", model.KindText, true), pdeclNoneFedSealed),
		pdecl(field("saml_sp_key_hint", model.KindText, true), pdeclNoneFedHint),
		pdecl(field("saml_sp_sign_cert_pem", model.KindText, true), pdeclNoneFedPublicCert),
		pdecl(field("saml_sp_sign_key_sealed", model.KindText, true), pdeclNoneFedSealed),
		pdecl(field("saml_sp_sign_key_hint", model.KindText, true), pdeclNoneFedHint),
		// Login-enforcement posture (non-secret operator intent). Nullable and
		// appended LAST so the additive reconcile (schema.go) ALTERs an existing DB and
		// v2 regenerates them on a fresh one — no hand-authored migration. A NULL
		// require_sso reads as false and a NULL list reads as empty, so a pre row
		// decodes to "no enforcement" (the open build never enforced anyway).
		field("require_sso", model.KindBool, true),
		pdecl(field("network_allow_cidrs", model.KindJSON, true),
			pdeclStrings("CIDR ranges of the login allow-list: core/model/federation.go:115")),
		// Group-mapping + JIT coherence (appended LAST — additive reconcile).
		pdecl(field("oidc_groups_claim", model.KindText, true), pdeclNoneFedGroupsName),
		pdecl(field("saml_groups_attr", model.KindText, true), pdeclNoneFedGroupsName),
		field("scim_authoritative", model.KindBool, true),
		// U4 first-class IdP entity key (appended LAST — additive reconcile ALTERs
		// an existing DB, v2 regenerates it on a fresh one). Nullable in storage; the
		// codec normalizes NULL/empty → "default", and the boot-time backfill
		// (reconcileCoreData, schema.go) rewrites legacy NULLs so the unique index below
		// enforces one "default" per scope IDENTICALLY on upgraded and fresh databases.
		pdecl(field("alias", model.KindText, true),
			model.None("the IdP's scope-unique alias slug: core/model/federation.go:62")),
		// U5 home-realm routing: the email domains this IdP claims (globally unique;
		// enforced in the service). Non-secret JSON list, appended LAST — additive reconcile.
		pdecl(field("claimed_domains", model.KindJSON, true),
			pdeclStrings("email domains used only as a routing key: core/model/federation.go:138")),
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
		pdecl(indexedField("target_tenant_id", model.KindUUID, false),
			model.None("the scope of the config that claims the domain: core/model/federation.go:168")),
		pdecl(indexedField("config_id", model.KindUUID, false),
			model.None("the federation config row the claim derives from: core/model/federation.go:171")),
		pdecl(field("domain", model.KindText, false),
			model.None("a normalized claimed email domain: core/model/federation.go:173")),
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

// --- TenantExclusion -----------------------------------------------------------

// tenantExclusionDescriptor stores the exclusions of accounts, and of single
// sessions, from one tenant. An offboard exclusion is also the account's
// retirement record there: its generation, state, the epoch it retired at, the
// ids that block it and each declared module's result. Like every auth row it
// lives in the system tenant; the excluding tenant is target_tenant_id. An
// offboard row stores an empty session_id so the unique key holds one record per
// account and tenant.
var tenantExclusionDescriptor = model.EntityDescriptor{
	Kind:  "core.tenant_exclusion",
	Table: "tenant_exclusions",
	Fields: []model.FieldSpec{
		pdecl(indexedField("user_id", model.KindUUID, false),
			model.Ref(model.EncodeUserID, model.ClassRestrict)),
		pdecl(indexedField("target_tenant_id", model.KindUUID, false),
			model.None("the tenant the account is excluded from: core/model/auth.go:402")),
		pdecl(field("session_id", model.KindText, false),
			model.None("the one excluded session, for a session exclusion: core/model/auth.go:404")),
		pdecl(field("kind", model.KindText, false),
			model.None("offboard or session: core/model/auth.go:406, core/model/auth.go:370")),
		pdecl(field("created_by", model.KindText, true), pdeclActorEvidence),
		field("retirement_generation", model.KindInt, true),
		pdecl(field("retirement_state", model.KindText, true),
			model.None("a retirement state from the closed vocabulary: core/model/auth.go:381")),
		field("retired_epoch", model.KindInt, true),
		// blocking_refs and module_results record, as text, the rows and module
		// steps a retirement found; they are evidence of the retirement.
		pdecl(field("blocking_refs", model.KindText, true), model.Scan(model.ClassEvidence)),
		pdecl(field("module_results", model.KindText, true), model.Scan(model.ClassEvidence)),
		field("attempts", model.KindInt, true),
		field("next_attempt_at", model.KindTimestamp, true),
	},
	Indexes: []model.IndexSpec{
		{Name: "tenant_exclusions_uniq", Columns: []string{"tenant_id", "user_id", "target_tenant_id", "kind", "session_id"}, Unique: true},
	},
}

var tenantExclusionCodec = model.Codec[model.TenantExclusion]{
	Base: func(e *model.TenantExclusion) *model.BaseFields { return &e.BaseFields },
	Encode: func(e model.TenantExclusion) (model.Record, error) {
		var retired any
		if e.RetiredEpoch != nil {
			retired = *e.RetiredEpoch
		}
		return model.Record{
			"user_id": e.UserID.String(), "target_tenant_id": encTenant(e.TargetTenantID),
			"session_id": e.SessionID.String(), "kind": string(e.Kind), "created_by": encOptStr(e.CreatedBy),
			"retirement_generation": encOptInt(e.RetirementGeneration),
			"retirement_state":      encOptStr(string(e.RetirementState)),
			"retired_epoch":         retired,
			"blocking_refs":         encOptStr(e.BlockingRefs), "module_results": encOptStr(e.ModuleResults),
			"attempts": encOptInt(e.Attempts), "next_attempt_at": encOptTS(e.NextAttemptAt),
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.TenantExclusion, error) {
		next, err := decOptTS(r, "next_attempt_at")
		if err != nil {
			return model.TenantExclusion{}, err
		}
		var retired *int64
		if !r.IsNull("retired_epoch") {
			v := r.Int("retired_epoch")
			retired = &v
		}
		return model.TenantExclusion{BaseFields: b, UserID: decID(r, "user_id"),
			TargetTenantID: decTenant(r, "target_tenant_id"), SessionID: decID(r, "session_id"),
			Kind: model.ExclusionKind(r.String("kind")), CreatedBy: r.String("created_by"),
			RetirementGeneration: r.Int("retirement_generation"),
			RetirementState:      model.RetirementState(r.String("retirement_state")),
			RetiredEpoch:         retired, BlockingRefs: r.String("blocking_refs"),
			ModuleResults: r.String("module_results"), Attempts: r.Int("attempts"),
			NextAttemptAt: next}, nil
	},
}

// --- AccountOffer --------------------------------------------------------------

// accountOfferDescriptor stores pending offers to existing accounts: to join a
// tenant, or to complete a deployment recovery. Offers have a table of their own
// so that a binary that predates them can never redeem one as an invitation.
// Only SHA-256(secret) is stored; the selector is the public lookup key.
var accountOfferDescriptor = model.EntityDescriptor{
	Kind:  "core.account_offer",
	Table: "account_offers",
	Fields: []model.FieldSpec{
		pdecl(field("kind", model.KindText, false),
			model.None("join or recovery: core/model/auth.go:440")),
		pdecl(indexedField("user_id", model.KindUUID, false),
			model.Ref(model.EncodeUserID, model.ClassAuthority)),
		// email is the address the offer was issued to; the account the offer is
		// for is user_id.
		pdecl(field("email", model.KindText, false),
			model.Ref(model.EncodeEmail, model.ClassEvidence)),
		pdecl(indexedField("target_tenant_id", model.KindUUID, true),
			model.None("the tenant the offer joins: core/model/auth.go:446")),
		pdecl(field("role", model.KindText, true), pdeclNoneAuthRole),
		field("authority_version", model.KindInt, false),
		pdecl(field("claim_tenant_id", model.KindUUID, true),
			model.None("the tenant whose identity provider claimed the domain: core/model/auth.go:452")),
		pdecl(field("trusted_tenant_id", model.KindUUID, true),
			model.None("the claiming tenant the target tenant trusted: core/model/auth.go:455")),
		pdecl(field("selector", model.KindText, false), pdeclNoneAuthSelector),
		pdecl(field("secret_hash", model.KindBytes, false), pdeclNoneAuthSecretHash),
		field("expires_at", model.KindTimestamp, false),
		field("accepted_at", model.KindTimestamp, true),
		field("voided_at", model.KindTimestamp, true),
		pdecl(field("void_reason", model.KindText, true),
			model.None("why the offer was voided: core/model/auth.go:467")),
		pdecl(field("created_by", model.KindText, true), pdeclActorEvidence),
		pdecl(field("reason", model.KindText, true),
			model.None("a deployment recovery's justification: core/model/auth.go:471")),
		pdecl(field("evidence_ref", model.KindText, true),
			model.None("a deployment recovery's evidence reference: core/model/auth.go:471")),
	},
	Indexes: []model.IndexSpec{
		{Name: "account_offers_selector_uniq", Columns: []string{"tenant_id", "selector"}, Unique: true},
	},
}

var accountOfferCodec = model.Codec[model.AccountOffer]{
	Base: func(o *model.AccountOffer) *model.BaseFields { return &o.BaseFields },
	Encode: func(o model.AccountOffer) (model.Record, error) {
		return model.Record{
			"kind": o.Kind, "user_id": o.UserID.String(), "email": o.Email,
			"target_tenant_id": encOptTenant(o.TargetTenantID), "role": encOptStr(o.Role),
			"authority_version": o.AuthorityVersion,
			"claim_tenant_id":   encOptTenant(o.ClaimTenantID), "trusted_tenant_id": encOptTenant(o.TrustedTenantID),
			"selector": o.Selector, "secret_hash": encBytes(o.SecretHash), "expires_at": encTS(o.ExpiresAt),
			"accepted_at": encOptTS(o.AcceptedAt), "voided_at": encOptTS(o.VoidedAt),
			"void_reason": encOptStr(o.VoidReason), "created_by": encOptStr(o.CreatedBy),
			"reason": encOptStr(o.Reason), "evidence_ref": encOptStr(o.EvidenceRef),
		}, nil
	},
	Decode: func(b model.BaseFields, r model.Record) (model.AccountOffer, error) {
		exp, err := decTS(r, "expires_at")
		if err != nil {
			return model.AccountOffer{}, err
		}
		acc, err := decOptTS(r, "accepted_at")
		if err != nil {
			return model.AccountOffer{}, err
		}
		void, err := decOptTS(r, "voided_at")
		if err != nil {
			return model.AccountOffer{}, err
		}
		return model.AccountOffer{BaseFields: b, Kind: r.String("kind"), UserID: decID(r, "user_id"),
			Email: r.String("email"), TargetTenantID: decTenant(r, "target_tenant_id"), Role: r.String("role"),
			AuthorityVersion: r.Int("authority_version"), ClaimTenantID: decTenant(r, "claim_tenant_id"),
			TrustedTenantID: decTenant(r, "trusted_tenant_id"), Selector: r.String("selector"),
			SecretHash: r.Bytes("secret_hash"), ExpiresAt: exp, AcceptedAt: acc, VoidedAt: void,
			VoidReason: r.String("void_reason"), CreatedBy: r.String("created_by"),
			Reason: r.String("reason"), EvidenceRef: r.String("evidence_ref")}, nil
	},
}
