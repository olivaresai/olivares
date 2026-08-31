// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"github.com/olivaresai/olivares/modules/security"
	"github.com/olivaresai/olivares/modules/sessions"
)

// securityWorkspaceClassifier is the composition-root adapter behind
// sessions.Classifier: it runs the security module's deterministic PII/secret
// catalog (security.ClassifySensitivity) over the content a governed file READ returns,
// so the workspace plane LABELS sensitive reads (and, in a deny-mode workspace, can
// refuse them) WITHOUT modules/sessions ever importing security or knowledge. It is the
// same proven pattern as knowledgeclassifier.go — the only place that speaks both
// modules' types — keeping the modules decoupled and the detectors single-owner in
// security. Deterministic, zero-egress, reproducible (the catalog version is fixed).
type securityWorkspaceClassifier struct{}

var _ sessions.Classifier = securityWorkspaceClassifier{}

func (securityWorkspaceClassifier) Classify(text string) ([]sessions.SensitivityHit, error) {
	hits := security.ClassifySensitivity(text)
	out := make([]sessions.SensitivityHit, len(hits))
	for i, h := range hits {
		out[i] = sessions.SensitivityHit{Class: h.Class, Rule: h.Rule, Count: h.Count, Severity: string(h.Severity)}
	}
	return out, nil
}
