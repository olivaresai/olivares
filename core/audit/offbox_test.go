// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/store"
)

// mockKMS is a stand-in for an off-box AWS/GCP/Azure KMS key: it signs the
// checkpoint preimage with an ECDSA P-256 key whose private half is held OUTSIDE
// the engine (here, just not exposed as an ed25519 on-box key). It mirrors the
// real connectors/kmssign backends without any network: SignCheckpoint signs
// SHA-256(preimage) and returns the ASN.1 DER ECDSA signature, exactly as
// AWS KMS Sign(MessageType=DIGEST, SigningAlgorithm=ECDSA_SHA_256) does.
type mockKMS struct {
	priv  *ecdsa.PrivateKey
	keyID string
}

func newMockKMS(t *testing.T) *mockKMS {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &mockKMS{priv: k, keyID: "arn:aws:kms:eu-west-1:0:key/mock-ledger"}
}

func (m *mockKMS) SignCheckpoint(_ context.Context, preimage []byte) ([]byte, error) {
	d := sha256.Sum256(preimage)
	return ecdsa.SignASN1(rand.Reader, m.priv, d[:])
}
func (m *mockKMS) Algorithm() audit.SigAlg { return audit.AlgECDSAP256SHA256 }
func (m *mockKMS) KeyID() string           { return m.keyID }
func (m *mockKMS) PublicKey(_ context.Context) ([]byte, error) {
	return x509.MarshalPKIXPublicKey(&m.priv.PublicKey) // DER SubjectPublicKeyInfo
}

// TestOffBoxCheckpointVerifiable is the R5 core proof: a checkpoint
// signed by an OFF-BOX key (no private key on the host) is verifiable off-box with
// the pinned public key, the hash chain stays intact, the default on-box Ed25519
// key does NOT satisfy it (so it is genuinely off-box), and per-event signing is
// untouched (still on-box Ed25519).
func TestOffBoxCheckpointVerifiable(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st)
	appendEvents(t, st, tenant, 3)

	onBoxPub, onBoxPriv, _ := ed25519.GenerateKey(nil)
	kms := newMockKMS(t)
	signer, err := audit.NewSigner(onBoxPriv, audit.WithCheckpointKey(kms))
	if err != nil {
		t.Fatal(err)
	}
	if !signer.OffBoxCheckpoints() {
		t.Fatal("expected off-box checkpoints")
	}

	ev, ok, err := signer.Checkpoint(ctx, st, tenant)
	if err != nil || !ok {
		t.Fatalf("checkpoint = (%v,%v)", ok, err)
	}
	if len(ev.Sig) == 0 {
		t.Fatal("checkpoint has no signature")
	}

	// The hash chain itself is intact (signing a checkpoint never perturbs the hash).
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		rep, e := sc.Audit().Verify(ctx, 1)
		if e != nil {
			return e
		}
		if !rep.OK {
			t.Fatalf("chain broken: %+v", rep)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Off-box verification with the pinned DER SubjectPublicKeyInfo succeeds, with no
	// cloud dependency in the verify path.
	der, _ := kms.PublicKey(ctx)
	offBox := audit.NewCheckpointVerifier()
	if e := offBox.AddPublicKey(audit.AlgECDSAP256SHA256, der); e != nil {
		t.Fatal(e)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		cr, e := audit.VerifyCheckpointsWith(ctx, sc.Audit(), offBox)
		if e != nil {
			return e
		}
		if !cr.OK || cr.Checkpoints != 1 || cr.LatestAttestedSeq != ev.Seq-1 {
			t.Fatalf("off-box verify = %+v", cr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// The on-box Ed25519 key must NOT satisfy the off-box ECDSA checkpoint — proving
	// the checkpoint was genuinely signed off-box (host-compromise control).
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		cr, e := audit.VerifyCheckpoints(ctx, sc.Audit(), onBoxPub)
		if e != nil {
			return e
		}
		if cr.OK || cr.Reason != "checkpoint-sig-invalid" {
			t.Fatalf("on-box key wrongly accepted off-box checkpoint: %+v", cr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// The engine's own combined verifier (on-box Ed25519 + off-box KMS) accepts it.
	v, err := signer.CheckpointVerifier(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		cr, e := audit.VerifyCheckpointsWith(ctx, sc.Audit(), v)
		if e != nil {
			return e
		}
		if !cr.OK {
			t.Fatalf("combined verify = %+v", cr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// (Per-event signing is deliberately untouched by the off-box path — it stays the
	// on-box Ed25519 store.AuditEventSigner; that guarantee is covered by eventsig_test.)
}

// TestCheckpointVerifierMixedChain proves a chain that switched signing key mid-life
// (on-box Ed25519 checkpoints, then off-box ECDSA checkpoints) verifies end-to-end
// with a verifier holding both candidate keys.
func TestCheckpointVerifierMixedChain(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st)
	appendEvents(t, st, tenant, 2)

	pub, priv, _ := ed25519.GenerateKey(nil)
	onBox, _ := audit.NewSigner(priv)
	if _, _, err := onBox.Checkpoint(ctx, st, tenant); err != nil { // Ed25519 checkpoint
		t.Fatal(err)
	}

	appendEvents(t, st, tenant, 2)
	kms := newMockKMS(t)
	offBox, _ := audit.NewSigner(priv, audit.WithCheckpointKey(kms))
	if _, _, err := offBox.Checkpoint(ctx, st, tenant); err != nil { // ECDSA checkpoint
		t.Fatal(err)
	}

	der, _ := kms.PublicKey(ctx)
	v := audit.NewCheckpointVerifier().AddEd25519(pub)
	if e := v.AddPublicKey(audit.AlgECDSAP256SHA256, der); e != nil {
		t.Fatal(e)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		cr, e := audit.VerifyCheckpointsWith(ctx, sc.Audit(), v)
		if e != nil {
			return e
		}
		if !cr.OK || cr.Checkpoints != 2 {
			t.Fatalf("mixed-chain verify = %+v", cr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestCheckpointVerifierRejectsBadKeyType ensures an algorithm/key-type mismatch is
// a loud error, not a silent never-verify.
func TestCheckpointVerifierRejectsBadKeyType(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	der, _ := x509.MarshalPKIXPublicKey(pub)
	v := audit.NewCheckpointVerifier()
	if err := v.AddPublicKey(audit.AlgECDSAP256SHA256, der); err == nil {
		t.Fatal("expected error adding an Ed25519 key under an ECDSA alg")
	}
}

// TestCheckpointVerifierPinsCurve ensures a curve mismatch (a P-384 key pinned under
// the P-256 algorithm) fails LOUDLY rather than silently verifying with a mismatched
// hash — ECDSA would otherwise accept a short SHA-256 digest against a P-384 key.
func TestCheckpointVerifierPinsCurve(t *testing.T) {
	k384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&k384.PublicKey)
	if err := audit.NewCheckpointVerifier().AddPublicKey(audit.AlgECDSAP256SHA256, der); err == nil {
		t.Fatal("expected error pinning a P-384 key under ecdsa-p256-sha256")
	}
	// The matching alg accepts it.
	if err := audit.NewCheckpointVerifier().AddPublicKey(audit.AlgECDSAP384SHA384, der); err != nil {
		t.Fatalf("P-384 key under ecdsa-p384-sha384 should be accepted: %v", err)
	}
}
