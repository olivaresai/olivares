// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

// PEPService is a registered Policy Enforcement Point whose authenticated
// requests the delegation verifier may accept. Registration binds the service
// identity to one PDP audience and to the controls the service can enforce.
type PEPService struct {
	BaseFields
	// TargetTenantID is the business tenant this service enforces for. Like
	// memberships and other auth-partition records, the row itself lives under
	// SystemTenantID while this explicit axis carries its governed tenant.
	TargetTenantID TenantID
	// Name is the operator-facing service name and is unique in its tenant.
	Name string
	// PDPAudience is copied into minted handles so a proof for one decision
	// audience cannot be replayed at another.
	PDPAudience string
	// Capabilities records the named controls this service is registered to
	// enforce; each value is the boolean equivalent of the SDK PEP capability.
	Capabilities map[string]bool
	// CapabilityVersion changes whenever the registered capability vector
	// changes, binding claims and verdicts to the version used for a decision.
	CapabilityVersion int
	// DisabledAt marks when the service stopped being eligible to present new
	// delegation proofs; nil means the registration remains active.
	DisabledAt *Timestamp
}

// PEPServiceCredential binds one purpose-restricted API credential to one PEP
// service. Multiple active rows for a service support overlapping rotation,
// while TokenID uniqueness prevents one credential from naming two services.
type PEPServiceCredential struct {
	BaseFields
	// ServiceID is the PEP service identity authenticated by this credential.
	ServiceID ID
	// TokenID is the API-token row whose opaque credential proves possession.
	TokenID ID
	// DisabledAt ends this credential binding without disabling other
	// credentials that overlap during service rotation.
	DisabledAt *Timestamp
}

// DelegationHandle is the server-side authority behind one opaque delegation
// credential. Its row ID is the single-use JTI claimed by the decision path.
type DelegationHandle struct {
	BaseFields
	// TargetTenantID is the business tenant this handle governs a subject in.
	// Like memberships and PEP services, the row itself lives under
	// SystemTenantID (BaseFields.TenantID) while this explicit axis carries the
	// governed business tenant — the value every tenant check, the request
	// fingerprint, and the claim idempotency comparison must use.
	TargetTenantID TenantID
	// Selector is the public lookup component of the opaque handle; it reveals
	// no authority without the independently generated secret.
	Selector string
	// SecretHash is the one-way hash checked against the presented secret so the
	// database never stores replayable credential material.
	SecretHash []byte
	// SourceCredKind identifies whether current subject authority must be
	// revalidated from a user credential or an API-token credential.
	SourceCredKind string
	// SourceCredID binds revalidation to the exact source credential from which
	// the handle was minted, so later revocation invalidates the handle.
	SourceCredID ID
	// SubjectUserID binds the handle to the user whose current status,
	// membership, role, and groups the verifier must revalidate.
	SubjectUserID ID
	// ActAsUserID records the delegated act-as subject when it differs from the
	// source credential owner; zero means there is no separate act-as subject.
	ActAsUserID ID
	// AgentRef binds agent-on-behalf-of authority to the stored agent identity
	// whose lifecycle and sponsor must still be valid at verification time.
	AgentRef string
	// MintRole is the role ceiling captured at mint time; later promotion of
	// the subject cannot raise authority through this handle.
	MintRole string
	// MintGroups is the resolved group closure captured at mint time; effective
	// groups are intersected with the subject's current closure.
	MintGroups []string
	// PEPServiceID binds presentation to the one registered PEP service for
	// which the handle was minted.
	PEPServiceID ID
	// Audience is the PEP service PDP audience copied at mint time, preventing a
	// valid handle from being redirected to another decision audience.
	Audience string
	// Operations is the allowlist of SDK operation kinds the handle may
	// authorize; an empty or unknown kind is outside the binding.
	Operations []string
	// BoundDigest optionally binds the handle to one content digest; empty means
	// the request fingerprint supplies the per-task content binding.
	BoundDigest string
	// ExpiresAt is the absolute end of the handle's authority.
	ExpiresAt Timestamp
	// RevokedAt records explicit invalidation before expiry; nil means the
	// handle has not been revoked.
	RevokedAt *Timestamp
}

// PDPDecisionClaim is the durable single-use claim made before policy
// evaluation. Its row ID is the server-minted DecisionID used by later phases.
type PDPDecisionClaim struct {
	BaseFields
	// TargetTenantID is the business tenant of the governed decision. The row
	// lives under SystemTenantID (BaseFields.TenantID); this explicit axis is the
	// tenant the verifier compares against the authenticated PEP and the value the
	// claim idempotency comparison uses (never BaseFields.TenantID).
	TargetTenantID TenantID
	// HandleJTI is the delegation-handle row ID and is globally single-use
	// within the auth partition.
	HandleJTI ID
	// PEPServiceID binds the claim and every later decision phase to the
	// authenticated PEP service.
	PEPServiceID ID
	// NonceHash is the SHA-256 hex digest of the presented nonce. The raw nonce
	// is never persisted, and uniqueness is scoped to PEPServiceID.
	NonceHash string
	// RequestFingerprint binds the claim to the canonical subject, tenant,
	// operation, content, nonce, issuance time, and capability vector.
	RequestFingerprint string
	// RequestIssuedAt is the presented request time used by the verifier's
	// freshness and future-skew checks.
	RequestIssuedAt Timestamp
	// State is "pending" until a verdict is durably finalized, then "final".
	State string
	// VerdictJSON is the finalized decision document. It is empty while the
	// claim is pending.
	VerdictJSON string
	// VerdictHash binds later protocol phases to the exact finalized verdict.
	// It is empty while the claim is pending.
	VerdictHash string
	// CapabilityVersion is the registered PEP capability version intersected
	// for the finalized decision.
	CapabilityVersion int
	// EffectiveCapabilities is the declared-and-registered control vector used
	// by the finalized decision.
	EffectiveCapabilities map[string]bool
	// PolicyVersion identifies the policy snapshot that produced the finalized
	// verdict.
	PolicyVersion string
	// ClaimedAt is the server time at which the single-use claim was inserted.
	ClaimedAt Timestamp
	// FinalizedAt is the server time at which the pending claim became final;
	// nil preserves crash-recoverable pending state.
	FinalizedAt *Timestamp
	// EvidenceAnchored is true IFF the most recent REQUIRED per-operation audit
	// for this row (the claim/overclaim at insert, then the finalize) is DURABLE.
	// A drop by a DEGRADE-mode audit spool leaves it false, turning the row into a
	// deny-closed tombstone every path (retry, service binding, finalize) refuses.
	// Deny-closed default: an unset/false value is treated as NOT anchored.
	EvidenceAnchored bool
}
