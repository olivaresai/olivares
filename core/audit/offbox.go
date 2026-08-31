// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SigAlg names the signature scheme a checkpoint was signed under (R5). The on-box default is Ed25519 (NewSigner, unchanged); an OFF-BOX KMS/HSM
// key signs under whatever asymmetric scheme the device exposes. AWS KMS has no
// Ed25519, so ECDSA-P256 is the cross-cloud default for an off-box signer; the
// rest are accepted by the verifier so an operator's existing KMS key (P-384,
// RSA-2048/3072/4096) drops in without a re-key.
//
// The algorithm is recorded (with the key id) in a checkpoint's Meta for
// operability — it is chained, so it cannot be altered without breaking the hash —
// but the VERIFY path never relies on it: an off-box auditor pins the (algorithm,
// public key) candidates explicitly, which is the attacker-resistant model
// (docs/SECURITY-HARDENING.md).
type SigAlg string

// The signature schemes the checkpoint verifier understands. They are the schemes
// a pure-Go (CGO_ENABLED=0) verifier can check with the standard library alone —
// no cloud dependency in the verify path.
const (
	// AlgEd25519 is the on-box default and every pre checkpoint. The
	// signature is over the raw checkpoint preimage (no pre-hash).
	AlgEd25519 SigAlg = "ed25519"
	// AlgECDSAP256SHA256 is the cross-cloud off-box default (AWS KMS ECC_NIST_P256
	// ECDSA_SHA_256, GCP EC_SIGN_P256_SHA256, Azure ES256). The signature is the
	// ASN.1 DER ECDSA signature over SHA-256(preimage).
	AlgECDSAP256SHA256 SigAlg = "ecdsa-p256-sha256"
	// AlgECDSAP384SHA384 is ECDSA over the P-384 curve, SHA-384(preimage).
	AlgECDSAP384SHA384 SigAlg = "ecdsa-p384-sha384"
	// AlgRSAPKCS1SHA256 is RSASSA-PKCS1-v1_5 over SHA-256(preimage).
	AlgRSAPKCS1SHA256 SigAlg = "rsa-pkcs1-sha256"
	// AlgRSAPSSSHA256 is RSASSA-PSS over SHA-256(preimage).
	AlgRSAPSSSHA256 SigAlg = "rsa-pss-sha256"
)

// Meta keys recorded on an off-box-signed checkpoint event. They are non-secret
// operability metadata (a public key id and an algorithm name), chained into the
// event hash, NEVER the signing key itself.
const (
	// MetaSigAlg records the SigAlg an off-box checkpoint was signed under. Absent
	// on the on-box Ed25519 default (and on every pre checkpoint).
	MetaSigAlg = "sig.alg"
	// MetaSigKey records the non-secret off-box key reference (a KMS key ARN /
	// resource name / key id). Present only on off-box-signed checkpoints.
	MetaSigKey = "sig.key"
)

// CheckpointKey is the seam that lets an OFF-BOX (KMS/HSM) key sign the ledger's
// checkpoints (R5). The on-box Ed25519 path (NewSigner) is the
// default and is unchanged; supplying a CheckpointKey via WithCheckpointKey is the
// opt-in that raises tamper-evidence from the DB-only attacker to the
// HOST-COMPROMISED attacker: the private key never lives on the host, so a
// checkpoint — the cross-time anchor an off-box verifier pins — cannot be forged
// without the KMS/HSM (docs/SECURITY-HARDENING.md).
//
// It signs the canonical checkpoint preimage (the SAME bytes the on-box path
// signs) and reports the algorithm + public key so an external auditor can verify
// off-box WITHOUT any cloud dependency in the verify path. The concrete backends
// live in core/audit/kmssign (AGPL-3.0-only, PURE-GO REST via SigV4 / bearer) —
// they are part of the ledger SIGNER (integrity infrastructure, not a reusable
// observation connector), so they reference this interface directly; the
// composition root constructs one from operator config and injects it via
// WithCheckpointKey. Because the backends are pure-Go, wiring an off-box signer
// keeps the engine binary CGO_ENABLED=0. A native PKCS#11 HSM that needs cgo is an
// out-of-process sidecar behind this same interface (over the AutoMTLS channel)
// or a KMS that itself is HSM-backed (CloudHSM / Managed HSM) — it is NEVER compiled
// into the core (same principle as SQLCipher).
//
// Per-event signing is deliberately NOT routed here: it is the synchronous,
// infallible hot path inside the store transaction (store.AuditEventSigner), where
// a per-append network round-trip is wrong. Per-event stays on-box Ed25519
// (defends the DB-only attacker between checkpoints); the off-box key signs the
// checkpoints (defends the host-compromised attacker) — the two compose.
type CheckpointKey interface {
	// SignCheckpoint signs the canonical checkpoint preimage off-box and returns
	// the detached signature. It may perform network I/O and may fail; the
	// checkpoint cadence tolerates that (it logs and retries next tick) because
	// checkpointing is off the hot append path. It MUST sign exactly the bytes
	// given (for a digest-only device, hash with the algorithm's hash and sign the
	// digest) so the off-box verifier reproduces the check from the preimage alone.
	SignCheckpoint(ctx context.Context, preimage []byte) ([]byte, error)
	// Algorithm names the scheme SignCheckpoint produces (recorded in Meta and used
	// to build the engine's self-verifier).
	Algorithm() SigAlg
	// KeyID is a non-secret reference to the off-box key (e.g. a KMS key ARN /
	// resource name), recorded in the checkpoint Meta for operability.
	KeyID() string
	// PublicKey returns the verification key an auditor pins off-box: Ed25519 = the
	// 32-byte key; ECDSA/RSA = DER-encoded SubjectPublicKeyInfo (parseable by
	// crypto/x509.ParsePKIXPublicKey). It may fetch+cache from the device once.
	PublicKey(ctx context.Context) ([]byte, error)
}

// Option configures a Signer. It is the additive seam that keeps NewSigner(priv)
// source-compatible while letting the composition root attach an off-box
// checkpoint key.
type Option func(*Signer)

// WithCheckpointKey makes Checkpoint/CheckpointAll sign checkpoints with an
// off-box key. nil leaves the on-box Ed25519 default in place. The
// per-event signer (SignEvent) is unaffected — it stays the on-box Ed25519 key.
func WithCheckpointKey(ck CheckpointKey) Option {
	return func(s *Signer) {
		if ck != nil {
			s.cp = ck
		}
	}
}

// signCheckpoint produces the checkpoint signature and the operability Meta for a
// canonical preimage: the off-box key when configured, else the on-box Ed25519
// key. The on-box path records no alg (absent == ed25519, preserving byte-identical
// behavior for the default and every existing checkpoint).
func (s *Signer) signCheckpoint(ctx context.Context, preimage []byte) (sig []byte, meta map[string]any, err error) {
	if s.cp == nil {
		return ed25519.Sign(s.priv, preimage), nil, nil
	}
	sig, err = s.cp.SignCheckpoint(ctx, preimage)
	if err != nil {
		return nil, nil, fmt.Errorf("audit: off-box checkpoint sign (%s): %w", s.cp.Algorithm(), err)
	}
	if len(sig) == 0 {
		return nil, nil, fmt.Errorf("audit: off-box checkpoint signer returned an empty signature")
	}
	return sig, map[string]any{MetaSigAlg: string(s.cp.Algorithm()), MetaSigKey: s.cp.KeyID()}, nil
}

// CheckpointVerifier verifies checkpoint signatures off-box. It holds one or more
// candidate public keys (e.g. the engine's on-box Ed25519 key AND an off-box
// KMS/HSM key) and accepts a checkpoint if ANY candidate verifies it — so a chain
// that switched signing key mid-life (a key rotation, or adopting an off-box
// signer) verifies end-to-end without needing the per-checkpoint algorithm from
// the (Walk-stripped) Meta. There is no cloud dependency: a KMS public key is the
// pinned bytes an auditor exported once. The zero value verifies nothing; build it
// with NewCheckpointVerifier.
type CheckpointVerifier struct {
	cands []verifyCandidate
}

type verifyCandidate struct {
	alg    SigAlg
	verify func(preimage, sig []byte) bool
}

// NewCheckpointVerifier returns an empty verifier; add candidate keys with
// AddEd25519 / AddPublicKey.
func NewCheckpointVerifier() *CheckpointVerifier { return &CheckpointVerifier{} }

// AddEd25519 adds an Ed25519 candidate (the on-box default scheme). An undersized
// key is ignored (it can never verify), keeping the builder chainable.
func (v *CheckpointVerifier) AddEd25519(pub ed25519.PublicKey) *CheckpointVerifier {
	if len(pub) == ed25519.PublicKeySize {
		key := append(ed25519.PublicKey(nil), pub...)
		v.cands = append(v.cands, verifyCandidate{
			alg:    AlgEd25519,
			verify: func(preimage, sig []byte) bool { return ed25519.Verify(key, preimage, sig) },
		})
	}
	return v
}

// AddPublicKey adds an off-box candidate from a DER-encoded SubjectPublicKeyInfo
// (what KMS GetPublicKey / Azure Get-Key (as SPKI) / GCP GetPublicKey (PEM body)
// return) and the algorithm it signs under. It returns an error for an
// unparseable key or an algorithm/key-type mismatch, so a misconfigured off-box
// key fails loudly rather than silently never verifying.
func (v *CheckpointVerifier) AddPublicKey(alg SigAlg, derSPKI []byte) error {
	pub, err := x509.ParsePKIXPublicKey(derSPKI)
	if err != nil {
		return fmt.Errorf("audit: parse off-box public key: %w", err)
	}
	c, err := candidateFor(alg, pub)
	if err != nil {
		return err
	}
	v.cands = append(v.cands, c)
	return nil
}

// AddEd25519Raw adds an Ed25519 candidate from raw 32-byte key bytes (the form an
// auditor pins for an off-box Ed25519 signer, e.g. GCP EC_SIGN_ED25519).
func (v *CheckpointVerifier) AddEd25519Raw(raw []byte) error {
	if len(raw) != ed25519.PublicKeySize {
		return fmt.Errorf("audit: ed25519 key is %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
	v.AddEd25519(ed25519.PublicKey(raw))
	return nil
}

// Empty reports whether the verifier has no candidate keys (it would reject every
// checkpoint). The caller decides whether that is a configuration error.
func (v *CheckpointVerifier) Empty() bool { return len(v.cands) == 0 }

// verify reports whether any candidate accepts the signature over the preimage.
func (v *CheckpointVerifier) verify(preimage, sig []byte) bool {
	for _, c := range v.cands {
		if c.verify(preimage, sig) {
			return true
		}
	}
	return false
}

// candidateFor builds a verify function for an (algorithm, parsed public key)
// pair, validating the key type matches the algorithm.
func candidateFor(alg SigAlg, pub crypto.PublicKey) (verifyCandidate, error) {
	switch alg {
	case AlgEd25519:
		k, ok := pub.(ed25519.PublicKey)
		if !ok {
			return verifyCandidate{}, fmt.Errorf("audit: alg %s needs an Ed25519 key, got %T", alg, pub)
		}
		return verifyCandidate{alg, func(p, s []byte) bool { return ed25519.Verify(k, p, s) }}, nil
	case AlgECDSAP256SHA256, AlgECDSAP384SHA384:
		k, ok := pub.(*ecdsa.PublicKey)
		if !ok {
			return verifyCandidate{}, fmt.Errorf("audit: alg %s needs an ECDSA key, got %T", alg, pub)
		}
		// Pin the curve to the algorithm so a misconfigured key (right type, wrong
		// curve — e.g. a P-384 key pinned under ecdsa-p256-sha256) fails LOUDLY rather
		// than silently verifying with a mismatched hash (ECDSA would otherwise accept
		// a short SHA-256 digest against a P-384 key).
		if alg == AlgECDSAP384SHA384 {
			if k.Curve != elliptic.P384() {
				return verifyCandidate{}, fmt.Errorf("audit: alg %s needs a P-384 key, got curve %s", alg, curveName(k.Curve))
			}
			return verifyCandidate{alg, func(p, s []byte) bool {
				d := sha512.Sum384(p)
				return ecdsa.VerifyASN1(k, d[:], s)
			}}, nil
		}
		if k.Curve != elliptic.P256() {
			return verifyCandidate{}, fmt.Errorf("audit: alg %s needs a P-256 key, got curve %s", alg, curveName(k.Curve))
		}
		return verifyCandidate{alg, func(p, s []byte) bool {
			d := sha256.Sum256(p)
			return ecdsa.VerifyASN1(k, d[:], s)
		}}, nil
	case AlgRSAPKCS1SHA256:
		k, ok := pub.(*rsa.PublicKey)
		if !ok {
			return verifyCandidate{}, fmt.Errorf("audit: alg %s needs an RSA key, got %T", alg, pub)
		}
		return verifyCandidate{alg, func(p, s []byte) bool {
			d := sha256.Sum256(p)
			return rsa.VerifyPKCS1v15(k, crypto.SHA256, d[:], s) == nil
		}}, nil
	case AlgRSAPSSSHA256:
		k, ok := pub.(*rsa.PublicKey)
		if !ok {
			return verifyCandidate{}, fmt.Errorf("audit: alg %s needs an RSA key, got %T", alg, pub)
		}
		return verifyCandidate{alg, func(p, s []byte) bool {
			d := sha256.Sum256(p)
			return rsa.VerifyPSS(k, crypto.SHA256, d[:], s, nil) == nil
		}}, nil
	default:
		return verifyCandidate{}, fmt.Errorf("audit: unknown checkpoint signature algorithm %q", alg)
	}
}

// curveName labels an elliptic curve for an error message (nil-safe).
func curveName(c elliptic.Curve) string {
	if c == nil {
		return "<nil>"
	}
	return c.Params().Name
}

// VerifyCheckpointsWith is VerifyCheckpoints generalized to an arbitrary set of
// candidate keys (Ed25519 on-box and/or off-box KMS/HSM), so an off-box auditor
// can verify a chain whose checkpoints were signed off-box — the host-compromise
// control of docs/SECURITY-HARDENING.md. It is identical to VerifyCheckpoints except for which
// keys may satisfy a checkpoint's signature.
func VerifyCheckpointsWith(ctx context.Context, log store.AuditLog, v *CheckpointVerifier) (CheckpointReport, error) {
	rep := CheckpointReport{}
	if v == nil || v.Empty() {
		return CheckpointReport{}, fmt.Errorf("audit: no checkpoint verification key configured")
	}
	seqHash := map[int64][]byte{}
	err := log.Walk(ctx, 1, func(ev model.AuditEvent) error {
		seqHash[ev.Seq] = ev.Hash
		if ev.Action != ActionCheckpoint {
			return nil
		}
		rep.Checkpoints++
		attestedSeq := ev.Seq - 1
		attestedHash := ev.PrevHash
		if len(ev.Sig) == 0 || !v.verify(checkpointPreimage(ev.TenantID.String(), attestedSeq, attestedHash), ev.Sig) {
			rep.fail(ev.Seq, "checkpoint-sig-invalid")
			return nil
		}
		if attestedSeq >= 1 {
			h, ok := seqHash[attestedSeq]
			if !ok || !bytes.Equal(h, attestedHash) {
				rep.fail(ev.Seq, "checkpoint-link-mismatch")
				return nil
			}
		}
		if attestedSeq > rep.LatestAttestedSeq {
			rep.LatestAttestedSeq = attestedSeq
		}
		return nil
	})
	if err != nil {
		return CheckpointReport{}, err
	}
	if rep.Checkpoints == 0 {
		rep.Reason = ReasonNoCheckpoints
	} else if rep.Reason == "" {
		rep.OK = true
	}
	return rep, nil
}
