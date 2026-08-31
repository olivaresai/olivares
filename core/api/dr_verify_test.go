// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// buildTestBundle seeds a file-backed SQLite ledger, snapshots it, seals the audit
// key and writes a real .drbundle — the input the console pre-verify receives.
func buildTestBundle(t *testing.T) (bundlePath string, cipher *dr.KeyCipher) {
	return buildTestBundleWithPassphrase(t, "correct horse battery staple")
}

func buildTestBundleWithPassphrase(t *testing.T, passphrase string) (bundlePath string, cipher *dr.KeyCipher) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "olivares.db")

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	st, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dbPath, Debug: true, SignEvent: signer.SignEvent}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	var tid model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		o, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		tid = o.TenantID
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Mutate(ctx, tid, func(sc store.Scope) error {
		for i := 0; i < 8; i++ {
			if _, e := sc.Audit().Append(ctx, model.AuditDraft{Actor: "u", ActorKind: "user", Action: "agent.create", TargetKind: "core.agent"}); e != nil {
				return e
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := signer.CheckpointAll(ctx, st); err != nil {
		t.Fatal(err)
	}

	// Snapshot + manifest (TipExact via a boot of the snapshot copy is what the CLI
	// does; here we build the manifest from the same live store, which for a quiesced
	// test ledger is identical — good enough to exercise the scratch verify).
	snap := filepath.Join(dir, "snap.db")
	if err := dr.SnapshotSQLite(ctx, dbPath, snap); err != nil {
		t.Fatal(err)
	}
	sum, size, err := dr.FileSHA256(snap)
	if err != nil {
		t.Fatal(err)
	}
	cpv, err := signer.CheckpointVerifier(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m, err := dr.BuildManifest(ctx, st, signer.PublicKey(), cpv, dr.BuildOptions{
		EngineKind: "sqlite", Version: "test",
		Store:    dr.StoreSnapshot{Method: dr.MethodVacuumInto, File: "store/olivares.db", SizeBytes: size, SHA256: sum},
		Keys:     []dr.KeyRef{{File: "keys/audit-signing.key.enc", Name: "audit-signing.key", Role: dr.RoleAudit, PubSHA256: dr.PubFingerprint(signer.PublicKey())}},
		TipMatch: dr.TipExact, Now: time.Now(), Notes: "test",
	})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	_ = st.Close()

	cipher, err = dr.NewPassphraseCipher([]byte(passphrase))
	if err != nil {
		t.Fatal(err)
	}
	keyFileBytes := []byte(base64.StdEncoding.EncodeToString(priv) + "\n")
	sealed, err := cipher.Seal(keyFileBytes)
	if err != nil {
		t.Fatal(err)
	}
	bundlePath = filepath.Join(dir, "test.drbundle")
	bf, err := os.Create(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := dr.WriteBundle(bf, dr.BundleInput{
		Manifest: m, KEK: cipher.Params(), SnapshotPath: snap,
		SealedKeys: map[string][]byte{"keys/audit-signing.key.enc": sealed},
	}); err != nil {
		t.Fatal(err)
	}
	_ = bf.Close()
	return bundlePath, cipher
}

// TestVerifyBundleScratchLegacyShortPassphrase proves that the creation floor
// does not lock out an existing bundle: the real bundle path still derives its
// KEK, decrypts its signing key and passes ledger-continuity verification.
func TestVerifyBundleScratchLegacyShortPassphrase(t *testing.T) {
	ctx := context.Background()
	bundlePath, _ := buildTestBundleWithPassphrase(t, "short")

	tmp := t.TempDir()
	f, err := os.Open(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	m, kek, err := dr.ExtractBundle(f, tmp)
	_ = f.Close()
	if err != nil {
		t.Fatalf("extract legacy bundle: %v", err)
	}
	cipher, err := dr.OpenCipher([]byte("short"), kek)
	if err != nil {
		t.Fatalf("open legacy bundle cipher: %v", err)
	}
	rep, err := verifyBundleScratch(ctx, tmp, m, cipher)
	if err != nil {
		t.Fatalf("verify legacy bundle: %v", err)
	}
	if !rep.OK {
		t.Fatalf("legacy bundle should verify OK, problems: %v", rep.Problems)
	}
}

// TestVerifyBundleScratchOK proves the console pre-verify accepts a good bundle: it
// restores to a continuity-safe ledger in a scratch dir, without touching any live
// store.
func TestVerifyBundleScratchOK(t *testing.T) {
	ctx := context.Background()
	bundlePath, cipher := buildTestBundle(t)

	tmp := t.TempDir()
	f, _ := os.Open(bundlePath)
	m, _, err := dr.ExtractBundle(f, tmp)
	_ = f.Close()
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	rep, err := verifyBundleScratch(ctx, tmp, m, cipher)
	if err != nil {
		t.Fatalf("verifyBundleScratch: %v", err)
	}
	if !rep.OK {
		t.Fatalf("good bundle should verify OK, problems: %v", rep.Problems)
	}
}

// TestVerifyBundleScratchWrongPassphrase proves a wrong KEK is refused (the audit key
// cannot be decrypted), so a restore under the wrong passphrase never promotes.
func TestVerifyBundleScratchWrongPassphrase(t *testing.T) {
	ctx := context.Background()
	bundlePath, _ := buildTestBundle(t)

	tmp := t.TempDir()
	f, _ := os.Open(bundlePath)
	m, kek, err := dr.ExtractBundle(f, tmp)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := dr.OpenCipher([]byte("not the passphrase"), kek)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBundleScratch(ctx, tmp, m, wrong); err == nil {
		t.Fatal("a wrong passphrase must fail the scratch verify")
	}
}

// TestDualControlDistinctApprover pins the two-humans rule: the initiator cannot
// self-approve; a distinct admin can; an unknown request is rejected.
//
// The parties are identified by their stable USER id. The actor string is
// carried alongside it for the trail, and deliberately differs from the user id
// here: an approver whose actor string differs from the initiator's is still the
// same person when the user id matches, which is exactly the bypass this pins.
func TestDualControlDistinctApprover(t *testing.T) {
	ds := newDRService(DRConfig{DataDir: t.TempDir()})
	alice := auth.PersonRef{User: "alice", Actor: "user:alice"}
	pr := ds.registerPending("upload-123.drbundle", alice, time.Now().UTC().Format(time.RFC3339))

	// Self-approval refused — including through a DIFFERENT credential of the same
	// person ("token:alice-pat" is not "user:alice", but alice is alice).
	alicePAT := auth.PersonRef{User: "alice", Actor: "token:alice-pat"}
	if _, err := ds.approvePending(pr.RequestID, "upload-123.drbundle", alicePAT); err != errSelfApprove {
		t.Fatalf("self-approval through a second credential should be refused, got %v", err)
	}
	ds.pmu.Lock()
	_, still := ds.pending[pr.RequestID]
	ds.pmu.Unlock()
	if !still {
		t.Fatal("a refused self-approval must LEAVE the pending request for another approver")
	}

	// Wrong upload id → not found.
	bob := auth.PersonRef{User: "bob", Actor: "user:bob"}
	if _, err := ds.approvePending(pr.RequestID, "other.drbundle", bob); err != errNoPendingRestore {
		t.Fatalf("mismatched upload should be errNoPendingRestore, got %v", err)
	}

	// Distinct approver succeeds and consumes the request.
	got, err := ds.approvePending(pr.RequestID, "upload-123.drbundle", bob)
	if err != nil {
		t.Fatalf("distinct approver should succeed: %v", err)
	}
	if got.Initiator != "user:alice" || got.InitiatorUser != "alice" {
		t.Fatalf("approved request should carry both initiator identities, got actor %q user %q", got.Initiator, got.InitiatorUser)
	}
	carol := auth.PersonRef{User: "carol", Actor: "user:carol"}
	if _, err := ds.approvePending(pr.RequestID, "upload-123.drbundle", carol); err != errNoPendingRestore {
		t.Fatal("an approved request must not be approvable twice")
	}
}

// TestDualControlRefusesPartyWithoutAStableUser pins the deny-closed half: a
// credential with no PERSON behind it (model.APIToken.UserID zero — "a standalone
// system token") can be neither of the two humans. Without this, the zero identity
// compares unequal to every real user and waves the approval through.
func TestDualControlRefusesPartyWithoutAStableUser(t *testing.T) {
	ds := newDRService(DRConfig{DataDir: t.TempDir()})
	alice := auth.PersonRef{User: "alice", Actor: "user:alice"}
	pr := ds.registerPending("u.drbundle", alice, time.Now().UTC().Format(time.RFC3339))

	// An approver with no user is refused — not compared.
	sysTok := auth.PersonRef{Actor: "token:sys"}
	if _, err := ds.approvePending(pr.RequestID, "u.drbundle", sysTok); err != errNoStableIdentity {
		t.Fatalf("a person-less approver must be refused, got %v", err)
	}
	ds.pmu.Lock()
	_, still := ds.pending[pr.RequestID]
	ds.pmu.Unlock()
	if !still {
		t.Fatal("a refused approval must LEAVE the pending request for a real second person")
	}

	// A request registered without a person cannot be approved by anyone: one
	// nameable human plus one anonymous credential is not two humans.
	anon := ds.registerPending("v.drbundle", auth.PersonRef{Actor: "token:sys"}, time.Now().UTC().Format(time.RFC3339))
	bob := auth.PersonRef{User: "bob", Actor: "user:bob"}
	if _, err := ds.approvePending(anon.RequestID, "v.drbundle", bob); err != errNoStableIdentity {
		t.Fatalf("a person-less initiator must not be approvable, got %v", err)
	}
}
