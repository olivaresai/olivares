// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
)

var errMalformedAuthorizationLeaseFence = errors.New(
	"store: malformed authorization lease/fence witness",
)

// AuthorizationLeaseFenceWitness is the minimal server-observed semantic
// witness needed to pin a leased fact. Its fields stay private so it cannot be
// populated by decoding an external payload or serialized as part of an
// AuthorizationFactRef. Callers construct one only through
// NewLeaseFenceAuthorizationFactRef.
type AuthorizationLeaseFenceWitness struct {
	subject  string
	fence    int64
	deadline model.Timestamp
}

// AuthorizationFactRef is an opaque version witness for one server-selected
// authorization fact. It carries no exported or serializable row fields: a
// workspace-confined caller may ask the store to keep a previously observed
// decision stable, but cannot use this capability to read tenant-wide data.
type AuthorizationFactRef struct {
	Kind    model.Kind
	ID      model.ID
	Version int64

	leaseFence AuthorizationLeaseFenceWitness
}

// LockedLeasedVersionConflict identifies the supplied leased fact whose locked
// row version differed. It carries no current row values and does not establish
// that the lease semantics or other authority facts remain valid. Callers use
// errors.Is for ErrConflict compatibility and Matches for this precise failure.
type LockedLeasedVersionConflict struct {
	ref AuthorizationFactRef
}

// NewLockedLeasedVersionConflict represents a locked leased-version failure.
// This constructor validates shape only; descriptor admission and the actual
// locked comparison belong to the Store implementation. Invalid input retains
// ordinary ErrConflict behavior without identifying a matching leased fact.
func NewLockedLeasedVersionConflict(ref AuthorizationFactRef) error {
	if !validLockedLeasedVersionRef(ref) {
		return ErrConflict
	}
	return &LockedLeasedVersionConflict{ref: ref}
}

func (*LockedLeasedVersionConflict) Error() string { return ErrConflict.Error() }

// Unwrap preserves the legacy conflict classification without revealing a row.
func (*LockedLeasedVersionConflict) Unwrap() error { return ErrConflict }

// Matches compares the complete original input, including its opaque witness.
// A nil or zero receiver never matches, including another malformed reference.
func (e *LockedLeasedVersionConflict) Matches(ref AuthorizationFactRef) bool {
	if e == nil || !validLockedLeasedVersionRef(e.ref) || !validLockedLeasedVersionRef(ref) {
		return false
	}
	sid, fence, deadline, _ := e.ref.LeaseFenceWitness()
	otherSID, otherFence, otherDeadline, _ := ref.LeaseFenceWitness()
	return e.ref.Kind == ref.Kind && e.ref.ID == ref.ID && e.ref.Version == ref.Version &&
		sid == otherSID && fence == otherFence && deadline.String() == otherDeadline.String()
}

func validLockedLeasedVersionRef(ref AuthorizationFactRef) bool {
	sid, fence, deadline, ok := ref.LeaseFenceWitness()
	if !ok {
		return false
	}
	_, err := NewLeaseFenceAuthorizationFactRef(ref.Kind, ref.ID, ref.Version, sid, fence, deadline)
	return err == nil
}

// NewLeaseFenceAuthorizationFactRef constructs an opaque leased authority
// reference from a row already observed by trusted server code. The store still
// revalidates every value after taking the row lock; this constructor is shape
// validation, not an authorization decision.
func NewLeaseFenceAuthorizationFactRef(
	kind model.Kind,
	id model.ID,
	version int64,
	subject string,
	fence int64,
	deadline model.Timestamp,
) (AuthorizationFactRef, error) {
	if !kind.Valid() || id.IsZero() || version < 1 || subject == "" || len(subject) > 1024 ||
		fence < 1 || deadline.IsZero() {
		return AuthorizationFactRef{}, errMalformedAuthorizationLeaseFence
	}
	if _, err := model.ParseTimestamp(deadline.String()); err != nil {
		return AuthorizationFactRef{}, errMalformedAuthorizationLeaseFence
	}
	return AuthorizationFactRef{
		Kind: kind, ID: id, Version: version,
		leaseFence: AuthorizationLeaseFenceWitness{
			subject: subject, fence: fence, deadline: deadline,
		},
	}, nil
}

// LeaseFenceWitness returns the semantic witness, when this ref was created as
// a leased fact. It never reads or returns a store row.
func (r AuthorizationFactRef) LeaseFenceWitness() (
	subject string,
	fence int64,
	deadline model.Timestamp,
	ok bool,
) {
	w := r.leaseFence
	if w.subject == "" || w.fence < 1 || w.deadline.IsZero() {
		return "", 0, model.Timestamp{}, false
	}
	return w.subject, w.fence, w.deadline, true
}

// AuthoritySnapshotLocker is an OPTIONAL Mutate-scope capability. It locks
// every referenced row on the surrounding transaction and succeeds only when
// all versions still match. A descriptor may additionally opt into an exact
// lease/fence validation and transaction-stamped OCC touch. Implementations
// must accept only entity descriptors explicitly marked as authorization facts,
// must return no row payload, and must fail closed when any reference is
// malformed, missing, stale or not allowlisted. A View scope returns ErrReadOnly.
//
// Workspace confinement preserves this capability because it reveals no row;
// the repositories themselves remain confined. This is the narrow bridge for
// atomically pinning tenant-wide authority facts to a workspace-local write.
type AuthoritySnapshotLocker interface {
	LockAuthoritySnapshot(context.Context, []AuthorizationFactRef) error
}

// UserAuthorityFactRef identifies one observed User fence without exposing a
// SYSTEM row or a repository. It is separate from the 1..64 tenant fact budget.
type UserAuthorityFactRef struct {
	UserID  model.ID
	Version int64
}

// AuthUserAuthorityEvidenceScope optionally observes one User fence through the
// current AuthView transaction. Trusted credential infrastructure selects the
// User ID. It returns no User or credential payload and never repairs absence.
// An AuthMutate scope may also implement this read-only operation; observation
// does not acquire a lock or confer authority.
type AuthUserAuthorityEvidenceScope interface {
	ReadUserAuthorityFact(context.Context, model.ID) (UserAuthorityFactRef, error)
}

// AuthoritySnapshotBundle is a single acquisition: canonical User fences first,
// then tenant facts in their existing lock order. Equal User references dedupe;
// conflicting versions refuse the whole bundle before taking any row lock.
type AuthoritySnapshotBundle struct {
	Facts           []AuthorizationFactRef
	UserAuthorities []UserAuthorityFactRef
}

// AuthoritySnapshotBundleLocker is optional. Consumers requiring User evidence
// must fail closed if it is absent; the legacy locker is not a substitute.
type AuthoritySnapshotBundleLocker interface {
	LockAuthoritySnapshotBundle(context.Context, AuthoritySnapshotBundle) error
}

// AuthoritySnapshotBundleReader optionally validates User fences and tenant
// facts in one View without authority locks or product writes. Facts contains 1..64
// references; UserAuthorities contains 0..64 supplied references before equal
// duplicates are removed. Conflicting User versions fail before any query.
// Consumers requiring User evidence must not substitute the legacy validator.
type AuthoritySnapshotBundleReader interface {
	ValidateAuthoritySnapshotBundle(context.Context, AuthoritySnapshotBundle) error
}

// AuthTenantAuthorityBarrier is an OPTIONAL auth-partition capability (ATA1).
//
// Credentials, users, memberships and invitations live under the reserved system
// tenant, but the decision that authorizes changing one of them belongs to a
// BUSINESS tenant. An AuthScope is pinned to SYSTEM, so the ordinary bundle
// locker cannot bind that tenant's epoch: it would refuse the business fact
// rather than silently authorize it, and a caller must never substitute SYSTEM's
// own epoch to satisfy that grammar. This barrier is the one Store-owned
// entrypoint that acquires directory admission and pins the complete supplied
// authority for one business tenant inside the SAME AuthMutate transaction.
//
// The tenant must be valid, non-zero and not the system tenant. The bundle keeps
// the existing fact allowlist, canonical order, lease coordinates, 1..64 fact
// budget, duplicate refusal and epoch-to-tenant binding; UserAuthorities accepts
// 0..64 supplied references before equal duplicates are removed. No fact, epoch
// or lease coordinate may be dropped or synthesized.
//
// The caller MUST invoke it before any repository row lock, write or audit lock
// in that callback. Unlocked reads may precede it but are not authoritative final
// target-state checks. It refuses a prior authority acquisition, an already-used
// directory writer and recorded audit-before-directory state; it cannot detect
// every preceding raw row lock, so the caller's own construction and tests must
// establish that order. An AuthView assertion may succeed, but invocation returns
// ErrReadOnly without a write.
//
// A successful return is a TRANSACTION-LOCAL PIN and nothing else. It is not an
// ALLOW decision, a principal, a credential, an invitation retry, a process-effect
// permit or a cross-Store atomicity claim: the rows stay pinned only until the
// surrounding transaction completes. A missing capability is an infrastructure
// refusal — consumers must fail closed and must not substitute a read validator
// or a tenant-only lock.
type AuthTenantAuthorityBarrier interface {
	LockAuthTenantAuthority(context.Context, model.TenantID, AuthoritySnapshotBundle) error
}

// DirectoryAuthoritySnapshotLocker is an OPTIONAL ordinary Mutate-scope
// capability (DAB1).
//
// A human-authorized BUSINESS mutation may need to pin SYSTEM User authority
// (H) and later append its tenant audit event. Supported AuthMutate writers
// always acquire the global directory lock BEFORE either User authority or
// audit, and enabled global spool budgeting adds a shared accounting row. A
// business caller that locked H first and only then appended audit could cycle
// with an auth writer that had already taken spool. The ordinary Mutate lineage
// prelude serializes same-tenant business writers; it does not serialize a
// SYSTEM AuthMutate against that business tenant.
//
// This capability is the one Store-owned entrypoint that takes global directory
// admission and pins the complete supplied authority for the SURROUNDING
// business transaction's own tenant, in the declared order:
//
//	ordinary lineage prelude -> directory global -> canonical H ->
//	canonical complete tenant facts -> product row -> tenant audit (+ spool)
//
// There is NO tenant parameter: the enclosing ordinary Mutate already selected
// the tenant, and this capability refuses to act for any other one. A read-only
// View returns ErrReadOnly and a SYSTEM-pinned scope refuses, so the auth
// partition cannot reach a business barrier through it. The bundle keeps the
// existing fact allowlist, canonical order, lease coordinates, 1..64 fact
// budget, duplicate refusal and epoch-to-tenant binding; UserAuthorities accepts
// 0..64 supplied references before equal duplicates are removed. No fact, epoch
// or lease coordinate may be dropped or synthesized, and admission itself bumps
// no User version, directory epoch or directory source row.
//
// The caller MUST invoke it as the first lock-bearing operation of the callback,
// after the Store-owned prelude. It refuses the states the tracker can actually
// observe: read-only, a poisoned binding, a missing or poisoned directory
// tracker, a prior authority acquisition, an already-used directory writer, and
// recorded audit-before-directory or authority-before-directory order. It CANNOT
// detect an arbitrary preceding raw row lock, nor a preceding
// TransactionLocker.LockTransaction call — PostgreSQL advisory transaction locks
// are reentrant, so spelling the store's internal directory key through that
// public port produces untracked exclusion and no refusal. Those two remain
// CALLER OBLIGATIONS that a consumer's own construction and contract tests must
// establish; this interface does not claim to enforce them.
//
// A successful return is a TRANSACTION-LOCAL PIN and nothing else. It is not an
// ALLOW decision, a principal, a process-effect permit, an audit receipt or a
// cross-Store atomicity claim: the rows stay pinned only until the surrounding
// transaction completes. Absence is meaningful only for a non-SQL implementation
// that does not provide it — every SQL scope exposes the method and refuses at
// runtime — so a consumer must handle BOTH absence and runtime refusal
// explicitly, and must never substitute AuthoritySnapshotBundleLocker, which
// takes no directory admission.
//
// Workspace confinement forwards this exact operation because it accepts opaque
// version references and returns no rows; the repositories themselves stay
// confined. Optionality is preserved: a raw scope that lacks the method never
// gains it.
type DirectoryAuthoritySnapshotLocker interface {
	LockDirectoryAuthoritySnapshot(context.Context, AuthoritySnapshotBundle) error
}
