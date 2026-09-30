// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

// This file declares the engine's authentication/authorization entities.
// They are the SOLE deliberate exception to the minimal-data rule (docs/SECURITY-HARDENING.md):
// a User carries an email (PII) and an argon2id password hash (a credential).
// Those values are never logged, never exported, and never placed in an audit
// event (audit references a user as "user:<id>", never by email — see
// model.AuditEvent.Actor).
//
// All of these rows live in the reserved system tenant (TenantID ==
// SystemTenantID): a User is a GLOBAL principal, not a member of one business
// tenant, and authentication must read across users/tenants BEFORE any tenant is
// resolved. They are reached only through the engine's auth partition
// (store.AuthScope via Store.AuthView/AuthMutate), which binds SystemTenantID as
// a normal, RLS-enforced tenant scope — never the cross-tenant System path. A
// module never holds a Store and so can never reach them.

// User is an operator account: a global principal that gains access to a
// business tenant through a Membership. Authentication identifies a User; the
// tenant it may act in is resolved separately from its memberships (and, for an
// APIToken, from the token's bound tenant).
type User struct {
	BaseFields
	// Email is the login identifier (unique among users). PII — never logged or
	// exported; audit references the user by id, not email.
	Email string
	// DisplayName is a human label safe to show.
	DisplayName string
	// Status is the account lifecycle state (active/inactive).
	Status LifecycleStatus
	// PasswordHash is the argon2id encoded hash ("" for an SSO-only account that
	// authenticates through the federation seam). Never logged or exported.
	PasswordHash string
	// IsSuperadmin grants the cross-tenant/system role (provisioning, global
	// verification). A superadmin is the only principal that may reach the System
	// path and cross-tenant operations.
	IsSuperadmin bool
	// ExternalID is the provisioning IdP's stable identifier for this account
	// (SCIM externalId, RFC 7643 §3.1). It is set when a user is created or
	// reconciled through the SCIM endpoint, empty for a locally-created user, and
	// is the key an IdP correlates its PATCH/DELETE on. It is a non-secret
	// directory reference, never a credential.
	ExternalID string
	// The SCIM enterprise User extension attributes (RFC 7643 §4.3,
	// urn:ietf:params:scim:schemas:extension:enterprise:2.0:User). They are the
	// subset the provider honors read- AND write-through (declared in /Schemas
	// only because they are honored — declared==honored). All non-secret directory
	// metadata, never a credential; empty for a locally-created or pre-extension
	// account.
	//
	// EmployeeNumber is the enterprise extension employeeNumber.
	EmployeeNumber string
	// Department is the enterprise extension department.
	Department string
	// Manager is the enterprise extension manager.value — the IdP's reference to
	// this user's manager (the manager's SCIM id or externalId, whatever the IdP
	// sends in manager.value). It is stored verbatim, not resolved to a local id.
	Manager string
	// SsoSubject is the ISSUER-QUALIFIED SSO subject that this account authenticates
	// as, "<issuer>\x1f<subject>" (U3). It is the login correlation key the SSO
	// path prefers over email so a login survives an email rename and can never
	// select the wrong account across IdPs — distinct from ExternalID, which is
	// SCIM's unqualified externalId key (a subject value alone, shared namespace).
	// Empty for a local/password account or a user that has never completed SSO;
	// stamped on first federated login and never overwritten thereafter. Uniquely
	// indexed (NULLs distinct, so local users coexist), non-secret, never a credential.
	SsoSubject string
	// CredentialCustody names who established the account's account-wide
	// credentials. The engine writes it at creation and in the deployment's own
	// ceremonies; no tenant API writes it. The empty value is legacy: every row that
	// existed before custody was recorded, of unknown origin.
	CredentialCustody CredentialCustody
	// CustodyTenantID is the tenant whose authority created the account, set only
	// with CustodyTenant.
	CustodyTenantID TenantID
}

// CredentialCustody names who established an account's account-wide credentials.
type CredentialCustody string

const (
	// CustodyLegacy is an account of unknown origin, recorded before custody was.
	CustodyLegacy CredentialCustody = ""
	// CustodyTenant is an account created under one tenant's authority: its
	// onboarding, its directory provisioning, or a first sign-in through its own
	// identity provider.
	CustodyTenant CredentialCustody = "tenant"
	// CustodyDeployment is an account the deployment itself created.
	CustodyDeployment CredentialCustody = "deployment"
	// CustodyHolder is an account whose credentials its holder established through
	// a proof no administrator holds.
	CustodyHolder CredentialCustody = "holder"
)

// CustodyScope is the tenant a sign-in of this account may act in, or the zero
// tenant when its sessions have account scope.
func (u User) CustodyScope() TenantID {
	if u.CredentialCustody == CustodyTenant {
		return u.CustodyTenantID
	}
	return ""
}

// Membership binds a User to a business tenant with a role. It lives in the
// system tenant (BaseFields.TenantID == SystemTenantID); the granted tenant is
// the TargetTenantID column, so login can enumerate every tenant a user belongs
// to in one query keyed by UserID, which a normal per-tenant scope could not do.
type Membership struct {
	BaseFields
	// UserID is the user this membership is for.
	UserID ID
	// TargetTenantID is the business tenant this membership grants access to.
	TargetTenantID TenantID
	// Role is the built-in role name granted in that tenant (owner/admin/editor/viewer).
	Role string
	// WorkspaceID OPTIONALLY scopes this membership to one workspace within
	// TargetTenantID (FASE X): zero is the historical tenant-wide
	// membership (the user acts across every workspace), a set value confines the
	// grant to that workspace. It is a workspace id in the GRANTED tenant's space,
	// not this row's own tenant (memberships live in the system tenant); there is
	// no DB foreign key, so validate it on write. Only models and
	// persists it — the ENFORCEMENT of a workspace-scoped membership is.
	WorkspaceID ID
}

// UserInvite is a pending, single-use invitation to onboard a NON-federated user
// into a tenant (FASE X). It lives in the system tenant like every auth
// row; the invited tenant is TargetTenantID. The token is a credential
// (selector + SHA-256 secret hash, never the secret itself, never logged); the
// invitee redeems it at /v1/invites/accept to set a password and activate the
// account. The membership is granted at invite time, so the account has its
// access the moment it is activated. The invitee's email is PII (never logged or
// in the ledger); the audit references the invite by id.
type UserInvite struct {
	BaseFields
	// Email is the invitee's login identifier (PII).
	Email string
	// TargetTenantID is the tenant the invite grants membership in.
	TargetTenantID TenantID
	// Role is the built-in role granted on acceptance (owner/admin/editor/viewer).
	Role string
	// Selector is the public, indexed lookup key of the invite token.
	Selector string
	// SecretHash is SHA-256(secret) of the invite token; never the secret itself.
	SecretHash []byte
	// ExpiresAt bounds how long the invite may sit unaccepted.
	ExpiresAt Timestamp
	// AcceptedAt is set when the invite is redeemed; nil while pending. A redeemed
	// invite is inert (single use).
	AcceptedAt *Timestamp
	// CreatedBy is the audit-actor string of the admin who issued the invite
	// (e.g. "user:<id>") — never an email.
	CreatedBy string
}

// UserGroup is a directory group provisioned into ONE business tenant (SCIM
// Groups, RFC 7643 §4.2). Like a Membership it lives in the system tenant
// (BaseFields.TenantID == SystemTenantID) with the granted tenant as the
// TargetTenantID column, so the SCIM handler bound to a tenant can enumerate
// that tenant's groups in one query. Group membership travels in
// UserGroupMember rows, never inline.
type UserGroup struct {
	BaseFields
	// TargetTenantID is the business tenant this group is provisioned into. Every
	// group read/write filters on it: all auth rows share SystemTenantID, so this
	// column — not RLS — is what isolates one tenant's groups from another's.
	TargetTenantID TenantID
	// DisplayName is the IdP's group name (SCIM displayName, required). It is NOT
	// unique: Microsoft Entra legally provisions duplicate group names and
	// correlates by ExternalID, so dedupe is application-level, not a DB index.
	DisplayName string
	// ExternalID is the provisioning IdP's stable identifier for this group
	// (SCIM externalId, RFC 7643 §3.1); empty when the IdP did not send one
	// (Okta correlates by displayName instead). A non-secret directory reference.
	ExternalID string
	// MappedRole is the built-in role this group's members are elevated to in
	// TargetTenantID; "" means no mapping. It is NEVER settable through any SCIM
	// inbound path (create/replace/patch all preserve it) — only the operator
	// role-mapping endpoint writes it, ceiling-checked against the acting
	// principal. Otherwise the IdP, not the tenant operator, would decide who is
	// an owner.
	MappedRole string
	// ProvisionedBy records who provisions this group's existence and
	// membership — the one boundary every writer respects (the group-origin
	// contract): "" (NULL) is today's SCIM/IdP-managed row, writable by SCIM
	// and by the operator console exactly as before; "operator" is a local
	// group managed from the console, writable by the console only; any other
	// value is the slug of the provisioner that owns the group, writable by
	// that provisioner only and read-only membership for everyone else. No
	// writer may adopt a group out of a claimed origin into another. Nullable
	// and appended LAST for the additive reconcile.
	ProvisionedBy string
	// ParentGroupID OPTIONALLY nests this group under another group of the SAME
	// TargetTenantID (S256 group hierarchy); zero is the historical un-nested
	// group. A member of this group is then ALSO a member of the parent for
	// authorization (loadGrants materializes the whole ancestor chain as Cedar
	// `Group::` principal parents), so a scoped grant on the parent reaches every
	// descendant's members. Like MappedRole it is NEVER settable through any SCIM
	// inbound path — only the operator nesting endpoint writes it (owner/superadmin
	// authority), so the IdP decides membership while the tenant operator decides
	// the hierarchy. There is no DB foreign key; the operator path validates the
	// parent exists in the same tenant and is acyclic, and loadGrants defensively
	// stops a chain that dangles or crosses tenants (deny-closed). Empty for a
	// group created before the column existed (additive, nullable).
	ParentGroupID ID
}

// UserGroupMember binds a User to a UserGroup. It lives in the system tenant
// like the group itself; the granted tenant is reached through the group's
// TargetTenantID. A user's group set is enumerated by UserID (the loadGrants
// fold), a group's roster by GroupID; (group, user) is unique.
type UserGroupMember struct {
	BaseFields
	// GroupID is the group this row belongs to.
	GroupID ID
	// UserID is the member user.
	UserID ID
}

// AuthSession is a human/panel login session: an opaque, server-side, revocable
// credential. The wire token is "olvs_<selector>_<secret>"; only the public
// Selector (indexed) and SHA-256(secret) are stored, so the database never holds
// anything that can be replayed if read. It lives in the system tenant.
//
// AAL/AMR: the session carries its authenticator assurance
// as SERVER-SIDE state — the token stays opaque (docs/SECURITY-HARDENING.md doctrine, no JWT
// claims) and the panel reads the assurance through whoami. The values are
// target-standard levels (NIST SP 800-63B-4 vocabulary); no conformance is
// claimed anywhere (docs/SECURITY-HARDENING.md).
type AuthSession struct {
	BaseFields
	// UserID is the authenticated user.
	UserID ID
	// Selector is the public, indexed lookup key (not secret).
	Selector string
	// SecretHash is SHA-256 of the session secret; compared in constant time.
	SecretHash []byte
	// ExpiresAt is when the session stops being valid.
	ExpiresAt Timestamp
	// Revoked marks an explicitly revoked (logged-out / killed) session.
	Revoked bool
	// CreatedIP is the client IP at issue time (non-sensitive operational detail).
	CreatedIP string
	// AAL is the authenticator assurance level the backend VERIFIED for this
	// session: 1 = single factor (password / federated login the engine cannot
	// vouch for), 3 = a phishing-resistant hardware ceremony (WebAuthn with user
	// verification, or PIV/CAC) verified by this engine. 0 means a legacy row
	// minted before the column existed and is read as 1 (fail-closed: the level
	// is never inflated). An elevated level requires a valid, nonfuture
	// AALAuthenticatedAt witness and an AALExpiresAt strictly in the future.
	AAL int
	// AMR lists the authentication methods used on this session, in order
	// ("pwd", "sso", "webauthn", "piv"). Product vocabulary shared with the
	// panel — SP 800-63-4 defines no amr conveyance mapping, so none is claimed.
	AMR []string
	// AALExpiresAt bounds an ELEVATED (step-up) assurance: past it the session
	// degrades back to AAL1 (the step-up freshness window, 800-63B-4 AAL3
	// reauthentication target). Nil for a never-elevated session.
	AALExpiresAt *Timestamp
	// TenantScope confines the session to one tenant. A session minted under a
	// tenant's authority (that tenant's identity provider, or any sign-in of an
	// account that tenant created) carries it and acts nowhere else; the zero value
	// is account scope. A scoped session's token carries the prefix "olvt".
	TenantScope TenantID
	// AALAuthenticatedAt is the server-observed successful hardware ceremony,
	// fixed once at UTC microsecond precision by the verified producer.
	// Nil is a legacy or never-elevated session; expiry never reconstructs it.
	AALAuthenticatedAt *Timestamp
}

// WebAuthnCredential is a registered FIDO2/WebAuthn authenticator for a user
//. It stores PUBLIC material only: the credential id and the
// public-key/attestation record the verifier needs — never a private key, never
// a challenge. It lives in the system tenant next to the other credential rows.
type WebAuthnCredential struct {
	BaseFields
	// UserID is the user this authenticator is registered to.
	UserID ID
	// Name is an optional operator-facing label.
	Name string
	// CredentialID is the authenticator's credential id, base64url (no padding).
	// It is the lookup/dedup key; WebAuthn requires it be unique per RP.
	CredentialID string
	// Credential is the full webauthn.Credential record as JSON (public key,
	// flags, sign count, attestation). The library mandates persisting the whole
	// record; storing its canonical JSON keeps the engine schema-stable across
	// library versions (it migrates its own serialization).
	Credential []byte
}

// TOTPCredential is a local account's RFC 6238 time-based second factor. It
// stores the SEED sealed at rest — a "v1:" envelope opened only by the TOTP
// seed sealer at verification time, exactly the doctrine of the federation
// secrets, so the database never holds raw key material. SeedHint is the
// non-secret display fingerprint. At most one row per account: enrolment
// replaces any prior row, activation stamps ConfirmedAt, and an admin reset
// deletes the row together with its recovery codes.
type TOTPCredential struct {
	BaseFields
	// AccountID is the local user this factor belongs to.
	AccountID ID
	// SeedSealed is the sealed TOTP shared secret (never the seed itself).
	SeedSealed string
	// SeedHint is the non-secret fingerprint of the seed, for display only.
	SeedHint string
	// Algorithm is the HMAC hash: "SHA1", "SHA256" or "SHA512".
	Algorithm string
	// Digits is the code length (6 or 8).
	Digits int
	// Period is the time step in seconds (30 by default).
	Period int
	// ConfirmedAt is set when the user proved possession of the seed; nil while
	// the enrolment is pending and the factor does not yet gate login.
	ConfirmedAt *Timestamp
	// LastUsedStep is the highest verified time step (unix/period). A code from
	// a step at or below it is refused: a captured code must not verify twice
	// (RFC 6238 §5.2). Zero means nothing verified yet.
	LastUsedStep int64
}

// TOTPRecoveryCode is one single-use recovery code of an account's TOTP factor,
// minted at activation and shown exactly once. Only SHA-256 of the code is
// stored — the same hashed-credential doctrine as sessions and API tokens —
// so a database read yields nothing replayable. UsedAt is nil until spent.
type TOTPRecoveryCode struct {
	BaseFields
	// AccountID is the local user whose factor this code recovers.
	AccountID ID
	// CodeHash is SHA-256 of the plaintext code; compared in constant time.
	CodeHash []byte
	// UsedAt is set when the code is spent; a used code never verifies again.
	UsedAt *Timestamp
}

// AuthPolicy is the deployment's local-account security policy singleton: one
// row in the system tenant, created on first write. An absent row is the
// default posture (no policy set). Unlike the SSO posture knobs — stored
// openly, enforced by the closed engine — the TOTP policy is enforced by the
// open engine itself: basic account security is Community scope.
type AuthPolicy struct {
	BaseFields
	// RequireTOTPAdmins requires administrators (superadmins, and admins/owners
	// of any tenant) to hold a confirmed TOTP factor: a password login without
	// one must enrol before it completes.
	RequireTOTPAdmins bool
}

// APIToken is a programmatic credential (CLI, Terraform provider, MCP). Like a
// session it is opaque ("olvk_<selector>_<secret>") with only Selector and
// SHA-256(secret) stored. It carries its own authorization: a bound tenant + role,
// or (BoundTenantID zero AND IsSuperadmin) a cross-tenant system token. It lives
// in the system tenant.
type APIToken struct {
	BaseFields
	// Name is a human label for the token (shown in listings).
	Name string
	// UserID is the user that owns/created the token (attribution); may be zero
	// for a standalone system token.
	UserID ID
	// Selector is the public, indexed lookup key.
	Selector string
	// SecretHash is SHA-256 of the token secret; compared in constant time.
	SecretHash []byte
	// BoundTenantID is the only tenant this token may act in. Zero means unbound:
	// valid only together with IsSuperadmin (a cross-tenant system token).
	BoundTenantID TenantID
	// Role is the built-in role the token acts with in its bound tenant.
	Role string
	// IsSuperadmin grants the system role (requires BoundTenantID zero).
	IsSuperadmin bool
	// ExpiresAt is the optional expiry (nil = no expiry).
	ExpiresAt *Timestamp
	// Revoked marks a revoked token.
	Revoked bool
	// LastUsedAt is the last time the token authenticated (nil if never).
	LastUsedAt *Timestamp
	// Purpose marks a purpose-restricted credential. Empty is an ordinary API
	// token; any non-empty value must be refused by ordinary authentication so
	// a credential registered for another protocol cannot double as user access.
	Purpose string

	// --- Delegation (RFC 8693 token exchange / RFC 8707 resource indicators) ---
	// These are populated only on a token minted by a token-exchange; they are
	// empty on an IssueToken-minted token. They make a delegated token a
	// down-scoped, audience-bound, traceable credential rather than an
	// indistinguishable bearer (ARCHITECTURE.md delegation chain).

	// Audience is the set of resource indicators (RFC 8707 absolute URIs) and/or
	// RFC 8693 logical audiences this token is bound to, newline-joined. A resource
	// server validates that it is named here before honoring the token, which
	// defeats the confused-deputy / token-replay attack (a token minted for one
	// service rejected at another). Empty means unbound (a general down-scope).
	Audience string
	// ActAsUserID is the principal this token acts ON BEHALF OF (RFC 8693
	// delegation): the subject of the exchange, while UserID owns/created it (the
	// actor). Empty on an impersonation exchange (no actor) or a non-delegated token.
	ActAsUserID ID
	// ParentTokenID is the API token this token was exchanged FROM. It is set to
	// the exact subject token id only when the subject is an API token, chaining
	// revocation to its children. It is empty both on an IssueToken-minted root
	// and on an exchange whose subject is a human auth session: that child is
	// clamped to the session expiry, but a session id is not token ancestry.
	ParentTokenID ID
	// Scope is the granted, down-scoped permission tier (space-delimited verbs,
	// e.g. "read" or "read write") recorded for introspection. The token's actual
	// authority is its Role (clamped at exchange time so the child can never exceed
	// the subject); Scope is the human/audit projection of that clamp.
	Scope string
	// AgentRef is the external_id of an agent identity this token is delegated to
	// (agent-OBO). When non-empty, the token represents the named agent
	// acting under its sponsor's authority. Empty on non-agent delegations.
	AgentRef string
	// SessionRef is the canonical session identity this credential is confined
	// to. It is populated only by a server-side session credential issuer;
	// public token issuance has no input for it. A credential bound to one
	// session must never be widened to its agent identity when authorizing a
	// sibling session's fenced work or communication.
	SessionRef string
	// WorkspaceID is the authz workspace of a purpose-restricted communication
	// session. It is server-authored and deliberately distinct from a sessions
	// runtime filesystem workspace. Zero on ordinary and work-session tokens.
	WorkspaceID ID
	// SessionRunRef is the exact supervised runtime generation. Unlike the
	// legacy work-session credential, communication-session persists this
	// binding in its own column rather than encoding it into Name.
	SessionRunRef string
	// SessionFence is the exact live Claim generation for SessionRef. Zero on
	// every token not minted by the communication-session issuer.
	SessionFence int64
}

// ExclusionKind distinguishes a whole-account exclusion from one tenant from the
// exclusion of one session.
type ExclusionKind string

const (
	// ExclusionOffboard excludes the account from the tenant. The row is also the
	// account's retirement record in that tenant.
	ExclusionOffboard ExclusionKind = "offboard"
	// ExclusionSession excludes one account-scope session from the tenant.
	ExclusionSession ExclusionKind = "session"
)

// RetirementState is where the retirement of an account from a tenant stands.
type RetirementState string

const (
	// RetirementRetiring is an offboard whose module steps have not all run clean.
	RetirementRetiring RetirementState = "retiring"
	// RetirementBlocked is a retirement a module step found references it may not
	// remove; the tenant resolves them and the retirement resumes.
	RetirementBlocked RetirementState = "blocked"
	// RetirementRetired is a retirement every declared module proved clean at an
	// authorization epoch of the tenant.
	RetirementRetired RetirementState = "retired"
	// RetirementLifted is a retired account the tenant re-admitted. The exclusion
	// no longer applies; the retirement epoch remains the account's floor there.
	RetirementLifted RetirementState = "lifted"
)

// TenantExclusion excludes an account, or one of its sessions, from one tenant:
// the authorizer refuses that principal there before any grant is evaluated. An
// offboard exclusion is also the account's retirement record in the tenant. It
// lives in the system tenant; the excluding tenant is TargetTenantID.
type TenantExclusion struct {
	BaseFields
	// UserID is the excluded account.
	UserID ID
	// TargetTenantID is the tenant the account is excluded from.
	TargetTenantID TenantID
	// SessionID names the one excluded session, for ExclusionSession.
	SessionID ID
	// Kind is ExclusionOffboard or ExclusionSession.
	Kind ExclusionKind
	// CreatedBy is the actor that wrote the exclusion.
	CreatedBy string
	// RetirementGeneration counts the offboards of this account from this tenant;
	// a re-admission and a new offboard start the next generation.
	RetirementGeneration int64
	// RetirementState is where the retirement stands.
	RetirementState RetirementState
	// RetiredEpoch is the tenant's authorization epoch at which every declared
	// module was proved clean; once lifted it is the account's floor in the tenant.
	RetiredEpoch *int64
	// BlockingRefs lists, by id only, the stored rows that block the retirement.
	BlockingRefs string
	// ModuleResults is the JSON record of each declared module's step: its state,
	// the fact version it ran at and the ids it found.
	ModuleResults string
	// Attempts counts the retirement passes that ran.
	Attempts int64
	// NextAttemptAt is when the retirement pump retries.
	NextAttemptAt *Timestamp
}

// InForce reports whether the exclusion refuses the account in its tenant.
func (e TenantExclusion) InForce() bool {
	return e.Kind != ExclusionOffboard || e.RetirementState != RetirementLifted
}

// AccountOffer is a pending offer to an existing account: to join a tenant, or
// to complete a deployment recovery. It binds the account, its address, the
// target tenant and role, the account's authority version and the claim state of
// the address's domain at issue. Only the secret's hash is stored.
type AccountOffer struct {
	BaseFields
	// Kind is "join" or "recovery".
	Kind string
	// UserID is the account the offer is for.
	UserID ID
	// Email is the normalized address the offer was issued to.
	Email string
	// TargetTenantID is the tenant the offer joins.
	TargetTenantID TenantID
	// Role is the role the join grants.
	Role string
	// AuthorityVersion is the account's authority version at issue.
	AuthorityVersion int64
	// ClaimTenantID is the tenant whose identity provider claimed the domain at
	// issue, if any.
	ClaimTenantID TenantID
	// TrustedTenantID is the claiming tenant the target tenant explicitly trusted.
	TrustedTenantID TenantID
	// Selector is the public lookup key of the offer's secret.
	Selector string
	// SecretHash is SHA-256 of the offer's secret.
	SecretHash []byte
	// ExpiresAt bounds the offer.
	ExpiresAt Timestamp
	// AcceptedAt is set once the offer is consumed.
	AcceptedAt *Timestamp
	// VoidedAt is set when the offer is voided.
	VoidedAt *Timestamp
	// VoidReason says why it was voided.
	VoidReason string
	// CreatedBy is the actor that issued the offer.
	CreatedBy string
	// Reason and EvidenceRef record a deployment recovery's justification.
	Reason      string
	EvidenceRef string
}
