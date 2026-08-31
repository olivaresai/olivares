// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package modelsign

import (
	"fmt"
	"strings"
)

// attestation.go —: generalizes the OMS verifier to ARBITRARY in-toto
// attestations under a caller-pinned predicate allow-list, so the same
// DSSE/PAE/anchoring machinery (and the same TrustPolicy/Verdict shapes the
// admission flow stores) verifies the supply-chain attestations federated MCP
// catalog entries carry: SLSA provenance, SBOM attestations (SPDX/CycloneDX) —
// e.g. a Docker-built MCP Catalog image's signed SBOM, or a GitHub-released
// server's SLSA provenance, both exported as Sigstore bundles (cosign download
// attestation / gh attestation download).
//
// What stays identical to Verify: deny-closed anchoring (empty policy
// admits nothing; keyless identity+issuer pinning with no PKI downgrade), the
// honest Rekor seam (TransparencyLogVerified is never claimed), ErrMalformedBundle
// vs recorded-failure semantics. What differs: the predicate is pinned by the
// CALLER's allow-list instead of the hard OMS pin, and artifact binding is the
// statement SUBJECT digest (an attestation attests the artifact named in its
// subject — for an OCI image, the image digest) instead of the OMS per-file
// manifest.

// Common in-toto predicate types for supply-chain attestations (the cosign/
// in-toto registry values).
const (
	// PredicateTypeSLSAProvenanceV1 is SLSA Build provenance v1.
	PredicateTypeSLSAProvenanceV1 = "https://slsa.dev/provenance/v1"
	// PredicateTypeSLSAProvenanceV02 is the older SLSA provenance many builders
	// (incl. slsa-github-generator outputs) still emit.
	PredicateTypeSLSAProvenanceV02 = "https://slsa.dev/provenance/v0.2"
	// PredicateTypeSPDX is an SPDX SBOM attestation.
	PredicateTypeSPDX = "https://spdx.dev/Document"
	// PredicateTypeCycloneDX is a CycloneDX SBOM attestation.
	PredicateTypeCycloneDX = "https://cyclonedx.org/bom"
)

// VerifyAttestation verifies a DSSE/Sigstore attestation bundle against the trust
// policy and a predicate ALLOW-LIST. allowedPredicateTypes is deny-closed: empty
// admits nothing, and a statement whose predicateType is not listed is refused —
// an SBOM must not satisfy a provenance requirement (or vice versa) by accident.
//
// expectedSubjectDigest, when non-empty, binds the attestation to a concrete
// artifact: a lowercase hex sha256 (with or without the "sha256:" prefix) that
// MUST appear among the statement's subject digests — e.g. the digest the Docker
// MCP Catalog pins for the image. A statement whose subjects do not cover it is
// refused (Verified=false): a valid signature over the WRONG artifact is not a
// verification. When empty, the signature/identity are verified and
// ArtifactVerified stays false with an honest coverage note.
func VerifyAttestation(bundleJSON []byte, policy TrustPolicy, allowedPredicateTypes []string, expectedSubjectDigest string) (Verdict, error) {
	v, st, done, err := verifyAnchoredStatement(bundleJSON, policy)
	if err != nil || done {
		return v, err
	}
	v.PredicateType = st.PredicateType
	if len(allowedPredicateTypes) == 0 {
		v.Reason = "no allowed predicate types supplied: an attestation verification must pin what it accepts (deny-closed)"
		return v, nil
	}
	if !contains(allowedPredicateTypes, st.PredicateType) {
		v.Reason = fmt.Sprintf("predicateType %q is not in the allowed set %v (deny-closed)", st.PredicateType, allowedPredicateTypes)
		return v, nil
	}
	if len(st.Subject) == 0 {
		v.Reason = "statement has no subjects (an attestation that names no artifact attests nothing)"
		return v, nil
	}
	v.SubjectName = st.Subject[0].Name
	v.SubjectDigest = st.Subject[0].Digest["sha256"]

	if expectedSubjectDigest != "" {
		want := normalizeSHA256(expectedSubjectDigest)
		if want == "" {
			// A SUPPLIED-but-unusable pin must refuse, never silently degrade to
			// "no pin": the caller believes the artifact is being bound (deny-closed,
			// the same posture as the empty predicate allow-list above).
			v.Reason = "expected artifact digest is not a sha256 hex digest (a supplied-but-unusable pin is refused, never silently ignored)"
			return v, nil
		}
		matched, ok := subjectsCover(st.Subject, want)
		if !ok {
			v.Reason = "statement subjects do not cover the expected artifact digest (a valid signature over a DIFFERENT artifact is not a verification)"
			return v, nil
		}
		// Record the subject that actually satisfied the binding (a multi-artifact
		// provenance names several subjects; citing subject[0] while subject[N]
		// matched would make the stored verdict contradict its own coverage note).
		v.SubjectName = matched.Name
		v.SubjectDigest = matched.Digest["sha256"]
		v.Verified = true
		v.ArtifactVerified = true
		v.ArtifactCoverage = "statement subject digest matches the expected artifact digest (sha256)"
		return v, nil
	}
	v.Verified = true
	v.ArtifactCoverage = "attestation not bound to a caller-supplied artifact digest; signature, signer identity and predicate type are verified — subject↔artifact binding is not"
	return v, nil
}

// subjectsCover returns the first subject whose sha256 digest equals want
// (already normalized lowercase hex).
func subjectsCover(subjects []subject, want string) (subject, bool) {
	for _, s := range subjects {
		if normalizeSHA256(s.Digest["sha256"]) == want {
			return s, true
		}
	}
	return subject{}, false
}

// normalizeSHA256 lowercases and strips an optional "sha256:" prefix; returns ""
// for anything that is not a 64-char hex string (a malformed digest must never
// accidentally compare equal).
func normalizeSHA256(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) != 64 {
		return ""
	}
	for _, r := range d {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ""
		}
	}
	return d
}
