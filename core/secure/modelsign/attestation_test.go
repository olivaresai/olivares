// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package modelsign

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
)

// attestationStatement builds an in-toto statement with an arbitrary predicate
// type over one subject digest (the image-attestation shape).
func attestationStatement(t *testing.T, predicateType, subjectName, subjectDigest string) []byte {
	t.Helper()
	st := statement{
		Type:          statementTypeInToto,
		Subject:       []subject{{Name: subjectName, Digest: map[string]string{"sha256": subjectDigest}}},
		PredicateType: predicateType,
		Predicate:     json.RawMessage(`{"buildType":"test"}`),
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const imgDigest = "b8938122495f7857c4cb81b77662f4737367665350700856d61724ce61109fac"

func signedAttestation(t *testing.T, predicateType string) ([]byte, TrustPolicy) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload := attestationStatement(t, predicateType, "mcp/brave-search", imgDigest)
	bundleJSON, pubPEM := bareKeyBundle(t, payload, signPAE(t, priv, payload), pub)
	return bundleJSON, TrustPolicy{Keys: []string{pubPEM}}
}

func TestVerifyAttestationSLSAProvenance(t *testing.T) {
	bundleJSON, policy := signedAttestation(t, PredicateTypeSLSAProvenanceV1)
	v, err := VerifyAttestation(bundleJSON, policy, []string{PredicateTypeSLSAProvenanceV1}, "sha256:"+imgDigest)
	if err != nil {
		t.Fatalf("VerifyAttestation: %v", err)
	}
	if !v.Verified || !v.ArtifactVerified {
		t.Fatalf("a valid provenance over the expected digest must verify, got %+v", v)
	}
	if v.Method != MethodBareKey || v.PredicateType != PredicateTypeSLSAProvenanceV1 || v.SubjectDigest != imgDigest {
		t.Errorf("verdict fields wrong: %+v", v)
	}
}

func TestVerifyAttestationPredicateAllowList(t *testing.T) {
	// An SBOM attestation must not satisfy a provenance requirement.
	bundleJSON, policy := signedAttestation(t, PredicateTypeSPDX)
	v, err := VerifyAttestation(bundleJSON, policy, []string{PredicateTypeSLSAProvenanceV1}, "")
	if err != nil || v.Verified {
		t.Fatalf("a predicate outside the allow-list must be refused, got %+v err=%v", v, err)
	}
	if !strings.Contains(v.Reason, "not in the allowed set") {
		t.Errorf("reason should explain the predicate refusal: %q", v.Reason)
	}
	// An EMPTY allow-list admits nothing (deny-closed).
	v, err = VerifyAttestation(bundleJSON, policy, nil, "")
	if err != nil || v.Verified {
		t.Fatalf("an empty allow-list must admit nothing, got %+v err=%v", v, err)
	}
}

func TestVerifyAttestationSubjectBinding(t *testing.T) {
	bundleJSON, policy := signedAttestation(t, PredicateTypeSPDX)
	// A VALID signature over the WRONG artifact is not a verification.
	wrong := strings.Repeat("0", 64)
	v, err := VerifyAttestation(bundleJSON, policy, []string{PredicateTypeSPDX}, wrong)
	if err != nil || v.Verified || v.ArtifactVerified {
		t.Fatalf("a subject mismatch must refuse, got %+v err=%v", v, err)
	}
	if !strings.Contains(v.Reason, "do not cover the expected artifact digest") {
		t.Errorf("reason should explain the subject mismatch: %q", v.Reason)
	}
	// Without an expected digest: verified, but artifact binding honestly unclaimed.
	v, err = VerifyAttestation(bundleJSON, policy, []string{PredicateTypeSPDX}, "")
	if err != nil || !v.Verified || v.ArtifactVerified {
		t.Fatalf("without a digest the signature verifies but the artifact is unclaimed, got %+v err=%v", v, err)
	}
	if !strings.Contains(v.ArtifactCoverage, "not bound") {
		t.Errorf("coverage must state the unbound subject: %q", v.ArtifactCoverage)
	}
	// A SUPPLIED-but-malformed digest (truncated paste) must REFUSE — silently
	// degrading to "no pin" would let the operator believe the artifact was bound.
	v, err = VerifyAttestation(bundleJSON, policy, []string{PredicateTypeSPDX}, "sha256:b89381")
	if err != nil || v.Verified || v.ArtifactVerified {
		t.Fatalf("a malformed expected digest must refuse deny-closed, got %+v err=%v", v, err)
	}
	if !strings.Contains(v.Reason, "not a sha256 hex digest") {
		t.Errorf("reason should explain the unusable pin: %q", v.Reason)
	}
}

func TestVerifyAttestationMultiSubject(t *testing.T) {
	// A multi-artifact provenance (the slsa-github-generator shape): the binding
	// may match subject[1]; the recorded verdict must cite the MATCHED subject,
	// never an arbitrary subject[0].
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	other := strings.Repeat("a", 64)
	st := statement{
		Type: statementTypeInToto,
		Subject: []subject{
			{Name: "artifact-zero", Digest: map[string]string{"sha256": other}},
			{Name: "mcp/brave-search", Digest: map[string]string{"sha256": imgDigest}},
		},
		PredicateType: PredicateTypeSLSAProvenanceV1,
		Predicate:     json.RawMessage(`{}`),
	}
	payload, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	bundleJSON, pubPEM := bareKeyBundle(t, payload, signPAE(t, priv, payload), pub)
	policy := TrustPolicy{Keys: []string{pubPEM}}

	v, err := VerifyAttestation(bundleJSON, policy, []string{PredicateTypeSLSAProvenanceV1}, imgDigest)
	if err != nil || !v.Verified || !v.ArtifactVerified {
		t.Fatalf("subject[1] match must verify, got %+v err=%v", v, err)
	}
	if v.SubjectName != "mcp/brave-search" || v.SubjectDigest != imgDigest {
		t.Errorf("the verdict must cite the MATCHED subject, got %q/%q", v.SubjectName, v.SubjectDigest)
	}
	// A subject carrying only a non-sha256 digest can never satisfy the pin.
	st.Subject = []subject{{Name: "x", Digest: map[string]string{"sha512": strings.Repeat("b", 128)}}}
	payload, _ = json.Marshal(st)
	bundleJSON, pubPEM = bareKeyBundle(t, payload, signPAE(t, priv, payload), pub)
	v, err = VerifyAttestation(bundleJSON, TrustPolicy{Keys: []string{pubPEM}}, []string{PredicateTypeSLSAProvenanceV1}, imgDigest)
	if err != nil || v.Verified {
		t.Errorf("a sha512-only subject must never satisfy a sha256 pin, got %+v err=%v", v, err)
	}
}

func TestVerifyAttestationDenyClosedAnchoring(t *testing.T) {
	bundleJSON, _ := signedAttestation(t, PredicateTypeSPDX)
	// An empty trust policy admits nothing — identical to the OMS path.
	v, err := VerifyAttestation(bundleJSON, TrustPolicy{}, []string{PredicateTypeSPDX}, "")
	if err != nil || v.Verified {
		t.Fatalf("an empty policy must admit nothing, got %+v err=%v", v, err)
	}
	// A different trusted key refuses the signature.
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, otherPEM := bareKeyBundle(t, []byte("x"), []byte("y"), otherPub)
	v, err = VerifyAttestation(bundleJSON, TrustPolicy{Keys: []string{otherPEM}}, []string{PredicateTypeSPDX}, "")
	if err != nil || v.Verified {
		t.Fatalf("an untrusted signer must be refused, got %+v err=%v", v, err)
	}
}

func TestNormalizeSHA256(t *testing.T) {
	cases := []struct{ in, want string }{
		{"sha256:" + imgDigest, imgDigest},
		{strings.ToUpper(imgDigest), imgDigest},
		{imgDigest, imgDigest},
		{"sha256:short", ""},
		{strings.Repeat("g", 64), ""}, // non-hex must never compare equal
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizeSHA256(c.in); got != c.want {
			t.Errorf("normalizeSHA256(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
