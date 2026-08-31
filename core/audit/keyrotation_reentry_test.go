// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit_test

import (
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestReEntryDoesNotWidenTheRetiredKeyFence is the contract for H-01(b), and
// the shape it measures is NOT the one the audit report described.
//
// The report re-ran the ceremony twice back to back and observed the fence move
// 4 -> 5. That widening is INERT: seq 5 is the first marker itself, and
// VerifyEventsFenced skips ActionAuditKeyRotation, so no ordinary event changes
// verdict. Reproducing only that shape would have proven a harmless off-by-one and
// closed the finding at the wrong severity.
//
// The shape that bites is re-entry AFTER SERVICE RESUMES. The ceremony takes the
// CURRENT head as the new boundary (cmd/olivares/cmd_audit_keyrotation.go:118-137),
// so once ordinary events exist above the honest boundary, the second marker fences
// the RETIRED key over the epoch that belongs to the NEW one. That is not a one-slot
// hole: it is DE-REVOCATION. An actor holding the retired key — the exact actor the
// boundary exists to lock out — regains a valid epoch over events it never signed,
// and the widened marker is itself off-box-signed, so the verifier accepts it.
//
// The oracle is the message the contrast used, kept verbatim so the two agree on
// what is being asserted:
//
//	re-entry widened retired key fence to N; honest boundary was M
func TestReEntryDoesNotWidenTheRetiredKeyFence(t *testing.T) {
	ctx := context.Background()
	_, retiredPriv, _ := ed25519.GenerateKey(nil)
	retiredPub := retiredPriv.Public().(ed25519.PublicKey)
	kms := newMockKMS(t)
	signer, err := audit.NewSigner(retiredPriv, audit.WithCheckpointKey(kms))
	if err != nil {
		t.Fatal(err)
	}
	// Every ordinary event in this store is signed by retiredPriv. Above the honest
	// boundary that stands for the forged tail an actor holding the retired key
	// could write; below it, it is the key's own legitimate epoch.
	st := signedStore(t, signer)
	tenant := provisionTenant(t, st) // org.create at seq 1
	appendEvents(t, st, tenant, 3)   // seqs 2,3,4 -> honest tail = 4

	newPub, _, _ := ed25519.GenerateKey(nil)
	retiredFP, newFP := audit.KeyFingerprint(retiredPub), audit.KeyFingerprint(newPub)
	const honestBoundary = 4

	record := func(priorLastSeq int64) {
		t.Helper()
		if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			_, _, rerr := audit.RecordKeyRotation(ctx, sc.Audit(), signer, audit.KeyRotationEvidence{
				Tenant:           tenant.String(),
				PriorFingerprint: retiredFP,
				PriorLastSeq:     priorLastSeq,
				NewFingerprint:   newFP,
				OffBoxKeyID:      kms.KeyID(),
			})
			return rerr
		}); err != nil {
			t.Fatalf("record key transition at boundary %d: %v", priorLastSeq, err)
		}
	}

	// Ceremony run 1, engine stopped: the honest boundary. Marker lands at seq 5.
	record(honestBoundary)

	// SERVICE RESUMES. Ordinary events accrue above the boundary — seqs 6,7,8.
	appendEvents(t, st, tenant, 3)

	// The operator re-enters the ceremony (a re-run after a partial failure, a
	// runbook executed twice). The command would derive the boundary from the
	// CURRENT head, which is now 8, not 4.
	headNow := headOf(t, st, tenant)
	if headNow != 8 {
		t.Fatalf("setup: head = %d, want 8", headNow)
	}
	record(headNow)

	der, _ := kms.PublicKey(ctx)
	pinned := audit.NewCheckpointVerifier()
	if err := pinned.AddPublicKey(kms.Algorithm(), der); err != nil {
		t.Fatal(err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		fences, ferr := audit.LocateKeyFences(ctx, sc.Audit(), pinned)
		if ferr != nil {
			return ferr
		}
		got := fences[retiredFP]
		if got != honestBoundary {
			t.Fatalf("re-entry widened retired key fence to %d; honest boundary was %d", got, honestBoundary)
		}
		// WHICH barrier holds this, measured rather than assumed. TWO now stand
		// between re-entry and a widened fence: RecordKeyRotation suppresses the
		// duplicate, and LocateKeyFences would keep the smaller boundary anyway.
		// Reverting ONLY the fence rule leaves this test green, because the
		// suppression blocks the path before the rule is ever consulted (verified by
		// mutation: LARGEST-wins survives here). So this test owns the SUPPRESSION —
		// asserted directly below — and the fence rule is owned by
		// TestConflictingFencesAreReportedNotResolvedSilently, where a second marker
		// genuinely exists. Neither assertion covers for the other.
		markers := 0
		if err := sc.Audit().Walk(ctx, 1, func(e model.AuditEvent) error {
			if e.Action == store.ActionAuditKeyRotation {
				markers++
			}
			return nil
		}); err != nil {
			return err
		}
		if markers != 1 {
			t.Fatalf("chain carries %d key-rotation markers, want 1: the re-entry was not suppressed", markers)
		}

		// The consequence, asserted directly rather than inferred from the number:
		// under the honest fence the retired key must NOT verify the events above
		// its epoch. If this passes while the fence is widened, the retired key has
		// been de-revoked over the new key's epoch.
		rep, eerr := audit.VerifyEventsFenced(ctx, sc.Audit(), 1, []audit.FencedKey{{Key: retiredPub, LastSeq: got}})
		if eerr != nil {
			return eerr
		}
		if rep.OK {
			t.Fatalf("the retired key verified EVERY event under fence %d: events above the honest boundary %d were accepted under a key that must not sign them (de-revocation)", got, honestBoundary)
		}
		// The first rejected event is seq 6, NOT honestBoundary+1 == 5. Seq 5 is the
		// first marker, and VerifyEventsFenced skips ActionAuditKeyRotation because
		// markers carry their own off-box domain. That gap is precisely why the
		// widening the audit report measured (4 -> 5) was inert, and why this test
		// had to append ordinary events above the boundary to expose the real defect.
		const firstOrdinaryAboveBoundary = 6
		if rep.FirstBadSeq != firstOrdinaryAboveBoundary {
			t.Fatalf("first rejected event = seq %d, want %d (the first ORDINARY event above the honest boundary; seq %d is the marker and is skipped)",
				rep.FirstBadSeq, firstOrdinaryAboveBoundary, honestBoundary+1)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestReEntryIsIdempotentAtTheSameBoundary pins the other half: re-running the
// ceremony with the SAME retired/new pair must not append a second marker at all.
// Suppressing the duplicate is what makes the ceremony safe to re-run, which is what
// makes it resumable — a re-run skips what is already recorded and continues.
func TestReEntryIsIdempotentAtTheSameBoundary(t *testing.T) {
	ctx := context.Background()
	_, retiredPriv, _ := ed25519.GenerateKey(nil)
	retiredPub := retiredPriv.Public().(ed25519.PublicKey)
	kms := newMockKMS(t)
	signer, err := audit.NewSigner(retiredPriv, audit.WithCheckpointKey(kms))
	if err != nil {
		t.Fatal(err)
	}
	st := signedStore(t, signer)
	tenant := provisionTenant(t, st)
	appendEvents(t, st, tenant, 3) // tail = 4

	newPub, _, _ := ed25519.GenerateKey(nil)
	ev := audit.KeyRotationEvidence{
		Tenant:           tenant.String(),
		PriorFingerprint: audit.KeyFingerprint(retiredPub),
		PriorLastSeq:     4,
		NewFingerprint:   audit.KeyFingerprint(newPub),
		OffBoxKeyID:      kms.KeyID(),
	}
	var first, second model.AuditEvent
	var firstExisted, secondExisted bool
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var rerr error
		first, firstExisted, rerr = audit.RecordKeyRotation(ctx, sc.Audit(), signer, ev)
		return rerr
	}); err != nil {
		t.Fatal(err)
	}
	if firstExisted || first.Seq != 5 {
		t.Fatalf("first record: existed=%v seq=%d (want false, 5)", firstExisted, first.Seq)
	}
	// Re-enter with the identical pair. The boundary the caller derives is now the
	// CURRENT head (5) — exactly what the command would pass on a re-run — so this
	// also proves the suppression is keyed on the KEY PAIR, not on the boundary.
	ev.PriorLastSeq = headOf(t, st, tenant)
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var rerr error
		second, secondExisted, rerr = audit.RecordKeyRotation(ctx, sc.Audit(), signer, ev)
		return rerr
	}); err != nil {
		t.Fatal(err)
	}
	if !secondExisted {
		t.Fatal("re-entry with the same retired/new pair appended a SECOND marker: the ceremony is not idempotent")
	}
	if second.Seq != first.Seq {
		t.Fatalf("re-entry reported marker seq %d, want the existing %d", second.Seq, first.Seq)
	}
	if got := headOf(t, st, tenant); got != first.Seq {
		t.Fatalf("chain head moved to %d after a suppressed re-entry (want %d): something was appended", got, first.Seq)
	}
}

// TestConflictingFencesAreReportedNotResolvedSilently covers the duplicate that
// idempotence does NOT suppress: two rotations away from the same retired key
// (A -> B, then A -> C) legitimately produce two markers naming A, with different
// boundaries. LocateKeyFences keeps the smallest, which is safe — but silence would
// mean a ledger recording that a revocation boundary MOVED and nothing saying so.
// The fail-closed pass must name it.
func TestConflictingFencesAreReportedNotResolvedSilently(t *testing.T) {
	ctx := context.Background()
	_, retiredPriv, _ := ed25519.GenerateKey(nil)
	retiredPub := retiredPriv.Public().(ed25519.PublicKey)
	kms := newMockKMS(t)
	signer, err := audit.NewSigner(retiredPriv, audit.WithCheckpointKey(kms))
	if err != nil {
		t.Fatal(err)
	}
	st := signedStore(t, signer)
	tenant := provisionTenant(t, st)
	appendEvents(t, st, tenant, 3) // tail = 4
	retiredFP := audit.KeyFingerprint(retiredPub)

	// Two DIFFERENT successors, so the idempotence probe (keyed on the pair) does
	// not suppress the second marker.
	genB, _, _ := ed25519.GenerateKey(nil)
	genC, _, _ := ed25519.GenerateKey(nil)
	record := func(newPub ed25519.PublicKey, boundary int64) {
		t.Helper()
		if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			_, _, rerr := audit.RecordKeyRotation(ctx, sc.Audit(), signer, audit.KeyRotationEvidence{
				Tenant: tenant.String(), PriorFingerprint: retiredFP, PriorLastSeq: boundary,
				NewFingerprint: audit.KeyFingerprint(newPub), OffBoxKeyID: kms.KeyID(),
			})
			return rerr
		}); err != nil {
			t.Fatalf("record boundary %d: %v", boundary, err)
		}
	}
	record(genB, 4)                // honest boundary, marker at seq 5
	appendEvents(t, st, tenant, 2) // seqs 6,7
	record(genC, 7)                // a SECOND boundary naming the same retired key, at seq 8

	der, _ := kms.PublicKey(ctx)
	pinned := audit.NewCheckpointVerifier()
	if err := pinned.AddPublicKey(kms.Algorithm(), der); err != nil {
		t.Fatal(err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		fences, ferr := audit.LocateKeyFences(ctx, sc.Audit(), pinned)
		if ferr != nil {
			return ferr
		}
		if got := fences[retiredFP]; got != 4 {
			t.Fatalf("fence for the retired key = %d, want 4 (the SMALLEST of the two recorded boundaries)", got)
		}
		rep, rerr := audit.VerifyKeyRotationMarkersWith(ctx, sc.Audit(), pinned)
		if rerr != nil {
			return rerr
		}
		if rep.OK {
			t.Fatal("two different boundaries for the same retired key verified OK: a moved revocation boundary passed unreported")
		}
		if rep.Reason != audit.ReasonConflictingFence {
			t.Fatalf("report reason = %q, want %q", rep.Reason, audit.ReasonConflictingFence)
		}
		if rep.FirstBadSeq != 8 {
			t.Fatalf("first bad seq = %d, want 8 (the marker that introduced the second boundary)", rep.FirstBadSeq)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func headOf(t *testing.T, st store.Store, tenant model.TenantID) int64 {
	t.Helper()
	var seq int64
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		head, ok, err := sc.Audit().Head(context.Background())
		if err != nil || !ok {
			return err
		}
		seq = head.Seq
		return nil
	}); err != nil {
		t.Fatalf("head: %v", err)
	}
	return seq
}
