// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit_test

import (
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// buildRotatedArchive exports a WORM archive of a ROTATED chain for one tenant:
// generation 1 (pub1) signs seqs 1-2, an off-box-signed audit.key.rotation marker
// lands at seq 3 (prior_last_seq=2), and generation 2 (pub2, the current key) signs
// seqs 4-5. When forgeSeq4 is true the CURRENT-epoch event at seq 4 is (maliciously)
// signed by the RETIRED key gen1 instead of gen2 — the archived form of F-07
// (retired key + DB write). The signature is excluded from the chain hash, so the
// legit and forged archives are byte-identical except for seq 4's sig and both
// export cleanly; only the per-event signature verifier can tell them apart.
func buildRotatedArchive(t *testing.T, kms *mockKMS, gen1, gen2 *audit.Signer, pub1, pub2 ed25519.PublicKey, forgeSeq4 bool) string {
	t.Helper()
	ctx := context.Background()

	// The store's per-event signer is switchable: gen1 while the first epoch is
	// written, gen2 after the boundary — the exact "keys rotate" ceremony.
	active := gen1
	signEvent := func(tenant string, seq int64, hash []byte) []byte {
		return active.SignEvent(tenant, seq, hash)
	}
	st, err := sqlstore.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", Debug: true, SignEvent: signEvent,
	}, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	tenant := provisionTenant(t, st) // seq 1 (org.create), signed by gen1
	appendEvents(t, st, tenant, 1)   // seq 2, gen1 -> gen1 owns epoch (0, 2]

	// Off-box-signed boundary at prior_last_seq=2: the marker lands at seq 3.
	evidence := audit.KeyRotationEvidence{
		Tenant:           tenant.String(),
		PriorFingerprint: audit.KeyFingerprint(pub1),
		PriorLastSeq:     2,
		NewFingerprint:   audit.KeyFingerprint(pub2),
		OffBoxKeyID:      kms.KeyID(),
	}
	var marker model.AuditEvent
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var rerr error
		marker, _, rerr = audit.RecordKeyRotation(ctx, sc.Audit(), gen1, evidence)
		return rerr
	}); err != nil {
		t.Fatalf("record key rotation: %v", err)
	}
	if marker.Seq != 3 || marker.Action != store.ActionAuditKeyRotation {
		t.Fatalf("rotation marker = seq %d action %s (want seq 3 key-rotation)", marker.Seq, marker.Action)
	}

	// Current epoch. seq 4 is the F-07 target: legit -> gen2 (current); forged ->
	// gen1 (retired) re-signs a current-epoch event.
	if forgeSeq4 {
		active = gen1
	} else {
		active = gen2
	}
	appendEvents(t, st, tenant, 1) // seq 4
	active = gen2
	appendEvents(t, st, tenant, 1) // seq 5 (always current key)

	dir := t.TempDir()
	sink, err := audit.NewDirSink(dir)
	if err != nil {
		t.Fatalf("dir sink: %v", err)
	}
	if _, err := audit.ExportSegments(ctx, st, tenant, sink, audit.ExportOptions{SegmentEvents: 100}, nil); err != nil {
		t.Fatalf("export segments: %v", err)
	}
	return dir
}

// TestArchiveVerifyFencedClosesF07 is the E4 repro on the ARCHIVE path (the
// sibling of TestVerifyEventsFencedClosesF07 for the live path). A rotated chain is
// exported to a WORM archive; the retired key gen1 re-signs a current-epoch event
// (seq 4). The FLAT candidate set (pre-fix archive behavior) ACCEPTS the forgery —
// left as a contrast witness, exactly as the live repro does — while the epoch-
// FENCED set {gen1@2, gen2} REJECTS it at seq 4 (event-sig-invalid) and still
// verifies the legit archive end-to-end.
func TestArchiveVerifyFencedClosesF07(t *testing.T) {
	ctx := context.Background()

	pub1, priv1, _ := ed25519.GenerateKey(nil)
	pub2, priv2, _ := ed25519.GenerateKey(nil)
	kms := newMockKMS(t)
	gen1, err := audit.NewSigner(priv1, audit.WithCheckpointKey(kms)) // holds the off-box key that seals the boundary
	if err != nil {
		t.Fatal(err)
	}
	gen2, err := audit.NewSigner(priv2)
	if err != nil {
		t.Fatal(err)
	}

	// The off-box checkpoint key that validates the boundary marker (mirrors the
	// operator pinning --pubkey <off-box>). Without it a fenced verify would (deny-
	// closed) fail the marker line as keyrotation-unverifiable before reaching seq 4.
	der, err := kms.PublicKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cpv := audit.NewCheckpointVerifier()
	if err := cpv.AddPublicKey(kms.Algorithm(), der); err != nil {
		t.Fatal(err)
	}

	fenced := []audit.FencedKey{{Key: pub1, LastSeq: 2}, {Key: pub2}} // gen2 = current (unbounded)
	flat := []audit.FencedKey{{Key: pub1}, {Key: pub2}}               // pre-fix: every key trusted for every seq

	legit := buildRotatedArchive(t, kms, gen1, gen2, pub1, pub2, false)
	forged := buildRotatedArchive(t, kms, gen1, gen2, pub1, pub2, true)

	// VERDE: the fenced verifier rejects the retired-key forgery at seq 4.
	rep, err := audit.VerifyArchiveDir(ctx, forged, audit.ArchiveVerifyOptions{EventKeys: fenced, Checkpoints: cpv})
	if err != nil {
		t.Fatalf("fenced verify (forged): %v", err)
	}
	if rep.OK || rep.Reason != "event-sig-invalid" || rep.BreakAt != 4 {
		t.Fatalf("fenced verifier accepted the archived retired-key forgery: %+v (want event-sig-invalid at seq 4)", rep)
	}

	// VERDE: the fenced verifier still accepts the legitimate rotated archive
	// end-to-end (gen1's in-range seqs 1-2 and gen2's seqs 4-5, plus the marker).
	rep, err = audit.VerifyArchiveDir(ctx, legit, audit.ArchiveVerifyOptions{EventKeys: fenced, Checkpoints: cpv})
	if err != nil {
		t.Fatalf("fenced verify (legit): %v", err)
	}
	if !rep.OK || rep.Events != 5 {
		t.Fatalf("fenced verifier rejected a legitimate rotated archive: %+v (want OK, Events=5)", rep)
	}

	// ROJO-CONTRASTE: the pre FLAT set is exactly F-07 in the archive — with
	// every candidate trusted for every sequence it (insecurely) ACCEPTS the same
	// forgery. This witnesses why the fence is load-bearing; the fenced check is the fix.
	rep, err = audit.VerifyArchiveDir(ctx, forged, audit.ArchiveVerifyOptions{EventKeys: flat, Checkpoints: cpv})
	if err != nil {
		t.Fatalf("flat verify (forged): %v", err)
	}
	if !rep.OK {
		t.Fatalf("expected the FLAT archive verifier to accept the forgery (documenting F-07): %+v", rep)
	}
}
