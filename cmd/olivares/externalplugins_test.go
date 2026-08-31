// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/secure/modelsign"
)

// These tests pin the S142 admission gate with REAL signed bundles, mirroring
// the bundle builders in core/secure/modelsign/attestation_test.go: an Ed25519
// bare-key DSSE bundle over an in-toto Statement v1 with a SLSA predicate and a
// subject digest — the "sign with your own key, trust your own pubkey" loop a
// third-party connector developer actually uses. Hermetic: temp files, no
// network, no engine boot (admitExternalPlugin is pure).

// extStatement builds an in-toto Statement v1 JSON with one subject digest.
func extStatement(t *testing.T, predicateType, subjectDigest string) []byte {
	t.Helper()
	st := map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"subject":       []map[string]any{{"name": "acme-source", "digest": map[string]string{"sha256": subjectDigest}}},
		"predicateType": predicateType,
		"predicate":     map[string]string{"buildType": "test"},
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// extSignPAE signs the DSSE pre-image ("DSSEv1 <len> <type> <len> <payload>")
// of payload under an Ed25519 key — the bare-key signature shape.
func extSignPAE(priv ed25519.PrivateKey, payload []byte) []byte {
	const payloadType = "application/vnd.in-toto+json"
	pae := fmt.Sprintf("DSSEv1 %d %s %d ", len(payloadType), payloadType, len(payload))
	return ed25519.Sign(priv, append([]byte(pae), payload...))
}

// extBundle assembles a detached Sigstore bundle JSON (bare-key verification
// material) and returns it together with the PEM public key for the trust list.
func extBundle(t *testing.T, payload, sig []byte, pub ed25519.PublicKey) ([]byte, string) {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	b := map[string]any{
		"mediaType":            "application/vnd.dev.sigstore.bundle.v0.3+json",
		"verificationMaterial": map[string]any{"publicKey": map[string]string{"hint": "test-key"}},
		"dsseEnvelope": map[string]any{
			"payload":     base64.StdEncoding.EncodeToString(payload),
			"payloadType": "application/vnd.in-toto+json",
			"signatures":  []map[string]string{{"sig": base64.StdEncoding.EncodeToString(sig)}},
		},
	}
	bj, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return bj, pubPEM
}

// extFixture is one fully-signed external plugin on disk: a binary, its digest,
// a bundle attesting that digest under predicateType, and the matching trust.
type extFixture struct {
	spec  externalPluginSpec
	trust connectorTrustSpec
	bin   string // binary path, so a test can edit it after signing
}

// newExtFixture writes a small "binary", signs an attestation over its digest
// with a fresh Ed25519 key and returns the admitted-by-construction inputs.
func newExtFixture(t *testing.T, predicateType string) extFixture {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "acme-source")
	content := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(bin, content, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload := extStatement(t, predicateType, digest)
	bundleJSON, pubPEM := extBundle(t, payload, extSignPAE(priv, payload), pub)
	bundlePath := filepath.Join(dir, "acme-source.bundle.json")
	if err := os.WriteFile(bundlePath, bundleJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	return extFixture{
		spec:  externalPluginSpec{Path: bin, SHA256: digest, Bundle: bundlePath},
		trust: connectorTrustSpec{TrustedKeys: []string{pubPEM}},
		bin:   bin,
	}
}

// requireRefusal asserts a deny-closed outcome whose refusal explains itself.
func requireRefusal(t *testing.T, digest, refusal, wantSubstr string) {
	t.Helper()
	if refusal == "" {
		t.Fatalf("expected a refusal containing %q, got admission (digest %q)", wantSubstr, digest)
	}
	if digest != "" {
		t.Errorf("a refusal must not also return a digest, got %q", digest)
	}
	if !strings.Contains(refusal, wantSubstr) {
		t.Errorf("refusal %q does not explain itself (want substring %q)", refusal, wantSubstr)
	}
}

func TestAdmitExternalPluginSignedAndPinned(t *testing.T) {
	// (a) Signed by a trusted key + digest matches → admitted, normalized digest.
	fx := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)
	digest, refusal := admitExternalPlugin(fx.spec, &fx.trust)
	if refusal != "" {
		t.Fatalf("a trusted, pinned, signed plugin must be admitted, got refusal %q", refusal)
	}
	if digest != fx.spec.SHA256 {
		t.Errorf("admitted digest = %q, want the pinned %q", digest, fx.spec.SHA256)
	}
}

func TestAdmitExternalPluginDenyClosedTrust(t *testing.T) {
	fx := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)

	// (b) No trust policy at all → refused before any file access.
	digest, refusal := admitExternalPlugin(fx.spec, nil)
	requireRefusal(t, digest, refusal, "configure connector_trust with at least one trust anchor")

	// (c) A trust spec with NO anchors (predicates alone anchor nothing).
	anchorless := connectorTrustSpec{AllowedPredicates: []string{modelsign.PredicateTypeSLSAProvenanceV1}}
	digest, refusal = admitExternalPlugin(fx.spec, &anchorless)
	requireRefusal(t, digest, refusal, "configure connector_trust with at least one trust anchor")
}

func TestAdmitExternalPluginDigestPin(t *testing.T) {
	fx := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)

	// (d) Missing / malformed sha256: a supplied-but-unusable pin refuses.
	for _, bad := range []string{"", "deadbeef", strings.Repeat("z", 64), "sha256:" + strings.Repeat("a", 63)} {
		spec := fx.spec
		spec.SHA256 = bad
		digest, refusal := admitExternalPlugin(spec, &fx.trust)
		requireRefusal(t, digest, refusal, "sha256 pin is missing or not a 64-char hex")
	}

	// Empty path refuses (after the pin checks — order is part of the contract).
	spec := fx.spec
	spec.Path = ""
	digest, refusal := admitExternalPlugin(spec, &fx.trust)
	requireRefusal(t, digest, refusal, "plugin path is empty")

	// An unreadable binary refuses, citing the operator-supplied path.
	spec = fx.spec
	spec.Path = filepath.Join(t.TempDir(), "missing")
	digest, refusal = admitExternalPlugin(spec, &fx.trust)
	requireRefusal(t, digest, refusal, "cannot read plugin binary")

	// (e) Binary edited AFTER signing → digest mismatch stating both digests.
	if err := os.WriteFile(fx.bin, []byte("#!/bin/sh\nexit 1\n# tampered\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	digest, refusal = admitExternalPlugin(fx.spec, &fx.trust)
	requireRefusal(t, digest, refusal, "digest mismatch")
	if !strings.Contains(refusal, "expected sha256 "+fx.spec.SHA256) {
		t.Errorf("mismatch refusal must state the expected digest, got %q", refusal)
	}
}

func TestAdmitExternalPluginRequiresBundle(t *testing.T) {
	fx := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)

	// (f) No bundle → unsigned external plugins never run.
	spec := fx.spec
	spec.Bundle = ""
	digest, refusal := admitExternalPlugin(spec, &fx.trust)
	requireRefusal(t, digest, refusal, "unsigned external plugins never run")

	// An unreadable bundle refuses, citing the operator-supplied path.
	spec = fx.spec
	spec.Bundle = filepath.Join(t.TempDir(), "missing.json")
	digest, refusal = admitExternalPlugin(spec, &fx.trust)
	requireRefusal(t, digest, refusal, "cannot read attestation bundle")

	// (i) A malformed bundle is refused as unparseable, not crashed on.
	garbled := filepath.Join(t.TempDir(), "garbled.json")
	if err := os.WriteFile(garbled, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec = fx.spec
	spec.Bundle = garbled
	digest, refusal = admitExternalPlugin(spec, &fx.trust)
	requireRefusal(t, digest, refusal, "not a parseable Sigstore attestation")
}

func TestAdmitExternalPluginUntrustedSigner(t *testing.T) {
	// (g) The bundle verifies cryptographically but under a key the operator
	// never trusted: a recorded negative verdict whose reason surfaces.
	fx := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(otherPub)
	if err != nil {
		t.Fatal(err)
	}
	otherPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	trust := connectorTrustSpec{TrustedKeys: []string{otherPEM}}
	digest, refusal := admitExternalPlugin(fx.spec, &trust)
	requireRefusal(t, digest, refusal, "attestation did not verify")
	if !strings.Contains(refusal, "does not verify under any trusted public key") {
		t.Errorf("refusal must carry the verdict reason, got %q", refusal)
	}
}

func TestAdmitExternalPluginPredicateAllowList(t *testing.T) {
	// (h) The trust policy allows only CycloneDX SBOMs; a SLSA provenance bundle
	// is signed and pinned but the WRONG KIND of attestation → refused.
	fx := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)
	fx.trust.AllowedPredicates = []string{modelsign.PredicateTypeCycloneDX}
	digest, refusal := admitExternalPlugin(fx.spec, &fx.trust)
	requireRefusal(t, digest, refusal, "not in the allowed set")

	// The per-source PredicateTypes NARROWS: requiring SPDX refuses a SLSA bundle
	// even though the (default) policy would have allowed SLSA.
	fx2 := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)
	fx2.spec.PredicateTypes = []string{modelsign.PredicateTypeSPDX}
	digest, refusal = admitExternalPlugin(fx2.spec, &fx2.trust)
	requireRefusal(t, digest, refusal, "not in the allowed set")

	// ...and can never WIDEN: requesting SLSA against a CycloneDX-only policy
	// yields an EMPTY intersection, which VerifyAttestation refuses deny-closed
	// (the catalog effectivePredicates posture).
	fx3 := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)
	fx3.trust.AllowedPredicates = []string{modelsign.PredicateTypeCycloneDX}
	fx3.spec.PredicateTypes = []string{modelsign.PredicateTypeSLSAProvenanceV1}
	digest, refusal = admitExternalPlugin(fx3.spec, &fx3.trust)
	requireRefusal(t, digest, refusal, "no allowed predicate types")

	// OMS is NOT in the external-plugin defaults: a model-weights signature must
	// not stand in for binary provenance at an exec gate.
	for _, p := range defaultExternalPluginPredicates() {
		if p == modelsign.PredicateTypeOMSv1 {
			t.Error("defaultExternalPluginPredicates must not include OMS (model-weights-shaped)")
		}
	}

	// OMS is filtered UNCONDITIONALLY: even an operator who lists it in
	// AllowedPredicates cannot make a model-signing bundle satisfy the exec gate.
	if got := effectiveExternalPredicates(connectorTrustSpec{AllowedPredicates: []string{modelsign.PredicateTypeOMSv1, modelsign.PredicateTypeSLSAProvenanceV1}}, nil); len(got) != 1 || got[0] != modelsign.PredicateTypeSLSAProvenanceV1 {
		t.Errorf("effectiveExternalPredicates must strip OMS even when operator-listed, got %v", got)
	}
}

func TestAdmitExternalPluginOneSidedKeylessRefused(t *testing.T) {
	// A keyless pin must set identities AND issuers (cosign-style). A one-sided
	// pin is an operator misconfiguration that can never admit anything, so it is
	// refused with a clear reason rather than failing opaquely per source. A
	// trusted_keys anchor is present so the refusal is the keyless guard, not the
	// no-anchor guard.
	fx := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)
	idOnly := fx.trust
	idOnly.AllowedIdentities = []string{"https://github.com/acme/.*"}
	digest, refusal := admitExternalPlugin(fx.spec, &idOnly)
	requireRefusal(t, digest, refusal, "BOTH allowed_identities and allowed_issuers")

	issuerOnly := fx.trust
	issuerOnly.AllowedIssuers = []string{"https://token.actions.githubusercontent.com"}
	digest, refusal = admitExternalPlugin(fx.spec, &issuerOnly)
	requireRefusal(t, digest, refusal, "BOTH allowed_identities and allowed_issuers")
}

func TestAdmitExternalPluginNormalizesDigestInput(t *testing.T) {
	// (j) A "sha256:"-prefixed, uppercase pin normalizes and admits; the digest
	// handed to the runtime (and into SecureConfig) is the lowercase hex form.
	fx := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)
	want := fx.spec.SHA256
	fx.spec.SHA256 = "sha256:" + strings.ToUpper(want)
	digest, refusal := admitExternalPlugin(fx.spec, &fx.trust)
	if refusal != "" {
		t.Fatalf("a prefixed/uppercase pin must normalize and admit, got refusal %q", refusal)
	}
	if digest != want {
		t.Errorf("normalized digest = %q, want %q", digest, want)
	}
}

func TestKnowledgeContentSourcePluginAdmission(t *testing.T) {
	t.Run("trusted matching admits", func(t *testing.T) {
		fx := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))
		pending := knowledgeContentSources(sourcesConfig{
			ConnectorTrust: &fx.trust,
			Documents: []documentSpec{{
				Name:   "corp-kb",
				Config: map[string]string{"mode": "live"},
				Plugin: &fx.spec,
			}},
		}, log)
		if len(pending) != 1 {
			t.Fatalf("trusted content-source plugin collected %d pending sources, want 1; log=%q", len(pending), buf.String())
		}
		if pending[0].plugin == nil || pending[0].digest != fx.spec.SHA256 {
			t.Fatalf("pending plugin metadata not preserved: %+v", pending[0])
		}
	})

	for _, tc := range []struct {
		name       string
		mutate     func(*testing.T, *extFixture)
		trust      func(*extFixture) *connectorTrustSpec
		wantReason string
	}{
		{
			name: "digest mismatch",
			mutate: func(t *testing.T, fx *extFixture) {
				t.Helper()
				if err := os.WriteFile(fx.bin, []byte("#!/bin/sh\nexit 42\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			trust:      func(fx *extFixture) *connectorTrustSpec { return &fx.trust },
			wantReason: "digest mismatch",
		},
		{
			name: "unsigned",
			mutate: func(_ *testing.T, fx *extFixture) {
				fx.spec.Bundle = ""
			},
			trust:      func(fx *extFixture) *connectorTrustSpec { return &fx.trust },
			wantReason: "unsigned external plugins never run",
		},
		{
			name:       "no anchors",
			trust:      func(*extFixture) *connectorTrustSpec { return nil },
			wantReason: "configure connector_trust",
		},
	} {
		t.Run(tc.name+" refuses", func(t *testing.T) {
			fx := newExtFixture(t, modelsign.PredicateTypeSLSAProvenanceV1)
			if tc.mutate != nil {
				tc.mutate(t, &fx)
			}
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			pending := knowledgeContentSources(sourcesConfig{
				ConnectorTrust: tc.trust(&fx),
				Documents: []documentSpec{{
					Name:   "corp-kb",
					Plugin: &fx.spec,
				}},
			}, log)
			if len(pending) != 0 {
				t.Fatalf("refused content-source plugin collected %d pending sources, want 0", len(pending))
			}
			out := buf.String()
			if !logHasLine(out, "external content-source plugin refused", "source NOT wired", tc.wantReason) {
				t.Fatalf("refusal log did not include %q; log=%q", tc.wantReason, out)
			}
		})
	}
}
