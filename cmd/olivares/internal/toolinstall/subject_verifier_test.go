// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import "context"

// HashMatchingVerifier is the test seam: it checks the extracted subject hash
// against the plan claim and that a proof blob was retained. It does not speak
// Rekor or Cosign and is not wired in production.
type HashMatchingVerifier struct{}

func (HashMatchingVerifier) VerifySubject(_ context.Context, subject SubjectExpectation, payloadSHA256 string, bundle []byte) error {
	if len(bundle) == 0 {
		return refuse(KindSignatureInvalid, "proof bundle for %s is empty", subject.Path)
	}
	if payloadSHA256 != subject.ExpectedSHA256 {
		return refuse(KindDigestMismatch, "subject %s sha256 %s does not match the plan claim %s", subject.Path, payloadSHA256, subject.ExpectedSHA256)
	}
	return nil
}

func (HashMatchingVerifier) Describe() string { return "hash-matching-test-double" }
