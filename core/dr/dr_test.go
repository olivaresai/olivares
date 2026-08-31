// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const testPass = "correct horse battery staple"

// builtBundle is the on-disk result of a backup, plus the source key needed to
// reconstruct the restored signer in tests.
type builtBundle struct {
	path     string
	manifest *dr.Manifest
	keyPriv  ed25519.PrivateKey
}

// makeBundle performs the full backup path against a live estate: snapshot,
// build the manifest from that snapshot (TipExact), seal the audit key under a
// passphrase, and write the gzip-tar bundle.
func makeBundle(t *testing.T, e *estate) builtBundle {
	t.Helper()
	ctx := context.Background()
	work := t.TempDir()
	snap := e.snapshotInto(t, filepath.Join(work, "olivares.db"))

	sum, size, err := dr.FileSHA256(snap)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}

	cipher, err := dr.NewPassphraseCipher([]byte(testPass))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	sealed, err := cipher.Seal(e.keyB64)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	keyRef := dr.KeyRef{
		File: "keys/audit-signing.key.enc", Name: "audit-signing.key", Role: dr.RoleAudit,
		PubSHA256: dr.PubFingerprint(e.pub()),
	}

	m, err := dr.BuildManifest(ctx, e.st, e.pub(), e.cpVerifier(t), dr.BuildOptions{
		EngineKind: string(store.EngineSQLite),
		Version:    "test",
		Store:      dr.StoreSnapshot{Method: dr.MethodVacuumInto, File: "store/olivares.db", SizeBytes: size, SHA256: sum},
		Keys:       []dr.KeyRef{keyRef},
		TipMatch:   dr.TipExact,
		Now:        time.Unix(1_700_000_000, 0),
	})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}

	bundlePath := filepath.Join(t.TempDir(), "estate.drbundle")
	f, err := os.Create(bundlePath)
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	if err := dr.WriteBundle(f, dr.BundleInput{
		Manifest:     m,
		KEK:          cipher.Params(),
		SnapshotPath: snap,
		SealedKeys:   map[string][]byte{keyRef.File: sealed},
	}); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close bundle: %v", err)
	}
	return builtBundle{path: bundlePath, manifest: m, keyPriv: e.priv}
}

// extractBundle opens a bundle into a fresh dir and checks the snapshot digest
// against the manifest (the bundle-integrity gate every restore runs first).
func extractBundle(t *testing.T, b builtBundle) (dir string, m *dr.Manifest, kek dr.KDFParams) {
	t.Helper()
	dir = t.TempDir()
	f, err := os.Open(b.path)
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	defer func() { _ = f.Close() }()
	m, kek, err = dr.ExtractBundle(f, dir)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	snap := filepath.Join(dir, m.Store.File)
	sum, _, err := dr.FileSHA256(snap)
	if err != nil {
		t.Fatalf("digest restored snapshot: %v", err)
	}
	if sum != m.Store.SHA256 {
		t.Fatalf("snapshot digest mismatch: extracted %s, manifest %s", sum, m.Store.SHA256)
	}
	return dir, m, kek
}

// decodeKeyFile turns the data-dir key file bytes back into a private key.
func decodeKeyFile(t *testing.T, b []byte) ed25519.PrivateKey {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		t.Fatalf("decode key file: %v", err)
	}
	return ed25519.PrivateKey(raw)
}

// TestBackupRestoreRoundTripPreservesChain is the make-or-break: a backup taken
// from a live, signed, checkpointed ledger restores to a fresh store whose chain,
// per-event signatures, checkpoints, tips and key fingerprint all verify GREEN.
func TestBackupRestoreRoundTripPreservesChain(t *testing.T) {
	ctx := context.Background()
	src := newEstate(t)
	ta := src.newTenant(t)
	tb := src.newTenant(t)
	src.appendN(t, ta, 5)
	src.appendN(t, tb, 3)
	src.checkpointAll(t)
	src.appendN(t, ta, 2) // events after the checkpoint (between-checkpoint tail)

	b := makeBundle(t, src)

	// Restore into a fresh data dir: extract, verify digest, decrypt the key, open.
	dir, m, kek := extractBundle(t, b)
	cipher, err := dr.OpenCipher([]byte(testPass), kek)
	if err != nil {
		t.Fatalf("open cipher: %v", err)
	}
	encBlob, err := os.ReadFile(filepath.Join(dir, m.Keys[0].File))
	if err != nil {
		t.Fatalf("read sealed key: %v", err)
	}
	keyBytes, err := cipher.Open(encBlob)
	if err != nil {
		t.Fatalf("decrypt key: %v", err)
	}
	restoredPriv := decodeKeyFile(t, keyBytes)

	rest := openRestored(t, filepath.Join(dir, m.Store.File), restoredPriv)
	rep, err := dr.RestoreVerify(ctx, rest.st, m, rest.pub(), rest.cpVerifier(t))
	if err != nil {
		t.Fatalf("restore verify: %v", err)
	}
	if !rep.OK {
		t.Fatalf("restore not OK: %+v", rep.Problems)
	}
	if !rep.Key.Match {
		t.Fatalf("key fingerprint not matched: %+v", rep.Key)
	}
	if len(rep.Tenants) < 3 { // ta, tb, system
		t.Fatalf("expected >=3 tenant chains, got %d", len(rep.Tenants))
	}
	for _, tv := range rep.Tenants {
		if !tv.ChainOK || !tv.EventsOK || !tv.CheckpointsOK || !tv.TipOK {
			t.Fatalf("tenant %s not green: %+v", tv.Tenant, tv)
		}
		if tv.EventsTotal == 0 || tv.EventsSigned != tv.EventsTotal {
			t.Fatalf("tenant %s events signed %d/%d", tv.Tenant, tv.EventsSigned, tv.EventsTotal)
		}
	}
}

// TestRestoreWithoutKeyIsDetected: the #1 DR hazard. Restore the store but lose
// the signing key (boot mints a fresh one). The chain looks internally fine, but
// per-event signatures fail against the new key AND the fingerprint mismatches —
// the restore is loudly NOT safe, never silently accepted.
func TestRestoreWithoutKeyIsDetected(t *testing.T) {
	ctx := context.Background()
	src := newEstate(t)
	tn := src.newTenant(t)
	src.appendN(t, tn, 4)
	src.checkpointAll(t)
	b := makeBundle(t, src)

	dir, m, _ := extractBundle(t, b)
	_, freshPriv := genKey(t) // operator forgot the key → boot makes a new one
	rest := openRestored(t, filepath.Join(dir, m.Store.File), freshPriv)

	rep, err := dr.RestoreVerify(ctx, rest.st, m, rest.pub(), rest.cpVerifier(t))
	if err != nil {
		t.Fatalf("restore verify: %v", err)
	}
	if rep.OK {
		t.Fatalf("restore wrongly reported OK with a missing/fresh key")
	}
	if rep.Key.Match {
		t.Fatalf("fresh key wrongly matched the manifest fingerprint")
	}
	if !anyContains(rep.Problems, "key") {
		t.Fatalf("expected a key problem, got %v", rep.Problems)
	}
	if !anyContains(rep.Problems, "per-event signatures") {
		t.Fatalf("expected per-event signature failures, got %v", rep.Problems)
	}
	// The chain itself is internally consistent (only the SIGNATURES are wrong).
	for _, tv := range rep.Tenants {
		if !tv.ChainOK {
			t.Fatalf("tenant %s chain unexpectedly broken: %s", tv.Tenant, tv.ChainReason)
		}
		if tv.EventsOK {
			t.Fatalf("tenant %s per-event signatures unexpectedly OK with a wrong key", tv.Tenant)
		}
	}
}

// TestPostRestoreAppendContinuesChain: after a verified restore, new appends
// chain from the restored tip and the whole chain stays green — continuity is
// preserved ACROSS the restore boundary, not just up to it.
func TestPostRestoreAppendContinuesChain(t *testing.T) {
	ctx := context.Background()
	src := newEstate(t)
	tn := src.newTenant(t)
	src.appendN(t, tn, 3)
	b := makeBundle(t, src)

	dir, m, kek := extractBundle(t, b)
	cipher, _ := dr.OpenCipher([]byte(testPass), kek)
	encBlob, _ := os.ReadFile(filepath.Join(dir, m.Keys[0].File))
	keyBytes, err := cipher.Open(encBlob)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	rest := openRestored(t, filepath.Join(dir, m.Store.File), decodeKeyFile(t, keyBytes))

	// Resume governance: append more events with the restored signer.
	rest.appendN(t, tn, 4)
	if err := rest.st.View(ctx, tn, func(sc store.Scope) error {
		chain, err := sc.Audit().Verify(ctx, 1)
		if err != nil {
			return err
		}
		if !chain.OK {
			t.Fatalf("post-restore chain broken at seq %d: %s", chain.BreakAt, chain.Reason)
		}
		ev, err := audit.VerifyEvents(ctx, sc.Audit(), rest.pub())
		if err != nil {
			return err
		}
		if !ev.OK || ev.Signed != ev.Events {
			t.Fatalf("post-restore per-event signatures not all valid: %+v", ev)
		}
		head, _, _ := sc.Audit().Head(ctx)
		if head.Seq <= manifestTip(m, tn.String()) {
			t.Fatalf("post-restore tip %d did not advance past backup tip %d", head.Seq, manifestTip(m, tn.String()))
		}
		return nil
	}); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

// TestRPOTailLossIsCleanNotBroken: events appended AFTER the backup are lost on
// restore (the RPO window), but the restored chain verifies GREEN at the backup
// tip — the loss is clean (a shorter intact chain), never a broken one.
func TestRPOTailLossIsCleanNotBroken(t *testing.T) {
	ctx := context.Background()
	src := newEstate(t)
	tn := src.newTenant(t)
	src.appendN(t, tn, 3)
	b := makeBundle(t, src) // backup point A

	// More events happen after the backup (these are the RPO loss on a disaster).
	src.appendN(t, tn, 5) // point B

	dir, m, kek := extractBundle(t, b)
	cipher, _ := dr.OpenCipher([]byte(testPass), kek)
	encBlob, _ := os.ReadFile(filepath.Join(dir, m.Keys[0].File))
	keyBytes, _ := cipher.Open(encBlob)
	rest := openRestored(t, filepath.Join(dir, m.Store.File), decodeKeyFile(t, keyBytes))

	rep, err := dr.RestoreVerify(ctx, rest.st, m, rest.pub(), rest.cpVerifier(t))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !rep.OK {
		t.Fatalf("restore-to-backup-point not green: %v", rep.Problems)
	}
	// The post-backup events are simply absent; the chain is whole at point A.
	if got := manifestTip(m, tn.String()); restoredTip(t, rest, tn) != got {
		t.Fatalf("restored tip %d != backup tip %d (clean RPO loss expected)", restoredTip(t, rest, tn), got)
	}
}

// TestRestoredLedgerStaysTamperEvident: a restore does not weaken tamper-
// evidence. Corrupting an event in the restored store is caught by RestoreVerify
// exactly as it would be in the live ledger.
func TestRestoredLedgerStaysTamperEvident(t *testing.T) {
	ctx := context.Background()
	src := newEstate(t)
	tn := src.newTenant(t)
	src.appendN(t, tn, 4)
	b := makeBundle(t, src)

	dir, m, kek := extractBundle(t, b)
	cipher, _ := dr.OpenCipher([]byte(testPass), kek)
	encBlob, _ := os.ReadFile(filepath.Join(dir, m.Keys[0].File))
	keyBytes, _ := cipher.Open(encBlob)
	restoredDB := filepath.Join(dir, m.Store.File)

	// Tamper a row directly in the restored snapshot (a DB-level attacker).
	tamperEvent(t, restoredDB, tn)

	rest := openRestored(t, restoredDB, decodeKeyFile(t, keyBytes))
	rep, err := dr.RestoreVerify(ctx, rest.st, m, rest.pub(), rest.cpVerifier(t))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.OK {
		t.Fatalf("tampered restored ledger wrongly reported OK")
	}
	if !anyContains(rep.Problems, "chain") && !anyContains(rep.Problems, "per-event") {
		t.Fatalf("expected a chain/per-event problem after tamper, got %v", rep.Problems)
	}
}

// TestManifestTipBumpDetectedExact: in TipExact mode, a manifest that claims a
// higher tip than the restored store holds (a truncated/incomplete restore) is a
// hard failure.
func TestManifestTipBumpDetectedExact(t *testing.T) {
	ctx := context.Background()
	src := newEstate(t)
	tn := src.newTenant(t)
	src.appendN(t, tn, 3)
	b := makeBundle(t, src)

	dir, m, kek := extractBundle(t, b)
	cipher, _ := dr.OpenCipher([]byte(testPass), kek)
	encBlob, _ := os.ReadFile(filepath.Join(dir, m.Keys[0].File))
	keyBytes, _ := cipher.Open(encBlob)
	rest := openRestored(t, filepath.Join(dir, m.Store.File), decodeKeyFile(t, keyBytes))

	// Pretend the backup expected one more event than the snapshot holds.
	for i := range m.Tenants {
		if m.Tenants[i].Tenant == tn.String() {
			m.Tenants[i].HeadSeq++
		}
	}
	rep, err := dr.RestoreVerify(ctx, rest.st, m, rest.pub(), rest.cpVerifier(t))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.OK {
		t.Fatalf("tip bump not detected in exact mode")
	}
	if !anyContains(rep.Problems, "tip") {
		t.Fatalf("expected a tip problem, got %v", rep.Problems)
	}
}

// TestAdvisoryTipBehindIsNotFailure: in TipAdvisory mode (Postgres online
// backup), a restored tip behind the manifest is the RPO window, not a failure.
func TestAdvisoryTipBehindIsNotFailure(t *testing.T) {
	ctx := context.Background()
	src := newEstate(t)
	tn := src.newTenant(t)
	src.appendN(t, tn, 3)
	b := makeBundle(t, src)

	dir, m, kek := extractBundle(t, b)
	m.TipMatch = dr.TipAdvisory // simulate a live-store-built manifest
	for i := range m.Tenants {
		if m.Tenants[i].Tenant == tn.String() {
			m.Tenants[i].HeadSeq += 7 // live read was ahead of the snapshot
			m.Tenants[i].HeadHash = "deadbeef"
		}
	}
	cipher, _ := dr.OpenCipher([]byte(testPass), kek)
	encBlob, _ := os.ReadFile(filepath.Join(dir, m.Keys[0].File))
	keyBytes, _ := cipher.Open(encBlob)
	rest := openRestored(t, filepath.Join(dir, m.Store.File), decodeKeyFile(t, keyBytes))

	rep, err := dr.RestoreVerify(ctx, rest.st, m, rest.pub(), rest.cpVerifier(t))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !rep.OK {
		t.Fatalf("advisory tip-behind wrongly failed: %v", rep.Problems)
	}
	for _, tv := range rep.Tenants {
		if tv.Tenant == tn.String() && tv.TipNote == "" {
			t.Fatalf("expected an advisory tip note for the RPO window")
		}
	}
}

func anyContains(ss []string, sub string) bool {
	for _, s := range ss {
		if bytes.Contains([]byte(s), []byte(sub)) {
			return true
		}
	}
	return false
}

func manifestTip(m *dr.Manifest, tenant string) int64 {
	for _, t := range m.Tenants {
		if t.Tenant == tenant {
			return t.HeadSeq
		}
	}
	return -1
}

func restoredTip(t *testing.T, e *estate, tenant model.TenantID) int64 {
	t.Helper()
	var seq int64
	if err := e.st.View(context.Background(), tenant, func(sc store.Scope) error {
		head, _, err := sc.Audit().Head(context.Background())
		seq = head.Seq
		return err
	}); err != nil {
		t.Fatalf("head: %v", err)
	}
	return seq
}
