// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit_test

import (
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/store"
)

// TestCheckpointStatusThreeAnswers pins the three answers a checkpoint verdict
// really has. The boolean OK collapses two of them onto false — an unattested
// young ledger and a tampered one read identically — and every surface that
// rendered that boolean painted a healthy first-boot install red.
func TestCheckpointStatusThreeAnswers(t *testing.T) {
	cases := []struct {
		name string
		rep  audit.CheckpointReport
		want audit.CheckpointStatus
	}{
		{
			name: "verified",
			rep:  audit.CheckpointReport{Checkpoints: 3, OK: true, LatestAttestedSeq: 40},
			want: audit.CheckpointStatusOK,
		},
		{
			name: "nothing attested yet is NOT a failure",
			rep:  audit.CheckpointReport{Checkpoints: 0, OK: false, Reason: audit.ReasonNoCheckpoints},
			want: audit.CheckpointStatusPending,
		},
		{
			name: "forged signature stays loud",
			rep:  audit.CheckpointReport{Checkpoints: 2, OK: false, FirstBadSeq: 9, Reason: "checkpoint-sig-invalid"},
			want: audit.CheckpointStatusFailed,
		},
		{
			name: "broken link stays loud",
			rep:  audit.CheckpointReport{Checkpoints: 1, OK: false, FirstBadSeq: 5, Reason: "checkpoint-link-mismatch"},
			want: audit.CheckpointStatusFailed,
		},
		{
			// Deny-closed: a zero value never came from a completed walk. "I could not
			// look" must not be waved through as "there was nothing to look at".
			name: "zero value is failed, never pending",
			rep:  audit.CheckpointReport{},
			want: audit.CheckpointStatusFailed,
		},
		{
			// Also deny-closed: an empty count that does not carry the NAME of the
			// empty case is incoherent, so it gets the strict answer.
			name: "empty count without the named reason is failed",
			rep:  audit.CheckpointReport{Checkpoints: 0, OK: false, Reason: "checkpoint-sig-invalid"},
			want: audit.CheckpointStatusFailed,
		},
		{
			// A reason string that says "not yet" while checkpoints were counted is
			// incoherent too: the count is the thing that was actually measured.
			name: "named reason with a non-zero count is failed",
			rep:  audit.CheckpointReport{Checkpoints: 4, OK: false, Reason: audit.ReasonNoCheckpoints},
			want: audit.CheckpointStatusFailed,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.rep.Status(); got != c.want {
				t.Fatalf("Status() = %q, want %q (report %+v)", got, c.want, c.rep)
			}
		})
	}
}

// TestCheckpointStatusFromRealVerification proves the tri-state against reports the
// verifier actually produces, not hand-built structs: a virgin chain is pending, a
// checkpointed chain is ok, and the SAME chain read under a foreign key — the
// forged-checkpoint shape — is failed. The last assertion is the one that matters:
// it is what stops "pending is calm" from becoming "a bad checkpoint is calm".
func TestCheckpointStatusFromRealVerification(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st) // seeds 1 audit event (org.create)
	appendEvents(t, st, tenant, 3)

	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}

	verify := func(p ed25519.PublicKey) audit.CheckpointReport {
		t.Helper()
		var rep audit.CheckpointReport
		if verr := st.View(ctx, tenant, func(sc store.Scope) error {
			r, e := audit.VerifyCheckpoints(ctx, sc.Audit(), p)
			rep = r
			return e
		}); verr != nil {
			t.Fatalf("verify: %v", verr)
		}
		return rep
	}

	// 1. Nothing has been attested yet — the first-boot state.
	virgin := verify(pub)
	if got := virgin.Status(); got != audit.CheckpointStatusPending {
		t.Fatalf("virgin ledger Status() = %q, want %q (report %+v)", got, audit.CheckpointStatusPending, virgin)
	}
	if virgin.OK {
		t.Fatalf("virgin ledger must not report OK (attesting nothing is not a pass): %+v", virgin)
	}

	// 2. A real checkpoint under the real key verifies.
	if _, ok, cerr := signer.Checkpoint(ctx, st, tenant); cerr != nil || !ok {
		t.Fatalf("checkpoint = (%v, %v)", ok, cerr)
	}
	good := verify(pub)
	if got := good.Status(); got != audit.CheckpointStatusOK {
		t.Fatalf("checkpointed ledger Status() = %q, want %q (report %+v)", got, audit.CheckpointStatusOK, good)
	}

	// 3. The same checkpoint under a foreign key is a forgery — and stays loud.
	otherPub, _, _ := ed25519.GenerateKey(nil)
	forged := verify(otherPub)
	if got := forged.Status(); got != audit.CheckpointStatusFailed {
		t.Fatalf("forged checkpoint Status() = %q, want %q (report %+v)", got, audit.CheckpointStatusFailed, forged)
	}
	if forged.Reason != "checkpoint-sig-invalid" {
		t.Fatalf("forged checkpoint reason = %q, want checkpoint-sig-invalid", forged.Reason)
	}
}

// TestReasonNoCheckpointsIsTheWireString guards the constant against a rename that
// would silently turn every "pending" into "failed" (the consumers match on the
// exact string, and it is part of the JSON both verify endpoints return).
func TestReasonNoCheckpointsIsTheWireString(t *testing.T) {
	if audit.ReasonNoCheckpoints != "no-checkpoints" {
		t.Fatalf("ReasonNoCheckpoints = %q, want no-checkpoints", audit.ReasonNoCheckpoints)
	}
}
