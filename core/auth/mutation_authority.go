// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// RouteMutationAuthorization is process-local evidence that ONE mutation question
// was authorized for ONE reconstructed principal, carrying the complete authority
// bundle that mutation's transaction must pin.
//
// ⛔ IT IS NOT A PERMIT, AND EVERY OMITTED METHOD IS THE REASON. There is no
// exported constructor, no mutable field, no boolean, no witness accessor and no
// codec: a value of this type can only come out of AuthorizeRouteMutation, and the
// only question it answers is AuthorityFor — "what authority did that exact
// decision rest on, and does it still hold at this instant for this exact
// question?". A caller that could read a bool out of it would eventually read the
// bool and skip the barrier, which is the failure this shape exists to prevent.
//
// ⛔ HOLDING ONE IS NOT PERMISSION TO WRITE. The bundle it returns must be locked
// on the SAME transaction as the product write, through
// store.AuthoritySnapshotBundleLocker; a consumer that needs the User fence and
// finds only the tenant-only locker must refuse, never fall back. Nothing here
// re-reads the directory, so authority that changed after issuance is caught by
// that lock and by nothing in this file.
//
// ⛔ ITS LIFETIME IS FINITE AND IT DOES NOT REPLAY. The value is valid only inside
// the half-open evidence window [ObservedAt, FreshUntil) it captured, which is
// already bounded by credential expiry and AAL expiry upstream; it cannot extend
// either, it is not a bearer credential, not a durable approval, not cross-process
// proof, and not evidence that a later external process effect can execute. Copying
// a valid value yields evidence for the same question, not a second permission:
// MA1 claims no single consumption, and deduplication belongs to the durable
// managed-stop intent that consumes it.
//
// The empty value always refuses.
type RouteMutationAuthorization struct {
	witness         RouteAuthorizationWitness
	issued          bool
	tenant          model.TenantID
	ref             PrincipalRef
	authorityMode   principalReadAuthorityMode
	userAuthority   store.UserAuthorityFactRef
	principalSeal   [sha256.Size]byte
	authorityDigest [sha256.Size]byte
}

// routeMutationAuthorityDomain is written verbatim as the first bytes of the
// integrity preimage. The trailing NUL is part of the domain, not a separator.
const routeMutationAuthorityDomain = "olivares.auth.route-mutation-authority.v1\x00"

// AuthorizeRouteMutation performs the ordinary route authorization EXACTLY ONCE and
// retains its complete authority for the mutation transaction that follows.
//
// ⛔ IT DOES NOT DUPLICATE THE AUTHORIZATION ALGEBRA AND IT DOES NOT PROMOTE A READ.
// The verdict is AuthorizeRoute's, including its step-up-before-evaluation order and
// its denied/undecided distinction; this method adds retention and refusal, never a
// second opinion. A human read decision remains unusable here even when its question
// spells a write, so DecideRouteRead is deliberately not called.
//
// ⛔ AND IT REQUIRES A FINITE, LIVE DEADLINE, which the older entry points do not.
// The value it returns outlives the call, so "how long may this stand?" has to have
// an answer at issuance: without a deadline there is nothing to bound the retained
// evidence against, and an unbounded lifetime is exactly what this type promises not
// to hand out. A missing or already-expired lifetime is ErrRouteUndecided — nothing
// denied, nothing could be established.
func (az *Authorizer) AuthorizeRouteMutation(
	ctx context.Context,
	req Request,
) (RouteMutationAuthorization, error) {
	if az == nil {
		return RouteMutationAuthorization{}, ErrAuthorizerUnavailable
	}
	if ctx == nil {
		return RouteMutationAuthorization{}, fmt.Errorf(
			"%w: a mutation authorization requires a context", ErrRouteUndecided)
	}
	// Cancellation is answered with the context's own error; an exhausted deadline is
	// the third answer, and it carries the cause so a caller can still tell them apart.
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.Canceled) {
			return RouteMutationAuthorization{}, err
		}
		return RouteMutationAuthorization{}, fmt.Errorf(
			"%w: the caller's lifetime is already over: %w", ErrRouteUndecided, err)
	}
	deadline, ok := ctx.Deadline()
	if !ok || deadline.IsZero() || !deadline.After(az.clock()) {
		return RouteMutationAuthorization{}, fmt.Errorf(
			"%w: a mutation authorization requires a finite, unexpired context deadline",
			ErrRouteUndecided)
	}

	// The retained value must not alias anything the caller still owns.
	copied := cloneEvidenceRequest(req)

	w, err := az.AuthorizeRoute(ctx, copied)
	if err != nil {
		return RouteMutationAuthorization{}, err
	}

	bundle, _, ok := principalCompleteAuthorizationEvidence(copied.Principal, copied.Tenant)
	if !ok {
		return RouteMutationAuthorization{}, fmt.Errorf(
			"%w: the principal's complete authority could not be extracted", ErrRouteUndecided)
	}
	credential, ok := copied.Principal.Ref()
	if !ok {
		return RouteMutationAuthorization{}, fmt.Errorf(
			"%w: the principal has no established credential reference", ErrRouteUndecided)
	}

	a := RouteMutationAuthorization{
		witness:       w,
		issued:        true,
		tenant:        copied.Tenant,
		ref:           credential,
		authorityMode: copied.Principal.evidence.authorityMode,
		userAuthority: copied.Principal.evidence.userAuthority,
		principalSeal: copied.Principal.evidence.seal,
	}
	a.witness.Decision.Facts = slices.Clone(w.Decision.Facts)
	if !a.wellFormed() {
		return RouteMutationAuthorization{}, fmt.Errorf(
			"%w: the authorized evidence is not a complete mutation authority", ErrRouteUndecided)
	}
	// The whole question, at the authorizer's clock — the same instant AuthorizeRoute
	// minted against — and a lifetime the caller's own deadline actually covers.
	if !a.witness.VerifyFor(az.clock(), copied) {
		return RouteMutationAuthorization{}, fmt.Errorf(
			"%w: the minted witness does not verify for this question", ErrRouteUndecided)
	}
	if a.witness.Decision.FreshUntil.After(deadline) {
		return RouteMutationAuthorization{}, fmt.Errorf(
			"%w: the evidence window outlives the caller's deadline", ErrRouteUndecided)
	}
	// The bundle extraction is the source of the User fence; the facts are the
	// witness's, which already carry the same directory fact in canonical order.
	if !slices.Equal(bundle.UserAuthorities, a.capturedUserAuthorities()) {
		return RouteMutationAuthorization{}, fmt.Errorf(
			"%w: the extracted User fence disagrees with the sealed provenance", ErrRouteUndecided)
	}
	a.authorityDigest = a.completeDigest()

	if err := ctx.Err(); err != nil {
		return RouteMutationAuthorization{}, err
	}
	return a, nil
}

// AuthorityFor returns the complete authority bundle this authorization rests on,
// for exactly this question, at exactly this instant.
//
// ⛔ IT VERIFIES; IT DOES NOT REFRESH. No database is queried, no principal is
// reconstructed, no epoch advances, no lifetime is extended and no external call is
// made. A principal whose provenance changed — a new seal, a bumped User version,
// another credential — is a DIFFERENT reconstruction and needs a fresh
// authorization, even when its resource ID is identical.
//
// ⛔ AND IT RETURNS ALL OR NOTHING. Every refusal is an empty bundle and
// ErrRouteUndecided; a partially valid bundle would be locked as though it were
// complete. The returned slices are fresh on every call, so editing one changes
// neither the result nor a later return.
//
// The bundle it returns is an input to store.AuthoritySnapshotBundleLocker on the
// mutation's own transaction. Receiving it is not permission to write.
func (a RouteMutationAuthorization) AuthorityFor(
	now time.Time,
	req Request,
) (store.AuthoritySnapshotBundle, error) {
	if !a.wellFormed() || a.completeDigest() != a.authorityDigest {
		return store.AuthoritySnapshotBundle{}, ErrRouteUndecided
	}
	// IsSound is minted ∧ ALLOW ∧ inside [ObservedAt, FreshUntil) ∧ unedited;
	// AnswersQuestion is the whole question. VerifyFor is both, and both are needed.
	if !a.witness.VerifyFor(now, req) {
		return store.AuthoritySnapshotBundle{}, ErrRouteUndecided
	}
	bundle, _, ok := principalCompleteAuthorizationEvidence(req.Principal, req.Tenant)
	if !ok {
		return store.AuthoritySnapshotBundle{}, ErrRouteUndecided
	}
	credential, ok := req.Principal.Ref()
	if !ok || req.Tenant != a.tenant || credential != a.ref ||
		req.Principal.evidence.seal != a.principalSeal ||
		req.Principal.evidence.authorityMode != a.authorityMode ||
		req.Principal.evidence.userAuthority != a.userAuthority ||
		!slices.Equal(bundle.UserAuthorities, a.capturedUserAuthorities()) {
		return store.AuthoritySnapshotBundle{}, ErrRouteUndecided
	}
	return store.AuthoritySnapshotBundle{
		Facts:           slices.Clone(a.witness.Decision.Facts),
		UserAuthorities: a.capturedUserAuthorities(),
	}, nil
}

// capturedUserAuthorities is the captured human fence, or none for a token. It
// allocates on every call so no caller can reach the retained value.
func (a RouteMutationAuthorization) capturedUserAuthorities() []store.UserAuthorityFactRef {
	if a.authorityMode != principalHumanAuthority {
		return nil
	}
	return []store.UserAuthorityFactRef{a.userAuthority}
}

// wellFormed is every structural predicate that does not depend on the present or
// on a caller-supplied question: issued by the minter, an ALLOW, a supported
// explicit authority mode with matching User coordinates, and canonical facts.
//
// A mode this package does not know is refused rather than defaulted: a system
// actor or a synthetic principal gains no path here, and neither does a future
// mode that has not stated what its User coordinates mean.
func (a RouteMutationAuthorization) wellFormed() bool {
	w := a.witness
	if !a.issued || !w.minted || w.Decision.Outcome != EvidenceAllow ||
		!validPrincipalEvidenceTenant(a.tenant) || !validPrincipalRef(a.ref) {
		return false
	}
	switch a.authorityMode {
	case principalHumanAuthority:
		if a.ref.kind != KindUser || !validPrincipalEvidenceID(a.userAuthority.UserID) ||
			a.userAuthority.Version < 1 {
			return false
		}
	case principalTokenDirectoryOnly:
		if a.ref.kind != KindToken || a.userAuthority != (store.UserAuthorityFactRef{}) {
			return false
		}
	default:
		return false
	}
	// Canonical order and duplicate/lease-conflict refusal are issuance-time
	// properties of the fact vector; recomputing them here means an edited vector is
	// rejected rather than repaired into a matching one.
	facts, ok := canonicalEvidenceFacts(w.Decision.Facts)
	return ok && slices.Equal(facts, w.Decision.Facts)
}

// completeDigest is the RouteMutationAuthorization integrity codec: SHA-256 over the
// NUL-terminated domain followed by the exact concatenation below.
//
//	u64(v)  unsigned 64-bit big-endian
//	text(s) u64(len([]byte(s))) || []byte(s)
//
// In order: the ordinary witness EvidenceDigest as 32 raw bytes; the captured
// principal seal as 32 raw bytes; one authority-mode byte; the User count — zero for
// a token, one for a human — and, for a human, its canonical User ID as
// length-prefixed UTF-8 followed by its positive version.
//
// ⛔ IT IS A CONSISTENCY COMMITMENT IN A TRUSTED PROCESS, NOT A MAC. It is unkeyed
// and authenticates nothing against the process that computes it; it exists so an
// edited private field cannot pass for the value the minter produced.
//
// ⛔ THE TENANT FACTS ARE NOT ENCODED HERE, AND THAT IS NOT AN OMISSION. Every fact,
// including complete lease coordinates, is already bound by the ordinary witness's
// versioned digest, which wellFormed requires to recompute and to be canonical;
// encoding them a second time would be a second implementation of one binding, and
// two implementations of a binding drift until one binds less than the other. The
// existing codec is not changed, and no decoder is added: this value is never
// serialized, so there is nothing to decode.
func (a RouteMutationAuthorization) completeDigest() [sha256.Size]byte {
	h := sha256.New()
	h.Write([]byte(routeMutationAuthorityDomain))
	h.Write(a.witness.EvidenceDigest[:])
	h.Write(a.principalSeal[:])
	h.Write([]byte{byte(a.authorityMode)})
	num := func(v uint64) {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], v)
		h.Write(b[:])
	}
	text := func(s string) { num(uint64(len(s))); h.Write([]byte(s)) }
	if a.authorityMode == principalHumanAuthority {
		num(1)
		text(a.userAuthority.UserID.String())
		num(uint64(a.userAuthority.Version))
	} else {
		num(0)
	}
	var digest [sha256.Size]byte
	copy(digest[:], h.Sum(nil))
	return digest
}
