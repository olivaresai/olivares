// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr_test

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/store"
)

// --- raw-DB tamper helpers (a DB-level attacker on the restored snapshot) ------

func rawExec(t *testing.T, dbPath string, stmts ...string) {
	t.Helper()
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer func() { _ = raw.Close() }()
	for _, s := range stmts {
		if _, err := raw.Exec(s); err != nil {
			t.Fatalf("exec %q: %v", s, err)
		}
	}
}

// openExtracted is the common restore-side setup: extract, verify digest, decrypt
// the audit key, and return the restored db path + manifest + the audit priv key.
func openExtracted(t *testing.T, b builtBundle) (dbPath string, m *dr.Manifest, priv ed25519.PrivateKey) {
	t.Helper()
	dir, mani, kek := extractBundle(t, b)
	cipher, err := dr.OpenCipher([]byte(testPass), kek)
	if err != nil {
		t.Fatalf("open cipher: %v", err)
	}
	blob, err := os.ReadFile(filepath.Join(dir, mani.Keys[0].File))
	if err != nil {
		t.Fatalf("read sealed key: %v", err)
	}
	keyBytes, err := cipher.Open(blob)
	if err != nil {
		t.Fatalf("decrypt key: %v", err)
	}
	return filepath.Join(dir, mani.Store.File), mani, decodeKeyFile(t, keyBytes)
}

func verifyRestored(t *testing.T, dbPath string, m *dr.Manifest, priv ed25519.PrivateKey) *dr.RestoreReport {
	t.Helper()
	rest := openRestored(t, dbPath, priv)
	rep, err := dr.RestoreVerify(context.Background(), rest.st, m, rest.pub(), rest.cpVerifier(t))
	if err != nil {
		t.Fatalf("restore verify: %v", err)
	}
	return rep
}

// TestRestoreMissingTenantDetectedAdvisory is the fix for the advisory-mode hole:
// a tenant present in the manifest with events but ENTIRELY absent from the
// restored store is a lost chain and must fail RestoreVerify even though an empty
// chain verifies vacuously and advisory mode tolerates a tip delta.
func TestRestoreMissingTenantDetectedAdvisory(t *testing.T) {
	src := newEstate(t)
	ta := src.newTenant(t)
	tb := src.newTenant(t)
	src.appendN(t, ta, 3)
	src.appendN(t, tb, 3)
	b := makeBundle(t, src)

	dbPath, m, priv := openExtracted(t, b)
	m.TipMatch = dr.TipAdvisory // the Postgres-style mode where the hole lived

	// Wipe tenant tb entirely from the restored store (drop the append-only delete
	// guard first, then remove its events AND its head row).
	rawExec(t, dbPath,
		"DROP TRIGGER audit_events_no_delete",
		"DELETE FROM audit_events WHERE tenant_id = '"+tb.String()+"'",
		"DELETE FROM audit_heads WHERE tenant_id = '"+tb.String()+"'",
	)

	rep := verifyRestored(t, dbPath, m, priv)
	if rep.OK {
		t.Fatalf("advisory restore wrongly OK with tenant %s fully missing", tb)
	}
	if !anyContains(rep.Problems, "MISSING") {
		t.Fatalf("expected a MISSING-chain problem, got %v", rep.Problems)
	}
	for _, tv := range rep.Tenants {
		if tv.Tenant == tb.String() && !tv.Missing {
			t.Fatalf("tenant %s not flagged Missing: %+v", tb, tv)
		}
	}
}

// TestRestoreDetectsTailTruncation: deleting the most-recent events while leaving
// audit_heads pointing past them (the classic silent-truncation attack) must be
// caught after restore — the headline DR-safety invariant.
func TestRestoreDetectsTailTruncation(t *testing.T) {
	src := newEstate(t)
	tn := src.newTenant(t)
	src.appendN(t, tn, 5) // seqs 2..6 (1 = org genesis)
	b := makeBundle(t, src)

	dbPath, m, priv := openExtracted(t, b)
	// Drop the tail events but leave audit_heads at the old tip (no seq gap remains).
	rawExec(t, dbPath,
		"DROP TRIGGER audit_events_no_delete",
		"DELETE FROM audit_events WHERE tenant_id = '"+tn.String()+"' AND seq > 4",
	)

	rep := verifyRestored(t, dbPath, m, priv)
	if rep.OK {
		t.Fatalf("tail truncation not detected")
	}
	var found bool
	for _, tv := range rep.Tenants {
		if tv.Tenant == tn.String() {
			found = true
			if tv.ChainOK || tv.ChainReason != "tail-truncated" {
				t.Fatalf("expected tail-truncated, got chainOK=%v reason=%q", tv.ChainOK, tv.ChainReason)
			}
		}
	}
	if !found {
		t.Fatalf("tenant not in report")
	}
}

// TestRestoreDetectsHeadHashMismatch: tampering only the audit_heads hash (same
// seq) is caught as head-mismatch after restore.
func TestRestoreDetectsHeadHashMismatch(t *testing.T) {
	src := newEstate(t)
	tn := src.newTenant(t)
	src.appendN(t, tn, 3)
	b := makeBundle(t, src)

	dbPath, m, priv := openExtracted(t, b)
	// audit_heads has no immutability trigger; its scope trigger allows the write
	// when no tenant pin is set (the System path).
	rawExec(t, dbPath, "UPDATE audit_heads SET hash = randomblob(32) WHERE tenant_id = '"+tn.String()+"'")

	rep := verifyRestored(t, dbPath, m, priv)
	if rep.OK {
		t.Fatalf("head-hash mismatch not detected")
	}
	for _, tv := range rep.Tenants {
		if tv.Tenant == tn.String() && tv.ChainReason != "head-mismatch" {
			t.Fatalf("expected head-mismatch, got %q", tv.ChainReason)
		}
	}
}

// TestRestoreDetectsOneFailingTenant: a corrupt chain in ONE tenant fails the
// restore while the other tenants are still reported healthy.
func TestRestoreDetectsOneFailingTenant(t *testing.T) {
	src := newEstate(t)
	ta := src.newTenant(t)
	tb := src.newTenant(t)
	src.appendN(t, ta, 4)
	src.appendN(t, tb, 4)
	b := makeBundle(t, src)

	dbPath, m, priv := openExtracted(t, b)
	rawExec(t, dbPath,
		"DROP TRIGGER audit_events_no_update",
		"UPDATE audit_events SET action = 'tampered' WHERE tenant_id = '"+ta.String()+"' AND seq = 2",
	)

	rep := verifyRestored(t, dbPath, m, priv)
	if rep.OK {
		t.Fatalf("corrupt tenant not detected")
	}
	var okTB, badTA bool
	for _, tv := range rep.Tenants {
		switch tv.Tenant {
		case ta.String():
			badTA = !tv.ChainOK
		case tb.String():
			okTB = tv.ChainOK && tv.EventsOK
		}
	}
	if !badTA {
		t.Fatalf("tenant ta should have failed chain verification")
	}
	if !okTB {
		t.Fatalf("tenant tb should still be healthy")
	}
}

// TestRestoreDetectsCheckpointSigTamper: corrupting a checkpoint event's signature
// is caught by the checkpoint verifier after restore (per-event verification skips
// checkpoint events, so this exercises the distinct VerifyCheckpoints path).
func TestRestoreDetectsCheckpointSigTamper(t *testing.T) {
	src := newEstate(t)
	tn := src.newTenant(t)
	src.appendN(t, tn, 3)
	src.checkpointAll(t)
	b := makeBundle(t, src)

	dbPath, m, priv := openExtracted(t, b)
	rawExec(t, dbPath,
		"DROP TRIGGER audit_events_no_update",
		"UPDATE audit_events SET sig = randomblob(64) WHERE tenant_id = '"+tn.String()+"' AND action = 'audit.checkpoint'",
	)

	rep := verifyRestored(t, dbPath, m, priv)
	if rep.OK {
		t.Fatalf("checkpoint signature tamper not detected")
	}
	if !anyContains(rep.Problems, "checkpoints") {
		t.Fatalf("expected a checkpoints problem, got %v", rep.Problems)
	}
}

// --- off-box (KMS/HSM) checkpoint path --------------------------------------

// fakeOffBox is an in-test CheckpointKey backed by an Ed25519 key — it models a
// KMS/HSM signer for the checkpoints without any cloud dependency.
type fakeOffBox struct{ priv ed25519.PrivateKey }

func (f fakeOffBox) SignCheckpoint(_ context.Context, preimage []byte) ([]byte, error) {
	return ed25519.Sign(f.priv, preimage), nil
}
func (f fakeOffBox) Algorithm() audit.SigAlg { return audit.AlgEd25519 }
func (f fakeOffBox) KeyID() string           { return "test-offbox-kms" }
func (f fakeOffBox) PublicKey(_ context.Context) ([]byte, error) {
	return f.priv.Public().(ed25519.PublicKey), nil
}

// TestRestoreWithOffBoxCheckpoints exercises a ledger whose CHECKPOINTS were signed
// off-box (KMS/HSM) — per-event stays on-box. Backup builds the manifest with the
// off-box verifier and restore re-verifies the off-box checkpoints, mirroring a
// deployment with OLIVARES_LEDGER_SIGNER configured on both ends.
func TestRestoreWithOffBoxCheckpoints(t *testing.T) {
	ctx := context.Background()
	_, auditPriv := genKey(t)
	_, cpPriv := genKey(t)
	off := fakeOffBox{priv: cpPriv}

	signer, err := audit.NewSigner(auditPriv, audit.WithCheckpointKey(off))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	if !signer.OffBoxCheckpoints() {
		t.Fatal("expected off-box checkpoints")
	}
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "olivares.db")
	st, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dbPath, Debug: true, SignEvent: signer.SignEvent}, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close() }()
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, e := sys.EnsureSystemTenant(ctx)
		return e
	}); err != nil {
		t.Fatalf("system tenant: %v", err)
	}
	e := &estate{st: st, signer: signer, priv: auditPriv, dir: dir, dbPath: dbPath, keyB64: keyFileBytes(auditPriv)}
	tn := e.newTenant(t)
	e.appendN(t, tn, 3)
	e.checkpointAll(t) // checkpoints signed OFF-BOX

	b := makeBundle(t, e)

	// Restore side: same off-box key configured (as a real deployment would).
	dbPath2, m, _ := openExtracted(t, b)
	restSigner, err := audit.NewSigner(auditPriv, audit.WithCheckpointKey(off))
	if err != nil {
		t.Fatal(err)
	}
	st2, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dbPath2, Debug: true, SignEvent: restSigner.SignEvent}, nil)
	if err != nil {
		t.Fatalf("open restored: %v", err)
	}
	defer func() { _ = st2.Close() }()
	cpv, err := restSigner.CheckpointVerifier(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := dr.RestoreVerify(ctx, st2, m, restSigner.PublicKey(), cpv)
	if err != nil {
		t.Fatalf("restore verify: %v", err)
	}
	if !rep.OK {
		t.Fatalf("off-box-checkpoint restore not green: %v", rep.Problems)
	}
	var sawCheckpoint bool
	for _, tv := range rep.Tenants {
		if tv.Tenant == tn.String() {
			sawCheckpoint = tv.CheckpointsOK
		}
	}
	if !sawCheckpoint {
		t.Fatalf("off-box checkpoint not verified for tenant %s", tn)
	}
}
