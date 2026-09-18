// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import "context"

// SubjectProofVerifier checks one publisher-signed subject. Production Codex
// origin-install refuses when this is unavailable rather than recording the
// release as publisher-signed. A hash match against the pointer document is
// not a Sigstore verification.
type SubjectProofVerifier interface {
	VerifySubject(ctx context.Context, subject SubjectExpectation, payloadSHA256 string, bundle []byte) error
	Describe() string
}

// UnavailableSubjectVerifier is the production Codex default until a real
// offline Cosign verifier is wired. It never succeeds.
type UnavailableSubjectVerifier struct{ Err error }

func (u UnavailableSubjectVerifier) VerifySubject(context.Context, SubjectExpectation, string, []byte) error {
	if u.Err != nil {
		return refuse(KindVerificationUnavailable, "no subject proof verifier is configured: %v", u.Err)
	}
	return refuse(KindVerificationUnavailable, "no subject proof verifier is configured; Codex origin install is refused rather than recorded as publisher-signed")
}

func (u UnavailableSubjectVerifier) Describe() string {
	if u.Err != nil {
		return "unavailable: " + u.Err.Error()
	}
	return "unavailable"
}
