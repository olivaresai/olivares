// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// degradeFixture provisions under a healthy spool, then reopens the same store
// with a one-byte DEGRADE budget. Tenant lifecycle operations now require their
// own durable audit anchor, so provisioning directly under a budget guaranteed to
// drop every event would correctly refuse before reaching the delegation behavior
// these tests exercise.
func degradeFixture(t *testing.T) delegFixture {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "delegation-degrade.db")
	healthy, err := sqlstore.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: dsn,
	}, nil)
	if err != nil {
		t.Fatalf("open healthy fixture store: %v", err)
	}
	fixture := newDelegFixtureFromStore(t, healthy)
	if err := healthy.Close(); err != nil {
		t.Fatalf("close healthy fixture store: %v", err)
	}

	degraded, err := sqlstore.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: dsn,
		AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade,
	}, nil)
	if err != nil {
		t.Fatalf("open degrade store: %v", err)
	}
	t.Cleanup(func() { _ = degraded.Close() })
	fixture.st = degraded
	fixture.a = auth.NewAuthenticator(degraded, nil)
	return fixture
}

// pendingDrops reads the GLOBAL durable loss-accounting counter. Provisioning the
// assertions below are on the DELTA across the call under test — an absolute
// ">=1" could pass from unrelated prior loss and miss a rolled-back audit.
func pendingDrops(t *testing.T, st store.Store) int64 {
	t.Helper()
	status, ok, err := st.(store.AuditSpoolStatuser).AuditSpoolStatus(context.Background())
	if err != nil {
		t.Fatalf("spool status: %v", err)
	}
	if !ok {
		t.Fatal("audit spool budget not configured on the degrade store")
	}
	return status.PendingDrops
}

// claimIDForHandle reads back the decision-claim decision id owning the given handle
// JTI, so a finalize test can target the pending claim even when the claim call
// itself refused evidence-or-refuse (the claim ROW is durably committed regardless).
func claimIDForHandle(t *testing.T, f delegFixture, handleJTI model.ID) model.ID {
	t.Helper()
	var id model.ID
	if err := f.st.AuthView(f.ctx, func(as store.AuthScope) error {
		claims, _, err := as.PDPDecisionClaims().List(f.ctx, model.Query{
			Filters: []model.Filter{{Column: "handle_jti", Op: model.OpEq, Value: handleJTI.String()}},
			Limit:   1,
		})
		if err != nil {
			return err
		}
		if len(claims) != 1 {
			t.Fatalf("want exactly 1 claim for handle %s, got %d", handleJTI, len(claims))
		}
		id = claims[0].ID
		return nil
	}); err != nil {
		t.Fatalf("read claim for handle: %v", err)
	}
	return id
}

// claimByID reads back the persisted claim row (state + evidence_anchored) so a test
// can assert whether a finalize attempt transitioned or left a tombstone intact.
func claimByID(t *testing.T, f delegFixture, id model.ID) model.PDPDecisionClaim {
	t.Helper()
	var claim model.PDPDecisionClaim
	if err := f.st.AuthView(f.ctx, func(as store.AuthScope) error {
		c, err := as.PDPDecisionClaims().Get(f.ctx, id)
		if err != nil {
			return err
		}
		claim = c
		return nil
	}); err != nil {
		t.Fatalf("read claim %s: %v", id, err)
	}
	return claim
}

// TestFinalizeDoesNotResurrectTombstonedClaim pins the D4-followup fix: a claim whose
// OWN claim/overclaim audit dropped under degrade (committed pending && !anchored) can
// NEVER be finalized — otherwise a healthy finalize would overwrite evidence_anchored
// false→true using only the delegation.finalize anchor and resurrect a decision whose
// claim anchor is still lost. A finalize attempt must refuse AND leave the row an intact
// tombstone (still pending, still !anchored), so a later claim retry keeps refusing.
// Before the fix the finalize TRANSITIONED the row to final (opening the resurrection).
func TestFinalizeDoesNotResurrectTombstonedClaim(t *testing.T) {
	f := degradeFixture(t)
	token, handle := f.mint(t, f.serviceA)
	pr := freshPresented("degrade-resurrect", "messages")

	// The claim's delegation.claim audit drops → tombstoned (pending && !anchored).
	if _, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr); !errors.Is(err, auth.ErrDelegationEvidenceFault) {
		t.Fatalf("claim err = %v, want ErrDelegationEvidenceFault", err)
	}
	decisionID := claimIDForHandle(t, f, handle.ID)
	if pre := claimByID(t, f, decisionID); pre.State != "pending" || pre.EvidenceAnchored {
		t.Fatalf("precondition: claim = {state %q, anchored %v}, want {pending, false}", pre.State, pre.EvidenceAnchored)
	}

	// A finalize on the tombstone must refuse and NOT transition the row.
	if err := f.a.FinalizeDecisionClaim(f.ctx, decisionID, []byte(`{"decision":"allow"}`), "policy-v1"); !errors.Is(err, auth.ErrDelegationEvidenceFault) {
		t.Fatalf("finalize of a tombstone err = %v, want ErrDelegationEvidenceFault", err)
	}
	// THE fix signal: the row is still an intact pending tombstone — before the fix the
	// finalize flipped it to state='final', which a recovered-spool finalize could then
	// re-anchor to true and expose the verdict.
	if post := claimByID(t, f, decisionID); post.State != "pending" || post.EvidenceAnchored {
		t.Fatalf("tombstone was disturbed by finalize: claim = {state %q, anchored %v}, want {pending, false}", post.State, post.EvidenceAnchored)
	}
	// A later claim retry still resolves the persisted tombstone and refuses.
	if _, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr); !errors.Is(err, auth.ErrDelegationEvidenceFault) {
		t.Fatalf("post-finalize claim retry err = %v, want ErrDelegationEvidenceFault", err)
	}
}

// TestFinalizeOfAnchoredClaimRefusesWhenFinalizeAuditDrops covers the OTHER finalize
// leg — an ALREADY-ANCHORED claim finalized during a LATER degrade episode, where the
// finalize audit itself drops. This is reproducible by reopening the SAME database with
// a different spool budget: the store recomputes usage on every budgeted boot, so a
// claim anchored under a healthy spool, then finalized after reopening under a 1-byte
// DEGRADE budget, commits pending→final but its delegation.finalize audit drops. The
// commit-then-classify discipline then refuses AFTER commit (gap counted, row a
// final,!anchored tombstone), and an idempotent retry keeps refusing.
func TestFinalizeOfAnchoredClaimRefusesWhenFinalizeAuditDrops(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "anchored-finalize-drop.db")

	// Phase 1 — healthy spool: provision, mint, and claim so the claim ANCHORS.
	healthy, err := sqlstore.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: dsn,
	}, nil)
	if err != nil {
		t.Fatalf("open healthy store: %v", err)
	}
	f := newDelegFixtureFromStore(t, healthy)
	token, handle := f.mint(t, f.serviceA)
	if _, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), freshPresented("anchored-final", "messages")); err != nil {
		t.Fatalf("healthy claim must anchor and allow: %v", err)
	}
	decisionID := claimIDForHandle(t, f, handle.ID)
	if pre := claimByID(t, f, decisionID); pre.State != "pending" || !pre.EvidenceAnchored {
		t.Fatalf("precondition: claim = {state %q, anchored %v}, want {pending, true}", pre.State, pre.EvidenceAnchored)
	}
	if err := healthy.Close(); err != nil {
		t.Fatalf("close healthy store: %v", err)
	}

	// Phase 2 — reopen the SAME db under a 1-byte DEGRADE budget: the finalize audit
	// now drops. A fresh authenticator over the reopened store runs the finalize.
	degraded, err := sqlstore.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: dsn,
		AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade,
	}, nil)
	if err != nil {
		t.Fatalf("reopen degrade store: %v", err)
	}
	t.Cleanup(func() { _ = degraded.Close() })
	a2 := auth.NewAuthenticator(degraded, nil)
	ctx := context.Background()

	before := pendingDrops(t, degraded)
	err = a2.FinalizeDecisionClaim(ctx, decisionID, []byte(`{"decision":"allow"}`), "policy-v1")
	if !errors.Is(err, auth.ErrDelegationEvidenceFault) {
		t.Fatalf("finalize with a dropped finalize audit err = %v, want ErrDelegationEvidenceFault", err)
	}
	// The transition COMMITTED (final) but the finalize audit dropped, so the row is a
	// final tombstone (!anchored) and the gap advanced (commit-then-classify, not a
	// rolled-back gate).
	if after := pendingDrops(t, degraded); after <= before {
		t.Fatalf("finalize gap rolled back: PendingDrops before=%d after=%d, want after>before", before, after)
	}
	post := claimByIDStore(t, ctx, degraded, decisionID)
	if post.State != "final" || post.EvidenceAnchored {
		t.Fatalf("anchored-claim finalize-drop = {state %q, anchored %v}, want {final, false}", post.State, post.EvidenceAnchored)
	}
	// An idempotent retry of the same finalize keeps refusing (final tombstone).
	if err := a2.FinalizeDecisionClaim(ctx, decisionID, []byte(`{"decision":"allow"}`), "policy-v1"); !errors.Is(err, auth.ErrDelegationEvidenceFault) {
		t.Fatalf("retry finalize of a final tombstone err = %v, want ErrDelegationEvidenceFault", err)
	}
}

// TestStoreFinalizeRefusesTombstonedClaimRaw pins the store-level backstop in
// isolation: a RAW AuthScope.FinalizeDecisionClaim on a tombstoned (pending &&
// !anchored) claim must return store.ErrEvidenceMissing and leave the row intact,
// so a core caller bypassing the auth wrapper's pre-check still cannot resurrect it.
func TestStoreFinalizeRefusesTombstonedClaimRaw(t *testing.T) {
	f := degradeFixture(t)
	token, handle := f.mint(t, f.serviceA)
	_, _ = f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), freshPresented("degrade-raw-final", "messages"))
	decisionID := claimIDForHandle(t, f, handle.ID)

	verdict := []byte(`{"decision":"allow"}`)
	hash := digest(string(verdict))
	err := f.st.AuthMutate(f.ctx, func(as store.AuthScope) error {
		// Read the current version, then attempt the raw store transition directly.
		claim, gerr := as.PDPDecisionClaims().Get(f.ctx, decisionID)
		if gerr != nil {
			return gerr
		}
		_, ferr := as.FinalizeDecisionClaim(f.ctx, claim.ID, claim.Version, verdict, hash, "policy-v1")
		return ferr
	})
	if !errors.Is(err, store.ErrEvidenceMissing) {
		t.Fatalf("raw store finalize of a tombstone err = %v, want store.ErrEvidenceMissing", err)
	}
	if post := claimByID(t, f, decisionID); post.State != "pending" || post.EvidenceAnchored {
		t.Fatalf("raw finalize disturbed the tombstone: claim = {state %q, anchored %v}, want {pending, false}", post.State, post.EvidenceAnchored)
	}
}

// claimByIDStore reads a claim directly from a store (used across a reopen, where the
// fixture's authenticator no longer owns the connection).
func claimByIDStore(t *testing.T, ctx context.Context, st store.Store, id model.ID) model.PDPDecisionClaim {
	t.Helper()
	var claim model.PDPDecisionClaim
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		c, err := as.PDPDecisionClaims().Get(ctx, id)
		if err != nil {
			return err
		}
		claim = c
		return nil
	}); err != nil {
		t.Fatalf("read claim %s: %v", id, err)
	}
	return claim
}

// TestVerifyAndClaimDelegationRefusesOnDegradeSpoolDrop pins FIX A for the claim leg:
// when the delegation.claim audit is DROPPED by a degrade-mode spool, the effect
// committed (the claim row + the durable gap accounting) but there is no
// per-operation anchor, so the OBSERVABLE decision MUST be refused with
// ErrDelegationEvidenceFault — mirroring the inference proxy's F9 discipline. The
// DELTA assertion proves the drop's loss accounting was COMMITTED, not rolled back.
func TestVerifyAndClaimDelegationRefusesOnDegradeSpoolDrop(t *testing.T) {
	f := degradeFixture(t)
	st := f.st
	token, _ := f.mint(t, f.serviceA)

	before := pendingDrops(t, st)
	_, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), freshPresented("degrade-claim", "messages"))
	if !errors.Is(err, auth.ErrDelegationEvidenceFault) {
		t.Fatalf("VerifyAndClaimDelegation under degrade err = %v, want ErrDelegationEvidenceFault", err)
	}
	if after := pendingDrops(t, st); after <= before {
		t.Fatalf("claim gap rolled back (rollback bug): PendingDrops before=%d after=%d, want after>before", before, after)
	}
}

// TestFinalizeDecisionClaimRefusesOnDegradeSpoolDrop pins the finalize leg under
// degrade. In a fixed-budget degrade store the CLAIM audit also drops, so the claim
// is a persisted tombstone (pending && !anchored). A finalize of a tombstone is
// refused by the PRE-TRANSITION anchor check (a read-only Get), so it MUST return
// ErrDelegationEvidenceFault WITHOUT attempting the finalize audit — hence the gap
// counter does NOT advance across this call (the tombstone refusal is free, and this
// pre-transition read rolls nothing back).
//
// Note: the OTHER finalize path — an ALREADY-ANCHORED claim finalized during a LATER
// degrade episode, where the finalize audit itself drops and the row commits final
// then refuses after commit — is not reproducible in a single fixed-budget test store
// (a degrade store cannot first ANCHOR a claim). That commit-then-classify shape is the
// same one the claim leg proves empirically here; the store op returns dropped=(finalize
// Seq==0) and the wrapper refuses after commit.
func TestFinalizeDecisionClaimRefusesOnDegradeSpoolDrop(t *testing.T) {
	f := degradeFixture(t)
	st := f.st
	token, handle := f.mint(t, f.serviceA)

	// Create the pending claim; ignore the claim leg's own evidence-or-refuse outcome.
	_, _ = f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), freshPresented("degrade-final", "messages"))
	decisionID := claimIDForHandle(t, f, handle.ID)

	verdict := []byte(`{"decision":"allow"}`)
	before := pendingDrops(t, st)
	err := f.a.FinalizeDecisionClaim(f.ctx, decisionID, verdict, "policy-v1")
	if !errors.Is(err, auth.ErrDelegationEvidenceFault) {
		t.Fatalf("FinalizeDecisionClaim of a degrade tombstone err = %v, want ErrDelegationEvidenceFault", err)
	}
	// The pre-transition tombstone refusal takes no finalize audit, so no NEW gap is
	// counted across the call (and the claim row is left an intact tombstone).
	if after := pendingDrops(t, st); after != before {
		t.Fatalf("tombstone finalize should take no new audit: PendingDrops before=%d after=%d, want equal", before, after)
	}
	if post := claimByID(t, f, decisionID); post.State != "pending" || post.EvidenceAnchored {
		t.Fatalf("tombstone disturbed: claim = {state %q, anchored %v}, want {pending, false}", post.State, post.EvidenceAnchored)
	}
}

// TestVerifyAndClaimDelegationSecondCallRefusesAfterDroppedClaim pins the D4 retry
// bypass: when a claim's delegation.claim audit dropped under degrade, the ROW is a
// persisted deny-closed tombstone, so an EXACT second presentation (which resolves the
// existing row without re-auditing) MUST ALSO refuse — deny-on-first must not become
// allow-on-immediate-retry while the spool is still degraded. Before the fix the second
// call ALLOWED (returned a sealed VerifiedDelegation with nil error), fail-open.
func TestVerifyAndClaimDelegationSecondCallRefusesAfterDroppedClaim(t *testing.T) {
	f := degradeFixture(t)
	st := f.st
	token, _ := f.mint(t, f.serviceA)
	pr := freshPresented("degrade-retry-claim", "messages")

	// First call: the delegation.claim audit drops → deny-on-first.
	if _, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr); !errors.Is(err, auth.ErrDelegationEvidenceFault) {
		t.Fatalf("first call err = %v, want ErrDelegationEvidenceFault", err)
	}
	// Second IDENTICAL presentation → resolves the persisted tombstone row. It MUST
	// refuse, NOT bypass evidence-or-refuse. The retry takes NO new audit, so the gap
	// counter must NOT advance across it (the refusal comes from persisted state).
	before := pendingDrops(t, st)
	_, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr)
	if !errors.Is(err, auth.ErrDelegationEvidenceFault) {
		t.Fatalf("retry call err = %v, want ErrDelegationEvidenceFault (persisted tombstone; before-fix it ALLOWED)", err)
	}
	if after := pendingDrops(t, st); after != before {
		t.Fatalf("retry advanced the gap counter: before=%d after=%d, want equal (no re-audit on retry)", before, after)
	}
}

// TestVerifyAndClaimDelegationSecondCallRefusesAfterDroppedOverclaim pins the same
// tombstone semantics when the dropped anchor is the delegation.capability_overclaim
// audit (a KNOWN-vocabulary capability the service does not register). The second call
// must still refuse.
func TestVerifyAndClaimDelegationSecondCallRefusesAfterDroppedOverclaim(t *testing.T) {
	f := degradeFixture(t)
	token, _ := f.mint(t, f.serviceA)
	pr := freshPresented("degrade-retry-overclaim", "messages")
	// serviceA registers buffer_request + streaming; declare batch (known vocab, NOT
	// registered) → an overclaim whose audit also drops under degrade.
	pr.DeclaredCapabilities = map[string]bool{"buffer_request": true, "batch": true}

	if _, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr); !errors.Is(err, auth.ErrDelegationEvidenceFault) {
		t.Fatalf("first call err = %v, want ErrDelegationEvidenceFault", err)
	}
	if _, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr); !errors.Is(err, auth.ErrDelegationEvidenceFault) {
		t.Fatalf("retry call err = %v, want ErrDelegationEvidenceFault (persisted tombstone; before-fix it ALLOWED)", err)
	}
}

// TestFinalizeDecisionClaimSecondCallRefusesAfterDroppedFinalize pins the finalize leg
// of the D4 bypass: when a finalize's delegation.finalize audit dropped, the row is a
// deny-closed final tombstone, so an EXACT second finalize (which hits the already-final
// idempotent branch) MUST ALSO refuse — NOT return nil. Before the fix the second
// finalize returned nil (idempotent success), fail-open.
func TestFinalizeDecisionClaimSecondCallRefusesAfterDroppedFinalize(t *testing.T) {
	f := degradeFixture(t)
	token, handle := f.mint(t, f.serviceA)

	_, _ = f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), freshPresented("degrade-retry-final", "messages"))
	decisionID := claimIDForHandle(t, f, handle.ID)

	verdict := []byte(`{"decision":"allow"}`)
	// First finalize: pending→final commits, the finalize audit drops → refuse.
	if err := f.a.FinalizeDecisionClaim(f.ctx, decisionID, verdict, "policy-v1"); !errors.Is(err, auth.ErrDelegationEvidenceFault) {
		t.Fatalf("first finalize err = %v, want ErrDelegationEvidenceFault", err)
	}
	// Second IDENTICAL finalize → already-final idempotent branch. It MUST still refuse.
	if err := f.a.FinalizeDecisionClaim(f.ctx, decisionID, verdict, "policy-v1"); !errors.Is(err, auth.ErrDelegationEvidenceFault) {
		t.Fatalf("retry finalize err = %v, want ErrDelegationEvidenceFault (final tombstone; before-fix it returned nil)", err)
	}
}

// TestCheckDecisionServiceBindingRefusesUnanchoredClaim pins that a decision with no
// durable anchor is not a usable service binding: a claim tombstoned under degrade must
// fail the binding check. Before the fix the binding check ignored the anchor and passed.
func TestCheckDecisionServiceBindingRefusesUnanchoredClaim(t *testing.T) {
	f := degradeFixture(t)
	token, handle := f.mint(t, f.serviceA)

	nonce := "degrade-binding"
	_, _ = f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), freshPresented(nonce, "messages"))
	decisionID := claimIDForHandle(t, f, handle.ID)

	if err := f.a.CheckDecisionServiceBinding(f.ctx, decisionID, nonce, f.pepA); !errors.Is(err, auth.ErrDecisionBindingMismatch) {
		t.Fatalf("binding on un-anchored claim err = %v, want ErrDecisionBindingMismatch (before-fix it passed nil)", err)
	}
}

// TestVerifyAndClaimDelegationHealthyAnchorsAndRetryAllowed is the positive control: a
// HEALTHY (non-degrade) claim anchors (evidence_anchored=true), so it is NOT refused and
// its idempotent retry still resolves. This guards against the tombstone fix breaking the
// happy path.
func TestVerifyAndClaimDelegationHealthyAnchorsAndRetryAllowed(t *testing.T) {
	f := newDelegFixture(t) // default (healthy) spool
	token, handle := f.mint(t, f.serviceA)
	pr := freshPresented("healthy-anchor", "messages")

	first, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr)
	if err != nil {
		t.Fatalf("healthy first claim: %v", err)
	}
	second, err := f.a.VerifyAndClaimDelegation(f.ctx, f.pepA, handleProof(token), pr)
	if err != nil {
		t.Fatalf("healthy retry: %v", err)
	}
	if !second.Retried() || first.DecisionID() != second.DecisionID() {
		t.Fatalf("healthy retry = {retried %v, id %q}, want idempotent {true, %q}", second.Retried(), second.DecisionID(), first.DecisionID())
	}
	// The persisted evidence_anchored flag is true (deny-closed default is false).
	assertClaimAnchored(t, f, handle.ID, true)
	// The healthy claim's binding check passes (it IS anchored).
	if err := f.a.CheckDecisionServiceBinding(f.ctx, first.DecisionID(), "healthy-anchor", f.pepA); err != nil {
		t.Fatalf("healthy binding check: %v", err)
	}
}

// assertClaimAnchored reads the persisted claim owning handleJTI and asserts its
// evidence_anchored flag equals want.
func assertClaimAnchored(t *testing.T, f delegFixture, handleJTI model.ID, want bool) {
	t.Helper()
	if err := f.st.AuthView(f.ctx, func(as store.AuthScope) error {
		claims, _, err := as.PDPDecisionClaims().List(f.ctx, model.Query{
			Filters: []model.Filter{{Column: "handle_jti", Op: model.OpEq, Value: handleJTI.String()}},
			Limit:   1,
		})
		if err != nil {
			return err
		}
		if len(claims) != 1 {
			t.Fatalf("want exactly 1 claim for handle %s, got %d", handleJTI, len(claims))
		}
		if claims[0].EvidenceAnchored != want {
			t.Fatalf("claim evidence_anchored = %v, want %v", claims[0].EvidenceAnchored, want)
		}
		return nil
	}); err != nil {
		t.Fatalf("read claim anchor: %v", err)
	}
}
