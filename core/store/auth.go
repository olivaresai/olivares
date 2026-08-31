// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// AuthScope is the engine's authentication/authorization partition: typed access
// to the credential tables (users, memberships, groups, auth sessions, API
// tokens), all of which live in the reserved system tenant. It is obtained ONLY through
// Store.AuthView / Store.AuthMutate, which bind SystemTenantID as a normal,
// RLS-enforced tenant scope — never the cross-tenant System path (whose cleared
// tenant binding would match zero rows under Postgres FORCE row-level security).
//
// A module never holds a Store, so it can never reach an AuthScope: credentials
// are unreachable from module code by construction. Authentication, token
// resolution and membership enumeration all happen before any business tenant is
// resolved and all read across users — exactly why these rows live in one
// engine-privileged partition rather than scattered per business tenant.
type AuthScope interface {
	// Users is the user-account repository. Definitive deletion is deliberately
	// absent; core.engine.RetireUser is the only hard-delete path because it also
	// fences every tenant and persists an anchored global tombstone atomically.
	Users() MutableRepository[model.User]
	// Memberships is the (user → granted-tenant + role) repository. A user's
	// tenants are enumerated by filtering on user_id.
	Memberships() Repository[model.Membership]
	// Groups is the SCIM-provisioned directory-group repository. Group rows share
	// the system tenant, so callers MUST filter target_tenant_id — the column,
	// not RLS, is what isolates one business tenant's groups from another's.
	Groups() Repository[model.UserGroup]
	// GroupMembers is the (group → member user) repository, enumerated by
	// group_id (a group's roster) or user_id (a user's groups, the grants fold).
	GroupMembers() Repository[model.UserGroupMember]
	// Sessions is the opaque human/panel session repository (looked up by selector).
	Sessions() Repository[model.AuthSession]
	// Tokens is the programmatic API-token repository (looked up by selector).
	Tokens() Repository[model.APIToken]
	// WebAuthnCredentials is the registered-FIDO2-authenticator repository
	//: public verifier material per user, looked up by
	// user_id (a user's authenticators) or credential_id (assertion lookup).
	WebAuthnCredentials() Repository[model.WebAuthnCredential]
	// Invites is the pending-onboarding-invitation repository (FASE X):
	// single-use tokens (selector + secret hash) to activate a non-federated
	// account, looked up by selector (the accept leg) or filtered by
	// target_tenant_id (the console's pending list).
	Invites() Repository[model.UserInvite]
	// FederationConfigs is the managed SSO/IdP configuration repository (FASE X): one row per scope (target_tenant_id; SystemTenantID is the global
	// config), secret-bearing fields sealed at rest. Resolved at login and
	// edited from the console.
	FederationConfigs() Repository[model.FederationConfig]
	// FederationDomainClaims is the derived home-realm routing index (U8): one row
	// per email domain a FederationConfig claims, uniquely indexed on the domain so a
	// login resolves the owning IdP by a point lookup and a claimed domain is globally
	// unique at the storage layer. A projection of FederationConfig.ClaimedDomains,
	// maintained transactionally with the config write and converged at boot.
	FederationDomainClaims() Repository[model.FederationDomainClaim]
	// Secrets is the runtime secret store: named secrets whose value is
	// sealed at rest, one row per (scope, name) in the system tenant. The engine
	// resolves a `store:<name>` config reference to the opened value at Open; the
	// console/CLI manage them. Reachable only here (auth partition), so a module
	// can never read a secret value.
	Secrets() Repository[model.SecretEntry]
	// Sources is the durable source roster: one row per operator-defined
	// observation source, holding connector settings + secret REFERENCES (never
	// values), one per (scope, name) in the system tenant. The live reconciler
	// diffs this against the running engine to add/remove/rotate connectors
	// without a restart; the console/CLI author it. Reachable only here (auth
	// partition), like the secret store it references.
	Sources() Repository[model.SourceDef]
	// SeenJTIs is the SET jti de-dup repository: append-only records of
	// processed SET identifiers, keyed by (publisher_id, jti) with a TTL
	// (expires_at) for garbage collection.
	SeenJTIs() Repository[model.SETSeenJTI]
	// DelegationHandles is the opaque delegation authority surface, keyed by
	// selector for verification and by expiry for bounded garbage collection. It is
	// intentionally the NARROW DelegationHandleStore (Get/List/Create/Delete): a
	// minted handle's ceiling, expiry and audience are immutable, and the only
	// permitted post-mint mutation — revocation — goes through
	// RevokeDelegationHandle. The generic Update is deliberately NOT exposed, so a
	// core caller can neither rewrite a handle's authority nor recover Update by a
	// type assertion.
	DelegationHandles() DelegationHandleStore
	// RevokeDelegationHandle flips ONLY the revoked_at column (plus updated_at and
	// version) of the handle identified by jti, via a targeted revoked_at IS NULL
	// guarded UPDATE. The changed return is true only when this call actually flipped
	// a row, so the caller audits delegation.revoke exactly once and a concurrent
	// second revoke emits no duplicate event. It is idempotent: revoking an
	// already-revoked handle is a no-op returning (false, nil); an absent handle
	// returns (false, ErrNotFound); a reload fault that is not ErrNotFound is
	// propagated. It is the only post-mint handle mutation on AuthScope, so a caller
	// can never rewrite a handle's ceiling, expiry or audience through it.
	RevokeDelegationHandle(ctx context.Context, jti model.ID, revokedAt model.Timestamp) (changed bool, err error)
	// PEPServices is the registered Policy Enforcement Point repository. Rows
	// live in the system auth partition and carry their governed business tenant
	// in TargetTenantID.
	PEPServices() Repository[model.PEPService]
	// PEPServiceCredentials binds purpose-restricted API credentials to
	// registered PEP service identities, including overlapping rotation rows.
	PEPServiceCredentials() Repository[model.PEPServiceCredential]
	// PDPDecisionClaims is the READ-ONLY decision-claim surface. A claim is
	// created only through ClaimDecision and mutated only through
	// FinalizeDecisionClaim; the generic Create/Update/Delete are deliberately
	// NOT exposed, so no core caller can forge a final claim, rewrite a decision,
	// or erase a pending single-use record through generic CRUD.
	PDPDecisionClaims() PDPClaimReader
	// ClaimDecision atomically inserts a pending claim. A uniqueness collision
	// on the handle JTI or service-scoped nonce hash returns the conflicting row
	// with created=false; a collision is replay data for the caller to classify,
	// never a storage error. On a fresh insert it emits the REQUIRED per-operation
	// audits INSIDE the transaction — delegation.claim always, and
	// delegation.capability_overclaim when droppedOverclaims is non-empty (sanitized,
	// sorted vocabulary) — and PERSISTS their outcome on the row's evidence_anchored
	// column (true IFF both anchored, Seq>0). A DEGRADE-mode drop leaves the row
	// un-anchored WITHOUT an error, so the caller COMMITS the effect + the durable
	// gap accounting and refuses the observable decision AFTER commit
	// (evidence-or-refuse). The persisted flag is the source of truth: the
	// conflict-reload path (created==false) returns the existing row with its
	// evidence_anchored, so a dropped anchor keeps every retry refused (deny-closed)
	// instead of bypassing on the second call.
	ClaimDecision(
		ctx context.Context,
		claim model.PDPDecisionClaim,
		droppedOverclaims map[string]bool,
	) (stored model.PDPDecisionClaim, created bool, err error)
	// FinalizeDecisionClaim atomically transitions the pending claim identified by
	// id to final, version-locked on version and guarded by the pending state. It
	// takes only the MINIMAL finalization inputs — the store FORCES state='final'
	// and stamps finalized_at/updated_at from its own clock, never from the caller.
	// It self-guards the verdict material (rejecting empty/non-JSON verdictJSON or a
	// verdictHash that is not sha256(verdictJSON) with ErrInvalidVerdict) and emits
	// the delegation.finalize audit — attributed to the claim's OWN immutable
	// PEPServiceID, never a caller-supplied actor — INSIDE the same transaction, so a
	// raw store caller can neither finalize with forged material, forge finalize
	// attribution, nor finalize without evidence. It is the ONLY claim mutation
	// exposed on AuthScope, so a core caller can neither forge a claim nor overwrite
	// an already finalized decision. It returns ErrConflict when the claim is no
	// longer pending at version (a concurrent finalize won) and ErrNotFound when
	// absent. The evidenceDropped return reports that the transition committed but its
	// delegation.finalize audit was dropped by a DEGRADE-mode spool (loss accounting
	// durably committed, no anchor); the caller COMMITS the transaction and refuses
	// the observable decision AFTER commit (evidence-or-refuse). It is false on the
	// idempotent no-op paths (no new audit is attempted).
	FinalizeDecisionClaim(ctx context.Context, id model.ID, version int64, verdictJSON []byte, verdictHash, policyVersion string) (evidenceDropped bool, err error)
	// Audit is the system-tenant evidence ledger, so auth events (login, token
	// issue/revoke, membership change) are recorded with the real actor in the
	// same transaction as the change.
	Audit() AuditLog
}

// PDPClaimReader is the READ-ONLY decision-claim surface exposed on AuthScope.
// Claims are created only through AuthScope.ClaimDecision and mutated only
// through AuthScope.FinalizeDecisionClaim; the generic Create/Update/Delete are
// intentionally absent so a core caller cannot forge a final claim, rewrite a
// decision, or erase a pending single-use record.
type PDPClaimReader interface {
	// Get returns the claim, or ErrNotFound if it is absent or owned by another
	// tenant.
	Get(ctx context.Context, id model.ID) (model.PDPDecisionClaim, error)
	// List returns a page of claims matching q, ordered by q.Sort then id.
	List(ctx context.Context, q model.Query) ([]model.PDPDecisionClaim, model.Page, error)
}

// DelegationHandleStore is the NARROW delegation-handle surface exposed on
// AuthScope. It exposes Get, List, Create and Delete — everything Mint (Create),
// selector lookup (List) and the expiry sweep (List, Delete) need — but
// deliberately OMITS Update: a minted handle's ceiling, expiry and audience are
// immutable, and the only permitted post-mint mutation, revocation, goes through
// the specialized AuthScope.RevokeDelegationHandle op (which flips revoked_at and
// nothing else). A core caller therefore cannot rewrite a handle's authority, and
// cannot type-assert this surface back to store.Repository to recover Update.
type DelegationHandleStore interface {
	// Get returns the handle, or ErrNotFound if it is absent or owned by another
	// tenant.
	Get(ctx context.Context, id model.ID) (model.DelegationHandle, error)
	// List returns a page of handles matching q, ordered by q.Sort then id.
	List(ctx context.Context, q model.Query) ([]model.DelegationHandle, model.Page, error)
	// Create inserts a new handle and returns it with stamped base fields.
	Create(ctx context.Context, v model.DelegationHandle) (model.DelegationHandle, error)
	// Delete removes the handle (hard delete). It returns ErrNotFound if the handle
	// is absent/other-tenant.
	Delete(ctx context.Context, id model.ID) error
}
