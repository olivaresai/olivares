// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"github.com/olivaresai/olivares/modules/knowledge"
	"github.com/olivaresai/olivares/modules/security"
)

// securitySensitivityClassifier is the composition-root adapter behind
// knowledge.SensitivityClassifier: it runs the security module's deterministic
// PII/secret catalog (security.ClassifySensitivity) over the text the
// knowledge plane is discovering — the detectors stay single-owner in the
// security module, the labels and the DLP policy that consume them stay with the
// data. Deterministic default-on, zero-egress, reproducible (the catalog version
// is recorded on every scan); a semantic classifier would be a separate, OPT-IN
// adapter behind the same seam. This is the only place that speaks both modules'
// types — neither module imports the other.
type securitySensitivityClassifier struct{}

var _ knowledge.SensitivityClassifier = securitySensitivityClassifier{}

func (securitySensitivityClassifier) Classify(text string) ([]knowledge.SensitivityHit, error) {
	hits := security.ClassifySensitivity(text)
	out := make([]knowledge.SensitivityHit, len(hits))
	for i, h := range hits {
		out[i] = knowledge.SensitivityHit{Class: h.Class, Rule: h.Rule, Count: h.Count, Severity: h.Severity}
	}
	return out, nil
}

func (securitySensitivityClassifier) Version() string {
	return security.SensitivityCatalogVersion
}

// securityRedactor is the B-02 composition-root adapter behind
// knowledge.Redactor. It runs the SAME catalog the classifier reports on
// (security.RedactSensitive) over every text the knowledge plane is about to
// chunk, embed, hash or persist.
//
// The two adapters being one catalog is the whole point. Before this, detection
// and minimization had drifted: the classifier recognized eighteen personal-data
// shapes and the write-path scrub removed one (email), so the product could say
// "this document contains an IBAN" precisely because the IBAN was still there —
// in the chunk store, in the clear, and in whatever the embedder was handed. What
// the product detects is now what it removes.
type securityRedactor struct{}

var _ knowledge.Redactor = securityRedactor{}

func (securityRedactor) Redact(text string) (string, []knowledge.SensitivityHit) {
	clean, hits := security.RedactSensitive(text)
	out := make([]knowledge.SensitivityHit, len(hits))
	for i, h := range hits {
		out[i] = knowledge.SensitivityHit{Class: h.Class, Rule: h.Rule, Count: h.Count, Severity: h.Severity}
	}
	return clean, out
}

func (securityRedactor) Version() string { return security.SensitivityCatalogVersion }
