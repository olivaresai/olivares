// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestRecordLocateVerifyKeyRotation is the marker end-to-end: a rotation
// records an off-box-signed audit.key.rotation boundary, LocateKeyFences derives
// the retired key's per-tenant last_seq, the fail-closed pass verifies it, and the
// ordinary per-event verifier SKIPS the marker (it is not a hot-path signature).
func TestRecordLocateVerifyKeyRotation(t *testing.T) {
	ctx := context.Background()
	// Build the store so its per-event signer IS the on-box (retiring) key, and
	// give the signer an off-box checkpoint key that seals the boundary marker.
	_, onBoxPriv, _ := ed25519.GenerateKey(nil)
	onBoxPub := onBoxPriv.Public().(ed25519.PublicKey)
	kms := newMockKMS(t)
	signer, err := audit.NewSigner(onBoxPriv, audit.WithCheckpointKey(kms))
	if err != nil {
		t.Fatal(err)
	}
	st := signedStore(t, signer)
	tenant := provisionTenant(t, st) // org.create at seq 1
	appendEvents(t, st, tenant, 3)   // seqs 2,3,4 -> tail = 4

	newPub, _, _ := ed25519.GenerateKey(nil)
	evidence := audit.KeyRotationEvidence{
		Tenant:           tenant.String(),
		PriorFingerprint: audit.KeyFingerprint(onBoxPub),
		PriorLastSeq:     4,
		NewFingerprint:   audit.KeyFingerprint(newPub),
		OffBoxKeyID:      kms.KeyID(),
	}
	var marker model.AuditEvent
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var rerr error
		marker, _, rerr = audit.RecordKeyRotation(ctx, sc.Audit(), signer, evidence)
		return rerr
	}); err != nil {
		t.Fatal(err)
	}
	if marker.Action != store.ActionAuditKeyRotation || len(marker.Sig) == 0 || marker.Seq != 5 {
		t.Fatalf("key rotation marker = %+v (want action=%s seq=5 signed)", marker, store.ActionAuditKeyRotation)
	}

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
		if got := fences[audit.KeyFingerprint(onBoxPub)]; got != 4 || len(fences) != 1 {
			t.Fatalf("located fences = %+v (want retired fp -> 4)", fences)
		}
		report, rerr := audit.VerifyKeyRotationMarkersWith(ctx, sc.Audit(), pinned)
		if rerr != nil || !report.OK || report.Markers != 1 || report.Valid != 1 {
			t.Fatalf("key rotation marker verification = %+v err=%v", report, rerr)
		}
		// nil verifier honors nothing.
		if fences, ferr := audit.LocateKeyFences(ctx, sc.Audit(), nil); ferr != nil || len(fences) != 0 {
			t.Fatalf("nil verifier returned fences: %+v err=%v", fences, ferr)
		}
		// A wrong pin locates nothing and fails the fail-closed pass.
		wrongPub, _, _ := ed25519.GenerateKey(nil)
		wrong := audit.NewCheckpointVerifier().AddEd25519(wrongPub)
		if fences, ferr := audit.LocateKeyFences(ctx, sc.Audit(), wrong); ferr != nil || len(fences) != 0 {
			t.Fatalf("wrong pin located fences: %+v err=%v", fences, ferr)
		}
		if rep, rerr := audit.VerifyKeyRotationMarkersWith(ctx, sc.Audit(), wrong); rerr != nil || rep.OK || rep.Reason != "keyrotation-sig-invalid" {
			t.Fatalf("wrong pin marker verification = %+v err=%v", rep, rerr)
		}
		// The ordinary per-event verifier skips the marker (action carries its own
		// off-box domain): events 1..4 verify under the on-box key; the marker at
		// seq 5 is neither counted nor failed.
		er, eerr := audit.VerifyEventsFenced(ctx, sc.Audit(), 1, []audit.FencedKey{{Key: onBoxPub}})
		if eerr != nil || !er.OK || er.Events != 4 || er.Signed != 4 {
			t.Fatalf("per-event verify over a chain with a rotation marker = %+v err=%v", er, eerr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRecordKeyRotationRefusesOnBoxSigner(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st)
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv) // no off-box checkpoint key
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, _, rerr := audit.RecordKeyRotation(ctx, sc.Audit(), signer, audit.KeyRotationEvidence{})
		return rerr
	})
	if err == nil || !strings.Contains(err.Error(), "off-box checkpoint signer") {
		t.Fatalf("RecordKeyRotation on-box signer error = %v, want off-box requirement", err)
	}
}

// TestKeyRotationMarkerRejectsUnsignedDraft proves a forged (unsigned) boundary
// naming the reserved action can never enter the immutable ledger.
func TestKeyRotationMarkerRejectsUnsignedDraft(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st)
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, aerr := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem,
			Action: store.ActionAuditKeyRotation, TargetKind: "core.audit_key_rotation",
		})
		return aerr
	})
	if !errors.Is(err, store.ErrReservedAuditAction) {
		t.Fatalf("unsigned key rotation draft error = %v, want ErrReservedAuditAction", err)
	}
}

// TestRecordKeyRotationRollsBackShiftedBoundary proves a boundary whose declared
// prior_last_seq does not match where the marker actually lands is rejected (and
// its INSERT rolled back), so a displaced, permanently-invalid fence never commits.
func TestRecordKeyRotationRollsBackShiftedBoundary(t *testing.T) {
	ctx := context.Background()
	_, onBoxPriv, _ := ed25519.GenerateKey(nil)
	onBoxPub := onBoxPriv.Public().(ed25519.PublicKey)
	kms := newMockKMS(t)
	signer, err := audit.NewSigner(onBoxPriv, audit.WithCheckpointKey(kms))
	if err != nil {
		t.Fatal(err)
	}
	st := signedStore(t, signer)
	tenant := provisionTenant(t, st) // seq 1
	appendEvents(t, st, tenant, 3)   // tail = 4
	newPub, _, _ := ed25519.GenerateKey(nil)

	err = st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, _, rerr := audit.RecordKeyRotation(ctx, sc.Audit(), signer, audit.KeyRotationEvidence{
			Tenant:           tenant.String(),
			PriorFingerprint: audit.KeyFingerprint(onBoxPub),
			PriorLastSeq:     9, // wrong: real tail is 4, marker would land at seq 5
			NewFingerprint:   audit.KeyFingerprint(newPub),
			OffBoxKeyID:      kms.KeyID(),
		})
		return rerr
	})
	if err == nil || !strings.Contains(err.Error(), "shifted the tail") {
		t.Fatalf("shifted boundary error = %v, want a rolled-back position mismatch", err)
	}
	// The rejected marker left no row: the tail is unchanged.
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		head, ok, herr := sc.Audit().Head(ctx)
		if herr != nil {
			return herr
		}
		if !ok || head.Seq != 4 {
			t.Fatalf("head after rejected rotation = seq %d (want 4)", head.Seq)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
