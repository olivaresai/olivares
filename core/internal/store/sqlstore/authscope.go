// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// authScope implements store.AuthScope over a tenantScope that is pinned to the
// reserved system tenant. It is a thin typed view: every repository it hands out
// is a normal tenant-pinned genericRepo bound to SystemTenantID, so RLS (Postgres)
// and the tripwire triggers (SQLite) still apply — the auth partition is isolated
// on exactly the same terms as any tenant, it just happens to be the system one.
type authScope struct {
	ts *tenantScope
}

var _ store.AuthScope = (*authScope)(nil)
var _ store.AuthPrincipalEvidenceScope = (*authScope)(nil)

// The auth partition is where first-boot bootstrap decides "no user exists yet,
// so I may create the first one". That decision is only safe if it serializes
// across every process on the same database, and a decorator that handed out a
// scope WITHOUT this capability would make the caller fail closed rather than
// race — so the assertion below is the contract, not decoration.
var _ store.TransactionLocker = (*authScope)(nil)

// LockTransaction forwards store.TransactionLocker to the tenantScope that backs
// this auth view, so the lock lands on the SAME *sql.Tx the credential write
// commits in. Forwarding is not a convenience: authScope holds its tenantScope in
// a NAMED field, so nothing is promoted, and without this method the capability
// would be silently absent — which the interface's own contract says a
// correctness-sensitive caller must treat as failure.
func (a *authScope) LockTransaction(ctx context.Context, key string) error {
	return a.ts.LockTransaction(ctx, key)
}

// TransactionNow forwards the auth-partition capability to the exact
// transaction backing this AuthScope. authScope is deliberately a wrapper, not
// an embedded tenantScope, so the forwarding method is required for a caller's
// capability assertion to succeed.
func (a *authScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	return a.ts.TransactionNow(ctx)
}

// ReadDirectoryEpochFact reads one business tenant's directory generation
// through the exact transaction backing this auth view. Credential and
// membership rows live under SYSTEM, so the tenant pin is changed only for the
// epoch read and restored before returning. A failed restore is evidence
// unavailability even when the epoch itself was legible.
func (a *authScope) ReadDirectoryEpochFact(
	ctx context.Context,
	tenant model.TenantID,
) (store.AuthorizationFactRef, error) {
	if _, err := canonicalDirectoryTenants([]model.TenantID{tenant}); err != nil {
		return store.AuthorizationFactRef{}, err
	}

	var epoch model.DirectoryEpoch
	err := a.ts.withDirectoryTenantBinding(ctx, tenant, func() error {
		observed, found, err := readDirectoryEpochRow(ctx, a.ts.tx, a.ts.s.dia, tenant)
		if err != nil {
			return directoryUnavailable("read auth principal directory epoch", err)
		}
		if !found {
			return directoryUnavailable("auth principal directory epoch is absent", nil)
		}
		epoch = observed
		return nil
	})
	if err != nil {
		return store.AuthorizationFactRef{}, err
	}
	return store.AuthorizationFactRef{
		Kind: model.DirectoryEpochKind, ID: epoch.ID, Version: epoch.Version,
	}, nil
}

// Users hands out the MutableRepository the merged store.AuthScope declares:
// definitive deletion is deliberately absent from this surface, and the
// concrete value is wrapped so a caller cannot recover hard deletion with a
// type assertion. Returning the wider store.Repository here — which is what the
// trunk side of this merge did, self-consistently with its own narrower
// interface — would not fail to compile against every caller, because a
// Repository's method set is a superset; it would defeat the control silently.
func (a *authScope) Users() store.MutableRepository[model.User] {
	inner := newTypedRepo(a.ts.repo(userDescriptor), userCodec)
	tracked := newDirectoryTrackedRepo(
		inner, a.ts.directoryWriter, authUserDirectoryResolver(a.ts),
	)
	return newMutableDirectoryRepo(tracked)
}

func (a *authScope) Memberships() store.Repository[model.Membership] {
	inner := newTypedRepo(a.ts.repo(membershipDescriptor), membershipCodec)
	return newDirectoryTrackedRepo(
		inner, a.ts.directoryWriter, authMembershipDirectoryResolver(a.ts, inner),
	)
}

func (a *authScope) Groups() store.Repository[model.UserGroup] {
	inner := newTypedRepo(a.ts.repo(userGroupDescriptor), userGroupCodec)
	return newDirectoryTrackedRepo(
		inner, a.ts.directoryWriter, authGroupDirectoryResolver(inner),
	)
}

func (a *authScope) GroupMembers() store.Repository[model.UserGroupMember] {
	inner := newTypedRepo(a.ts.repo(userGroupMemberDescriptor), userGroupMemberCodec)
	groups := newTypedRepo(a.ts.repo(userGroupDescriptor), userGroupCodec)
	return newDirectoryTrackedRepo(
		inner, a.ts.directoryWriter,
		authGroupMemberDirectoryResolver(a.ts, inner, groups),
	)
}

func (a *authScope) Sessions() store.Repository[model.AuthSession] {
	inner := newTypedRepo(a.ts.repo(authSessionDescriptor), authSessionCodec)
	return authSessionAuthorityRepo(a.ts, inner)
}

func (a *authScope) Tokens() store.Repository[model.APIToken] {
	inner := newTypedRepo(a.ts.repo(apiTokenDescriptor), apiTokenCodec)
	return authTokenAuthorityRepo(a.ts, inner)
}

func (a *authScope) WebAuthnCredentials() store.Repository[model.WebAuthnCredential] {
	inner := newTypedRepo(a.ts.repo(webauthnCredentialDescriptor), webauthnCredentialCodec)
	return webAuthnAuthorityRepo(a.ts, inner)
}

func (a *authScope) Invites() store.Repository[model.UserInvite] {
	return newTypedRepo(a.ts.repo(userInviteDescriptor), userInviteCodec)
}

func (a *authScope) FederationConfigs() store.Repository[model.FederationConfig] {
	return newTypedRepo(a.ts.repo(federationConfigDescriptor), federationConfigCodec)
}

func (a *authScope) FederationDomainClaims() store.Repository[model.FederationDomainClaim] {
	return newTypedRepo(a.ts.repo(federationDomainClaimDescriptor), federationDomainClaimCodec)
}

func (a *authScope) Secrets() store.Repository[model.SecretEntry] {
	return newTypedRepo(a.ts.repo(secretEntryDescriptor), secretEntryCodec)
}

func (a *authScope) Sources() store.Repository[model.SourceDef] {
	return newTypedRepo(a.ts.repo(sourceDefDescriptor), sourceDefCodec)
}

func (a *authScope) SeenJTIs() store.Repository[model.SETSeenJTI] {
	return newTypedRepo(a.ts.repo(setSeenJTIDescriptor), setSeenJTICodec)
}

func (a *authScope) DelegationHandles() store.DelegationHandleStore {
	inner := newTypedRepo(a.ts.repo(delegationHandleDescriptor), delegationHandleCodec)
	return delegationHandleStore{inner: delegationHandleAuthorityRepo(a.ts, inner)}
}

func (a *authScope) PEPServices() store.Repository[model.PEPService] {
	return newTypedRepo(a.ts.repo(pepServiceDescriptor), pepServiceCodec)
}

func (a *authScope) PEPServiceCredentials() store.Repository[model.PEPServiceCredential] {
	inner := newTypedRepo(a.ts.repo(pepServiceCredentialDescriptor), pepServiceCredentialCodec)
	return pepServiceCredentialAuthorityRepo(a.ts, inner)
}

func (a *authScope) PDPDecisionClaims() store.PDPClaimReader {
	return pdpClaimReader{inner: newTypedRepo(a.ts.repo(pdpDecisionClaimDescriptor), pdpDecisionClaimCodec)}
}

// pdpClaimReader is the CONCRETE read-only claim surface returned by
// PDPDecisionClaims. It deliberately does NOT embed store.Repository: its method
// set is EXACTLY Get + List, so a core caller cannot type-assert it back to
// store.Repository[model.PDPDecisionClaim] and recover Create/Update/Delete to
// forge a final claim or rewrite a decision. It delegates field by field to the
// inner typed repo.
type pdpClaimReader struct {
	inner store.Repository[model.PDPDecisionClaim]
}

func (r pdpClaimReader) Get(ctx context.Context, id model.ID) (model.PDPDecisionClaim, error) {
	return r.inner.Get(ctx, id)
}

func (r pdpClaimReader) List(ctx context.Context, q model.Query) ([]model.PDPDecisionClaim, model.Page, error) {
	return r.inner.List(ctx, q)
}

// delegationHandleStore is the CONCRETE narrow handle surface returned by
// DelegationHandles. Like pdpClaimReader it deliberately does NOT embed
// store.Repository: its method set is EXACTLY Get/List/Create/Delete, so a core
// caller cannot type-assert it back to store.Repository[model.DelegationHandle]
// and recover Update to rewrite a handle's ceiling, expiry, audience or
// revoked_at. It delegates field by field to the inner typed repo.
type delegationHandleStore struct {
	inner store.Repository[model.DelegationHandle]
}

func (r delegationHandleStore) Get(ctx context.Context, id model.ID) (model.DelegationHandle, error) {
	return r.inner.Get(ctx, id)
}

func (r delegationHandleStore) List(ctx context.Context, q model.Query) ([]model.DelegationHandle, model.Page, error) {
	return r.inner.List(ctx, q)
}

func (r delegationHandleStore) Create(ctx context.Context, v model.DelegationHandle) (model.DelegationHandle, error) {
	return r.inner.Create(ctx, v)
}

func (r delegationHandleStore) Delete(ctx context.Context, id model.ID) error {
	return r.inner.Delete(ctx, id)
}

// Audit hands out an audit log that takes the GLOBAL directory lock before it takes
// any tenant-audit lock, so the order retirement needs — global before audit — holds
// without the AuthMutate prelude taking that lock on every transaction.
//
// WHY THIS SHAPE AND NOT noteAudit. noteAudit only RECORDS that audit came first so a
// later prepare can refuse. That is right for an ordinary Mutate and wrong here,
// because AuthMutate must ACCEPT audit-then-credential — directoryretirement_test.go
// pins that sequence finishing, and the earlier candidate died by turning it into a
// rejection. Taking the global FIRST makes the sequence legal instead of merely
// detectable, which is what the prelude bought; the difference is that it is now paid
// by the transactions that actually touch audit rather than by every one of them, so a
// read-only callback never takes a global lock and two of them can no longer deadlock.
//
// AND IT COVERS LockAppends, the hole that killed the earlier candidate: auditLog
// .Append calls noteAudit before its own lockTenant, but the optional
// AuditAppendLocker capability takes the same transaction lock WITHOUT it, and
// AuthScope.Audit is exactly where that object is handed out.
func (a *authScope) Audit() store.AuditLog {
	return &authAuditLog{auditLog: a.ts.auditLog(), ts: a.ts}
}

// authAuditLog embeds the CONCRETE *auditLog, not the store.AuditLog interface, and the
// difference is the whole correctness of this type.
//
// Embedding the INTERFACE forwards the methods that interface declares and silently
// drops every OPTIONAL capability the underlying value also had —
// RecordedHeadReader, CanonicalWalker and VerifiedAuditAnchorReader
// (core/store/audit.go:154,187,203) — because a type assertion interrogates the OUTER
// type, and the outer type satisfies only what it declares. Forwarding is about calls;
// assertions are about identity, and an embedded interface preserves the first and
// destroys the second.
//
// Declaring the missing methods one by one is WORSE, not better: a declared method makes
// the wrapper satisfy that interface UNCONDITIONALLY, so a caller's comma-ok is true even
// when the inner value cannot do the thing — the wrapper then has to answer something,
// and answering nil is a capability OVERCLAIM that reports success for work never done.
//
// Embedding the concrete type ends both failure modes at once: every method is promoted,
// so the wrapper's capability set is EXACTLY the inner's, by construction rather than by
// an inventory somebody has to keep in step.
type authAuditLog struct {
	*auditLog
	ts *tenantScope
}

// globalFirst takes the directory writer's global lock before any audit lock. prepare
// only acquires once, so repeated calls are free.
func (a *authAuditLog) globalFirst(ctx context.Context) error {
	return a.ts.directoryWriter.prepare(ctx, func() ([]model.TenantID, error) {
		return nil, nil
	})
}

func (a *authAuditLog) Append(
	ctx context.Context,
	d model.AuditDraft,
) (model.AuditEvent, error) {
	if err := a.globalFirst(ctx); err != nil {
		return model.AuditEvent{}, err
	}
	return a.auditLog.Append(ctx, d)
}

// LockAppends takes the global lock first and then delegates to the concrete inner log,
// which really does implement the capability (audit.go:504). There is no comma-ok here
// on purpose: with a concrete embed the capability is not in doubt, so there is no
// branch that could answer nil and pretend a lock was taken.
func (a *authAuditLog) LockAppends(ctx context.Context) error {
	if err := a.globalFirst(ctx); err != nil {
		return err
	}
	return a.auditLog.LockAppends(ctx)
}

// AuthView runs fn against the auth partition in a read-only transaction pinned
// to the system tenant. SystemTenantID is non-zero, so View accepts it and binds
// it for RLS; this is the deliberate, RLS-enforced path to the credential tables,
// NOT the GUC-clearing System path.
func (s *sqlStore) AuthView(ctx context.Context, fn func(store.AuthScope) error) error {
	return s.View(ctx, model.SystemTenantID, func(sc store.Scope) error {
		return fn(&authScope{ts: sc.(*tenantScope)})
	})
}

// AuthMutate runs fn against the auth partition in a read-write transaction
// pinned to the system tenant, so a credential change and its audit event commit
// atomically.
func (s *sqlStore) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	return s.Mutate(ctx, model.SystemTenantID, func(sc store.Scope) error {
		ts := sc.(*tenantScope)
		// No unconditional prelude here, and its absence is the design.
		//
		// This used to take the global directory lock for EVERY auth transaction,
		// to stop a callback taking the tenant-audit lock first and only later
		// reaching for the global one -- retirement takes global then audit, so the
		// inverse order is a cycle. The cost was that two auth transactions
		// serialized on a global lock even when neither wrote directory at all.
		//
		// The order is preserved where it is actually created instead. Every guarded
		// wrapper calls prepare before it validates or writes its source, and
		// authScope.Audit hands out a log that takes the global lock before any
		// audit lock, so the callback cannot reach audit-then-global from either
		// door. That is what the norm asks for: the ADDENDUM binds every mutation
		// and every wrapper, not the transaction envelope, and a transaction that
		// mutates no directory owes it nothing.
		return fn(&authScope{ts: ts})
	})
}
