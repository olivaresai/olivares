// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"fmt"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestRecordAndLocateRecoveryRequiresPinnedOffBoxKey(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st)
	appendEvents(t, st, tenant, 2)

	_, onBoxPriv, _ := ed25519.GenerateKey(nil)
	kms := newMockKMS(t)
	signer, err := audit.NewSigner(onBoxPriv, audit.WithCheckpointKey(kms))
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, ok, err := signer.Checkpoint(ctx, st, tenant)
	if err != nil || !ok {
		t.Fatalf("checkpoint: ok=%v err=%v", ok, err)
	}
	appendEvents(t, st, tenant, 2)

	evidence := audit.RecoveryEvidence{
		Tenant: tenant.String(), BreakReason: "hash-mismatch",
		BreakAt: checkpoint.Seq + 2, ReanchorSeq: checkpoint.Seq - 1,
		OffBoxCheckpointSeq: checkpoint.Seq - 1, OffBoxKeyID: kms.KeyID(),
		QuarantinedFrom: checkpoint.Seq + 2, QuarantinedTo: checkpoint.Seq + 2,
		QuarantinedSHA256: strings.Repeat("ab", 32),
		Approvers:         []string{"user:alice", "user:bob"}, Reason: "operator incident", RequestedBy: "svc:recovery",
	}
	var marker model.AuditEvent
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var rerr error
		marker, rerr = audit.RecordRecovery(ctx, sc.Audit(), signer, evidence)
		return rerr
	}); err != nil {
		t.Fatal(err)
	}
	if marker.Action != store.ActionAuditRecover || len(marker.Sig) == 0 {
		t.Fatalf("recovery marker = %+v", marker)
	}

	der, _ := kms.PublicKey(ctx)
	pinned := audit.NewCheckpointVerifier()
	if err := pinned.AddPublicKey(kms.Algorithm(), der); err != nil {
		t.Fatal(err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		found, seq, got, lerr := audit.LocateRecoveryEvidence(ctx, sc.Audit(), pinned)
		if lerr != nil {
			return lerr
		}
		if !found || seq != marker.Seq || got.QuarantinedSHA256 != evidence.QuarantinedSHA256 || len(got.Approvers) != 2 {
			t.Fatalf("located = found=%v seq=%d evidence=%+v", found, seq, got)
		}
		report, rerr := audit.VerifyRecoveryMarkersWith(ctx, sc.Audit(), pinned)
		if rerr != nil || !report.OK || report.Markers != 1 || report.Valid != 1 {
			t.Fatalf("recovery marker verification = %+v err=%v", report, rerr)
		}
		found, _, _, _, lerr = audit.LocateRecovery(ctx, sc.Audit(), nil)
		if lerr != nil || found {
			t.Fatalf("nil verifier honored marker: found=%v err=%v", found, lerr)
		}
		wrongPub, _, _ := ed25519.GenerateKey(nil)
		found, _, _, _, lerr = audit.LocateRecovery(ctx, sc.Audit(), audit.NewCheckpointVerifier().AddEd25519(wrongPub))
		if lerr != nil || found {
			t.Fatalf("wrong pin honored marker: found=%v err=%v", found, lerr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRecordRecoveryRefusesOnBoxSigner(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st)
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, rerr := audit.RecordRecovery(ctx, sc.Audit(), signer, audit.RecoveryEvidence{})
		return rerr
	})
	if err == nil || !strings.Contains(err.Error(), "off-box checkpoint signer") {
		t.Fatalf("RecordRecovery on-box signer error = %v, want off-box signer requirement", err)
	}
}

func TestRecordRecoveryRollsBackShiftedMarker(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st)
	appendEvents(t, st, tenant, 3)

	var before store.HeadRef
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		var ok bool
		var herr error
		before, ok, herr = sc.Audit().Head(ctx)
		if herr == nil && !ok {
			herr = fmt.Errorf("test ledger has no head")
		}
		return herr
	}); err != nil {
		t.Fatal(err)
	}

	_, onBoxPriv, _ := ed25519.GenerateKey(nil)
	kms := newMockKMS(t)
	signer, err := audit.NewSigner(onBoxPriv, audit.WithCheckpointKey(kms))
	if err != nil {
		t.Fatal(err)
	}
	evidence := audit.RecoveryEvidence{
		Tenant: tenant.String(), BreakReason: "hash-mismatch", BreakAt: 2,
		ReanchorSeq: 1, OffBoxCheckpointSeq: 1, OffBoxKeyID: kms.KeyID(),
		QuarantinedFrom: 2,
		// Simulate evidence captured at the previous tail: Append will land at
		// before.Seq+1, so its signed QuarantinedTo must be before.Seq, not -1.
		QuarantinedTo: before.Seq - 1, QuarantinedSHA256: strings.Repeat("ab", 32),
		Approvers: []string{"user:alice", "user:bob"}, Reason: "race regression", RequestedBy: "svc:test",
	}
	err = st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, rerr := audit.RecordRecovery(ctx, sc.Audit(), signer, evidence)
		return rerr
	})
	if err == nil || !strings.Contains(err.Error(), "concurrent append shifted the tail") {
		t.Fatalf("shifted recovery error = %v, want concurrent-tail displacement", err)
	}

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		markers := 0
		if werr := sc.Audit().Walk(ctx, 1, func(ev model.AuditEvent) error {
			if ev.Action == store.ActionAuditRecover {
				markers++
			}
			return nil
		}); werr != nil {
			return werr
		}
		after, ok, herr := sc.Audit().Head(ctx)
		if herr != nil {
			return herr
		}
		if markers != 0 || !ok || after.Seq != before.Seq || !bytes.Equal(after.Hash, before.Hash) {
			t.Fatalf("rollback left markers=%d head=%+v; want no marker and head=%+v", markers, after, before)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
