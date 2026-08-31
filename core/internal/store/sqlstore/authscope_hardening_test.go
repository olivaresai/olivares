// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestPDPDecisionClaimsReaderCannotRecoverRepository pins FIX 1 (M7): the
// read-only claim surface handed out by PDPDecisionClaims() must NOT be
// type-assertable back to the full store.Repository, and no Create/Update/Delete
// method may be reachable on it — otherwise a core caller could forge a final
// claim or rewrite a decision through generic CRUD, defeating the single-use
// contract.
func TestPDPDecisionClaimsReaderCannotRecoverRepository(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)

	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		reader := as.PDPDecisionClaims()
		if _, ok := reader.(store.Repository[model.PDPDecisionClaim]); ok {
			t.Error("PDPDecisionClaims() is assertable to store.Repository — generic CRUD recoverable")
		}
		if _, ok := reader.(interface {
			Create(context.Context, model.PDPDecisionClaim) (model.PDPDecisionClaim, error)
		}); ok {
			t.Error("PDPDecisionClaims() exposes Create")
		}
		if _, ok := reader.(interface {
			Update(context.Context, model.PDPDecisionClaim) (model.PDPDecisionClaim, error)
		}); ok {
			t.Error("PDPDecisionClaims() exposes Update")
		}
		if _, ok := reader.(interface {
			Delete(context.Context, model.ID) error
		}); ok {
			t.Error("PDPDecisionClaims() exposes Delete")
		}
		return nil
	}); err != nil {
		t.Fatalf("auth view: %v", err)
	}
}

// TestDelegationHandlesStoreCannotRecoverRepository pins FIX 2: the delegation
// handle surface must expose only Get/List/Create/Delete and must NOT be
// type-assertable back to store.Repository (which would recover Update and let a
// caller rewrite a handle's ceiling, expiry, audience or revoked_at directly).
func TestDelegationHandlesStoreCannotRecoverRepository(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)

	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		handles := as.DelegationHandles()
		if _, ok := handles.(store.Repository[model.DelegationHandle]); ok {
			t.Error("DelegationHandles() is assertable to store.Repository — Update recoverable")
		}
		if _, ok := handles.(interface {
			Update(context.Context, model.DelegationHandle) (model.DelegationHandle, error)
		}); ok {
			t.Error("DelegationHandles() exposes Update")
		}
		return nil
	}); err != nil {
		t.Fatalf("auth view: %v", err)
	}
}

// TestRevokeDelegationHandleStoreOpIdempotent confirms FIX 2(b) and FIX C: the
// specialized RevokeDelegationHandle op flips revoked_at (and bumps version) and
// REPORTS changed=true only when it actually flipped a row; a second call on an
// already-revoked handle is an idempotent no-op reporting changed=false; an absent
// handle returns (false, ErrNotFound). The changed signal is what lets the auth
// wrapper audit delegation.revoke exactly once (no duplicate on a concurrent loser).
func TestRevokeDelegationHandleStoreOpIdempotent(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	expires := model.NewTimestamp(time.Now().Add(5 * time.Minute))
	revokedAt := model.NewTimestamp(time.Now())

	var service model.PEPService
	var user model.User
	var token model.APIToken
	var handle model.DelegationHandle
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		var err error
		user, err = as.Users().Create(ctx, model.User{
			Email: "revoke-handle-source@example.test", Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		token, err = as.Tokens().Create(ctx, model.APIToken{
			Name: "revoke-handle-source", UserID: user.ID,
			Selector: "revoke-handle-source", SecretHash: []byte("source-hash"),
		})
		if err != nil {
			return err
		}
		service, err = as.PEPServices().Create(ctx, model.PEPService{Name: "revoke-pep"})
		if err != nil {
			return err
		}
		candidate := testDelegationHandle("revoke-selector", service.ID, expires)
		candidate.SourceCredID = token.ID
		candidate.SubjectUserID = user.ID
		handle, err = as.DelegationHandles().Create(ctx, candidate)
		return err
	}); err != nil {
		t.Fatalf("seed handle: %v", err)
	}

	var firstChanged bool
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		var e error
		firstChanged, e = as.RevokeDelegationHandle(ctx, handle.ID, revokedAt)
		return e
	}); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	if !firstChanged {
		t.Error("first revoke changed = false, want true (it flipped the row)")
	}
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		got, err := as.DelegationHandles().Get(ctx, handle.ID)
		if err != nil {
			return err
		}
		if got.RevokedAt == nil {
			t.Error("revoked_at not set after revoke")
		}
		if got.Version < 2 {
			t.Errorf("version = %d, want >= 2 after revoke", got.Version)
		}
		return nil
	}); err != nil {
		t.Fatalf("read revoked: %v", err)
	}

	// Second revoke is an idempotent no-op: changed=false, nil error.
	var secondChanged bool
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		var e error
		secondChanged, e = as.RevokeDelegationHandle(ctx, handle.ID, model.NewTimestamp(time.Now()))
		return e
	}); err != nil {
		t.Fatalf("idempotent second revoke: %v", err)
	}
	if secondChanged {
		t.Error("second revoke changed = true, want false (already revoked, nothing changed)")
	}

	// Absent handle is (false, ErrNotFound).
	var absentChanged bool
	err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		var e error
		absentChanged, e = as.RevokeDelegationHandle(ctx, model.NewID(), revokedAt)
		return e
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("revoke absent handle err = %v, want ErrNotFound", err)
	}
	if absentChanged {
		t.Error("absent revoke changed = true, want false")
	}
}

// TestStoreFinalizeDecisionClaimSelfGuardsAndAudits pins FIX 3: the store's
// FinalizeDecisionClaim op rejects forged verdict material on its own (empty or
// non-JSON verdict, or a hash that does not match the bytes) and writes the
// delegation.finalize audit INSIDE the same transaction — so a raw store caller
// can neither finalize with invalid material nor finalize without evidence.
func TestStoreFinalizeDecisionClaimSelfGuardsAndAudits(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	issued := model.NewTimestamp(time.Now())

	var service model.PEPService
	var claim model.PDPDecisionClaim
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		var err error
		service, err = as.PEPServices().Create(ctx, model.PEPService{Name: "finalize-pep"})
		if err != nil {
			return err
		}
		claim, _, err = as.ClaimDecision(ctx, model.PDPDecisionClaim{
			HandleJTI:          model.NewID(),
			PEPServiceID:       service.ID,
			NonceHash:          "finalize-nonce",
			RequestFingerprint: "finalize-fp",
			RequestIssuedAt:    issued,
		}, nil)
		return err
	}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}

	good := []byte(`{"decision":"allow"}`)
	goodHash := sha256HexBytes(good)

	// (a) Non-JSON verdict is rejected, even with a hash that matches the bytes.
	badJSON := []byte("not json")
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, e := as.FinalizeDecisionClaim(ctx, claim.ID, claim.Version, badJSON, sha256HexBytes(badJSON), "p1")
		return e
	}); !errors.Is(err, store.ErrInvalidVerdict) {
		t.Errorf("non-JSON finalize err = %v, want ErrInvalidVerdict", err)
	}

	// (b) Valid JSON with a hash that does NOT match the bytes is rejected.
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, e := as.FinalizeDecisionClaim(ctx, claim.ID, claim.Version, good, "deadbeef", "p1")
		return e
	}); !errors.Is(err, store.ErrInvalidVerdict) {
		t.Errorf("hash-mismatch finalize err = %v, want ErrInvalidVerdict", err)
	}

	// The rejected finalizes must NOT have advanced the claim out of pending.
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		got, err := as.PDPDecisionClaims().Get(ctx, claim.ID)
		if err != nil {
			return err
		}
		if got.State != "pending" {
			t.Errorf("state after rejected finalizes = %q, want pending", got.State)
		}
		return nil
	}); err != nil {
		t.Fatalf("read pending: %v", err)
	}

	// (c) A well-formed finalize succeeds, stamps state=final + a store finalized_at,
	// and leaves a delegation.finalize audit event.
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, e := as.FinalizeDecisionClaim(ctx, claim.ID, claim.Version, good, goodHash, "p1")
		return e
	}); err != nil {
		t.Fatalf("valid finalize: %v", err)
	}
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		got, err := as.PDPDecisionClaims().Get(ctx, claim.ID)
		if err != nil {
			return err
		}
		if got.State != "final" || got.FinalizedAt == nil {
			t.Errorf("finalized claim = %+v, want state=final with finalized_at set", got)
		}
		if got.VerdictHash != goodHash {
			t.Errorf("stored verdict hash = %q, want %q", got.VerdictHash, goodHash)
		}
		found := false
		if werr := as.Audit().Walk(ctx, 1, func(ev model.AuditEvent) error {
			if ev.Action == "delegation.finalize" && ev.TargetID == claim.ID {
				found = true
			}
			return nil
		}); werr != nil {
			return werr
		}
		if !found {
			t.Error("no delegation.finalize audit event written inside the store transaction")
		}
		return nil
	}); err != nil {
		t.Fatalf("verify finalize: %v", err)
	}
}

// TestAuthUsersCannotRecoverRepository pins the deny-closed property that
// core/store/repo.go:39-42 states in words and that nothing pinned in code: the
// value handed out by AuthScope.Users() must NOT be type-assertable back to the
// full store.Repository, because that recovers Delete — and definitive deletion
// of a user is deliberately absent from this surface, with core.engine.RetireUser
// named in core/store/auth.go:26-28 as the only hard-delete path (it also fences
// every tenant and persists an anchored global tombstone atomically).
//
// WHAT WAS ALREADY THERE, SAID PLAINLY BECAUSE THE FIRST VERSION OF THIS COMMENT
// GOT IT WRONG. The property was NOT unpinned: directoryretirement_test.go already
// makes both assertions on auth.Users(). What it does not do is make them inside
// AuthMutate — it only inspects AuthView — and the mutate scope is the dangerous
// one, because a caller already holding write authority is exactly who could use a
// recovered Delete. That gap, plus a non-firing direction no sibling guard has, is
// what this test adds.
//
// It became load-bearing when the kernel chain narrowed Users() from Repository to
// MutableRepository while the trunk kept the wide version. Note precisely which
// mistake the compiler catches and which it does not: keeping the trunk's SIGNATURE
// fails to build, because Go requires an exact match to satisfy AuthScope. What
// builds green is keeping the narrow signature and returning a WIDER VALUE — a
// Repository's method set is a superset of what the interface asks for — and that is
// the mutation M-1 below performs. No content gate can see it, because the hole is
// the ABSENCE of a wrapper rather than the presence of anything to search for.
//
// THE NON-FIRING DIRECTION IS A REAL WRITE, NOT AN INTERFACE ASSERTION, AND THAT
// CORRECTION IS THE POINT. The first version of this test asserted that Create and
// Update remained type-assertable — which cannot fail: the declared return type is
// store.MutableRepository, so the compiler already guarantees both, and a check
// that cannot go red measures nothing. What a deny-all wrapper would actually break
// is the OPERATION, so the second half performs one: a user is created through
// Users() and read back inside the same mutate scope. Without it, "hide everything"
// would satisfy every assertion above while breaking every legitimate caller.
func TestAuthUsersCannotRecoverRepository(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)

	assertNarrowed := func(t *testing.T, scope string, users store.MutableRepository[model.User]) {
		t.Helper()
		if _, ok := users.(store.Repository[model.User]); ok {
			t.Errorf("%s: Users() is assertable to store.Repository — hard delete recoverable", scope)
		}
		if _, ok := users.(interface {
			Delete(context.Context, model.ID) error
		}); ok {
			t.Errorf("%s: Users() exposes Delete", scope)
		}
	}

	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		assertNarrowed(t, "AuthView", as.Users())
		return nil
	}); err != nil {
		t.Fatalf("auth view: %v", err)
	}

	// The mutate scope matters most: a caller already holding write authority is
	// exactly who could use a recovered Delete.
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		users := as.Users()
		assertNarrowed(t, "AuthMutate", users)

		// Non-firing direction: the surface that must stay narrow must also stay
		// USABLE for what MutableRepository promises.
		created, err := users.Create(ctx, model.User{
			Email:  "narrowed-surface@olivares.test",
			Status: model.StatusActive,
		})
		if err != nil {
			return fmt.Errorf("Users().Create must still work on the narrowed surface: %w", err)
		}
		got, err := users.Get(ctx, created.ID)
		if err != nil {
			return fmt.Errorf("Users().Get must still work on the narrowed surface: %w", err)
		}
		if got.Email != created.Email {
			t.Errorf("read back email = %q, want %q", got.Email, created.Email)
		}
		return nil
	}); err != nil {
		t.Fatalf("auth mutate: %v", err)
	}
}
